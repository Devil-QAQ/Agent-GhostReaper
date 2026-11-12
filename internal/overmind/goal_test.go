package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-06 完成标准 ----
// E2E:mock 靶站上种子 → 拆解 → 执行 → 入图 → Goal 完成判定通过;
//      LLM 写的"已满足"被仲裁者拒收。

// seedServiceAssets 种 service 类资产(title=URL)。
func seedServiceAssets(t *testing.T, store *rings.Store, task string, ids []string, urls map[string]string) {
	t.Helper()
	for _, id := range ids {
		if _, err := store.Append(context.Background(), task, rings.Event{
			Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
				Title: urls[id], Status: "active", AssetClass: rings.AssetService},
		}); err != nil {
			t.Fatalf("种资产 %s 失败: %v", id, err)
		}
	}
}

// e2ePredicate E2E 目标谓词:两类资产各需"1 条已确认事实 + 1 条
// port_probe 开放观察"(业务结论渠道 + 机器观察渠道缺一不可)。
func e2ePredicate() map[string]any {
	return map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
			map[string]any{"kind": "tool_observed", "asset_id": "web1", "tool": "port_probe", "result_contains": "state=open", "min": 1},
			map[string]any{"kind": "fact_confirmed", "asset_id": "adm1", "min": 1},
			map[string]any{"kind": "tool_observed", "asset_id": "adm1", "tool": "port_probe", "result_contains": "state=open", "min": 1},
		},
	}
}

// goalStatus 取目标当前状态。
func goalStatus(t *testing.T, store *rings.Store, task, goalID string) string {
	t.Helper()
	g, err := store.Node(context.Background(), task, goalID)
	if err != nil {
		t.Fatalf("查目标 %s 失败: %v", goalID, err)
	}
	return g.Status
}

// verdictEvents 取全部仲裁裁决事件(负载解码)。
func verdictEvents(t *testing.T, store *rings.Store, task string) []rings.ArbiterVerdict {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.ArbiterVerdict
	for _, ev := range evs {
		if ev.Kind != rings.EventArbiterVerdict {
			continue
		}
		var v rings.ArbiterVerdict
		if err := json.Unmarshal(ev.Payload, &v); err != nil {
			t.Fatalf("解码 arbiter.verdict: %v", err)
		}
		if ev.Actor != arbiter.ActorArbiter {
			t.Fatalf("裁决事件 actor 应为 %s,得到 %s", arbiter.ActorArbiter, ev.Actor)
		}
		out = append(out, v)
	}
	return out
}

// goalAchievedBy 查目标状态迁移到 achieved 的事件归属者。
func goalAchievedBy(t *testing.T, store *rings.Store, task, goalID string) string {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码 node.state_changed: %v", err)
		}
		if p.NodeID == goalID && p.Status == GoalStatusAchieved {
			return ev.Actor
		}
	}
	return ""
}

// countToolObservations 数账本内指定工具的机器观察记录(执行器写入的
// swarm.record tool_call,非错误、结果含 pattern)。
func countToolObservations(t *testing.T, store *rings.Store, task, tool, pattern string) int {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 swarm.record: %v", err)
		}
		if r.Type == rings.SwarmRecToolCall && r.ToolName == tool && !r.IsError &&
			strings.Contains(r.ResultHead, pattern) {
			n++
		}
	}
	return n
}

