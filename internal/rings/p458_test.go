package rings

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// ---- P4-58 全站全量扫描清剿:读侧轻量原语的回归测试 ----

// p458OpenStore 开临时库。
func p458OpenStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestP458EventsByKinds kind 定向直查:只取所需种类、revision 升序、
// 与全量 Events 的同种类过滤逐条一致(回放语义不变)。
func TestP458EventsByKinds(t *testing.T) {
	store := p458OpenStore(t)
	ctx := context.Background()
	if _, err := store.Append(ctx, "p458-kinds", []Event{
		{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created"}},
		{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "g1", NodeType: NodeGoal, Title: "目标"}},
		{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{StepID: "s1", UnitID: "u1", Type: SwarmRecToolCall, ToolName: "http_probe"}},
		{Kind: EventBlackboxRecord, Actor: "blackbox", Payload: BlackboxRecord{Type: BlackboxHeartbeat}},
		{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "g1", Status: "achieved"}},
		{Kind: EventMirrorRecord, Actor: "mirror", Payload: MirrorRecord{ExchangeID: "ex1", Type: MirrorRecHTTP, StepID: "s1", UnitID: "u1", Method: "GET", URL: "http://t/x", BlobRef: "sha256:seed"}},
		{Kind: EventSteerRecord, Actor: "creator", Payload: SteerRecord{SteerID: "st1", StepID: "s1", Command: SteerCommandSteer, State: SteerStateQueued, Message: "纠偏"}},
	}...); err != nil {
		t.Fatal(err)
	}

	got, err := store.EventsByKinds(ctx, "p458-kinds", EventSwarmRecord, EventSteerRecord)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{EventSwarmRecord, EventSteerRecord}
	if len(got) != len(wantKinds) {
		t.Fatalf("返回 %d 条,want %d 条", len(got), len(wantKinds))
	}
	for i, ev := range got {
		if ev.Kind != wantKinds[i] {
			t.Fatalf("第 %d 条 kind = %s,want %s", i, ev.Kind, wantKinds[i])
		}
	}
	if got[0].Payload == nil || got[1].Payload == nil {
		t.Fatal("payload 未随行")
	}

	// 与全量 Events 同种类过滤的一致性(权威序对账)。
	all, err := store.Events(ctx, "p458-kinds", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var filtered []RawEvent
	for _, ev := range all {
		if ev.Kind == EventSwarmRecord || ev.Kind == EventSteerRecord {
			filtered = append(filtered, ev)
		}
	}
	for i := range got {
		if got[i].Revision != filtered[i].Revision || got[i].Seq != filtered[i].Seq {
			t.Fatalf("第 %d 条与全量过滤不一致: got rev=%d seq=%d, want rev=%d seq=%d",
				i, got[i].Revision, got[i].Seq, filtered[i].Revision, filtered[i].Seq)
		}
	}

	// 单种类 + 空种类入参守卫。
	if only, err := store.EventsByKinds(ctx, "p458-kinds", EventBlackboxRecord); err != nil || len(only) != 1 {
		t.Fatalf("单种类直查 = %v, err = %v", len(only), err)
	}
	if _, err := store.EventsByKinds(ctx, "p458-kinds"); err == nil {
		t.Fatal("空种类入参应报错")
	}
}

