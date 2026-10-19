package swarm

import (
	"context"
	"fmt"
	"time"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// ---- 意图级模型错误退避重跑与 blocked 态(P2-06e,第七章) ----
//
// Provider 层(failover.Chain)已消化建连重试/空响应重试/零交付重放/
// 主备切换;穿透到会话层的模型错误(主备皆亡、确定性 4xx、超长)在
// 意图级退避重跑:重试只追发短续跑指令(requestID 防续跑消息重复注入
// ——任务包原文只在首发注入一次),退避等待与零进展失败段不计预算
//(block_wait,故障不吃预算);耗尽后 Step 落 blocked,等配置恢复/
// 切换自动重开(终态任务不重开)。

// 模型错误重跑默认值(Executor 字段零值时取用;测试缩小不改语义)。
const (
	DefaultModelRetryLimit   = 3               // 退避重跑次数上限(不含首发)
	DefaultModelRetryBackoff = 1 * time.Second // 退避基值,指数(1s/2s/4s)
)

// rerunLimitResolver 重试覆盖⑤(意图级重跑)的解析面:failover 链按
// 当前将路由的腿(任务感知)解析 per-配置覆盖值(P2-08c)。ok=false
// 表示该腿未覆盖,取全局默认。
type rerunLimitResolver interface {
	RerunLimit(ctx context.Context) (int, bool)
}

// modelRetryLimit 意图级模型错误重跑次数上限:测试旋钮(Executor 字段)
// > 链上当前腿的 per-配置覆盖(0=默认 / -1=关闭→0 次 / >0=值) >
// 全局默认。
func (e *Executor) modelRetryLimit(ctx context.Context) int {
	if e.ModelRetryLimit > 0 {
		return e.ModelRetryLimit
	}
	if r, ok := e.Provider.(rerunLimitResolver); ok && ctx != nil {
		if v, covered := r.RerunLimit(ctx); covered {
			return v
		}
	}
	return DefaultModelRetryLimit
}

// modelRetryBackoff 第 n 次重跑前的退避时长(指数,上限 30s)。
func (e *Executor) modelRetryBackoff(n int) time.Duration {
	base := DefaultModelRetryBackoff
	if e.ModelRetryBackoff > 0 {
		base = e.ModelRetryBackoff
	}
	d := base << (n - 1)
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
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
// model_error 终因)。返回简报(ok=false 表示不是模型故障,按原路径)。
func modelFailure(segCtx context.Context, streamErr, termErr error, res *RunResult) (string, bool) {
	if segCtx.Err() == nil && streamErr != nil {
		return clipLine(streamErr.Error(), 200), true
	}
	if res.Reason == string(harness.ReasonModelError) {
		brief := "模型错误"
		if termErr != nil {
			brief = clipLine(termErr.Error(), 200)
		}
		return brief, true
	}
	return "", false
}

// blockedReason 受阻终因文案(次数 + 最近一次故障简报)。
func blockedReason(fails int, brief string) string {
	return fmt.Sprintf("模型连续失败(%d 次): %s", fails, brief)
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

// modelNowFunc Provider 支持按请求解析当前模型时返回其查询函数(failover
// 链主备切换后 token 按模型分开记账);普通 Provider 返回 nil。
func modelNowFunc(p llm.Provider) func() string {
	type activeModeler interface{ ActiveModel() string }
	if am, ok := p.(activeModeler); ok {
		return am.ActiveModel
	}
	return nil
}
