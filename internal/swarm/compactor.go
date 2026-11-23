// Unit context compaction preserves original transcripts and evidence. Old
// bulky process outputs become excerpts with durable read references; receipts
// and the latest five process results remain in the working conversation.
package swarm

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

var microClearable = map[string]bool{
	"bash": true, "read": true, "list_claims": true,
	"http_probe": true, "port_probe": true,
	"web_search": true, "web_fetch": true,
}

const (
	microKeepN    = 5
	microOldBytes = 24 * 1024 // Eligible old bodies, independent of model window.
	microMinBody  = 1536
	microPrefix   = "[旧工具结果已存档；原文摘录]"
)

type unitCompactor struct {
	inner    *compaction.Compactor
	window   int
	reserved int
	store    *rings.Store
	blobs    *BlobStore
	taskID   string
	unitID   string
	stepID   string
}

func newUnitCompactor(store *rings.Store, blobs *BlobStore, taskID, stepID, unitID string, window, maxOutput int, p llm.Provider) *unitCompactor {
	if window <= 0 && (store == nil || blobs == nil) {
		return nil
	}
	if maxOutput <= 0 || maxOutput > 20000 {
		maxOutput = 20000
	}
	c := &unitCompactor{window: window, reserved: maxOutput, store: store, blobs: blobs, taskID: taskID, stepID: stepID, unitID: unitID}
	if window > 0 {
		var summarizer compaction.Summarizer
		if p != nil {
			summarizer = compaction.ProviderSummarizer(p, maxOutput, false)
		}
		c.inner = compaction.New(compaction.Config{ContextWindow: window, MaxOutputTokens: maxOutput}, summarizer)
	}
	return c
}

func (c *unitCompactor) microThreshold() int { return (c.window - c.reserved) * 6 / 10 }

func (c *unitCompactor) Pre(ctx context.Context, msgs []llm.Message, lastInputTokens int) []llm.Message {
	if ctx.Err() != nil {
		return msgs
	}
	tok := lastInputTokens
	if tok <= 0 {
		tok = compaction.EstimateTokens(llm.MessagesForAPI(msgs)) * 4 / 3
	}
	old := microCandidates(msgs)
	bytes := 0
	for _, text := range old {
		bytes += len(text)
	}
	if c.store != nil && c.blobs != nil && (bytes >= microOldBytes || c.window > 0 && tok > c.microThreshold()) {
		out, cleared, freed, refs := microCompactGR(msgs, old, c.blobs.Write)
		if cleared > 0 && c.noteMicro(ctx, tok, cleared, freed, refs) == nil {
			msgs, lastInputTokens = out, 0
		}
	}
	if c.inner != nil {
		return c.inner.Pre(ctx, msgs, lastInputTokens)
	}
	return msgs
}

func (c *unitCompactor) Reactive(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool) {
	if c.inner == nil {
		return msgs, false
	}
	return c.inner.Reactive(ctx, msgs)
}

func (c *unitCompactor) IsOverflow(err error) bool { return compaction.IsOverflow(err) }

// Only inspect the current API view, never the audit head before a boundary.
func microCandidates(msgs []llm.Message) map[string]string {
	view := llm.MessagesForAPI(msgs)
	var ids []string
	for _, m := range view {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse && microClearable[b.Name] {
				ids = append(ids, b.ID)
			}
		}
	}
	if len(ids) <= microKeepN {
		return nil
	}
	eligible := map[string]bool{}
	for _, id := range ids[:len(ids)-microKeepN] {
		eligible[id] = true
	}
	out := map[string]string{}
	for _, m := range view {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult || !eligible[b.ToolUseID] {
				continue
			}
			var parts []string
			for _, child := range b.Content {
				if child.Type != llm.BlockText {
					parts = nil
					break
				}
				parts = append(parts, child.Text)
			}
			text := strings.Join(parts, "\n")
			if len(text) >= microMinBody && !strings.HasPrefix(text, microPrefix) {
				out[b.ToolUseID] = text
			}
		}
	}
	return out
}

func microExcerpt(s string, head, tail int) string {
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	start := len(s) - tail
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[:head] + "\n…\n" + s[start:]
}

func microCompactGR(msgs []llm.Message, old map[string]string, archive func([]byte) (string, error)) ([]llm.Message, int, int, []string) {
	out := append([]llm.Message(nil), msgs...)
	cleared, freed := 0, 0
	var refs []string
	for i, m := range msgs {
		var blocks []llm.ContentBlock
		for j, b := range m.Content {
			text, ok := old[b.ToolUseID]
			if b.Type != llm.BlockToolResult || !ok {
				continue
			}
			ref, err := archive([]byte(text))
			if err != nil {
				continue // Failed archival never discards a body.
			}
			preview := fmt.Sprintf("%s\n%s\n原文 %d 字节；按需读取 read(path=%q)，可用 offset/limit 分段。", microPrefix, microExcerpt(text, 512, 256), len(text), ref)
			if blocks == nil {
				blocks = append([]llm.ContentBlock(nil), m.Content...)
			}
			blocks[j].Content = []llm.ContentBlock{llm.TextBlock(preview)}
			refs = append(refs, ref)
			cleared++
			freed += len(text) - len(preview)
		}
		if blocks != nil {
			out[i].Content = blocks
		}
	}
	if cleared == 0 {
		return msgs, 0, 0, nil
	}
	return out, cleared, freed, refs
}

// Durable references precede the shortened view and pin the original blobs
// through the existing ownership/sweep mechanism. No new context state file.
func (c *unitCompactor) noteMicro(ctx context.Context, tok, cleared, freed int, refs []string) error {
	detail := map[string]any{"cleared": cleared, "freed_chars": freed, "tokens_est": tok, "window": c.window, "archive_refs": refs}
	err := blackbox.Record(ctx, c.store, c.taskID, "swarm:"+c.unitID,
		rings.BlackboxRecord{Type: rings.BlackboxMicroCompact, StepID: c.stepID, UnitID: c.unitID, Detail: detail})
	if err != nil {
		runlog.Errorf("微压缩记录失败，保留原结果: 任务 %s 单位 %s", c.taskID, c.unitID)
		return err
	}
	runlog.Infof("微压缩: 任务 %s 单位 %s 归档旧轮工具结果 %d 条(减少 %d 字节,估算 %d token,窗口 %d)", c.taskID, c.unitID, cleared, freed, tok, c.window)
	return nil
}
