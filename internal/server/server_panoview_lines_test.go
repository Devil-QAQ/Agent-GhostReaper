package server

import (
	"reflect"
	"testing"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

func TestP509CurrentDispatchLineage(t *testing.T) {
	h, store := p509Server(t)
	var created map[string]string
	post(t, h, "POST", "/api/tasks", `{"task_type":"redteam","title":"当前来源机制","goals":["检查来源连线"],"assets":[{"class":"service","title":"https://fixture.example.test/"}]}`, 200, &created)
	task := created["id"]
	assets, err := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if err != nil || len(assets) != 1 {
		t.Fatal("asset seed", err)
	}
	asset := assets[0].ID
	// 使用当前派发入口，让覆盖、前置与来源按运行时机制分别落账。
	err = overmind.DispatchStep(t.Context(), store, task, "intent-a", "读取公开信息", overmind.DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{asset}, SourceRefs: []string{asset}})
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "claim-b", NodeType: rings.NodeTestClaim, Title: "已确认结果", Status: rings.TestClaimCandidate, Detail: map[string]any{"step_id": "intent-a"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{NodeID: "claim-b", Status: rings.TestClaimConfirmed}},
	)
	err = overmind.DispatchStep(t.Context(), store, task, "intent-c", "依据结果继续核对", overmind.DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{asset}, DependsOn: []string{"intent-a"}, SourceRefs: []string{"claim-b"}})
	if err != nil {
		t.Fatal(err)
	}
	view := p509View(t, h, task)
	got := map[string]bool{}
	for _, e := range view.Edges {
		got[e.From+"→"+e.To] = true
	}
	want := map[string]bool{asset + "→intent-a": true, "intent-a→claim-b": true, "claim-b→intent-c": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("current dispatch must produce only the actual lineage: got=%v want=%v", got, want)
	}
}

func TestP509SingleLinesAcrossAliasesAndDetails(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s1", ToNode: "a-sub"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s1", ToNode: "f-1"}},
	)
	rev, _ := store.CurrentRevision(t.Context(), task)
	rawBefore, _ := store.Edges(t.Context(), task)
	view := p509View(t, h, task)
	var combined *panoEdgeRow
	seen := map[string]bool{}
	for i := range view.Edges {
		e := &view.Edges[i]
		key := e.From + "\x00" + e.To
		if seen[key] || e.From == e.To {
			t.Fatal("parallel/self edge after merging asset/fact aliases")
		}
		seen[key] = true
		if e.From == "a-root" && e.To == "s1" {
			combined = e
		}
	}
	if combined == nil || combined.Rel != "sourced_from" || !reflect.DeepEqual(combined.Refs, []string{"a-root", "a-sub"}) {
		t.Fatalf("alias sources duplicated or coverage included: %+v", combined)
	}
	var links panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/f-1", 200, &links)
	count := 0
	for _, e := range links.Edges {
		if e.From == "a-root" && e.To == "s1" {
			count++
			if !reflect.DeepEqual(e.Refs, combined.Refs) {
				t.Fatal("focus view lost original source references")
			}
		}
	}
	if count != 1 {
		t.Fatal("focus view reintroduced a parallel line")
	}
	var detail map[string]any
	get(t, h, "/api/tasks/"+task+"/pview/details/s1", 200, &detail)
	sources := detail["sources"].([]any)
	kinds := map[string]bool{}
	for _, source := range sources {
		row := source.(map[string]any)
		if row["id"] == "a-root" {
			kinds[row["relation"].(string)] = true
		}
	}
	if !kinds["covers"] || !kinds["sourced_from"] {
		t.Fatal("display merge hid a relationship from the detail view")
	}
	rawAfter, _ := store.Edges(t.Context(), task)
	afterRev, _ := store.CurrentRevision(t.Context(), task)
	if rev != afterRev || !reflect.DeepEqual(rawBefore, rawAfter) {
		t.Fatal("display merge changed ledger relations")
	}
}

func TestP509LineageChainAndMultipleOrigins(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		// 同一产出由创建者、显式来源与派生关系记录，仍只有 A→B 一条。
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-1", ToNode: "s2"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "tc-1", ToNode: "s2"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s-from-b", NodeType: rings.NodeStep, Title: "由漏洞继续核对", Status: "done"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-from-b", ToNode: "tc-1"}},
		// 第二个真实来源是已合入 s1 的事实，另保留 s1→C。
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-from-b", ToNode: "f-1"}},
		// 测试对象与执行前置不变成额外来源。
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s-from-b", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s-from-b", ToNode: "s3"}},
	)
	view := p509View(t, h, task)
	parents, births := map[string]bool{}, 0
	for _, e := range view.Edges {
		if e.To == "s-from-b" {
			parents[e.From] = true
		}
		if e.From == "s2" && e.To == "tc-1" {
			births++
			if !reflect.DeepEqual(e.Refs, []string{"s2"}) {
				t.Fatal("original source reference was lost")
			}
		}
		if e.From == "s-from-b" && (e.To == "tc-1" || e.To == "s1") {
			t.Fatal("source direction was reversed")
		}
	}
	if births != 1 || !reflect.DeepEqual(parents, map[string]bool{"tc-1": true, "s1": true}) {
		t.Fatalf("expected A→B→C and the other real source only: births=%d parents=%v", births, parents)
	}
	for focus, want := range map[string]int{"s2": 0, "tc-1": 0, "tc-1?target=s-from-b": 1, "s-from-b": 1} {
		var links panoLinksView
		get(t, h, "/api/tasks/"+task+"/pview/links/"+focus, 200, &links)
		count := 0
		for _, e := range links.Edges {
			if e.From == "tc-1" && e.To == "s-from-b" {
				count++
			}
		}
		if count != want {
			t.Fatalf("B→C must follow the selected production boundary %s: got %d want %d", focus, count, want)
		}
	}
}

func TestP509SharedFactKeepsEveryProducer(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f-shared", NodeType: rings.NodeFact, Title: "共同结果", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-shared"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s2", ToNode: "f-shared"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s-shared", NodeType: rings.NodeStep, Title: "引用共同结果", Status: "done"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-shared", ToNode: "f-shared"}},
	)
	view := p509View(t, h, task)
	parents := map[string]bool{}
	for _, e := range view.Edges {
		if e.To == "s-shared" {
			parents[e.From] = true
			if !reflect.DeepEqual(e.Refs, []string{"f-shared"}) {
				t.Fatal("fact reference was replaced by its display owner")
			}
		}
	}
	if !reflect.DeepEqual(parents, map[string]bool{"s1": true, "s2": true}) {
		t.Fatalf("shared producer dropped: %v", parents)
	}
}
