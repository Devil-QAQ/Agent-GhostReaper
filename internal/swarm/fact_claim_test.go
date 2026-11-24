package swarm

import (
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

func TestFactAttributionUsesExistingClaimWithoutResubmission(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	const task = "fact-attribution"
	seedGraph(t, s, task, "补充已有观察")
	if _, err := s.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-original", NodeType: rings.NodeTestClaim, Title: "配置可匿名读取", Status: rings.TestClaimConfirmed}}); err != nil {
		t.Fatal(err)
	}
	env := &unitEnv{Executor: NewExecutor(s, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	id, err := env.submitCandidateFact(t.Context(), "配置内包含明文凭据，尚未验证有效性", "人工固定证据片段：password=fixture-only", "target", nil, nil, "tc-original", rings.FactResultData)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := s.Node(t.Context(), task, id)
	if err != nil || fact.Detail["claim_id"] != "tc-original" || fact.Status != "candidate" || fact.Detail["transcript_id"] != "swarm-u1" {
		t.Fatal("attribution lost observation/provenance", fact, err)
	}
	claims, _ := s.NodesByType(t.Context(), task, rings.NodeTestClaim, "")
	if len(claims) != 1 || claims[0].Status != rings.TestClaimConfirmed || len(claimRecords(t, s, task, rings.TestClaimRecSubmit)) != 0 {
		t.Fatal("fact created another submission or changed the verdict", claims)
	}
	for _, ref := range []string{"target", "missing", "s1"} {
		if _, err := env.submitCandidateFact(t.Context(), "观察", "实际证据", "target", nil, nil, ref); err == nil {
			t.Fatalf("invalid claim attribution %q accepted", ref)
		}
	}
	if _, err := env.SubmitCandidateFact(t.Context(), "普通观察", "普通证据", "target", nil); err != nil {
		t.Fatal("ordinary observations acquired a claim requirement", err)
	}
}
