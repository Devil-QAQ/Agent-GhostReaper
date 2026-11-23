package overmind

import (
	"context"
	"testing"
	"time"
)

type heldSettleTransition struct{ markEntered, releaseMark, finished chan struct{} }

func (s *heldSettleTransition) MarkSettling(string, map[string]any) error {
	close(s.markEntered)
	<-s.releaseMark
	return nil
}
func (s *heldSettleTransition) FinishSettling(string, bool, map[string]any) error {
	close(s.finished)
	return nil
}

func TestSettlingCannotFinishBeforeManagerTransitionCompletes(t *testing.T) {
	store := openStore(t)
	ctl := &heldSettleTransition{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	l := &Loop{TaskID: "settle-transition", Store: store, SettleCtl: ctl}
	enterDone := make(chan struct{})
	go func() { defer close(enterDone); l.enterSettling(context.Background()) }()
	select {
	case <-ctl.markEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("manager transition not reached")
	}
	defer func() {
		select {
		case <-ctl.releaseMark:
		default:
			close(ctl.releaseMark)
		}
	}()
	checkDone := make(chan struct{})
	go func() {
		defer close(checkDone)
		if l.isSettling() {
			l.settle(context.Background())
		}
	}()
	select {
	case <-ctl.finished:
		t.Fatal("terminal check ran before the manager entered settling")
	case <-time.After(50 * time.Millisecond):
	}
	close(ctl.releaseMark)
	select {
	case <-enterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("settling transition did not finish")
	}
	select {
	case <-checkDone:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal check did not resume")
	}
	select {
	case <-ctl.finished:
	default:
		t.Fatal("terminal state was not applied after the manager transition")
	}
}
