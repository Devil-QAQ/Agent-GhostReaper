package swarm

import (
	"context"
	"fmt"
	"time"

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

// ---- 轮次预算里程碑(模型请求用量独立在 usage.go 记账) ----

// turnBudgetTracker 只消费引擎正常轮次通知,维护门卫读数与过半里程碑。
// 失败尝试与辅助调用消耗记入用量表,不冒充完成轮次或占用该预算。
type turnBudgetTracker struct {
	Store        *rings.Store
	TaskID       string
	Agent        string
	MaxTurns     int
	StepID       string
	UnitID       string
	turns        int
	halfRecorded bool
}

func (t *turnBudgetTracker) Turns() int { return t.turns }

func (t *turnBudgetTracker) OnTurn() error {
	t.turns++
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
