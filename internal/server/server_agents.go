// Agent 配置栏正式 API(P2-08d):牧者 / 主宰 / 虫群三组——提示词三段式
// 的正式编辑入口(A 段可编辑正文保存/恢复默认,B/C 固定段只读展示)+
// agent 运行参数(虫群并发上限 / 重锚定步数 / 心跳巡检间隔 / 执行预算,
// 自系统设置迁入,参数跟 agent 放一起)。展示与操作同源:三组视图与
// 保存读写都走 PromptSpec 规约常量与键值表单写者(06d 临时入口的正式
// 化,无旁路装配);参数写与虫群 Claim / 主宰派发决策同一读法。
//
//	GET  /api/agents               三组视图 + 运行参数生效值(含审计)
//	POST /api/agents/prompt        保存可编辑正文(对新会话生效)
//	POST /api/agents/prompt/reset  恢复默认正文(删键)
//	POST /api/agents/params        运行参数写(字段缺省 = 不改该项)
package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ghostreaper/internal/interject"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
)

// agentSegmentView 固定段(B/C…)的只读展示项。
type agentSegmentView struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// agentRoleView 一组(一个 agent 角色)的配置视图。
type agentRoleView struct {
	Title   string `json:"title"`
	Summary string `json:"summary"` // 一句话职责(操作者语言)
	Key     string `json:"key"`     // A 段在键值表的键
	// EditableNow 当前生效正文(键值覆盖或代码默认);DefaultEditable
	// 代码默认正文(恢复默认的对照)。
	EditableNow       string             `json:"editable_now"`
	DefaultEditable   string             `json:"default_editable"`
	Overridden        bool               `json:"overridden"` // 是否被覆盖
	EditableBy        string             `json:"editable_by,omitempty"`
	EditableUpdatedAt string             `json:"editable_updated_at,omitempty"`
	Fixed             []agentSegmentView `json:"fixed"` // 代码固定段(只读展示)
	Note              string             `json:"note"`
}

