package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-02 完成标准 ----
// mock 靶站上漏洞走完整状态机(候选 → 竞争解释 → 反证(多轮)→
// Confirmed / 待确认 → 重开再证);两维声明分开、严重级别跟影响面证据走
//(发现者无级别通道);LLM 陈述作为证据被仲裁者拒收;反证过程流量经镜
// 留痕,收割定稿的复现步骤可回放(Burp 可验证)。

// claimStates 取节点状态迁移历史(node.added 的初始状态 + 逐次 state_changed)。
func claimStates(t *testing.T, store *rings.Store, task, nodeID string) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var states []string
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.NodeID == nodeID {
				states = append(states, p.Status)
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.NodeID == nodeID {
				states = append(states, p.Status)
			}
		}
	}
	return states
}

// testclaimRecords 取任务内全部 testclaim.record(按类型过滤,空为全部)。
func testclaimRecords(t *testing.T, store *rings.Store, task, typ string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 testclaim.record: %v", err)
		}
		if typ == "" || r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

// claimByTitle 按标题找 TestClaim 节点。
func claimByTitle(t *testing.T, store *rings.Store, task, substr string) *rings.NodeFull {
	t.Helper()
	claims, err := store.NodesByType(context.Background(), task, rings.NodeTestClaim, "")
	if err != nil {
		t.Fatalf("查声明失败: %v", err)
	}
	for i := range claims {
		if strings.Contains(claims[i].Title, substr) {
			return &claims[i]
		}
	}
	t.Fatalf("未找到标题含 %q 的声明: %+v", substr, claims)
	return nil
}

