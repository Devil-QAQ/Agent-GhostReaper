package overmind

// 任务级总闸:有序收尾时序(P2-04,第四章 §6)。设计定稿:
//
//	每个任务可设整体超时(绝对墙钟),到点依序收场:
//	进入 settling → 虫群单位停止领取新意图 → 在跑单位优雅收尾
//	(宽限 90 秒,超时硬停)→ 主宰终局判定 → 定终态。
//
// 机制分工(与单位级预算 R16 衔接):
//   - 收尾线锚点:created 事件发生时刻 + 超时秒数(rings.TaskGate,
//     事件流不可变;暂停时间照算——绝对墙钟)。
//   - 停止派发:cycle 每轮迭代顶部查收尾线,过线即停止拆解/派发/调度
//     (dispatched 步骤原样留在图上,如实呈现"已派发未执行")。
//   - 在跑单位优雅收尾:单位墙钟预算 = min(运行预算, 距收尾线剩余)
//     (swarm 侧推导)——到点单位就地进 Settlement 把发现写回图,
//     终因 timeout、步骤落 exhausted(暂停接力语义,不硬杀)。
//   - 宽限硬停:进入收尾即布防宽限定时器(SettleGraceSeconds,默认
//     90 秒);收尾轮本身跑飞的单位被逐个 cancel(swarm.CancelRunningUnits,
//     真中断 aborted——硬停丢的只是没写完的总结,进度早在账本里)。
//   - 终局判定:收割(事实转正/声明裁决/假设结论)后 Goal 全量过
//     仲裁者机器判定(R16:不得因超时静默标记失败)。
//   - 定终态:全达成 → completed;未全达成 → stopped(整体超时有序
//     收尾,终态可审计)。经 SettleCtl(Manager)记账。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
)

// loadDeadline 装载任务收尾线(created 事件锚点)。无超时保持零值。
// P2-08:基础线(deadlineBase)与现行线(deadline)分开存——唤醒路径按
// 基础线 + 累计冬眠时长顺延现行线(冬眠期间任务总闸墙钟停走);装载时
// 即应用账本里的历史抵扣(重启后唤醒/收养拉起的新循环不丢冬眠时长,
// 否则旧墙钟立即过线误收尾)。
func (l *Loop) loadDeadline(ctx context.Context) {
	secs, createdAt, err := l.Store.TaskGate(ctx, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	if secs <= 0 || createdAt.IsZero() {
		return
	}
	base := createdAt.Add(time.Duration(secs) * time.Second)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deadlineBase = base
	l.deadline = base
	if l.Hiber != nil {
		if credit, cerr := l.Store.HibernateCredit(ctx, l.TaskID); cerr == nil && credit > 0 {
			l.deadline = base.Add(credit)
		}
	}
}

// graceSeconds 收尾宽限(秒,归一)。
func (l *Loop) graceSeconds() int {
	if l.SettleGraceSeconds > 0 {
		return l.SettleGraceSeconds
	}
	return DefaultSettleGraceSeconds
}

// enterSettling 进入收尾(幂等,并发安全——收尾线定时器与循环顶部
// 两路触发):黑匣子留痕 + 操控面翻转任务状态(收尾中到点即见,不等
// cycle 归来)+ 唤醒循环进入 settle 流程。宽限硬停在 Run 启动时已独立
// 布防(armHardStop)——收尾轮卡死也能被打断。收尾中熔断由 StopBy
// 兜住(硬停优先,ctx 结束后 settle 流程不再定终态)。
// P2-08:冬眠中不推进(任务总闸墙钟冻结)——唤醒后按顺延的收尾线
// 复判,仍过线才收场。
func (l *Loop) enterSettling(ctx context.Context) {
	if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
		return
	}
	l.mu.Lock()
	if l.settling {
		l.mu.Unlock()
		return
	}
	l.settling = true
	l.mu.Unlock()
	detail := map[string]any{
		"reason":        "任务整体超时,进入有序收尾(停止派发,在跑单位优雅收尾)",
		"deadline":      l.deadline.Format(time.RFC3339),
		"grace_seconds": l.graceSeconds(),
	}
	if err := blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
		rings.BlackboxRecord{Type: rings.BlackboxTaskSettling, Detail: detail}); err != nil {
		l.setErr(err)
	}
	if l.SettleCtl != nil {
		if err := l.SettleCtl.MarkSettling(l.TaskID, detail); err != nil {
			// 已熔断/已让位:收尾流程照走(账本如实),终态由他方已定。
			l.setErr(err)
		}
	}
	// 自唤醒:把可能在 select 里等待的循环带回 settle 流程(容量 1 合并)。
	if l.settleKick != nil {
		select {
		case l.settleKick <- struct{}{}:
		default:
		}
	}
}

