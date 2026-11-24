package rings

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppendReturnsAfterCommitNotificationReadsBatch(t *testing.T) {
	for _, checked := range []bool{false, true} {
		for _, raw := range []bool{false, true} {
			name := "append/typed"
			if checked {
				name = "checked/typed"
			}
			if raw {
				name += "/raw"
			}
			t.Run(name, func(t *testing.T) {
				store := openTestStore(t)
				const task = "commit-notify"
				node := NodeAdded{NodeID: "step-one", NodeType: NodeStep, Title: "原始内容", Detail: map[string]any{"marker": "before"}}
				batch := []Event{{Kind: EventNodeAdded, Actor: "test", Payload: node}}
				var encoded json.RawMessage
				if raw {
					encoded, _ = json.Marshal(node)
					batch[0].Payload = encoded
				}
				entered, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				observed := make(chan string, 1)
				// Test-only barrier exposes the ownership boundary. The actual
				// observer is still required to be non-blocking, as before.
				store.SetOnCommit(func(gotTask string, events []Event) {
					close(entered)
					<-release
					got := node
					if raw {
						if err := json.Unmarshal(events[0].Payload.(json.RawMessage), &got); err != nil {
							t.Error(err)
						}
					} else {
						got = events[0].Payload.(NodeAdded)
					}
					observed <- gotTask + ":" + got.Title + ":" + got.Detail["marker"].(string)
				})
				type result struct {
					revs []int64
					err  error
				}
				done := make(chan result, 1)
				go func() {
					var r result
					if checked {
						r.revs, r.err = store.AppendAtRevision(t.Context(), task, 0, batch...)
					} else {
						r.revs, r.err = store.Append(t.Context(), task, batch...)
					}
					done <- r
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("commit observer did not start")
				}
				var r result
				early := false
				select {
				case r = <-done:
					early = true
				case <-time.After(100 * time.Millisecond):
				}
				unblock()
				if !early {
					select {
					case r = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("append did not return after notification")
					}
				}
				if r.err != nil || len(r.revs) != 1 {
					t.Fatal(r)
				}
				if got := <-observed; got != task+":原始内容:before" {
					t.Fatal("notification read another batch", got)
				}
				// Append's caller can now reuse both the slice and its payload.
				batch[0] = Event{}
				node.Detail["marker"] = "after"
				for i := range encoded {
					encoded[i] = ' '
				}
				stored, err := store.Node(t.Context(), task, node.NodeID)
				if err != nil || stored.Title != node.Title || stored.Detail["marker"] != "before" {
					t.Fatal("committed content changed", stored, err)
				}
				if early {
					t.Error("Append returned while the commit notification still owned its event batch")
				}
			})
		}
	}
}

func TestCommitNotificationSkipsRollbackAndAllowsNoObserver(t *testing.T) {
	store := openTestStore(t)
	var calls atomic.Int32
	store.SetOnCommit(func(string, []Event) { calls.Add(1) })
	_, err := store.Append(t.Context(), "notify-rollback",
		Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: "rolled-back", NodeType: NodeStep, Title: "不应提交"}},
		Event{Kind: EventNodeStateChanged, Actor: "test", Payload: NodeStateChanged{NodeID: "missing", Status: "done"}},
	)
	if err == nil || calls.Load() != 0 {
		t.Fatal("rolled-back write notified observers", err, calls.Load())
	}
	if _, err := store.Node(t.Context(), "notify-rollback", "rolled-back"); err == nil {
		t.Fatal("failed batch was committed")
	}
	store.SetOnCommit(nil)
	if _, err := store.Append(t.Context(), "notify-no-observer", Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: "valid", NodeType: NodeStep, Title: "正常提交"}}); err != nil {
		t.Fatal(err)
	}
}
