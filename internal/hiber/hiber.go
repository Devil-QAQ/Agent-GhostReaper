// Package hiber 实现任务冬眠门(P2-08,第七章「任务冬眠(Hibernation)
// 语义」;P4-33 起冬眠改冻结收场):一个任务进入冬眠时,主宰循环、
// 虫群单位在各自边界收场落地(与断点冻结同约定),牧者会话入口、
// 审批裁决在同一扇门上冻结。门在收场完成后保持关闭至唤醒——期间
// 面板只读、审批冻结;单位侧的门等待仅剩收场过渡(收场令与门同发,
// 等待者带节拍复查见令即落地,不驻留门上过冬眠)。门只管"冻结/解冻"
// 的运行时信号,生命周期记账(task.lifecycle hibernated/唤醒事件)由
// Manager 权威落账——先记账后关闸、先记账后开闸,账本是审计权威,
// 门是执行层的同步原语。
//
// 单位级暂停(P2-08,第四章 §7"单位级暂停为同款语义的细粒度版")不用
// 本门:粒度是单个虫群单位,闩在 swarm.Executor 内(swarm 不需要感知
// 任务生命周期,只需要"这扇门开着吗")。
package hiber

import (
	"context"
	"sync"
	"time"
)

// Gate 任务冬眠门:taskID → 冬眠分片。分片的 ch 在运行期为已关闭通道
// (select 立即通过),Enter 时换成开放通道(全部等待者挂起),Exit 时
// 关闭(全部等待者解冻)。Generation 每次冬眠自增——等待方用它区分
// "等待期间发生过冬眠"(唤醒后需按 R14 复检前提、延长收尾线)。
type Gate struct {
	mu sync.Mutex
	m  map[string]*shard
}

type shard struct {
	ch  chan struct{}
	pre string // 冬眠前的生命周期状态(started|settling),唤醒时恢复
	gen uint64 // 冬眠代数(每次 Enter 自增)
}

// New 构建冬眠门。
func New() *Gate { return &Gate{m: map[string]*shard{}} }

// Enter 进入冬眠:换开放通道,记录冬眠前状态。已在冬眠时幂等(仅更新
// pre,Generation 不动——同一次冬眠)。调用方(Manager)保证生命周期
// 记账先于本调用。
func (g *Gate) Enter(taskID, pre string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.m[taskID]
	if s == nil {
		s = &shard{ch: make(chan struct{})}
		g.m[taskID] = s
	} else {
		select {
		case <-s.ch: // 运行中 → 换新通道冻结
			s.ch = make(chan struct{})
		default: // 已在冬眠 → 幂等,只更新 pre
			s.pre = pre
			return
		}
	}
	s.pre = pre
	s.gen++
}

// Exit 唤醒:关闭通道解冻全部等待者,返回冬眠前状态(空 = 本不在冬眠,
// 幂等无动作)。调用方(Manager)保证生命周期记账先于本调用。
func (g *Gate) Exit(taskID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.m[taskID]
	if s == nil {
		return ""
	}
	select {
	case <-s.ch: // 本就运行中
	default:
		close(s.ch)
	}
	pre := s.pre
	s.pre = ""
	return pre
}

// Active 任务当前是否冬眠中。
func (g *Gate) Active(taskID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.m[taskID]
	if s == nil {
		return false
	}
	select {
	case <-s.ch:
		return false
	default:
		return true
	}
}

// Generation 当前冬眠代数(累计冬眠次数;从未冬眠为 0)。等待方在进入
// 等待前取快照、解除后对比,即可判定"等待期间发生过冬眠"。
func (g *Gate) Generation(taskID string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.m[taskID]; s != nil {
		return s.gen
	}
	return 0
}

// Pre 冬眠前状态快照(started|settling;非冬眠返回空)——唤醒方据此
// 恢复生命周期状态(记账在开闸之前,先读后记)。
func (g *Gate) Pre(taskID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.m[taskID]; s != nil && s.pre != "" {
		return s.pre
	}
	return ""
}

// Wait 挂起至任务非冬眠(false = ctx 先结束)。冬眠等待即计时暂停点:
// 调用方(虫群单位/主宰循环/牧者工作器)阻塞在此期间不消耗预算。
// 复查循环保证"Exit 与下一次 Enter 竞速"时等待者不会凭旧通道漏跑。
func (g *Gate) Wait(ctx context.Context, taskID string) bool {
	return g.WaitTick(ctx, taskID, 0)
}

// WaitTick 带重查节拍的挂起等待:tick > 0 时每 tick 醒一次回到调用方
// 复查(P4-33 冬眠收场——单位与循环的挂起等待用它复查断点令,防"检查
// 完冬眠门刚要挂起、武装令恰好落下"的夹缝等待者错过收场信号无限滞留);
// tick = 0 退化为一等到底(牧者工作器等真唤醒,无需节拍)。返回语义
// 同 Wait:false = ctx 先结束。
func (g *Gate) WaitTick(ctx context.Context, taskID string, tick time.Duration) bool {
	for {
		if !g.Active(taskID) {
			return ctx.Err() == nil
		}
		g.mu.Lock()
		var ch chan struct{}
		if s := g.m[taskID]; s != nil {
			ch = s.ch
		}
		g.mu.Unlock()
		if ch == nil {
			return ctx.Err() == nil
		}
		if tick > 0 {
			t := time.NewTimer(tick)
			select {
			case <-ch: // 唤醒关闭 → 回顶复查(可能立即又冬眠)
			case <-t.C:
				t.Stop() // 节拍到:回顶复查(断点令等收场信号)
			case <-ctx.Done():
				t.Stop()
				return false
			}
			continue
		}
		select {
		case <-ch: // 唤醒关闭 → 回顶复查(可能立即又冬眠)
		case <-ctx.Done():
			return false
		}
	}
}
