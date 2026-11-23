package rings

import (
	"context"
	"errors"
	"testing"
)

func TestGeneratedDisplaySourceIsStableAndReplayKeepsHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	const task, id = "display-version", "fact-one"
	mustAppend(t, store, task, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: id, NodeType: NodeStep, Status: "dispatched", Title: "初始观察"}})
	sources, _ := store.NodeTextSourceRevs(ctx, task)
	first := StepTitlePolish{StepID: id, SourceRev: sources[id], Title: "名称", Statement: "第一次观察"}
	if _, err := store.AppendDisplay(ctx, task, Event{Kind: EventStepTitle, Actor: "overmind", Payload: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDisplay(ctx, task, Event{Kind: EventStepTitle, Actor: "overmind", Payload: first}); !errors.Is(err, ErrDisplayAlreadyGenerated) {
		t.Fatalf("same source duplicated: %v", err)
	}
	mustAppend(t, store, task, Event{Kind: EventNodeStateChanged, Actor: "test", Payload: NodeStateChanged{NodeID: id, Status: "done"}})
	unchanged, _ := store.NodeTextSourceRevs(ctx, task)
	if unchanged[id] != first.SourceRev {
		t.Fatal("execution status changed the content version")
	}
	title := "更新后的观察"
	mustAppend(t, store, task, Event{Kind: EventNodeAmended, Actor: "creator", Payload: NodeAmended{NodeID: id, Title: &title, Note: "更新材料"}})
	if texts, _ := store.NodePolish(ctx, task); len(texts) != 0 {
		t.Fatal("old draft still masked the new source")
	}
	current, _ := store.NodeTextSourceRevs(ctx, task)
	second := StepTitlePolish{StepID: id, SourceRev: current[id], Title: "新名称", Statement: "更新后的说明"}
	if _, err := store.AppendDisplay(ctx, task, Event{Kind: EventStepTitle, Actor: "overmind", Payload: second}); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildProjections(ctx); err != nil {
		t.Fatalf("history with later source edits cannot replay: %v", err)
	}
	texts, _ := store.NodePolish(ctx, task)
	if texts[id].SourceRev != second.SourceRev || texts[id].Statement != second.Statement {
		t.Fatal("replayed draft has the wrong source")
	}
}

func TestUnfinishedOldDisplayCannotPublishAfterSourceEdit(t *testing.T) {
	store := openTestStore(t)
	const task, id = "late-display", "intent-one"
	mustAppend(t, store, task, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: id, NodeType: NodeStep, Status: "dispatched", Title: "原意图"}})
	sources, _ := store.NodeTextSourceRevs(t.Context(), task)
	title := "新意图"
	mustAppend(t, store, task, Event{Kind: EventNodeAmended, Actor: "creator", Payload: NodeAmended{NodeID: id, Title: &title}})
	before, _ := store.CurrentRevision(t.Context(), task)
	if _, err := store.AppendDisplay(t.Context(), task, Event{Kind: EventStepTitle, Actor: "overmind", Payload: StepTitlePolish{StepID: id, SourceRev: sources[id], Title: "旧稿"}}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("late draft: %v", err)
	}
	if after, _ := store.CurrentRevision(t.Context(), task); after != before {
		t.Fatal("rejected draft left a partial event")
	}
}
