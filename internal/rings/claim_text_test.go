package rings

import (
	"context"
	"reflect"
	"testing"
)

func TestContentClaimTextChangesOnlyDisplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-copy"
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{NodeID: "tc-1", NodeType: NodeTestClaim, Title: "原始存在性判断", Status: TestClaimConfirmed, Detail: map[string]any{"impact": "原始影响"}}},
		Event{Kind: EventTestClaimRecord, Actor: "overmind", Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecRepro, Statement: "原始定稿说明", Impact: "已核实的影响", Severity: "medium", Facts: []FactStatement{{FactID: "f-1", Statement: "已有事实说明"}}}})
	original, _ := s.Node(ctx, task, "tc-1")
	before, _, _ := s.ClaimCaseOf(ctx, task, "tc-1")
	source, err := s.ClaimTextSourceRev(ctx, task, "tc-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"备份无需登录可读", "备份访问限制缺失"} {
		mustAppend(t, s, task, Event{Kind: EventClaimText, Actor: "overmind", Payload: ClaimTextPolish{ClaimID: "tc-1", SourceRev: source, Title: title, Statement: "未登录时可以读取这份备份。", Impact: "备份中的配置可被读取。"}})
		row, _, _ := s.ClaimCaseOf(ctx, task, "tc-1")
		node, _ := s.Node(ctx, task, "tc-1")
		if row.Summary.Title != title || !reflect.DeepEqual(row.Repro, before.Repro) || !reflect.DeepEqual(row.Evidence, before.Evidence) || !reflect.DeepEqual(row.Timeline, before.Timeline) || !reflect.DeepEqual(original, node) || !reflect.DeepEqual(row.Summary.Facts, before.Summary.Facts) {
			t.Fatal("显示稿改变了证据、业务状态或来源")
		}
		actual, e := s.ClaimTextSourceRev(ctx, task, "tc-1")
		if e != nil || actual != source {
			t.Fatal("显示稿抬高了业务依据版本")
		}
	}
	row, _, _ := s.ClaimCaseOf(ctx, task, "tc-1")
	if err = s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, _, _ := s.ClaimCaseOf(ctx, task, "tc-1")
	if !reflect.DeepEqual(row, replayed) {
		t.Fatal("显示稿重放不一致")
	}
	mustAppend(t, s, task, Event{Kind: EventNodeStateChanged, Actor: "arbiter", Payload: NodeStateChanged{NodeID: "tc-1", Status: TestClaimFixed}})
	rev, _ := s.CurrentRevision(ctx, task)
	for _, p := range []ClaimTextPolish{
		{ClaimID: "tc-1", SourceRev: source, Title: "过期稿", Statement: "旧结论"},
		{ClaimID: "missing", SourceRev: source, Title: "错误对象", Statement: "说明"},
		{ClaimID: "tc-1", SourceRev: 0, Title: "缺依据", Statement: "说明"},
	} {
		if _, e := s.Append(ctx, task, Event{Kind: EventClaimText, Actor: "overmind", Payload: p}); e == nil {
			t.Fatalf("应拒绝无依据或过期稿: %+v", p)
		}
	}
	after, _ := s.CurrentRevision(ctx, task)
	if after != rev {
		t.Fatal("拒绝的显示稿留下了事件")
	}
}
