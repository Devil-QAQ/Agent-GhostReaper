package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-56 ⑤(问题清单 #80②):反证时间线人话三段 ----
//
// 确认定稿时对当前战役每轮反证冻结"疑点→怎么验的→结论";模型失败回退
// 确定性拼句(疑问句词形 + 结论词 已排除/未排除/推翻);解析宽容
// (剥围栏、按轮归位、漏答轮次保留拼句)。R17:读定稿零 LLM。

func appendLoopEvents(t *testing.T, s *rings.Store, task string, evs ...rings.Event) {
	t.Helper()
	if _, err := s.Append(context.Background(), task, evs...); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
}

func p456SeedClaimWithRounds(t *testing.T) (*rings.Store, string) {
	t.Helper()
	s := openLoopStore(t)
	const task = "p456-rounds"
	altDetail := map[string]any{
		"kind": "falsify", "testclaim_id": "tc-1", "round": 1.0,
	}
	altDetail2 := map[string]any{
		"kind": "falsify", "testclaim_id": "tc-1", "round": 2.0,
	}
	raw1, _ := json.Marshal(altDetail)
	raw2, _ := json.Marshal(altDetail2)
	d1, d2 := map[string]any{}, map[string]any{}
	_ = json.Unmarshal(raw1, &d1)
	_ = json.Unmarshal(raw2, &d2)
	appendLoopEvents(t, s, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "/old-backup/ 备份目录可公开列举",
			Status: rings.TestClaimTesting,
			Detail: map[string]any{
				"unit_id":      "u1",
				"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染"}, map[string]any{"id": "alt2", "label": "不可复现"}},
			}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-f1", NodeType: rings.NodeStep, Title: "反证第 1 轮", Status: "done", Detail: d1}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-f2", NodeType: rings.NodeStep, Title: "反证第 2 轮", Status: "done", Detail: d2}},
		// 第 1 轮:未全排除(alt1 排除,alt2 未排除);第 2 轮:全排除。
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecFalsify, StepID: "s-f1", UnitID: "u2", Round: 1,
			Alternatives: []rings.AltResult{
				{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: false}},
			EvidenceHead: "两次请求返回一致列举"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecFalsify, StepID: "s-f2", UnitID: "u3", Round: 2,
			Alternatives: []rings.AltResult{
				{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
			ExistenceRefuted: true,
			EvidenceHead:     "重放二十余次全 404"}},
	)
	return s, task
}

// TestP456GenRoundHumansFallback 零 Provider:确定性拼句冻结(疑问句词形
// + 结论词逐轮正确),素材按当前战役轮次装配。
func TestP456GenRoundHumansFallback(t *testing.T) {
	s, task := p456SeedClaimWithRounds(t)
	l := &Loop{Store: s, TaskID: task} // Provider nil:保留拼句,零外呼
	rec := &rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecRepro}
	l.genRoundHumans(context.Background(), "tc-1", rec)

	if len(rec.Rounds) != 2 {
		t.Fatalf("应冻结 2 轮人话,得 %d(%+v)", len(rec.Rounds), rec.Rounds)
	}
	r1 := rec.Rounds[0]
	if !strings.Contains(r1.Doubt, "会不会是「缓存污染」") || !strings.Contains(r1.Doubt, "会不会是「不可复现」") {
		t.Errorf("第 1 轮疑点应为疑问句逐条列出: %q", r1.Doubt)
	}
	if r1.Conclusion != "未排除" {
		t.Errorf("第 1 轮 alt2 未排除,结论应未排除: %q", r1.Conclusion)
	}
	r2 := rec.Rounds[1]
	if r2.Conclusion != "推翻" {
		t.Errorf("第 2 轮存在性被推翻,结论应推翻: %q", r2.Conclusion)
	}
	if !strings.Contains(r2.How, "u3") || !strings.Contains(r2.How, "重放二十余次全 404") {
		t.Errorf("第 2 轮怎么验的应含单位与证据: %q", r2.How)
	}
}

// TestP456ParseRoundHumans 解析宽容:围栏剥除、按轮归位、越界轮次丢弃、
// 空回复失败。
func TestP456ParseRoundHumans(t *testing.T) {
	got, err := parseRoundHumans("```json\n{\"rounds\":[{\"round\":1,\"doubt\":\"会不会是缓存?\",\"how\":\"两次一致\",\"conclusion\":\"已排除\"},{\"round\":9,\"doubt\":\"越界\"}]}\n```", 1)
	if err != nil {
		t.Fatalf("围栏解析失败: %v", err)
	}
	if len(got) != 1 || got[1].Doubt != "会不会是缓存?" || got[1].Conclusion != "已排除" {
		t.Fatalf("按轮归位不符: %+v", got)
	}
	if _, err := parseRoundHumans("不是 JSON", 1); err == nil {
		t.Fatal("坏回复应失败(调用方保留拼句)")
	}
	if _, err := parseRoundHumans(`{"rounds":[]}`, 1); err == nil {
		t.Fatal("空 rounds 应失败")
	}
}

// TestP456RoundHumanProjection repro 记录的 Rounds 随投影透出(面板与
// 报告读同一份,读定稿零 LLM)。
func TestP456RoundHumanProjection(t *testing.T) {
	s, task := p456SeedClaimWithRounds(t)
	appendLoopEvents(t, s, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Rounds: []rings.FalsifyRoundHuman{{
				Round: 1, Doubt: "会不会是缓存?", How: "两次一致", Conclusion: "已排除"}}}},
	)
	proj, ok, err := s.ClaimCaseOf(context.Background(), task, "tc-1")
	if err != nil || !ok {
		t.Fatalf("读卷宗: %v ok=%v", err, ok)
	}
	if proj.Repro == nil || len(proj.Repro.Rounds) != 1 || proj.Repro.Rounds[0].Doubt != "会不会是缓存?" {
		t.Fatalf("Rounds 未随投影透出: %+v", proj.Repro)
	}
}

// TestP456ParseRoundHumansGlobalRoundNumbers 重开战役的轮号是全局续号
// (战役内 1 轮、轮号 7):解析上界须取实际轮号(活体实锤——按战役内
// 序数过滤会把模型合法回复整体丢弃回退拼句)。
func TestP456ParseRoundHumansGlobalRoundNumbers(t *testing.T) {
	got, err := parseRoundHumans(
		`{"rounds":[{"round":7,"doubt":"会不会是演示数据?","how":"重放 200","conclusion":"已排除"}]}`, 7)
	if err != nil {
		t.Fatalf("全局轮号解析失败: %v", err)
	}
	if r, ok := got[7]; !ok || r.Conclusion != "已排除" {
		t.Fatalf("第 7 轮应按轮号归位: %+v", got)
	}
	// 上界之外仍丢弃(防模型编造轮次)
	if got2, err := parseRoundHumans(`{"rounds":[{"round":9,"doubt":"x"}]}`, 7); err == nil || len(got2) != 0 {
		t.Fatalf("越界轮号应丢弃: %v %v", got2, err)
	}
}
