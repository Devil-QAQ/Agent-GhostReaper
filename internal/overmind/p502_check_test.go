package overmind

// P5-02 交卷即查与补考链路验收(完成标准①②③⑤⑥⑧ 的账本级集成):
//   - 集成形态的三查分支(真账本:引用命中/marker/标准/自查);
//   - 补考链路:首败注入反馈→原会话续跑→重交确认;排队档位队首;
//     上限 1 次、二次不过落证据不足;补考未重交兜底;
//   - reopen 重验推翻(收割兜底记推翻终态);
//   - 交卷即查时延与事件序(不经收割轮);
//   - 砍除项零残留扫描。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p502Base 铺公共底座:任务 + 资产 + 交卷步骤(带 bash 工具记录:结果
// blob 含 marker)——直连形态(URL 引用)的三查输入。
func p502Base(t *testing.T, task, claimID, url, marker string) (*rings.Store, *swarm.Executor) {
	t.Helper()
	store := openStore(t)
	mkTask(t, store, task)
	p462SeedAsset(t, store, task, "a-t", "t.example:8080")
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "发现步骤", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: claimID + " 存在性陈述",
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1",
				"standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: "a-t"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: "s1"}},
	); err != nil {
		t.Fatalf("铺底座失败: %v", err)
	}
	ex := swarm.NewExecutor(store, nil, t.TempDir(), swarm.Budget{})
	result := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" +
		`{"data":{"phone":"` + marker + `"}}`
	ref, err := ex.Blobs.Write([]byte(result))
	if err != nil {
		t.Fatalf("写结果 blob 失败: %v", err)
	}
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
		Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall,
			ToolName: "bash", ToolArgs: json.RawMessage(`{"command":"curl -sS -i -m 5 ` + url + `"}`),
			ResultRef: ref},
	}); err != nil {
		t.Fatalf("铺工具记录失败: %v", err)
	}
	return store, ex
}

// p502Submit 落一条交换单(新交;P5-01 交卷单事件形态)。
func p502Submit(t *testing.T, store *rings.Store, task, claimID, stepID, url, marker string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "swarm:u1",
		Payload: rings.TestClaimRecord{
			ClaimID: claimID, StepID: stepID, UnitID: "u1", Type: rings.TestClaimRecSubmit,
			Summary: claimID + " 声明", Severity: "high",
			EvidenceRefs: []string{url}, ProvingMarker: marker,
			CountercheckNote: "重放两次一致,排除缓存假象",
		},
	}); err != nil {
		t.Fatalf("铺交换单失败: %v", err)
	}
}

