package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

func TestExecutionDrainsActualReturnBeforeEndAndRejectsLateCallbacks(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "execution-drain"
	seedGraph(t, store, task, "核对本地观察")
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(t.Context(), task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	env, _, _, err := ex.beginUnit(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	env.openSegment(cancel)
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	inner := tool.Build(tool.Spec{Name: "slow_local_observation", Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
		close(entered)
		<-release
		return tool.Text("actual-observation-after-cancel"), nil
	}})
	guard := newToolGuard(env, pack, nil, 100, time.Time{}, 0)
	wrapped := guard.wrapAll([]tool.CoreTool{inner})[0]
	go func() {
		_, err := wrapped.Call(ctx, json.RawMessage(`{}`), &tool.ToolContext{ToolUseID: "call-one"})
		returned <- err
	}()
	<-entered
	drained := make(chan struct{})
	go func() { env.drainCallbacks(true); close(drained) }()
	waitFor(t, 2*time.Second, "关闭新调用入口", func() bool { env.callsMu.Lock(); defer env.callsMu.Unlock(); return env.executionClosed })
	select {
	case <-drained:
		t.Fatal("execution ended before actual callback returned")
	default:
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("actual return not saved")
	}
	<-drained
	revs, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:" + env.UnitID, Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}}, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID, Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionEnd, StepID: "s1", UnitID: env.UnitID, TranscriptID: env.TranscriptID, Note: "status=done"}})
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := store.Events(t.Context(), task, 0, 0)
	observed := 0
	var startRev, resultRev int64
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		json.Unmarshal(ev.Payload, &r)
		if r.ToolCallID == "call-one" {
			if r.Type == rings.SwarmRecToolStarted {
				startRev = ev.Revision
				continue
			}
			observed++
			resultRev = ev.Revision
			if ev.Revision >= revs[0] || r.ResultHead != "actual-observation-after-cancel" {
				t.Fatal("observation lost or written after end")
			}
		}
	}
	if observed != 1 {
		t.Fatal("actual observation count", observed)
	}
	if startRev <= 0 || startRev >= resultRev || resultRev >= revs[0] {
		t.Fatal("actual start/result/end order lost")
	}
	var reviews atomic.Int32
	ex.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		reviews.Add(1)
		return ActionReview{Decision: ActionApproval, Reason: "test"}, nil
	}
	d := env.approvalGate(pack)(context.Background(), "read", json.RawMessage(`{"path":"notes.txt"}`), permission.Context{})
	if d.Behavior != permission.Deny || reviews.Load() != 0 {
		t.Fatal("late callback entered policy review")
	}
	if _, err := env.SubmitCandidateFact(context.Background(), "late fact", "actual observation", "", nil); !errors.Is(err, rings.ErrExecutionEnded) {
		t.Fatal("late business accepted", err)
	}
}

func TestLedgerGateFailedDecisionRetainsDurableRequest(t *testing.T) {
	store := openStore(t, t.TempDir())
	gate := NewLedgerGate(store)
	ticket, err := gate.Enter(ApprovalRequest{TaskID: "task", StepID: "s", UnitID: "u", ActionLevel: rings.ActionReadOnly, ToolName: "read", ToolArgs: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gate.Decide(t.Context(), "task", ticket.Req.ID, GateDecision{Approved: true}); err == nil {
		t.Fatal("failed persistence reported approval")
	}
	if len(gate.Pending("task")) != 1 || len(ticket.ch) != 0 {
		t.Fatal("failed decision lost request or notified execution")
	}
}

func TestLedgerGateRejectsDecisionAfterOwningExecutionEnded(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "gate-ended-owner"
	seedGraph(t, store, task, "观察")
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(t.Context(), task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	env, _, _, err := ex.beginUnit(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewLedgerGate(store)
	ticket, err := gate.Enter(ApprovalRequest{TaskID: task, StepID: "s1", UnitID: env.UnitID, ActionLevel: rings.ActionReadOnly, ToolName: "read", ToolArgs: `{}`, owner: env.owner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: "s1", Status: "aborted"}}); err != nil {
		t.Fatal(err)
	}
	if err := gate.Decide(t.Context(), task, ticket.Req.ID, GateDecision{Approved: true}); !errors.Is(err, ErrGateRequestNotFound) {
		t.Fatal("late approval applied", err)
	}
	if len(gate.Pending(task)) != 0 {
		t.Fatal("orphan approval remains")
	}
	rows, err := store.ApprovalByRequest(t.Context(), task, ticket.Req.ID)
	if err != nil || len(rows) != 2 || rows[1].Type != rings.ApprovalRecDenied || rows[1].DenyLine != rings.DenyLineCloseout {
		t.Fatal("late decision did not close truthfully", rows, err)
	}
	node, _ := store.Node(t.Context(), task, "s1")
	if node.Status != "aborted" {
		t.Fatal("late approval revived step")
	}
}
