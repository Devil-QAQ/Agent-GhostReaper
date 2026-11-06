package failover

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// ---- 流式读超时(P4-42 ①,问题清单 #69:主宰模型调用挂死) ----
//
// 病根:LLM 流式调用请求建立后,对端/中间代理停发数据——HTTP 连接
// ESTABLISHED 不动,调用永不出结果、不报错、不重试、不切备,主宰循环
// 干等(任务假活:心跳照跳、零模型失败日志、虫群无活)。SDK 建连重试
// 只覆盖建连阶段,中途停发无感。
//
// 修法(按数据间隔判,不按总时长——慢流不误杀):
//   - 相邻数据事件间隔超过 ReadStall(默认 5 分钟)即判失败;
//   - 首个数据事件前允许更长间隔 ReadFirst(默认 10 分钟,覆盖大上下文
//     长思考——思考期间对端可能一个字节都不发);
//   - 单请求整体墙钟 ReadOverall(默认 30 分钟)兜底;
//   - 判死即取消在途请求,上抛瞬时类错误——走既有错误链(链内零交付
//     重放/熔断切备、单位侧长退避/受阻,全部现成)。
//
// 慢速持续出字(间隔远小于阈值)与长思考首包(小于首包宽限)完整成功,
// 不因总时长长被杀。Complete(非流式,含关流式垫片)整响应一次到达:
// 首包宽限即响应上限,整体墙钟取小者生效。
const (
	// DefaultReadStall 相邻数据事件的最大间隔。
	DefaultReadStall = 5 * time.Minute
	// DefaultReadFirst 首个数据事件前的最大等待(建连+长思考)。
	DefaultReadFirst = 10 * time.Minute
	// DefaultReadOverall 单请求整体墙钟上限。
	DefaultReadOverall = 30 * time.Minute
)

// readTimeoutWrap 给一条腿的 Provider 挂读超时(Chain.buildProvider 与
// 测试注入共用同一入口;零值字段归一为默认)。
func (c *Chain) readTimeoutWrap(p llm.Provider) llm.Provider {
	first, stall, overall := c.ReadFirst, c.ReadStall, c.ReadOverall
	if first <= 0 {
		first = DefaultReadFirst
	}
	if stall <= 0 {
		stall = DefaultReadStall
	}
	if overall <= 0 {
		overall = DefaultReadOverall
	}
	return readTimeoutProvider{inner: p, first: first, stall: stall, overall: overall}
}

// readTimeoutProvider 逐段读超时装饰器:内层事件经通道转发,看门狗
// 计时首包/间隔/整体三限;判死取消派生 ctx(掐断在途 HTTP),上抛合成
// 错误(瞬时类,无状态码与额度/超长特征词,进既有重试链)。
type readTimeoutProvider struct {
	inner   llm.Provider
	first   time.Duration // 首事件前允许的等待
	stall   time.Duration // 相邻事件间允许的间隔
	overall time.Duration // 整体墙钟
}

// errReadTimeout 判死错误(合成,不带底层取消痕迹——context.Canceled/
// DeadlineExceeded 会被 Classify 归为调用方取消直接上抛,进不了重试链)。
func errReadTimeout(kind string, limit time.Duration) error {
	return fmt.Errorf("failover: 模型响应停滞(%s超过 %.0f 秒,已掐断在途请求)", kind, limit.Seconds())
}

func (p readTimeoutProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	// 非流式整响应一次到达:等待上限取首包宽限与整体墙钟的小者。
	limit := p.first
	if p.overall > 0 && p.overall < limit {
		limit = p.overall
	}
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	msg, sr, usage, err := p.inner.Complete(rctx, req)
	if err != nil && rctx.Err() != nil && ctx.Err() == nil {
		err = errReadTimeout("无完整响应", limit)
	}
	return msg, sr, usage, err
}

func (p readTimeoutProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type item struct {
			ev  llm.StreamEvent
			err error
		}
		ch := make(chan item)
		go func() {
			defer close(ch)
			for ev, err := range p.inner.Stream(rctx, req) {
				ch <- item{ev, err}
				if err != nil {
					return
				}
			}
		}()
		// abort 判死收场:取消在途请求,后台排空内层收尾(取消后内层
		// 还会送出末个事件/错误,无人接收会卡住发送 goroutine),上抛
		// 合成错误并结束本迭代器。
		abort := func(kind string, limit time.Duration) {
			cancel()
			go func() {
				for range ch {
				}
			}()
			yield(llm.StreamEvent{}, errReadTimeout(kind, limit))
		}
		overallT := time.NewTimer(p.overall)
		defer overallT.Stop()
		idleT := time.NewTimer(p.first)
		defer idleT.Stop()
		gotFirst := false // idleT 语义开关:首事件前=首包宽限,其后=间隔阈值
		for {
			select {
			case <-overallT.C:
				abort("整体墙钟", p.overall)
				return
			case <-idleT.C:
				if gotFirst {
					abort("数据间隔", p.stall)
				} else {
					abort("首包", p.first)
				}
				return
			case it, ok := <-ch:
				if !ok {
					return // 内层正常收尾
				}
				if !idleT.Stop() {
					// 已触发但 select 先醒了 ch:非阻塞排掉旧值再重置。
					select {
					case <-idleT.C:
					default:
					}
				}
				idleT.Reset(p.stall)
				gotFirst = true
				if it.err != nil {
					// 真实错误或调用方取消(外层 ctx)原样上抛;本装饰器
					// 取消引发的内层收尾错误只会出现在判死之后——判死
					// 路径已上抛合成错误并返回,不会走到这里。
					yield(it.ev, it.err)
					return
				}
				if !yield(it.ev, nil) {
					// 调用方停止消费:取消内层并排空,不当失败处理。
					cancel()
					go func() {
						for range ch {
						}
					}()
					return
				}
			}
		}
	}
}
