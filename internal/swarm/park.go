package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ghostreaper/internal/rings"
)

// ---- 单位级暂停与任务冬眠的执行层落地(P2-08,第四章 §7 / 第七章;
// 任务冬眠 P4-33 起改冻结收场语义) ----
//
// 暂停语义(单位级):当前工具调用完成后挂起——单位不退出、goroutine
// 与 Norma 会话原地冻结(transcript 不中断,续跑经转录 Resume 原会话
// 继续)、计时停走(挂起等待计入 pause_wait,不进 billable);继续 =
// 原地接着干,继续前按 R14 复检图版本(挂起期间 RoE/前置事实变过则
// 拒绝旧计划,步骤回可领取交主宰重判)。
//
// 生效点:工具调用边界(与舵杆注入同点,guard.go)——暂停请求到达时
// 正在执行的工具调用照常完成,边界处收拢当前执行段(被暂缓的调用未
// 执行),单位挂在暂停闩(单位级)上等继续。段间路径(审批续跑/模型
// 重试回到段循环顶)同样检查,无死角。
// 任务冬眠(P4-33):不再原地驻留——收场令与门同发,门等待仅剩收场
// 过渡(节拍复查见令即落地);暂停闩由收场放行统一清闩(闩是进程内
// 控制,过不了收场)。

// hibernating 任务当前是否冬眠中(nil 门 = 否)。
func (e *Executor) hibernating(taskID string) bool {
	return e.Hiber != nil && e.Hiber.Active(taskID)
}

// hiberRecheckTick 冬眠门等待的重查节拍(P4-33):防"检查完断点令刚要
// 进冬眠门、武装令恰好落下"的夹缝等待者错过收场信号无限滞留——每秒
// 回到检查顶,见武装令即落地收场。
const hiberRecheckTick = time.Second

// unitShouldPark 单位此刻是否应挂起:单位暂停闩在、任务冬眠中,或断点
// 已武装(P3-07:边界即收——被暂缓的调用不执行,收段后段循环顶落地;
// 复测单位豁免,侧会话跑完自然落地)。P4-33:冬眠冻结同样豁免复测
// 单位(收场不等侧会话挂起;其审批票经 RetireTickets 放行)。
func (e *Executor) unitShouldPark(env *unitEnv) bool {
	if e.unitLatch(env.TaskID, env.StepID) != nil {
		return true
	}
	if env.retest == nil && e.hibernating(env.TaskID) {
		return true
	}
	return env.retest == nil && e.ckptArmed(env.TaskID)
}

// unitLatch 取单位暂停闩(nil = 未暂停)。
func (e *Executor) unitLatch(taskID, stepID string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unitLatches[steerKey(taskID, stepID)]
}

// PauseUnit 单位级暂停(P2-08 三级控制·暂停):校验步骤在跑且单位在飞
// → 落单位暂停闩 + 控制记账。暂停在当前工具调用完成后生效(guard 边界
// 收段挂起);单位在生效前自然落地时闩随之失效(pause_lapse 如实留痕)。
func (e *Executor) PauseUnit(ctx context.Context, taskID, stepID, by, reason string) error {
	step, err := e.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return fmt.Errorf("swarm: 暂停指向步骤 %s: %w", stepID, err)
	}
	if step.Type != rings.NodeStep {
		return fmt.Errorf("swarm: 节点 %s 是 %s 不是 step", stepID, step.Type)
	}
	if step.Status != "running" {
		return fmt.Errorf("swarm: 步骤 %s 当前状态 %s,不在执行中——暂停只指向在跑的虫群单位", stepID, step.Status)
	}
	e.mu.Lock()
	key := steerKey(taskID, stepID)
	if _, dup := e.unitLatches[key]; dup {
		e.mu.Unlock()
		return nil // 幂等:已在暂停中
	}
	e.unitLatches[key] = make(chan struct{})
	unitID := e.stepUnits[key]
	e.mu.Unlock()
	if unitID == "" {
		// 竞态:步骤 running 但单位刚落地(领取竞态)。清闩如实报错。
		e.mu.Lock()
		delete(e.unitLatches, key)
		e.mu.Unlock()
		return fmt.Errorf("swarm: 步骤 %s 无在跑单位(领取竞态:单位刚干完)", stepID)
	}
	return e.recordUnitControl(taskID, rings.UnitControlRecord{
		Target: "unit", StepID: stepID, UnitID: unitID,
		Action: rings.UnitCtlPause, By: by, Reason: reason,
	})
}

