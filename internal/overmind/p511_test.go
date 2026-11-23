package overmind

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestP511DispatchedWorkStartsWhileAssessmentIsHeld(t *testing.T) {
	store := openStore(t)
	model := newP442HangModel()
	defer model.Close()
	release := sync.OnceFunc(model.release)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const task = "p511-held-assessment"
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created"}}, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "started"}}); err != nil {
		t.Fatal(err)
	}
	if err := SeedGoal(ctx, store, task, "goal", "核对调度，不执行目标操作", nil); err != nil {
		t.Fatal(err)
	}
	mustSeedAsset(t, store, task, "asset", "https://fixture.example.test/")
	provider := model.provider(t)
	root := filepath.Join(t.TempDir(), "tasks")
	executor := swarm.NewExecutor(store, provider, root, swarm.Budget{})
	loop := &Loop{TaskID: task, Store: store, Provider: provider, Swarm: executor, RootDir: root, MaxConcurrentUnits: 1, HeartbeatInterval: time.Hour}
	dispatch := func(id string) {
		t.Helper()
		if err := DispatchStep(ctx, store, task, id, "人工调度样例，不调用目标工具", DispatchOptions{GoalID: "goal", ActionLevel: "只读探测", CoversAssets: []string{"asset"}}); err != nil {
			t.Fatal(err)
		}
	}
	dispatch("first")
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	done := make(chan struct{})
	started := time.Now()
	go func() { defer close(done); loop.Run(ctx, wake) }()
	defer func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("loop failed to stop")
		}
	}()
	starts := func(step string) int {
		events, err := store.Events(ctx, task, 0, 0)
		if err != nil {
			return 0
		}
		count := 0
		for _, event := range events {
			if event.Kind != rings.EventSwarmRecord {
				continue
			}
			var rec rings.SwarmRecord
			if json.Unmarshal(event.Payload, &rec) == nil && rec.Type == rings.SwarmRecSessionStarted && rec.StepID == step {
				count++
			}
		}
		return count
	}
	waitFor(t, 5*time.Second, "首单位出生且研判请求仍被挂住", func() bool { return starts("first") == 1 && model.requests() >= 2 })
	t.Logf("initial wake to first unit: %dms; assessment not released", time.Since(started).Milliseconds())
	waitFor(t, 5*time.Second, "首单位完成，名额释放", func() bool { n, e := store.Node(ctx, task, "first"); return e == nil && n.Status == "done" })
	dispatch("second")
	queued := time.Now()
	for range 50 {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	waitFor(t, 5*time.Second, "研判仍挂住时，后续唤醒直接领取", func() bool { return starts("second") == 1 })
	t.Logf("new dispatch wake to unit: %dms; same assessment still held", time.Since(queued).Milliseconds())
	waitFor(t, 5*time.Second, "第二单位完成", func() bool { n, e := store.Node(ctx, task, "second"); return e == nil && n.Status == "done" })
	if starts("first") != 1 || starts("second") != 1 {
		t.Fatal("repeated wakes duplicated an execution")
	}
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "new-observation", NodeType: rings.NodeFact, Title: "固定观察", Status: "candidate", Detail: map[string]any{"evidence": "固定响应记录"}}}); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	waitFor(t, 2*time.Second, "研判仍挂住时，事实及时确认", func() bool {
		n, e := store.Node(ctx, task, "new-observation")
		return e == nil && n.Status == "confirmed"
	})
}
