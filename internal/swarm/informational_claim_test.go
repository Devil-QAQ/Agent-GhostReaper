package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func TestInformationalResultCannotBecomeClaim(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "informational-result"
	seedGraph(t, store, task, "普通资料核对")
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	for _, severity := range []string{"none", " NONE ", "None"} {
		before, _ := store.CurrentRevision(ctx, task)
		id, result, err := env.SubmitClaim(ctx, "一致性核对通过，未发现漏洞", severity, []string{"ms-x.1"}, "MATCH", "实际值一致", "", "target", "", "")
		if err == nil || id != "" || result != nil || !strings.Contains(err.Error(), "submit_candidate_fact") || !strings.Contains(err.Error(), "不要改级别") {
			t.Fatalf("information accepted or unclear feedback: %q %+v %v", id, result, err)
		}
		after, _ := store.CurrentRevision(ctx, task)
		if before != after {
			t.Fatal("rejected information created business records")
		}
	}
	res, err := env.submitClaimTool().Call(ctx, []byte(`{"summary":"验证通过，无漏洞","severity":"none","evidence_refs":["ms-x.1"],"proving_marker":"MATCH","countercheck_note":"值一致","asset_id":"target"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("formal tool accepted information: %+v %v", res, err)
	}
	claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
	if len(claims) != 0 {
		t.Fatal("information polluted claim graph")
	}
	// It must not silently convert the rejected model interpretation into fact.
	facts, _ := store.NodesByType(ctx, task, rings.NodeFact, "")
	if len(facts) != 0 {
		t.Fatal("rejected claim was auto-converted to fact")
	}
	if _, err := env.SubmitCandidateFact(ctx, "人工清单4项，其中1项重复", "原始响应count=4, duplicate=1", "target", nil); err != nil {
		t.Fatal(err)
	}
}
