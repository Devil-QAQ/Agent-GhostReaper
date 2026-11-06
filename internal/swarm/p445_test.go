package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-45(问题清单 #61 ③④):摘除即止损与资产范围操作 ----
// 影响预览(ScopeImpactOf)、止损本体(StopLossForAssets:排队取消 +
// 在跑 kill)、纳入/摘除两向(IncludeAssets/ExcludeAssets)与领取防
// 复活(beginUnit 只领 dispatched)。

// p445ScopeEnv 范围止损测试环境:红队任务(范围操作两种类型通用),
// 资产 a-x/a-y,覆盖 a-x 的在跑(s-run)/排队(s-q)步骤与覆盖 a-y 的
// 排队步骤(s-other),外加舵杆假钩子(s-run 可停)。
func p445ScopeEnv(t *testing.T) (*rings.Store, *Helm, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p445-task"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-y", NodeType: rings.NodeAsset, Title: "y.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "在跑探测", Status: "running",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-q", NodeType: rings.NodeStep, Title: "排队探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-other", NodeType: rings.NodeStep, Title: "别处探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-run", ToNode: "a-x"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-q", ToNode: "a-x"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-other", ToNode: "a-y"}},
	); err != nil {
		t.Fatal(err)
	}
	helm := NewHelm(store)
	stopped := map[string]bool{}
	helm.attachTask(task, func(taskID, stepID string) (string, bool) {
		if taskID != task || stepID != "s-run" || stopped[stepID] {
			return "", false
		}
		stopped[stepID] = true
		return "u9", true
	})
	return store, helm, task
}

