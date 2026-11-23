package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

func seedAssetProposalTask(t *testing.T, s *Server, task string) {
	t.Helper()
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{
		Action: "created", Title: "资产收编测试", Detail: map[string]any{"task_type": rings.TaskTypePentest}}}}
	for i := range 2 {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: fmt.Sprintf("f%d", i), NodeType: rings.NodeFact, Title: "观察到测试对象", Status: "candidate",
			Detail: map[string]any{"evidence": "本地测试夹具证据", "unit_id": "u1", "new_asset": map[string]any{"class": "domain", "title": fmt.Sprintf("sample%d.example.org", i)}}}})
	}
	if _, err := s.Store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	if _, err := overmind.Harvest(t.Context(), s.Store, task); err != nil {
		t.Fatal(err)
	}
}

func TestAssetAdoptionFormalAPIHasNoAccessDenySideEffects(t *testing.T) {
	s, mux, _ := newInterjectTestServer(t)
	const task = "adoption-api"
	seedAssetProposalTask(t, s, task)
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/approvals", "")
	if code != http.StatusOK {
		t.Fatalf("pending API status=%d", code)
	}
	cards := out["pending"].([]any)
	if len(cards) != 2 {
		t.Fatalf("pending=%d", len(cards))
	}
	rid := cards[0].(map[string]any)["id"].(string)
	if cards[0].(map[string]any)["kind"] != "asset" {
		t.Fatal("membership presented as action permission")
	}
	code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/"+rid, `{"approved":false}`)
	if code != http.StatusOK {
		t.Fatalf("decline status=%d", code)
	}
	roe, _ := s.Store.CurrentRoE(t.Context(), task)
	assets, _ := s.Store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if roe.UpdatedRev != 0 || len(assets) != 0 {
		t.Fatal("decline created an asset or altered permission")
	}
	code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/"+rid, `{"approved":true}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate decision status=%d", code)
	}
	remaining, _ := s.Store.PendingAssetProposals(t.Context(), task)
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/batch", fmt.Sprintf(`{"ids":[%q],"approved":true}`, remaining[0].RequestID))
	if code != http.StatusOK || out["handled"] != float64(1) {
		t.Fatalf("batch failed: %d %v", code, out)
	}
	assets, _ = s.Store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	remaining, _ = s.Store.PendingAssetProposals(t.Context(), task)
	if len(assets) != 1 || len(remaining) != 0 {
		t.Fatal("batch adoption did not converge")
	}
}

func TestInfoSourceManagementEndpointsRetired(t *testing.T) {
	_, mux, _ := newInterjectTestServer(t)
	for _, action := range []string{"mark", "revoke"} {
		code, _ := callJSON(t, mux, "POST", "/api/tasks/irrelevant/roe/infosources/"+action, `{"host":"example.org"}`)
		if code != http.StatusNotFound {
			t.Fatalf("retired information-source endpoint remains: %s status=%d", action, code)
		}
	}
}

func TestAssetAdoptionManualJoinAfterDeclineAndPendingChoice(t *testing.T) {
	s, mux, _ := newInterjectTestServer(t)
	const task = "manual-adoption"
	seedAssetProposalTask(t, s, task)
	cards, _ := s.Store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 2 {
		t.Fatal("missing choices")
	}
	if _, err := overmind.DecideAssetProposal(t.Context(), s.Store, task, cards[0].RequestID, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		var p overmind.AssetProposal
		if json.Unmarshal([]byte(card.ToolArgs), &p) != nil {
			t.Fatal("invalid proposal")
		}
		code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/assets", fmt.Sprintf(`{"assets":[{"class":"subdomain","title":%q}]}`, strings.ToUpper(p.Title)+"."))
		if code != http.StatusOK {
			t.Fatal("explicit manual join failed", code)
		}
	}
	pending, _ := s.Store.PendingAssetProposals(t.Context(), task)
	if len(pending) != 0 {
		t.Fatal("manual choice left an obsolete pending card")
	}
	ag, err := s.Store.AssetGraph(t.Context(), task)
	if err != nil || len(ag.Assets) != 2 {
		t.Fatal("manual joins missing", err)
	}
	for _, a := range ag.Assets {
		if a.FactCount != 1 || a.AssetClass != rings.AssetDomain {
			t.Fatal("original observation not bound", a.ID, a.FactCount)
		}
	}
	roe, _ := s.Store.CurrentRoE(t.Context(), task)
	if roe.UpdatedRev != 0 {
		t.Fatal("manual inventory choice changed access boundary")
	}
	code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/assets", `{"assets":[{"class":"domain","title":"SAMPLE0.example.org."}]}`)
	if code != http.StatusBadRequest {
		t.Fatal("canonical duplicate accepted", code)
	}
}

func TestAssetIdentitySeedCanonicalization(t *testing.T) {
	for _, assets := range [][]assetIn{
		{{Class: "domain", Title: "API.example.org."}, {Class: "subdomain", Title: "api.example.org"}},
		{{Class: "service", Title: "https://app.example.org:443/a"}, {Class: "api", Title: "https://APP.example.org/b"}},
	} {
		if _, err := buildAssetSeed(assets); err == nil {
			t.Fatal("canonical duplicate seed accepted")
		}
	}
	seed, err := buildAssetSeed([]assetIn{{Class: "subdomain", Title: "API.example.org."}, {Class: "service", Title: "http://one.example.org:8080/"}, {Class: "service", Title: "https://one.example.org:8080/"}, {Class: "service", Title: "http://two.example.org:8080/"}, {Class: "service", Title: "http://one.example.org:8081/"}})
	if err != nil || len(seed.events) != 5 {
		t.Fatal("distinct services merged", err)
	}
	node := seed.events[0].Payload.(rings.NodeAdded)
	if node.AssetClass != rings.AssetDomain || node.Title != "api.example.org" {
		t.Fatal("new domain class not normalized")
	}
}

func TestRoEExcludedTargetsRemainManageableOutsideInventory(t *testing.T) {
	s, mux, _ := newInterjectTestServer(t)
	const task = "excluded-management"
	_, err := s.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "old", NodeType: rings.NodeAsset, AssetClass: rings.AssetDomain, Title: "old.example.test", Status: "active", Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{ExcludedAssets: []string{"old"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var roe roeView
	get(t, mux, "/api/tasks/"+task+"/roe", 200, &roe)
	if len(roe.ExcludedTargets) != 1 || roe.ExcludedTargets[0].ID != "old" || roe.ExcludedTargets[0].Title != "old.example.test" {
		t.Fatal("explicit exclusion cannot be managed", roe.ExcludedTargets)
	}
	ag, err := s.Store.AssetGraph(t.Context(), task)
	if err != nil || len(ag.Assets) != 0 {
		t.Fatal("excluded target leaked into inventory")
	}
	before, _ := s.Store.Events(t.Context(), task, 0, 0)
	code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", `{"excluded":[]}`)
	if code != 200 {
		t.Fatal("explicit restore failed", code)
	}
	ag, err = s.Store.AssetGraph(t.Context(), task)
	if err != nil || len(ag.Assets) != 1 || ag.Assets[0].ID != "old" {
		t.Fatal("explicit restore failed to join inventory", err)
	}
	after, _ := s.Store.Events(t.Context(), task, 0, 0)
	for i, ev := range before {
		if string(ev.Payload) != string(after[i].Payload) || ev.Revision != after[i].Revision {
			t.Fatal("old exclusion history rewritten")
		}
	}
}
