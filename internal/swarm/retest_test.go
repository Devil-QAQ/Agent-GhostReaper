package swarm

// P3-06 复测会话测试:受限工具集(无声明参数,串号在结构上不可能)、
// 结论写回与状态迁移(fixed / unfixed 回滚 / inconclusive 只入记录)、
// 每轮一次、发起前置校验与并发互斥、悬空会话收口、脚本模型引擎全链路
// (真实 HTTP/SSE 会话 + bash 流量经镜留痕归因到复测锚)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
)

// seedRetestableClaim 种一条可复测图:任务 + 资产 + 已确认声明(经
// candidate→testing→confirmed 状态机)+ 收割定稿(复现步骤两条)。
// 资产 ID 随声明派生(同任务多次种图不撞节点)。
func seedRetestableClaim(t *testing.T, store *rings.Store, task, claimID, statement string) {
	t.Helper()
	ctx := context.Background()
	assetID := "a-" + claimID
	_, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: assetID, NodeType: rings.NodeAsset, Title: "http://靶标", Status: "active",
			AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: statement,
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{
				"impact": "可读全部用户数据", "vulnclass": "信息暴露",
				"unit_id": "u1", "step_id": "s1", "transcript_id": "swarm-u1",
				"evidence": "GET /api/user 200 全量用户",
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: assetID}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: claimID, Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: claimID, Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: claimID, Type: rings.TestClaimRecVerdict, Decision: rings.VerdictConfirm,
			Satisfied: true, Conditions: []rings.ArbiterConditionResult{{Kind: "falsify_completed", Satisfied: true}}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: claimID, Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{
				{Seq: 1, Desc: "请求用户接口", Op: "bash curl /api/user", Method: "GET",
					URL: "http://靶标/api/user", Status: 200},
				{Seq: 2, Desc: "读取返回", Op: "read out.json", Method: "GET",
					URL: "http://靶标/api/user", Status: 200},
			}}},
	)
	if err != nil {
		t.Fatalf("种图失败: %v", err)
	}
}

// retestRecordsOf 声明的全部 retest 记录(入账序)。
func retestRecordsOf(t *testing.T, store *rings.Store, task, claimID string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.Type == rings.TestClaimRecRetest && rec.ClaimID == claimID {
			out = append(out, rec)
		}
	}
	return out
}

// newRetestEnv 测试内构建绑定了复测会话的单位环境。
func newRetestEnv(t *testing.T, dir, task, claimID string, round int) (*Executor, *unitEnv) {
	t.Helper()
	store := openStore(t, dir)
	seedRetestableClaim(t, store, task, claimID, "用户接口未授权可读")
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{
		Executor: ex, TaskID: task, StepID: RetestSID(claimID, round),
		UnitID: "rt-test1", TranscriptID: RetestSID(claimID, round),
		retest: &retestBinding{ClaimID: claimID, Round: round, SID: RetestSID(claimID, round)},
	}
	return ex, env
}

// TestRetestToolsetRestricted 受限工具集:复测单位只有 bash/只读探测/
// read + submit_retest_result——无候选回流、无声明提交、无假设通道;
// 结论工具参数无声明 ID(服务端定死,串号在结构上不可能)。
func TestRetestToolsetRestricted(t *testing.T) {
	dir := t.TempDir()
	_, env := newRetestEnv(t, dir, "retest-tools", "tc-a1", 1)
	got := map[string]bool{}
	for _, tl := range env.toolset() {
		got[tl.Name()] = true
	}
	want := []string{"bash", "port_probe", "http_probe", "read", "submit_retest_result"}
	if len(got) != len(want) {
		t.Fatalf("受限集应恰 %d 只,得到 %v", len(want), got)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("受限集缺 %s:%v", w, got)
		}
	}
	for _, banned := range []string{
		"submit_candidate_fact", "submit_test_claim", "submit_hypothesis",
		"submit_falsify_result", "web_search", "web_fetch", "list_claims",
	} {
		if got[banned] {
			t.Fatalf("复测单位不得携带 %s(复测不是发现通道)", banned)
		}
	}
	// 结论工具 schema 只暴露 verdict/evidence/note——无声明 ID 参数。
	var submit tool.CoreTool
	for _, tl := range env.toolset() {
		if tl.Name() == "submit_retest_result" {
			submit = tl
		}
	}
	if submit == nil {
		t.Fatalf("缺 submit_retest_result")
	}
	raw, _ := json.Marshal(submit.InputSchema())
	for _, banned := range []string{"claim_id", "claim", "testclaim"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("结论工具参数不得含声明标识 %q:%s", banned, raw)
		}
	}
	for _, need := range []string{"verdict", "evidence"} {
		if !strings.Contains(string(raw), need) {
			t.Fatalf("结论工具缺参数 %q:%s", need, raw)
		}
	}
}