func TestP445ScopeImpactOf(t *testing.T) {
	store, _, task := p445ScopeEnv(t)
	impact, err := ScopeImpactOf(context.Background(), store, task, []string{"a-x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Units) != 1 || impact.Units[0].StepID != "s-run" {
		t.Fatalf("在跑命中应只有 s-run: %+v", impact.Units)
	}
	if len(impact.Queued) != 1 || impact.Queued[0].StepID != "s-q" {
		t.Fatalf("排队命中应只有 s-q: %+v", impact.Queued)
	}
	// 无步骤覆盖的资产:零命中如实。
	impact, err = ScopeImpactOf(context.Background(), store, task, []string{"a-y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Units) != 0 || len(impact.Queued) != 1 || impact.Queued[0].StepID != "s-other" {
		t.Fatalf("a-y 命中应只有排队 s-other: %+v %+v", impact.Units, impact.Queued)
	}
}

func TestP445StopLossForAssets(t *testing.T) {
	store, helm, task := p445ScopeEnv(t)
	ctx := context.Background()
	killed, aborted, err := StopLossForAssets(ctx, store, helm, task, []string{"a-x"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 1 || killed[0].StepID != "s-run" {
		t.Fatalf("应终止在跑 s-run: %+v", killed)
	}
	if len(aborted) != 1 || aborted[0].StepID != "s-q" {
		t.Fatalf("应取消排队 s-q: %+v", aborted)
	}
	// 排队步骤落 aborted(留痕),在跑步骤的 aborted 由引擎真中断收场写
	// (假钩子只 cancel,图上仍 running——活体验收实证引擎侧)。
	if s, err := store.Node(ctx, task, "s-q"); err != nil || s.Status != "aborted" {
		t.Fatalf("s-q 应已中止: %+v %v", s, err)
	}
	if s, err := store.Node(ctx, task, "s-other"); err != nil || s.Status != "dispatched" {
		t.Fatalf("无关步骤不应被动: %+v %v", s, err)
	}
	// 止损留痕:kill 舵杆记录 + aborted 状态事件(Note=范围摘除)。
	evs, _ := store.Events(ctx, task, 0, 0)
	var killRec, abortNote int
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventSteerRecord:
			var r rings.SteerRecord
			if jsonUnmarshal(ev.Payload, &r) == nil && r.Command == rings.SteerCommandKill {
				killRec++
				if !strings.Contains(r.Reason, "范围摘除止损") {
					t.Fatalf("kill 理由应含范围摘除止损: %q", r.Reason)
				}
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if jsonUnmarshal(ev.Payload, &p) == nil && p.NodeID == "s-q" && p.Status == "aborted" {
				if p.Note != "范围摘除" {
					t.Fatalf("取消留痕 Note 应为 范围摘除: %q", p.Note)
				}
				abortNote++
			}
		}
	}
	if killRec != 1 || abortNote != 1 {
		t.Fatalf("留痕笔数不符: kill=%d abort=%d", killRec, abortNote)
	}
	// 幂等:重复止损零新落笔(已终态不动)。
	killed, aborted, err = StopLossForAssets(ctx, store, helm, task, []string{"a-x"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 || len(aborted) != 0 {
		t.Fatalf("重复止损应零新落笔: %+v %+v", killed, aborted)
	}
}

func TestP445ExcludeAssets(t *testing.T) {
	store, helm, task := p445ScopeEnv(t)
	ctx := context.Background()
	res, err := ExcludeAssets(ctx, store, helm, task, []string{"a-x"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Excluded) != 1 || res.Excluded[0] != "a-x" {
		t.Fatalf("应摘除 a-x: %+v", res.Excluded)
	}
	if len(res.KilledUnits) != 1 || len(res.AbortedSteps) != 1 {
		t.Fatalf("摘除应触发止损: %+v %+v", res.KilledUnits, res.AbortedSteps)
	}
	roe, err := store.CurrentRoE(ctx, task)
	if err != nil || len(roe.ExcludedAssets) != 1 || roe.ExcludedAssets[0] != "a-x" {
		t.Fatalf("摘除清单应含 a-x: %+v %v", roe.ExcludedAssets, err)
	}
	if !strings.Contains(roe.Note, "摘除") || !strings.Contains(roe.Note, "x.example.com") {
		t.Fatalf("备注应含摘除明细: %q", roe.Note)
	}
	// 重复摘除:AlreadyOut 如实,零新落账零止损。
	before, _ := store.CurrentRevision(ctx, task)
	res, err = ExcludeAssets(ctx, store, helm, task, []string{"a-x"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AlreadyOut) != 1 || len(res.Excluded) != 0 {
		t.Fatalf("重复摘除应归 AlreadyOut: %+v", res)
	}
	after, _ := store.CurrentRevision(ctx, task)
	if after != before {
		t.Fatalf("重复摘除不应落账: %d → %d", before, after)
	}
	// 不在图上的标识拒收(带可用资产提示)。
	if _, err := ExcludeAssets(ctx, store, helm, task, []string{"a-nonexist"}, "shepherd"); err == nil ||
		!strings.Contains(err.Error(), "不在本任务图上") {
		t.Fatalf("未知资产应拒收: %v", err)
	}
}

// p445PentestEnv 渗透任务 + 种子域名 + 范围外发现资产(待批)。
func p445PentestEnv(t *testing.T) (*rings.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p445-pen"
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-disc", NodeType: rings.NodeAsset, Title: "api.other.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	return store, task
}

func TestP445IncludeAssets(t *testing.T) {
	store, task := p445PentestEnv(t)
	ctx := context.Background()
	if pending, err := store.PendingScopeAssets(ctx, task); err != nil || len(pending) != 1 || pending[0].ID != "a-disc" {
		t.Fatalf("前置:a-disc 应待批: %+v %v", pending, err)
	}
	res, err := IncludeAssets(ctx, store, task, []string{"a-disc", "a-seed"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Approved) != 1 || res.Approved[0] != "a-disc" {
		t.Fatalf("待批资产应放行: %+v", res.Approved)
	}
	if len(res.AlreadyIn) != 1 || res.AlreadyIn[0] != "a-seed" {
		t.Fatalf("种子资产应归 AlreadyIn: %+v", res.AlreadyIn)
	}
	if pending, _ := store.PendingScopeAssets(ctx, task); len(pending) != 0 {
		t.Fatalf("放行后应无待批: %+v", pending)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ScopeApproved) != 1 || roe.ScopeApproved[0] != "a-disc" {
		t.Fatalf("放行清单应含 a-disc: %+v", roe.ScopeApproved)
	}
	if !strings.Contains(roe.Note, "放行") || !strings.Contains(roe.Note, "api.other.com") {
		t.Fatalf("备注应含放行明细: %q", roe.Note)
	}
	// 全部本就范围内:零落账(如实在 AlreadyIn)。
	before, _ := store.CurrentRevision(ctx, task)
	res, err = IncludeAssets(ctx, store, task, []string{"a-seed"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AlreadyIn) != 1 || len(res.Approved)+len(res.Reincluded) != 0 {
		t.Fatalf("无变更应归 AlreadyIn: %+v", res)
	}
	after, _ := store.CurrentRevision(ctx, task)
	if after != before {
		t.Fatalf("无变更不应落账: %d → %d", before, after)
	}
}

func TestP445IncludeAssetsExcludedReinclude(t *testing.T) {
	store, helm, task := p445ScopeEnv(t)
	ctx := context.Background()
	// 红队任务:先摘除 a-y(其排队步骤 s-other 一并取消),再纳入。
	if _, err := ExcludeAssets(ctx, store, helm, task, []string{"a-y"}, "shepherd"); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Node(ctx, task, "s-other"); s.Status != "aborted" {
		t.Fatalf("摘除 a-y 应取消 s-other: %+v", s)
	}
	res, err := IncludeAssets(ctx, store, task, []string{"a-y"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reincluded) != 1 || res.Reincluded[0] != "a-y" {
		t.Fatalf("应移回范围: %+v", res.Reincluded)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ExcludedAssets) != 0 {
		t.Fatalf("纳入后摘除清单应空: %+v", roe.ExcludedAssets)
	}
	// 红队任务范围内资产纳入:AlreadyIn(答复面如实说"本就全开")。
	res, err = IncludeAssets(ctx, store, task, []string{"a-x"}, "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AlreadyIn) != 1 {
		t.Fatalf("红队未摘除资产应归 AlreadyIn: %+v", res)
	}
}

func TestP445BeginUnitRefusesNonDispatched(t *testing.T) {
	store, _, task := p445ScopeEnv(t)
	ctx := context.Background()
	// 步骤已被范围摘除取消(aborted):领取不得复活。
	if _, _, err := StopLossForAssets(ctx, store, nil, task, []string{"a-y"}, "creator"); err != nil {
		t.Fatal(err)
	}
	ex := NewExecutor(store, nil, filepath.Join(t.TempDir(), "tasks"), Budget{})
	if _, err := ex.RunStep(ctx, task, "s-other"); !errors.Is(err, ErrStaleGraph) {
		t.Fatalf("领取已收场步骤应拒绝(R14 同款): %v", err)
	}
	if s, err := store.Node(ctx, task, "s-other"); err != nil || s.Status != "aborted" {
		t.Fatalf("已收场步骤不得被复活: %+v %v", s, err)
	}
}

// jsonUnmarshal 测试内联 JSON 解码别名。
func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
