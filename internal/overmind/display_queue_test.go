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

func TestLatestDisplayCoalescesPendingVersionsWithoutParallelSameObject(t *testing.T) {
	store := openStore(t)
	model := newGatedScriptedModel(func(*recordedRequest) scriptedReply { return scriptedReply{} })
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{Store: store, Provider: provider}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var mu sync.Mutex
	var versions []int
	first := func(context.Context, map[string]bool) {
		mu.Lock()
		versions = append(versions, 1)
		mu.Unlock()
		close(entered)
		<-release
	}
	l.queueLatestDisplay(t.Context(), "claim", "same", 1, first)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial display did not run")
	}
	for version := 2; version <= 100; version++ {
		v := version
		l.queueLatestDisplay(t.Context(), "claim", "same", int64(v), func(context.Context, map[string]bool) {
			mu.Lock()
			versions = append(versions, v)
			mu.Unlock()
		})
	}
	if l.queueLatestDisplay(t.Context(), "claim", "same", 99, func(context.Context, map[string]bool) { t.Error("older pending version replaced latest") }) {
		t.Fatal("out-of-order version accepted")
	}
	other := make(chan struct{})
	l.queueLatestDisplay(t.Context(), "claim", "other", 1, func(context.Context, map[string]bool) { close(other) })
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("one claim blocked unrelated work")
	}
	mu.Lock()
	before := append([]int(nil), versions...)
	mu.Unlock()
	if len(before) != 1 {
		t.Fatal("same object ran concurrently", before)
	}
	l.polishMu.Lock()
	pending := len(l.polishQueue)
	l.polishMu.Unlock()
	if pending != 1 {
		t.Fatalf("pending versions=%d want=1", pending)
	}
	unblock()
	l.polishWG.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 100 {
		t.Fatal("intermediate revisions generated", versions)
	}
}

func TestSupplementWhilePackagePendingKeepsFlowAndLatestFacts(t *testing.T) {
	const task, claim = "display-new-evidence", "tc-proof"
	store, ex := p502Base(t, task, claim, "http://t.example:8080/proof", "fixed-marker")
	p502Submit(t, store, task, claim, "s1", "http://t.example:8080/proof", "fixed-marker")
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var once sync.Once
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "复现步骤事实") {
			once.Do(func() { close(entered); <-release })
			return scriptedReply{Text: `{"steps":[{"seq":1,"short":"读取记录","desc":"请求 t.example:8080 的记录接口并核对响应内容","purpose":"核对已记录的响应"}]}`, Finish: "stop"}
		}
		if strings.Contains(rr.LastUser, "判断原文与相关事实") {
			if !strings.Contains(rr.LastUser, "f-latest") {
				t.Error("latest supplemental fact missing from summary")
			}
			return scriptedReply{Text: `{"title":"接口返回记录","statement":"测试请求返回了记录，已补充两项实际观察。","impact":"影响以实际取得的记录为限。","facts":[]}`, Finish: "stop"}
		}
		return scriptedReply{Text: `{"narrative":"测试请求返回了记录，随后核对了响应和访问前提。"}`, Finish: "stop"}
	})
	defer model.Close()
	model.release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{Store: store, TaskID: task, Swarm: ex, Provider: provider}
	result, err := l.CheckClaimOnSubmit(t.Context(), task, claim)
	if err != nil || result.Status != rings.TestClaimConfirmed {
		t.Fatal(result, err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial package generation did not start")
	}
	for _, id := range []string{"f-middle", "f-latest"} {
		if _, err := store.Append(t.Context(), task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeFact, Status: "confirmed", Title: "实际取得记录的补充观察", Detail: map[string]any{"claim_id": claim}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: claim, ToNode: id}},
		); err != nil {
			t.Fatal(err)
		}
		l.polishClaimTextAsync(t.Context(), claim)
	}
	unblock()
	l.polishWG.Wait()
	row, _, err := store.ClaimCaseOf(t.Context(), task, claim)
	if err != nil || row.Repro.StepsIncomplete != "" || len(row.Repro.Steps) != 1 || len(row.Summary.Facts) != 2 {
		t.Fatal("flow or supplemental facts lost", row, err)
	}
	counts := map[string]int{}
	for _, request := range model.requests() {
		switch {
		case strings.Contains(request.LastUser, "复现步骤事实"):
			counts["flow"]++
		case strings.Contains(request.LastUser, "判断原文与相关事实"):
			counts["summary"]++
		default:
			counts["narrative"]++
		}
	}
	if counts["flow"] != 2 || counts["summary"] != 1 || counts["narrative"] != 1 {
		t.Fatal("stale display continued or intermediate version ran", counts)
	}
	before := len(model.requests())
	if err := store.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := &Loop{Store: store, TaskID: task, Swarm: ex, Provider: provider}
	// Fact text itself is outside this test; only inspect the completed claim.
	source, _ := store.ClaimTextSourceRev(t.Context(), task, claim)
	done, err := store.HasClaimText(t.Context(), task, claim, source)
	if err != nil || !done {
		t.Fatal("latest display did not survive replay", err)
	}
	restarted.queueClaimTextAt(t.Context(), claim, source)
	restarted.polishWG.Wait()
	if len(model.requests()) != before {
		t.Fatal("completed source regenerated on restart")
	}
}
