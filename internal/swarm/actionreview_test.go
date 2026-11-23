package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

func actionGateFixture(t *testing.T) (*unitEnv, *TaskPack, *LedgerGate) {
	t.Helper()
	store := openStore(t, t.TempDir())
	gate := NewLedgerGate(store)
	env := &unitEnv{TaskID: "review-task", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
		Executor: &Executor{Store: store, Gate: gate}}
	pack := &TaskPack{TaskID: env.TaskID, StepID: env.StepID, GoalTitle: "核对本地测试数据",
		StepIntent: "读取测试报告", ActionLevel: rings.ActionReadOnly}
	return env, pack, gate
}

func callActionGate(env *unitEnv, pack *TaskPack, tool, args string) permission.Decision {
	return env.approvalGate(pack)(context.Background(), tool, json.RawMessage(args), permission.Context{})
}

func TestActionGateHardRuleCannotBecomeApprovalOrPass(t *testing.T) {
	for _, tool := range []string{"bash", "http_probe"} {
		t.Run(tool, func(t *testing.T) {
			env, pack, gate := actionGateFixture(t)
			// Simulate an archived gate-shaped rule; current evaluation must
			// still reject the matching action, never resurrect its old permit.
			err := env.Executor.Store.SetSetting(t.Context(), keyHardRulePrefix+"legacy-rule", `{"id":"legacy-rule","group":"系统破坏命令","name":"测试拒绝规则","pattern":"review_block","enabled":true,"action":"gate"}`, "creator")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
				calls++
				return ActionReview{Decision: ActionAllow, Reason: "测试"}, nil
			}
			env.armGateApproval(ApprovalRequest{ToolName: tool, ToolArgs: `{"value":"review_block"}`})
			for _, args := range []string{`{"value":"review_block"}`, `{"value":"text 'review_block'"}`} {
				if d := callActionGate(env, pack, tool, args); d.Behavior != permission.Deny {
					t.Fatal("enabled hard rule was bypassed")
				}
			}
			if calls != 0 || len(gate.Pending(pack.TaskID)) != 0 || env.deniedReason != "" {
				t.Fatalf("hard hit must reject only the action: calls=%d pending=%d", calls, len(gate.Pending(pack.TaskID)))
			}
		})
	}
}

func TestActionGateLocalOperationNeedsNoInventedTarget(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.ReviewAction = func(_ context.Context, in ActionReviewInput) (ActionReview, error) {
		if string(in.Arguments) != `{"command":"cat report.result.txt"}` || in.Intent != pack.StepIntent {
			t.Fatalf("lost actual action: %+v", in)
		}
		return ActionReview{Decision: ActionAllow, Reason: "读取本地报告，没有外部连接"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"cat report.result.txt"}`); d.Behavior != permission.Allow {
		t.Fatalf("local action denied: %+v", d)
	}
	assets, err := env.Executor.Store.NodesByType(t.Context(), pack.TaskID, rings.NodeAsset, "")
	if err != nil || len(assets) != 0 || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatalf("local filename became asset/card: assets=%d err=%v", len(assets), err)
	}
	recs := approvalEvents(t, env.Executor.Store, pack.TaskID)
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecActionReview || recs[0].Note != ActionAllow {
		t.Fatalf("missing actual review evidence: %+v", recs)
	}
}

func TestActionGateApprovalOnlyPermitsExactActionOnce(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	calls := 0
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		calls++
		return ActionReview{Decision: ActionApproval, Reason: "需要核对本次操作"}, nil
	}
	args := `{"command":"echo review","timeout":5}`
	if d := callActionGate(env, pack, "bash", args); d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("expected real pending approval: %+v", d)
	}
	req := env.park.Req
	if err := gate.Decide(t.Context(), pack.TaskID, req.ID, GateDecision{Approved: true}); err != nil {
		t.Fatal(err)
	}
	env.park = nil
	env.armGateApproval(req)
	if d := callActionGate(env, pack, "bash", `{"timeout":5,"command":"echo review"}`); d.Behavior != permission.Allow || calls != 1 {
		t.Fatalf("exact approved invocation did not resume: %+v calls=%d", d, calls)
	}
	if d := callActionGate(env, pack, "bash", args); d.Behavior != permission.Deny || calls != 2 {
		t.Fatalf("approval escaped its one invocation: %+v calls=%d", d, calls)
	}

}

func TestActionGateReviewFailureAsksHumanWithoutKillingUnit(t *testing.T) {
	for _, mode := range []string{"provider_error", "invalid_verdict", "missing_basis"} {
		t.Run(mode, func(t *testing.T) {
			env, pack, gate := actionGateFixture(t)
			env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
				switch mode {
				case "provider_error":
					return ActionReview{}, errors.New("private provider response")
				case "invalid_verdict":
					return ActionReview{Decision: "unknown", Reason: "不可用"}, nil
				default:
					return ActionReview{Decision: ActionAllow}, nil
				}
			}
			d := callActionGate(env, pack, "bash", `{"command":"cat report.txt"}`)
			if d.Behavior != permission.Deny || len(gate.Pending(pack.TaskID)) != 1 || env.deniedReason != "" {
				t.Fatalf("failure must not claim permission or kill unit: %+v", d)
			}
			if strings.Contains(gate.Pending(pack.TaskID)[0].Note, "private") {
				t.Fatal("raw provider error entered approval record")
			}
		})
	}
}

func TestActionGatePolicyEditDuringReviewInvalidatesResult(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		_, err := env.Executor.Store.Append(t.Context(), pack.TaskID, rings.Event{Kind: rings.EventRoeUpdated,
			Actor: "creator", Payload: rings.RoeUpdated{CreatorNotes: new("限制已更新")}})
		return ActionReview{Decision: ActionAllow, Reason: "旧边界允许"}, err
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo review"}`); d.Behavior != permission.Deny || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatalf("stale decision used: %+v", d)
	}
}

