package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"ghostreaper/internal/rings"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetRelationsToolAndQuery(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	ctx := context.Background()
	const task = "relations"
	seedGraph(t, s, task, "观察资产关联")
	for _, id := range []string{"ip", "other"} {
		if _, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset, Title: id, AssetClass: rings.AssetHost, Status: "active"}}); err != nil {
			t.Fatal(err)
		}
	}
	env := &unitEnv{Executor: NewExecutor(s, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	input := json.RawMessage(`{"statement":"目标域名与另一个域名都解析到同一主机","evidence":"DNS response bytes","asset_id":"target","asset_relations":[{"from":"target","to":"ip","kind":"resolves_to"},{"from":"other","to":"ip","kind":"resolves_to"}]}`)
	res, err := env.submitCandidateFactTool().Call(ctx, input, nil)
	if err != nil || res.IsError {
		t.Fatalf("tool failed: %+v %v", res, err)
	}
	facts, _ := s.NodesByType(ctx, task, rings.NodeFact, "")
	if len(facts) != 1 {
		t.Fatal("fact not written")
	}
	fid := facts[0].ID
	g, err := s.AssetGraph(ctx, task)
	if err != nil || len(g.Relations) != 0 {
		t.Fatal("candidate relation exposed")
	}
	if _, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{NodeID: fid, Status: "confirmed"}}); err != nil {
		t.Fatal(err)
	}
	got := mustCall(t, env.listAssetsTool(), `{"asset_id":"ip"}`)
	for _, text := range []string{"target 解析到 ip", "other 解析到 ip", fid} {
		if !strings.Contains(got, text) {
			t.Fatalf("query missing %q: %s", text, got)
		}
	}
	if strings.Contains(got, "覆盖") {
		t.Fatal("read query introduced planning authority")
	}
	for _, bad := range []string{`{"statement":"s","evidence":"e","asset_relations":[{"from":"target","to":"s1","kind":"related_to"}]}`, `{"statement":"s","evidence":"e","inferred":true,"asset_relations":[{"from":"target","to":"ip","kind":"related_to"}]}`} {
		r, e := env.submitCandidateFactTool().Call(ctx, json.RawMessage(bad), nil)
		if e != nil || !r.IsError {
			t.Fatal("invalid submission accepted")
		}
	}
	facts, _ = s.NodesByType(ctx, task, rings.NodeFact, "")
	if len(facts) != 1 {
		t.Fatal("rejected input wrote a fact")
	}
}

func TestAssetRelationsQueryPagination(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	ctx := context.Background()
	const task = "relation-pages"
	seedGraph(t, s, task, "观察关系")
	var relations []rings.AssetRelation
	for i := 0; i < 23; i++ {
		id := fmt.Sprintf("peer-%02d", i)
		if _, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset, Title: id, AssetClass: rings.AssetHost, Status: "active"}}); err != nil {
			t.Fatal(err)
		}
		relations = append(relations, rings.AssetRelation{From: "target", To: id, Kind: "resolves_to"})
	}
	if _, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "f-pages", NodeType: rings.NodeFact, Title: "observed", Status: "confirmed", Detail: map[string]any{"evidence": "observed replies", "asset_relations": relations}}}); err != nil {
		t.Fatal(err)
	}
	env := &unitEnv{Executor: NewExecutor(s, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task}
	first := mustCall(t, env.listAssetsTool(), `{"asset_id":"target"}`)
	next := mustCall(t, env.listAssetsTool(), `{"asset_id":"target","relation_offset":20}`)
	if strings.Count(first, "解析到") != 20 || !strings.Contains(first, "relation_offset=20") || strings.Count(next, "解析到") != 3 || strings.Contains(next, "relation_offset=") {
		t.Fatalf("incomplete pagination: %s / %s", first, next)
	}
	for _, r := range relations {
		if strings.Count(first+next, r.To+" (依据事实:") != 1 {
			t.Fatalf("lost or repeated relation %s", r.To)
		}
	}
}
