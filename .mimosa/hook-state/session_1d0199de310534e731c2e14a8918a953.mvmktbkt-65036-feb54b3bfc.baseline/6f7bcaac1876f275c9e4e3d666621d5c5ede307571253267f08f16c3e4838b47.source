package failover

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/Autumn-27/norma/llm"
)

// nonStreaming 关流式垫片(P2-08c 输出与思考·流式开关):HTTP 请求以
// 非流式(整段一次,stream:false)发出,事件面保持流式形状——引擎与
// 会话逻辑零感知。事件序列对照 llm 包 Accumulator:usage 只在
// message_delta 带全量(与 openai 适配器同形状,避免双计)。
type nonStreaming struct{ inner llm.Provider }

func (p nonStreaming) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.inner.Complete(ctx, req)
}

func (p nonStreaming) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, stop, usage, err := p.inner.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil) {
			return
		}
		for _, b := range msg.Content {
			switch b.Type {
			case llm.BlockText:
				if b.Text != "" && !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: b.Text}, nil) {
					return
				}
			case llm.BlockThinking:
				if b.Thinking != "" && !yield(llm.StreamEvent{Type: llm.SEThinkingDelta, Text: b.Thinking}, nil) {
					return
				}
			case llm.BlockToolUse:
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: b.ID, ToolName: b.Name}, nil) {
					return
				}
				raw, _ := json.Marshal(b.Input)
				if !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(raw)}, nil) {
					return
				}
			}
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop, Usage: usage}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}
