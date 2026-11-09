package overmind

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-06c 拒因回流:入口守卫拒收的 LastErr 摘要注入下一轮态势快照 ----

// TestGuardRejectRingLimited 限量防噪:环形保留最近 maxGuardRejects 条,
// 更旧的被挤掉;超长行经 clipEval 裁剪。
func TestGuardRejectRingLimited(t *testing.T) {
	l := &Loop{}
	for i := 1; i <= 5; i++ {
		l.noteGuardReject(fmt.Sprintf("意图「意图%d」被拒: 原因%d", i, i))
	}
	got := l.guardRejectBriefs()
	if len(got) != maxGuardRejects {
		t.Fatalf("简报应限量 %d 条,实际 %d", maxGuardRejects, len(got))
	}
	if !strings.Contains(got[0], "意图3") || !strings.Contains(got[len(got)-1], "意图5") {
		t.Fatalf("应保留最新 %d 条(意图3~5): %v", maxGuardRejects, got)
	}
	// 超长拒因裁剪(clipEval 语义)。
	l.noteGuardReject(strings.Repeat("长", 300))
	got = l.guardRejectBriefs()
	if n := len(got[len(got)-1]); n > 165 {
		t.Fatalf("超长拒因应被裁剪,实际 %d 字节", n)
	}
}

// TestSnapshotInjectsGuardRejects 快照注入节:有拒因逐条呈现,无拒因
// 如实标注"(无)"。
func TestSnapshotInjectsGuardRejects(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "reject-snap-task"
	if err := SeedGoal(ctx, store, task, "g1", "目标", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}

	// 空:节存在且标注无。
	snap, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if !strings.Contains(snap, "上轮提交被拒(入口校验拒收") || !strings.Contains(snap, "(无)") {
		t.Fatalf("无拒因时节应呈现\"(无)\":\n%s", snap)
	}

	// 有拒因:摘要逐条进快照。
	l.noteGuardReject("组合声明「链条组合」被拒: 链环节 f-1 不是已确认 Fact(状态 fact/candidate)——未验证线索不得作为组合前提(R13)")
	snap, _, err = l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if !strings.Contains(snap, "组合声明「链条组合」被拒") || !strings.Contains(snap, "R13") {
		t.Fatalf("拒因摘要应进快照:\n%s", snap)
	}
}

// TestGuardRejectBackflowE2E 字段传递(mock 快照经真实引擎链路):拆解
// 产物被入口守卫拒收(covers 幽灵资产,账本整批拒、无事件)→ cycle 回
// 静默等唤醒(既有零进展防热循环语义不变)→ 拒因回流进下一次唤醒的
// 拆解会话用户提示 → 主宰换真实资产重派成功。全程 cycle 真实流转,
// LLM 为进程内脚本模型(确定性)。
func TestGuardRejectBackflowE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "reject-e2e-task"

	if err := SeedGoal(ctx, store, task, "g1", "确认靶标暴露面", map[string]any{
		"predicate": map[string]any{
			"op":         "all_of",
			"conditions": []any{map[string]any{"kind": "fact_confirmed", "asset_id": "a1", "min": 1}},
		},
	}); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset,
			Title: "靶标 127.0.0.1", Status: "active", AssetClass: rings.AssetHost},
	}); err != nil {
		t.Fatalf("种资产失败: %v", err)
	}

	var sawReject atomic.Bool
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			// 拆解:首轮派幽灵资产意图(必被守卫拒);看到拒因回流后换
			// 图上真实资产重派——图上已有该步骤(任何状态)即不再重复
			// 派发(提示词规则 2 同构;P4-04 起落地间有增量轮,无此守卫
			// 会逐轮复读重派)。
			if strings.Contains(rr.LastUser, "上轮提交被拒") &&
				strings.Contains(rr.LastUser, "探测幽灵资产") &&
				!strings.Contains(rr.LastUser, "] 探测真实靶标") {
				sawReject.Store(true)
				return scriptedReply{Text: `{"new_intents":[{"intent":"探测真实靶标","expected_observation":"端口证据","action_level":"只读探测","covers":["a1"]}]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[{"intent":"探测幽灵资产","expected_observation":"证据","action_level":"只读探测","covers":["ghost1"]}]}`, Finish: "stop"}
		}
		// 虫群单位:提交候选事实后收口。
		if !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "f1", Name: "submit_candidate_fact",
				Args: `{"statement":"靶标暴露面已确认","evidence":"实测","asset_id":"a1"}`,
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "意图完成。", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	model.release() // 本测试不观测并发闸门:虫群请求直接放行
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}

	// 第一次唤醒:幽灵意图被守卫拒,零派发回静默(不崩溃、不热循环)。
	if loop.cycle(ctx) {
		t.Fatalf("整批被拒后不应判定完成")
	}
	if loop.LastErr() == nil {
		t.Fatalf("派发失败应如实留在 LastErr")
	}
	if steps, _ := store.NodesByType(ctx, task, rings.NodeStep, ""); len(steps) != 0 {
		t.Fatalf("幽灵意图不得入图,实际 %d 步", len(steps))
	}
	// P4-29：拒因保留到下一次真实触发，不以空唤醒连续重试。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{}}); err != nil {
		t.Fatal(err)
	}
	loop.cycle(ctx)
	loop.unitsWG.Wait() // 调度启动即返；收拢真实单位，再消费落地结果。
	if !loop.cycle(ctx) {
		t.Fatalf("拒收—回流—纠偏后任务应经机器判定完成(lastErr=%v)", loop.LastErr())
	}
	// 幽灵意图确实被守卫拒(图上无 ghost 覆盖边,重派成功后 s1 done)。
	steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil || len(steps) != 1 || steps[0].Status != "done" {
		t.Fatalf("重派意图应恰 1 步且 done: err=%v steps=%+v", err, steps)
	}
	covers, err := store.Edges(ctx, task, rings.EdgeCovers)
	if err != nil || len(covers) != 1 || covers[0].To != "a1" {
		t.Fatalf("重派意图应 covers 真实资产 a1: err=%v %+v", err, covers)
	}
	// 字段传递:第二次拆解的用户提示里带拒因节与拒收摘要。
	if !sawReject.Load() {
		t.Fatalf("第二次拆解未看到拒因回流(字段传递失败),拆解请求: %+v", model.decomposeRequests())
	}
	ds := model.decomposeRequests()
	if len(ds) < 2 {
		t.Fatalf("至少两次拆解(拒收一轮+重派一轮),实际 %d", len(ds))
	}
	if !strings.Contains(ds[1].LastUser, "上轮提交被拒") ||
		!strings.Contains(ds[1].LastUser, "意图「探测幽灵资产」被拒") {
		t.Fatalf("第二次拆解提示应含拒因摘要:\n%s", ds[1].LastUser)
	}
	if strings.Contains(ds[0].LastUser, "意图「探测幽灵资产」被拒") {
		t.Fatalf("首轮拆解前不应有拒因条目(节标题常驻、内容为无):\n%s", ds[0].LastUser)
	}
}
