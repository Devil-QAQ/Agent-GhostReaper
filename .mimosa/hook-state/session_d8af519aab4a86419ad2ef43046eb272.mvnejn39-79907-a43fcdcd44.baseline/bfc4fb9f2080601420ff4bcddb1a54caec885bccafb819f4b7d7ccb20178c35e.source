// Package ckpt 实现断点冻结信号(P3-07,第七章「故障与恢复语义」
// 断点续接):操作者存档断点后,虫群单位与主宰循环在各自的工具调用/
// 轮次边界看到信号即收场——单位落 session_finished(带 checkpointed
// 标记,会话保留可续)、循环退出让位。与冬眠门(hiber.Gate)的分工:
// 冬眠=内存冻结(挂起等待,原地解冻),断点=进程级收场(单位落地、
// 进程可亡,重启后原会话续跑)。信号只管"收场令"的运行时传递,生命
// 周期记账(task.lifecycle checkpointed)由 Manager 权威落账——先记账
// 后武装,账本是审计权威,门是执行层的同步原语。
//
// 信号在任务重新拉起(launch/收养/恢复)时解除——新进程/新循环体不应
// 出生即收场;解除由 Manager.launch 统一执行。
package ckpt

import "sync"

// Gate 断点冻结信号:taskID → 武装标记。Arm 后各边界轮询 Armed 即收场。
type Gate struct {
	mu    sync.Mutex
	armed map[string]bool
}

// New 构建断点信号门。
func New() *Gate { return &Gate{armed: map[string]bool{}} }

// Arm 武装断点:该任务的单位与循环在下一边界收场(幂等)。
func (g *Gate) Arm(taskID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed[taskID] = true
}

// Armed 任务当前是否已武装断点。
func (g *Gate) Armed(taskID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armed[taskID]
}

// Clear 解除信号(任务重新拉起时;幂等)。
func (g *Gate) Clear(taskID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.armed, taskID)
}
