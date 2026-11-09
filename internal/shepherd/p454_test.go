package shepherd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// ---- P4-54:牧者最高权限操作面工具 ----
// 目标改/删、停聚焦、优先级、注意事项、范围标识与通配——与面板同源
// (同一账本动作),回执如实。

// runP454Tool 按名直调工具(确定性单测;对话编排在活体验收实证)。
func runP454Tool(t *testing.T, s *Service, task, name, args string) (string, bool) {
	t.Helper()
	for _, tl := range s.toolset(task, "sm-test") {
		if tl.Name() != name {
			continue
		}
		res, err := tl.Call(context.Background(), json.RawMessage(args), nil)
		if err != nil {
			t.Fatalf("工具 %s 执行失败: %v", name, err)
		}
		return res.Flatten(), res.IsError
	}
	t.Fatalf("工具集应含 %s", name)
	return "", false
}

// p454Env 牧者操作面测试环境:两个目标(g1 聚焦候选/g2 其他方向)、
// 各方向排队步骤、种子资产。
func p454Env(t *testing.T) (*Service, *rings.Store, string) {
	t.Helper()
	store := openStore(t)
	const task = "sh-p454"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "只做这个", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g2", NodeType: rings.NodeGoal, Title: "其他方向", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "聚焦方向探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "其他方向探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g2", "action_level": "只读探测"}}},
	); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store, RootDir: t.TempDir(), Wake: func(string) {}}
	return s, store, task
}

func TestP454ToolsGoalAmendDeleteFocus(t *testing.T) {
	s, store, task := p454Env(t)
	ctx := context.Background()

	// 修改目标:标题 + 完成条件重新冻结。
	out, isErr := runP454Tool(t, s, task, "update_goal",
		`{"goal_id":"g1","title":"改后的目标","predicate":{"op":"all_of","conditions":[{"kind":"fact_confirmed","asset_id":"a-x","min":1}]}}`)
	if isErr {
		t.Fatalf("修改目标不应报错: %s", out)
	}
	g, _ := store.Node(ctx, task, "g1")
	if g.Title != "改后的目标" {
		t.Fatalf("标题应已改: %q", g.Title)
	}
	if _, ok := g.Detail["predicate"].(map[string]any); !ok {
		t.Fatalf("谓词应已重新冻结: %+v", g.Detail)
	}
	// 非法谓词拒绝(人话回执)。
	out, isErr = runP454Tool(t, s, task, "update_goal", `{"goal_id":"g1","predicate":{"op":"bad"}}`)
	if !isErr || !strings.Contains(out, "完成条件非法") {
		t.Fatalf("非法谓词应拒绝: %s", out)
	}

	// 停聚焦:非 g1 方向排队步骤取消,回执报数。
	out, isErr = runP454Tool(t, s, task, "set_focus", `{"goal_id":"g1"}`)
	if isErr {
		t.Fatalf("停聚焦不应报错: %s", out)
	}
	if !strings.Contains(out, "取消排队步骤 1 个") || !strings.Contains(out, "已聚焦「改后的目标」") {
		t.Fatalf("停聚焦回执应报取消笔数与聚焦目标: %s", out)
	}
	if st, _ := store.Node(ctx, task, "s2"); st.Status != "cancelled" {
		t.Fatalf("其他方向排队步骤应取消: %+v", st)
	}
	if st, _ := store.Node(ctx, task, "s1"); st.Status != "dispatched" {
		t.Fatalf("聚焦方向步骤不应被动: %+v", st)
	}
	if overmind.FocusGoalID(ctx, store, task) != "g1" {
		t.Fatal("聚焦标记应在 g1")
	}
	// 解除聚焦:如实回执。
	out, isErr = runP454Tool(t, s, task, "set_focus", `{}`)
	if isErr || !strings.Contains(out, "方向聚焦已解除") {
		t.Fatalf("解除聚焦应如实回执: %s", out)
	}

	// 删除目标:该方向步骤取消、目标转已删除,回执报数。
	out, isErr = runP454Tool(t, s, task, "delete_goal", `{"goal_id":"g2","reason":"不要了"}`)
	if isErr {
		t.Fatalf("删除目标不应报错: %s", out)
	}
	if !strings.Contains(out, "取消排队步骤 0 个") { // s2 已在聚焦时取消
		t.Fatalf("删除回执应如实报数: %s", out)
	}
	g2, _ := store.Node(ctx, task, "g2")
	if g2.Status != overmind.GoalStatusDiscarded {
		t.Fatalf("目标应转已删除: %+v", g2)
	}
	// 已删目标再改:拒绝。
	out, isErr = runP454Tool(t, s, task, "update_goal", `{"goal_id":"g2","title":"x"}`)
	if !isErr || !strings.Contains(out, "已删除") {
		t.Fatalf("已删目标应拒改: %s", out)
	}
}

