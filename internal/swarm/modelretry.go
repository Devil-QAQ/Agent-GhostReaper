package swarm

import (
	"context"
	"fmt"
	"time"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
)

// ---- 意图级模型错误长退避与 blocked 态(P2-06e 起,P4-27 ①⑥ 重定) ----
//
// Provider 层(failover.Chain)已消化建连重试/空响应重试/零交付重放/
// 主备切换;穿透到会话层的模型错误(主备皆亡、确定性 4xx、超长)在
// 意图级处置。重试语义表(P4-27 ⑥ 合成):
//
//	瞬时失败 → 长退避循环续试(1m/5m/15m 梯度,模型不通持续等,
//	           原会话续跑不换人,重试只追发短续跑指令);仅连续
//	           失败跨深阈值(默认 15 分钟全不通,可配)才落 blocked;
//	确定性拒绝(4xx determinant / 上下文超长)→ 立即 blocked 不重试
//	           (确定性错误重试无意义纯烧 token),归因 determinant;
//	额度耗尽 → 链上切备(本层只见穿透后的错误)。
//
// 退避等待与零进展失败段不计预算(block_wait,故障不吃预算);重试
// 追发短续跑指令(requestID 防续跑消息重复注入——任务包原文只在首发
// 注入一次);blocked 等配置恢复/切换自动重开(终态任务不重开)。
// 确定性拒绝不进熔断计数与任务受阻计数(链层分类已排除;单位侧经
// RunResult.BlockedClass 透出,归因随步骤 detail 落账)。

// 模型失败处置默认值(Executor 字段零值时取用;测试缩小梯子与阈值,
// 不改语义)。
const (
	// SettingKeyBlockedDeepMinutes 长退避深阈值的全局键(P4-27 ①,Agent
	// 栏虫群参数"受阻判定时长",分钟)。
	SettingKeyBlockedDeepMinutes = "swarm.modelretry.deep_minutes"
	// DefaultBlockedDeepMinutes 深阈值默认:连续失败 15 分钟全不通才落
	// blocked(造物主拍板 2026-10-24)。
	DefaultBlockedDeepMinutes = 15
)

// DefaultModelRetryLadder 长退避梯度(第 1/2/3+ 次重试前的等待)。
var DefaultModelRetryLadder = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// blocked 归因(随步骤 detail 落账,P4-27 ⑥⑦):
// model_fault = 模型故障(跨深阈值的普遍故障,计任务受阻);
// determinant = 确定性拒绝(程序缺陷/配置错误,不计故障计数)。
const (
	BlockedClassModelFault  = "model_fault"
	BlockedClassDeterminant = "determinant"
)

// modelBlockedDeepAfter 深阈值解析:测试旋钮(Executor 字段)> 全局键
// (分钟) > 默认 15 分钟。
func (e *Executor) modelBlockedDeepAfter(ctx context.Context) time.Duration {
	if e.BlockedDeepAfter > 0 {
		return e.BlockedDeepAfter
	}
	if n, ok := settingPositiveInt(ctx, e.Store, SettingKeyBlockedDeepMinutes); ok {
		return time.Duration(n) * time.Minute
	}
	return DefaultBlockedDeepMinutes * time.Minute
}

// modelRetryLadder 退避梯度(测试旋钮 > 默认)。
func (e *Executor) modelRetryLadder() []time.Duration {
	if len(e.ModelRetryLadder) > 0 {
		return e.ModelRetryLadder
	}
	return DefaultModelRetryLadder
}

// modelRetryBackoff 第 n 次重跑前的退避:梯度取档(超界封顶档);深阈
// 值将至时截断到剩余时长——blocked 落在阈值线上,不无谓拖后。
func (e *Executor) modelRetryBackoff(n int, streak, deepAfter time.Duration) time.Duration {
	ladder := e.modelRetryLadder()
	i := n - 1
	if i >= len(ladder) {
		i = len(ladder) - 1
	}
	d := ladder[i]
	if rem := deepAfter - streak; rem > 0 && d > rem {
		d = rem
	}
	if d < 0 {
		d = 0
	}
	return d
}

// nextRequestID 单位内顺序生成逻辑注入标记(首发/审批续跑各一个;
// 重试沿用当前标记,只追发短指令)。
func nextRequestID(unitID string, seq *int) string {
	*seq++
	return fmt.Sprintf("req-%s-%d", unitID, *seq)
}

// modelFailure 判定一次段执行是否为模型故障(非取消的流断,或引擎
// model_error 终因)。返回简报与原始错误(ok=false 表示不是模型故障,
// 按原路径;错误供确定性分类)。
func modelFailure(segCtx context.Context, streamErr, termErr error, res *RunResult) (string, error, bool) {
	if segCtx.Err() == nil && streamErr != nil {
		return clipLine(streamErr.Error(), 200), streamErr, true
	}
	if res.Reason == string(harness.ReasonModelError) {
		brief := "模型错误"
		err := termErr
		if termErr != nil {
			brief = clipLine(termErr.Error(), 200)
		}
		return brief, err, true
	}
	return "", nil, false
}

// blockedReason 受阻终因文案(模型故障:次数 + 连续失败时长 + 简报)。
func blockedReason(fails int, streak time.Duration, brief string) string {
	return fmt.Sprintf("模型连续失败(%d 次,持续 %s): %s", fails, roundDur(streak), brief)
}

// blockedReasonDeterminant 确定性拒绝的受阻终因(不重试)。
func blockedReasonDeterminant(brief string) string {
	return fmt.Sprintf("模型确定性拒绝(不重试): %s", brief)
}

// roundDur 时长的人话呈现(秒级取整;分钟以上"X 分 Y 秒")。
func roundDur(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return fmt.Sprintf("%d 分 %02d 秒", int(d.Minutes()), int(d.Seconds())%60)
}

// modelRetryNote 重试追发的短续跑指令:携带 requestID 与次数,transcript
// 可查;不重复任务包原文(防续跑消息重复注入)。
func modelRetryNote(reqID string, attempt int) string {
	return fmt.Sprintf("【模型重试 · %s · 第 %d 次】刚才的模型调用失败,非任务问题;请继续完成当前意图,已完成的工作仍然有效。",
		reqID, attempt)
}

// sleepInterruptible 可被 ctx 打断的等待(false=被取消)。
func sleepInterruptible(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// modelNowFunc 记账模型解析(failover.ModelNow 的薄封装,P4-41 统一入口)。
func modelNowFunc(p llm.Provider, taskID, transcriptID string) func() string {
	return failover.ModelNow(p, taskID, transcriptID)
}

// ModelFaultBlockedCount 图上模型故障归因的 blocked 步骤数(P4-27 ②:
// 报告侧受阻派生——确定性归因不计故障;面板受阻另走链级实时信号,
// 见 server)。跨包消费(report)。
func ModelFaultBlockedCount(ctx context.Context, store *rings.Store, taskID string) int {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, StepBlocked)
	if err != nil {
		return 0
	}
	n := 0
	for _, st := range steps {
		if class, _ := st.Detail["blocked_class"].(string); class != BlockedClassDeterminant {
			n++
		}
	}
	return n
}
