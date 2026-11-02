// P2-12 一级·观测:Token 成本仪表盘 + 工具执行流水 + 两个吞吐指标。
// 全部为既有记账的只读投影,零新增采集——token 仪表盘读 token 独立表
//(第四章 §6 账本分工,对账以本表为唯一真值);工具流水读账本审计流
// 的 tool_call 记录(swarm.record / shepherd.record——工具调用本就逐次
// 入账,黑匣子通道只收预算/心跳类系统事件);吞吐指标从事件流确定性
// 推导:意图等待领取时长 = 步骤 dispatched→running 的时间差,意图队列
// 深度 = 各时刻已派发未领取的步骤数(主宰是否成为瓶颈的实证依据,
// 收割减负方案挂账 P3)。

package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
)

// obsTokenBucketCount 时间维度分桶数(仪表盘曲线:全程等宽桶)。
const obsTokenBucketCount = 48

// obsAgentGroup 记账归属归组:swarm:uN→swarm、interject:*→interject、
// overmind / shepherd 原样,其余取冒号前缀(未知形态如实保留)。
func obsAgentGroup(agent string) string {
	for _, prefix := range []string{"swarm", "interject"} {
		if agent == prefix || strings.HasPrefix(agent, prefix+":") {
			return prefix
		}
	}
	if i := strings.IndexByte(agent, ':'); i > 0 {
		return agent[:i]
	}
	return agent
}

// obsTokenDim 仪表盘一个维度行的聚合(任务 / 模型 / 角色共用形状)。
type obsTokenDim struct {
	Name       string `json:"name"` // 任务 ID / 模型名 / 角色组
	Title      string `json:"title,omitempty"`
	Calls      int    `json:"calls"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
	Models     int    `json:"models,omitempty"`
	FirstAt    string `json:"first_at,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
}

func (d *obsTokenDim) add(x rings.TokenUsage) {
	d.Calls++
	d.Input += x.InputTokens
	d.Output += x.OutputTokens
	d.CacheRead += x.CacheReadTokens
	d.CacheWrite += x.CacheWriteTokens
}

// handleObsTokens Token 成本仪表盘(P2-12):token 独立表的全局汇总——
// 总量 / 按任务 / 按模型 / 按角色 / 时间分桶曲线。点任务钻取的逐次明细
// 走既有端点 GET /api/tasks/{id}/tokens(本端点只出聚合,同源同真值)。
func (s *Server) handleObsTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.AllTokenUsageRows(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	tasks, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	titles := map[string]string{}
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}

	totals := struct {
		Calls      int `json:"calls"`
		Tasks      int `json:"tasks"`
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cache_read"`
		CacheWrite int `json:"cache_write"`
	}{}
	byTask := map[string]*obsTokenDim{}
	byModel := map[string]*obsTokenDim{}
	byAgent := map[string]*obsTokenDim{}
	modelsOfTask := map[string]map[string]bool{}
	firstOf, lastOf := map[string]time.Time{}, map[string]time.Time{}
	var firstAt, lastAt time.Time
	for _, x := range rows {
		totals.Calls++
		totals.Input += x.InputTokens
		totals.Output += x.OutputTokens
		totals.CacheRead += x.CacheReadTokens
		totals.CacheWrite += x.CacheWriteTokens
		if firstAt.IsZero() || x.OccurredAt.Before(firstAt) {
			firstAt = x.OccurredAt
		}
		if x.OccurredAt.After(lastAt) {
			lastAt = x.OccurredAt
		}

		t, ok := byTask[x.TaskID]
		if !ok {
			t = &obsTokenDim{Name: x.TaskID, Title: titles[x.TaskID]}
			byTask[x.TaskID] = t
			modelsOfTask[x.TaskID] = map[string]bool{}
		}
		m, ok := byModel[x.Model]
		if !ok {
			m = &obsTokenDim{Name: x.Model}
			byModel[x.Model] = m
		}
		a, ok := byAgent[obsAgentGroup(x.Agent)]
		if !ok {
			a = &obsTokenDim{Name: obsAgentGroup(x.Agent)}
			byAgent[a.Name] = a
		}
		for _, d := range []*obsTokenDim{t, m, a} {
			d.add(x)
		}
		modelsOfTask[x.TaskID][x.Model] = true
		if ft, ok := firstOf[x.TaskID]; !ok || x.OccurredAt.Before(ft) {
			firstOf[x.TaskID] = x.OccurredAt
		}
		if x.OccurredAt.After(lastOf[x.TaskID]) {
			lastOf[x.TaskID] = x.OccurredAt
		}
	}
	totals.Tasks = len(byTask)

	// 时间维度:全程等宽 48 桶(单点数据退化为首桶,如实呈现)。
	type bucketRow struct {
		T          string `json:"t"`
		Input      int    `json:"input"`
		Output     int    `json:"output"`
		CacheRead  int    `json:"cache_read"`
		CacheWrite int    `json:"cache_write"`
	}
	series := []bucketRow{}
	if len(rows) > 0 {
		span := lastAt.Sub(firstAt)
		if span <= 0 {
			span = time.Second
		}
		width := span / obsTokenBucketCount
		if width <= 0 {
			width = time.Second
		}
		buckets := make([]bucketRow, obsTokenBucketCount)
		for i := range buckets {
			buckets[i].T = firstAt.Local().Add(time.Duration(i) * width).Format(time.RFC3339)
		}
		for _, x := range rows {
			idx := int(x.OccurredAt.Sub(firstAt) / width)
			if idx < 0 {
				idx = 0
			}
			if idx >= obsTokenBucketCount {
				idx = obsTokenBucketCount - 1
			}
			buckets[idx].Input += x.InputTokens
			buckets[idx].Output += x.OutputTokens
			buckets[idx].CacheRead += x.CacheReadTokens
			buckets[idx].CacheWrite += x.CacheWriteTokens
		}
		for _, b := range buckets {
			series = append(series, b)
		}
	}

	mkList := func(m map[string]*obsTokenDim) []*obsTokenDim {
		out := make([]*obsTokenDim, 0, len(m))
		for _, v := range m {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Calls > out[j].Calls })
		return out
	}
	taskList := mkList(byTask)
	for _, t := range taskList {
		t.Models = len(modelsOfTask[t.Name])
		t.FirstAt = firstOf[t.Name].Local().Format("2006-01-02 15:04:05")
		t.LastAt = lastOf[t.Name].Local().Format("2006-01-02 15:04:05")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totals":   totals,
		"by_task":  taskList,
		"by_model": mkList(byModel),
		"by_agent": mkList(byAgent),
		"series":   series,
	})
}