// ResumeUnit 单位级继续(P2-08 三级控制·继续):开闩放行,单位原地
// 接着干(继续前按 R14 复检由挂起等待路径执行)。
func (e *Executor) ResumeUnit(ctx context.Context, taskID, stepID, by string) error {
	e.mu.Lock()
	key := steerKey(taskID, stepID)
	ch, ok := e.unitLatches[key]
	if ok {
		delete(e.unitLatches, key)
		unitID := e.stepUnits[key]
		e.mu.Unlock()
		close(ch)
		if unitID == "" {
			return nil // 单位已落地(pause_lapse 已留痕),继续指令无事可做
		}
		return e.recordUnitControl(taskID, rings.UnitControlRecord{
			Target: "unit", StepID: stepID, UnitID: unitID,
			Action: rings.UnitCtlResume, By: by,
		})
	}
	e.mu.Unlock()
	return fmt.Errorf("swarm: 步骤 %s 不在暂停中", stepID)
}

// UnitPaused 步骤当前是否挂着单位暂停闩(面板状态投影)。
func (e *Executor) UnitPaused(taskID, stepID string) bool {
	return e.unitLatch(taskID, stepID) != nil
}

// dropUnitLatch 单位落地时清闩:暂停在生效前落地(或落地时仍挂着)即
// 失效,pause_lapse 如实留痕。须在 finishUnit 的 defer 清理段调用。
func (e *Executor) dropUnitLatch(taskID, stepID string) {
	e.mu.Lock()
	key := steerKey(taskID, stepID)
	ch, ok := e.unitLatches[key]
	unitID := e.stepUnits[key]
	if ok {
		delete(e.unitLatches, key)
	}
	e.mu.Unlock()
	if ok {
		close(ch)
		if unitID != "" {
			_ = e.recordUnitControl(taskID, rings.UnitControlRecord{
				Target: "unit", StepID: stepID, UnitID: unitID,
				Action: rings.UnitCtlPauseLapse, By: "swarm:" + unitID,
				Note: "单位在暂停生效前/期间自然干完,暂停失效",
			})
		}
	}
}

// clearUnitLatch 静默清闩(不落 pause_lapse):挂起路径自身终结时用
// (如 R14 拒绝旧计划收场)——暂停已履行完职责,不是失效。
func (e *Executor) clearUnitLatch(taskID, stepID string) {
	e.mu.Lock()
	key := steerKey(taskID, stepID)
	ch, ok := e.unitLatches[key]
	if ok {
		delete(e.unitLatches, key)
	}
	e.mu.Unlock()
	if ok {
		close(ch)
	}
}

// recordUnitControl 控制操作记账(谁、何时、对谁、做了什么;R7)。
func (e *Executor) recordUnitControl(taskID string, rec rings.UnitControlRecord) error {
	_, err := e.Store.Append(ledgerCtx, taskID, rings.Event{
		Kind: rings.EventUnitControl, Actor: "creator", Payload: rec,
	})
	if err != nil {
		return fmt.Errorf("swarm: 单位控制记账: %w", err)
	}
	return nil
}

