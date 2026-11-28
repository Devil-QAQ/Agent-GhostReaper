package rings

import "testing"

func TestServiceIdentityUsesHostAndEffectivePort(t *testing.T) {
	for _, group := range [][]string{{"192.0.2.1:2222", "http://192.0.2.1:2222", "http://192.0.2.1:2222/sssss", "https://192.0.2.1:2222/x?a=b"}, {"example.test:80", "http://EXAMPLE.test/", "http://example.test:0080/path"}, {"[2001:db8::1]:443", "https://[2001:0db8:0:0::1]/path"}} {
		want := ""
		for _, title := range group {
			_, _, key, err := AssetIdentity(AssetService, title)
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				want = key
			}
			if want != key {
				t.Fatal(group, want, key)
			}
		}
	}
}

func TestRejoinedEndpointOverridesOnlyItsCoveringWithdrawal(t *testing.T) {
	assets := []NodeFull{}
	for _, a := range []struct {
		id    string
		class AssetClass
		title string
	}{{"ip", AssetHost, "192.0.2.1"}, {"bare", AssetService, "192.0.2.1:2222"}, {"url", AssetService, "http://192.0.2.1:2222/path"}, {"sibling", AssetService, "http://192.0.2.1:3333"}} {
		assets = append(assets, NodeFull{NodeView: NodeView{ID: a.id, Type: NodeAsset, AssetClass: a.class, Title: a.title, Status: "active"}, Detail: map[string]any{"source": "manual"}})
	}
	excluded := map[string]bool{"ip": true, "url": true}
	covered := ExcludedCoveredAssetIDs(assets, excluded, map[string]bool{"bare": true})
	if covered["bare"] || covered["url"] || !covered["ip"] || !covered["sibling"] {
		t.Fatal("rejoin changed unrelated targets", covered)
	}
	if members := InventoryAssetIDs(assets, RoeState{ExcludedAssets: []string{"ip", "url"}, ScopeApproved: []string{"bare"}}); !members["bare"] || !members["url"] || members["sibling"] {
		t.Fatal(members)
	}
	if covered := ExcludedCoveredAssetIDs(assets, excluded); !covered["bare"] || !covered["url"] {
		t.Fatal("fresh withdrawal lost coverage", covered)
	}
}

func TestExplicitRejoinRetainsLegacyResourceAsset(t *testing.T) {
	assets := []NodeFull{{NodeView: NodeView{ID: "legacy", Type: NodeAsset, AssetClass: AssetAPI, Title: "https://example.test:2222/a", Status: "active"}, Detail: map[string]any{"source": AssetSourceDiscovered, "scope": AssetScopeOut}}}
	if members := InventoryAssetIDs(assets, RoeState{ScopeApproved: []string{"legacy"}}); !members["legacy"] {
		t.Fatal("explicit rejoin lost legacy asset", members)
	}
}
