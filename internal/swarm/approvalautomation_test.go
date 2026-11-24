package swarm

import (
	"context"
	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/permission"
	"testing"
)

func TestAutomaticApprovalKeepsHardRulesAndStopsWhenDisabled(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	if err := env.Executor.Store.SetApprovalAutomationFlag(t.Context(), env.TaskID, "auto_approve", true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		calls++
		return ActionReview{Decision: ActionApproval, Reason: "真实业务新增", Permission: &rings.PurposePermission{Actions: []string{"create"}, Resource: "测试对象", Purpose: "新增测试对象", Effects: "新增测试对象", Targets: []string{"test-system"}}}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo test-allowed"}`); d.Behavior != permission.Allow || len(gate.Pending(env.TaskID)) != 0 {
		t.Fatal(d)
	}
	if err := env.Executor.Store.SetSetting(t.Context(), keyHardRulePrefix+"auto-test", `{"id":"auto-test","group":"测试规则","name":"拒绝测试命令","pattern":"test-blocked","enabled":true}`, "creator"); err != nil {
		t.Fatal(err)
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo test-blocked"}`); d.Behavior != permission.Deny || calls != 1 {
		t.Fatal("hard rule bypassed", d, calls)
	}
	if err := env.Executor.Store.SetApprovalAutomationFlag(t.Context(), env.TaskID, "auto_approve", false); err != nil {
		t.Fatal(err)
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo test-next"}`); d.Behavior != permission.Deny || len(gate.Pending(env.TaskID)) != 1 {
		t.Fatal("disabled mode retained automatic authority", d)
	}
}

func TestAutomaticPendingApprovalDoesNotApproveHardRuleHit(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{Decision: ActionApproval, Reason: "测试待决"}, nil
	}
	callActionGate(env, pack, "bash", `{"command":"echo late-rule"}`)
	if len(gate.Pending(env.TaskID)) != 1 {
		t.Fatal("missing original ticket")
	}
	if err := env.Executor.Store.SetSetting(t.Context(), keyHardRulePrefix+"late", `{"id":"late","group":"测试规则","name":"后设硬规则","pattern":"late-rule","enabled":true}`, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := env.Executor.Store.SetApprovalAutomationFlag(t.Context(), env.TaskID, "auto_approve", true); err != nil {
		t.Fatal(err)
	}
	if _, err := AutoApprovePending(t.Context(), env.Executor.Store, gate, env.TaskID); err != nil {
		t.Fatal(err)
	}
	rows, _ := env.Executor.Store.ApprovalRows(t.Context(), env.TaskID)
	for _, row := range rows {
		if row.Type == rings.ApprovalRecApproved {
			t.Fatal("hard-rule hit received automatic approval")
		}
	}
}
