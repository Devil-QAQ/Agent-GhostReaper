package swarm

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

func ownedHelmFixture(t *testing.T) (*Executor, *unitEnv, *TaskPack) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	seedGraph(t, store, "owned-helm", "核对固定材料")
	e := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := e.Claim(t.Context(), "owned-helm", "s1")
	if err != nil {
		t.Fatal(err)
	}
	env, _, _, err := e.beginUnit(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	e.Helm.attachTask(env.TaskID, e.killUnitByStep)
	return e, env, pack
}

func replaceHelmExecution(t *testing.T, e *Executor, old *unitEnv) *unitEnv {
	t.Helper()
	_, err := e.Store.Append(t.Context(), old.TaskID,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:" + old.UnitID, Payload: rings.NodeStateChanged{NodeID: old.StepID, Status: "done"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + old.UnitID, Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionEnd, StepID: old.StepID, UnitID: old.UnitID, Note: "status=done"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: old.StepID, Status: "dispatched"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	delete(e.stepUnits, steerKey(old.TaskID, old.StepID))
	e.mu.Unlock()
	pack, err := e.Claim(t.Context(), old.TaskID, old.StepID)
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, err := e.beginUnit(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestOwnedHelmDoesNotTransferToReplacementUnit(t *testing.T) {
	e, old, pack := ownedHelmFixture(t)
	id, err := e.Helm.SteerWorkForExecution(t.Context(), old.TaskID, *old.owner, "依据已有观察修正方向", ActorOvermindHelm)
	if err != nil {
		t.Fatal(err)
	}
	next := replaceHelmExecution(t, e, old)
	dirs, err := e.Helm.takeForUnit(next.TaskID, next.StepID, next.UnitID)
	if err != nil || len(dirs) != 0 {
		t.Fatal("bound guidance transferred to a new execution")
	}
	recs := steerRecordsFromLedger(t, e.Store, old.TaskID)
	latest := recs[len(recs)-1]
	if latest.SteerID != id || latest.State != rings.SteerStateStale || latest.UnitID != old.UnitID {
		t.Fatal("stale guidance has no truthful receipt")
	}
	if _, err := e.Helm.SteerWorkForExecution(t.Context(), old.TaskID, *old.owner, "late old plan", ActorOvermindHelm); !errors.Is(err, rings.ErrExecutionEnded) {
		t.Fatal("stale model plan queued a new steer")
	}
	var canceled atomic.Bool
	e.mu.Lock()
	e.stepUnits[steerKey(next.TaskID, next.StepID)] = next.UnitID
	e.unitCancels[next.UnitID] = func() { canceled.Store(true) }
	e.mu.Unlock()
	if _, err := e.Helm.KillWorkForExecution(t.Context(), old.TaskID, *old.owner, "old decision", ActorOvermindHelm); !errors.Is(err, rings.ErrExecutionEnded) || canceled.Load() {
		t.Fatal("stale model plan stopped the replacement unit")
	}
	guard := newToolGuard(old, pack, e.Helm, 100, time.Time{}, 0)
	if err := guard.recordDeliveredBatch([]rings.SteerRecord{{SteerID: "late-delivery", StepID: old.StepID, UnitID: old.UnitID, Command: rings.SteerCommandSteer, Message: "old unit"}}, 1); !errors.Is(err, rings.ErrExecutionEnded) {
		t.Fatal("late delivery crossed the actual execution boundary")
	}
}

func TestOwnedKillRechecksLiveUnitAfterDurableAdmission(t *testing.T) {
	e, old, _ := ownedHelmFixture(t)
	var newCanceled atomic.Bool
	e.Helm.attachTask(old.TaskID, func(task, step, expected string) (string, bool) {
		next := replaceHelmExecution(t, e, old)
		e.mu.Lock()
		e.stepUnits[steerKey(task, step)] = next.UnitID
		e.unitCancels[next.UnitID] = func() { newCanceled.Store(true) }
		e.mu.Unlock()
		return e.killUnitByStep(task, step, expected)
	})
	if _, err := e.Helm.KillWorkForExecution(t.Context(), old.TaskID, *old.owner, "前提失效", ActorOvermindHelm); err == nil || newCanceled.Load() {
		t.Fatal("replacement execution was killed between admission and application")
	}
	recs := steerRecordsFromLedger(t, e.Store, old.TaskID)
	if len(recs) != 2 || recs[0].State != rings.SteerStateQueued || recs[1].State != rings.SteerStateStale {
		t.Fatalf("control outcome missing: %+v", recs)
	}
}

func TestSteerReceiptRequiresActualDeliveryAndSurvivesRecreation(t *testing.T) {
	e, env, pack := ownedHelmFixture(t)
	id, err := e.Helm.SteerWorkForExecution(t.Context(), env.TaskID, *env.owner, "核对已有材料后继续", ActorOvermindHelm)
	if err != nil {
		t.Fatal(err)
	}
	r := steerReceipt{SteerID: id, Adopted: true}
	if err := e.Helm.RecordVerdict(env.TaskID, env.StepID, env.UnitID, r); err == nil {
		t.Fatal("queued instruction falsely acknowledged as adopted")
	}
	dirs, err := e.Helm.takeForUnit(env.TaskID, env.StepID, env.UnitID)
	if err != nil || len(dirs) != 1 {
		t.Fatal("pending guidance lost")
	}
	g := newToolGuard(env, pack, e.Helm, 100, time.Time{}, 0)
	if err := g.recordDeliveredBatch(dirs, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.Helm.RecordVerdict(env.TaskID, env.StepID, "different-unit", r); err == nil {
		t.Fatal("another execution acknowledged the instruction")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := NewHelm(e.Store).RecordVerdict(env.TaskID, env.StepID, env.UnitID, r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	adopted := 0
	for _, rec := range steerRecordsFromLedger(t, e.Store, env.TaskID) {
		if rec.SteerID == id && rec.State == rings.SteerStateAdopted {
			adopted++
		}
	}
	if adopted != 1 {
		t.Fatalf("durable receipt duplicated %d times", adopted)
	}
	if err := NewHelm(e.Store).RecordVerdict(env.TaskID, env.StepID, env.UnitID, r); err != nil {
		t.Fatal("recreated helm forgot durable acknowledgement")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Helm.RecordVerdict(env.TaskID, env.StepID, env.UnitID, r); err == nil {
		t.Fatal("unavailable ledger advertised a saved receipt")
	}
}

func TestOldUnitCannotConsumeOrExpireReplacementGuidance(t *testing.T) {
	e, old, _ := ownedHelmFixture(t)
	next := replaceHelmExecution(t, e, old)
	id, err := e.Helm.SteerWork(t.Context(), next.TaskID, next.StepID, "读取已有证据再继续", "creator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Helm.takeForUnit(old.TaskID, old.StepID, old.UnitID); !errors.Is(err, rings.ErrExecutionEnded) {
		t.Fatalf("late old unit consumed replacement queue: %v", err)
	}
	e.Helm.expirePending(old.TaskID, old.StepID, old.UnitID, "旧单位退出")
	dirs, err := e.Helm.takeForUnit(next.TaskID, next.StepID, next.UnitID)
	if err != nil || len(dirs) != 1 || dirs[0].SteerID != id || dirs[0].UnitID != next.UnitID {
		t.Fatalf("replacement guidance lost: %+v %v", dirs, err)
	}
	rec, err := e.Store.LatestSteerRecord(t.Context(), next.TaskID, id)
	if err != nil || rec.State != rings.SteerStateQueued {
		t.Fatalf("guidance falsely delivered or expired: %+v %v", rec, err)
	}
}