// ---- 工具执行流水 ----

// obsToolRow 工具流水一行:一次工具调用(虫群或牧者)的审计投影。
type obsToolRow struct {
	TaskID     string `json:"task_id"`
	TaskTitle  string `json:"task_title"`
	Agent      string `json:"agent"`             // swarm:uN / shepherd(面板侧译为 虫群单位N / 牧者)
	StepID     string `json:"step_id,omitempty"` // 虫群调用钉回的步骤;牧者无
	Transcript string `json:"transcript,omitempty"`
	Tool       string `json:"tool"`
	Args       string `json:"args,omitempty"`
	IsError    bool   `json:"is_error"`
	ResultHead string `json:"result_head,omitempty"`
	ResultRef  string `json:"result_ref,omitempty"`
	At         string `json:"at"`
	atTime     time.Time
}

// handleObsTools 工具执行流水(P2-12):扫账本审计流的 tool_call 记录
// (swarm.record + shepherd.record,零新增采集),按任务 / 工具 / 状态
// 过滤,新到旧。tools 为过滤前全量工具计数(筛选选项与对账的依据);
// counts 为过滤后(限流前)的计数。
func (s *Server) handleObsTools(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fTask := q.Get("task")
	fTool := q.Get("tool")
	fStatus := q.Get("status") // ok|error,空 = 全部
	limit := 200
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}

	tasks, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	all := []obsToolRow{}
	for _, t := range tasks {
		if fTask != "" && t.ID != fTask {
			continue
		}
		evs, err := s.Store.Events(r.Context(), t.ID, 0, 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, ev := range evs {
			var row obsToolRow
			switch ev.Kind {
			case rings.EventSwarmRecord:
				var rec rings.SwarmRecord
				if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.SwarmRecToolCall {
					continue
				}
				row = obsToolRow{
					Agent: ev.Actor, StepID: rec.StepID, Transcript: rec.TranscriptID,
					Tool: rec.ToolName, Args: string(rec.ToolArgs), IsError: rec.IsError,
					ResultHead: rec.ResultHead, ResultRef: rec.ResultRef,
				}
			case rings.EventShepherdRecord:
				var rec rings.ShepherdRecord
				if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.ShepherdRecToolCall {
					continue
				}
				row = obsToolRow{
					Agent: shepherd.ActorShepherd, Tool: rec.ToolName, Args: string(rec.ToolArgs),
					IsError: rec.IsError, ResultHead: rec.ResultHead,
				}
			default:
				continue
			}
			row.TaskID, row.TaskTitle = t.ID, t.Title
			row.At = ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
			row.atTime = ev.OccurredAt
			all = append(all, row)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].atTime.Equal(all[j].atTime) {
			return all[i].atTime.After(all[j].atTime)
		}
		return all[i].TaskID < all[j].TaskID
	})

	// 工具选项:过滤前全量计数(名称升序稳定呈现)。
	type toolStat struct {
		Name   string `json:"name"`
		Count  int    `json:"count"`
		Errors int    `json:"errors"`
	}
	toolIdx := map[string]*toolStat{}
	for _, x := range all {
		tc, ok := toolIdx[x.Tool]
		if !ok {
			tc = &toolStat{Name: x.Tool}
			toolIdx[x.Tool] = tc
		}
		tc.Count++
		if x.IsError {
			tc.Errors++
		}
	}
	tools := make([]*toolStat, 0, len(toolIdx))
	for _, tc := range toolIdx {
		tools = append(tools, tc)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	counts := struct {
		Total int `json:"total"` // 符合当前过滤条件的总行数(Shown ≤ Total,超出部分被 limit 截断)
		Ok    int `json:"ok"`
		Error int `json:"error"`
		Shown int `json:"shown"`
		Limit int `json:"limit"`
	}{Limit: limit}
	rows := make([]obsToolRow, 0, limit)
	for _, x := range all {
		if fTool != "" && x.Tool != fTool {
			continue
		}
		switch fStatus {
		case "ok":
			if x.IsError {
				continue
			}
		case "error":
			if !x.IsError {
				continue
			}
		}
		counts.Total++
		if x.IsError {
			counts.Error++
		} else {
			counts.Ok++
		}
		if len(rows) < limit {
			rows = append(rows, x)
		}
	}
	counts.Shown = len(rows)
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": rows, "tools": tools, "counts": counts,
	})
}

