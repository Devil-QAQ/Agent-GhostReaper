package rings

import "testing"

func TestComponentsAndNetworkServicesAreNotSystemSubjects(t *testing.T) {
	nodes := []NodeFull{
		{NodeView: NodeView{ID: "site", Type: NodeAsset, AssetClass: AssetService, Title: "https://192.0.2.1:8111", CreatedRev: 1}},
		{NodeView: NodeView{ID: "ip", Type: NodeAsset, AssetClass: AssetHost, Title: "192.0.2.1"}},
		{NodeView: NodeView{ID: "xmpp", Type: NodeAsset, AssetClass: AssetService, Title: "xmpp://192.0.2.1:15222/"}, Detail: map[string]any{"source": "discovered", "adopted": true}},
		{NodeView: NodeView{ID: "component", Type: NodeAsset, AssetClass: AssetApp, Title: "Spring OAuth2 授权服务器（/uaa 上下文）"}, Detail: map[string]any{"source": "discovered", "adopted": true}},
	}
	members := map[string]bool{"site": true, "ip": true, "xmpp": true, "component": true}
	relations := []AssetRelationView{{AssetRelation: AssetRelation{From: "site", To: "component", Kind: "hosted_on"}, FactIDs: []string{"proof"}}, {AssetRelation: AssetRelation{From: "site", To: "ip", Kind: "address"}}}
	hierarchy := AssetDisplayHierarchy(nodes, []EdgeView{{Type: EdgeSubOf, From: "xmpp", To: "ip"}}, members, relations)
	views := []AssetNodeView{}
	for _, n := range nodes {
		views = append(views, AssetNodeView{NodeView: n.NodeView, Source: AssetSourceOf(n.Detail), Independent: IndependentAsset(n)})
	}
	subjects := AssetSubjectAliases(views, hierarchy, relations)
	if len(subjects["component"]) != 1 || subjects["component"][0] != "site" {
		t.Fatal("component elevated into a system", subjects)
	}
	if len(subjects["xmpp"]) != 0 {
		t.Fatal("protocol endpoint became a website or inherited system ownership from IP", subjects)
	}
	if len(subjects["site"]) != 1 || subjects["site"][0] != "site" {
		t.Fatal(subjects)
	}
}
