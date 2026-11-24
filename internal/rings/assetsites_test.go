package rings

import "testing"

func TestResourceAddressesResolveToWebsiteIdentity(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"GET https://EXAMPLE.test:443/login?next=a", "GET https://example.test/login?next=b#page", true},
		{"POST https://example.test/login", "GET https://example.test/login", true},
		{"https://example.test/Login", "https://example.test/login", true},
		{"https://example.test/login?a=1", "https://example.test/login?b=1", true},
		{"https://example.test/login?a=1&b=2", "https://example.test/login?b=3&a=4&a=5", true},
		{"https://example.test:1122/login", "https://example.test:1123/login", false},
		{"https://[2001:db8::1]:1122/login", "https://[2001:0db8:0:0::1]:1122/login", true},
	}
	for _, c := range cases {
		_, _, a, ea := AssetIdentity(AssetAPI, c.a)
		_, _, b, eb := AssetIdentity(AssetAPI, c.b)
		if ea != nil || eb != nil || (a == b) != c.same {
			t.Fatalf("identity %s / %s: %q %q %v %v", c.a, c.b, a, b, ea, eb)
		}
	}
}

func TestWebsitesRemainRootsWithSharedAddressAndMajorInterfaces(t *testing.T) {
	assets := []NodeFull{}
	for _, a := range []struct {
		id    string
		class AssetClass
		title string
	}{
		{"ip", AssetHost, "192.0.2.1"},
		{"one", AssetService, "https://192.0.2.1:1122/"}, {"two", AssetService, "https://192.0.2.1:1123/"},
		{"vhost", AssetService, "https://portal.example.test:1122/"},
		{"login", AssetAPI, "POST https://192.0.2.1:1122/login"},
		{"app", AssetApp, "https://192.0.2.1:1123/admin/"},
		{"admin", AssetAPI, "GET https://192.0.2.1:1123/admin/users"},
		{"otherpath", AssetAPI, "GET https://192.0.2.1:1123/administrator"},
	} {
		assets = append(assets, NodeFull{NodeView: NodeView{ID: a.id, Type: NodeAsset, AssetClass: a.class, Title: a.title}})
	}
	members := map[string]bool{}
	for _, a := range assets {
		members[a.ID] = true
	}
	hierarchy := AssetDisplayHierarchy(assets, []EdgeView{{Type: EdgeSubOf, From: "one", To: "ip"}}, members)
	parents := map[string]string{}
	for _, e := range hierarchy {
		parents[e.From] = e.To
	}
	if parents["one"] != "" || parents["two"] != "" || parents["vhost"] != "" || parents["login"] != "" || parents["admin"] != "" || parents["app"] != "two" || parents["otherpath"] != "" {
		t.Fatal(parents)
	}
}

func TestInventoryAdmissionAndWithdrawalDoNotInventHostOwnership(t *testing.T) {
	assets := []NodeFull{{NodeView: NodeView{ID: "seed", Type: NodeAsset, AssetClass: AssetService, Title: "https://[2001:db8::1]:1122/path"}}}
	for _, title := range []string{"https://[2001:db8::1]:1123", "[2001:db8::1]:22"} {
		if AutomaticallyInventory(AssetService, title, assets, RoeState{}) {
			t.Fatal("shared host admitted an unconfirmed system", title)
		}
	}
	if AutomaticallyInventory(AssetService, "https://elsewhere.test:1122", assets, RoeState{}) {
		t.Fatal("unproven ownership admitted")
	}
	for _, child := range []string{"https://[2001:db8::1]:1123/", "http://[2001:db8::1]:1122/"} {
		if AssetContainsIdentity(AssetService, assets[0].Title, AssetService, child) {
			t.Fatal("withdrawing a site removed another site", child)
		}
	}
	if !AssetContainsIdentity(AssetService, assets[0].Title, AssetAPI, "POST https://[2001:db8::1]:1122/login") {
		t.Fatal("site child retained after withdrawal")
	}
	if !AssetContainsIdentity(AssetAPI, "POST https://[2001:db8::1]:1122/login", AssetAPI, "GET https://[2001:db8::1]:1122/login") {
		t.Fatal("resource did not resolve to its website")
	}
}

func TestNamedSystemGroupsOnlyEvidenceBackedEntries(t *testing.T) {
	assets := []NodeFull{}
	for _, a := range []struct {
		id    string
		class AssetClass
		title string
	}{
		{"system", AssetApp, "业务门户"}, {"one", AssetService, "https://one.example.test:1122"}, {"two", AssetService, "https://two.example.test:1123"}, {"unrelated", AssetService, "https://one.example.test:1124"},
	} {
		assets = append(assets, NodeFull{NodeView: NodeView{ID: a.id, Type: NodeAsset, AssetClass: a.class, Title: a.title}})
	}
	members := map[string]bool{"system": true, "one": true, "two": true, "unrelated": true}
	relationships := []AssetRelationView{{AssetRelation: AssetRelation{From: "system", To: "one", Kind: "hosted_on"}, FactIDs: []string{"f1"}}, {AssetRelation: AssetRelation{From: "two", To: "system", Kind: "same_system"}, FactIDs: []string{"f2"}}}
	parents := map[string]string{}
	for _, e := range AssetDisplayHierarchy(assets, nil, members, relationships) {
		parents[e.From] = e.To
	}
	if parents["one"] != "system" || parents["two"] != "system" || parents["unrelated"] != "" || parents["system"] != "" {
		t.Fatal(parents)
	}
}
