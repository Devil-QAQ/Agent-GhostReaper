package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

func TestSubmitClaimProvenanceAtomic(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "submit-sources"
	seedGraph(t, store, task, "组合验证")
	_, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "fact", NodeType: rings.NodeFact, Title: "确认事实", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "prior", NodeType: rings.NodeTestClaim, Title: "已确认漏洞", Status: rings.TestClaimConfirmed}},
	)
	if err != nil {
		t.Fatal(err)
	}
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	submit := func(title string, refs ...string) (string, error) {
		id, _, e := env.SubmitClaim(ctx, title, "high", []string{"ms-source.1"}, "proof", "对照验证通过", "影响证据", "target", "信息暴露", "", refs...)
		return id, e
	}
	before, _ := store.CurrentRevision(ctx, task)
	if _, err = submit("无效来源", "missing"); err == nil {
		t.Fatal("无效来源未拒绝")
	}
	after, _ := store.CurrentRevision(ctx, task)
	if before != after {
		t.Fatal("无效来源留下部分交卷")
	}
	id, err := submit("跨节点组合漏洞", "fact", "prior", "target", "fact")
	if err != nil {
		t.Fatal(err)
	}
	edges, err := store.Edges(ctx, task, rings.EdgeSourcedFrom)
	if err != nil || len(edges) != 3 {
		t.Fatalf("混合来源未去重完整入账: %v %v", edges, err)
	}
	for _, e := range edges {
		if e.From != id {
			t.Fatal("来源挂错交卷")
		}
	}
	records := claimRecords(t, store, task, rings.TestClaimRecSubmit)
	if len(records) != 1 || records[0].ClaimID != id || len(records[0].EvidenceRefs) != 1 {
		t.Fatal("来源记账改变交卷证据契约")
	}
}