// TestSubmitRetestResultVerdicts 结论三值的写回与状态迁移:
// fixed→fixed;fixed 后 unfixed→回滚到修复前(最近非 fixed 态);
// inconclusive 只入记录不动状态;每轮一次。
func TestSubmitRetestResultVerdicts(t *testing.T) {
	dir := t.TempDir()
	const task, claim = "retest-verdicts", "tc-v1"
	ex, env := newRetestEnv(t, dir, task, claim, 1)
	ctx := context.Background()

	// 第 1 轮:fixed → 状态迁 fixed。
	if err := env.SubmitRetestResult(ctx, rings.RetestVerdictFixed, "重放 GET /api/user 返回 403,漏洞不再可触发", ""); err != nil {
		t.Fatalf("第 1 轮提交失败: %v", err)
	}
	node, err := ex.Store.Node(ctx, task, claim)
	if err != nil {
		t.Fatalf("读声明: %v", err)
	}
	if node.Status != rings.TestClaimFixed {
		t.Fatalf("fixed 结论后状态应 fixed,得到 %s", node.Status)
	}
	// 同轮二次提交被拒(每轮一次,R1)。
	if err := env.SubmitRetestResult(ctx, rings.RetestVerdictUnfixed, "再提一次", ""); err == nil {
		t.Fatalf("同轮二次提交应被拒")
	}

	// 第 2 轮:inconclusive → 只入记录,状态不动(fixed)。
	env2 := &unitEnv{Executor: ex, TaskID: task, StepID: RetestSID(claim, 2),
		UnitID: "rt-test2", TranscriptID: RetestSID(claim, 2),
		retest: &retestBinding{ClaimID: claim, Round: 2, SID: RetestSID(claim, 2)}}
	if err := env2.SubmitRetestResult(ctx, rings.RetestVerdictInconclusive, "目标不可达,未能重放", "超时"); err != nil {
		t.Fatalf("inconclusive 提交失败: %v", err)
	}
	node, _ = ex.Store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimFixed {
		t.Fatalf("inconclusive 不得动状态,得到 %s", node.Status)
	}

	// 第 3 轮:unfixed(修复回归)→ 回滚到修复前最近非 fixed 态(confirmed)。
	env3 := &unitEnv{Executor: ex, TaskID: task, StepID: RetestSID(claim, 3),
		UnitID: "rt-test3", TranscriptID: RetestSID(claim, 3),
		retest: &retestBinding{ClaimID: claim, Round: 3, SID: RetestSID(claim, 3)}}
	if err := env3.SubmitRetestResult(ctx, rings.RetestVerdictUnfixed, "重放 GET /api/user 返回 200 全量用户,漏洞仍在", ""); err != nil {
		t.Fatalf("第 3 轮提交失败: %v", err)
	}
	node, _ = ex.Store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimConfirmed {
		t.Fatalf("unfixed 应回滚到 confirmed,得到 %s", node.Status)
	}

	recs := retestRecordsOf(t, ex.Store, task, claim)
	if len(recs) != 3 {
		t.Fatalf("应恰 3 条 retest 记录,得到 %d", len(recs))
	}
	for i, want := range []string{rings.RetestVerdictFixed, rings.RetestVerdictInconclusive, rings.RetestVerdictUnfixed} {
		if recs[i].RetestVerdict != want || recs[i].Round != i+1 {
			t.Fatalf("第 %d 条记录不符:%+v", i+1, recs[i])
		}
		if recs[i].StepID != RetestSID(claim, i+1) || recs[i].UnitID == "" {
			t.Fatalf("第 %d 条记录缺会话锚/单位:%+v", i+1, recs[i])
		}
		if recs[i].EvidenceHead == "" && recs[i].EvidenceRef == "" {
			t.Fatalf("第 %d 条记录缺证据", i+1)
		}
	}
}

