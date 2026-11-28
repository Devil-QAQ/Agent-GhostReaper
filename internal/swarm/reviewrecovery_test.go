package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
)

func TestReviewContextErrorsReturnToolResults(t *testing.T) {
	for _, kind := range []string{"missing_source", "outside_root", "oversized_source", "unknown_approval", "invalid_approval_arguments"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			readerCalls := 0
			p := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
				calls++
				if calls == 1 {
					name, args := "read_context_file", `{"path":"missing.py"}`
					if strings.Contains(kind, "approval") {
						name = "read_approval"
						args = `{"request_id":"not-in-index"}`
						if kind == "invalid_approval_arguments" {
							args = `{"request_id":42}`
						}
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "context", Name: name, Input: json.RawMessage(args)}}}
				}
				last := r.Messages[len(r.Messages)-1].Content[0]
				if !last.IsError || msgText(llm.Message{Content: last.Content}) == "" {
					t.Fatal("missing readable tool error", last)
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"当前调用无业务变化","changes":[]}`)}}
			}}
			got, err := ActionReviewFor(p, func(string) ([]byte, error) {
				readerCalls++
				switch kind {
				case "outside_root":
					return nil, actionReviewError("不属于可读取的工作源码")
				case "oversized_source":
					return nil, actionReviewError("源码超过 64 KiB")
				default:
					return nil, os.ErrNotExist
				}
			})(t.Context(), ActionReviewInput{Tool: "bash", Arguments: json.RawMessage(`{"command":"cat existing-report.txt"}`)})
			if calls != 2 {
				t.Fatal("reviewer did not receive error", calls, err)
			}
			if strings.Contains(kind, "approval") {
				if err != nil || got.Decision != ActionAllow || readerCalls != 0 {
					t.Fatal(got, err)
				}
			} else if err != nil || got.Decision != ActionAllow || readerCalls != 1 {
				t.Fatal("tool result was not left for model judgment", got, err, readerCalls)
			}
		})
	}
}

func TestReviewCanRetryFailedSourceWithoutModelWait(t *testing.T) {
	reads, calls := 0, 0
	p := contextReviewProvider{run: func(r llm.CompletionRequest) llm.Message {
		calls++
		if calls <= 2 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "read", Name: "read_context_file", Input: json.RawMessage(`{"path":"script.py"}`)}}}
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"源码核对完成，无业务变化","changes":[]}`)}}
	}}
	got, err := ActionReviewFor(p, func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return nil, os.ErrNotExist
		}
		return []byte("print('read only')"), nil
	})(t.Context(), ActionReviewInput{Tool: "bash", Arguments: json.RawMessage(`{"command":"python3 script.py"}`)})
	if err != nil || got.Decision != ActionAllow || calls != 3 || reads != 3 {
		t.Fatal(got, calls, reads, err)
	}
}

func TestLocalReviewFailureKeepsUnitRunningAndActionUnexecuted(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "review-local-recovery"
	seedGraph(t, store, task, "核对本地观察")
	marker := filepath.Join(dir, "must-not-execute")
	args, _ := json.Marshal(map[string]string{"command": "touch " + marker})
	corrected := filepath.Join(dir, "corrected-command-executed")
	correctedArgs, _ := json.Marshal(map[string]string{"command": "touch " + corrected})
	model := newScriptedModel(func(r *recordedRequest) scriptedReply {
		if r.Index == 0 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "blocked-local", Name: "bash", Args: string(args)}}}
		}
		if r.Index == 1 {
			found := false
			for _, m := range r.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, "源码文件不存在") {
					found = true
				}
			}
			if !found {
				t.Error("worker did not receive corrective tool error")
			}
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "corrected-local", Name: "bash", Args: string(correctedArgs)}}}
		}
		return scriptedReply{Text: "已按错误调整调用并完成。", Finish: "stop"}
	})
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := NewExecutor(store, provider, filepath.Join(dir, "tasks"), Budget{})
	reviews := 0
	ex.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		reviews++
		if reviews == 1 {
			return ActionReview{}, actionReviewError("源码文件不存在，本次调用未执行；核对路径后重试")
		}
		return ActionReview{Decision: ActionAllow, Reason: "已重新核对调整后的调用"}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := ex.RunStep(ctx, task, "s1")
	if err != nil || result.StepStatus != "done" || model.requestCount() != 3 || reviews != 2 {
		t.Fatal("local error entered model recovery", result, err, model.requestCount())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreviewed command executed", err)
	}
	if _, err := os.Stat(corrected); err != nil {
		t.Fatal("corrected action did not run", err)
	}
	for _, r := range model.requests() {
		for _, m := range r.Messages {
			if strings.Contains(m.Content, "【模型重试") {
				t.Fatal("false model retry")
			}
		}
	}
}

func TestLocalReviewErrorDoesNotCancelSegmentOrCreateApproval(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	env.openSegment(cancel)
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{}, actionReviewError("批准已失效或不在当前索引")
	}
	d := callActionGate(env, pack, "bash", `{"command":"echo local"}`)
	if d.Behavior != permission.Deny || ctx.Err() != nil || env.reviewErr != nil || env.deniedReason != "" || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatal(d, ctx.Err(), env.reviewErr)
	}
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{Decision: ActionAllow, Reason: "已重新核对当前调用"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"echo corrected"}`); d.Behavior != permission.Allow {
		t.Fatal(d)
	}
}