func TestP454ToolsPriorityAndNotes(t *testing.T) {
	s, store, task := p454Env(t)
	ctx := context.Background()

	// 优先级:按方向调 g2 的排队步骤为 high,回执报数。
	out, isErr := runP454Tool(t, s, task, "set_priority", `{"goal_id":"g2","priority":"high"}`)
	if isErr {
		t.Fatalf("优先级调整不应报错: %s", out)
	}
	if !strings.Contains(out, "优先级已调为 high,生效步骤 1 个") {
		t.Fatalf("优先级回执应报生效数: %s", out)
	}
	st, _ := store.Node(ctx, task, "s2")
	if p, _ := st.Detail["priority"].(string); p != "high" {
		t.Fatalf("优先级应落 detail: %+v", st.Detail)
	}
	// 非法词汇:拒绝。
	if _, isErr = runP454Tool(t, s, task, "set_priority", `{"step_ids":["s1"],"priority":"urgent"}`); !isErr {
		t.Fatal("非法优先级应拒绝")
	}

	// 注意事项三向:追加 → 修改(覆盖) → 清空;RoE 同源可读。
	out, isErr = runP454Tool(t, s, task, "set_notes", `{"action":"append","text":"用小字典"}`)
	if isErr || !strings.Contains(out, "已追加一行") {
		t.Fatalf("追加注意事项应成功: %s", out)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.CreatorNotes != "用小字典" {
		t.Fatalf("注意事项应入 RoE: %q", roe.CreatorNotes)
	}
	runP454Tool(t, s, task, "set_notes", `{"action":"append","text":"不要修改任何数据"}`)
	roe, _ = store.CurrentRoE(ctx, task)
	if roe.CreatorNotes != "用小字典\n不要修改任何数据" {
		t.Fatalf("追加应换行合并: %q", roe.CreatorNotes)
	}
	out, isErr = runP454Tool(t, s, task, "set_notes", `{"action":"replace","text":"温柔点"}`)
	if isErr || !strings.Contains(out, "已更新") {
		t.Fatalf("覆盖注意事项应成功: %s", out)
	}
	roe, _ = store.CurrentRoE(ctx, task)
	if roe.CreatorNotes != "温柔点" {
		t.Fatalf("覆盖应整体替换: %q", roe.CreatorNotes)
	}
	runP454Tool(t, s, task, "set_notes", `{"action":"clear"}`)
	roe, _ = store.CurrentRoE(ctx, task)
	if roe.CreatorNotes != "" {
		t.Fatalf("清空后应为空: %q", roe.CreatorNotes)
	}
}

func TestP454SetAssetScopeIdentifiersAndWildcard(t *testing.T) {
	s, store, task := p454Env(t)
	ctx := context.Background()

	// 纳入·新标识:入图(来源手动)+ 默认范围内,回执如实(红队任务
	// 本就全部可测,纳入答复如实说明)。
	out, isErr := runP454Tool(t, s, task, "set_asset_scope", `{"action":"include","assets":["bbb.example.com"]}`)
	if isErr {
		t.Fatalf("纳入新标识不应报错: %s", out)
	}
	if !strings.Contains(out, "无需纳入") {
		t.Fatalf("手动入图资产默认范围内,回执应如实: %s", out)
	}
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	var bbb *rings.NodeFull
	for i := range assets {
		if assets[i].Title == "bbb.example.com" {
			bbb = &assets[i]
		}
	}
	if bbb == nil || rings.AssetSourceOf(bbb.Detail) != rings.AssetSourceManual {
		t.Fatalf("新标识应入图且来源手动: %+v", bbb)
	}

	// 摘除·通配写法:模式资产入图并进摘除清单(发现形态,与建任务
	// 初始摘除同形)。
	out, isErr = runP454Tool(t, s, task, "set_asset_scope", `{"action":"exclude","assets":["*.bad.org"]}`)
	if isErr || !strings.Contains(out, "已摘除") {
		t.Fatalf("通配摘除应成功: %s", out)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ExcludedAssets) != 1 {
		t.Fatalf("摘除清单应有通配资产: %+v", roe.ExcludedAssets)
	}
	w, _ := store.Node(ctx, task, roe.ExcludedAssets[0])
	if w.Title != "*.bad.org" || rings.AssetScopeOf(w.Detail) != rings.AssetScopeOut {
		t.Fatalf("通配资产形态应为发现+范围外: %+v", w)
	}

	// 摘除·图上 ID:直接命中既有资产。
	out, isErr = runP454Tool(t, s, task, "set_asset_scope", `{"action":"exclude","assets":["a-x"]}`)
	if isErr || !strings.Contains(out, "已摘除 a-x(x.example.com)") {
		t.Fatalf("按 ID 摘除应命中既有资产: %s", out)
	}

	// 陈旧 ID 引用:报错带可用资产(自纠面),不把笔误 ID 当主机入图。
	out, isErr = runP454Tool(t, s, task, "set_asset_scope", `{"action":"exclude","assets":["a-nonexist"]}`)
	if !isErr || !strings.Contains(out, "不在本任务图上") {
		t.Fatalf("陈旧 ID 应报错: %s", out)
	}
}
