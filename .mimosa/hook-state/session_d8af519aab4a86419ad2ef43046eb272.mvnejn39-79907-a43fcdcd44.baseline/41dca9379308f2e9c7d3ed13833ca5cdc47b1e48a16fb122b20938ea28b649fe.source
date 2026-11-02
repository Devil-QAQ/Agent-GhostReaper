package failover

import (
	"context"
	"strings"

	"ghostreaper/internal/runlog"
	"github.com/Autumn-27/norma/llm"
)

// ---- 出站消息清洗(P4-27 ⑤,问题清单 #47:422 坏消息根治) ----
//
// 诊断在案:长会话压缩/工具结果边界会产生 content 完全缺失或为空的
// 坏消息,DeepSeek openai 端点对 role:tool 消息缺 content 字段回 422
// "missing field content"(czgs1024 任务 u56~u63 同字节报错实锤,坏
// 消息随会话历史传染接力单位)。本层在 Chain 入口(Complete/Stream
// 路由前)对消息列表做出站清洗——坏请求根本不出站:
//
//   - content 完全缺失/全空的消息(无文本、无思考、无 tool_use、无
//     tool_result)整条剔除;
//   - tool_result 内层无文本的(空输出的工具结果)补合法占位——
//     tool_use/tool_result 配对必须保住(剔除会破坏配对,anthropic
//     端点直接 400),占位使序列化永远带 content 字段;
//   - 有内容的消息一字不动(未发生清洗时请求原样返回,逐字节一致)。
//
// 与 anthropic usage 传输层补全(anthropicusage.go)同层同模式:适配
// 引擎内部产生的消息形态,不改引擎(norma 是外部依赖)。

// emptyResultPlaceholder 空 tool_result 的合法占位文本(模型可见,非
// 界面文案):如实陈述该调用无文本输出。
const emptyResultPlaceholder = "(该调用无文本输出)"

// sanitizeMessages 出站清洗。返回清洗后的消息列表、被剔除的消息数、
// 被补占位的 tool_result 数。未发生清洗时原样返回原切片。
func sanitizeMessages(msgs []llm.Message) (out []llm.Message, dropped, patched int) {
	out = make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if !messageUsable(m) {
			dropped++
			continue
		}
		if !hasEmptyToolResult(m) {
			out = append(out, m)
			continue
		}
		// tool_result 内层空文本 → 复制后补占位(原消息不动,保配对,
		// 序列化永远带 content 字段)。
		nm := m
		nm.Content = append([]llm.ContentBlock(nil), m.Content...)
		for i, b := range nm.Content {
			if b.Type == llm.BlockToolResult && flattenBlocksText(b.Content) == "" {
				nm.Content[i].Content = []llm.ContentBlock{llm.TextBlock(emptyResultPlaceholder)}
				patched++
			}
		}
		out = append(out, nm)
	}
	if dropped == 0 && patched == 0 {
		return msgs, 0, 0
	}
	return out, dropped, patched
}

// messageUsable 消息是否携带任何可用内容(文本/思考/工具调用/工具
// 结果任一非空;纯空白文本视同空——语义上无内容)。
func messageUsable(m llm.Message) bool {
	for _, b := range m.Content {
		switch b.Type {
		case llm.BlockText:
			if strings.TrimSpace(b.Text) != "" {
				return true
			}
		case llm.BlockThinking:
			if b.Thinking != "" {
				return true
			}
		case llm.BlockToolUse, llm.BlockToolResult:
			return true
		}
	}
	return false
}

// hasEmptyToolResult 消息里是否存在内层无文本的 tool_result 块(空白
// 视同空)。
func hasEmptyToolResult(m llm.Message) bool {
	for _, b := range m.Content {
		if b.Type == llm.BlockToolResult && strings.TrimSpace(flattenBlocksText(b.Content)) == "" {
			return true
		}
	}
	return false
}

// flattenBlocksText 块列表的文本拼接(与 norma openai 适配器的
// flattenText 同口径:只拼 text 块)。
func flattenBlocksText(blocks []llm.ContentBlock) string {
	s := ""
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			s += b.Text
		}
	}
	return s
}

// sanitizeReq 请求出站清洗入口:发生清洗时落一条运行日志(422 形态
// 出站前拦截,诊断可见)。
func sanitizeReq(ctx context.Context, req llm.CompletionRequest) llm.CompletionRequest {
	msgs, dropped, patched := sanitizeMessages(req.Messages)
	if dropped == 0 && patched == 0 {
		return req
	}
	runlog.Warnf("出站消息清洗: 任务 %s 剔除 %d 条空消息、补 %d 个空工具结果(422 坏消息根治)",
		TaskFromContext(ctx), dropped, patched)
	req.Messages = msgs
	return req
}