// TestSubmitRetestResultGuards 写回入口的拒绝线:结论词受限、证据非空、
// 非复测单位、锤炼中状态拒绝、串号防护(绑定 A 只写 A,B 不动)。
func TestSubmitRetestResultGuards(t *testing.T) {
	dir := t.TempDir()
	const task = "retest-guards"
	ex, env := newRetestEnv(t, dir, task, "tc-g1", 1)
	// 另一条声明(串号防护的对照):B 保持不动。
	seedRetestableClaim(t, ex.Store, task, "tc-g2", "另一条声明")
	ctx := context.Background()

	if err := env.SubmitRetestResult(ctx, "maybe", "证据", ""); err == nil {
		t.Fatalf("结论词受限:maybe 应被拒")
	}
	if err := env.SubmitRetestResult(ctx, rings.RetestVerdictFixed, "  ", ""); err == nil {
		t.Fatalf("空证据应被拒")
	}
	plain := &unitEnv{Executor: ex, TaskID: task, StepID: "s9", UnitID: "u9"}
	if err := plain.SubmitRetestResult(ctx, rings.RetestVerdictFixed, "证据", ""); err == nil {
		t.Fatalf("非复测单位无结论通道,应被拒")
	}
	// 锤炼中(candidate)拒绝写回:把 A 打回 candidate 再试。
	if _, err := ex.Store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "tc-g1", Status: rings.TestClaimCandidate},
	}); err != nil {
		t.Fatalf("改状态: %v", err)
	}
	if err := env.SubmitRetestResult(ctx, rings.RetestVerdictFixed, "证据", ""); err == nil {
		t.Fatalf("锤炼中状态应拒绝复测写回")
	}
	// 串号防护:绑定 g1 的单位成功写回,g2 名下零 retest 记录且状态不动。
	if _, err := ex.Store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "tc-g1", Status: rings.TestClaimConfirmed},
	}); err != nil {
		t.Fatalf("改回确认: %v", err)
	}
	if err := env.SubmitRetestResult(ctx, rings.RetestVerdictFixed, "重放显示已不可触发", ""); err != nil {
		t.Fatalf("合法提交失败: %v", err)
	}
	if got := retestRecordsOf(t, ex.Store, task, "tc-g2"); len(got) != 0 {
		t.Fatalf("串号防护失败:g2 名下出现复测记录 %v", got)
	}
	if b, err := ex.Store.Node(ctx, task, "tc-g2"); err != nil || b.Status != rings.TestClaimConfirmed {
		t.Fatalf("g2 状态应不动: %s %v", b.Status, err)
	}
}