// TestClaimLifecycleE2E P2-02 完整负向认证 E2E(镜已装配,流量全程留痕):
//
//	备份目录声明(冻结两条竞争解释):
//	  候选 → testing(第 1 轮:排除"归属错误"、缓存污染未除)→
//	  awaiting_round(轮末机器评估:解释未全灭)→ testing(第 2 轮:缓存
//	  穿透重放排除"缓存污染")→ Confirmed(定级 high,收割定稿含复现步骤)
//	  → 重开(造物主反驳)→ testing(重开轮:全部解释复活并重新排除)→
//	  再 Confirmed(新裁决取代旧裁决,历史不删)。
//	版本暴露声明(单条解释,一轮即终态):
//	  候选 → testing(第 1 轮:解释已除但影响面证据不足;Note 里 LLM
//	  自称"影响面证据充分建议确认")→ 待确认(LLM 陈述被仲裁者拒收,R4
//	  机器搁置理由)。
//
// TestClaimLifecycleE2E P5-01 声明生命周期 e2e(单会话连打版):虫群在
// 同一会话内探索→发现→证明→反证自查→交卷(submit_claim 六字段),
// 交卷即停 candidate(机器三查归 P5-02);同任务零反证/验证步骤产生
// (P5-01⑤);reopen 保留 reopen 记录与 testing 迁移、零新步骤。
// (旧多轮反证/裁决链/收割定稿链路已随三段流水线退役,重建归 P5-02/03。)
func TestClaimLifecycleE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "claim-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 脚本模型分支(P5-01 单会话连打):
	//   拆解(无工具):审计意图已派过才派,否则空列表;
	//   连打包:robots → 备份目录 → 版本接口 → 两条 submit_claim 交卷
	//     (备份 high/版本 none,evidence_refs 引证明 URL)→ 收口。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "审计主站敏感路径") {
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[{"intent":"审计主站敏感路径暴露(robots/备份目录/版本接口)","expected_observation":"敏感路径清单与各自内容证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
		}
		switch {
		case !seenToolResult(rr, "bash", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/robots.txt"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "mock backup archive listing"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d2", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", `"version":"2.4.1"`):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d3", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_claim", "备份目录可公开匿名列举"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d4", Name: "submit_claim",
				Args: fmt.Sprintf(`{"summary":"/old-backup/ 备份目录可公开匿名列举","severity":"high","evidence_refs":[%q],"proving_marker":"db-2026-09-01.sql.gz","countercheck_note":"直连重放与加随机参数绕缓存重放各一次,返回同一列举,排除缓存假象;URL 归属本站,排除归属错误","impact_note":"备份文件 db-2026-09-01.sql.gz 与 config.bak 可被未授权下载","asset_id":"web1","vulnclass":"信息暴露"}`, mainURL+"/old-backup/"),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_claim", "公开暴露服务版本号"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d5", Name: "submit_claim",
				Args: fmt.Sprintf(`{"summary":"/api/version 公开暴露服务版本号","severity":"none","evidence_refs":[%q],"proving_marker":"2.4.1","countercheck_note":"重放一次确认稳定返回,非一次性状态;仅版本号本身无直接危害","asset_id":"web1","vulnclass":"信息暴露"}`, mainURL+"/api/version"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "version-observation", Name: "submit_candidate_fact", Args: `{"statement":"版本号观察：服务公开返回2.4.1","evidence":"本次/api/version原始响应为{\"version\":\"2.4.1\"}，仅版本本身无直接危害","asset_id":"web1"}`}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "实际漏洞交卷与版本观察均已写回。", Finish: "stop"}
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
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())

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
	if _, err := m.CreateTask(ctx, task, "交卷生命周期 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "审计 mock 靶站主站暴露面", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1"}, map[string]string{"web1": mainURL})
	tk := m.Task(task)
	tk.Wake()

	// 实际漏洞确认，信息性误交卷拒收后按工具反馈提交事实。
	waitFor(t, 60*time.Second, "漏洞确认且版本观察按事实写回", func() bool {
		backup := claimByTitleSoft(store, task, "备份目录")
		facts, _ := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
		for _, f := range facts {
			if strings.Contains(f.Title, "版本号观察") {
				return backup != nil && backup.Status == rings.TestClaimConfirmed
			}
		}
		return false
	})
	backup := claimByTitle(t, store, task, "备份目录")
	if claimByTitleSoft(store, task, "版本号") != nil {
		t.Fatal("信息性结果不得进入漏洞图")
	}

	// 交卷单事件六字段落账(引用=证明 URL;marker 为证据原文关键值)。
	submits := testclaimRecords(t, store, task, rings.TestClaimRecSubmit)
	if len(submits) != 1 {
		t.Fatalf("应恰1条真实漏洞交卷，信息性尝试不得落单: %+v", submits)
	}
	for _, sub := range submits {
		if sub.Summary == "" || !rings.IsValidSubmitSeverity(sub.Severity) ||
			len(sub.EvidenceRefs) == 0 || sub.ProvingMarker == "" || sub.CountercheckNote == "" {
			t.Fatalf("交卷单六字段必须齐全: %+v", sub)
		}
		if sub.StepID == "" || sub.UnitID == "" {
			t.Fatalf("交卷单必须锚定交卷步骤与单位: %+v", sub)
		}
	}

	// ---- 零反证步骤产生(P5-01⑤):同任务无 kind=falsify/verify 步骤,
	// 旧 falsify_result 写回零条 ----
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	for _, st := range steps {
		if k, _ := st.Detail["kind"].(string); k == "falsify" || k == "verify" {
			t.Fatalf("P5-01⑤:任务不得产生反证/验证步骤: %+v", st)
		}
	}
	if got := len(testclaimRecords(t, store, task, rings.TestClaimRecFalsify)); got != 0 {
		t.Fatalf("P5-01:新账本零反证写回: %d", got)
	}

	// ---- 单会话连打:同一单位同一会话完成探索与交卷(transcript 连续) ----
	starterUnits := map[string]bool{}
	evs, _ := store.Events(ctx, task, 0, 0)
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.SwarmRecSessionStarted {
			continue
		}
		starterUnits[rec.StepID] = true
	}
	if len(starterUnits) != 1 { // 拆解只派一条审计意图,零验证/反证意图
		t.Fatalf("连打形态应恰 1 条工作线步骤有单位起飞: %v", starterUnits)
	}

	// ---- 阶段二:reopen 保留(记录+回候选+派重验步骤,P5-02⑦) ----
	stepsBefore := 0
	steps, _ = store.NodesByType(ctx, task, rings.NodeStep, "")
	stepsBefore = len(steps)
	// reopen 需终态入口(交卷即查即时判定;测试直接置一终态再重开
	// ——reopen 机制本身的回归)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "test",
		Payload: rings.NodeStateChanged{NodeID: backup.ID, Status: rings.TestClaimConfirmed},
	}); err != nil {
		t.Fatalf("置终态失败: %v", err)
	}
	if err := ReopenClaim(ctx, store, task, backup.ID, rings.ReopenManual,
		"靶站疑似调整缓存策略,备份目录结论须复核", "creator"); err != nil {
		t.Fatalf("重开备份声明失败: %v", err)
	}
	if b := claimByTitle(t, store, task, "备份目录"); b.Status != rings.TestClaimCandidate {
		t.Fatalf("重开应回候选(等新交卷重验): %s", b.Status)
	}
	reopens := testclaimRecords(t, store, task, rings.TestClaimRecReopen)
	if len(reopens) != 1 || reopens[0].Trigger != rings.ReopenManual ||
		!strings.Contains(reopens[0].Reason, "复核") {
		t.Fatalf("重开记录不符: %+v", reopens)
	}
	// P5-02⑦:重开同批派重验步骤(拉起原会话带新反证重跑)。
	steps, _ = store.NodesByType(ctx, task, rings.NodeStep, "")
	if len(steps) != stepsBefore+1 {
		t.Fatalf("P5-02⑦:重开应派 1 条重验步骤: 前 %d 后 %d", stepsBefore, len(steps))
	}
	retakeSteps := 0
	for _, s := range steps {
		if k, _ := s.Detail["kind"].(string); k == "retake" {
			if cid, _ := s.Detail["claim_id"].(string); cid == backup.ID {
				if rv, _ := s.Detail["reverify"].(bool); !rv {
					t.Fatalf("重开派的是重验步骤(reverify): %+v", s.Detail)
				}
				retakeSteps++
			}
		}
	}
	if retakeSteps != 1 {
		t.Fatalf("重开应恰派 1 条 reverify 步骤: %d", retakeSteps)
	}

	// ---- 真实性:靶站被真实命中(证明交换非账本自说自话) ----
	if got := target.Hits("main", "/old-backup/"); got < 1 {
		t.Fatalf("/old-backup/ 应被真实命中,实际 %d", got)
	}
	if got := target.Hits("main", "/api/version"); got < 1 {
		t.Fatalf("/api/version 应被真实命中,实际 %d", got)
	}

	// ---- 静默:终态后主宰收尾回静默,拆解计数稳定不再增长 ----
	last, stable := -1, 0
	waitFor(t, 30*time.Second, "终态后主宰回静默(拆解计数稳定)", func() bool {
		n := len(model.decomposeRequests())
		if n == last {
			stable++
		} else {
			last, stable = n, 0
		}
		return stable >= 20 // ≈300ms 无新拆解
	})
	if err := loopErr(loop); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// claimByTitleSoft claimByTitle 的非致命版(未找到返回 nil,轮询用)。
