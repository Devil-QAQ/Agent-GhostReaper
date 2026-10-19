package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-04 完成标准 ----
// 预算耗尽触发暂停而非失败,接力单位正确续跑;settling 时序实测
//(收尾中状态可见、宽限硬停生效、账本可查收尾事件)。

// blackboxRecs 取任务内全部黑匣子记录。
func blackboxRecs(t *testing.T, store *rings.Store, task string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码黑匣子记录: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// lifecycleActionsFor 任务生命周期动作序列(overmind 侧测试用)。
func lifecycleActionsFor(t *testing.T, store *rings.Store, task string) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		out = append(out, p.Action)
	}
	return out
}

// transcriptExists 主宰会话转录 JSONL 是否已落盘(四类记录之二)。
func transcriptExists(t *testing.T, root, task, prefix string) bool {
	t.Helper()
	dir := filepath.Join(root, task, "transcripts")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}

// TestBudgetRenewalE2E P4-01 ② 到线续跑主证(R16 语义修订:配了上限的
// 单位到线 = 收住写回 + 原会话续跑,非失败、非切段):
//
//	s1 意图(3 轮预算)跑满不收敛 → Settlement 收尾把第一条事实写回 →
//	**同一单位原会话续跑**(预算逐段重算,黑匣子 unit_budget_renew
//	留痕)→ 续跑段写回第二条事实收口 done → Goal 机器判定达成 →
//	completed。全程单单位单会话(转录连续,零换人税),无 exhausted
//	落地、无接力派发。
func TestBudgetRenewalE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "renewal-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()
	packURLRe := regexp.MustCompile(`-\s+web1 \[service\] (http://\S+)`)

	// 脚本分支:
	//   拆解:无步骤 → 派 s1;其余一律空列表(到线续跑不需要接力派发)。
	//   虫群包:收尾轮写回第一条事实;续跑轮写回第二条事实收口;常规轮
	//   逐轮探测烧满 3 轮预算。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			switch {
			case strings.Contains(rr.LastUser, "步骤:\n  (无)"):
				return scriptedReply{Text: `{"new_intents":[{"intent":"枚举靶站敏感路径(robots/版本)","expected_observation":"路径存在性与内容证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			default:
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
		}
		base := mainURL
		if u := packURLRe.FindStringSubmatch(rr.LastUser); u != nil {
			base = u[1]
		}
		// 收尾轮(引擎注入):把 s1 已取得的发现写回再收口。
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			if !seenToolResult(rr, "submit_candidate_fact", "已入候选事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "w1", Name: "submit_candidate_fact",
					Args: fmt.Sprintf(`{"statement":"robots.txt 与 /api/version 路径存在","evidence":"http_probe 实测 robots Disallow 条目与版本 JSON","asset_id":"web1"}`),
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "预算到线,发现已写回,等待续跑。", Finish: "stop"}
		}
		// 续跑轮(P4-01 ② 原会话续):补齐第二条事实后收口。判据按调用
		// 参数查(上一段收尾的提交结果已在历史,"已入候选事实"不可复用)。
		if strings.Contains(rr.LastUser, "预算 · 续跑") {
			if !seenToolCallArg(rr, "submit_candidate_fact", "剩余路径枚举完成") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "r1", Name: "submit_candidate_fact",
					Args: fmt.Sprintf(`{"statement":"剩余路径枚举完成,靶站共两个敏感路径","evidence":"续跑:沿用本会话已确认发现,补齐 /old-backup/ 列举线索","asset_id":"web1"}`),
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "续跑完成:剩余路径已枚举。", Finish: "stop"}
		}
		// s1 单位:逐轮探测,不收敛(烧满 3 轮预算)。
		switch {
		case !seenToolResult(rr, "http_probe", "Disallow"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "p" + strconv.Itoa(rr.Index), Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, base+"/robots.txt"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "http_probe", `"version"`):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "v" + strconv.Itoa(rr.Index), Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, base+"/api/version"),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "x" + strconv.Itoa(rr.Index), Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, base+"/"),
			}}, Finish: "tool_calls"}
		}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root,
		swarm.Budget{MaxTurns: 3, RunSeconds: 300, WrapupMaxTurns: 2})
	ex.Model = "scripted-overmind-model"

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov,
		RootDir: root, Model: "scripted-overmind-model"}
	m := newManagerAsm(t, store, root, loop)
	if _, err := m.CreateTask(ctx, task, "预算续跑 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	mustSeedGoal(t, store, task, "g1", "枚举靶站敏感路径", map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 2},
		},
	})
	mustSeedAsset(t, store, task, "web1", mainURL)
	m.Task(task).Wake()

	waitFor(t, 60*time.Second, "任务经原会话续跑达成完成终态", func() bool {
		return m.Task(task).Status() == "completed"
	})

	// ---- s1 同单位续跑完成(done,非 exhausted;无接力步) ----
	s1 := mustNode(t, store, task, "s1")
	if s1.Status != "done" {
		t.Fatalf("s1 应原会话续跑完成: %s", s1.Status)
	}
	if steps := countNodes(t, store, task, rings.NodeStep, ""); steps != 1 {
		t.Fatalf("应只有 s1 一个步骤(无接力派发),实际 %d", steps)
	}

	// ---- 转录连续可证:同文件含收尾指令与续跑指令 ----
	tsPath := filepath.Join(root, task, "transcripts", "swarm-u1.jsonl")
	raw, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("读 u1 转录: %v", err)
	}
	if !strings.Contains(string(raw), "运行预算即将耗尽") || !strings.Contains(string(raw), "预算 · 续跑") {
		t.Fatalf("u1 转录应同文件含收尾与续跑指令(原会话连续)")
	}

	// ---- 黑匣子:到线续跑留痕 + 过半里程碑;零 exhausted 落地 ----
	var renew, exhausted, half int
	for _, rec := range blackboxRecs(t, store, task) {
		switch rec.Type {
		case rings.BlackboxBudgetRenewal:
			renew++
			if rec.StepID != "s1" {
				t.Fatalf("续跑留痕应锚定 s1: %+v", rec)
			}
		case rings.BlackboxUnitExhausted:
			exhausted++
		case rings.BlackboxBudgetHalf:
			half++
		}
	}
	if renew < 1 || exhausted != 0 || half < 1 {
		t.Fatalf("黑匣子应含续跑≥1+过半≥1 且零 exhausted: renew=%d exhausted=%d half=%d", renew, exhausted, half)
	}

	// ---- 四类记录分工对账 ----
	// ① 业务事件入年轮:两条事实确认 + 血缘。
	facts := countNodes(t, store, task, rings.NodeFact, "confirmed")
	if facts != 2 {
		t.Fatalf("应有两条已确认事实,实际 %d", facts)
	}
	// ② 会话原文 JSONL:虫群与主宰会话转录文件都在。
	if !transcriptExists(t, root, task, "swarm-u") || !transcriptExists(t, root, task, "overmind-"+task+"-d") {
		t.Fatalf("会话转录 JSONL 缺失(虫群/主宰)")
	}
	// ③ token 独立表:单一虫群单位 + 主宰拆解的逐次用量都在(零换人税:
	// 全程只有 u1,无第二只单位)。
	rows, err := store.TokenUsageRows(ctx, task)
	if err != nil {
		t.Fatalf("查 token 表失败: %v", err)
	}
	agents := map[string]int{}
	for _, r := range rows {
		agents[r.Agent]++
	}
	if agents["swarm:u1"] == 0 || agents["swarm:u2"] != 0 || agents[ActorOvermind] == 0 {
		t.Fatalf("token 表应只有 u1 与主宰(无接力单位): %v", agents)
	}
	// 生命周期:无超时任务不进收尾。
	if acts := lifecycleActionsFor(t, store, task); !containsInOrder(acts, []string{"created", "started", "completed"}) || contains(acts, "settling") {
		t.Fatalf("无超时任务不应进收尾: %v", acts)
	}
}

// TestTaskSettlingE2E P2-04 settling 时序(优雅收尾形态):整体超时 4 秒,
// 在跑单位挂死 → 到点就地进收尾(Settlement 把发现写回)→ exhausted;
// 任务状态先翻"收尾中"(面板可见),终局判定目标达成 → completed;
// 账本可查:task.lifecycle 序列 + 黑匣子 settling/settled 事件。
func TestTaskSettlingE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "settle-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()
	packURLRe := regexp.MustCompile(`-\s+web1 \[service\] (http://\S+)`)

	// 脚本:拆解派 1 条意图(P4-04 起落地间有增量轮,图上已有步骤即
	// 不再重复派发——提示词规则 2 同构);虫群单位主轮挂死(sleep 30
	// 越过 4 秒收尾线),收尾轮把发现写回(优雅收尾)。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "步骤:\n  (无)") {
				return scriptedReply{Text: `{"new_intents":[{"intent":"探测靶站版本与路径","expected_observation":"版本与路径证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
		}
		_ = packURLRe // 挂死形态不读任务包 URL;保留正则说明意图来源
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			switch {
			case !seenToolResult(rr, "bash", "收尾整理完成"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "sort1", Name: "bash", Args: `{"command":"sleep 1 && echo 收尾整理完成"}`,
				}}, Finish: "tool_calls"}
			case !seenToolResult(rr, "submit_candidate_fact", "已入候选事实"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "w1", Name: "submit_candidate_fact",
					Args: `{"statement":"收尾写回:靶站版本接口存在","evidence":"任务整体超时收尾,探测所得写回","asset_id":"web1"}`,
				}}, Finish: "tool_calls"}
			default:
				return scriptedReply{Text: "整体超时收尾,发现已写回。", Finish: "stop"}
			}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "s" + strconv.Itoa(rr.Index), Name: "bash",
			Args: `{"command":"sleep 30"}`,
		}}, Finish: "tool_calls"}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root,
		swarm.Budget{MaxTurns: 20, RunSeconds: 300, WrapupMaxTurns: 3})

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov,
		RootDir: root, SettleGraceSeconds: 8}
	// 收尾轮含 1 秒整理:收尾中状态有稳定可见窗口(宽限 8 秒足够)。
	m := newManagerAsm(t, store, root, loop)
	if _, err := m.CreateTask(ctx, task, "settling 时序 E2E", rings.RoeUpdated{},
		map[string]any{"timeout_seconds": 4}); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	mustSeedGoal(t, store, task, "g1", "探测靶站", map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
		},
	})
	mustSeedAsset(t, store, task, "web1", mainURL)

	// 收尾中状态可见性:全程轮询任务状态(收尾窗口短暂,采样 2ms)。
	var sawSettling atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m.Task(task).Status() != "completed" && m.Task(task).Status() != "stopped" {
			if m.Task(task).Status() == "settling" {
				sawSettling.Store(true)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	m.Task(task).Wake()
	waitFor(t, 60*time.Second, "收尾终局判定达成完成终态", func() bool {
		return m.Task(task).Status() == "completed"
	})
	<-done

	if !sawSettling.Load() {
		t.Fatalf("任务状态应经过\"收尾中\"(面板可见)")
	}
	// 单位:挂死被收尾线打断 → 收尾写回 → exhausted(timeout,非 aborted)。
	s1 := mustNode(t, store, task, "s1")
	if s1.Status != "exhausted" {
		t.Fatalf("收尾线打断应落 exhausted: %s", s1.Status)
	}
	if facts := countNodes(t, store, task, rings.NodeFact, "confirmed"); facts != 1 {
		t.Fatalf("收尾应把发现写回并确认,实际 %d", facts)
	}
	// 账本可查收尾事件:生命周期序列含 settling;黑匣子 settling/settled。
	acts := lifecycleActionsFor(t, store, task)
	if !containsInOrder(acts, []string{"created", "started", "settling", "completed"}) {
		t.Fatalf("生命周期序列应含 settling→completed: %v", acts)
	}
	var settleEvts int
	for _, rec := range blackboxRecs(t, store, task) {
		switch rec.Type {
		case rings.BlackboxTaskSettling, rings.BlackboxTaskSettled:
			settleEvts++
		case rings.BlackboxGraceHardStop:
			t.Fatalf("优雅收尾形态不应触发宽限硬停: %+v", rec)
		}
	}
	if settleEvts != 2 {
		t.Fatalf("黑匣子应有 settling+settled 两条,实际 %d", settleEvts)
	}
}

// TestSettlingHardStopE2E P2-04 宽限硬停:收尾轮自己也挂死(引擎对收尾
// 轮不设墙钟上限),宽限(2 秒)到点硬停单位(aborted 真中断);终局
// 判定目标未达成 → stopped;黑匣子 grace_hard_stop 留痕含被停单位。
func TestSettlingHardStopE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "hardstop-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 脚本:主轮与收尾轮都挂死(sleep 30)——收尾轮无墙钟上限,只有
	// 宽限硬停能打断。拆解仅首轮派 1 条(P4-04 增量轮下不重复派发)。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "步骤:\n  (无)") {
				return scriptedReply{Text: `{"new_intents":[{"intent":"探测靶站","expected_observation":"证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
		}
		_ = mainURL
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "s" + strconv.Itoa(rr.Index), Name: "bash",
			Args: `{"command":"sleep 30"}`,
		}}, Finish: "tool_calls"}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root,
		swarm.Budget{MaxTurns: 20, RunSeconds: 300, WrapupMaxTurns: 2})

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov,
		RootDir: root, SettleGraceSeconds: 2}
	m := newManagerAsm(t, store, root, loop)
	if _, err := m.CreateTask(ctx, task, "宽限硬停 E2E", rings.RoeUpdated{},
		map[string]any{"timeout_seconds": 3}); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	mustSeedGoal(t, store, task, "g1", "探测靶站", map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
		},
	})
	mustSeedAsset(t, store, task, "web1", mainURL)
	m.Task(task).Wake()

	waitFor(t, 60*time.Second, "宽限硬停后定停止终态", func() bool {
		return m.Task(task).Status() == "stopped"
	})

	// 单位被硬停:aborted(真中断);进度实时入账的纪律下无事实,如实为 0。
	s1 := mustNode(t, store, task, "s1")
	if s1.Status != "aborted" {
		t.Fatalf("宽限硬停应落 aborted: %s", s1.Status)
	}
	acts := lifecycleActionsFor(t, store, task)
	if !containsInOrder(acts, []string{"created", "started", "settling", "stopped"}) {
		t.Fatalf("生命周期序列应含 settling→stopped: %v", acts)
	}
	var hardStop *rings.BlackboxRecord
	for i, rec := range blackboxRecs(t, store, task) {
		if rec.Type == rings.BlackboxGraceHardStop {
			r := blackboxRecs(t, store, task)[i]
			hardStop = &r
		}
	}
	if hardStop == nil {
		t.Fatalf("黑匣子应留痕宽限硬停")
	}
	if units, _ := hardStop.Detail["units"].([]any); len(units) != 1 {
		t.Fatalf("硬停留痕应含恰 1 只单位: %+v", hardStop.Detail)
	}
	// 终态 stopped 的因果:终局判定未达成(R16:不得因超时静默标失败)。
	if facts := countNodes(t, store, task, rings.NodeFact, ""); facts != 0 {
		t.Fatalf("挂死单位未写回发现,事实应如实为 0: %d", facts)
	}
}

