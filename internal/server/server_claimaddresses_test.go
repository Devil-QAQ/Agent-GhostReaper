package server

import (
	"ghostreaper/internal/rings"
	"reflect"
	"sort"
	"testing"
)

func TestClaimAddressesUseBindingsAndKeepHistoricalTargets(t *testing.T) {
	s, mux, _ := newInterjectTestServer(t)
	const task = "claim-addresses"
	evs := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created"}}}
	for _, a := range []rings.NodeAdded{
		{NodeID: "url", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://APP.example.test:443/a", Status: "active"},
		{NodeID: "ip", NodeType: rings.NodeAsset, AssetClass: rings.AssetHost, Title: "192.0.2.9", Status: "active"},
		{NodeID: "org", NodeType: rings.NodeAsset, AssetClass: rings.AssetOrganization, Title: "背景登记机构", Status: "active", Detail: map[string]any{"source": rings.AssetSourceDiscovered}},
	} {
		evs = append(evs, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: a})
	}
	for _, id := range []string{"multiple", "background-only", "unbound"} {
		evs = append(evs, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeTestClaim, Title: "完整发现说明", Status: rings.TestClaimConfirmed}})
	}
	for _, id := range []string{"url", "ip", "org"} {
		evs = append(evs, rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "multiple", ToNode: id}})
	}
	evs = append(evs, rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "background-only", ToNode: "org"}}, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{ExcludedAssets: []string{"url"}}})
	if _, err := s.Store.Append(t.Context(), task, evs...); err != nil {
		t.Fatal(err)
	}
	var rows []claimView
	get(t, mux, "/api/tasks/"+task+"/claims", 200, &rows)
	var wanted []string
	for _, row := range rows {
		if row.ID == "multiple" {
			wanted = append([]string{}, row.Addresses...)
			sort.Strings(wanted)
			if !reflect.DeepEqual(wanted, []string{"192.0.2.9", "https://app.example.test/a"}) {
				t.Fatal("address guessed from label or omitted historical target", row.Addresses)
			}
		} else if len(row.Addresses) != 0 {
			t.Fatal("background/missing binding became an address", row.ID, row.Addresses)
		}
	}
	if len(wanted) != 2 {
		t.Fatal("claim not returned")
	}
	var detail claimDetailView
	get(t, mux, "/api/tasks/"+task+"/claims/multiple", 200, &detail)
	addresses := append([]string{}, detail.Claim.Addresses...)
	sort.Strings(addresses)
	if !reflect.DeepEqual(addresses, wanted) || detail.Claim.Statement != "完整发现说明" {
		t.Fatal("formal detail lost addresses or full text")
	}
}