// TestP502CheckOnSubmitConfirms ①集成形态:直连 URL 引用命中工具记录、
// marker 在结果原文 → 三查全过确认,复现包同批落账(确认 ⟙ 必有包),
// 事件序紧邻 submit(不经收割轮)。
func TestP502CheckOnSubmitConfirms(t *testing.T) {
	const task, claim = "p502-confirm", "tc-c"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	p502Submit(t, store, task, claim, "s1", url, "13800138000")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()

	start := time.Now()
	out, err := l.CheckClaimOnSubmit(ctx, task, claim)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("交卷即查失败: %v", err)
	}
	if out.Status != rings.TestClaimConfirmed {
		t.Fatalf("真证据应三查全过确认: %s 原因=%q", out.Status, out.Reason)
	}
	// ⑤时延:毫秒-秒级(纯代码,零 LLM 路径下毫秒)。
	if elapsed > 10*time.Second {
		t.Fatalf("交卷即查应毫秒-秒级: %v", elapsed)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("声明应已确认: %s", node.Status)
	}
	// 确认 ⟙ 必有包:repro 同批落账且含种子(工具记录组包)。
	rec := p473LatestRepro(t, store, task, claim)
	if rec == nil || len(rec.Steps) == 0 {
		t.Fatalf("确认必须同批落复现包(种子在内): %+v", rec)
	}
	if rec.Severity != "high" {
		t.Fatalf("定稿级别应取交换单自报级别: %s", rec.Severity)
	}
	// ⑤事件序:submit 与 verdict 之间零收割轮事件(紧邻或同批)。
	evs, _ := store.Events(ctx, task, 0, 0)
	subRev, verdictRev := int64(0), int64(0)
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claim {
			continue
		}
		switch r.Type {
		case rings.TestClaimRecSubmit:
			subRev = ev.Revision
		case rings.TestClaimRecVerdict:
			verdictRev = ev.Revision
		}
	}
	if subRev == 0 || verdictRev == 0 || verdictRev <= subRev {
		t.Fatalf("verdict 应紧随 submit(事件序可证不经收割轮): sub=%d verdict=%d", subRev, verdictRev)
	}
	// ①集成形态的拒收分支:悬空引用(URL 无工具记录命中)。
	const ghost = "p502-ghost"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: ghost, NodeType: rings.NodeTestClaim, Title: ghost, Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: ghost, ToNode: "a-t"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u1", Payload: rings.TestClaimRecord{
			ClaimID: ghost, StepID: "s1", UnitID: "u1", Type: rings.TestClaimRecSubmit,
			Summary: ghost, Severity: "high", EvidenceRefs: []string{"http://t.example:8080/none"},
			ProvingMarker: "x", CountercheckNote: "自查",
		}},
	); err != nil {
		t.Fatal(err)
	}
	out2, err := l.CheckClaimOnSubmit(ctx, task, ghost)
	if err != nil {
		t.Fatalf("悬空引用三查失败: %v", err)
	}
	if out2.Status != rings.TestClaimCandidate || out2.Reason != arbiter.ReasonUntraceableEvidence {
		t.Fatalf("引用不命中应拒并注入补考反馈: %+v", out2)
	}
}

// TestP502RetakeThenConfirm ②补考链路:三查不过 → retake 裁决+反馈注入+
// 派补考步骤(队首档) → 重交(同单第二单) → 三查确认;补考步骤收场。
func TestP502RetakeThenConfirm(t *testing.T) {
	const task, claim = "p502-retake", "tc-r"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	// 首交:marker 抄错(不在响应体)→ 查一不过。
	p502Submit(t, store, task, claim, "s1", url, "WRONG-MARKER")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()

	out, err := l.CheckClaimOnSubmit(ctx, task, claim)
	if err != nil || out.Status != rings.TestClaimCandidate {
		t.Fatalf("marker 不在响应体应首败回候选: %+v err=%v", out, err)
	}
	if out.Reason != arbiter.ReasonUntraceableEvidence {
		t.Fatalf("失败原因应为证明物不匹配: %q", out.Reason)
	}
	// retake 裁决落账 + 补考步骤已派(队首档 detail.kind=retake+reason 注入)。
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimCandidate {
		t.Fatalf("首败后声明应留候选等补考: %s", node.Status)
	}
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	var retakeStep *rings.NodeFull
	for i := range steps {
		if k, _ := steps[i].Detail["kind"].(string); k == retakeStepKind {
			retakeStep = &steps[i]
		}
	}
	if retakeStep == nil {
		t.Fatalf("首败应派补考步骤: %+v", steps)
	}
	if cid, _ := retakeStep.Detail["claim_id"].(string); cid != claim {
		t.Fatalf("补考步骤应绑定声明: %+v", retakeStep.Detail)
	}
	if r, _ := retakeStep.Detail["reason"].(string); r != arbiter.ReasonUntraceableEvidence {
		t.Fatalf("失败原因应注入补考步骤(续跑首条指令): %q", r)
	}
	if os, _ := retakeStep.Detail["origin_step"].(string); os != "s1" {
		t.Fatalf("补考步骤应记原交卷步骤(原会话续跑): %q", os)
	}
	// 排队档位:补考步骤与 scope_head 同档压普通步骤前(StepLaunchLess)。
	plain := rings.NodeFull{NodeView: rings.NodeView{ID: "s-plain"}, Detail: map[string]any{}}
	if !StepLaunchLess(*retakeStep, plain) {
		t.Fatalf("补考步骤应压普通排队步骤之前(队首档)")
	}
	// 重交(补考:同单第二单,marker 修正) → 确认;补考步骤收场。
	p502Submit(t, store, task, claim, "s1", url, "13800138000")
	out2, err := l.CheckClaimOnSubmit(ctx, task, claim)
	if err != nil || out2.Status != rings.TestClaimConfirmed {
		t.Fatalf("补考重交应确认: %+v err=%v", out2, err)
	}
	if st, _ := store.Node(ctx, task, retakeStep.ID); st.Status != "cancelled" {
		t.Fatalf("确认后排队补考步骤应收场: %s", st.Status)
	}
}

