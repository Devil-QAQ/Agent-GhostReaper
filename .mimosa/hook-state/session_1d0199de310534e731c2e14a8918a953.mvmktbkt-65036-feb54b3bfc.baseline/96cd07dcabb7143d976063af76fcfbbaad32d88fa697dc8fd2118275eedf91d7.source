// P4-18 收口信号(问题清单 #30)的服务端:
//
//	POST /api/tasks/{id}/closure  收口提醒的裁决(body: {action,
//	                                 reason?};action=continue|end|dismiss)
//	GET  /api/tasks               任务视图携带待决提醒(closure 字段,
//	                                 仅运行中任务;面板据此弹窗)
//
// 计数与待决态从账本确定性推导(overmind.ClosureOf,展示与操作同源):
// 「继续」只落 closure.record(continue)——任务不停,同规则重新计数;
// 「结束」落 closure.record(end) 后经在跑主宰循环走优雅收尾(与任务
// 总闸同一条收尾时序,终态 detail.settled_by=closure_prompt),收尾
// 完成后报告页渲染的即终版报告(确定性渲染,R17 不破)。
// P4-23:「dismiss」= 弹窗被直接关闭、未裁决——待决保留并标 parked,
// 弹窗不再自动弹、任务列表标红点,点红点重开弹窗处理。
package server

