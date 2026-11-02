package rings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestAppendAtRevisionAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	add := func(id string) Event {
		return Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: id, NodeType: NodeStep, Status: "dispatched"}}
	}
	if _, err = s.AppendAtRevision(ctx, "t", 0, add("s1")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendAtRevision(ctx, "t", 0, add("s2")); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("未拒过期写: %v", err)
	}
	if _, err = s.Node(ctx, "t", "s2"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("拒收仍写入节点: %v", err)
	}
	if _, err = s.AppendAtRevision(ctx, "other", 0, add("s2")); err != nil {
		t.Fatal("跨任务版本串扰", err)
	}
	rev, err := s.CurrentRevision(ctx, "t")
	if err != nil || rev != 1 {
		t.Fatalf("拒收改变事件版本: %d %v", rev, err)
	}
}
