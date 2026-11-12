package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-49(问题清单 #70):注意事项三处结构性注入 ----
// ① RoE 约束块行(虫群/主宰共用渲染器);② 任务包授权节携带;③ 重锚
// 定重申;领取时冻结(与操作约束同款次序)。

func TestP449RoEConstraintNotesLine(t *testing.T) {
	notes := "用小字典,不要用大字典\n不要修改任何数据"
	with := RenderRoEConstraint("你的探索手段受此框定。", nil, nil, "", nil, nil, notes, nil)
	if !strings.Contains(with, "造物主注意事项") {
		t.Fatalf("约束块应含注意事项行:\n%s", with)
	}
	for _, want := range []string{"用小字典,不要用大字典", "不要修改任何数据"} {
		if !strings.Contains(with, want) {
			t.Fatalf("约束块注意事项应逐行呈现 %q:\n%s", want, with)
		}
	}
	// 留空任务:行不出现(行为与历史一致)。
	without := RenderRoEConstraint("你的探索手段受此框定。", nil, nil, "", nil, nil, "", nil)
	if strings.Contains(without, "造物主注意事项") {
		t.Fatalf("无注意事项时行不应出现:\n%s", without)
	}
}

func TestP449TaskPackCarriesNotes(t *testing.T) {
	notes := "爆破用小字典\n不要修改任何数据"
	pack := &TaskPack{
		TaskID: "t1", StepID: "s1", GoalID: "g1", GoalTitle: "拿到登录口令",
		StepIntent:    "对登录口做口令尝试",
		Authorization: RoE{CreatorNotes: notes},
	}
	rendered := pack.Render()
	if !strings.Contains(rendered, "造物主注意事项") {
		t.Fatalf("任务包授权节应含注意事项:\n%s", rendered)
	}
	for _, want := range []string{"爆破用小字典", "不要修改任何数据"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("任务包注意事项应逐行呈现 %q:\n%s", want, rendered)
		}
	}
	// 无注意事项:行不出现。
	plain := (&TaskPack{TaskID: "t1", StepID: "s1", StepIntent: "探测"}).Render()
	if strings.Contains(plain, "造物主注意事项") {
		t.Fatalf("无注意事项的任务包不应出现该行:\n%s", plain)
	}
}

func TestP449AnchorRestatesNotes(t *testing.T) {
	notes := "用小字典\n不要修改任何数据"
	g := &toolGuard{
		env:    &unitEnv{Executor: &Executor{}, TaskID: "t1", UnitID: "u1"},
		pack:   &TaskPack{TaskID: "t1", StepID: "s1", GoalID: "g1", GoalTitle: "拿到登录口令", StepIntent: "口令尝试", Authorization: RoE{CreatorNotes: notes}},
		everyN: 10,
	}
	msg := g.anchorMessage(10)
	if !strings.Contains(msg, "造物主注意事项(重申") {
		t.Fatalf("任务锚应重申注意事项:\n%s", msg)
	}
	for _, want := range []string{"用小字典", "不要修改任何数据"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("任务锚应逐行重申 %q:\n%s", want, msg)
		}
	}
	// 无注意事项:重申不出现。
	gNone := &toolGuard{
		env:    g.env,
		pack:   &TaskPack{TaskID: "t1", StepID: "s1", StepIntent: "探测", Authorization: RoE{}},
		everyN: 10,
	}
	if m := gNone.anchorMessage(10); strings.Contains(m, "造物主注意事项") {
		t.Fatalf("无注意事项的任务锚不应重申:\n%s", m)
	}
}

// TestP449ClaimFreezesNotes 领取时冻结:建任务带的注意事项随授权副本
// 进任务包(改注意事项只对下一批领取生效,与操作约束同款次序)。
func TestP449ClaimFreezesNotes(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "t-p449-claim"
	seedGraph(t, store, task, "对登录口做口令尝试")
	notes := "爆破用小字典"
	mustAppendSwarm(t, store, task, rings.Event{Kind: rings.EventRoeUpdated, Actor: "manager",
		Payload: rings.RoeUpdated{CreatorNotes: &notes}})

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Authorization.CreatorNotes != notes {
		t.Fatalf("任务包授权应冻结注意事项: got %q want %q", pack.Authorization.CreatorNotes, notes)
	}
	if rendered := pack.Render(); !strings.Contains(rendered, "爆破用小字典") {
		t.Fatalf("任务包应携带注意事项:\n%s", rendered)
	}

	// RoE 变更不带注意事项(nil = 不动):新领取仍见同值。
	mustAppendSwarm(t, store, task, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{DeniedTools: []string{"nmap"}, Note: "收紧工具"}})
	pack2, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("第二笔领取失败: %v", err)
	}
	if pack2.Authorization.CreatorNotes != notes {
		t.Fatalf("nil 注意事项的新领取应保持: got %q", pack2.Authorization.CreatorNotes)
	}
}

// mustAppendSwarm 测试内落账(swarm 包不引 rings_test 的 mustAppend)。
func mustAppendSwarm(t *testing.T, store *rings.Store, task string, events ...rings.Event) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
}