// ---- 吞吐指标(虫群闲置等待 + 意图队列深度)----

// obsQueuePoint 队列深度时间线的一个变化点(阶跃线)。
type obsQueuePoint struct {
	T     string `json:"t"`
	Depth int    `json:"depth"`
}

// handleObsThroughput 两个吞吐指标(P2-12,从事件流确定性推导,零新增
// 采集):① 意图等待领取时长——每次 dispatched→running 的时间差(虫群
// 单位等主宰派发节奏的实证);② 意图队列深度——各时刻已派发未领取的
// 步骤数(阶跃时间线)。?task= 限定单任务,缺省跨任务合并。
//
// 推导口径:逐任务按 revision 序(账本权威序)重放步骤状态机,得到
// 每个步骤的「派发区间」[入队时刻, 出队时刻](仍在队的开口区间);
// 等待时长 = dispatched→running 的时间差(时刻显式,与事件次序无关);
// 深度曲线 = 全部区间端点按发生时刻扫掠(+1 入队 / -1 出队,同刻先入
// 后出)——同批事件的 occurred_at 与 revision 序可能不一致,按区间扫
// 掠对两种序都得出同一张物理时间线。
func (s *Server) handleObsThroughput(w http.ResponseWriter, r *http.Request) {
	fTask := r.URL.Query().Get("task")
	tasks, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	type waitRow struct {
		TaskID  string  `json:"task_id"`
		StepID  string  `json:"step_id"`
		At      string  `json:"at"`
		Seconds float64 `json:"seconds"`
		atTime  time.Time
	}
	waits := []waitRow{}
	// queueIntv 派发区间:end 零值 = 仍在队(开口)。
	type queueIntv struct {
		start, end time.Time
	}
	intvs := []queueIntv{}

	for _, t := range tasks {
		if fTask != "" && t.ID != fTask {
			continue
		}
		evs, err := s.Store.Events(r.Context(), t.ID, 0, 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// stepStat 步骤状态机:known = 经 node.added 见过(直接以终态
		// 入账的种子步骤不进队列,如实);intv = 在队区间下标(出队后 -1)。
		type stepStat struct {
			known      bool
			dispatched bool
			since      time.Time
			intv       int
		}
		steps := map[string]*stepStat{}
		enter := func(ss *stepStat, at time.Time) {
			ss.dispatched, ss.since, ss.intv = true, at, len(intvs)
			intvs = append(intvs, queueIntv{start: at})
		}
		leave := func(ss *stepStat, at time.Time) {
			if ss.dispatched {
				intvs[ss.intv].end = at
				ss.dispatched, ss.intv = false, -1
			}
		}
		for _, ev := range evs {
			switch ev.Kind {
			case rings.EventNodeAdded:
				var p rings.NodeAdded
				if json.Unmarshal(ev.Payload, &p) != nil || p.NodeType != rings.NodeStep {
					continue
				}
				ss := &stepStat{known: true, intv: -1}
				if p.Status == "dispatched" {
					enter(ss, ev.OccurredAt)
				}
				steps[p.NodeID] = ss
			case rings.EventNodeStateChanged:
				var p rings.NodeStateChanged
				if json.Unmarshal(ev.Payload, &p) != nil {
					continue
				}
				ss, ok := steps[p.NodeID]
				if !ok || !ss.known {
					continue
				}
				switch {
				case p.Status == "running" && ss.dispatched:
					waits = append(waits, waitRow{
						TaskID: t.ID, StepID: p.NodeID,
						At:      ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
						Seconds: ev.OccurredAt.Sub(ss.since).Seconds(), atTime: ev.OccurredAt,
					})
					leave(ss, ev.OccurredAt)
				case p.Status == "dispatched" && !ss.dispatched:
					// running → dispatched(重启收养的幽灵清理):重新入队。
					enter(ss, ev.OccurredAt)
				case p.Status != "dispatched" && p.Status != "running" && ss.dispatched:
					// 队列内直接结案(如收尾结案):离开队列。
					leave(ss, ev.OccurredAt)
				}
			}
		}
	}

	// 端点扫掠:同刻先入队(+1)后出队(-1),得到阶跃时间线。
	type endpoint struct {
		t   time.Time
		det int
	}
	ends := make([]endpoint, 0, len(intvs)*2)
	for _, iv := range intvs {
		ends = append(ends, endpoint{t: iv.start, det: 1})
		if !iv.end.IsZero() {
			ends = append(ends, endpoint{t: iv.end, det: -1})
		}
	}
	sort.SliceStable(ends, func(i, j int) bool {
		if !ends[i].t.Equal(ends[j].t) {
			return ends[i].t.Before(ends[j].t)
		}
		return ends[i].det > ends[j].det
	})
	queue := []obsQueuePoint{}
	depth, maxDepth := 0, 0
	for _, e := range ends {
		depth += e.det
		if depth > maxDepth {
			maxDepth = depth
		}
		queue = append(queue, obsQueuePoint{T: e.t.Local().Format("2006-01-02 15:04:05"), Depth: depth})
	}

	sort.SliceStable(waits, func(i, j int) bool { return waits[i].atTime.Before(waits[j].atTime) })
	sum, maxWait := 0.0, 0.0
	for _, x := range waits {
		sum += x.Seconds
		if x.Seconds > maxWait {
			maxWait = x.Seconds
		}
	}
	avg := 0.0
	if len(waits) > 0 {
		avg = sum / float64(len(waits))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"waits": waits,
		"summary": map[string]any{
			"claims":      len(waits),
			"avg_seconds": avg,
			"max_seconds": maxWait,
			"waiting_now": depth,
			"max_depth":   maxDepth,
		},
		"queue": queue,
	})
}