import (
	"errors"
	"fmt"
	"net/http"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// closurePromptView 任务视图的待决收口提醒(面板弹窗数据面;数字全部
// 取自提醒落账时的机器快照,陈述事实)。
type closurePromptView struct {
	Rounds           int    `json:"rounds"`            // 连续零新发现研判轮数(触发时)
	Minutes          int    `json:"minutes"`           // 距锚点分钟数(触发时)
	RoundsThreshold  int    `json:"rounds_threshold"`  // 生效轮数阈值
	MinutesThreshold int    `json:"minutes_threshold"` // 生效间隔阈值(分钟)
	AnchorKind       string `json:"anchor_kind"`       // 锚点种类(finding|continue|created,措辞依据)
	PromptedAt       string `json:"prompted_at"`       // 提醒时刻
	Parked           bool   `json:"parked"`            // 弹窗被直接关闭后的搁置态(P4-23:不自动弹,任务列表红点)
}

// closureConfigView 任务视图的收口配置投影(审核迭代 1):生效值(三层:
// 任务级 > Agent 栏全局默认 > 代码默认)+ 任务级显式值(0 = 未单独
// 设定)。工作台修改入口的数据面。
type closureConfigView struct {
	Enabled     bool `json:"enabled"`      // 开关(生效)
	Rounds      int  `json:"rounds"`       // 轮数阈值(生效)
	Minutes     int  `json:"minutes"`      // 间隔阈值(分钟,生效)
	TaskSet     bool `json:"task_set"`     // 有无任务级配置
	TaskRounds  int  `json:"task_rounds"`  // 任务级轮数(0 = 未单独设定)
	TaskMinutes int  `json:"task_minutes"` // 任务级间隔(0 = 未单独设定)
}

// closureViewOf 任务的待决收口提醒视图(无待决返回 nil)。仅运行中
// (started)任务呈现:收尾中/终态/冻结态(冬眠、断点、暂停)不弹——
// 任务冻结时提醒无处裁决,唤醒后仍在则重新可见。
func (s *Server) closureViewOf(r *http.Request, taskID, status string) *closurePromptView {
	if status != "started" {
		return nil
	}
	st, err := overmind.ClosureOf(r.Context(), s.Store, taskID)
	if err != nil || st.Pending == nil {
		return nil
	}
	p := st.Pending
	return &closurePromptView{
		Rounds:           p.Record.Rounds,
		Minutes:          p.Record.Minutes,
		RoundsThreshold:  p.Record.RoundsThreshold,
		MinutesThreshold: p.Record.MinutesThreshold,
		AnchorKind:       p.Record.AnchorKind,
		PromptedAt:       p.PromptedAt.Local().Format("2006-01-02 15:04:05"),
		Parked:           p.Parked,
	}
}

// closureConfigViewOf 任务的收口配置投影(任何状态都呈——终态任务的
// 配置可查,改动入口在面板按运行态呈现)。
func (s *Server) closureConfigViewOf(r *http.Request, taskID string) *closureConfigView {
	st, err := overmind.ClosureOf(r.Context(), s.Store, taskID)
	if err != nil {
		return nil
	}
	enabled, rounds, minutes := overmind.ClosureEffective(r.Context(), s.Store, st.TaskConfig)
	return &closureConfigView{
		Enabled: enabled, Rounds: rounds, Minutes: minutes,
		TaskSet: st.TaskConfig.HasConfig, TaskRounds: st.TaskConfig.Rounds, TaskMinutes: st.TaskConfig.Minutes,
	}
}

// closureBodyIn 收口配置请求体(建任务初始组与运行中变更共用;指针字段
// 缺省 = 该项不改/不设)。
type closureBodyIn struct {
	Enabled *bool `json:"enabled"`
	Rounds  *int  `json:"rounds"`  // 轮数;0/缺省 = 用全局默认
	Minutes *int  `json:"minutes"` // 间隔分钟;0/缺省 = 用全局默认
}

// validate 收口配置取值范围(与 Agent 栏全局默认同上界)。
func (b *closureBodyIn) validate() error {
	if b == nil {
		return nil
	}
	if b.Rounds != nil && (*b.Rounds < 0 || *b.Rounds > agentClosureRoundsMax) {
		return fmt.Errorf("收口信号轮数须为 0–%d 的整数(0 = 用全局默认)", agentClosureRoundsMax)
	}
	if b.Minutes != nil && (*b.Minutes < 0 || *b.Minutes > agentClosureMinutesMax) {
		return fmt.Errorf("收口信号间隔须为 0–%d 的整数(0 = 用全局默认)", agentClosureMinutesMax)
	}
	return nil
}

// detailOf 建任务 created detail.closure 的显式项(全缺省返回 nil =
// 不落键)。
func (b *closureBodyIn) detailOf() map[string]any {
	if b == nil {
		return nil
	}
	out := map[string]any{}
	if b.Enabled != nil {
		out["enabled"] = *b.Enabled
	}
	if b.Rounds != nil && *b.Rounds > 0 {
		out["rounds"] = *b.Rounds
	}
	if b.Minutes != nil && *b.Minutes > 0 {
		out["minutes"] = *b.Minutes
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleTaskClosure POST /api/tasks/{id}/closure:收口提醒的裁决。
// 「继续」= 任务不停、同规则重新计数(锚点移到本次「继续」);「结束」=
// 优雅收尾(在跑单位收尾 → 终局判定 → 定终态,非熔断);「搁置」(P4-23,
// 弹窗被直接关闭)= 待决保留、标 parked,任务列表红点是持久落点。
func (s *Server) handleTaskClosure(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	st, err := overmind.ClosureOf(r.Context(), s.Store, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if st.Pending == nil {
		writeErr(w, http.StatusConflict, errors.New("该任务当前没有待决的收口提醒"))
		return
	}
	switch req.Action {
	case "continue":
		if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
			Kind: rings.EventClosureRecord, Actor: "creator",
			Payload: rings.ClosureRecord{Type: rings.ClosureRecContinue, Reason: req.Reason},
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "continued"})
	case "dismiss":
		// 弹窗被直接关闭(P4-23):待决保留、标搁置;幂等语义——重复
		// dismiss 同样成功(未裁决期间多次关闭弹窗只留搁置事实)。
		if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
			Kind: rings.EventClosureRecord, Actor: "creator",
			Payload: rings.ClosureRecord{Type: rings.ClosureRecDismiss},
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
	case "end":
		// 结束走优雅收尾:需在跑的主宰循环驱动收尾时序(与暂停/继续
		// 控制同一条注册表面),没有循环时如实拒绝,不熔断顶替
		//(熔断是硬停,与「结束=优雅收尾」语义不符)。
		if s.taskHibernated(taskID) {
			writeErr(w, http.StatusConflict, errors.New("任务冬眠中,唤醒后再结束"))
			return
		}
		if s.Loops == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("主宰循环注册表未装配"))
			return
		}
		l := s.Loops.Get(taskID)
		if l == nil {
			writeErr(w, http.StatusConflict, errors.New("任务当前无在跑的主宰循环,无法走收尾"))
			return
		}
		if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
			Kind: rings.EventClosureRecord, Actor: "creator",
			Payload: rings.ClosureRecord{Type: rings.ClosureRecEnd, Reason: req.Reason},
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		l.SettleNow(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"status": "settling"})
	default:
		writeErr(w, http.StatusBadRequest, errors.New("action 须为 continue、end 或 dismiss"))
	}
}

// handleTaskClosureConfig POST /api/tasks/{id}/closure/config(审核迭代 1):
// 任务级配置变更(开关 + 轮数 + 间隔)——快照式整体覆盖落 closure.record
// (config),最新一条即当前;下一次研判轮评估即用新值,在跑任务不中断。
func (s *Server) handleTaskClosureConfig(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req closureBodyIn
	if !readJSON(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, errors.New("开关必填"))
		return
	}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventClosureRecord, Actor: "creator",
		Payload: rings.ClosureRecord{
			Type: rings.ClosureRecConfig, Enabled: req.Enabled,
			Rounds: derefInt(req.Rounds), Minutes: derefInt(req.Minutes),
		},
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.closureConfigViewOf(r, taskID))
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
