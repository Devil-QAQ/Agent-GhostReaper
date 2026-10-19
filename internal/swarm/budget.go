package swarm

import (
	"context"
	"fmt"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
)

// ---- 任务级总闸:整体超时(第四章 §6,实施于 P2-04) ----
//
// 每个任务可设整体超时(绝对墙钟):deadline = 任务 created 事件发生
// 时刻 + 超时秒数(锚点钉在不可变事件流上,见 rings.TaskGate)。虫群
// 单位领取任务包时据此推导本单位的墙钟预算上限:
//
//	单位 MaxDuration = min(RunSeconds, 距任务收尾线的剩余时间)
//
// 引擎语义(Norma Settlement)保证:墙钟撞线(含工具执行中)不打断审计——
// 就地进入收尾环节把已发现写回图,终因 timeout、步骤落 exhausted(R16
// 暂停接力)。已过收尾线的领取(竞态:收尾判定与新单位起飞赛跑)收敛为
// 1 秒墙钟——立即进收尾,绝不放任无限跑。

// taskDeadlineOf 事件流锚点 → 收尾线;未设超时(0 秒)返回零值(不限)。
func taskDeadlineOf(timeoutSeconds int, createdAt time.Time) time.Time {
	if timeoutSeconds <= 0 || createdAt.IsZero() {
		return time.Time{}
	}
	return createdAt.Add(time.Duration(timeoutSeconds) * time.Second)
}

// unitMaxDuration 单位墙钟预算:运行预算与任务收尾线取小。maxDur 是已经
// 归一的运行预算(RunSeconds,0 = 不限——此时收尾线就是唯一上限,
// P4-01 默认不限后总闸仍封顶);deadline 零值(不限)原样返回。
func unitMaxDuration(maxDur time.Duration, deadline time.Time) time.Duration {
	if deadline.IsZero() {
		return maxDur
	}
	rem := time.Until(deadline)
	if maxDur <= 0 || rem < maxDur {
		if rem < time.Second {
			return time.Second // 已过收尾线:立即进收尾,不放任无限跑
		}
		return rem
	}
	return maxDur
}

// ---- token 逐次记账 + 预算里程碑(第四章 §6 账本分工) ----

// UsageTracker 把引擎逐轮上报的累计用量(kind=usage,每模型轮一次)切成
// 逐次增量行(token 独立表一行 = 一次模型调用);MaxTurns > 0 时在轮次
// 过半时落一次聚合里程碑(blackbox budget_half,单位预算专用;主宰等
// 规划会话置 0 关闭里程碑)。记账走独立 ctx:横切记账不随任务熔断断链。
// swarm 单位与 overmind 会话共用(四类记录分工的统一记账器)。
// P2-06e:ModelOf 非空时逐次行按"服务该次调用的模型"记账——主备切换
// 后同会话的 token 按模型分开记账;nil 退静态 Model 字段。
type UsageTracker struct {
	Store      *rings.Store
	TaskID     string
	Agent      string // 记账归属(如 swarm:u1 / overmind)
	Model      string
	ModelOf    func() string // 按请求解析模型名(failover 链注入;nil=用 Model)
	Transcript string        // 会话原文 JSONL 的文件名锚点
	MaxTurns   int           // >0 时启用轮次过半里程碑
	StepID     string        // 里程碑留痕锚点(单位侧填,规划会话留空)
	UnitID     string

	turns        int
	halfRecorded bool
	prev         llm.Usage
}

// Turns 已记账的模型轮次(P2-05 重锚定的剩余预算读数;与 OnUsage 同
// goroutine 调用,无并发)。
func (t *UsageTracker) Turns() int { return t.turns }

// beginSegment 锚定新执行段的增量切分基线(P2-06f 修订):引擎的
// KindUsage 是单次 Prompt 运行内的累计(每段从零起算,跨段/跨进程不
// 连会计——Norma query 循环的 usage 不从会话恢复值起算),段首清零
// 基线,保证每行恰为本次模型调用的真实增量:段间不产生负增量(审批
// 续跑/模型重试段),重启收养的原会话续也不重复计历史(历史行的记账
// 属上一进程的原单位)。
func (t *UsageTracker) beginSegment() {
	t.prev = llm.Usage{}
}

// modelOfThisCall 本次调用行的记账模型(动态优先,退静态)。
func (t *UsageTracker) modelOfThisCall() string {
	if t.ModelOf != nil {
		if m := t.ModelOf(); m != "" {
			return m
		}
	}
	return t.Model
}

// OnUsage 处理一次累计用量事件:切增量入 token 独立表;轮次恰过半时记
// 一次黑匣子里程碑。返回错误由调用方汇总暴露——不中断执行,也不静默
// 吞掉审计缺口。
func (t *UsageTracker) OnUsage(cur llm.Usage) error {
	t.turns++
	if err := t.Store.RecordTokens(context.Background(), rings.TokenUsage{
		TaskID: t.TaskID, Agent: t.Agent, Model: t.modelOfThisCall(), TranscriptID: t.Transcript,
		Turn:             t.turns,
		InputTokens:      cur.InputTokens - t.prev.InputTokens,
		OutputTokens:     cur.OutputTokens - t.prev.OutputTokens,
		CacheReadTokens:  cur.CacheReadTokens - t.prev.CacheReadTokens,
		CacheWriteTokens: cur.CacheWriteTokens - t.prev.CacheWriteTokens,
	}); err != nil {
		return fmt.Errorf("swarm: token 用量记账: %w", err)
	}
	t.prev = cur
	if t.MaxTurns > 0 && !t.halfRecorded && t.turns >= (t.MaxTurns+1)/2 {
		t.halfRecorded = true
		if err := blackbox.Record(context.Background(), t.Store, t.TaskID, t.Agent,
			rings.BlackboxRecord{
				Type: rings.BlackboxBudgetHalf, StepID: t.StepID, UnitID: t.UnitID,
				Detail: map[string]any{
					"turns": t.turns, "max_turns": t.MaxTurns,
					"note": "轮次预算过半(聚合里程碑)",
				},
			}); err != nil {
			return fmt.Errorf("swarm: 预算过半留痕: %w", err)
		}
	}
	return nil
}
