package rings

import (
	"path/filepath"
	"testing"
)

func TestApprovalAutomationPersistsAndIsTaskScoped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetApprovalAutomationFlag(t.Context(), "one", "auto_approve", true); err != nil {
		t.Fatal(err)
	}
	if err = s.SetApprovalAutomationFlag(t.Context(), "one", "auto_join", true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.ApprovalAutomation(t.Context(), "one")
	if err != nil || !state.AutoApprove || !state.AutoJoin {
		t.Fatal(state, err)
	}
	other, err := s.ApprovalAutomation(t.Context(), "two")
	if err != nil || other.AutoApprove || other.AutoJoin {
		t.Fatal(other, err)
	}
	if err = s.SetApprovalAutomationFlag(t.Context(), "one", "auto_approve", false); err != nil {
		t.Fatal(err)
	}
	state, _ = s.ApprovalAutomation(t.Context(), "one")
	if state.AutoApprove || !state.AutoJoin {
		t.Fatal("independent flag overwritten", state)
	}
}