// ---- 拦截命中统计(P4-07,问题清单 #14)----

// 拦截命中是审批审计流(approval.record)的只读投影,零新增采集:
// 拒绝类命中 = Type=denied 且 DenyLine=hard_rule / intent_rule(对账口径,
// 与账本逐笔相等);弹门类命中 = Type=requested 且 Note 带规则命中上下文
//(P4-03 ② 弹门处置的硬规则),裁决经 RequestID 关联回命中行、不另计
// 命中。规则身份从记录自带的拒绝原因 / 命中上下文解析——账本不携带
// 规则 ID,规则可改名删除,以命中当时的文本分组,数字与账本天然一致。
// 被拒单位的后续走向(换路成功继续 / 卡死 / 任务已停止等)从事件流
// 确定性推导,可下钻看被拒后的后续轨迹。误伤模式数据化提前暴露:
// 高频命中规则排前(问题清单 #14 的初衷)。

// obsHitOutcome 命中结局(拒绝线/弹门裁决的投影词,面板按词表译界面词)。
const (
	hitDeniedHard   = "denied_hard"   // 内置拦截规则拒绝(deny_line=hard_rule)
	hitDeniedIntent = "denied_intent" // 任务级拦截拒绝(deny_line=intent_rule)
	hitApproved     = "approved"      // 弹门:面板批准执行
	hitDeniedPanel  = "denied_panel"  // 弹门:面板拒绝(deny_line=panel)
	hitCloseout     = "closeout"      // 弹门:收尾按拒绝结案(deny_line=closeout)
	hitPending      = "pending"       // 弹门:待裁决
)

// obsFollowOutcome 被拒后的走向(事件流确定性推导;优先级见 followOf)。
const (
	followReroutedComplete = "rerouted_complete" // 换路成功·任务完成
	followRerouted         = "rerouted"          // 换路成功继续
	followTaskStopped      = "task_stopped"      // 任务已停止
	followStalled          = "stalled"           // 卡死
	followActive           = "active"            // 继续中
	followPaused           = "paused"            // 已暂停
)

// obsStepWords / obsLifeWords / obsTaskNowWords 界面词表(术语门槛:
// 步骤状态 / 生命周期动作 / 任务当前状态翻译为操作者语言;步骤状态是
// 封闭词表,表外值兜底为中性词不裸露机器词)。
var obsStepWords = map[string]string{
	"dispatched": "派发", "running": "开始执行", "done": "完成",
	"blocked": "受阻", "approval_denied": "审批拒绝终止",
	"exhausted": "预算耗尽", "failed": "失败", "aborted": "中止",
	"checkpointed": "断点存档", // 单位收场状态(P3-07;步骤不取此值)
}

var obsLifeWords = map[string]string{
	"created": "任务创建", "queued": "任务排队", "started": "任务启动",
	"resumed": "任务恢复", "paused": "任务暂停", "stopped": "任务停止",
	"completed": "任务完成", "settling": "任务收尾", "hibernated": "任务冬眠",
	"checkpointed": "任务断点存档",
}

var obsTaskNowWords = map[string]string{
	"created": "已创建", "queued": "排队中", "started": "运行中", "resumed": "运行中",
	"paused": "已暂停", "hibernated": "冬眠中", "checkpointed": "断点存档",
	"settling": "收尾中", "stopped": "已停止", "completed": "已完成",
}