// TestP502RetakeLimitInsufficient ②上限:补考重交仍不过 → 证据不足终态;
// 再交被拒(受控入口补考上限)。
func TestP502RetakeLimitInsufficient(t *testing.T) {
	const task, claim = "p502-limit", "tc-l"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	p502Submit(t, store, task, claim, "s1", url, "WRONG-1")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()
	if out, err := l.CheckClaimOnSubmit(ctx, task, claim); err != nil || out.Status != rings.TestClaimCandidate {
		t.Fatalf("首败: %+v %v", out, err)
	}
	// 补考重交仍错 → 证据不足终态。
	p502Submit(t, store, task, claim, "s1", url, "WRONG-2")
	out, err := l.CheckClaimOnSubmit(ctx, task, claim)
	if err != nil || out.Status != rings.TestClaimInsufficient {
		t.Fatalf("补考再败应落证据不足: %+v err=%v", out, err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimInsufficient {
		t.Fatalf("声明应为证据不足终态: %s", node.Status)
	}
	dgs, _ := store.ClaimDigests(ctx, task)
	if dgs[claim].PendingReason != arbiter.ReasonUntraceableEvidence {
		t.Fatalf("证据不足理由应入账: %q", dgs[claim].PendingReason)
	}
	// 受控入口侧(swarm)补考上限由 campaignSubmitCount 控制,这里钉
	// Repeated notification returns the durable result without another verdict.
	before, _ := store.CurrentRevision(ctx, task)
	if repeated, err := l.CheckClaimOnSubmit(ctx, task, claim); err != nil || repeated.Status != rings.TestClaimInsufficient {
		t.Fatalf("终态重复通知未返回原结果: %+v %v", repeated, err)
	}
	if after, _ := store.CurrentRevision(ctx, task); after != before {
		t.Fatal("终态重复通知产生了新裁决")
	}
}

// TestP502RetakeNoResubmitInsufficient ②兜底:补考步骤终态而无重交 →
// 收割兜底落证据不足("补考未重交")。
func TestP502RetakeNoResubmitInsufficient(t *testing.T) {
	const task, claim = "p502-noresub", "tc-n"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	p502Submit(t, store, task, claim, "s1", url, "WRONG")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()
	if _, err := l.CheckClaimOnSubmit(ctx, task, claim); err != nil {
		t.Fatal(err)
	}
	// 补考步骤被领走跑完(终态 done)而模型没重交。
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	for _, s := range steps {
		if k, _ := s.Detail["kind"].(string); k == retakeStepKind {
			if _, err := store.Append(ctx, task, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
				Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "running"},
			}, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
				Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "done"},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := l.checkPendingClaims(ctx)
	if err != nil || n != 1 {
		t.Fatalf("收割兜底应收口补考未重交: n=%d err=%v", n, err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimInsufficient {
		t.Fatalf("补考未重交应落证据不足: %s", node.Status)
	}
	dgs, _ := store.ClaimDigests(ctx, task)
	if dgs[claim].PendingReason != "补考未重交" {
		t.Fatalf("理由应逐字'补考未重交': %q", dgs[claim].PendingReason)
	}
}

// TestP502ReopenReverifyRefuted ④reopen 重验推翻:reopen 派重验步骤,
// 步骤终态而无新交单 → 收割兜底记推翻终态(账本留痕,显示层移出漏洞页
// =状态 refuted 不进成果集)。
func TestP502ReopenReverifyRefuted(t *testing.T) {
	const task, claim = "p502-reverify", "tc-v"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	p502Submit(t, store, task, claim, "s1", url, "13800138000")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()
	if _, err := l.CheckClaimOnSubmit(ctx, task, claim); err != nil {
		t.Fatal(err)
	}
	// reopen(新反证)→ 回候选 + 派重验步骤。
	if err := ReopenClaim(ctx, store, task, claim, rings.ReopenNewEvidence, "靶标已修复,须重验", "creator"); err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	var reverify string
	for _, s := range steps {
		if k, _ := s.Detail["kind"].(string); k == retakeStepKind {
			if rv, _ := s.Detail["reverify"].(bool); rv {
				reverify = s.ID
			}
		}
	}
	if reverify == "" {
		t.Fatalf("reopen 应派重验步骤(reverify)")
	}
	// 重验单位跑完未再交卷(立不住)→ 收割兜底记推翻终态。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: reverify, Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: reverify, Status: "done"}},
	); err != nil {
		t.Fatal(err)
	}
	if n, err := l.checkPendingClaims(ctx); err != nil || n != 1 {
		t.Fatalf("收割兜底应收口重验未再坐实: n=%d err=%v", n, err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimRefuted {
		t.Fatalf("重验未再坐实应记推翻终态: %s", node.Status)
	}
	// 推翻终态留痕:verdict(refuted) 在案;词面"被推翻"(P5-07⑧ 换词,
	// 显示层移出漏洞页)。
	vs := p473Events(t, store, task, rings.TestClaimRecVerdict, claim)
	found := false
	for _, v := range vs {
		if v.Decision == rings.VerdictRefuted {
			found = true
		}
	}
	if !found {
		t.Fatalf("账本应记推翻裁决: %+v", vs)
	}
	if w := rings.ClaimStatusWord(node.Status); w != "被推翻" {
		t.Fatalf("推翻终态显示词: %q", w)
	}
}

// TestP502CheckWindowRescue ②兜底:交卷即查失败窗口(submit 落账而三查
// 未跑)→ 收割兜底重查(幂等,确认可达)。
func TestP502CheckWindowRescue(t *testing.T) {
	const task, claim = "p502-rescue", "tc-w"
	const url = "http://t.example:8080/api/user?phone=222"
	store, ex := p502Base(t, task, claim, url, "13800138000")
	p502Submit(t, store, task, claim, "s1", url, "13800138000")
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	ctx := context.Background()
	n, err := l.checkPendingClaims(ctx)
	if err != nil || n != 1 {
		t.Fatalf("交卷未查应由兜底重查: n=%d err=%v", n, err)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("兜底重查应完成确认: %s", node.Status)
	}
}

// TestP502RemovedSymbolsScan ⑥砍除项扫描断言:提交契约与判定代码中
// alternatives/impact_supported/verifier_independent 零残留(类级:全
// 提交契约与判定文件逐个扫,白名单=旧账本读容错注释)。
func TestP502RemovedSymbolsScan(t *testing.T) {
	files := []string{
		// 提交契约。
		"internal/swarm/tools.go",
		"internal/swarm/prompt.go",
		"internal/swarm/promptspec.go",
		"internal/swarm/pack.go",
		"internal/rings/event.go",
		"internal/rings/project.go",
		// 判定代码。
		"internal/arbiter/claim.go",
		"internal/arbiter/doc.go",
		"internal/overmind/claimcheck.go",
		"internal/overmind/claimnarrative.go",
	}
	// 允许出现的语境:旧账本读容错/词表退役注释(中文说明,不是代码
	// 标识符或 JSON 字段)。
	allow := func(line string) bool {
		return strings.Contains(line, "旧") || strings.Contains(line, "已随") ||
			strings.Contains(line, "已删") || strings.Contains(line, "退役") ||
			strings.Contains(line, "砍除") || strings.Contains(line, "不再产生") ||
			strings.Contains(line, "保留解码") || strings.Contains(line, "P5-01③") ||
			strings.Contains(line, "P5-02⑥") || strings.Contains(line, "容错")
	}
	for _, f := range files {
		path := filepath.Join("..", "..", f)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, sym := range []string{"alternatives", "impact_supported", "verifier_independent"} {
				if strings.Contains(line, sym) && !allow(line) {
					t.Errorf("%s:%d 残留砍除项 %q: %s", f, i+1, sym, strings.TrimSpace(line))
				}
			}
		}
	}
}
