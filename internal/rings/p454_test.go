package rings

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// ---- P4-54(问题清单 #78＋#73⑧):node.amended 受控变更与操作者取消
// 挂起记录 ----
// 目标修改(标题/完成条件/聚焦)、步骤优先级直接落账;声明挂起记录
// (suspend)的校验与投影。

func p454OpenStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestP454NodeAmendedGoalProjection 目标变更投影:标题覆盖、谓词与聚焦
// 键合并、updated_rev 推进;非法键/非法节点类型被受控入口拒收。
func TestP454NodeAmendedGoalProjection(t *testing.T) {
	s := p454OpenStore(t)
	ctx := context.Background()
	const task = "p454-amend"
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}},
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "g1", NodeType: NodeGoal, Title: "旧标题", Status: "active",
			Detail: map[string]any{"predicate": map[string]any{
				"op": "all_of", "conditions": []map[string]any{{"kind": "fact_confirmed", "asset_id": "a1", "min": 1}},
			}}},
		},
	); err != nil {
		t.Fatal(err)
	}
	title := "新标题"
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "creator", Payload: NodeAmended{
		NodeID: "g1", Title: &title,
		Detail: map[string]any{"focus": true, "predicate": map[string]any{
			"op": "any_of", "conditions": []map[string]any{{"kind": "fact_confirmed", "asset_id": "a2", "min": 2}},
		}},
		Note: "改目标",
	}}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Node(ctx, task, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Title != "新标题" {
		t.Fatalf("标题应已覆盖: %q", g.Title)
	}
	if f, _ := g.Detail["focus"].(bool); !f {
		t.Fatalf("聚焦标记应已合并进 detail: %+v", g.Detail)
	}
	pred, _ := g.Detail["predicate"].(map[string]any)
	if pred["op"] != "any_of" {
		t.Fatalf("谓词应整体覆盖为新值: %+v", pred)
	}
	if g.UpdatedRev <= g.CreatedRev {
		t.Fatalf("updated_rev 应推进: created=%d updated=%d", g.CreatedRev, g.UpdatedRev)
	}

	// 键白名单:goal 不允许改 priority。
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "creator",
		Payload: NodeAmended{NodeID: "g1", Detail: map[string]any{"priority": "high"}}}); err == nil {
		t.Fatal("goal 的 priority 键应被拒收")
	}
	// fact 节点不支持变更。
	if _, err := s.Append(ctx, task,
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "f1", NodeType: NodeFact, Title: "事实", Status: "confirmed"}},
		Event{Kind: EventNodeAmended, Actor: "creator",
			Payload: NodeAmended{NodeID: "f1", Detail: map[string]any{"predicate": map[string]any{}}}},
	); err == nil {
		t.Fatal("fact 节点的变更应被拒收")
	}
	// 不存在的节点。
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "creator",
		Payload: NodeAmended{NodeID: "g-none", Detail: map[string]any{"focus": true}}}); err == nil {
		t.Fatal("不存在的节点应被拒收")
	}
}

// TestP454NodeAmendedStepPriority 步骤优先级变更:合并进 detail、显式
// null 移除回缺省。
func TestP454NodeAmendedStepPriority(t *testing.T) {
	s := p454OpenStore(t)
	ctx := context.Background()
	const task = "p454-prio"
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}},
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "s1", NodeType: NodeStep, Title: "探测", Status: "dispatched"}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "shepherd",
		Payload: NodeAmended{NodeID: "s1", Detail: map[string]any{"priority": "high"}}}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Node(ctx, task, "s1")
	if p, _ := st.Detail["priority"].(string); p != "high" {
		t.Fatalf("优先级应已落 detail: %+v", st.Detail)
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeAmended, Actor: "shepherd",
		Payload: NodeAmended{NodeID: "s1", Detail: map[string]any{"priority": nil}}}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Node(ctx, task, "s1")
	if _, ok := st.Detail["priority"]; ok {
		t.Fatalf("显式 null 应移除优先级键: %+v", st.Detail)
	}
}

// TestP454ExclusionDropsClosure 摘除即去权(P4-54 活体暴露的缺口回归):
// 被摘除的手动/种子资产不贡献闭包——否则领取时闭包仍含其主机,命令级
// 范围检查被"闭包内"短路,摘除后的单位照打该目标。
func TestP454ExclusionDropsClosure(t *testing.T) {
	seeds := []ScopeSeed{
		{ID: "a-seed", Title: "127.0.0.1:9961", Source: AssetSourceSeed},
		{ID: "a-manual", Title: "127.0.0.1:9962", Source: AssetSourceManual},
	}
	// 未摘除:手动资产贡献闭包。
	c := BuildScopeClosure(seeds, nil, nil)
	if !c.CoversTarget("127.0.0.1", "9962", nil) {
		t.Fatal("手动资产应贡献闭包")
	}
	// 摘除 a-manual 后:9962 出闭包(9961 不受影响)。
	ex := map[string]bool{"a-manual": true}
	c = BuildScopeClosure(seeds, nil, ex)
	if c.CoversTarget("127.0.0.1", "9962", nil) {
		t.Fatal("摘除后的手动资产不应再授权(摘除即去权)")
	}
	if !c.CoversTarget("127.0.0.1", "9961", nil) {
		t.Fatal("未摘除的种子不受影响")
	}
}

// TestP454SuspendRecordValidationAndProjection 操作者取消挂起记录:理由
// 必填、锚点必须是判断;投影 latest-wins 进 pending_reason、时间线留
// suspend 条目。
func TestP454SuspendRecordValidationAndProjection(t *testing.T) {
	s := p454OpenStore(t)
	ctx := context.Background()
	const task = "p454-suspend"
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}},
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "tc1", NodeType: NodeTestClaim, Title: "判断", Status: "testing"}},
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "a1", NodeType: NodeAsset, Title: "x.com", Status: "active"}},
	); err != nil {
		t.Fatal(err)
	}
	// 理由为空:拒。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "shepherd",
		Payload: TestClaimRecord{ClaimID: "tc1", Type: TestClaimRecSuspend}}); err == nil {
		t.Fatal("挂起理由为空应被拒")
	}
	// 锚点不是判断:拒。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "shepherd",
		Payload: TestClaimRecord{ClaimID: "a1", Type: TestClaimRecSuspend, Reason: "r"}}); err == nil {
		t.Fatal("锚点不是判断应被拒")
	}
	// 合法挂起:投影 pending_reason + 时间线。
	if _, err := s.Append(ctx, task,
		Event{Kind: EventNodeStateChanged, Actor: "shepherd",
			Payload: NodeStateChanged{NodeID: "tc1", Status: TestClaimPending, Note: "方向被收窄"}},
		Event{Kind: EventTestClaimRecord, Actor: "shepherd",
			Payload: TestClaimRecord{ClaimID: "tc1", Type: TestClaimRecSuspend, Reason: "方向被收窄"}},
	); err != nil {
		t.Fatal(err)
	}
	cs, ok, err := s.ClaimCaseOf(ctx, task, "tc1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if cs.PendingReason != "方向被收窄" {
		t.Fatalf("投影应带挂起理由: %+v", cs)
	}
	found := false
	for _, e := range cs.Timeline {
		if e.K == "suspend" {
			found = true
			var rec TestClaimRecord
			if json.Unmarshal(e.P, &rec) != nil || rec.Reason != "方向被收窄" {
				t.Fatalf("suspend 时间线应带原始负载: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("时间线应含 suspend 条目: %+v", cs.Timeline)
	}
}
