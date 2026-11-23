package rings

import (
	"context"
	"testing"
)

// Retired contracts must fail atomically; ordinary observations remain replayable.
func TestRetiredHypothesisContractsRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "retired-contracts"
	mustAppend(t, s, task, na(NodeAsset, "a", "example.test", "active", AssetDomain, nil), na(NodeStep, "s", "读取公开服务标识", "done", "", nil))
	for _, ev := range []Event{
		{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: "h", NodeType: NodeType("hypothesis"), Title: "猜测", Status: "candidate"}},
		{Kind: EventKind("hypothesis.record"), Actor: "overmind", Payload: map[string]any{"hypothesis_id": "h", "type": "evaluation", "decision": "verify", "reason": "r"}},
		{Kind: EventKind("hypo.text"), Actor: "overmind", Payload: map[string]any{"hypothesis_id": "h", "title": "t"}},
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeType("verifies"), FromNode: "s", ToNode: "a"}},
	} {
		before, _ := s.CurrentRevision(ctx, task)
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("retired contract accepted: %+v", ev)
		}
		after, _ := s.CurrentRevision(ctx, task)
		if after != before {
			t.Fatal("rejected contract changed ledger")
		}
	}
	for _, kinds := range []any{nil, []string{"plain"}, []string{"credential"}, []string{"data"}} {
		detail := map[string]any{"inferred": true, "evidence": "未验证线索的依据"}
		if kinds != nil {
			detail["result_types"] = kinds
		}
		before, _ := s.CurrentRevision(ctx, task)
		if _, err := s.Append(ctx, task, na(NodeFact, "guess", "未验证结论", "candidate", "", detail)); err == nil {
			t.Fatal("speculation accepted as fact")
		}
		after, _ := s.CurrentRevision(ctx, task)
		if before != after {
			t.Fatal("rejected speculation changed ledger")
		}
	}
	mustAppend(t, s, task, na(NodeFact, "f", "响应包含服务标识", "candidate", "", map[string]any{"evidence": "Server: example", "result_types": []string{"plain"}}), ea(EdgeProduces, "s", "f"), ea(EdgeAbout, "f", "a"), Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "f", Status: "confirmed"}})
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := s.Node(ctx, task, "f")
	if err != nil || n.Status != "confirmed" || n.Detail["evidence"] != "Server: example" {
		t.Fatalf("observation lost: %+v %v", n, err)
	}
}
