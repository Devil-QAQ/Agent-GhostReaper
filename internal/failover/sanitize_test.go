package failover

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// ---- P4-27 ⑤ 出站消息清洗(422 坏消息根治) ----

func TestSanitizeMessagesDropsEmpty(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("")}}, // 全空文本
		{Role: llm.RoleAssistant, Content: nil},                              // content 缺失
		llm.UserText("正常消息"),
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("  ")}}, // 空白文本
	}
	out, dropped, patched := sanitizeMessages(msgs)
	if dropped != 3 || patched != 0 {
		t.Fatalf("应剔除 3 条空消息、零补位: dropped=%d patched=%d", dropped, patched)
	}
	if len(out) != 1 || out[0].Text() != "正常消息" {
		t.Fatalf("应只剩正常消息: %+v", out)
	}
}

func TestSanitizeMessagesPatchesEmptyToolResult(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
			Type: llm.BlockToolUse, ID: "c1", Name: "bash",
			Input: json.RawMessage(`{"command":"pwd"}`),
		}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{
			// 空输出的工具结果:内层 content 缺失——DeepSeek 422
			// "missing field content" 的坏消息形态(czgs1024 同款)。
			Type: llm.BlockToolResult, ToolUseID: "c1",
		}}},
	}
	out, dropped, patched := sanitizeMessages(msgs)
	if dropped != 0 || patched != 1 {
		t.Fatalf("应零剔除、补 1 个空结果: dropped=%d patched=%d", dropped, patched)
	}
	if len(out) != 2 {
		t.Fatalf("配对必须保住(2 条都在): %+v", out)
	}
	var inner []llm.ContentBlock
	for _, b := range out[1].Content {
		if b.Type == llm.BlockToolResult {
			inner = b.Content
		}
	}
	if flattenBlocksText(inner) != emptyResultPlaceholder {
		t.Fatalf("空 tool_result 应补占位: %q", flattenBlocksText(inner))
	}
	// tool_use 消息一字不动(深相等;清洗发生时列表重建,以内容对账)。
	a, _ := json.Marshal(out[0])
	b, _ := json.Marshal(msgs[0])
	if string(a) != string(b) {
		t.Fatalf("有内容的消息不应被改动: %s ≠ %s", a, b)
	}
}

func TestSanitizeMessagesKeepsGoodUntouched(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("任务包"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock("调用工具"),
			{Type: llm.BlockToolUse, ID: "c1", Name: "http_probe", Input: json.RawMessage(`{"url":"http://x"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{
			Type: llm.BlockToolResult, ToolUseID: "c1",
			Content: []llm.ContentBlock{llm.TextBlock("200 OK")},
		}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("结论")}},
	}
	out, dropped, patched := sanitizeMessages(msgs)
	if dropped != 0 || patched != 0 {
		t.Fatalf("全好消息应零改动: dropped=%d patched=%d", dropped, patched)
	}
	// 原切片原样返回(请求逐字节一致)。
	if len(out) != len(msgs) {
		t.Fatalf("条数应不变")
	}
	for i := range out {
		if &out[i] != &msgs[i] {
			t.Fatalf("第 %d 条消息不应被复制", i)
		}
	}
}

func TestSanitizeReqFullFlow(t *testing.T) {
	req := llm.CompletionRequest{
		System:   []string{"sys"},
		Messages: []llm.Message{llm.UserText("hi"), {Role: llm.RoleUser, Content: nil}},
	}
	got := sanitizeReq(context.Background(), req)
	if len(got.Messages) != 1 || got.Messages[0].Text() != "hi" {
		t.Fatalf("清洗后应只剩正常消息: %+v", got.Messages)
	}
	if len(req.Messages) != 2 {
		t.Fatal("入参请求不应被就地改动")
	}
}

func TestChainAllCooling(t *testing.T) {
	c, _, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	c.mu.Lock()
	lead := c.lead
	c.mu.Unlock()
	if c.AllCooling() {
		t.Fatal("closed 状态不应判全冷却")
	}
	// 连续瞬时失败开闸 → 全冷却。
	for i := 0; i < 3; i++ {
		c.onFailure(lead, ClassTransient, errTransient(0))
	}
	if !c.AllCooling() {
		t.Fatal("唯一腿开闸后应判全冷却")
	}
	// 冷却期满待探测 → 不算冷却中。
	c.mu.Lock()
	lead.brk.OpenUntil = c.Now().Add(-time.Second)
	c.mu.Unlock()
	if c.AllCooling() {
		t.Fatal("冷却期满待探测不应判全冷却")
	}
}