// waitUnpark 挂起等待:单位暂停闩与冬眠门任一在即挂起,全开才放行
// (false = ctx 先结束,随任务熔断/止损中止)。单位暂停期间任务又冬眠
// 的,唤醒后仍停在闩上——细粒度控制独立于任务级。
// P3-07:断点武装即放行(true)——挂起中的单位回段循环顶落地收场
// (进程级收场不等闩开:闩是进程内控制,过不了夜;工作经 Checkpointed
// 会话保留)。
// P4-33:冬眠门等待带重查节拍(WaitTick)——收场武装与关门的夹缝
// 等待者每秒回顶复查,见武装令即落地;复测单位不等冬眠门(侧会话
// 跑完自然落地)。
func (e *Executor) waitUnpark(ctx context.Context, env *unitEnv) bool {
	for {
		if env.retest == nil && e.ckptArmed(env.TaskID) {
			return true
		}
		if env.retest == nil && e.hibernating(env.TaskID) {
			if e.Hiber == nil || !e.Hiber.WaitTick(ctx, env.TaskID, hiberRecheckTick) {
				return false
			}
			continue // 唤醒/节拍到:回顶复查(断点令已武装即落地收场)
		}
		latch := e.unitLatch(env.TaskID, env.StepID)
		if latch == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-latch: // 继续指令开闩 → 重查冬眠(暂停期间任务可能已冬眠)
		}
	}
}

// FlushSuspended 冬眠收场放行(P4-33,装配层经 manager.HibernateSink
// 调用):任务下全部挂起等待者放行——暂停闩上的单位清闩(等待者回
// 检查顶见断点令即落地;闩是进程内控制,过不了收场,与重启后闩自然
// 蒸发等价)、审批票上的单位经 RetireTickets 送达放行令(票面保持
// 待裁决冻结)。
func (e *Executor) FlushSuspended(taskID string) {
	if rel, ok := e.Gate.(interface{ RetireTickets(string) int }); ok {
		rel.RetireTickets(taskID)
	}
	e.mu.Lock()
	var keys []string
	for k := range e.unitLatches {
		if id, _, ok := splitSteerKey(k); ok && id == taskID {
			keys = append(keys, k)
		}
	}
	e.mu.Unlock()
	for _, k := range keys {
		if id, stepID, ok := splitSteerKey(k); ok {
			e.clearUnitLatch(id, stepID) // 静默清闩:收场解除,不是暂停失效
		}
	}
}

// DropPendingApprovals 唤醒清出(P4-33,装配层经 manager.HibernateSink
// 调用):收场让位后遗留的待裁决票清出待决表(零裁决记录,重启等价)。
func (e *Executor) DropPendingApprovals(taskID string) {
	if drop, ok := e.Gate.(interface{ DropTickets(string) int }); ok {
		drop.DropTickets(taskID)
	}
}

// ExpirePendingApprovals 熔断结案(P4-33,装配层经 manager.HibernateSink
// 调用):遗留待裁决票按拒绝结案留痕。
func (e *Executor) ExpirePendingApprovals(taskID, reason string) {
	if exp, ok := e.Gate.(interface{ ExpireTickets(string, string) int }); ok {
		exp.ExpireTickets(taskID, reason)
	}
}

// premiseStale 挂起期间的 R14 前提复检(P2-08:冬眠期间 RoE/前置事实
// 变过则拒绝旧计划;单位暂停期间友邻单位照常推进,故只捕捉真实前提
// 变化,不捕捉记账噪音):扫 afterRev 之后的事件——RoE 更新、既有
// Fact/Asset/Goal 节点的状态迁移(CreatedRev > afterRev 的是挂起期间
// 新生的节点,不算"前置变过")、本步骤被他方迁移,三者任一即过期。
// 返回过期原因(空串 = 前提未变,可原地续跑)。
func (e *Executor) premiseStale(ctx context.Context, taskID, stepID string, afterRev int64) string {
	evs, err := e.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return "" // 读失败 fail-open:恢复不了前提就按未变续跑(与领取复检的读失败语义一致由上层暴露)
	}
	for _, ev := range evs {
		if ev.Revision <= afterRev {
			continue
		}
		switch ev.Kind {
		case rings.EventRoeUpdated:
			return "RoE 授权边界已更新"
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.NodeID == stepID {
				return fmt.Sprintf("本步骤状态已被他方迁移为 %s", p.Status)
			}
			n, nerr := e.Store.Node(ctx, taskID, p.NodeID)
			if nerr != nil {
				continue
			}
			switch n.Type {
			case rings.NodeFact, rings.NodeAsset, rings.NodeGoal:
				if n.CreatedRev <= afterRev { // 既有前提节点变过(新节点是进展,不是前提推翻)
					return fmt.Sprintf("前提节点 %s(%s)状态迁移为 %s", p.NodeID, n.Type, p.Status)
				}
			}
		}
	}
	return ""
}

