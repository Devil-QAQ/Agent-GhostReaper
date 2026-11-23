package overmind

import (
	"context"
	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
	"iter"
	"strings"
	"testing"
)

type noAssetModel struct{ t *testing.T }

func (p noAssetModel) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.t.Fatal("asset placement started a dedicated model judgment")
	return llm.Message{}, "", llm.Usage{}, nil
}
func (p noAssetModel) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.t.Fatal("asset placement started dedicated work")
	return nil
}

func TestPassiveAssetPlacementUsesFactsAndWebsiteIdentity(t *testing.T) {
	store := openStore(t)
	const task = "passive-sites"
	seedDiscoveredGraph(t, store, task)
	evidence := "https://example.com:1122/login GET 200; https://example.com:1122/login POST 200; https://example.com:1122/ui/hints POST 200; https://example.com:1123/ 200"
	_, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "observed", NodeType: rings.NodeFact, Status: "confirmed", Title: "已发现两个网站及站内资源", Detail: map[string]any{"evidence": evidence}}})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{Store: store, TaskID: task, Provider: noAssetModel{t}}
	fact, _ := store.Node(t.Context(), task, "observed")
	observed := rings.AssetObservations{Assets: []rings.AssetObservation{
		{Class: rings.AssetAPI, Title: "GET https://example.com:1122/login", Evidence: "https://example.com:1122/login GET 200"},
		{Class: rings.AssetAPI, Title: "POST https://example.com:1122/login", Evidence: "https://example.com:1122/login POST 200"},
		{Class: rings.AssetAPI, Title: "POST https://example.com:1122/ui/hints", Evidence: "https://example.com:1122/ui/hints POST 200"},
		{Class: rings.AssetService, Title: "https://example.com:1123", Evidence: "https://example.com:1123/ 200"},
	}}
	if err := loop.recordFactAssets(t.Context(), *fact, observed); err != nil {
		t.Fatal(err)
	}
	graph, err := store.AssetGraph(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Assets) != 3 || len(graph.Hierarchy) != 0 {
		t.Fatalf("resources became independent assets: %+v", graph)
	}
	for _, a := range graph.Assets {
		if a.AssetClass == rings.AssetAPI || strings.Contains(a.DisplayTitle, "POST") || strings.Contains(a.DisplayTitle, "GET") {
			t.Fatal(a)
		}
	}
	rev, _ := store.CurrentRevision(t.Context(), task)
	loop.syncRecordedAssets(t.Context())
	next, _ := store.CurrentRevision(t.Context(), task)
	if rev != next {
		t.Fatal("recording existing facts is not idempotent")
	}
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 0 {
		t.Fatal("site resources caused extra approval")
	}
}

func TestNormalPlanningRetainsOptionalAssetRecordsWithoutExtraWork(t *testing.T) {
	reply, err := parseIntents(`{"new_intents":[],"asset_observations":[{"fact_id":"f1","assets":[{"class":"service","title":"https://example.test:1122","evidence":"observed entry"}],"relations":[]}]}`)
	if err != nil || len(reply.AssetObservations) != 1 || reply.AssetObservations[0].FactID != "f1" || len(reply.NewIntents) != 0 {
		t.Fatal("normal planning lost its passive recording", reply, err)
	}
}
