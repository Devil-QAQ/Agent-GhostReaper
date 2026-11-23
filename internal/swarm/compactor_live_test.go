package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// Opt-in fixed-material replay, with actual returned usage. This tests context
// retention only; it does not execute target tools or prove autonomous discovery.
func TestUnitContextLiveFixedReplay(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_CONTEXT_MODEL_FILE")
	if file == "" {
		t.Skip("private context replay model configuration not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("cannot read private configuration")
	}
	var cfg struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Format  string `json:"format"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private model configuration")
	}
	p, err := llm.NewProvider(llm.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, Format: llm.Format(cfg.Format), MaxRetries: -1})
	if err != nil {
		t.Fatal("cannot initialize private model provider")
	}
	env, store := p439Env(t)
	msgs := []llm.Message{llm.UserText("当前意图：核对固定材料。可信事实 f-stable：49项检查已完成；权限验证尚未进行。未完成事项：验证权限边界。所有工具记录均为人工合成材料，不是实际发现。")}
	for i := 0; i < 20; i++ {
		var body strings.Builder
		fmt.Fprintf(&body, "合成材料 %d；正常读取；没有新的业务结论。\n", i)
		for j := 0; j < 40; j++ {
			fmt.Fprintf(&body, "row=%d digest=%x observed=fixture-only verified=false\n", j, sha256.Sum256([]byte(fmt.Sprintf("fixed-row-%d-%d", i, j))))
		}
		id := fmt.Sprintf("read-%d", i)
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: "read", Input: json.RawMessage(`{"path":"fixture.txt"}`)}}}, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: id, Content: []llm.ContentBlock{llm.TextBlock(body.String())}}}})
	}
	msgs = append(msgs, llm.UserText("只返回JSON：fact_id为字符串、checked_count为整数、permission_verified为布尔值、pending为字符串，概括当前可信事实和未完成事项。不要重新检查，不要调用工具。"))
	type row struct {
		Round     int       `json:"round"`
		Version   string    `json:"version"`
		Usage     llm.Usage `json:"usage"`
		ElapsedMS int64     `json:"elapsed_ms"`
		Text      string    `json:"text"`
		Error     string    `json:"error,omitempty"`
	}
	var report []row
	save := func() {
		if out := os.Getenv("GHOSTREAPER_CONTEXT_EVIDENCE"); out != "" {
			body, _ := json.MarshalIndent(report, "", "  ")
			if os.WriteFile(out, append(body, '\n'), 0600) != nil {
				t.Fatal("cannot save sanitized replay evidence")
			}
		}
	}
	totals := map[string]llm.Usage{}
	for round := 1; round <= 3; round++ {
		for _, version := range []string{"baseline", "current"} {
			view := msgs
			if version == "current" {
				view = newUnitCompactor(store, env.Executor.Blobs, env.TaskID, env.StepID, env.UnitID, 1048576, 4096, nil).Pre(t.Context(), msgs, 0)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			start := time.Now()
			msg, _, usage, callErr := p.Complete(ctx, llm.CompletionRequest{
				System:   []string{"你只核对人工合成的会话材料。所有事实以原记录为准，不增加验证结论。按最后的要求直接输出JSON。"},
				Messages: llm.MessagesForAPI(view), Tools: []llm.ToolSchema{{Name: "read", Description: "读取固定本地材料", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
				MaxTokens: 256, Thinking: "disabled",
			})
			cancel()
			r := row{Round: round, Version: version, Usage: usage, ElapsedMS: time.Since(start).Milliseconds(), Text: msg.Text()}
			if callErr != nil {
				r.Error = "model request failed; private error details withheld"
			}
			report = append(report, r)
			save()
			if callErr != nil {
				t.Fatal(r.Error)
			}
			var answer struct {
				FactID   string `json:"fact_id"`
				Checked  int    `json:"checked_count"`
				Verified *bool  `json:"permission_verified"`
				Pending  string `json:"pending"`
			}
			if json.Unmarshal([]byte(msg.Text()), &answer) != nil || answer.FactID != "f-stable" || answer.Checked != 49 || answer.Verified == nil || *answer.Verified || !strings.Contains(answer.Pending, "权限") {
				t.Fatal("fixed material fact/boundary retention failed; sanitized response saved")
			}
			if usage.InputTokens <= 0 {
				t.Fatal("actual input usage missing; no estimated billing substituted")
			}
			if version == "baseline" && usage.InputTokens > (1048576-4096)*6/10 {
				t.Fatal("fixed baseline exceeded its original micro-compaction threshold")
			}
			u := totals[version]
			u.Add(usage)
			totals[version] = u
		}
	}
	baseline, current := totals["baseline"], totals["current"]
	t.Logf("fixed-material replay: baseline input=%d output=%d cache_read=%d; current input=%d output=%d cache_read=%d", baseline.InputTokens, baseline.OutputTokens, baseline.CacheReadTokens, current.InputTokens, current.OutputTokens, current.CacheReadTokens)
	if current.InputTokens*100 > baseline.InputTokens*80 {
		t.Fatal("fixed replay input reduction below 20%; report preserved")
	}
}