// parkAnchorRev 挂起进入时的 R14 锚点:此刻任务正在冬眠(段间路径的
// 迟到检测)→ 取入眠事件后的图版本,入眠与检测之间的窗口变化全在
// 复检面内;否则取当前版本(此后的一切变化——含挂起后才开始的冬眠
// 期间变化——都在扫描面内)。
func (e *Executor) parkAnchorRev(ctx context.Context, taskID string) int64 {
	if e.hibernating(taskID) {
		if rev, err := e.Store.LastHibernateRev(ctx, taskID); err == nil && rev > 0 {
			return rev
		}
	}
	rev, err := e.Store.CurrentRevision(ctx, taskID)
	if err != nil {
		return 0
	}
	return rev
}

// unitResumePrompt 挂起后的续跑指令(进会话上下文,transcript 可查):
// 明确的恢复声明 + 前提复检结论,不是凭空继续。
func unitResumePrompt(paused bool) string {
	word := "任务唤醒"
	if paused {
		word = "单位继续"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【%s · 续跑】", word)
	b.WriteString("挂起期间图上前提(RoE/前置事实)已复检未变。继续完成本步骤意图;已执行过的动作与已写回的结论勿重复。挂起期间的运行预算未消耗。")
	return b.String()
}

// ---- 舵杆回执标记(P2-08 回执状态机:已采纳/被拒) ----
//
// 纠偏指令注入时要求单位在下一条回复中输出回执标记行;drive 消费助手
// 文本流时扫描标记并落 steer.record(adopted|rejected)。单位未表态落地
// 的,状态停在已送达(不伪造回执)。

// steerReceiptRe 回执标记:【STEER:st-xxxx:采纳|拒绝:理由】。
var steerReceiptRe = regexp.MustCompile(`【STEER:(st-[A-Za-z0-9]+):(采纳|拒绝)(?:[:：]([^】]*))?】`)

// steerReceipt 一次扫描命中。
type steerReceipt struct {
	SteerID string
	Adopted bool
	Reason  string
}

// scanSteerReceipts 从文本中提取回执标记(去重:同 ID 只取首次出现)。
func scanSteerReceipts(text string, seen map[string]bool) []steerReceipt {
	var out []steerReceipt
	for _, m := range steerReceiptRe.FindAllStringSubmatch(text, -1) {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, steerReceipt{
			SteerID: id, Adopted: m[2] == "采纳", Reason: strings.TrimSpace(m[3]),
		})
	}
	return out
}

// receiptVerdict 回执的账本状态词。
func receiptVerdict(adopted bool) string {
	if adopted {
		return rings.SteerStateAdopted
	}
	return rings.SteerStateRejected
}

// markSteerStaleExpire 舵杆过期收尾的共用件:步骤落地/止损后,同步骤
// 已排队未送达的指令全部落 stale 回执(生效点没了,如实闭环)。
func (h *Helm) expirePending(taskID, stepID, reason string) {
	dirs := h.take(taskID, stepID)
	for _, d := range dirs {
		d.State = rings.SteerStateStale
		d.Note = reason
		_, _ = h.Store.Append(steerCtx, taskID, rings.Event{
			Kind: rings.EventSteerRecord, Actor: "swarm", Payload: d,
		})
	}
}