func claimByTitleSoft(store *rings.Store, task, substr string) *rings.NodeFull {
	claims, err := store.NodesByType(context.Background(), task, rings.NodeTestClaim, "")
	if err != nil {
		return nil
	}
	for i := range claims {
		if strings.Contains(claims[i].Title, substr) {
			return &claims[i]
		}
	}
	return nil
}

// (TestHarvestClaimsNoDispatch 已随 HarvestClaims 派发链整段删除而退役,
// P5-03①——收割侧无声明派发工作,零派发成为结构事实,无单测对象。)

// nodeCountOf 任务内指定类型节点计数。
func nodeCountOf(t *testing.T, store *rings.Store, task string, nt rings.NodeType) int {
	t.Helper()
	ns, err := store.NodesByType(context.Background(), task, nt, "")
	if err != nil {
		t.Fatalf("查节点失败: %v", err)
	}
	return len(ns)
}

// TestReopenClaimGuards 重开入口守卫:非终态/幽灵声明/空理由拒绝;
// 合法重开落 reopen 记录 + 反证步骤 + testing 迁移一批事务。
func TestReopenClaimGuards(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "reopen-guards"

	altList := []any{map[string]any{"id": "alt1", "label": "缓存污染"}}
	mustAppendReopen := func(events ...rings.Event) {
		t.Helper()
		if _, err := store.Append(ctx, task, events...); err != nil {
			t.Fatalf("铺垫失败: %v", err)
		}
	}
	mustAppendReopen(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "发现", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-c", NodeType: rings.NodeTestClaim, Title: "已确认声明", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-c", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-c", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-t", NodeType: rings.NodeTestClaim, Title: "反证中声明", Status: "testing",
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-t", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-t", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf1", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-c", "round": 1}}},
	)

	// 反证中声明不可重开;幽灵声明拒绝;空理由拒绝。
	if err := ReopenClaim(ctx, store, task, "tc-t", rings.ReopenManual, "理由", "creator"); err == nil {
		t.Fatalf("反证中声明不可重开")
	}
	if err := ReopenClaim(ctx, store, task, "tc-404", rings.ReopenManual, "理由", "creator"); err == nil {
		t.Fatalf("幽灵声明不可重开")
	}
	if err := ReopenClaim(ctx, store, task, "tc-c", rings.ReopenManual, "  ", "creator"); err == nil {
		t.Fatalf("空理由不可重开")
	}

	// 合法重开(P5-02⑦:reopen 记录 + 回候选 + 派重验步骤拉起原会话)。
	stepsBefore := nodeCountOf(t, store, task, rings.NodeStep)
	if err := ReopenClaim(ctx, store, task, "tc-c", rings.ReopenExpired, "结论时效过期", "creator"); err != nil {
		t.Fatalf("合法重开失败: %v", err)
	}
	if got := claimStates(t, store, task, "tc-c"); len(got) != 2 || got[1] != rings.TestClaimCandidate {
		t.Fatalf("重开应回候选(等新交卷重验): %v", got)
	}
	if got := nodeCountOf(t, store, task, rings.NodeStep); got != stepsBefore+1 {
		t.Fatalf("重开应恰派 1 条重验步骤: 前 %d 后 %d", stepsBefore, got)
	}
	reopens := testclaimRecords(t, store, task, rings.TestClaimRecReopen)
	if len(reopens) != 1 || reopens[0].Trigger != rings.ReopenExpired || reopens[0].Reason != "结论时效过期" {
		t.Fatalf("重开记录不符: %+v", reopens)
	}
}
