package failover

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/Autumn-27/norma/llm"
)

type usageSinkKey struct{}
type usageSink struct {
	report func(string, llm.Usage, bool)
	seen   atomic.Bool
}

// 非流式响应转事件时,完整用量已知但事件可能尚未送完就被取消。
// 暂存原返回值供请求端记账,不把它伪装成已交付事件而改变重试语义。
type returnedUsageKey struct{}
type returnedUsage struct {
	mu    sync.Mutex
	known bool
	usage llm.Usage
}

func preserveReturnedUsage(ctx context.Context, u llm.Usage) {
	if r, ok := ctx.Value(returnedUsageKey{}).(*returnedUsage); ok {
		r.mu.Lock()
		r.known, r.usage = true, u
		r.mu.Unlock()
	}
}

func (r *returnedUsage) resolve(streamed llm.Usage) llm.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.known {
		return r.usage
	}
	return streamed
}

// ObserveUsage 供请求记账使用。链逐服务腿尝试报告,外层 finish 仅在
// 普通 Provider 未报告时兜底一次,避免主备重放丢账或成功行重复入账。
// complete 表示请求正常返回,不代表工具执行或任务完成。SDK 内部重试不可见。
func ObserveUsage(ctx context.Context, report func(string, llm.Usage, bool)) (context.Context, func(string, llm.Usage, bool)) {
	s := &usageSink{report: report}
	return context.WithValue(ctx, usageSinkKey{}, s), func(model string, u llm.Usage, complete bool) {
		if !s.seen.Swap(true) {
			s.report(model, u, complete)
		}
	}
}

func reportUsage(ctx context.Context, model string, u llm.Usage, complete bool) {
	if s, ok := ctx.Value(usageSinkKey{}).(*usageSink); ok {
		s.seen.Store(true)
		s.report(model, u, complete)
	}
}

// 已知本地没有可用服务腿,未调用 Provider,不虚记一次未知用量请求。
func noUsageAttempt(ctx context.Context) {
	if s, ok := ctx.Value(usageSinkKey{}).(*usageSink); ok {
		s.seen.Store(true)
	}
}
