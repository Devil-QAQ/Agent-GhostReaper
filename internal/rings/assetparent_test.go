package rings

import "testing"

func TestAssetParentSeparatesContainmentFromDNSAndSharedHosting(t *testing.T) {
	for _, tt := range []struct {
		childClass  AssetClass
		child       string
		parentClass AssetClass
		parent      string
		valid       bool
	}{
		{AssetDomain, "api.example.test", AssetDomain, "example.test", true},
		{AssetSubdomain, "API.example.test.", AssetDomain, "EXAMPLE.test", true},
		{AssetService, "https://api.example.test:8443/path", AssetDomain, "api.example.test", true},
		{AssetAPI, "http://192.0.2.4:8080/api", AssetHost, "192.0.2.4", true},
		{AssetService, "[2001:db8::1]:443", AssetHost, "2001:db8::1", true},
		{AssetDomain, "otherexample.test", AssetDomain, "example.test", false},
		{AssetDomain, "api.example.test", AssetHost, "192.0.2.4", false},
		{AssetHost, "192.0.2.4", AssetDomain, "api.example.test", false},
		{AssetService, "https://other.example.test", AssetDomain, "api.example.test", false},
		{AssetService, "https://api.example.test", AssetHost, "192.0.2.4", false},
		{AssetDomain, "example.test", AssetDomain, "example.test", false},
		{AssetDomain, "example.test", AssetOrganization, "登记机构", false},
	} {
		if got := ValidateAssetParent(tt.childClass, tt.child, tt.parentClass, tt.parent) == nil; got != tt.valid {
			t.Errorf("parent compatibility %s→%s: got %v want %v", tt.child, tt.parent, got, tt.valid)
		}
	}
}

func TestAssetDisplayHierarchyUsesAddressStructureOnly(t *testing.T) {
	assets := []NodeFull{}
	for _, a := range []struct {
		id    string
		class AssetClass
		title string
	}{
		{"root", AssetDomain, "example.test"}, {"sub", AssetSubdomain, "api.example.test"},
		{"deep", AssetDomain, "v1.api.example.test"}, {"ip", AssetHost, "192.0.2.21"},
		{"site", AssetService, "https://api.example.test:8443/"},
		{"service", AssetAPI, "https://api.example.test:8443/v1/items?q=one"},
		{"ip-service", AssetService, "http://192.0.2.21:8080/path"},
		{"outside", AssetDomain, "outside.example.test"},
	} {
		assets = append(assets, NodeFull{NodeView: NodeView{ID: a.id, Type: NodeAsset, AssetClass: a.class, Title: a.title}})
	}
	members := map[string]bool{"root": true, "sub": true, "deep": true, "ip": true, "site": true, "service": true, "ip-service": true}
	edges := []EdgeView{{Type: EdgeSubOf, From: "ip", To: "root"}, {Type: EdgeSubOf, From: "service", To: "ip"}}
	got := AssetDisplayHierarchy(assets, edges, members)
	want := map[string]string{"sub": "root", "deep": "sub"}
	if len(got) != len(want) {
		t.Fatalf("hierarchy: %+v", got)
	}
	for _, e := range got {
		if want[e.From] != e.To {
			t.Fatalf("DNS or unrelated host became containment: %+v", e)
		}
	}
	if AssetDisplayTitle(AssetAPI, assets[5].Title) != "api.example.test:8443" || assets[5].Title != "https://api.example.test:8443/v1/items?q=one" {
		t.Fatal("service label lost exact original resource")
	}
	// Identical identities have identical structure regardless of source.
	for i := range assets {
		assets[i].Detail = map[string]any{"source": "discovered"}
	}
	other := AssetDisplayHierarchy(assets, edges, members)
	for i, e := range got {
		if e != other[i] {
			t.Fatal("source changed address structure")
		}
	}
}
