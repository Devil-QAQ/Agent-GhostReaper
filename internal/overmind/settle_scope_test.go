package overmind

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/swarm"
)

func TestTaskGraceStopDoesNotCancelAnotherTasksUnit(t *testing.T) {
	store := openStore(t)
	model := newGatedScriptedModel(func(*recordedRequest) scriptedReply {
		return scriptedReply{Text: "人工调度样例完成", Finish: "stop"}
	})
	defer model.Close()
	release := sync.OnceFunc(model.release)
	defer release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := swarm.NewExecutor(store, provider, filepath.Join(t.TempDir(), "tasks"), swarm.Budget{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, other := make(chan struct{}), make(chan struct{})
	for _, task := range []string{"grace-one", "grace-other"} {
		if err := SeedGoal(ctx, store, task, "goal", "人工等待样例", nil); err != nil {
			t.Fatal(err)
		}
		mustSeedAsset(t, store, task, "asset", "https://fixture.example.test/")
		if err := DispatchStep(ctx, store, task, "step", "等待本地脚本响应", DispatchOptions{GoalID: "goal", ActionLevel: "只读探测", CoversAssets: []string{"asset"}}); err != nil {
			t.Fatal(err)
		}
		ch := first
		if task == "grace-other" {
			ch = other
		}
		go func(task string, done chan struct{}) { defer close(done); _, _ = ex.RunStep(ctx, task, "step") }(task, ch)
	}
	defer func() {
		cancel()
		release()
		for _, done := range []chan struct{}{first, other} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("unit did not close")
			}
		}
	}()
	waitFor(t, 5*time.Second, "两个任务的单位均在飞", func() bool {
		return len(ex.RunningUnitSteps("grace-one")) == 1 && len(ex.RunningUnitSteps("grace-other")) == 1
	})
	l := &Loop{TaskID: "grace-one", Store: store, Swarm: ex, SettleGraceSeconds: 1, deadline: time.Now().Add(-time.Second), runDone: make(chan struct{})}
	stopDone := l.armHardStop(ctx)
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("grace hard stop did not run")
	}
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("expired task's unit was not cancelled")
	}
	select {
	case <-other:
		t.Fatal("grace stop cancelled another task's unit")
	default:
	}
	if len(ex.RunningUnitSteps("grace-other")) != 1 {
		t.Fatal("unrelated task lost its running unit")
	}
}
