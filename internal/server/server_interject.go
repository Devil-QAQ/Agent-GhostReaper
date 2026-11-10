// Package server 的人机通道面板 API(P2-08,第四章 §7 v1.8:插话 +
// 三级控制):单位列表(虫群组的控制/插话入口)、插话子会话读写、
// 单位控制(暂停/继续;停止走既有 kill-work)、主宰循环控制、任务
// 冬眠/唤醒(熔断走既有 stop)。全部操控走账本(R7),与执行体共持
// 同一实例(舵杆/冬眠门/审批门),无旁路装配。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/interject"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// ---- 单位列表(虫群组数据面) ----

// finishView 收场信息(P4-06 ② 人话状态行的数据源):轮次与终因。
// 时间账 note 原文不进界面——翻译成人话摘要是唯一呈现,原始 note 留在
// 账本(审计走账本 / 诊断包既有通道,界面零入口)。
type finishView struct {
	Turns  int    `json:"turns"`
	Reason string `json:"reason,omitempty"` // 机器终因,面板词表翻译;拒绝/受阻终因本就是人话
}

// UnitRow 一只虫群单位的列表行(以会话为锚:transcript_id 跨进程稳定,
// 单位序号 uN 每进程重排——P2-06f 结论)。Info 为该单位会话的信息条
// 数据(token 独立表按会话锚聚合,P2-08b 单位会话页用)。
type UnitRow struct {
	UnitID      string          `json:"unit_id"`
	StepID      string          `json:"step_id"`
	StepTitle   string          `json:"step_title"`
	SessionID   string          `json:"session_id"`
	Status      string          `json:"status"` // running|done|exhausted|failed|aborted|approval_denied|blocked
	Running     bool            `json:"running"`
	Paused      bool            `json:"paused"`
	Finish      *finishView     `json:"finish,omitempty"` // P4-06 ② 收场信息(note 原文不出账本)
	StartedAt   string          `json:"started_at"`
	FinishedAt  string          `json:"finished_at,omitempty"`
	InterjectID string          `json:"interject_id"` // 插话子会话锚(sessionKey,面板深链/去重)
	Info        sessionInfoBar  `json:"info"`
	Meta        sessionMetaView `json:"meta"` // 会话标记(重命名/备注/置顶,P3-13b ②)
	// P4-02 ① 排队中条目:派发未领取的 dispatched 步骤(Queued=true,
	// 无会话锚——Status 为步骤真实状态,点击看步骤详情)。
	Queued bool `json:"queued,omitempty"`
	// P4-02 ② 拒绝来源:approval_denied 终态的单位带上终结它的拒绝线
	// 类别与原因(硬规则的规则名从原因里解析,面板区分呈现)。
	DenyLine   string `json:"deny_line,omitempty"`
	DenyReason string `json:"deny_reason,omitempty"`
	// P4-02 ④ 有效运行时长(billable,秒):该会话全部 session_finished
	// 账值的合计(原会话续跑链) + 在飞段的活动时钟读数。
	BillableSec float64 `json:"billable_seconds"`
}