// relayBriefs 预算耗尽(exhausted)步骤的执行轨迹简报(P2-04 接力依据,
// 第四章 §6:主宰下轮读取其执行轨迹,决定派"接着上次进度继续"的意图
// 还是换方向)。逐条拼:意图 · 单位终因/轮数 · 工具实调计数 · 血缘产出。
// 轨迹是确定性拼装(零 LLM):session_finished 的 Note、tool_call 实调
// 计数与 produces 边全部来自账本。
func (l *Loop) relayBriefs(ctx context.Context, steps []rings.NodeFull) []string {
	var exhausted []rings.NodeFull
	for _, s := range steps {
		if s.Status == "exhausted" {
			exhausted = append(exhausted, s)
		}
	}
	if len(exhausted) == 0 {
		return nil
	}
	// session_finished 逐步骤最新一笔(终因/轮数)+ tool_call 逐步骤计数。
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 接力轨迹查事件: %w", err))
		return nil
	}
	finishNote := map[string]string{}
	toolCalls := map[string]map[string]int{} // stepID -> tool -> count
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		switch rec.Type {
		case rings.SwarmRecSessionEnd:
			finishNote[rec.StepID] = rec.Note
		case rings.SwarmRecToolCall:
			if toolCalls[rec.StepID] == nil {
				toolCalls[rec.StepID] = map[string]int{}
			}
			if rec.ToolName != "" {
				toolCalls[rec.StepID][rec.ToolName]++
			}
		}
	}
	// produces 血缘产出(Step → Fact|Hypothesis)。
	produces, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeProduces)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 接力轨迹查血缘: %w", err))
		return nil
	}
	producedBy := map[string][]string{}
	for _, e := range produces {
		producedBy[e.From] = append(producedBy[e.From], e.To)
	}

	var out []string
	for _, s := range exhausted {
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s", s.ID, s.Title)
		if note := finishNote[s.ID]; note != "" {
			fmt.Fprintf(&b, " | %s", note)
		}
		if tools := toolCalls[s.ID]; len(tools) > 0 {
			var names []string
			for name := range tools {
				names = append(names, name)
			}
			sort.Strings(names)
			var parts []string
			for _, name := range names {
				parts = append(parts, fmt.Sprintf("%s×%d", name, tools[name]))
			}
			fmt.Fprintf(&b, " | 工具: %s", strings.Join(parts, " "))
		}
		if ids := producedBy[s.ID]; len(ids) > 0 {
			fmt.Fprintf(&b, " | 已产出: %s", strings.Join(ids, ","))
		}
		out = append(out, b.String())
	}
	return out
}

// armHardStop 布防收尾宽限硬停(P2-08 冬眠感知版):收尾线 + 宽限到点
// 对仍在飞的单位逐个 cancel(swarm.CancelRunningUnits,真中断 aborted
// ——硬停丢的只是没写完的总结,进度早已实时入账)。冬眠中到点不推进
// (收尾预算冻结),唤醒后按顺延的收尾线重算。无单位在飞则无事发生
// (零留痕,不打扰)。
func (l *Loop) armHardStop(ctx context.Context) {
	grace := time.Duration(l.graceSeconds()) * time.Second
	go func() {
		for {
			d := time.Until(l.currentDeadline().Add(grace))
			if d < grace {
				d = grace // 已过线恢复的任务至少保一个宽限窗
			}
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			case <-l.runDone:
				t.Stop()
				return // 循环已收场(完成/熔断),硬停失义
			}
			// 到点:冬眠中不推进(收尾预算冻结),唤醒后重算。
			if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
				if !l.Hiber.Wait(ctx, l.TaskID) {
					return
				}
				l.extendDeadline()
				continue
			}
			ids := l.Swarm.CancelRunningUnits()
			if len(ids) == 0 {
				return
			}
			_ = blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
				rings.BlackboxRecord{
					Type: rings.BlackboxGraceHardStop,
					Detail: map[string]any{
						"units": ids, "grace_seconds": l.graceSeconds(),
						"reason": "收尾宽限超时,对仍挂着的单位硬停(进度早已实时入账)",
					},
				})
			return
		}
	}()
}

// settle 收尾收场:等在飞单位落地(各自优雅收尾/宽限硬停兜底)→
// 终局判定(收割 + Goal 全量过仲裁者)→ 定终态。ctx 结束(收尾中被
// 熔断)时不再定终态——stopped 已由操控方记账,如实让位。
// P2-08:收尾中冬眠——等待循环在门上挂起(在飞单位同步冻结,收尾
// 预算停走),唤醒后继续等齐。
// P4-04:单位生命周期独立于循环迭代(调度不等批),在飞等齐以本
// 轮询为准(收尾轮卡死由宽限硬停打断,打断后单位随即落地)。
func (l *Loop) settle(ctx context.Context) {
	// ① 等待在飞单位落地。
	for ctx.Err() == nil {
		if !l.parkGate(ctx) {
			return // 冬眠唤醒后继续等;ctx 结束如实让位
		}
		running, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "running")
		if err != nil {
			l.setErr(err)
			break
		}
		if len(running) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ctx.Err() != nil {
		return // 收尾中被熔断:终态已由 StopBy 记账
	}

	// ② 终局判定:收割(候选事实转正 + 声明裁决 + 假设结论迁移)后
	// Goal 全量过仲裁者(R16:超时不是失败,达成与否由机器判定)。
	if _, err := Harvest(ctx, l.Store, l.TaskID); err != nil {
		l.setErr(err)
	}
	if _, err := HarvestClaims(ctx, l.Store, l.TaskID); err != nil {
		l.setErr(err)
	}
	if _, err := l.harvestHypotheses(ctx); err != nil {
		l.setErr(err)
	}
	if _, err := l.judgeClaims(ctx); err != nil {
		l.setErr(err)
	}
	achieved, err := l.judgeGoals(ctx)
	if err != nil {
		l.setErr(err)
	}

	// ③ 定终态:全达成 completed;否则 stopped(整体超时有序收尾)。
	terminal := "stopped"
	reason := "任务整体超时,有序收尾(目标未全部达成,进度已入账可恢复查看)"
	if achieved {
		terminal = "completed"
		reason = "任务整体超时,收尾终局判定:目标已全部达成"
	}
	detail := map[string]any{"reason": reason, "achieved": achieved, "settled_by": "task_gate"}
	_ = blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
		rings.BlackboxRecord{
			Type: rings.BlackboxTaskSettled,
			Detail: map[string]any{
				"achieved": achieved, "terminal": terminal, "reason": reason,
			},
		})
	if l.SettleCtl != nil {
		if err := l.SettleCtl.FinishSettling(l.TaskID, achieved, detail); err != nil {
			l.setErr(err)
		}
	}
}