func TestActionReviewParserRejectsUncertainOrExtraResults(t *testing.T) {
	for _, text := range []string{"", `{"decision":"allow"}`, `{"decision":"no","reason":"不明"}`,
		`{"decision":"allow","reason":"ok"} {"decision":"approval","reason":"no"}`,
		`{"decision":"allow","reason":"ok","host_permission":true}`} {
		if _, err := parseActionReview(text); err == nil {
			t.Fatalf("accepted invalid review %q", text)
		}
	}
	if _, err := parseActionReview(`{"decision":"allow","reason":"纯本地读操作"}`); err != nil {
		t.Fatal(err)
	}
}

func TestActionGateRealProviderBoundaryUsage(t *testing.T) {
	env, pack, _ := actionGateFixture(t)
	env.Executor.Provider = auxTestProvider{complete: func(ctx context.Context) (llm.Message, string, llm.Usage, error) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock(`{"decision":"allow","reason":"读取本地测试输出"}`)}}, "stop", llm.Usage{InputTokens: 100, OutputTokens: 12}, nil
	}}
	if d := callActionGate(env, pack, "bash", `{"command":"cat result.txt"}`); d.Behavior != permission.Allow {
		t.Fatalf("provider-backed review failed: %+v", d)
	}
	rows, err := env.Executor.Store.TokenUsageRows(t.Context(), pack.TaskID)
	if err != nil || len(rows) != 1 || rows[0].Purpose != "action_review" || rows[0].InputTokens != 100 || rows[0].OutputTokens != 12 {
		t.Fatalf("review usage not attributed once: rows=%+v err=%v", rows, err)
	}
}

func TestActionGateDeletedRulesNeverReappearAsHiddenFallback(t *testing.T) {
	env, pack, _ := actionGateFixture(t)
	seedHardRulesOrFail(t, env.Executor.Store)
	for _, r := range mustListHardRules(t, env.Executor.Store) {
		if err := DeleteHardRule(t.Context(), env.Executor.Store, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := SeedHardRules(t.Context(), env.Executor.Store); err != nil {
		t.Fatal(err)
	}
	rules, err := ListHardRules(t.Context(), env.Executor.Store)
	if err != nil || len(rules) != 0 {
		t.Fatalf("deleted rules resurrected: %d %v", len(rules), err)
	}
	calls := 0
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		calls++
		return ActionReview{Decision: ActionApproval, Reason: "当前动作由操作者决定"}, nil
	}
	callActionGate(env, pack, "bash", `{"command":"echo review"}`)
	if calls != 1 || env.park == nil {
		t.Fatal("empty visible rules were replaced by an invisible rule set")
	}
}

func TestActionGateUnreadableRuleCannotFallThroughToModel(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	if err := env.Executor.Store.SetSetting(t.Context(), keyHardRulePrefix+"broken", "{broken", "creator"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		calls++
		return ActionReview{Decision: ActionAllow, Reason: "测试"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo review"}`); d.Behavior != permission.Deny || calls != 0 || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatal("unreadable rule silently became a model allowance or approval")
	}
}

func TestActionGateTaskConstraintsAreReviewedWithoutAnotherRegexGate(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	if _, err := env.Executor.Store.Append(t.Context(), pack.TaskID, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{
		CreatorNotes: new("仅处理本地报告"), IntentRules: []rings.IntentRule{{ID: "legacy", Action: "访问外部测试服务", Disposition: "forbid", Patterns: []string{"cat"}}}}}); err != nil {
		t.Fatal(err)
	}
	called := false
	env.Executor.ReviewAction = func(_ context.Context, in ActionReviewInput) (ActionReview, error) {
		called = true
		if !strings.Contains(in.Boundaries.Notes, "禁止访问外部测试服务") || !strings.Contains(in.Boundaries.Notes, "仅处理本地报告") {
			t.Fatal("lost operator constraints")
		}
		return ActionReview{Decision: ActionAllow, Reason: "当前只是读取本地报告，不涉及外部服务"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"cat report.txt"}`); d.Behavior != permission.Allow || !called || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatal("legacy pattern became a parallel hard-rule chain")
	}
}
