package rings

import (
	"context"
	"encoding/json"
)

// Approval source snapshots live in the append-only event, so old databases
// need no schema rewrite and process recovery preserves the reviewed content.
type ApprovalContext struct {
	Sources    map[string]string  `json:"source_hashes,omitempty"`
	Permission *PurposePermission `json:"permission,omitempty"`
}

func (s *Store) ApprovalContexts(ctx context.Context, task string) (map[string]ApprovalContext, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE task_id=? AND kind='approval.record' AND (json_type(payload,'$.source_hashes')='object' OR json_type(payload,'$.permission')='object') ORDER BY revision`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ApprovalContext{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var record struct {
			Request string `json:"request_id"`
			ApprovalContext
		}
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		out[record.Request] = record.ApprovalContext
	}
	return out, rows.Err()
}

func (s *Store) ApprovalSources(ctx context.Context, task string) (map[string]map[string]string, error) {
	contexts, err := s.ApprovalContexts(ctx, task)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for request, context := range contexts {
		out[request] = context.Sources
	}
	return out, nil
}
