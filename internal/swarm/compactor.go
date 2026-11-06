// compactor.go P4-39 ②「微压缩(旧轮工具结果体清理)」的宿主实现。
//
// 核实结论(2026-11-05,本阶段开工核实):集成的 Norma v0.4.1 带两级压缩
// ——微压缩(清旧轮可压缩工具结果体、保最近 5 条、60% 有效窗口触发、
// 替换文本 "[Old tool result content cleared]")与摘要压缩(引擎默认
// 85~90% 区间,边界+摘要非破坏,Reactive 413 兜底)。但引擎的可压缩
// 工具表只认内置工具名(Read/Bash/Grep/…,首字母大写),本项目虫群工具
// 为小写自定义名(bash/read/http_probe/…)——直接开 opts.Compaction 对
// 单位会话微压缩空转。按子计划②的"不带则按同语义自实现"分支(此处为
// "带但工具表不覆盖",等效),本文件以宿主 Compactor 实现:
//   - 微压缩:同语义复刻(同门限算式、同保 5 条、同替换文本、同幂等),
//     可压缩表换成本项目工具名;submit_* 受控回流入口的结果是事实回执
//     (P4-39 ③防返工纪律:清出的只是过程叙述),不进可压缩表。
//   - 摘要压缩:整体委托引擎 compaction.Compactor(时机与参数走默认,
//     不提前——前缀缓存靠长稳定前缀,激进提前得不偿失,ARTEX 对照结论)。
//
// 原文可回读:微压缩只改写内存会话的 API 视图,转录 JSONL 在消息出生时
// 已记录原文(会话原文面板可查);落盘 blob(工具输出存档)永不删。
// 记账(P4-39 ④):每次实际清理记黑匣子 micro_compact(清出条数/字节数/
// 触发时估算 token),运行日志同步一行。
package swarm