// obsInterceptHit 一次拦截命中(近期命中记录行)。
type obsInterceptHit struct {
	At        string         `json:"at"` // 命中时刻(弹门为请求时刻)
	Rev       int64          `json:"rev"`
	atTime    time.Time      // 排序锚(revision 仅任务内单调,跨任务按发生时刻)
	TaskID    string         `json:"task_id"`
	TaskTitle string         `json:"task_title"`
	StepID    string         `json:"step_id,omitempty"`
	UnitID    string         `json:"unit_id,omitempty"`
	Intent    string         `json:"intent,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Args      string         `json:"args,omitempty"` // 调用参数原文(人即兜底的核对物)
	Outcome   string         `json:"outcome"`
	Reason    string         `json:"reason,omitempty"`
	Note      string         `json:"note,omitempty"`   // 弹门命中上下文
	Follow    *obsFollowView `json:"follow,omitempty"` // 后续走向(拒绝类命中携带)
}

// obsFollowEvent 后续轨迹时间线一行(按账本次序,时间正序呈现)。
type obsFollowEvent struct {
	Rev  int64  `json:"rev"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// obsFollowView 被拒单位后续走向与轨迹(下钻数据)。
type obsFollowView struct {
	Outcome   string           `json:"outcome"`
	TaskNow   string           `json:"task_now,omitempty"` // 任务当前状态词
	StepsDone int              `json:"steps_done"`         // 被拒后新派发并完成的步骤数
	UnitCalls int              `json:"unit_calls"`         // 被拒单位被拒后继续的工具调用数(意图拦截不熔断)
	UnitEnd   string           `json:"unit_end,omitempty"` // 被拒单位收场状态词(空 = 会话仍在)
	Events    []obsFollowEvent `json:"events"`
	Truncated bool             `json:"truncated,omitempty"` // 轨迹超上限截断(保留紧邻被拒的前段)
}

// obsRuleHits 一条拦截规则的命中聚合(高频在前,服务端排序)。
type obsRuleHits struct {
	Line        string            `json:"line"` // hard_rule | intent_rule
	Group       string            `json:"group,omitempty"`
	Name        string            `json:"name"`
	Hits        int               `json:"hits"`   // 命中合计 = 拒绝 + 弹门
	Denied      int               `json:"denied"` // 拒绝类命中(对账口径:账本 approval.record 逐笔)
	Gate        int               `json:"gate"`   // 弹门类命中(请求出现即计)
	Approved    int               `json:"approved"`
	PanelDenied int               `json:"panel_denied"`
	Closeout    int               `json:"closeout"`
	Pending     int               `json:"pending"`
	Follows     map[string]int    `json:"follows,omitempty"` // 拒绝类命中的走向分布
	LastAt      string            `json:"last_at"`
	Recent      []obsInterceptHit `json:"recent"` // 近期命中(新到旧,至多 10 条)
}

// obsInterceptTaskScan 单任务的走向推导上下文(事件流一遍扫出)。
type obsInterceptTaskScan struct {
	id, title, status string
	stepFirst         map[string]int64          // 步骤首见 revision(node.added 或首次迁移)
	stepLast          map[string]string         // 步骤最新状态(现势判据)
	transitions       []obsFollowEvent          // 步骤迁移轨迹
	lifecycle         []obsFollowEvent          // 生命周期轨迹
	unitEnd           map[string]obsFollowEvent // 各单位最后一次收场(session_finished)
	unitCalls         map[string][]int64        // 各单位工具调用的 revision 清单
}

// obsHardRuleRef 解析硬规则身份(拒绝原因与弹门上下文同构,前缀不同):
// "内置硬规则命中:组 · 名称(命中片段 …)" / "命中内置拦截规则:组 · 名称(…)"。
// 组名与规则名以首段" · "分隔、括号前截取,与面板拒绝文案的解析口径一致。
func obsHardRuleRef(text, prefix string) (group, name string, ok bool) {
	if !strings.HasPrefix(text, prefix) {
		return "", "", false
	}
	head := strings.SplitN(text[len(prefix):], "(命中片段", 2)[0]
	i := strings.Index(head, " · ")
	if i < 0 {
		return "", "", false
	}
	group, name = strings.TrimSpace(head[:i]), strings.TrimSpace(head[i+len(" · "):])
	return group, name, group != "" && name != ""
}

// obsIntentRuleRef 解析任务级拦截身份(禁止与超额两种拒绝原因同身):
// "任务级拦截:禁止对 X 做 A" / "任务级拦截:对 X 的 A 已用满 N 次上限…" → "A · X"。
func obsIntentRuleRef(reason string) (label string, ok bool) {
	const p = "任务级拦截:"
	if !strings.HasPrefix(reason, p) {
		return "", false
	}
	rest := reason[len(p):]
	if s, f := strings.CutPrefix(rest, "禁止对"); f {
		if i := strings.Index(s, " 做"); i > 0 {
			return strings.TrimSpace(s[i+len(" 做"):]) + " · " + strings.TrimSpace(s[:i]), true
		}
		return "", false
	}
	if s, f := strings.CutPrefix(rest, "对"); f {
		i := strings.Index(s, " 的")
		if i <= 0 {
			return "", false
		}
		if j := strings.Index(s[i:], "已用满"); j > 0 {
			action := strings.TrimSpace(s[i+len(" 的") : i+j])
			return action + " · " + strings.TrimSpace(s[:i]), true
		}
	}
	return "", false
}

// noteStatus 从 session_finished note 取收场状态词(status= 字段,
// P4-06 的结构化收场同源口径);缺失返回空串。
func noteStatus(note string) string {
	i := strings.Index(note, "status=")
	if i < 0 {
		return ""
	}
	v := note[i+len("status="):]
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	return v
}

// scanInterceptTask 一遍扫任务事件流,建走向推导上下文(事件按 revision
// 升序 = 账本权威序)。
func scanInterceptTask(t rings.TaskRow, evs []rings.RawEvent) *obsInterceptTaskScan {
	sc := &obsInterceptTaskScan{
		id: t.ID, title: t.Title, status: t.Status,
		stepFirst: map[string]int64{}, stepLast: map[string]string{},
		unitEnd: map[string]obsFollowEvent{}, unitCalls: map[string][]int64{},
	}
	stepType := map[string]bool{}
	seeStep := func(id string, rev int64) {
		if _, ok := sc.stepFirst[id]; !ok {
			sc.stepFirst[id] = rev
		}
	}
	wordOf := func(status string) string {
		if w, ok := obsStepWords[status]; ok {
			return w
		}
		return "状态变化" // 封闭词表外的兜底,不裸露机器词
	}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil || p.NodeType != rings.NodeStep {
				continue
			}
			stepType[p.NodeID] = true
			seeStep(p.NodeID, ev.Revision)
			sc.stepLast[p.NodeID] = p.Status
			// 入图即带状态(真实派发形态:node.added dispatched)也入
			// 轨迹——派发时刻是"换路"时间线的起点。
			if p.Status != "" {
				sc.transitions = append(sc.transitions, obsFollowEvent{
					Rev: ev.Revision, At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
					Text: "步骤 " + p.NodeID + " · " + wordOf(p.Status),
				})
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil || !stepType[p.NodeID] {
				continue
			}
			seeStep(p.NodeID, ev.Revision)
			sc.stepLast[p.NodeID] = p.Status
			sc.transitions = append(sc.transitions, obsFollowEvent{
				Rev: ev.Revision, At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
				Text: "步骤 " + p.NodeID + " · " + wordOf(p.Status),
			})
		case rings.EventTaskLifecycle:
			var p rings.TaskLifecycle
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			word, known := obsLifeWords[p.Action]
			if !known {
				word = "任务状态变化"
			}
			if p.Action == "started" && p.Detail != nil && p.Detail["woken"] == true {
				word = "任务唤醒"
			}
			sc.lifecycle = append(sc.lifecycle, obsFollowEvent{
				Rev: ev.Revision, At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
				Text: word,
			})
		case rings.EventSwarmRecord:
			var p rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Type {
			case rings.SwarmRecToolCall:
				// 只计实际执行的调用(被拒绝的调用留痕是失败记录,不算
				//"继续")。
				if !p.IsError {
					sc.unitCalls[p.UnitID] = append(sc.unitCalls[p.UnitID], ev.Revision)
				}
			case rings.SwarmRecSessionEnd:
				sc.unitEnd[p.UnitID] = obsFollowEvent{
					Rev: ev.Revision, At: ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
					Text: "单位 " + p.UnitID + " · " + wordOf(noteStatus(p.Note)),
				}
			}
		}
	}
	return sc
}