// TestP458PanoramaFlight 在途全景直查:四结构与旧全量路径(NodesByType
// 全部步骤 + Graph 全图边)派生的装配入参等价——flight 集合、covers/
// deps 列表(created_rev 序)、资产标题、前置状态桩逐一一致。
func TestP458PanoramaFlight(t *testing.T) {
	store := p458OpenStore(t)
	ctx := context.Background()
	evs := []Event{
		{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: "a1", NodeType: NodeAsset, Title: "资产一"}},
		{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: "a2", NodeType: NodeAsset, Title: "资产二"}},
		{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "s-done", NodeType: NodeStep, Title: "已完成"}},
		{Kind: EventNodeStateChanged, Actor: "swarm:u1", Payload: NodeStateChanged{NodeID: "s-done", Status: "done"}},
		{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "s-run", NodeType: NodeStep, Title: "在跑", Status: "running",
			Detail: map[string]any{"priority": "high"}}},
		{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "s-queue", NodeType: NodeStep, Title: "排队", Status: "dispatched"}},
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeCovers, FromNode: "s-run", ToNode: "a1"}},
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeCovers, FromNode: "s-queue", ToNode: "a2"}},
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeDependsOn, FromNode: "s-queue", ToNode: "s-done"}},
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeDependsOn, FromNode: "s-run", ToNode: "s-done"}},
		// 噪声边:非在途步骤的边不得进结果。
		{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeCovers, FromNode: "s-done", ToNode: "a1"}},
	}
	if _, err := store.Append(ctx, "p458-pano", evs...); err != nil {
		t.Fatal(err)
	}

	got, err := store.PanoramaFlight(ctx, "p458-pano")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Covers) != 2 || len(got.Deps) != 2 {
		t.Fatalf("covers/deps 应只含在途步骤: covers=%v deps=%v", got.Covers, got.Deps)
	}
	if !reflect.DeepEqual(got.Covers["s-run"], []string{"a1"}) || !reflect.DeepEqual(got.Covers["s-queue"], []string{"a2"}) {
		t.Fatalf("covers 不符: %v", got.Covers)
	}
	if !reflect.DeepEqual(got.Deps["s-run"], []string{"s-done"}) || !reflect.DeepEqual(got.Deps["s-queue"], []string{"s-done"}) {
		t.Fatalf("deps 不符: %v", got.Deps)
	}
	if got.AssetTitle["a1"] != "资产一" || got.AssetTitle["a2"] != "资产二" {
		t.Fatalf("资产标题不符: %v", got.AssetTitle)
	}
	run, ok := got.Steps["s-run"]
	if !ok || run.Status != "running" || run.Detail["priority"] != "high" {
		t.Fatalf("在跑步骤应带 detail 供排序: %+v", run)
	}
	// 前置状态桩:s-done 以状态入 Steps(等前置判定查表面)。
	if dep, ok := got.Steps["s-done"]; !ok || dep.Status != "done" {
		t.Fatalf("前置状态桩缺失或不符: %+v", got.Steps["s-done"])
	}

	// 等价对账:旧路径(NodesByType 全部 + Graph)派生的四结构在
	// panoramaRows 消费面(在跑/排队行、covers/deps/标题)上逐项一致。
	list, err := store.NodesByType(ctx, "p458-pano", NodeStep, "")
	if err != nil {
		t.Fatal(err)
	}
	oldSteps := map[string]NodeFull{}
	for _, st := range list {
		oldSteps[st.ID] = st
	}
	for id, st := range got.Steps {
		old, ok := oldSteps[id]
		if !ok {
			t.Fatalf("直查步骤 %s 不在全量步骤表", id)
		}
		if old.Status != st.Status {
			t.Fatalf("步骤 %s 状态不一致: 直查=%s 全量=%s", id, st.Status, old.Status)
		}
	}
}

// TestP458PanoramaFlightEmpty 无在途步骤时返回空结构(装配侧得到空行
// 列表,与旧路径语义一致)。
func TestP458PanoramaFlightEmpty(t *testing.T) {
	store := p458OpenStore(t)
	ctx := context.Background()
	if _, err := store.Append(ctx, "p458-empty", Event{
		Kind: EventNodeAdded, Actor: "overmind",
		Payload: NodeAdded{NodeID: "s1", NodeType: NodeStep, Status: "done"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.PanoramaFlight(ctx, "p458-empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 0 || len(got.Covers) != 0 || len(got.Deps) != 0 || len(got.AssetTitle) != 0 {
		t.Fatalf("应全空: %+v", got)
	}
}
