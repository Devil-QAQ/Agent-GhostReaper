package overmind

import (
	"context"
	"ghostreaper/internal/rings"
	"strings"
	"testing"
)

func TestObservedFactRemainsPlanningSource(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	const task = "observation-planning"
	l, model := p424Loop(t, s, task, func(rr *recordedRequest) scriptedReply {
		if !strings.Contains(rr.LastUser, "响应列出了管理入口") || strings.Contains(rr.LastUser, "虫群直觉回传") {
			t.Errorf("planning snapshot lost facts or retained hypothesis workflow: %s", rr.LastUser)
		}
		return scriptedReply{Text: `{"assessment":{"q_access":"只确认页面列出入口，尚未验证访问权限"},"new_intents":[{"intent":"验证已观察到的管理入口是否需要登录","expected_observation":"入口实际访问权限","action_level":"只读探测","covers":["a"],"source_refs":["f"]}],"combined_claims":[]}`, Finish: "stop"}
	})
	defer model.Close()
	_, err := s.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a", NodeType: rings.NodeAsset, AssetClass: rings.AssetDomain, Title: "example.test", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Title: "响应列出了管理入口", Status: "candidate", Detail: map[string]any{"evidence": "<a href=/admin>管理</a>"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f", ToNode: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	hv, err := Harvest(ctx, s, task)
	if err != nil || len(hv.Confirmed) != 1 {
		t.Fatalf("normal harvest failed: %+v %v", hv, err)
	}
	reply, _, err := l.decompose(ctx)
	if err != nil || len(reply.NewIntents) != 1 {
		t.Fatalf("normal planning failed: %+v %v", reply, err)
	}
	p := reply.NewIntents[0]
	if err := DispatchStep(ctx, s, task, "followup", p.Intent, DispatchOptions{ActionLevel: p.ActionLevel, CoversAssets: p.Covers, SourceRefs: p.SourceRefs, ExpectedObservation: p.ExpectedObservation}); err != nil {
		t.Fatal(err)
	}
	edges, _ := s.Edges(ctx, task, rings.EdgeSourcedFrom)
	if len(edges) != 1 || edges[0].From != "followup" || edges[0].To != "f" {
		t.Fatalf("source lost: %+v", edges)
	}
	n, _ := s.Node(ctx, task, "f")
	if n.Title != "响应列出了管理入口" || n.Detail["evidence"] != "<a href=/admin>管理</a>" {
		t.Fatal("planning altered original observation")
	}
}