// activeWorkNow 任务当前是否有在队或执行中的步骤(走向"继续中"的判据)。
func (sc *obsInterceptTaskScan) activeWorkNow() bool {
	for _, st := range sc.stepLast {
		if st == "dispatched" || st == "running" {
			return true
		}
	}
	return false
}

// stepIDOf 从轨迹行文本取步骤 ID("步骤 sN · 词" → sN)。
func stepIDOf(text string) string {
	s, ok := strings.CutPrefix(text, "步骤 ")
	if !ok {
		return ""
	}
	if i := strings.Index(s, " · "); i > 0 {
		return s[:i]
	}
	return s
}

// followOf 被拒后的走向推导(锚 = 拒绝类命中的账本 revision;只看其后)。
// 优先级:任务完成 > 后续推进 > 任务停止 > 该单位后续受阻 > 冻结态 >
// 在队在跑 > 卡死。"换路成功"只认被拒后新出现的步骤完成——被拒时已在
// 跑的步骤后来收好属"其余工作照常",不算对被拒方向的换路(轨迹里如实
// 可见);"卡死"= 任务仍在跑但零在队零在跑零推进(等心跳的沉默任务)。
func (sc *obsInterceptTaskScan) followOf(unitID string, rev int64) *obsFollowView {
	fv := &obsFollowView{TaskNow: obsTaskNowWords[sc.status], Events: []obsFollowEvent{}}
	if fv.TaskNow == "" {
		fv.TaskNow = sc.status
	}
	newDone, completed, stopped, unitDone, unitBlocked := 0, false, false, false, false
	for _, tr := range sc.transitions {
		if tr.Rev <= rev {
			continue
		}
		fv.Events = append(fv.Events, tr)
		if strings.HasSuffix(tr.Text, " · 完成") && sc.stepFirst[stepIDOf(tr.Text)] > rev {
			newDone++
		}
	}
	for _, lc := range sc.lifecycle {
		if lc.Rev <= rev {
			continue
		}
		fv.Events = append(fv.Events, lc)
		switch lc.Text {
		case "任务完成":
			completed = true
		case "任务停止":
			stopped = true
		}
	}
	if end, ok := sc.unitEnd[unitID]; ok && end.Rev > rev {
		fv.Events = append(fv.Events, end)
		switch {
		case strings.HasSuffix(end.Text, " · 完成"):
			unitDone = true
		case strings.HasSuffix(end.Text, " · 受阻"):
			unitBlocked = true
		}
		fv.UnitEnd = strings.TrimPrefix(end.Text, "单位 "+unitID+" · ")
	}
	for _, r := range sc.unitCalls[unitID] {
		if r > rev {
			fv.UnitCalls++
		}
	}
	sort.SliceStable(fv.Events, func(i, j int) bool { return fv.Events[i].Rev < fv.Events[j].Rev })
	if len(fv.Events) > 60 {
		fv.Events, fv.Truncated = fv.Events[:60], true
	}
	fv.StepsDone = newDone

	switch {
	case completed:
		fv.Outcome = followReroutedComplete
	case newDone > 0 || unitDone:
		fv.Outcome = followRerouted
	case stopped:
		fv.Outcome = followTaskStopped
	case unitBlocked:
		fv.Outcome = followStalled // 该单位被拒后最终受阻
	case sc.status == "paused" || sc.status == "hibernated" || sc.status == "checkpointed":
		fv.Outcome = followPaused
	case sc.status == "stopped":
		// 任务已停(含停止先于拒绝入账的收尾结案竞态):死任务里遗留的
		// running 步骤永远不会动,"继续中"是误读。
		fv.Outcome = followTaskStopped
	case sc.status == "completed":
		fv.Outcome = followReroutedComplete
	case sc.activeWorkNow():
		fv.Outcome = followActive
	default:
		fv.Outcome = followStalled
	}
	return fv
}