// TestBeginRetestGuardsAndRounds 发起前置校验与轮次:不可复测状态拒绝、
// 依赖缺失拒绝、并发互斥(同声明一轮)、完成后再发起轮次递增、
// 悬空会话(上一进程残留)收口后照常发起。
func TestBeginRetestGuardsAndRounds(t *testing.T) {
	dir := t.TempDir()
	const task, claim = "retest-begin", "tc-b1"
	store := openStore(t, dir)
	seedRetestableClaim(t, store, task, claim, "声明一")
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ctx := context.Background()

	// 依赖缺失(无镜):ErrRetestUnavailable(状态先过——已确认的声明
	// 才走到依赖检查)。
	if _, err := ex.BeginRetest(ctx, task, claim); !errors.Is(err, ErrRetestUnavailable) {
		t.Fatalf("无镜应 ErrRetestUnavailable,得到 %v", err)
	}
	idle := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "(不驱动)", Finish: "stop"}
	})
	defer idle.Close()
	idleProv, err := idle.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	ex.Provider = idleProv
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())

	// 不可复测状态(candidate):拒绝。
	if _, err := ex.Store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: claim, Status: rings.TestClaimCandidate},
	}); err != nil {
		t.Fatalf("改状态: %v", err)
	}
	if _, err := ex.BeginRetest(ctx, task, claim); !errors.Is(err, ErrRetestNotRetestable) {
		t.Fatalf("candidate 应 ErrRetestNotRetestable,得到 %v", err)
	}
	if _, err := ex.Store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: claim, Status: rings.TestClaimConfirmed},
	}); err != nil {
		t.Fatalf("改回确认: %v", err)
	}

	// 第 1 轮起飞:上下文与锚齐备;未落地前并发发起被拒。
	run1, err := ex.BeginRetest(ctx, task, claim)
	if err != nil {
		t.Fatalf("发起第 1 轮失败: %v", err)
	}
	if run1.Round() != 1 || run1.SID() != RetestSID(claim, 1) {
		t.Fatalf("第 1 轮锚不符: %d %s", run1.Round(), run1.SID())
	}
	if !strings.Contains(run1.env.retest.firstMessage, "存在性判断: 声明一") ||
		!strings.Contains(run1.env.retest.firstMessage, "复现步骤") ||
		!strings.Contains(run1.env.retest.firstMessage, "请求用户接口") {
		t.Fatalf("自动首条消息缺判断/复现步骤上下文:\n%s", run1.env.retest.firstMessage)
	}
	if _, err := ex.BeginRetest(ctx, task, claim); !errors.Is(err, ErrRetestInFlight) {
		t.Fatalf("并发发起应 ErrRetestInFlight,得到 %v", err)
	}
	if !ex.RetestRunning(task, claim) {
		t.Fatalf("活动登记应为真")
	}

	// 悬空收口(进程重启语义):另造一条上一进程的悬空会话(不在活动
	// 登记),再以同一账本起新执行器(活动登记清零 = 重启)发起——
	// 第 1 轮(未落地)与悬空轮都被收口留痕,轮次计入全部历史轮。
	if _, err := ex.Store.Append(ctx, task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:rt-old",
		Payload: rings.SwarmRecord{StepID: RetestSID(claim, 9), UnitID: "rt-old",
			TranscriptID: RetestSID(claim, 9), Type: rings.SwarmRecSessionStarted},
	}); err != nil {
		t.Fatalf("造悬空会话: %v", err)
	}
	restarted := NewExecutor(store, nil, filepath.Join(dir, "tasks2"), Budget{})
	restarted.Mirror = ex.Mirror
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "(不驱动)", Finish: "stop"}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	restarted.Provider = prov
	run2, err := restarted.BeginRetest(ctx, task, claim)
	if err != nil {
		t.Fatalf("重启后发起失败: %v", err)
	}
	// 轮次 = 历史会话数(第 1 轮 + 悬空第 9 轮)+ 1 = 3。
	if run2.Round() != 3 {
		t.Fatalf("轮次应计入全部历史轮(2 存在过 +1),得到 %d", run2.Round())
	}
	closed1, closed9 := false, false
	for _, r := range swarmRecords(t, store, task) {
		if r.Type != rings.SwarmRecSessionEnd || !strings.Contains(r.Note, "复测中断") {
			continue
		}
		if r.StepID == RetestSID(claim, 1) {
			closed1 = true
		}
		if r.StepID == RetestSID(claim, 9) {
			closed9 = true
		}
	}
	if !closed1 || !closed9 {
		t.Fatalf("悬空会话应被收口留痕: r1=%v r9=%v", closed1, closed9)
	}
}

