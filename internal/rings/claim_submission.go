package rings

import (
	"context"
	"database/sql"
	"errors"
)

var ErrClaimCheckApplied = errors.New("本次交卷已有核验结果")

// ClaimSubmissionVersion names existing ledger events, not another state
// machine. Its predicate runs in the same transaction as verdict/package/state.
type ClaimSubmissionVersion struct {
	ClaimID   string
	SubmitRev int64
	// Empty means submit. Retake closeout checks the exact earlier verdict or
	// reopen record that it inspected, so a new submission wins over old work.
	RecordType string
}

func (s *Store) AppendForClaimSubmission(ctx context.Context, task string, version ClaimSubmissionVersion, expected *int64, events ...Event) ([]int64, error) {
	if task == "" {
		return nil, ErrEmptyTaskID
	}
	return s.enqueueEvents(ctx, writeRequest{taskID: task, events: events, claim: &version, expectedRevision: expected, resp: make(chan writeResult, 1)})
}

func validateClaimSubmission(tx *sql.Tx, task string, version ClaimSubmissionVersion, events []Event) error {
	var rev int64
	var typ string
	if err := tx.QueryRow(`SELECT revision,json_extract(payload,'$.type') FROM events
 WHERE task_id=? AND kind='testclaim.record' AND json_extract(payload,'$.claim_id')=?
 AND json_extract(payload,'$.type') IN ('submit','verdict','reopen') ORDER BY revision DESC LIMIT 1`, task, version.ClaimID).Scan(&rev, &typ); err != nil {
		return err
	}
	expectedType := version.RecordType
	if expectedType == "" {
		expectedType = TestClaimRecSubmit
	}
	if typ == TestClaimRecVerdict && rev > version.SubmitRev {
		return ErrClaimCheckApplied
	}
	if typ != expectedType || rev != version.SubmitRev {
		return ErrStaleRevision
	}
	var status string
	if err := tx.QueryRow(`SELECT status FROM nodes WHERE task_id=? AND node_id=? AND node_type='testclaim'`, task, version.ClaimID).Scan(&status); err != nil {
		return err
	}
	if status != TestClaimCandidate {
		return ErrStaleRevision
	}
	// Cancellation prepared before a unit was claimed must not overwrite its
	// running state. Re-read and rebuild this small batch instead.
	for _, event := range events {
		p, ok := event.Payload.(NodeStateChanged)
		if !ok || p.Status != "cancelled" {
			continue
		}
		if err := tx.QueryRow(`SELECT status FROM nodes WHERE task_id=? AND node_id=?`, task, p.NodeID).Scan(&status); err != nil {
			return err
		}
		if status != "dispatched" {
			return ErrStaleRevision
		}
	}
	return nil
}