import (
	"context"

	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// microClearable 本项目可压缩工具表(镜像引擎 compactableTools 的取舍
// 意图:文件读/命令/搜索/网络类过程输出;排除受控回流入口与短回执)。
var microClearable = map[string]bool{
	"bash": true, "read": true, "list_claims": true,
	"http_probe": true, "port_probe": true,
	"web_search": true, "web_fetch": true,
}

// microKeepN 保最近 N 条可压缩结果不清(引擎同款定值 5)。
const microKeepN = 5

// microClearedText 清理替换文本(与引擎 microClearedMessage 同字面——
// 引擎侧 isCleared 幂等检查因此对本实现产出的占位同样成立)。
const microClearedText = "[Old tool result content cleared]"

// unitCompactor 单位会话的宿主压缩器:自有微压缩(本项目工具名)+
// 委托引擎摘要压缩。实现 harness.Compactor。
type unitCompactor struct {
	inner    *compaction.Compactor
	window   int // 上下文窗口(token)
	reserved int // 摘要预留(与引擎 reservedForSummary 同式)
	store    *rings.Store
	taskID   string
	unitID   string
	stepID   string
}

// newUnitCompactor 构建单位压缩器;window <= 0(未配置)返回 nil——
// 不挂压缩(与牧者 s.window() 的装配纪律一致)。maxOutput 与会话
// MaxTokens 同源(摘要预留 min(上限,20000) 的口径);摘要器经引擎
// ProviderSummarizer 从同一 Provider 构造(摘要压缩开启、参数默认)。
func newUnitCompactor(store *rings.Store, taskID, stepID, unitID string, window, maxOutput int, p llm.Provider) *unitCompactor {
	if window <= 0 {
		return nil
	}
	if maxOutput <= 0 || maxOutput > 20000 {
		maxOutput = 20000
	}
	// Provider 缺位(单测形态)时摘要器置 nil:引擎自动跳过摘要压缩
	//(compaction.New 对 nil 摘要器的既定行为),微压缩照常——防御性
	// 降级,生产路径 Provider 恒在(无 Provider 的单位首轮即失败)。
	var summarizer compaction.Summarizer
	if p != nil {
		summarizer = compaction.ProviderSummarizer(p, maxOutput, false)
	}
	return &unitCompactor{
		inner: compaction.New(
			compaction.Config{ContextWindow: window, MaxOutputTokens: maxOutput}, summarizer),
		window:   window,
		reserved: maxOutput,
		store:    store, taskID: taskID, stepID: stepID, unitID: unitID,
	}
}

// microThreshold 微压缩触发线(与引擎同式:有效窗口 × 6/10)。
func (c *unitCompactor) microThreshold() int { return (c.window - c.reserved) * 6 / 10 }

// Pre 请求前条件化历史:过微压缩线 → 清本项目工具的旧轮结果体(保最近
// 5 条)→ 记黑匣子 → 委托引擎(清理过则 lastInputTokens 置 0 强制重估,
// 与引擎清理后的重估纪律一致;未过线原参直通)。摘要压缩时机全在引擎。
func (c *unitCompactor) Pre(ctx context.Context, msgs []llm.Message, lastInputTokens int) []llm.Message {
	tok := lastInputTokens
	if tok <= 0 {
		tok = compaction.EstimateTokens(llm.MessagesForAPI(msgs)) * 4 / 3
	}
	if tok <= c.microThreshold() {
		return c.inner.Pre(ctx, msgs, lastInputTokens)
	}
	if out, cleared, freedChars := microCompactGR(msgs); cleared > 0 {
		c.noteMicro(tok, cleared, freedChars)
		return c.inner.Pre(ctx, out, 0)
	}
	return c.inner.Pre(ctx, msgs, lastInputTokens)
}

// Reactive 413 强制摘要压缩:委托引擎(同款语义)。
func (c *unitCompactor) Reactive(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool) {
	return c.inner.Reactive(ctx, msgs)
}

// IsOverflow 委托引擎的超长错误判定。
func (c *unitCompactor) IsOverflow(err error) bool { return c.inner.IsOverflow(err) }

// microCompactGR 清旧轮可压缩结果体:收集本项目工具的 tool_use 顺序,
// 除最近 microKeepN 条外清空结果体。返回(新消息, 清出条数, 清出字符数)。
// 幂等:已清空的结果体(单块且为占位文本)跳过。
func microCompactGR(msgs []llm.Message) ([]llm.Message, int, int) {
	var ids []string
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse && microClearable[b.Name] {
				ids = append(ids, b.ID)
			}
		}
	}
	if len(ids) <= microKeepN {
		return msgs, 0, 0
	}
	clearSet := make(map[string]bool, len(ids)-microKeepN)
	for _, id := range ids[:len(ids)-microKeepN] {
		clearSet[id] = true
	}
	cleared, freedChars := 0, 0
	out := append([]llm.Message(nil), msgs...)
	for i := range out {
		var nc []llm.ContentBlock
		for j, b := range out[i].Content {
			if b.Type != llm.BlockToolResult || !clearSet[b.ToolUseID] {
				continue
			}
			if len(b.Content) == 1 && b.Content[0].Type == llm.BlockText && b.Content[0].Text == microClearedText {
				continue
			}
			if nc == nil {
				nc = append([]llm.ContentBlock(nil), out[i].Content...)
			}
			for _, cb := range b.Content {
				freedChars += len(cb.Text) + len(cb.Thinking)
			}
			nc[j].Content = []llm.ContentBlock{llm.TextBlock(microClearedText)}
			cleared++
		}
		if nc != nil {
			out[i].Content = nc
		}
	}
	if cleared == 0 {
		return msgs, 0, 0
	}
	return out, cleared, freedChars
}

// noteMicro 微压缩动作记黑匣子与运行日志(独立 ctx,不随任务熔断断链)。
func (c *unitCompactor) noteMicro(tok, cleared, freedChars int) {
	detail := map[string]any{
		"cleared": cleared, "freed_chars": freedChars,
		"tokens_est": tok, "window": c.window,
	}
	if err := blackbox.Record(context.Background(), c.store, c.taskID, "swarm:"+c.unitID,
		rings.BlackboxRecord{Type: rings.BlackboxMicroCompact, StepID: c.stepID, UnitID: c.unitID, Detail: detail}); err != nil {
		runlog.Errorf("黑匣子写入失败: 微压缩 %s/%s: %v", c.taskID, c.unitID, err)
	}
	runlog.Infof("微压缩: 任务 %s 单位 %s 清出旧轮工具结果 %d 条(清出约 %d 字符,触发时估算 %d token,窗口 %d)",
		c.taskID, c.unitID, cleared, freedChars, tok, c.window)
}
