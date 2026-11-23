package server

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/interject"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
	"github.com/Autumn-27/norma/llm"
)

type conversationThinkingModel struct {
	llm.Provider
	thought string
	started chan struct{}
	release chan struct{}
	once    sync.Once
	fail    bool
}

func (p *conversationThinkingModel) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 31}}, nil) {
			return
		}
		if p.thought != "" && !yield(llm.StreamEvent{Type: llm.SEThinkingDelta, Text: p.thought}, nil) {
			return
		}
		close(p.started)
		select {
		case <-ctx.Done():
			yield(llm.StreamEvent{}, ctx.Err())
			return
		case <-p.release:
		}
		if p.fail {
			yield(llm.StreamEvent{}, errors.New("fixed interrupted response"))
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "固定材料已核对。"}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 17}}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
func (p *conversationThinkingModel) unblock() { p.once.Do(func() { close(p.release) }) }

func TestConversationThinkingFormalRoutes(t *testing.T) {
	for _, route := range []string{"shepherd", "followup", "overmind", "unit"} {
		for _, mode := range []string{"thinking", "none", "interrupted"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				srv, mux, _ := newInterjectTestServer(t)
				const task = "thinking-formal"
				seedIJUnit(t, srv, task)
				_, err := srv.Store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Title: "固定材料事实", Status: "confirmed"}})
				if err != nil {
					t.Fatal(err)
				}
				model := &conversationThinkingModel{started: make(chan struct{}), release: make(chan struct{}), fail: mode == "interrupted"}
				if mode != "none" {
					model.thought = strings.Repeat("实际返回片段。", 800)
				}
				defer model.unblock()
				srv.Shepherd.Provider, srv.Interject.Provider = model, model
				var postPath, getPath, body string
				switch route {
				case "shepherd":
					postPath = "/shepherd/messages"
					getPath = "/shepherd"
					body = `{"text":"核对固定材料"}`
				case "followup":
					postPath = "/shepherd/claim/f1/messages"
					getPath = "/shepherd/claim/f1"
					body = `{"text":"核对固定材料"}`
				case "overmind":
					postPath = "/interject/messages"
					getPath = "/interject?target=overmind"
					body = `{"target":"overmind","mode":"ask","text":"核对固定材料"}`
				case "unit":
					postPath = "/interject/messages"
					getPath = "/interject?target=unit&session=swarm-u1"
					body = `{"target":"unit","unit_id":"u1","step_id":"s1","session_id":"swarm-u1","mode":"ask","text":"核对固定材料"}`
				}
				prefix := "/api/tasks/" + task
				if code, out := callJSON(t, mux, http.MethodPost, prefix+postPath, body); code != 200 {
					t.Fatalf("enqueue: %d %v", code, out)
				}
				select {
				case <-model.started:
				case <-time.After(5 * time.Second):
					t.Fatal("model did not start")
				}
				latestBusy := false
				read := func() map[string]any {
					t.Helper()
					code, out := callJSON(t, mux, http.MethodGet, prefix+getPath, "")
					if code != 200 {
						t.Fatalf("read: %d %v", code, out)
					}
					latestBusy, _ = out["busy"].(bool)
					msgs, _ := out["messages"].([]any)
					if len(msgs) != 1 {
						t.Fatalf("message missing: %v", out)
					}
					return msgs[0].(map[string]any)
				}
				before := read()
				if before["reply"] != "" && before["reply"] != nil {
					t.Fatal("premature answer")
				}
				if mode != "none" && (before["thinking"] != model.thought || before["thinking_active"] != true) {
					t.Fatal("actual thinking not visible before response completed")
				}
				if mode == "none" && before["thinking"] != nil {
					t.Fatal("invented thinking for no-thinking model")
				}
				model.unblock()
				var final map[string]any
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					final = read()
					if (final["state"] == "replied" || final["state"] == "failed") && !latestBusy {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				wantState := "replied"
				if model.fail {
					wantState = "failed"
				}
				if final["state"] != wantState {
					t.Fatalf("unexpected final state: %v", final["state"])
				}
				if mode != "none" && final["thinking"] != model.thought {
					t.Fatal("thinking truncated or duplicated")
				}
				// Reconstruct through fresh service instances; no in-memory text cache.
				srv.Shepherd = &shepherd.Service{Store: srv.Store, RootDir: srv.RootDir}
				srv.Interject = &interject.Service{Store: srv.Store, RootDir: srv.RootDir}
				if restored := read(); restored["thinking"] != final["thinking"] {
					t.Fatal("thinking not durable")
				}
				rows, err := srv.Store.TokenUsageRows(t.Context(), task)
				if err != nil || len(rows) != 1 || rows[0].InputTokens != 31 {
					t.Fatalf("stream cost missing or duplicate: %+v %v", rows, err)
				}
				if model.fail && rows[0].Purpose != "interrupted" {
					t.Fatal("partial failed request not metered")
				}
			})
		}
	}
}

func TestLiveConversationTopics(t *testing.T) {
	srv, ts, _ := newLiveTestServer(t)
	const task = "live-conversation"
	createLiveTask(t, srv, task)
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})
	for _, rec := range []struct {
		event rings.Event
		topic string
	}{
		{rings.Event{Kind: rings.EventShepherdRecord, Actor: "shepherd", Payload: rings.ShepherdRecord{MessageID: "m1", Type: rings.ShepherdRecThinking, Text: "真实片段"}}, "shepherd:" + task},
		{rings.Event{Kind: rings.EventShepherdRecord, Actor: "shepherd", Payload: rings.ShepherdRecord{MessageID: "m2", ClaimID: "f1", Round: 1, Type: rings.ShepherdRecThinking, Text: "真实片段"}}, "shepherd:" + task + ":f1"},
		{rings.Event{Kind: rings.EventInterjectRecord, Actor: "interject", Payload: rings.InterjectRecord{MessageID: "m3", Target: "overmind", Type: rings.InterjectRecThinking, Text: "真实片段"}}, "interject:" + task + ":overmind:"},
		{rings.Event{Kind: rings.EventInterjectRecord, Actor: "interject", Payload: rings.InterjectRecord{MessageID: "m4", Target: "unit", SessionID: "swarm-u1", Type: rings.InterjectRecThinking, Text: "真实片段"}}, "interject:" + task + ":unit:swarm-u1"},
	} {
		commitEvents(t, srv, task, rec.event)
		c.expectMsg(2*time.Second, map[string]any{"t": "dirty", "k": rec.topic})
	}
}
