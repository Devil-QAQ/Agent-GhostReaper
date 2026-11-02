package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-03 完成标准 ----
// E2E:一个假设从提交到转 Step 派发验证再到结论入图全链路跑通;
// 全程类型化边留痕(produces/about/verifies),评估三去向与验证结论
// 入 hypothesis.record 审计流;R12 验证单位 ≠ 提出单位;验证中取得的
// 观察经既有回流通道入图(候选事实转正)。

// hypothesisRecords 取任务内全部 hypothesis.record(按类型过滤,空为全部)。
func hypothesisRecords(t *testing.T, store *rings.Store, task, typ string) []rings.HypothesisRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.HypothesisRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventHypothesisRecord {
			continue
		}
		var r rings.HypothesisRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 hypothesis.record: %v", err)
		}
		if typ == "" || r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

// hypByTitle 按标题找 Hypothesis 节点。
func hypByTitle(t *testing.T, store *rings.Store, task, substr string) *rings.NodeFull {
	t.Helper()
	hyps, err := store.NodesByType(context.Background(), task, rings.NodeHypothesis, "")
	if err != nil {
		t.Fatalf("查假设失败: %v", err)
	}
	for i := range hyps {
		if strings.Contains(hyps[i].Title, substr) {
			return &hyps[i]
		}
	}
	t.Fatalf("未找到标题含 %q 的假设: %+v", substr, hyps)
	return nil
}

// hypByTitleSoft hypByTitle 的非致命版(轮询用)。
func hypByTitleSoft(store *rings.Store, task, substr string) *rings.NodeFull {
	hyps, err := store.NodesByType(context.Background(), task, rings.NodeHypothesis, "")
	if err != nil {
		return nil
	}
	for i := range hyps {
		if strings.Contains(hyps[i].Title, substr) {
			return &hyps[i]
		}
	}
	return nil
}