// TestGoalArbiterE2E 正例:mock 靶站(主站 + 管理后台两个真实 HTTP 服务)
// 上种子 → 主宰拆解派发 → 虫群真实探测(port_probe 真连 TCP、bash+curl
// 真发 HTTP)→ 候选入图 → 收割转正 → 仲裁者对冻结谓词求值通过 → 目标
// achieved(actor=arbiter)→ 主宰停派(达成后不再拆解)。
func TestGoalArbiterE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "goal-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	urls := map[string]string{"web1": target.MainURL(), "adm1": target.AdminURL()}

	// 脚本模型:拆解按"未覆盖 service 资产"派意图;虫群按任务包资产
	// 逐轮 port_probe(真实 TCP)→ curl(真实 HTTP)→ 提交候选 → 收口。
	uncoveredRe := regexp.MustCompile(`-\s+([a-z0-9]+) \[service\] (http://\S+) \(未覆盖\)`)
	packAssetRe := regexp.MustCompile(`-\s+([a-z0-9]+) \[service\] (http://\S+)`)
	markerOf := func(assetID string) string {
		if assetID == "adm1" {
			return "mock admin console"
		}
		return "gr-mock-main"
	}
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			var plans []string
			for _, m := range uncoveredRe.FindAllStringSubmatch(rr.LastUser, -1) {
				plans = append(plans, fmt.Sprintf(
					`{"intent":"探测服务 %s 的端口开放状态与 HTTP 指纹并提交候选事实","expected_observation":"端口开放与 HTTP 内容指纹","action_level":"只读探测","covers":["%s"]}`,
					m[2], m[1]))
			}
			return scriptedReply{Text: `{"new_intents":[` + strings.Join(plans, ",") + `]}`, Finish: "stop"}
		}
		am := packAssetRe.FindStringSubmatch(rr.LastUser)
		if am == nil {
			return scriptedReply{Text: "任务包无绑定资产,无事可做。", Finish: "stop"}
		}
		assetID, rawURL := am[1], am[2]
		u, err := url.Parse(rawURL)
		if err != nil || u.Port() == "" {
			return scriptedReply{Text: "资产地址无法解析。", Finish: "stop"}
		}
		switch {
		case !seenToolResult(rr, "port_probe", "state="):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "p" + assetID, Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%s}`, u.Hostname(), u.Port()),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", markerOf(assetID)):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c" + assetID, Name: "bash",
				Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s"}`, rawURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "s" + assetID, Name: "submit_candidate_fact",
				Args: fmt.Sprintf(`{"statement":"%s 服务在线,HTTP 可达,内容指纹已确认","evidence":"port_probe 实测 state=open;curl 返回内容含指纹 %s","asset_id":"%s"}`,
					rawURL, markerOf(assetID), assetID),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图完成:端口与 HTTP 指纹均已实测,候选事实已提交待收割。", Finish: "stop"}
		}
	})
	t.Cleanup(model.Close)
	model.release() // 本测试不观测并发,闸门常开
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := dir
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}
	m, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, _ string, wake <-chan struct{}) { loop.Run(tctx, wake) },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(task)
		m.Close()
	})
	tk, err := m.CreateTask(ctx, task, "Goal 完成判定 E2E", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 种子:目标(带冻结谓词,入口校验通过)+ 两类 service 资产,首次唤醒。
	if err := SeedGoal(ctx, store, task, "g1", "摸清 mock 靶站两类服务的暴露面",
		map[string]any{"predicate": e2ePredicate()}); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1", "adm1"}, urls)
	tk.Wake()

	// ---- Goal 完成判定通过:目标 achieved,裁决来自仲裁者 ----
	waitFor(t, 60*time.Second, "目标 g1 经仲裁者判定达成", func() bool {
		return goalStatus(t, store, task, "g1") == GoalStatusAchieved
	})

	verdicts := verdictEvents(t, store, task)
	if len(verdicts) != 1 {
		t.Fatalf("应恰 1 条达成裁决,得到 %d: %+v", len(verdicts), verdicts)
	}
	v := verdicts[0]
	if !v.Satisfied || v.GoalID != "g1" || v.Op != arbiter.OpAllOf || v.EvaluatedRev <= 0 {
		t.Fatalf("达成裁决不符: %+v", v)
	}
	if len(v.Conditions) != 4 {
		t.Fatalf("裁决应含 4 条条件(两资产 × 两渠道),得到 %d", len(v.Conditions))
	}
	for _, c := range v.Conditions {
		if !c.Satisfied || c.Actual < c.Required {
			t.Fatalf("达成裁决的条件必须逐条满足: %+v", c)
		}
	}
	if actor := goalAchievedBy(t, store, task, "g1"); actor != arbiter.ActorArbiter {
		t.Fatalf("目标 achieved 迁移应归 arbiter,得到 %q", actor)
	}

	// ---- 全链路中间产物真实存在 ----
	if facts := countNodes(t, store, task, rings.NodeFact, "confirmed"); facts != 2 {
		t.Fatalf("应确认 2 条事实(每资产 1 条),实际 %d", facts)
	}
	if got := countToolObservations(t, store, task, "port_probe", "state=open"); got != 2 {
		t.Fatalf("应有 2 条 port_probe 开放观察(机器记录),实际 %d", got)
	}
	// mock 靶站确实被真实探测过(HTTP 命中,而非账本自说自话)。
	if target.Hits("main", "/") < 1 || target.Hits("admin", "/") < 1 {
		t.Fatalf("靶站应被真实 HTTP 探测: main=%d admin=%d",
			target.Hits("main", "/"), target.Hits("admin", "/"))
	}

	// ---- 停派:机器判定达成后不再拆解(后续唤醒也不拆) ----
	// P4-04:拆解轮与单位落地紧邻(派发后/落地后各可有轮),达成时刻
	// 的拆解总数不定;不变式是达成后零新增——判定先于拆解,达成即停。
	atAchieved := len(model.decomposeRequests())
	if atAchieved < 1 {
		t.Fatalf("达成前应至少发生首轮拆解,实际 %d", atAchieved)
	}
	tk.Wake()
	time.Sleep(500 * time.Millisecond)
	if got := len(model.decomposeRequests()); got != atAchieved {
		t.Fatalf("目标达成后主宰不得再拆解: 达成时 %d 次现在 %d 次", atAchieved, got)
	}
	if got := countNodes(t, store, task, rings.NodeStep, ""); got != 2 {
		t.Fatalf("全程应恰 2 条步骤,实际 %d", got)
	}
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// TestArbiterRejectsLLMSatisfaction 负例:LLM 把"已满足"写进它的每个
// 合法文本通道,仲裁者一律拒收——只有机器可核验的产物(执行器写入的
// 工具观察记录)才翻动裁决。
func TestArbiterRejectsLLMSatisfaction(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "goal-llm"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	pred := map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
			map[string]any{"kind": "tool_observed", "asset_id": "web1", "tool": "port_probe", "result_contains": "state=open", "min": 1},
		},
	}

	// 阶段驱动的脚本模型:
	//   1 = 虫群不探测,直接经受控入口提交"已满足"候选事实(全合法通道);
	//   2 = 主宰拆解回复"目标已满足"+空意图(LLM 的达成宣称);
	//   3 = 虫群真实 port_probe(机器观察落地)。
	phase := 1
	packAssetRe := regexp.MustCompile(`-\s+(web1) \[service\] (http://\S+)`)
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			return scriptedReply{
				Text:   "目标已满足:全部完成条件均已达成,无需派发任何意图。{\"new_intents\":[]}",
				Finish: "stop",
			}
		}
		am := packAssetRe.FindStringSubmatch(rr.LastUser)
		if am == nil {
			return scriptedReply{Text: "任务包无绑定资产。", Finish: "stop"}
		}
		switch {
		case phase == 1 && !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "claim", Name: "submit_candidate_fact",
				Args: fmt.Sprintf(`{"statement":"Goal g1 已满足:主站暴露面已全部确认,无需进一步探测","evidence":"模型研判:目标完成条件已满足","asset_id":"web1"}`),
			}}, Finish: "tool_calls"}
		case phase >= 3 && !seenToolResult(rr, "port_probe", "state="):
			u, _ := url.Parse(mainURL)
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "probe", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%s}`, u.Hostname(), u.Port()),
			}}, Finish: "tool_calls"}
		default:
			if phase >= 3 {
				return scriptedReply{Text: "探测完成:port_probe 已实测端口开放。", Finish: "stop"}
			}
			return scriptedReply{Text: "目标已达成,无需继续探测。", Finish: "stop"}
		}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := dir
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}
	m, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, _ string, wake <-chan struct{}) { loop.Run(tctx, wake) },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(task)
		m.Close()
	})
	tk, err := m.CreateTask(ctx, task, "LLM 已满足拒收", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "确认主站暴露面", map[string]any{"predicate": pred}); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1"}, map[string]string{"web1": mainURL})

	// ---- 阶段 1:虫群经全合法受控入口宣称"已满足",收割照常转正 ----
	if err := DispatchStep(ctx, store, task, "s1", "确认主站暴露面(LLM 直接宣称完成)",
		DispatchOptions{GoalID: "g1", CoversAssets: []string{"web1"}, ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("派发 s1 失败: %v", err)
	}
	if _, err := ex.RunStep(ctx, task, "s1"); err != nil {
		t.Fatalf("执行 s1 失败: %v", err)
	}
	if _, err := Harvest(ctx, store, task, nil); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	if facts := countNodes(t, store, task, rings.NodeFact, "confirmed"); facts != 1 {
		t.Fatalf("LLM 的\"已满足\"候选应已照常收割转正(拒收发生在仲裁者,不在收割),实际 %d", facts)
	}

	// ---- 拒收:裁决不满足——陈述走了全部合法通道,仍不算机器证据 ----
	v, err := arbiter.Evaluate(ctx, store, task, "g1")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Satisfied {
		t.Fatalf("LLM 陈述不得使 Goal 判定通过: %+v", v)
	}
	fc, to := v.Conditions[0], v.Conditions[1]
	if fc.Kind != arbiter.CondFactConfirmed || !fc.Satisfied || fc.Actual != 1 {
		t.Fatalf("事实渠道被满足属预期(候选走了合法通道): %+v", fc)
	}
	if to.Kind != arbiter.CondToolObserved || to.Satisfied || to.Actual != 0 {
		t.Fatalf("机器观察渠道必须为 0(无真实探测): %+v", to)
	}
	if got := goalStatus(t, store, task, "g1"); got != GoalStatusActive {
		t.Fatalf("拒收后目标应保持 active,得到 %s", got)
	}
	if vs := verdictEvents(t, store, task); len(vs) != 0 {
		t.Fatalf("不得有任何裁决落账: %+v", vs)
	}

	// ---- 阶段 2:主宰拆解宣称"目标已满足"——同样翻不动目标 ----
	phase = 2
	tk.Wake()
	waitFor(t, 30*time.Second, "宣称达成的拆解请求到达", func() bool {
		return len(model.decomposeRequests()) == 1
	})
	time.Sleep(300 * time.Millisecond) // 给循环留足回静默的时间
	if got := goalStatus(t, store, task, "g1"); got != GoalStatusActive {
		t.Fatalf("LLM 拆解的\"已满足\"宣称不得迁移目标状态,得到 %s", got)
	}
	if vs := verdictEvents(t, store, task); len(vs) != 0 {
		t.Fatalf("LLM 宣称不得产生裁决: %+v", vs)
	}

	// ---- 阶段 3:机器观察落地(port_probe 实测),裁决才翻为达成 ----
	phase = 3
	if err := DispatchStep(ctx, store, task, "s2", "对主站执行只读端口探测",
		DispatchOptions{GoalID: "g1", CoversAssets: []string{"web1"}, ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("派发 s2 失败: %v", err)
	}
	if _, err := ex.RunStep(ctx, task, "s2"); err != nil {
		t.Fatalf("执行 s2 失败: %v", err)
	}
	tk.Wake()
	waitFor(t, 30*time.Second, "机器观察落地后目标经仲裁者判定达成", func() bool {
		return goalStatus(t, store, task, "g1") == GoalStatusAchieved
	})
	vs := verdictEvents(t, store, task)
	if len(vs) != 1 || !vs[0].Satisfied {
		t.Fatalf("应恰 1 条达成裁决: %+v", vs)
	}
	if vs[0].Conditions[1].Actual != 1 {
		t.Fatalf("达成应源于真实机器观察: %+v", vs[0].Conditions[1])
	}
	if got := len(model.decomposeRequests()); got != 1 {
		t.Fatalf("达成应由机器判定而非 LLM 宣称触发,拆解应保持 1 次,实际 %d", got)
	}
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// ---- 入口校验:非法谓词的目标创建即拒(契约①的创建侧) ----

func TestSeedGoalRejectsInvalidPredicate(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "goal-bad-pred"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "坏谓词"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	bad := []map[string]any{
		{"predicate": map[string]any{"op": "vibes", "conditions": []any{map[string]any{"kind": "fact_confirmed", "asset_id": "a1"}}}},
		{"predicate": map[string]any{"op": "all_of", "conditions": []any{}}},
		{"predicate": "目标完成就算完成"}, // 非对象
	}
	for i, detail := range bad {
		if err := SeedGoal(ctx, store, task, fmt.Sprintf("g%d", i+1), "坏谓词目标", detail); err == nil {
			t.Fatalf("非法谓词 #%d 应在创建入口被拒", i+1)
		}
	}
	// 合法谓词照常入账。
	if err := SeedGoal(ctx, store, task, "g-ok", "好目标", map[string]any{
		"predicate": map[string]any{"op": "all_of", "conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "a1", "min": 1}}},
	}); err != nil {
		t.Fatalf("合法谓词不应被拒: %v", err)
	}
	if got := goalStatus(t, store, task, "g-ok"); got != GoalStatusActive {
		t.Fatalf("合法目标应 active 入图,得到 %s", got)
	}
}
