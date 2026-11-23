package swarm

import (
	"encoding/json"
	"ghostreaper/internal/rings"
)

// Actual results are saved by the wrapped call before callback drain completes.
// The harness may emit a synthetic cancellation result before that real return;
// the real observation still reaches the audit ledger and is never an action.
func (env *unitEnv) recordActualToolResult(id, name string, args json.RawMessage, text string, isError bool) error {
	env.auditMu.Lock()
	defer env.auditMu.Unlock()
	rec := rings.SwarmRecord{StepID: env.StepID, UnitID: env.UnitID, TranscriptID: env.TranscriptID,
		Type: rings.SwarmRecToolCall, ToolName: name, ToolArgs: args, IsError: isError, ToolCallID: id}
	rec.GraphVersion = env.graphVersion
	if err := env.persistToolResult(rec, text); err != nil {
		return err
	}
	if id != "" {
		if env.auditedResults == nil {
			env.auditedResults = map[string]int{}
		}
		env.auditedResults[id]++
	}
	return nil
}
func (env *unitEnv) takeAuditedResult(id string) bool {
	env.auditMu.Lock()
	defer env.auditMu.Unlock()
	if env.auditedResults[id] <= 0 {
		return false
	}
	env.auditedResults[id]--
	return true
}
func (env *unitEnv) persistToolResult(rec rings.SwarmRecord, text string) error {
	e := env.Executor
	if ref := extractBlobRef(text); ref != "" && e.Blobs.Exists(ref) {
		rec.ResultRef, rec.ResultHead = ref, clipLine(text, 512)
	} else if ref, head := e.Blobs.spill(text); ref != "" {
		rec.ResultRef, rec.ResultHead = ref, clipLine(head, 512)
	} else {
		rec.ResultHead = clipLine(text, 2048)
	}
	_, err := e.Store.Append(ledgerCtx, env.TaskID, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID, Payload: rec})
	return err
}