// agentIntParam 一个整数运行参数的生效视图:生效值 + 代码默认 + 覆盖
// 审计(未覆盖为空)。
type agentIntParam struct {
	Value      int    `json:"value"`
	Default    int    `json:"default"`
	Overridden bool   `json:"overridden"`
	By         string `json:"by,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// agentParamsView 运行参数生效视图(装配默认 + 全局覆盖 + 审计)。
// 停滞窗口与消耗异常阈是心跳预筛的判定阈值,随心跳展示(只读,装配值)。
// 收口信号双阈值(P4-18)随主宰展示:连续零新发现轮数 + 距上次新发现
// 间隔(分钟),双条件同时满足才弹提醒。
type agentParamsView struct {
	Budget      BudgetView    `json:"budget"` // 执行预算(轮次/时长/收尾)
	Queue       agentIntParam `json:"queue"`
	Units       agentIntParam `json:"units"`        // 虫群并发上限(个单位)
	Reanchor    agentIntParam `json:"reanchor"`     // 重锚定默认间隔(步)
	Heartbeat   agentIntParam `json:"heartbeat"`    // 心跳巡检间隔(秒)
	BlockedDeep agentIntParam `json:"blocked_deep"` // 受阻判定时长(分钟,P4-27 ①:模型连续失败跨线才受阻)
	Closure     ClosureParams `json:"closure"`      // 收口信号阈值(P4-18)
	// 心跳预筛判定阈值(只读呈现,不开放写)。
	StallWindowSeconds int `json:"stall_window_seconds"`
	BurnAnomalyTokens  int `json:"burn_anomaly_tokens"`
}

// ClosureParams 收口信号双阈值(P4-18):轮数与分钟数。
type ClosureParams struct {
	Rounds  agentIntParam `json:"rounds"`  // 连续零新发现研判轮数阈值(轮)
	Minutes agentIntParam `json:"minutes"` // 距上次新发现间隔阈值(分钟)
}

// agentsReply GET /api/agents 的回复。
type agentsReply struct {
	Roles  map[string]*agentRoleView `json:"roles"` // shepherd|overmind|swarm
	Params agentParamsView           `json:"params"`
}

// agentEditableLimit 可编辑正文的长度上限(受控校验)。
const agentEditableLimit = 4000

// agentRoleSpec 一组角色的展示素材:规约(与组装同源)+ 文案与固定段
// 标签。标签顺序与 PromptSpec.Fixed 一一对应。
type agentRoleSpec struct {
	spec        swarm.PromptSpec
	title       string
	summary     string
	fixedLabels []string
	note        string
}

// agentRoleSpecs 三组定义。视图从规约常量直接构建——保存后的新正文只
// 能落到 A 段,固定段永远来自代码。
func agentRoleSpecs() map[string]agentRoleSpec {
	return map[string]agentRoleSpec{
		"shepherd": {
			spec:        shepherd.ShepherdPromptSpec,
			title:       "牧者",
			summary:     "对话入口。回答造物主的问题,把意图转成账本上的正式动作",
			fixedLabels: []string{"工具与纪律", "输出文风"},
			note:        "新会话开始时组装;固定段由代码持有,编辑不可删。",
		},
		"overmind": {
			spec:        overmind.OvermindPromptSpec,
			title:       "主宰",
			summary:     "拆解与派发。把目标拆成意图交给虫群,检查归纳发现并规划",
			fixedLabels: []string{"规划与拆解纪律", "输出规约"},
			note:        "每轮拆解前组装;固定段由代码持有,编辑不可删。",
		},
		"swarm": {
			spec:        swarm.WorkerPromptSpec,
			title:       "虫群",
			summary:     "执行单位。领取一条意图,跑工具,写回发现",
			fixedLabels: []string{"执行铁律", "写回与收尾规约"},
			note:        "每只单位起飞时按领取的授权与任务包组装;固定段由代码持有,编辑不可删。",
		},
		// 插话子会话(审核迭代加入):会话旁问/改的旁路 LLM 角色,问侧
		// 独立应答(改侧走舵杆/建议不经它);与三组同一三段式规约。
		"interject": {
			spec:        interject.InterjectPromptSpec,
			title:       "插话子会话",
			summary:     "旁路应答。会话旁提问,基于该 agent 的历史作答,不改执行",
			fixedLabels: []string{"应答纪律", "文风条款"},
			note:        "每次插话子会话组装;固定段由代码持有,编辑不可删。",
		},
	}
}

// agentRoleViewFromSpec 构建一组视图:正文走 Assemble 同一读法(键值
// 覆盖或默认),固定段直读规约常量;有覆盖时带 by/updated_at 审计。
func (s *Server) agentRoleViewFromSpec(ctx context.Context, def agentRoleSpec) *agentRoleView {
	v := &agentRoleView{
		Title:           def.title,
		Summary:         def.summary,
		Key:             def.spec.SettingKey,
		DefaultEditable: def.spec.DefaultEditable,
		Note:            def.note,
	}
	for i, text := range def.spec.Fixed {
		label := fmt.Sprintf("固定段 %d", i+1)
		if i < len(def.fixedLabels) {
			label = def.fixedLabels[i]
		}
		v.Fixed = append(v.Fixed, agentSegmentView{Label: label, Text: text})
	}
	v.EditableNow = def.spec.DefaultEditable
	if e, ok, err := s.Store.Setting(ctx, def.spec.SettingKey); err == nil && ok {
		if body := strings.TrimSpace(e.Value); body != "" {
			v.EditableNow = body
			v.Overridden = true
			v.EditableBy = e.UpdatedBy
			v.EditableUpdatedAt = e.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		}
	}
	return v
}

// agentIntEffective 计算一个整数参数的生效视图:默认 + 全局覆盖 + 审计
// (读失败/值非法退默认,与消费点同一 fail-safe 语义)。
func (s *Server) agentIntEffective(ctx context.Context, key string, def int) agentIntParam {
	p := agentIntParam{Value: def, Default: def}
	e, ok, err := s.Store.Setting(ctx, key)
	if err != nil || !ok {
		return p
	}
	n, perr := strconv.Atoi(strings.TrimSpace(e.Value))
	if perr != nil || n <= 0 {
		return p
	}
	p.Value, p.Overridden, p.By, p.UpdatedAt = n, true,
		e.UpdatedBy, e.UpdatedAt.Local().Format("2006-01-02 15:04:05")
	return p
}

// effectiveAgentParams 运行参数生效视图(与各消费点同一默认值来源)。
func (s *Server) effectiveAgentParams(ctx context.Context) agentParamsView {
	return agentParamsView{
		Budget:      s.effectiveBudget(ctx),
		Queue:       s.agentIntEffective(ctx, overmind.SettingKeyQueueLimit, overmind.DefaultQueueLimit),
		Units:       s.agentIntEffective(ctx, overmind.SettingKeyUnitsLimit, overmind.DefaultMaxConcurrentUnits),
		Reanchor:    s.agentIntEffective(ctx, swarm.SettingKeyReanchorSteps, swarm.DefaultAnchorEverySteps),
		Heartbeat:   s.agentIntEffective(ctx, overmind.SettingKeyHeartbeatSeconds, int(overmind.DefaultHeartbeatInterval.Seconds())),
		BlockedDeep: s.agentIntEffective(ctx, swarm.SettingKeyBlockedDeepMinutes, swarm.DefaultBlockedDeepMinutes),
		Closure: ClosureParams{
			Rounds:  s.agentIntEffective(ctx, overmind.SettingKeyClosureRounds, overmind.DefaultClosureRounds),
			Minutes: s.agentIntEffective(ctx, overmind.SettingKeyClosureMinutes, overmind.DefaultClosureMinutes),
		},
		StallWindowSeconds: overmind.DefaultStallSeconds,
		BurnAnomalyTokens:  overmind.DefaultBurnAnomalyTokens,
	}
}

// agentsStateReply 全量视图(读写后统一回这一份,面板据此刷新)。
func (s *Server) agentsStateReply(ctx context.Context) agentsReply {
	specs := agentRoleSpecs()
	roles := make(map[string]*agentRoleView, len(specs))
	for id, def := range specs {
		roles[id] = s.agentRoleViewFromSpec(ctx, def)
	}
	return agentsReply{Roles: roles, Params: s.effectiveAgentParams(ctx)}
}

// handleAgentsGet 三组视图 + 运行参数。
func (s *Server) handleAgentsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.agentsStateReply(r.Context()))
}

// agentRoleKey 角色 ID → A 段配置键(与各角色组装读同一键)。
func agentRoleKey(role string) (string, bool) {
	def, ok := agentRoleSpecs()[role]
	if !ok {
		return "", false
	}
	return def.spec.SettingKey, true
}

// agentPromptBody POST prompt 的请求体。
type agentPromptBody struct {
	Role string `json:"role"` // shepherd|overmind|swarm
	Body string `json:"body"`
}

// agentRoleOnlyBody POST reset 的请求体。
type agentRoleOnlyBody struct {
	Role string `json:"role"`
}

// handleAgentPromptSave 保存可编辑正文(经键值表单写者,by=creator):
// 只覆盖 A 段,对新会话生效;在跑会话不硬中断(与预算/RoE 同纪律)。
func (s *Server) handleAgentPromptSave(w http.ResponseWriter, r *http.Request) {
	var body agentPromptBody
	if !readJSON(w, r, &body) {
		return
	}
	key, ok := agentRoleKey(body.Role)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("角色须为 shepherd、overmind、swarm 或 interject"))
		return
	}
	v := strings.TrimSpace(body.Body)
	if v == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("正文不能为空;要恢复默认请用「恢复默认」"))
		return
	}
	if len(v) > agentEditableLimit {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("正文过长(现 %d 字,上限 %d 字)", len(v), agentEditableLimit))
		return
	}
	if err := s.Store.SetSetting(r.Context(), key, v, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.agentsStateReply(r.Context()))
}

// handleAgentPromptReset 恢复默认正文(删键;组装随即回代码默认)。
func (s *Server) handleAgentPromptReset(w http.ResponseWriter, r *http.Request) {
	var body agentRoleOnlyBody
	if !readJSON(w, r, &body) {
		return
	}
	key, ok := agentRoleKey(body.Role)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("角色须为 shepherd、overmind、swarm 或 interject"))
		return
	}
	if err := s.Store.DeleteSetting(r.Context(), key); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.agentsStateReply(r.Context()))
}

// agentParamsBody POST params 的请求体:各项可调,字段缺省 = 不改该项。
type agentParamsBody struct {
	QueueLimit         *int `json:"queue_limit"`
	MaxTurns           *int `json:"max_turns"`
	RunSeconds         *int `json:"run_seconds"`
	MaxConcurrentUnits *int `json:"max_concurrent_units"`
	AnchorEverySteps   *int `json:"anchor_every_steps"`
	HeartbeatSeconds   *int `json:"heartbeat_seconds"`
	ClosureRounds      *int `json:"closure_rounds"`       // P4-18 收口信号轮数阈值
	ClosureMinutes     *int `json:"closure_minutes"`      // P4-18 收口信号间隔阈值(分钟)
	BlockedDeepMinutes *int `json:"blocked_deep_minutes"` // P4-27 ① 受阻判定时长(分钟)
}

// 运行参数取值范围(受控校验)。并发上限上限取 64(单任务同时单位数
// 的现实上限);心跳间隔下限 10 秒(预筛每拍全量读事件流,过密会压垮
// 账本);其余与既有口径一致(轮次 1–1000,时长 1–86400,重锚定
// 1–1000 与建任务表单同口径)。
const (
	agentUnitsLimitMax   = 64
	agentHeartbeatMinSec = 10
	agentAnchorLimitMax  = 1000
	// 收口信号阈值取值范围(P4-18):轮数 1–100(长跑任务也用不着更多
	// 轮的确认);间隔 1 分钟–24 小时(分钟粒度)。
	agentClosureRoundsMax  = 100
	agentClosureMinutesMax = 1440
)

// handleAgentParamsUpdate 运行参数写(P2-08d 自系统设置迁入):写通用
// 键值表(by=creator,带 updated_at 轻量审计)。生效节奏与消费点同源:
// 预算与重锚定对之后领取的单位生效,并发上限下一轮派发生效,心跳间隔
// 下一拍生效——在飞单位一律不硬中断。预算两项(P4-01)值 0 = 不限
// (删键回默认;面板"留空为不限"),其余项 0 非法。
func (s *Server) handleAgentParamsUpdate(w http.ResponseWriter, r *http.Request) {
	var body agentParamsBody
	if !readJSON(w, r, &body) {
		return
	}
	if body.MaxTurns == nil && body.RunSeconds == nil && body.MaxConcurrentUnits == nil &&
		body.AnchorEverySteps == nil && body.HeartbeatSeconds == nil &&
		body.ClosureRounds == nil && body.ClosureMinutes == nil &&
		body.BlockedDeepMinutes == nil && body.QueueLimit == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("未提交任何修改项"))
		return
	}
	writes := []struct {
		key       string
		val       *int
		min       int
		max       int
		label     string
		unlimited bool // true: 0 = 不限(删键回默认,P4-01 预算专用)
	}{
		{swarm.SettingKeyMaxTurns, body.MaxTurns, 0, budgetMaxTurnsLimit, "轮次上限(轮)", true},
		{swarm.SettingKeyRunSeconds, body.RunSeconds, 0, budgetRunSecondsLimit, "时长上限(秒)", true},
		{overmind.SettingKeyQueueLimit, body.QueueLimit, 1, 1000, "派发上限(步)", false},
		{overmind.SettingKeyUnitsLimit, body.MaxConcurrentUnits, 1, agentUnitsLimitMax, "并发上限(个单位)", false},
		{swarm.SettingKeyReanchorSteps, body.AnchorEverySteps, 1, agentAnchorLimitMax, "重锚定步数(步)", false},
		{overmind.SettingKeyHeartbeatSeconds, body.HeartbeatSeconds, agentHeartbeatMinSec, budgetRunSecondsLimit, "心跳巡检间隔(秒)", false},
		{overmind.SettingKeyClosureRounds, body.ClosureRounds, 1, agentClosureRoundsMax, "收口信号轮数(轮)", false},
		{overmind.SettingKeyClosureMinutes, body.ClosureMinutes, 1, agentClosureMinutesMax, "收口信号间隔(分钟)", false},
		{swarm.SettingKeyBlockedDeepMinutes, body.BlockedDeepMinutes, 1, agentClosureMinutesMax, "受阻判定时长(分钟)", false},
	}
	// 先整批校验再落键(避免半批写入)。
	for _, wv := range writes {
		if wv.val == nil {
			continue
		}
		if *wv.val < wv.min || *wv.val > wv.max {
			if wv.unlimited {
				writeErr(w, http.StatusBadRequest,
					fmt.Errorf("%s须为 0–%d 的整数(0 = 不限)", wv.label, wv.max))
			} else {
				writeErr(w, http.StatusBadRequest,
					fmt.Errorf("%s须为 %d–%d 的整数", wv.label, wv.min, wv.max))
			}
			return
		}
	}
	for _, wv := range writes {
		if wv.val == nil {
			continue
		}
		// 预算 0 = 不限:删键回默认(与恢复默认正文同款落法)。
		if wv.unlimited && *wv.val == 0 {
			if err := s.Store.DeleteSetting(r.Context(), wv.key); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			continue
		}
		if err := s.Store.SetSetting(r.Context(), wv.key, strconv.Itoa(*wv.val), "creator"); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.agentsStateReply(r.Context()))
}
