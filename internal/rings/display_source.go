package rings

import (
	"context"
	"database/sql"
	"errors"
)

var ErrDisplayAlreadyGenerated = errors.New("该版本已有显示稿")

// Content versions ignore execution status changes. A unit finishing must not
// cause its unchanged intent text to be generated all over again.
func (s *Store) NodeTextSourceRevs(ctx context.Context, task string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json_extract(payload,'$.node_id'),MAX(revision) FROM events WHERE task_id=? AND kind IN ('node.added','node.amended') GROUP BY json_extract(payload,'$.node_id')`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var rev int64
		if err := rows.Scan(&id, &rev); err != nil {
			return nil, err
		}
		out[id] = rev
	}
	return out, rows.Err()
}

func validateNodeTextSource(tx *sql.Tx, task, id string, source, applyingRev int64) error {
	if source == 0 {
		return nil
	} // Existing event payloads predate content versions.
	var current int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM events WHERE task_id=? AND kind IN ('node.added','node.amended') AND json_extract(payload,'$.node_id')=? AND revision<?`, task, id, applyingRev).Scan(&current); err != nil {
		return err
	}
	if current != source {
		return ErrStaleRevision
	}
	return nil
}

// Deduplication belongs to new writes, not projection replay: historical
// display corrections remain readable in their actual original order.
func (s *Store) AppendDisplay(ctx context.Context, task string, events ...Event) ([]int64, error) {
	if task == "" {
		return nil, ErrEmptyTaskID
	}
	return s.enqueueEvents(ctx, writeRequest{taskID: task, events: events, display: true, resp: make(chan writeResult, 1)})
}

func validateGeneratedDisplay(tx *sql.Tx, task string, events []Event) error {
	for _, event := range events {
		var id, field string
		var source int64
		switch p := event.Payload.(type) {
		case FactTextPolish:
			id, field, source = p.FactID, "fact_id", p.SourceRev
		case StepTitlePolish:
			id, field, source = p.StepID, "step_id", p.SourceRev
		case ClaimTextPolish:
			id, field, source = p.ClaimID, "claim_id", p.SourceRev
		case OvermindPolish:
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND kind=? AND json_extract(payload,'$.rec_rev')=?`, task, string(event.Kind), p.RecRev).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return ErrDisplayAlreadyGenerated
			}
			continue
		default:
			continue
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE task_id=? AND kind=? AND json_extract(payload,?)=? AND COALESCE(json_extract(payload,'$.source_rev'),0)=?`, task, string(event.Kind), "$."+field, id, source).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrDisplayAlreadyGenerated
		}
	}
	return nil
}