// hasEdge 测试辅助:任务内是否存在指定类型化边。
func hasEdge(store *rings.Store, task string, et rings.EdgeType, from, to string) bool {
	es, err := store.Edges(context.Background(), task, et)
	if err != nil {
		return false
	}
	for _, e := range es {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}

// TestHypothesisBackflowE2E P2-03 完整假设回流 E2E(镜已装配,流量留痕):
//
//	管理后台假设:提交(produces 边)→ 主宰评估"转验证"(evaluation
//	  verify + 验证 Step 派发 + verifies 边 + testing)→ 独立单位真实
//	  探测 web2(镜留痕)→ submit_hypothesis_result(supported)+
//	  submit_candidate_fact → 收割迁移 confirmed + 候选事实转正(结论入图);
//	隐藏路径假设:评估直接废弃(理由留痕,R10);
//	备份凭据假设:存疑挂账(留在图上待唤醒;重复 hold 不重复记账)。
func TestHypothesisBackflowE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "hypo-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL, adminURL := target.MainURL(), target.AdminURL()

	// 脚本模型分支:
	//   假设评估(无工具,提示含"假设评估态势快照"):按陈述裁决三去向;
	//   拆解(无工具):发现意图已派过才空列表;
	//   验证包(任务包含"九、假设验证任务"):探测 web2 → 候选事实 +
	//     结构化结论(supported);
	//   发现包:robots → 三条假设(管理后台→verify / 隐藏→discard /
	//     凭据→hold)→ 收口。
	hypLineRe := regexp.MustCompile(`(?m)^\s*- (h-[0-9a-f]+) \[(?:candidate|held)\] (.*)$`)
	packHypIDRe := regexp.MustCompile(`假设 (h-[0-9a-f]+)`)
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 && strings.Contains(rr.LastUser, "假设评估态势快照") {
			var verdicts []string
			for _, m := range hypLineRe.FindAllStringSubmatch(rr.LastUser, -1) {
				id, stmt := m[1], m[2]
				switch {
				case strings.Contains(stmt, "管理后台"):
					verdicts = append(verdicts, fmt.Sprintf(
						`{"hypothesis_id":%q,"decision":"verify","reason":"独立端口服务形态可疑且可低成本证伪","verify_intent":"只读探测 web2 根路径,确认服务形态与未授权可否进入","action_level":"只读探测"}`, id))
				case strings.Contains(stmt, "隐藏"):
					verdicts = append(verdicts, fmt.Sprintf(
						`{"hypothesis_id":%q,"decision":"discard","reason":"robots 条目数量的风格猜测,无具体可验证线索","action_level":"只读探测"}`, id))
				case strings.Contains(stmt, "凭据"):
					verdicts = append(verdicts, fmt.Sprintf(
						`{"hypothesis_id":%q,"decision":"hold","reason":"备份存在但内容未取得,挂账待新证据","action_level":"只读探测"}`, id))
				}
			}
			return scriptedReply{Text: `{"verdicts":[` + strings.Join(verdicts, ",") + `]}`, Finish: "stop"}
		}
		if len(rr.ToolsOffered) == 0 { // 主宰拆解
			if strings.Contains(rr.LastUser, "提交直觉线索") {
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[{"intent":"审计主站并提交直觉线索","expected_observation":"robots 内容与直觉假设清单","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
		}
		if strings.Contains(rr.LastUser, "九、假设验证任务") {
			hm := packHypIDRe.FindStringSubmatch(rr.LastUser)
			if hm == nil {
				return scriptedReply{Text: "验证任务包缺假设 ID。", Finish: "stop"}
			}
			hypID := hm[1]
			switch {
			case !seenToolResult(rr, "bash", "mock admin console"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "vb1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -i -m 5 %s/"}`, adminURL),
				}}, Finish: "tool_calls"}
			case !hasToolCallWithArg(rr, "submit_candidate_fact", "认证的管理后台"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "vf1", Name: "submit_candidate_fact",
					Args: `{"statement":"web2 为需 Basic 认证的管理后台(401 质询)","evidence":"curl -i 实测返回 401 + Basic 质询,正文 mock admin console","asset_id":"web2"}`,
				}}, Finish: "tool_calls"}
			case !seenToolResult(rr, "submit_hypothesis_result", "已记录"):
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "vr1", Name: "submit_hypothesis_result",
					Args: fmt.Sprintf(`{"hypothesis_id":%q,"verdict":"supported","evidence":"独立探测 web2:401 + Basic 质询 + mock admin console,确为管理后台(未授权不可进入)","note":"后台形态证实"}`, hypID),
				}}, Finish: "tool_calls"}
			default:
				return scriptedReply{Text: "验证完成:结论与候选事实均已写回。", Finish: "stop"}
			}
		}
		// 收尾提示(预算耗尽):无新发现可写,如实收口,不重复提交。
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			return scriptedReply{Text: "收尾:无尚未提交的可保留发现。", Finish: "stop"}
		}
		// 发现任务分支。
		switch {
		case !seenToolResult(rr, "bash", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/robots.txt"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_hypothesis", "管理后台"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d2", Name: "submit_hypothesis",
				Args: `{"statement":"web2 独立端口服务疑似管理后台","asset_id":"web2","basis":"主页注释与独立端口服务形态(弱依据)"}`,
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_hypothesis", "隐藏"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d3", Name: "submit_hypothesis",
				Args: `{"statement":"主站可能还有未编入 robots 的隐藏敏感路径","asset_id":"web1","basis":"robots 只有一条 Disallow"}`,
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_hypothesis", "凭据"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d4", Name: "submit_hypothesis",
				Args: `{"statement":"备份目录中的 config.bak 可能包含可用凭据","asset_id":"web1","basis":"备份文件名风格"}`,
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "审计完成:三条直觉候选已提交。", Finish: "stop"}
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
	if _, err := m.CreateTask(ctx, task, "P2-03 假设回流 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "审计 mock 靶站并收割直觉线索", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1", "web2"},
		map[string]string{"web1": mainURL, "web2": adminURL})
	tk := m.Task(task)
	tk.Wake()

	// ---- 阶段一:三条假设各到目标状态(成立 / 已废弃 / 挂账) ----
	waitFor(t, 60*time.Second, "三条假设到目标状态", func() bool {
		h1 := hypByTitleSoft(store, task, "管理后台")
		h2 := hypByTitleSoft(store, task, "隐藏")
		h3 := hypByTitleSoft(store, task, "凭据")
		return h1 != nil && h2 != nil && h3 != nil &&
			h1.Status == rings.HypothesisConfirmed &&
			h2.Status == rings.HypothesisDiscarded &&
			h3.Status == rings.HypothesisHeld
	})
	h1 := hypByTitle(t, store, task, "管理后台")
	h2 := hypByTitle(t, store, task, "隐藏")
	h3 := hypByTitle(t, store, task, "凭据")

	// 全链路状态机:提交 candidate → testing(转验证)→ confirmed(收割迁移)。
	if got := claimStates(t, store, task, h1.ID); len(got) != 3 ||
		got[0] != rings.HypothesisCandidate || got[1] != rings.HypothesisTesting ||
		got[2] != rings.HypothesisConfirmed {
		t.Fatalf("管理后台假设状态机轨迹不符: %v", got)
	}
	if got := claimStates(t, store, task, h2.ID); len(got) != 2 ||
		got[0] != rings.HypothesisCandidate || got[1] != rings.HypothesisDiscarded {
		t.Fatalf("隐藏路径假设应直接废弃: %v", got)
	}
	if got := claimStates(t, store, task, h3.ID); len(got) != 2 ||
		got[0] != rings.HypothesisCandidate || got[1] != rings.HypothesisHeld {
		t.Fatalf("备份凭据假设应挂账: %v", got)
	}

	// ---- 回流轨迹:全程类型化边 ----
	// 提交轨迹:发现步骤 ─produces→ 假设;资产锚点 ─about→。
	discoverStep, _ := h1.Detail["step_id"].(string)
	if discoverStep == "" {
		t.Fatalf("假设 detail 缺提出步骤记录")
	}
	if !hasEdge(store, task, rings.EdgeProduces, discoverStep, h1.ID) {
		t.Fatalf("提交轨迹缺 produces 边(%s → %s)", discoverStep, h1.ID)
	}
	if !hasEdge(store, task, rings.EdgeAbout, h1.ID, "web2") {
		t.Fatalf("假设应锚定 web2(about 边)")
	}
	// 转验证轨迹:评估记录 verify + 验证 Step 绑定 + verifies 边。
	evals := hypothesisRecords(t, store, task, rings.HypothesisRecEvaluation)
	var h1Eval *rings.HypothesisRecord
	for i := range evals {
		if evals[i].HypothesisID == h1.ID && evals[i].Decision == rings.HypothesisVerify {
			h1Eval = &evals[i]
		}
	}
	if h1Eval == nil || h1Eval.StepID == "" || h1Eval.VerifyIntent == "" || h1Eval.Reason == "" {
		t.Fatalf("转验证评估记录应带验证步骤/意图/理由: %+v", h1Eval)
	}
	vStep, err := store.Node(ctx, task, h1Eval.StepID)
	if err != nil {
		t.Fatalf("查验证步骤失败: %v", err)
	}
	if vStep.Detail["kind"] != StepDetailKindVerify || vStep.Detail["hypothesis_id"] != h1.ID {
		t.Fatalf("验证步骤绑定不符: %+v", vStep.Detail)
	}
	if vStep.Detail["action_level"] != "只读探测" {
		t.Fatalf("验证步骤应携带意图分级(审批强度依据): %+v", vStep.Detail)
	}
	if !hasEdge(store, task, rings.EdgeVerifies, vStep.ID, h1.ID) {
		t.Fatalf("转验证轨迹缺 verifies 边(%s → %s)", vStep.ID, h1.ID)
	}
	if !hasEdge(store, task, rings.EdgeCovers, vStep.ID, "web2") {
		t.Fatalf("验证步骤应 covers 假设锚定资产 web2")
	}

	// ---- 验证结论:结构化写回 + R12 验证者 ≠ 提出者 ----
	results := hypothesisRecords(t, store, task, rings.HypothesisRecResult)
	if len(results) != 1 || results[0].HypothesisID != h1.ID ||
		results[0].Verdict != rings.HypothesisSupported ||
		results[0].StepID != vStep.ID || results[0].EvidenceHead == "" {
		t.Fatalf("验证结论记录不符: %+v", results)
	}
	proposer, _ := h1.Detail["unit_id"].(string)
	if proposer == "" || results[0].UnitID == "" || results[0].UnitID == proposer {
		t.Fatalf("R12 违例:验证者 %s 与提出者 %s", results[0].UnitID, proposer)
	}

	// ---- 结论入图:验证中取得的观察走既有回流通道并转正 ----
	facts, err := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
	if err != nil {
		t.Fatalf("查已确认事实失败: %v", err)
	}
	found := false
	for _, f := range facts {
		if strings.Contains(f.Title, "管理后台") &&
			hasEdge(store, task, rings.EdgeProduces, vStep.ID, f.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("验证单位的候选事实应转正并挂在验证步骤血缘下(结论入图)")
	}

	// ---- 真实性:靶站被独立验证单位真实命中(非账本自说自话) ----
	if got := target.Hits("admin", "/"); got < 1 {
		t.Fatalf("web2 应被验证单位真实探测,实际命中 %d", got)
	}

	// ---- 废弃与挂账:理由留痕(R10);重复挂账不重复记账 ----
	var h2Eval, h3Eval *rings.HypothesisRecord
	h3Evals := 0
	for i := range evals {
		switch evals[i].HypothesisID {
		case h2.ID:
			h2Eval = &evals[i]
		case h3.ID:
			h3Eval = &evals[i]
			h3Evals++
		}
	}
	if h2Eval == nil || h2Eval.Decision != rings.HypothesisDiscard || h2Eval.Reason == "" {
		t.Fatalf("废弃必须带理由(R10 废案有理由): %+v", h2Eval)
	}
	if h3Eval == nil || h3Eval.Decision != rings.HypothesisHold || h3Eval.Reason == "" {
		t.Fatalf("挂账必须带理由: %+v", h3Eval)
	}
	if h3Evals != 1 {
		t.Fatalf("已挂账假设重复 hold 不得重复记账(防噪音),实际 %d 条评估记录", h3Evals)
	}

	// ---- 静默:终态后主宰回静默,拆解/评估计数稳定不再增长 ----
	last, stable := -1, 0
	waitFor(t, 30*time.Second, "终态后主宰回静默(无工具会话计数稳定)", func() bool {
		n := len(model.decomposeRequests())
		if n == last {
			stable++
		} else {
			last, stable = n, 0
		}
		return stable >= 20 // ≈300ms 无新会话
	})
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// TestCloseHypotheses 确定性结论收割(零 LLM):testing 假设按最新验证
// Step 的终态与结构化结论迁移——supported→成立、refuted→已废弃、
// inconclusive→回候选、步骤终态无结论→如实回候选(不悬空);验证仍在途
// 不迁移。
func TestCloseHypotheses(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "hypo-close"

	// 铺垫:资产 + 假设 ×5(各挂一个验证步骤,状态各异)。
	mk := func(hypID, stepID, stepStatus string, result *rings.HypothesisRecord) {
		t.Helper()
		events := []rings.Event{
			{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
				NodeID: hypID, NodeType: rings.NodeHypothesis, Title: "假设 " + hypID, Status: rings.HypothesisTesting,
				Detail: map[string]any{"basis": "b", "unit_id": "u1", "step_id": "s0"}}},
			{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: stepID, NodeType: rings.NodeStep, Title: "验证: " + hypID, Status: stepStatus,
				Detail: map[string]any{"kind": "verify", "hypothesis_id": hypID, "action_level": "只读探测"}}},
		}
		if result != nil {
			events = append(events, rings.Event{Kind: rings.EventHypothesisRecord, Actor: "swarm:u2",
				Payload: *result})
		}
		if _, err := store.Append(ctx, task, events...); err != nil {
			t.Fatalf("铺垫 %s 失败: %v", hypID, err)
		}
	}
	mk("h-sup", "v1", "done", &rings.HypothesisRecord{
		HypothesisID: "h-sup", StepID: "v1", UnitID: "u2", Type: rings.HypothesisRecResult, Verdict: rings.HypothesisSupported})
	mk("h-ref", "v2", "done", &rings.HypothesisRecord{
		HypothesisID: "h-ref", StepID: "v2", UnitID: "u2", Type: rings.HypothesisRecResult, Verdict: rings.HypothesisRefuted})
	mk("h-inc", "v3", "exhausted", &rings.HypothesisRecord{
		HypothesisID: "h-inc", StepID: "v3", UnitID: "u2", Type: rings.HypothesisRecResult, Verdict: rings.HypothesisInconclusive})
	mk("h-nores", "v4", "failed", nil)        // 步骤失败未写回 → 如实回候选
	mk("h-inflight", "v5", "dispatched", nil) // 验证仍在途 → 不迁移

	l := &Loop{TaskID: task, Store: store}
	moved, err := l.closeHypotheses(ctx)
	if err != nil {
		t.Fatalf("确定性结论收割失败: %v", err)
	}
	if moved != 4 {
		t.Fatalf("应恰迁移 4 条(在途不迁): %d", moved)
	}
	for id, want := range map[string]string{
		"h-sup":      rings.HypothesisConfirmed,
		"h-ref":      rings.HypothesisDiscarded,
		"h-inc":      rings.HypothesisCandidate,
		"h-nores":    rings.HypothesisCandidate,
		"h-inflight": rings.HypothesisTesting,
	} {
		n, err := store.Node(ctx, task, id)
		if err != nil {
			t.Fatalf("查 %s: %v", id, err)
		}
		if n.Status != want {
			t.Fatalf("%s 应为 %s,实际 %s", id, want, n.Status)
		}
	}
	// 幂等:已迁移/在途的不再动。
	if moved, err := l.closeHypotheses(ctx); err != nil || moved != 0 {
		t.Fatalf("二次收割应零迁移: %d %v", moved, err)
	}
}
