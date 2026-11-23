package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

func seedDisplayFacts(t *testing.T, store *rings.Store, task string, count int) []string {
	t.Helper()
	var ids []string
	var events []rings.Event
	for i := range count {
		id := fmt.Sprintf("fact-%03d", i)
		ids = append(ids, id)
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeFact, Status: "confirmed", Title: "固定观察 " + id, Detail: map[string]any{"evidence": "固定响应"}}})
	}
	if _, err := store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	return ids
}

func cleanDisplayFactReply(t *testing.T, rr *recordedRequest) scriptedReply {
	t.Helper()
	_, raw, ok := strings.Cut(rr.LastUser, "事实原文与证据摘要:\n")
	var materials []factTextMaterial
	if !ok || json.Unmarshal([]byte(raw), &materials) != nil {
		t.Errorf("unexpected generation request: %.120s", rr.LastUser)
		return scriptedReply{Text: "{}", Finish: "stop"}
	}
	var facts []rings.FactTextPolish
	for _, m := range materials {
		facts = append(facts, rings.FactTextPolish{FactID: m.FactID, Title: "固定观察", Statement: m.Statement})
	}
	body, _ := json.Marshal(map[string]any{"facts": facts})
	return scriptedReply{Text: string(body), Finish: "stop"}
}

func TestDisplayQueueBoundsOverflowRecoversAndRestartDoesNotRegenerate(t *testing.T) {
	store := openStore(t)
	const task = "display-bounded"
	ids := seedDisplayFacts(t, store, task, 160)
	free := make(chan struct{})
	release := sync.OnceFunc(func() { close(free) })
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply { <-free; return cleanDisplayFactReply(t, rr) })
	defer model.Close()
	defer release()
	model.release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{TaskID: task, Store: store, Provider: provider}
	loop.polishConfirmedFactsAsync(t.Context(), ids)
	waitFor(t, 2*time.Second, "两项显示生成实际在途", func() bool { return len(model.requests()) == displayWorkers })
	loop.polishMu.Lock()
	active, backlog, deferred := loop.polishActive, len(loop.polishQueue), loop.polishDeferred
	loop.polishMu.Unlock()
	if active > displayWorkers || backlog > displayBacklog || !deferred {
		t.Fatalf("active=%d backlog=%d deferred=%t", active, backlog, deferred)
	}
	for range 10 {
		loop.polishConfirmedFactsAsync(t.Context(), ids)
	}
	if len(model.requests()) != displayWorkers {
		t.Fatal("duplicate notifications started extra requests")
	}
	release()
	loop.polishWG.Wait()
	texts, err := store.NodePolish(t.Context(), task)
	if err != nil || len(texts) != len(ids) {
		t.Fatalf("deferred work lost: texts=%d want=%d err=%v", len(texts), len(ids), err)
	}
	seen := map[string]int{}
	for _, rr := range model.requests() {
		_, raw, _ := strings.Cut(rr.LastUser, "事实原文与证据摘要:\n")
		var rows []factTextMaterial
		_ = json.Unmarshal([]byte(raw), &rows)
		for _, row := range rows {
			seen[row.FactID]++
		}
	}
	for _, id := range ids {
		if seen[id] != 1 || texts[id].SourceRev == 0 || texts[id].Statement != "固定观察 "+id {
			t.Fatalf("source %s generated %d times or lost content: %+v", id, seen[id], texts[id])
		}
	}
	before := len(model.requests())
	restarted := &Loop{TaskID: task, Store: store, Provider: provider}
	restarted.recoverDisplay(t.Context())
	restarted.polishWG.Wait()
	if len(model.requests()) != before {
		t.Fatal("restart regenerated already completed source versions")
	}
}

func TestCancelledDisplayStopsAndMissingDraftRecovers(t *testing.T) {
	store := openStore(t)
	const task = "display-cancel"
	ids := seedDisplayFacts(t, store, task, 40)
	held := newP442HangModel()
	defer held.Close()
	defer held.release()
	ctx, cancel := context.WithCancel(t.Context())
	loop := &Loop{TaskID: task, Store: store, Provider: held.provider(t), polishCtx: ctx}
	loop.polishConfirmedFactsAsync(ctx, ids)
	waitFor(t, 2*time.Second, "显示请求已在途", func() bool { return held.requests() > 0 })
	cancel()
	done := make(chan struct{})
	go func() { loop.polishWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled display kept the task waiting")
	}
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply { return cleanDisplayFactReply(t, rr) })
	defer model.Close()
	model.release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Loop{TaskID: task, Store: store, Provider: provider}
	restarted.recoverDisplay(t.Context())
	restarted.polishWG.Wait()
	texts, err := store.NodePolish(t.Context(), task)
	if err != nil || len(texts) != len(ids) {
		t.Fatalf("missing display not recovered: %d %v", len(texts), err)
	}
}
