package swarm

import (
	"context"
	"encoding/json"
	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"iter"
	"os"
	"path/filepath"
	"testing"
)

type contextReviewProvider struct {
	run func(llm.CompletionRequest) llm.Message
}

func (p contextReviewProvider) Complete(_ context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.run(r), "stop", llm.Usage{}, nil
}
func (p contextReviewProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return nil
}
func TestReviewReadsSourceAndDetectsChanges(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{true: "changed", false: "stable"}[change], func(t *testing.T) {
			env, _, _ := actionGateFixture(t)
			env.Executor.RootDir = t.TempDir()
			dir := env.Executor.stepDir(env.TaskID, env.StepID)
			os.MkdirAll(dir, 0700)
			path := filepath.Join(dir, "work.py")
			os.WriteFile(path, []byte("print('local')"), 0600)
			calls := 0
			p := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
				calls++
				if calls == 1 {
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "source", Name: "read_context_file", Input: json.RawMessage(`{"path":"work.py"}`)}}}
				}
				last := r.Messages[len(r.Messages)-1]
				if last.Content[0].Content[0].Text != "print('local')" {
					t.Fatal("source not supplied")
				}
				if change {
					os.WriteFile(path, []byte("print('changed')"), 0600)
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"decision":"allow","reason":"local"}`)}}
			}}
			_, err := ActionReviewFor(p, env.readReviewSource)(t.Context(), ActionReviewInput{Tool: "bash", Arguments: json.RawMessage(`{"command":"python3 work.py"}`)})
			if (err != nil) != change || calls != 2 {
				t.Fatalf("change=%v calls=%d err=%v", change, calls, err)
			}
		})
	}
}
func TestReviewCarriesApprovalAcrossCommandVariantsWithCurrentPolicy(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{Decision: ActionApproval, Reason: "确认本次本地处理"}, nil
	}
	callActionGate(env, pack, "bash", `{"command":"echo one"}`)
	req := env.park.Req
	if err := gate.Decide(t.Context(), pack.TaskID, req.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	env.park = nil
	env.Executor.ReviewAction = func(_ context.Context, in ActionReviewInput) (ActionReview, error) {
		if len(in.Boundaries.Approvals) != 1 || in.Boundaries.Approvals[0].ToolArgs != req.ToolArgs {
			t.Fatal("approval context lost")
		}
		return ActionReview{Decision: ActionAllow, Reason: "same purpose and effect"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"printf two"}`); d.Behavior != permission.Allow {
		t.Fatal(d)
	}
	env.Executor.Store.Append(t.Context(), pack.TaskID, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{CreatorNotes: new("禁止写入")}})
	roe, _ := env.Executor.Store.CurrentRoE(t.Context(), pack.TaskID)
	b, err := env.actionBoundary(t.Context(), pack, roe)
	if err != nil || len(b.Approvals) != 1 || b.Notes != "禁止写入" || b.ApprovalEpoch <= b.Approvals[0].Rev {
		t.Fatal("history or current restriction lost", err)
	}
}

func TestChangedScriptCannotConsumeOldAllowance(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.RootDir = t.TempDir()
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "work.py")
	os.WriteFile(path, []byte("print('before')"), 0600)
	env.readReviewSource(path)
	old := env.reviewSources
	args := `{"command":"python3 work.py"}`
	env.armGateApproval(ApprovalRequest{ToolName: "bash", ToolArgs: args, Sources: old})
	os.WriteFile(path, []byte("print('after')"), 0600)
	calls := 0
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		calls++
		return ActionReview{Decision: ActionApproval, Reason: "actual contents changed"}, nil
	}
	if d := callActionGate(env, pack, "bash", args); d.Behavior != permission.Deny || calls != 1 || len(gate.Pending(pack.TaskID)) != 1 {
		t.Fatal("changed file inherited exact approval", d, calls)
	}
	if env.allowance != nil {
		t.Fatal("expired allowance retained")
	}
}
