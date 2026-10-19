package overmind

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-07 纳谏:牧者建议的主宰裁决(快照注入 → 回复裁决 → 入账) ----

// adviceEvents 任务里全部建议与裁决事件(读面)。
func adviceEvents(t *testing.T, store *rings.Store, taskID string) (int, []rings.ShepherdRecord) {
	t.Helper()
	evs, err := store.Events(context.Background(), taskID, 0, 0)
	if err != nil {
		t.Fatalf("读事件失败: %v", err)
	}
	pending, verdicts := 0, []rings.ShepherdRecord{}
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) != nil {
			continue
		}
		switch rec.Type {
		case rings.ShepherdRecAdvice:
			pending++
		case rings.ShepherdRecAdviceVerdict:
			verdicts = append(verdicts, rec)
		}
	}
	return pending, verdicts
}

// TestSnapshotInjectsPendingAdvices 快照注入:待裁决建议逐条呈现;裁决
// 后不再出现。
func TestSnapshotInjectsPendingAdvices(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "adv-snap-task"
	if err := SeedGoal(ctx, store, task, "g1", "目标", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: "shepherd",
		Payload: rings.ShepherdRecord{Type: rings.ShepherdRecAdvice,
			AdviceID: "av-1", Text: "优先覆盖 web 入口"},
	}); err != nil {
		t.Fatalf("种建议失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}
	snap, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if !strings.Contains(snap, "待裁决建议") || !strings.Contains(snap, "av-1") ||
		!strings.Contains(snap, "优先覆盖 web 入口") {
		t.Fatalf("快照应含待裁决建议:\n%s", snap)
	}

	// 裁决后:不再出现在快照。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: ActorOvermind,
		Payload: rings.ShepherdRecord{Type: rings.ShepherdRecAdviceVerdict,
			AdviceID: "av-1", State: rings.ShepherdAdviceRejected, Reason: "web 资产不在图上"},
	}); err != nil {
		t.Fatalf("种裁决失败: %v", err)
	}
	snap, _, err = l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if strings.Contains(snap, "av-1") {
		t.Fatalf("裁决后的建议不应再出现在快照:\n%s", snap)
	}
	if !strings.Contains(snap, "(无)") {
		t.Fatalf("无待裁决建议时应如实标注:\n%s", snap)
	}
}

// TestAdviceAdjudicationE2E 裁决入账:拆解回复的 advices 落 advice_verdict
// 事件(actor=overmind,带理由);幽灵/重复裁决不入账(幂等);裁决
// 字段经真实引擎链路(快照 → 用户提示)。
func TestAdviceAdjudicationE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "adv-e2e-task"
	if err := SeedGoal(ctx, store, task, "g1", "确认暴露面", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: "shepherd",
		Payload: rings.ShepherdRecord{Type: rings.ShepherdRecAdvice,
			AdviceID: "av-7", Text: "从子域名枚举切入"},
	}); err != nil {
		t.Fatalf("种建议失败: %v", err)
	}

	var sawAdvice atomic.Bool
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "av-7") && strings.Contains(rr.LastUser, "从子域名枚举切入") {
				sawAdvice.Store(true)
				// 采纳建议(本轮以新意图体现)+ 一条幽灵裁决与一条空理由裁决
				//(均不应入账)。
				return scriptedReply{Text: `{"assessment":{"note":"收到建议"},"new_intents":[{"intent":"子域名枚举","expected_observation":"子域名清单","action_level":"只读探测"}],"advices":[{"id":"av-7","adopt":true,"reason":"图上无子域名覆盖,采纳后本轮派发枚举意图"},{"id":"av-ghost","adopt":false,"reason":"不存在"},{"id":"av-7","adopt":false,"reason":""}]}`}
			}
			return scriptedReply{Text: `{"assessment":{"note":"无新变化"}}`}
		}
		return scriptedReply{Text: "意图完成。"}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}

	if loop.cycle(ctx) {
		t.Fatalf("目标未达成不应判完成")
	}
	if !sawAdvice.Load() {
		t.Fatalf("拆解请求应看到待裁决建议(字段传递失败): %+v", model.decomposeRequests())
	}
	_, verdicts := adviceEvents(t, store, task)
	if len(verdicts) != 1 {
		t.Fatalf("应恰 1 条裁决入账(幽灵与空理由不入账),实际 %d: %+v", len(verdicts), verdicts)
	}
	v := verdicts[0]
	if v.AdviceID != "av-7" || v.State != rings.ShepherdAdviceAdopted || !strings.Contains(v.Reason, "本轮派发") {
		t.Fatalf("裁决内容不符: %+v", v)
	}
	// 采纳体现:建议驱动的意图已派发(步骤标题含子域名)。
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	if len(steps) != 1 || !strings.Contains(steps[0].Title, "子域名") {
		t.Fatalf("采纳建议应体现为意图派发: %+v", steps)
	}
	// 幂等:再跑一轮(建议已裁决),模型不再看到待裁决建议,零新增裁决。
	if loop.cycle(ctx) {
		t.Fatalf("目标未达成不应判完成")
	}
	_, verdicts = adviceEvents(t, store, task)
	if len(verdicts) != 1 {
		t.Fatalf("重复裁决不得入账,实际 %d", len(verdicts))
	}
}

// ---- P2-08:插话建议与牧者建议同一裁决语义 ----

// TestInterjectAdviceJudgedSamePath 插话子会话提交的建议(interject.record
// advice)与牧者建议同一裁决路径:快照注入可见;裁决写回同源事件
// (interject.record advice_verdict),裁决后不再待决。
func TestInterjectAdviceJudgedSamePath(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "adv-ij-task"
	if err := SeedGoal(ctx, store, task, "g1", "目标", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventInterjectRecord, Actor: "creator",
		Payload: rings.InterjectRecord{Target: "overmind", Type: rings.InterjectRecAdvice,
			AdviceID: "av-ij1", Text: "插话建议:优先验证上传接口"},
	}); err != nil {
		t.Fatalf("种插话建议失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}
	snap, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if !strings.Contains(snap, "插话建议:优先验证上传接口") || !strings.Contains(snap, "av-ij1") {
		t.Fatalf("快照应注入插话建议: %s", snap)
	}
	// 裁决(采纳):写回 interject.record advice_verdict。
	l.writeAdviceVerdicts(ctx, []adviceVerdictPlan{{ID: "av-ij1", Adopt: true, Reason: "图上未覆盖上传面"}})
	advices, err := l.pendingAdvices(ctx)
	if err != nil || len(advices) != 0 {
		t.Fatalf("裁决后不应有待决建议: %+v %v", advices, err)
	}
	evs, err := store.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("读事件失败: %v", err)
	}
	verdictSeen := false
	for _, ev := range evs {
		if ev.Kind != rings.EventInterjectRecord {
			continue
		}
		var rec rings.InterjectRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.Type == rings.InterjectRecAdviceVerdict && rec.AdviceID == "av-ij1" &&
			rec.State == rings.ShepherdAdviceAdopted {
			verdictSeen = true
		}
	}
	if !verdictSeen {
		t.Fatalf("裁决应写回 interject.record advice_verdict(同源)")
	}
}
