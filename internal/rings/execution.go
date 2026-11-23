package rings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ExecutionOwner binds a business write to the ledger's actual execution start.
// It uses the existing step and session records, not a second execution state.
type ExecutionOwner struct {
	StepID       string
	UnitID       string
	TranscriptID string
	StartedRev   int64
	// ApprovalRequest permits only the recorded gate-yield continuation.
	ApprovalRequest string
}

var ErrExecutionEnded = errors.New("所属执行已结束或已被替换")

func (s *Store) AppendForExecution(ctx context.Context, task string, owner ExecutionOwner, events ...Event) ([]int64, error) {
	if task == "" {
		return nil, ErrEmptyTaskID
	}
	req := writeRequest{taskID: task, events: events, owner: &owner, resp: make(chan writeResult, 1)}
	return s.enqueueEvents(ctx, req)
}

// Runs inside the very transaction that commits the requested write. A late
// approval or submission cannot cross a terminal/next-executor commit.
type executionReader interface{ QueryRow(string, ...any) *sql.Row }

func (s *Store) ExecutionCurrent(task string, owner ExecutionOwner) error {
	return validateExecutionOwner(s.db, task, owner)
}

// CurrentExecutionOwner resolves a step-level operator request to one actual
// execution. The controlled write still rechecks this binding at commit.
func (s *Store) CurrentExecutionOwner(ctx context.Context, task, step string) (ExecutionOwner, error) {
	owner := ExecutionOwner{StepID: step}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT revision,payload FROM events WHERE task_id=? AND kind='swarm.record' AND json_extract(payload,'$.type')='session_started' AND json_extract(payload,'$.step_id')=? ORDER BY revision DESC LIMIT 1`, task, step).Scan(&owner.StartedRev, &raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return owner, ErrExecutionEnded
		}
		return owner, err
	}
	var rec SwarmRecord
	if err = json.Unmarshal([]byte(raw), &rec); err != nil {
		return owner, err
	}
	owner.UnitID, owner.TranscriptID = rec.UnitID, rec.TranscriptID
	if err := validateExecutionBusiness(s.db, task, owner); err != nil {
		return owner, err
	}
	return owner, nil
}
func validateExecutionBusiness(tx executionReader, task string, owner ExecutionOwner) error {
	var taskStatus string
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE task_id=?`, task).Scan(&taskStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if taskStatus == "stopped" || taskStatus == "completed" || taskStatus == "hibernated" || taskStatus == "checkpointed" {
		return ErrExecutionEnded
	}
	return validateExecutionOwner(tx, task, owner)
}
func validateExecutionOwner(tx executionReader, task string, owner ExecutionOwner) error {
	if owner.StepID == "" || owner.UnitID == "" || owner.StartedRev <= 0 {
		return ErrExecutionEnded
	}
	var status string
	if err := tx.QueryRow(`SELECT status FROM nodes WHERE task_id=? AND node_id=? AND node_type='step'`, task, owner.StepID).Scan(&status); err != nil {
		return ErrExecutionEnded
	}
	var raw string
	var rev int64
	err := tx.QueryRow(`SELECT revision,payload FROM events
 WHERE task_id=? AND kind='swarm.record' AND json_extract(payload,'$.step_id')=?
 AND json_extract(payload,'$.type') IN ('session_started','session_finished')
 AND (json_extract(payload,'$.type')='session_started' OR (json_extract(payload,'$.unit_id')=? AND revision>?))
 ORDER BY revision DESC LIMIT 1`, task, owner.StepID, owner.UnitID, owner.StartedRev).Scan(&rev, &raw)
	if err != nil {
		return ErrExecutionEnded
	}
	var rec SwarmRecord
	if json.Unmarshal([]byte(raw), &rec) != nil || rec.UnitID != owner.UnitID {
		return ErrExecutionEnded
	}
	if rec.Type == SwarmRecSessionStarted && rev == owner.StartedRev && status == "running" {
		return nil
	}
	if owner.ApprovalRequest != "" && (status == StepGateWait || status == "running") && rec.Type == SwarmRecSessionEnd && rec.Yield != nil && rec.Yield.Source == "approval" && rec.Yield.Request == owner.ApprovalRequest {
		return nil
	}
	return ErrExecutionEnded
}

// LatestExecutionOwners reads execution segments, not the transcript aggregate:
// an original transcript can be resumed by a different unit many times.
func LatestExecutionOwners(events []RawEvent) map[string]ExecutionOwner {
	owners := map[string]ExecutionOwner{}
	for _, ev := range events {
		if ev.Kind != EventSwarmRecord {
			continue
		}
		var rec SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil {
			continue
		}
		switch rec.Type {
		case SwarmRecSessionStarted:
			owners[rec.StepID] = ExecutionOwner{StepID: rec.StepID, UnitID: rec.UnitID, TranscriptID: rec.TranscriptID, StartedRev: ev.Revision}
		case SwarmRecSessionEnd:
			if owner, ok := owners[rec.StepID]; ok && owner.UnitID == rec.UnitID && ev.Revision > owner.StartedRev {
				delete(owners, rec.StepID)
			}
		}
	}
	return owners
}
