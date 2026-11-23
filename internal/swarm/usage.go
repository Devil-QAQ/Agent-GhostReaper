package swarm

import (
	"context"
	"iter"
	"sync/atomic"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// MeterProvider 为主会话和辅助调用提供唯一请求记账入口。按可观察的
// 服务腿尝试写行,包括重放、失败、取消与提前停流;不依赖引擎正常结束
// 通知,也不在终态补计。Purpose 空表示主会话,其成功请求连续编号,
// 未完成请求记 interrupted;辅助请求保留原用途且不占主会话轮次。
// 无用量返回记 UsageMissing,不估算成本。SDK 内部不可见的重试不虚构行。
func MeterProvider(p llm.Provider, store *rings.Store, row rings.TokenUsage, onError ...func(error)) llm.Provider {
	if p == nil {
		return nil
	}
	m := &meteredProvider{Provider: p, store: store, row: row,
		model: failover.ModelNow(p, row.TaskID, row.TranscriptID)}
	if len(onError) > 0 {
		m.onError = onError[0]
	}
	return m
}

type meteredProvider struct {
	llm.Provider
	store   *rings.Store
	row     rings.TokenUsage
	model   func() string
	turns   atomic.Int64
	onError func(error)
}

func (env *unitEnv) meteredActionReview() ActionReviewFunc {
	return ActionReviewFor(MeterProvider(env.Executor.Provider, env.Executor.Store, rings.TokenUsage{
		TaskID: env.TaskID, Agent: "swarm:" + env.UnitID, Model: env.Executor.Model,
		TranscriptID: env.TranscriptID, Purpose: "action_review",
	}))
}

func (p *meteredProvider) callContext(ctx context.Context) context.Context {
	return failover.WithLLMSession(failover.WithTask(ctx, p.row.TaskID), p.row.TranscriptID)
}

func (p *meteredProvider) record(model string, u llm.Usage, complete bool) {
	if p.store == nil {
		return
	}
	r := p.row
	if r.Purpose == "" {
		if complete {
			r.Turn = int(p.turns.Add(1))
		} else {
			r.Purpose, r.Turn = "interrupted", 0
		}
	}
	if model != "" {
		r.Model = model
	}
	r.InputTokens, r.OutputTokens = u.InputTokens, u.OutputTokens
	r.CacheReadTokens, r.CacheWriteTokens = u.CacheReadTokens, u.CacheWriteTokens
	r.UsageMissing = u == (llm.Usage{})
	if err := p.store.RecordTokens(context.Background(), r); err != nil {
		runlog.Errorf("模型用量记账失败: 任务 %s 角色 %s 用途 %s: %v", r.TaskID, r.Agent, r.Purpose, err)
		if p.onError != nil {
			p.onError(err)
		}
	}
}

func (p *meteredProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx, finish := failover.ObserveUsage(p.callContext(ctx), p.record)
	msg, stop, usage, err := p.Provider.Complete(ctx, req)
	finish(p.modelName(), usage, err == nil)
	return msg, stop, usage, err
}

func (p *meteredProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		acc := llm.NewAccumulator()
		ctx, finish := failover.ObserveUsage(p.callContext(ctx), p.record)
		complete := false
		defer func() { finish(p.modelName(), acc.Usage, complete) }()
		for ev, err := range p.Provider.Stream(ctx, req) {
			if err == nil {
				acc.Add(ev)
			}
			if !yield(ev, err) || err != nil {
				return
			}
		}
		complete = true
	}
}

func (p *meteredProvider) modelName() string {
	if p.model != nil {
		return p.model()
	}
	return p.row.Model
}
