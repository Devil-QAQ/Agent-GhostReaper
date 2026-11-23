package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func approvalEventsForGate(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	out := []rings.ApprovalRecord{}
	for _, r := range approvalEvents(t, store, task) {
		if strings.HasPrefix(r.RequestID, "ap-") {
			out = append(out, r)
		}
	}
	return out
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func callToolText(t *testing.T, wrapped *wrappedTool, args string) string {
	t.Helper()
	result, err := wrapped.Call(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		return ""
	}
	return flattenResult(result.Content)
}
