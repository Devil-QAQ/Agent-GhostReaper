package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"ghostreaper/internal/rings"
)

// Enabling automation settles existing action tickets through the normal
// decision channel. Hard rules are rechecked here and again before execution.
// Automated decisions never become permanent manual purpose permissions.
func AutoApprovePending(ctx context.Context, store *rings.Store, gate ApprovalGate, task string) (int, error) {
	if gate == nil {
		return 0, nil
	}
	handled := 0
	for _, request := range gate.Pending(task) {
		enabled, err := store.ApprovalAutomationFlag(ctx, task, "auto_approve")
		if err != nil {
			return handled, err
		}
		if !enabled {
			break
		}
		rule, _, err := matchHardRule(ctx, store, request.ToolName, json.RawMessage(request.ToolArgs))
		if err != nil {
			return handled, err
		}
		if rule.ID != "" {
			err = gate.Expire(task, request.ID, "硬规则命中，未自动批准")
		} else {
			err = gate.Decide(ctx, task, request.ID, GateDecision{Approved: true, By: "automation", Reason: "审批自动放行"})
		}
		if errors.Is(err, ErrGateRequestNotFound) {
			continue
		}
		if errors.Is(err, ErrGateFrozen) {
			return handled, nil
		}
		if err != nil {
			return handled, err
		}
		handled++
	}
	return handled, nil
}