// TestRunRetestScriptedE2E 复测会话引擎全链路:脚本模型(真实 HTTP/SSE)
// 驱动受限会话——先 bash 重放(流量经镜归因复测锚),再 submit_retest_result
// (fixed),自然收场;转录首条消息即自动复测上下文。
func TestRunRetestScriptedE2E(t *testing.T) {
	dir := t.TempDir()
	const task, claim = "retest-e2e", "tc-e1"
	store := openStore(t, dir)
	seedRetestableClaim(t, store, task, claim, "靶标 robots 路径可读")
	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", "Disallow"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "bash",
				Args: fmt.Sprintf(`{"command":%q}`, "curl -s "+target.MainURL()+"/robots.txt"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_retest_result", "已记录"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "submit_retest_result",
				Args: `{"verdict":"fixed","evidence":"重放 curl 返回 403,原文:Forbidden","note":"重放一次完成"}`,
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "复测完成:漏洞已不可触发,结论已写回。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())
	ctx := context.Background()

	run, err := ex.BeginRetest(ctx, task, claim)
	if err != nil {
		t.Fatalf("发起复测失败: %v", err)
	}
	res, err := ex.DriveRetest(ctx, run)
	if err != nil {
		t.Fatalf("驱动复测失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("复测应完成收场: %+v", res)
	}

	// 会话事件:started/finished 成对,StepID=TranscriptID=复测锚。
	srecs := swarmRecords(t, store, task)
	started, finished := false, false
	for _, r := range srecs {
		if r.StepID != RetestSID(claim, 1) {
			continue
		}
		if r.Type == rings.SwarmRecSessionStarted {
			started = true
			if !strings.Contains(r.Note, "复测第 1 轮") || !strings.HasPrefix(r.UnitID, "rt-") {
				t.Fatalf("session_started 注记不符: %+v", r)
			}
		}
		if r.Type == rings.SwarmRecSessionEnd {
			finished = true
		}
	}
	if !started || !finished {
		t.Fatalf("复测会话事件不齐: started=%v finished=%v", started, finished)
	}

	// 结论入账 + 状态迁移。
	recs := retestRecordsOf(t, store, task, claim)
	if len(recs) != 1 || recs[0].RetestVerdict != rings.RetestVerdictFixed {
		t.Fatalf("应恰 1 条 fixed 记录: %+v", recs)
	}
	node, _ := store.Node(ctx, task, claim)
	if node.Status != rings.TestClaimFixed {
		t.Fatalf("状态应 fixed,得到 %s", node.Status)
	}

	// 转录落盘,首条消息是自动复测上下文。
	tpath := filepath.Join(dir, "tasks", task, "transcripts", RetestSID(claim, 1)+".jsonl")
	raw, err := os.ReadFile(tpath)
	if err != nil {
		t.Fatalf("读转录: %v", err)
	}
	if !strings.Contains(string(raw), "复测上下文") || !strings.Contains(string(raw), "靶标 robots 路径可读") {
		t.Fatalf("转录首条消息应为复测上下文(含声明):\n%s", clipLine(string(raw), 400))
	}

	// 流量留痕:重放流量经镜,归因到复测锚(StepID=SID)。
	evs, _ := store.Events(ctx, task, 0, 0)
	mirrorHits := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if json.Unmarshal(ev.Payload, &m) == nil && m.StepID == RetestSID(claim, 1) {
			mirrorHits++
		}
	}
	if mirrorHits == 0 {
		t.Fatalf("复测重放流量应经镜留痕并归因到复测锚")
	}
	if ex.RetestRunning(task, claim) {
		t.Fatalf("落地后活动登记应注销")
	}
}

// TestRetestPromptSpecCarriesStyle P3-11a ①:复测单位会话带常驻文风段
// (复测叙述/结论说明按人话写,submit_retest_result 参数照契约)。
func TestRetestPromptSpecCarriesStyle(t *testing.T) {
	found := false
	for _, seg := range RetestPromptSpec.Fixed {
		if seg == ResidentStyleRules {
			found = true
		}
	}
	if !found {
		t.Fatalf("复测基线固定段须含常驻文风条款(ResidentStyleRules)")
	}
	segs := RetestSystemSegments(context.Background(), nil, RoE{}, &retestBinding{ClaimID: "tc-x", Round: 1})
	joined := strings.Join(segs, "\n")
	if !strings.Contains(joined, "结构化输出照契约") {
		t.Fatalf("复测 system 须含结构化照契约作用域行(契约零破坏)")
	}
}
