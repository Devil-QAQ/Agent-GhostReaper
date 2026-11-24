package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type contextReviewProvider struct {
	run func(llm.CompletionRequest) llm.Message
}

func TestReviewKeepsStablePolicyPrefixAndReadsLegacyGrantsOnlyWhenNeeded(t *testing.T) {
	boundary := ActionBoundary{TaskDescription: "测试对象授权", Notes: "禁止删除已有数据"}
	for i := range 135 {
		boundary.Approvals = append(boundary.Approvals, rings.ApprovalRow{
			RequestID: fmt.Sprintf("approved-%03d", i), Type: rings.ApprovalRecApproved, DecidedBy: "creator", ToolName: "bash",
			ToolArgs: `{"command":"` + strings.Repeat("original-command-material-", 26) + `"}`,
			Note:     "旧操作的业务影响", Reason: "仅允许创建测试用户，不允许管理员权限",
		})
	}
	one := ActionReviewInput{TaskType: "pentest", Tool: "bash", Arguments: json.RawMessage(`{"command":"cat local-evidence"}`), Boundaries: boundary}
	two := one
	two.Tool, two.Arguments = "http_probe", json.RawMessage(`{"url":"https://example.test/status"}`)
	a, _, err := actionReviewBody(one)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := actionReviewBody(two)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(a), `"tool":"bash","arguments":`)
	if start < 0 || string(a[:start]) != string(b[:start]) || len(a) > 40000 || strings.Contains(string(a), "original-command-material") {
		t.Fatalf("policy prefix changed or original commands were sent eagerly: bytes=%d", len(a))
	}
	for _, grant := range boundary.Approvals {
		if !strings.Contains(string(a), grant.RequestID) || !strings.Contains(string(a), grant.Reason) {
			t.Fatal("grant index or operator restriction lost")
		}
	}
	calls := 0
	provider := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
		calls++
		if len(r.Messages) != 1 || strings.Contains(msgText(r.Messages[0]), "original-command-material") {
			t.Fatal("read-only action unnecessarily fetched legacy commands")
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"只读已有材料，无业务变化","changes":[]}`)}}
	}}
	got, err := ActionReviewFor(provider)(t.Context(), one)
	if err != nil || got.Decision != ActionAllow || calls != 1 {
		t.Fatal(got, calls, err)
	}
	original, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("135条旧批准的合成对照：原表示%d字节，初始研判输入%d字节（原命令保留、按需读取；不代表整任务Token降幅）", len(original), len(a))
}

func TestReviewLegacyGrantRequiresOriginalAndPreservesOperatorRestriction(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		for _, read := range []bool{false, true} {
			t.Run(fmt.Sprintf("fenced=%v/read=%v", fenced, read), func(t *testing.T) {
				grant := rings.ApprovalRow{RequestID: "grant-users", Type: rings.ApprovalRecApproved, DecidedBy: "creator", ToolName: "bash", ToolArgs: `{"command":"fixture-create-user"}`, Note: "旧目的", Reason: "仅普通测试用户，不允许管理员"}
				calls := 0
				provider := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
					calls++
					if read && calls == 1 {
						return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "grant-read", Name: "read_approval", Input: json.RawMessage(`{"request_id":"grant-users"}`)}}}
					}
					if read {
						var detail map[string]string
						result := r.Messages[len(r.Messages)-1].Content[0].Content[0].Text
						if json.Unmarshal([]byte(result), &detail) != nil || detail["arguments"] != grant.ToolArgs || detail["reason"] != grant.Reason || detail["note"] != grant.Note {
							t.Fatal("legacy original or manual restriction lost", result)
						}
					}
					text := `{"reason":"同一目的和普通用户权限","changes":[{"permission":{"actions":["create"],"resource":"测试用户","targets":["https://example.test"],"purpose":"验证用户","effects":"普通用户"},"approved_by":"grant-users"}]}`
					if fenced {
						text = "```json\n" + text + "\n```"
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}
				}}
				got, err := ActionReviewFor(provider)(t.Context(), ActionReviewInput{Tool: "bash", Arguments: json.RawMessage(`{"command":"fixture-other-command"}`), Boundaries: ActionBoundary{Approvals: []rings.ApprovalRow{grant}}})
				if read && (err != nil || got.Decision != ActionAllow || calls != 2) {
					t.Fatal(got, calls, err)
				}
				if !read && (err == nil || got.Decision == ActionAllow) {
					t.Fatal("unread legacy grant was treated as authority", got, calls)
				}
			})
		}
	}
}

func TestApprovalLookupDoesNotConsumeScriptContextAllowance(t *testing.T) {
	var grants []rings.ApprovalRow
	for _, id := range []string{"first", "second"} {
		grants = append(grants, rings.ApprovalRow{RequestID: id, Type: rings.ApprovalRecApproved, DecidedBy: "creator", ToolName: "bash", ToolArgs: `{"command":"fixture-script"}`})
	}
	calls := 0
	provider := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
		calls++
		if calls <= 10 {
			name, args := "read_context_file", fmt.Sprintf(`{"path":"source-%d.py"}`, calls-2)
			if calls <= 2 {
				name, args = "read_approval", fmt.Sprintf(`{"request_id":%q}`, grants[calls-1].RequestID)
			}
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: fmt.Sprint(calls), Name: name, Input: json.RawMessage(args)}}}
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"原调用与源码核对完成，同一许可范围","changes":[{"permission":{"actions":["create"],"resource":"普通测试用户","targets":["https://example.test"],"purpose":"创建用户","effects":"普通用户"},"approved_by":"first"},{"permission":{"actions":["create"],"resource":"测试记录","targets":["https://example.test"],"purpose":"创建测试记录","effects":"测试记录"},"approved_by":"second"}]}`)}}
	}}
	got, err := ActionReviewFor(provider, func(path string) ([]byte, error) { return []byte("fixed source " + path), nil })(t.Context(), ActionReviewInput{Tool: "bash", Arguments: json.RawMessage(`{"command":"python3 source-1.py"}`), Boundaries: ActionBoundary{Approvals: grants}})
	if err != nil || got.Decision != ActionAllow || calls != 11 {
		t.Fatal("approval lookup consumed source-read allowance", got, calls, err)
	}
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
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"local","changes":[]}`)}}
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