// handleUnitsList 虫群单位列表(会话 Started 升序;面板自上而下取最新
// 即倒序读)。paused/running 是进程内活状态(重启后按账本如实呈现)。
// P4-02:末尾追加"排队中"条目(dispatched 步骤,派发即可见);拒绝线
// 与 billable 随行携带。
// P4-36 起读 unit_sessions 投影(session_started/finished 由写侧同事务
// 维护,note 的终态/轮次/终因/计费在写入时机器解析入列)——不再扫描
// 事件流,巨账本任务的虫群列表秒开。
func (s *Server) handleUnitsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	steps := map[string]rings.NodeFull{}
	if list, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeStep, ""); err == nil {
		for _, st := range list {
			steps[st.ID] = st
		}
	}
	// 步骤意图标题校对稿优先表(P4-65 ⑩:显示层优先,任务包与机器消费
	// 用节点原文;空表零变化)。
	stepTitlePolish := s.polishedNodeTitleOf(r.Context(), taskID)
	sessions, err := s.Store.UnitSessions(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 拒绝来源(P4-02 ②):步骤 → 最近一次 denied 记录(approval_records
	// 投影,rev 升序留最后即最新)。
	lastDeny, err := s.Store.StepDenyLatest(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]UnitRow, 0, len(sessions))
	for _, sess := range sessions {
		row := UnitRow{
			UnitID: sess.UnitID, StepID: sess.StepID, SessionID: sess.Transcript,
			Status: "running", Running: true, StartedAt: sess.StartedAt,
			InterjectID: "u:" + sess.Transcript,
			BillableSec: sess.BillableSec,
		}
		if st, ok := steps[sess.StepID]; ok {
			row.StepTitle = st.Title
			if t := stepTitlePolish[sess.StepID]; t != "" {
				row.StepTitle = t
			}
		}
		if sess.EndStatus != "" {
			row.Status, row.Running, row.FinishedAt = sess.EndStatus, false, sess.FinishedAt
			row.Finish = &finishView{Turns: sess.Turns, Reason: sess.Reason}
			// P4-51 ①:范围等待批准的会话,显示态随步骤现值走——步骤还
			// 在 scope_wait = 等待批准;已被裁决唤醒回 dispatched = 排队
			// 中(重领后 session_started 清 end_status 回执行中);被禁
			// 止测试收口 = 中止。
			if sess.EndStatus == rings.StepScopeWait {
				if st, ok := steps[sess.StepID]; ok {
					switch st.Status {
					case rings.StepScopeWait:
						row.Status = rings.StepScopeWait
					case "dispatched":
						row.Status, row.Finish = "dispatched", nil // 已批准,排队中
					default:
						row.Status = st.Status // aborted(禁止测试/任务收场)等终态
					}
				}
			}
		} else if st, ok := steps[sess.StepID]; ok && st.Status != "running" {
			row.Status, row.Running = st.Status, false
		}
		out = append(out, row)
	}
	// 拒绝来源随行(approval_denied 终态才带;规则名从硬规则原因解析)。
	for i := range out {
		if out[i].Status != "approval_denied" {
			continue
		}
		if ar, ok := lastDeny[out[i].StepID]; ok {
			out[i].DenyLine = ar.DenyLine
			out[i].DenyReason = ar.Reason
		}
	}
	// P4-02 ① 排队中条目:当前 dispatched 的步骤(派发未领取 / 受阻
	// 重开 / 断点存档回可领取),追加在会话行之后(面板排在列表尾段)。
	// P4-08 ①:排序与调度器同源(overmind.StepLaunchLess)——面板排队
	// 次序即领取次序,high 探索与反证并存时反证在前可直接在面板核对。
	var queued []rings.NodeFull
	for _, st := range steps {
		if st.Status == "dispatched" {
			queued = append(queued, st)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return overmind.StepLaunchLess(queued[i], queued[j]) })
	for _, st := range queued {
		title := st.Title
		if t := stepTitlePolish[st.ID]; t != "" {
			title = t
		}
		out = append(out, UnitRow{
			StepID: st.ID, StepTitle: title,
			Status: "dispatched", Queued: true,
		})
	}
	// 进程内活状态:暂停闩(会话已结束的行不标——闩随落地清理)。
	if s.swarmExec() != nil {
		for i := range out {
			if out[i].Running && s.swarmExec().UnitPaused(taskID, out[i].StepID) {
				out[i].Paused = true
			}
		}
		// P4-02 ④ 在飞段的活动时钟读数(等待中冻结在已收拢值)。
		for i := range out {
			if !out[i].Running {
				continue
			}
			if d, ok := s.swarmExec().ClockBillable(taskID, out[i].SessionID); ok {
				out[i].BillableSec += d.Seconds()
			}
		}
	}
	// 每单位信息条数据(token 独立表按会话锚聚合;重启接力的同名单位
	// token 行同属一个 transcript_id,自然合并——P2-06f 结论的消费面)。
	if rows, err := s.Store.TokenUsageRows(r.Context(), taskID); err == nil {
		type lastUse struct {
			model string
			input int
		}
		bySession := map[string]*sessionInfoBar{}
		last := map[string]*lastUse{}
		for i := range out {
			bySession[out[i].SessionID] = &out[i].Info
		}
		for _, x := range rows {
			info, ok := bySession[x.TranscriptID]
			if !ok {
				continue
			}
			info.InputTokens += x.InputTokens
			info.OutputTokens += x.OutputTokens
			info.CacheTokens += x.CacheReadTokens + x.CacheWriteTokens
			info.Calls++
			info.Model = x.Model
			lu := last[x.TranscriptID]
			if lu == nil {
				lu = &lastUse{}
				last[x.TranscriptID] = lu
			}
			lu.model, lu.input = x.Model, x.InputTokens
		}
		for i := range out {
			info := &out[i].Info
			if info.Model == "" && s.Chain != nil {
				info.Model = s.Chain.ActiveModel()
			}
			lu := last[out[i].SessionID]
			model := info.Model
			if lu != nil && lu.model != "" {
				model = lu.model
			}
			window := failover.ContextWindowFor(r.Context(), s.Store, model)
			info.ContextWindow = window
			if window > 0 && lu != nil {
				info.ContextRatio = float64(lu.input) / float64(window)
			}
		}
	}
	if out == nil {
		out = []UnitRow{}
	}
	// 会话标记(P3-13b ②):单位会话的自定义名称/备注/置顶(置顶单位
	// 靠前由面板按 Meta 排序,服务端保持 Started 升序原序)。
	if labels, lerr := s.Store.SessionLabels(r.Context(), taskID); lerr == nil {
		for i := range out {
			out[i].Meta = sessionMetaOf(labels, out[i].SessionID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"units":      out,
		"hibernated": s.taskHibernated(taskID),
	})
}

// P4-36:session_finished note 的终态/轮次/终因/计费解析已收归 rings
//(applySwarmSession 写入时机器解析入 unit_sessions 投影,NoteStatus/
// NoteTurns/NoteReason/noteSeconds),server 侧原副本删除(铁律 10)。

// ---- 插话子会话(问/改) ----

// interjectInfoBar 插话子会话的顶部信息条数据(独立计:按 agent=
// interject:<key> 过滤求和,与 token 独立表同源对账)。
type interjectInfoBar struct {
	Model         string  `json:"model,omitempty"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CacheTokens   int     `json:"cache_tokens"`
	ContextWindow int     `json:"context_window,omitempty"`
	ContextRatio  float64 `json:"context_ratio,omitempty"`
	Calls         int     `json:"calls"`
}

// handleInterjectGet 子会话读视图:发言流(问/改)+ 回执状态机 +
// 建议裁决 + 信息条 + 冻结态。
func (s *Server) handleInterjectGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Interject == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("插话服务未装配"))
		return
	}
	target, sessionID := r.URL.Query().Get("target"), r.URL.Query().Get("session")
	if target != interject.TargetOvermind && target != "unit" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("target 须为 overmind|unit"))
		return
	}
	if target == "unit" && sessionID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unit 目标缺 session 参数"))
		return
	}
	view, err := s.Interject.View(r.Context(), taskID, target, sessionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := struct {
		*interject.View
		Info         interjectInfoBar `json:"info"`
		HibernateSec float64          `json:"hibernate_seconds"` // P4-02 ④ 时长冻结口径的冬眠抵扣(含进行中)
	}{View: view, Info: s.interjectInfo(r, taskID, target, sessionID),
		HibernateSec: s.Store.HibernateDeduct(r.Context(), taskID, time.Now()).Seconds()}
	writeJSON(w, http.StatusOK, out)
}

// interjectInfo 子会话信息条(纯读,零额外采集;口径同牧者)。
func (s *Server) interjectInfo(r *http.Request, taskID, target, sessionID string) interjectInfoBar {
	var info interjectInfoBar
	rows, err := s.Store.TokenUsageRows(r.Context(), taskID)
	if err != nil {
		return info
	}
	key := "overmind"
	if target == "unit" {
		key = "u:" + sessionID
	}
	agent := "interject:" + key
	lastModel, lastInput := "", 0
	for _, x := range rows {
		if x.Agent != agent {
			continue
		}
		info.InputTokens += x.InputTokens
		info.OutputTokens += x.OutputTokens
		info.CacheTokens += x.CacheReadTokens + x.CacheWriteTokens
		info.Calls++
		lastModel, lastInput = x.Model, x.InputTokens
		info.Model = x.Model
	}
	if info.Model == "" && s.Chain != nil {
		info.Model = s.Chain.ActiveModel()
	}
	window := failover.ContextWindowFor(r.Context(), s.Store, lastModel)
	info.ContextWindow = window
	if window > 0 {
		info.ContextRatio = float64(lastInput) / float64(window)
	}
	return info
}

// handleInterjectPost 插话发言入队(mode=ask|steer)。
func (s *Server) handleInterjectPost(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Interject == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("插话服务未装配"))
		return
	}
	var req struct {
		Target    string `json:"target"`
		UnitID    string `json:"unit_id"`
		StepID    string `json:"step_id"`
		SessionID string `json:"session_id"`
		Mode      string `json:"mode"`
		Text      string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	msgID, refID, err := s.Interject.Enqueue(r.Context(), taskID,
		req.Target, req.UnitID, req.StepID, req.SessionID, req.Mode, req.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out := map[string]string{"message_id": msgID}
	if refID != "" {
		out["ref_id"] = refID
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 单位控制(暂停/继续;停止走既有 kill-work) ----

func (s *Server) handleUnitPause(w http.ResponseWriter, r *http.Request) {
	s.unitControl(w, r, "pause")
}

func (s *Server) handleUnitResume(w http.ResponseWriter, r *http.Request) {
	s.unitControl(w, r, "resume")
}

func (s *Server) unitControl(w http.ResponseWriter, r *http.Request, action string) {
	taskID, stepID := r.PathValue("id"), r.PathValue("step")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,单位控制已冻结(唤醒后操作)"))
		return
	}
	if s.swarmExec() == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("虫群执行器未装配(未配模型)"))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	var err error
	if action == "pause" {
		err = s.swarmExec().PauseUnit(r.Context(), taskID, stepID, "creator", req.Reason)
	} else {
		err = s.swarmExec().ResumeUnit(r.Context(), taskID, stepID, "creator")
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": action + "d"})
}

// ---- 主宰循环控制(暂停/继续;停止不设——任务终止走熔断) ----

// handleOvermindStatus 主宰会话页数据面(P2-08b):循环状态(徽标的
// 数据源:冬眠中循环随任务冻结、终态任务无循环——呈现一律以服务端为
// 准,不用本地推断)+ 每轮研判轨迹(五问/收割/派发决策,账本派生)+
// 会话信息条数据。
func (s *Server) handleOvermindStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	taskStatus := ""
	if t := s.Manager.Task(taskID); t != nil {
		taskStatus = t.Status()
	}
	running, paused := false, false
	if s.Loops != nil {
		if l := s.Loops.Get(taskID); l != nil {
			running, paused = true, l.LoopPaused()
		}
	}
	rounds, err := s.overmindRounds(r, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 会话标记(P3-13b ②):主宰面板会话的组织信息,锚 = 稳定键
	// overmind(与插话目标键同源,跨进程不变)。
	meta := sessionMetaView{}
	if labels, lerr := s.Store.SessionLabels(r.Context(), taskID); lerr == nil {
		meta = sessionMetaOf(labels, interject.TargetOvermind)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": running, "paused": paused,
		"hibernated": s.taskHibernated(taskID), "task_status": taskStatus,
		"hibernate_seconds": s.Store.HibernateDeduct(r.Context(), taskID, time.Now()).Seconds(),
		"rounds":            rounds, "info": s.overmindInfo(r, taskID),
		"meta": meta,
	})
}

func (s *Server) handleOvermindPause(w http.ResponseWriter, r *http.Request) {
	s.overmindControl(w, r, "pause")
}

func (s *Server) handleOvermindResume(w http.ResponseWriter, r *http.Request) {
	s.overmindControl(w, r, "resume")
}

func (s *Server) overmindControl(w http.ResponseWriter, r *http.Request, action string) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,主宰循环已随任务冻结(唤醒后操作)"))
		return
	}
	if s.Loops == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("主宰循环注册表未装配"))
		return
	}
	l := s.Loops.Get(taskID)
	if l == nil {
		writeErr(w, http.StatusConflict, errors.New("任务当前无在跑的主宰循环"))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	var err error
	if action == "pause" {
		err = l.PauseLoop("creator", req.Reason)
	} else {
		err = l.ResumeLoop("creator")
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": action + "d"})
}

// ---- 任务控制(冬眠/唤醒;熔断走既有 stop) ----

// handleTaskHibernate 冬眠:响应携带待裁决审批笔数(入口提示用,
// 第七章「任务冬眠」:仅告知,不强制)。
func (s *Server) handleTaskHibernate(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	detail := map[string]any{"by": "creator"}
	if req.Reason != "" {
		detail["reason"] = req.Reason
	}
	if err := s.Manager.Hibernate(taskID, detail); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "hibernated", "pending_approvals": len(s.Gate.Pending(taskID)),
	})
}

// handleTaskHibernateWake 唤醒(唯一恢复入口;区别于既有 /wake——
// 那是 RoE/目标改动后唤醒主宰循环的事件通知)。P4-25 审核迭代 2:
// 「冬眠中」盖三种底层态,唤醒按钮统一走本端点,按实际状态路由恢复
// 通道——真冬眠(hibernated)走 Wake(冬眠时长抵扣),面板暂停
// (paused)与断点存档(checkpointed)走 Resume(原地拉起);其余
// 状态如实报错。
func (s *Server) handleTaskHibernateWake(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	st := ""
	if t := s.Manager.Task(taskID); t != nil {
		st = t.Status()
	}
	switch st {
	case manager.StatusHibernated:
		if err := s.Manager.Wake(taskID, nil); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	case manager.StatusPaused, manager.StatusCheckpointed:
		if err := s.Manager.Resume(taskID); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("任务 %s 状态 %s 不可唤醒", taskID, st))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "awake"})
}

// taskHibernated 任务当前是否冬眠中(门为运行时权威:记账与关闸的
// 窗口期内以门为准——面板只见真实冻结态)。
func (s *Server) taskHibernated(taskID string) bool {
	if s.HiberGate != nil {
		return s.HiberGate.Active(taskID)
	}
	if t := s.Manager.Task(taskID); t != nil {
		return t.Status() == manager.StatusHibernated
	}
	return false
}

// handleTaskCheckpoint 存档断点(P3-07,POST /api/tasks/{id}/checkpoint):
// 手动断点——在跑单位到边界收拢后返回 frozen(此刻账本一致,进程可关;
// 重启后任务自动原地续跑)。响应如实携带 frozen:超时未收拢时为 false
// (此刻关进程仍安全,按恢复语义续跑)。
func (s *Server) handleTaskCheckpoint(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	detail := map[string]any{"by": "creator"}
	if req.Reason != "" {
		detail["note"] = req.Reason
	}
	frozen, err := s.Manager.Checkpoint(taskID, detail)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": manager.StatusCheckpointed, "frozen": frozen,
	})
}