// ---- 小件 ----

// newManagerAsm 装配 Manager:OnTaskRun 挂入给定循环体,SettleCtl 接
// Manager 本体(与生产 main.go 同构:收尾状态翻转/定终态经 Manager)。
func newManagerAsm(t *testing.T, store *rings.Store, root string, loop *Loop) *manager.Manager {
	t.Helper()
	var mgr *manager.Manager
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			loop.SettleCtl = mgr // 闭包引用变量,任务拉起前必已赋值
			loop.Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	return mgr
}

func mustNode(t *testing.T, store *rings.Store, task, nodeID string) *rings.NodeFull {
	t.Helper()
	n, err := store.Node(context.Background(), task, nodeID)
	if err != nil {
		t.Fatalf("查节点 %s 失败: %v", nodeID, err)
	}
	return n
}

func mustSeedGoal(t *testing.T, store *rings.Store, task, id, title string, predicate map[string]any) {
	t.Helper()
	detail := map[string]any{}
	if predicate != nil {
		detail["predicate"] = predicate
	}
	if err := SeedGoal(context.Background(), store, task, id, title, detail); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
}

func mustSeedAsset(t *testing.T, store *rings.Store, task, id, title string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
			Title: title, Status: "active", AssetClass: rings.AssetService},
	}); err != nil {
		t.Fatalf("种资产失败: %v", err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// seenToolCallArg 历史里是否已有某工具的调用参数含 marker(原会话续跑
// 场景:上一段的提交结果已在历史,按调用参数区分本段的新提交)。
func seenToolCallArg(rr *recordedRequest, toolName, marker string) bool {
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == toolName && strings.Contains(tc.Function.Arguments, marker) {
				return true
			}
		}
	}
	return false
}

func containsInOrder(xs, want []string) bool {
	i := 0
	for _, v := range xs {
		if i < len(want) && v == want[i] {
			i++
		}
	}
	return i == len(want)
}