// obsHitNeedsFollow 哪些结局需要走向:弹门批准/待决不熔断单位、无走向;
// 其余(硬规则拒/意图拦拒/面板拒/收尾结案)单位死在命中上,推导走向。
func obsHitNeedsFollow(outcome string) bool {
	return outcome == hitDeniedHard || outcome == hitDeniedIntent ||
		outcome == hitDeniedPanel || outcome == hitCloseout
}

const obsRecentHitsPerRule = 10

// obsInterceptRule 规则聚合的工作形态:全量命中行在内存里凑齐(裁决
// 关联与走向分布都要看全量),末端一次出计数与近期列表。
type obsInterceptRule struct {
	obsRuleHits
	hits   []*obsInterceptHit
	byRole map[string]*obsInterceptHit // request_id → 弹门命中行(裁决关联)
}

// handleObsIntercepts 拦截命中统计(P4-07):每条拦截规则的命中次数与
// 近期命中记录、被拒单位的后续走向与轨迹下钻;规则按命中合计降序
// (高频突出)。?task= 限定单任务,缺省跨任务合并。
func (s *Server) handleObsIntercepts(w http.ResponseWriter, r *http.Request) {
	fTask := r.URL.Query().Get("task")
	tasks, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rules := map[string]*obsInterceptRule{}
	// 计数与走向分布按全量命中行的最终结局一次推导(裁决会让弹门命中
	// 的结局从待决迁移到终局,不能边加边数)。
	countOf := func(ra *obsInterceptRule) {
		for _, h := range ra.hits {
			switch h.Outcome {
			case hitDeniedHard, hitDeniedIntent:
				ra.Denied++
			case hitApproved:
				ra.Gate++
				ra.Approved++
			case hitDeniedPanel:
				ra.Gate++
				ra.PanelDenied++
			case hitCloseout:
				ra.Gate++
				ra.Closeout++
			case hitPending:
				ra.Gate++
				ra.Pending++
			}
			if h.Follow != nil {
				if ra.Follows == nil {
					ra.Follows = map[string]int{}
				}
				ra.Follows[h.Follow.Outcome]++
			}
		}
		ra.Hits = len(ra.hits)
	}

	for _, t := range tasks {
		if fTask != "" && t.ID != fTask {
			continue
		}
		evs, err := s.Store.Events(r.Context(), t.ID, 0, 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		sc := scanInterceptTask(t, evs)
		getRule := func(key string, proto obsRuleHits) *obsInterceptRule {
			ra, ok := rules[key]
			if !ok {
				proto.Follows = nil
				ra = &obsInterceptRule{obsRuleHits: proto, byRole: map[string]*obsInterceptHit{}}
				rules[key] = ra
			}
			return ra
		}
		for _, ev := range evs {
			if ev.Kind != rings.EventApprovalRecord {
				continue
			}
			var rec rings.ApprovalRecord
			if json.Unmarshal(ev.Payload, &rec) != nil {
				continue
			}
			at := ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
			base := obsInterceptHit{
				At: at, atTime: ev.OccurredAt, Rev: ev.Revision, TaskID: t.ID, TaskTitle: t.Title,
				StepID: rec.StepID, UnitID: rec.UnitID, Intent: rec.Intent,
				Tool: rec.ToolName, Args: string(rec.ToolArgs), Note: rec.Note,
			}
			switch {
			// 拒绝类·硬规则:规则身份取拒绝原因。
			case rec.Type == rings.ApprovalRecDenied && rec.DenyLine == rings.DenyLineHardRule:
				group, name, ok := obsHardRuleRef(rec.Reason, "内置硬规则命中:")
				if !ok {
					group, name = "未识别规则", clipText(rec.Reason, 60)
				}
				h := base
				h.Outcome, h.Reason = hitDeniedHard, rec.Reason
				h.Follow = sc.followOf(rec.UnitID, ev.Revision)
				ra := getRule("hard\x00"+group+"\x00"+name,
					obsRuleHits{Line: rings.DenyLineHardRule, Group: group, Name: name})
				ra.hits = append(ra.hits, &h)
			// 拒绝类·任务级拦截:规则身份取动作 · 范围。
			case rec.Type == rings.ApprovalRecDenied && rec.DenyLine == rings.DenyLineIntentRule:
				label, ok := obsIntentRuleRef(rec.Reason)
				if !ok {
					label = clipText(rec.Reason, 60)
				}
				h := base
				h.Outcome, h.Reason = hitDeniedIntent, rec.Reason
				h.Follow = sc.followOf(rec.UnitID, ev.Revision)
				ra := getRule("intent\x00"+label, obsRuleHits{Line: rings.DenyLineIntentRule, Name: label})
				ra.hits = append(ra.hits, &h)
			// 弹门类:请求即命中(结局从待决随裁决迁移),身份取命中上下文。
			case rec.Type == rings.ApprovalRecRequested &&
				strings.HasPrefix(rec.Note, "命中内置拦截规则:"):
				group, name, ok := obsHardRuleRef(rec.Note, "命中内置拦截规则:")
				if !ok {
					group, name = "未识别规则", clipText(rec.Note, 60)
				}
				h := base
				h.Outcome = hitPending
				ra := getRule("hard\x00"+group+"\x00"+name,
					obsRuleHits{Line: rings.DenyLineHardRule, Group: group, Name: name})
				ra.hits = append(ra.hits, &h)
				ra.byRole[rec.RequestID] = &h
			// 弹门裁决(approved / 面板拒绝 / 收尾结案):关联回命中行,
			// 不另计命中;拒绝类裁决补走向(单位死在裁决时刻,锚取裁决笔)。
			case (rec.Type == rings.ApprovalRecApproved && rec.RequestID != "") ||
				(rec.Type == rings.ApprovalRecDenied &&
					(rec.DenyLine == rings.DenyLinePanel || rec.DenyLine == rings.DenyLineCloseout)):
				var outcome string
				switch {
				case rec.Type == rings.ApprovalRecApproved:
					outcome = hitApproved
				case rec.DenyLine == rings.DenyLinePanel:
					outcome = hitDeniedPanel
				default:
					outcome = hitCloseout
				}
				for _, ra := range rules {
					h, ok := ra.byRole[rec.RequestID]
					if !ok || h.TaskID != t.ID {
						continue
					}
					h.Outcome, h.Reason = outcome, rec.Reason
					if obsHitNeedsFollow(outcome) {
						h.Follow = sc.followOf(rec.UnitID, ev.Revision)
					} else {
						h.Follow = nil
					}
					break
				}
			}
		}
	}

	list := make([]*obsRuleHits, 0, len(rules))
	totals := struct {
		Hits    int `json:"hits"`
		Denied  int `json:"denied"` // 对账口径:账本 approval.record 拒绝类(deny_line=hard_rule/intent_rule)笔数
		Gate    int `json:"gate"`
		Pending int `json:"pending"`
		Rules   int `json:"rules"`
	}{}
	for _, ra := range rules {
		if len(ra.hits) == 0 {
			continue
		}
		countOf(ra)
		sort.SliceStable(ra.hits, func(i, j int) bool {
			if !ra.hits[i].atTime.Equal(ra.hits[j].atTime) {
				return ra.hits[i].atTime.After(ra.hits[j].atTime)
			}
			return ra.hits[i].TaskID < ra.hits[j].TaskID
		})
		ra.Recent = make([]obsInterceptHit, 0, obsRecentHitsPerRule)
		for _, h := range ra.hits {
			if len(ra.Recent) < obsRecentHitsPerRule {
				ra.Recent = append(ra.Recent, *h)
			}
		}
		ra.LastAt = ra.hits[0].At
		totals.Hits += ra.Hits
		totals.Denied += ra.Denied
		totals.Gate += ra.Gate
		totals.Pending += ra.Pending
		list = append(list, &ra.obsRuleHits)
	}
	totals.Rules = len(list)
	sort.Slice(list, func(i, j int) bool {
		if list[i].Hits != list[j].Hits {
			return list[i].Hits > list[j].Hits
		}
		if list[i].Line != list[j].Line {
			return list[i].Line < list[j].Line
		}
		return list[i].Group+list[i].Name < list[j].Group+list[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"totals": totals, "rules": list})
}
