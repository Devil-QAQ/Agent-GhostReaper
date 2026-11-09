package swarm

// P4-57 ③ 转录保真:会话转录完整保存思考内容原文(传回规则的数据源)
// ——真实引擎链路(agentcore Session + 真实 openai 适配器 + scripted 模型
// 流式回 reasoning_content),断言转录 JSONL 里 assistant 记录带思考块
// 原文与签名位,存回可读。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// TestTranscriptPreservesThinking 思考原文入转录、存回可读:模型流式回
// 思考+正文,转录 JSONL 的 assistant 记录带思考块原文;Resume 恢复后
// 会话历史同样带思考块(传回规则的运行时数据源)。
func TestTranscriptPreservesThinking(t *testing.T) {
	m := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Reasoning: "思考原文:先核对数列增量。", Text: "答:下一项是 5。"}
	})
	t.Cleanup(m.Close)
	prov, err := m.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	dir := t.TempDir()
	ts := transcript.NewStore(dir)
	const sid = "u-p457-fidelity"
	sess := agentcore.NewSession(agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{"测试基线"},
		PermissionMode:         permission.ModeDefault,
		Transcript:             ts,
		SessionID:              sid,
		Compaction:             &compaction.Config{ContextWindow: 100000, MaxOutputTokens: 2048},
		MaxTurns:               2,
		DisableBackgroundTasks: true,
	})
	for ev, err := range sess.Prompt(context.Background(), "数列 2,4 的下一项?") {
		if err != nil {
			t.Fatalf("会话错误: %v", err)
		}
		_ = ev
	}
	sess.Close()

	// 转录 JSONL:assistant 记录带思考块原文(openai 线格式
	// reasoning_content → BlockThinking)。
	recs, err := ts.Load(ts.MainPath(sid))
	if err != nil {
		t.Fatalf("读转录: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.Message == nil || r.Message.Role != llm.RoleAssistant {
			continue
		}
		for _, b := range r.Message.Content {
			if b.Type == llm.BlockThinking && b.Thinking == "思考原文:先核对数列增量。" {
				found = true
			}
		}
	}
	if !found {
		raw, _ := json.Marshal(recs)
		t.Fatalf("转录缺思考原文(存回不可读): %s", clipT(string(raw), 400))
	}

	// Resume 恢复:会话历史带思考块(出站传回的数据源在位)。
	sess2 := agentcore.NewSession(agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{"测试基线"},
		PermissionMode:         permission.ModeDefault,
		Transcript:             ts,
		SessionID:              sid,
		MaxTurns:               2,
		DisableBackgroundTasks: true,
	})
	defer sess2.Close()
	if err := sess2.Resume(sid); err != nil {
		t.Fatalf("恢复会话: %v", err)
	}
	resumed := false
	for _, msg := range sess2.Messages() {
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == llm.BlockThinking && b.Thinking != "" {
				resumed = true
			}
		}
	}
	if !resumed {
		t.Fatal("恢复后的会话历史无思考块(传回数据源丢失)")
	}
}

func clipT(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
