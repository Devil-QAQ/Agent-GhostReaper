package swarm

import (
	"time"
)

// ---- 运行时钟(P4-02 ④:面板会话时长改有效运行时长口径) ----
//
// 时间账的活体读数源:billable(有效执行段合计)在 drive 段循环里逐段
// 累计,等待(弹门 / 模型退避 / 单位挂起 / 任务冬眠)期间不前进——
// session_finished note 只在落地时落账,面板需要"跑着的时候也能读到
// 当前的 billable"。本表按转录锚登记在飞单位的活体读数:
//
//	acc   已收拢段的 billable 累计(与 drive 局部变量同值)
//	since 当前活动段起点(零值 = 等待中,读数不前进)
//
// 读数 = acc + (since 非零 ? now − since : 0)。落地即注销,落账后的
// 权威值在 session_finished note(含原会话续跑链:同一转录多段落账,
// 读侧按转录求和)。

// runClock 一只单位的活动时钟条目。
type runClock struct {
	acc   time.Duration
	since time.Time // 零值 = 等待中
}

// clockKey 时钟登记键(转录锚,与单位列表行同键)。
func clockKey(taskID, transcriptID string) string {
	return taskID + "\x00" + transcriptID
}

// clockSet 登记/刷新一只单位的活动时钟:acc 为当前累计 billable,
// active=true 标记进入活动段(since=now),false 标记进入等待(读数
// 冻结在 acc)。
func (e *Executor) clockSet(taskID, transcriptID string, acc time.Duration, active bool) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	c := e.clocks[clockKey(taskID, transcriptID)]
	if c == nil {
		c = &runClock{}
		e.clocks[clockKey(taskID, transcriptID)] = c
	}
	c.acc = acc
	if active {
		c.since = time.Now()
	} else {
		c.since = time.Time{}
	}
}

// clockDrop 注销一只单位的活动时钟(单位落地)。
func (e *Executor) clockDrop(taskID, transcriptID string) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	delete(e.clocks, clockKey(taskID, transcriptID))
}

// ClockBillable 在飞单位的活体 billable 读数(等待中读数不前进);
// 未登记(未起飞/已落地)返回 ok=false。
func (e *Executor) ClockBillable(taskID, transcriptID string) (time.Duration, bool) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()
	c, ok := e.clocks[clockKey(taskID, transcriptID)]
	if !ok {
		return 0, false
	}
	total := c.acc
	if !c.since.IsZero() {
		total += time.Since(c.since)
	}
	return total, true
}
