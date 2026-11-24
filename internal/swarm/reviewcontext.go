package swarm

import (
	"encoding/json"
	"strings"

	"ghostreaper/internal/rings"
)

// Only the model-facing representation changes. The ledger and the boundary
// used to validate references keep the complete, current operator grants.
type reviewGrant struct {
	RequestID  string                   `json:"request_id"`
	Permission *rings.PurposePermission `json:"permission,omitempty"`
	Tool       string                   `json:"tool,omitempty"`
	Note       string                   `json:"note,omitempty"`
	Reason     string                   `json:"reason,omitempty"`
}

type reviewBoundary struct {
	ActionBoundary
	Approvals []reviewGrant `json:"prior_approvals,omitempty"`
}

func actionReviewBody(in ActionReviewInput) ([]byte, map[string]rings.ApprovalRow, error) {
	grants := make([]reviewGrant, 0, len(in.Boundaries.Approvals))
	legacy := map[string]rings.ApprovalRow{}
	for _, row := range in.Boundaries.Approvals {
		grant := reviewGrant{RequestID: row.RequestID, Permission: row.Permission, Reason: row.Reason}
		if row.Permission == nil {
			grant.Tool = row.ToolName
			grant.Note = row.Note
			legacy[row.RequestID] = row
		}
		grants = append(grants, grant)
	}
	body, err := json.Marshal(struct {
		Boundaries reviewBoundary  `json:"boundaries"`
		TaskType   string          `json:"task_type"`
		Tool       string          `json:"tool"`
		Arguments  json.RawMessage `json:"arguments"`
	}{
		Boundaries: reviewBoundary{ActionBoundary: in.Boundaries, Approvals: grants},
		TaskType:   in.TaskType, Tool: in.Tool, Arguments: in.Arguments,
	})
	return body, legacy, err
}

// A legacy grant has no structured permission. Its original action must be
// read before the model can cite it as authority; a short index is not a grant.
func unreadLegacyGrant(text string, legacy map[string]rings.ApprovalRow, read map[string]bool) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		_, body, ok := strings.Cut(text, "\n")
		if ok {
			text = strings.TrimSpace(strings.TrimSuffix(body, "```"))
		}
	}
	var effects actionEffects
	if json.Unmarshal([]byte(text), &effects) != nil || effects.Prohibition != "" {
		return false // The ordinary parser reports invalid responses.
	}
	for _, change := range effects.Changes {
		if _, ok := legacy[change.ApprovedBy]; ok && !read[change.ApprovedBy] {
			return true
		}
	}
	return false
}
