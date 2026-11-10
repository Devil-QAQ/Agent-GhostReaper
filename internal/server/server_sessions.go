// Package server 的会话页数据面(P2-08b,8.5 Agent 会话页):主宰每轮
// 研判轨迹(五问 / 收割 / 派发决策,账本派生)+ 会话原文 JSONL 读
// (单位执行轨迹与研判原文下钻的统一入口)+ 会话信息条数据(主宰 /
// 单位,口径同牧者)。零新增采集——全部读既有账本事件、既有 token
// 独立表与既有转录文件。
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// ---- 会话原文读(转录 JSONL → 结构化消息流) ----

// transcriptSIDRe 会话标识白名单:仅字母数字 / 下划线 / 连字符(转录
// 命名 swarm-* / overmind-* / interject-* / shepherd-* 全在此列;从根上
// 排除路径穿越)。
var transcriptSIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TranscriptTool 一次工具调用(assistant 侧)。
type TranscriptTool struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

// TranscriptResult 一次工具结果(user 消息携带)。
type TranscriptResult struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

// TranscriptEntry 转录中的一条呈现单元:user / assistant 消息(块分解:
// 文本 / 思考 / 工具调用 / 工具结果)或压缩边界。
type TranscriptEntry struct {
	Kind       string             `json:"kind"` // user|assistant|boundary
	At         string             `json:"at,omitempty"`
	Text       string             `json:"text,omitempty"`
	Thinking   string             `json:"thinking,omitempty"`
	Tools      []TranscriptTool   `json:"tools,omitempty"`
	Results    []TranscriptResult `json:"results,omitempty"`
	PreTokens  int                `json:"pre_tokens,omitempty"` // boundary:压缩前 token
	Summarized int                `json:"summarized,omitempty"` // boundary:被压缩消息数
}

// handleTranscriptGet 会话原文读:按转录标识读任务的会话 JSONL 并分解
// 为消息流(虫群单位执行轨迹、主宰研判原文下钻、插话子会话原文共用)。
// 文件不存在如实呈现 exists=false(单位尚未动工 / 未落转录)。
func (s *Server) handleTranscriptGet(w http.ResponseWriter, r *http.Request) {
	taskID, sid := r.PathValue("id"), r.PathValue("sid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if !transcriptSIDRe.MatchString(sid) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("会话标识 %q 非法", sid))
		return
	}
	entries := []TranscriptEntry{}
	exists := false
	if s.RootDir != "" {
		tstore := transcript.NewStore(filepath.Join(s.RootDir, taskID, "transcripts"))
		path := tstore.MainPath(sid)
		if _, err := os.Stat(path); err == nil {
			recs, err := tstore.Load(path)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("读取会话 %s: %w", sid, err))
				return
			}
			exists = true
			entries = transcriptEntries(recs)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sid, "exists": exists, "entries": entries,
	})
}

// transcriptEntries 转录记录 → 呈现单元。消息按 role 归并为 user /
// assistant 两条形态:文本块拼接、思考块拼接、tool_use 逐条成卡、
// tool_result 归入所在 user 消息;boundary(压缩边界)独立成行。
// 超长截断(任务包与正文 16000 / 思考 4000 / 入参 2000 / 结果 4000)。
func transcriptEntries(recs []transcript.Record) []TranscriptEntry {
	out := []TranscriptEntry{}
	for _, rec := range recs {
		at := recAt(rec.Timestamp)
		if rec.Type == "boundary" && rec.Boundary != nil {
			out = append(out, TranscriptEntry{Kind: "boundary", At: at,
				PreTokens: rec.Boundary.PreTokens, Summarized: rec.Boundary.MessagesSummarized})
			continue
		}
		m := rec.Message
		if m == nil {
			continue
		}
		e := TranscriptEntry{Kind: string(m.Role), At: at}
		var texts, thinks []string
		for _, blk := range m.Content {
			switch blk.Type {
			case llm.BlockText:
				if t := strings.TrimSpace(blk.Text); t != "" {
					texts = append(texts, t)
				}
			case llm.BlockThinking:
				if t := strings.TrimSpace(blk.Thinking); t != "" {
					thinks = append(thinks, t)
				}
			case llm.BlockToolUse:
				e.Tools = append(e.Tools, TranscriptTool{
					ID: blk.ID, Name: blk.Name, Input: clipText(string(blk.Input), 2000),
				})
			case llm.BlockToolResult:
				e.Results = append(e.Results, TranscriptResult{
					ID: blk.ToolUseID, Text: clipText(flattenBlocks(blk.Content), 4000),
					IsError: blk.IsError,
				})
			}
		}
		e.Text, e.Thinking = clipText(strings.Join(texts, "\n"), 16000), clipText(strings.Join(thinks, "\n"), 4000)
		if e.Text == "" && e.Thinking == "" && len(e.Tools) == 0 && len(e.Results) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// recAt 转录时间戳 → 本地呈现格式(与账本事件侧 at 同一格式,会话页
// 按时间归并排序用)。解析失败原样返回。
func recAt(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return ts
}

// flattenBlocks 工具结果块文本拼合(同 interject.view)。
func flattenBlocks(bs []llm.ContentBlock) string {
	var b strings.Builder
	for _, blk := range bs {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// clipText 超长截断(带省略号)。
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- 主宰研判轨迹(账本派生,零新增采集) ----

// RoundNode 研判窗口内的一枚图上节点(步骤 / 声明 / 假设),状态取
// 当前投影(派发时的决策 + 走到哪了,一眼可对)。
type RoundNode struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

// PanoramaAsset 在途全景行的目标归属资产(covers 边的资产端)。
type PanoramaAsset struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// PanoramaRow 研判轮在途全景行(P4-11 ②,问题清单 #19):按该轮研判
// 所读账本版本回放的图状态确定性派生,零 LLM——该时刻全部在跑与排队
// 步骤,等前置如实标注,排序与领取次序同源(overmind.StepLaunchLess,
// P4-08 同一比较器口径)。研判叙述仍由模型自由写——表是机器的、叙述
// 是模型的,互补不重替。
type PanoramaRow struct {
	ID      string          `json:"id"`
	Title   string          `json:"title,omitempty"`   // 意图摘要(步骤标题)
	Status  string          `json:"status"`            // running | dispatched
	Waiting bool            `json:"waiting,omitempty"` // 等前置(排队且前置未全完成,与依赖闸同口径)
	Assets  []PanoramaAsset `json:"assets,omitempty"`  // 目标归属(covers 资产)
}

// panoramaReplay 在途全景的账本回放器(P4-11 ②):按事件序增量维护
// 步骤/资产标题/覆盖边/依赖边,在任意研判版本处定格快照(版本号快照,
// 不读当前投影)。
type panoramaReplay struct {
	steps      map[string]rings.NodeFull
	assetTitle map[string]string
	covers     map[string][]string // stepID → covers 资产
	deps       map[string][]string // stepID → 前置步骤
}

func newPanoramaReplay() *panoramaReplay {
	return &panoramaReplay{
		steps: map[string]rings.NodeFull{}, assetTitle: map[string]string{},
		covers: map[string][]string{}, deps: map[string][]string{},
	}
}

// apply 喂入一条账本事件(只认节点/边三类,其余研判流事件与全景无关)。
func (rep *panoramaReplay) apply(ev rings.RawEvent) {
	switch ev.Kind {
	case rings.EventNodeAdded:
		var p rings.NodeAdded
		if json.Unmarshal(ev.Payload, &p) != nil {
			return
		}
		switch p.NodeType {
		case rings.NodeStep:
			rep.steps[p.NodeID] = rings.NodeFull{
				NodeView: rings.NodeView{ID: p.NodeID, Type: p.NodeType, Title: p.Title,
					Status: p.Status, CreatedRev: ev.Revision},
				Detail: p.Detail,
			}
		case rings.NodeAsset:
			rep.assetTitle[p.NodeID] = p.Title
		}
	case rings.EventNodeStateChanged:
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) != nil {
			return
		}
		if st, ok := rep.steps[p.NodeID]; ok {
			st.Status = p.Status
			rep.steps[p.NodeID] = st
		}
	case rings.EventEdgeAdded:
		var p rings.EdgeAdded
		if json.Unmarshal(ev.Payload, &p) != nil {
			return
		}
		switch p.EdgeType {
		case rings.EdgeCovers:
			rep.covers[p.FromNode] = append(rep.covers[p.FromNode], p.ToNode)
		case rings.EdgeDependsOn:
			rep.deps[p.FromNode] = append(rep.deps[p.FromNode], p.ToNode)
		}
	}
}

// panoramaRows 在途全景行装配(历史快照与实时视图同一语义,P4-46 ①):
// 在跑+排队步骤按领取次序排(调度器同一比较器,面板次序即可核对领取
// 次序),等前置如实标注,行带 covers 资产归属。panoramaReplay 的按轮
// 快照与 handlePanoramaLive 的当前投影实时视图共用本装配,两视图不漂移。
// titlePolish 为步骤意图标题校对稿优先表(P4-65 ⑩:空表零变化——节点
// 标题原文;与 RoundNode 同一口径,任务包与机器消费不走本装配)。
func panoramaRows(steps map[string]rings.NodeFull, covers, deps map[string][]string, assetTitle map[string]string, titlePolish map[string]string) []PanoramaRow {
	var flight []rings.NodeFull
	for _, st := range steps {
		if st.Status == "running" || st.Status == "dispatched" {
			flight = append(flight, st)
		}
	}
	sort.Slice(flight, func(i, j int) bool { return overmind.StepLaunchLess(flight[i], flight[j]) })
	rows := make([]PanoramaRow, 0, len(flight))
	for _, st := range flight {
		title := st.Title
		if t := titlePolish[st.ID]; t != "" {
			title = t
		}
		row := PanoramaRow{ID: st.ID, Title: title, Status: st.Status}
		for _, dep := range deps[st.ID] {
			if d, ok := steps[dep]; !ok || d.Status != "done" { // 前置未完成/不存在:一律等待(依赖闸同口径)
				row.Waiting = true
				break
			}
		}
		for _, a := range covers[st.ID] {
			row.Assets = append(row.Assets, PanoramaAsset{ID: a, Title: assetTitle[a]})
		}
		rows = append(rows, row)
	}
	return rows
}

// snapshot 定格当前回放状态的在途全景(历史轮次卡的快照数据面)。
func (rep *panoramaReplay) snapshot(titlePolish map[string]string) []PanoramaRow {
	return panoramaRows(rep.steps, rep.covers, rep.deps, rep.assetTitle, titlePolish)
}

// handlePanoramaLive 在途全景实时视图(P4-46 ①):当前投影的执行中/
// 排队(含等前置)步骤——与虫群会话列表排队条目、工作台水位同源(同一
// 账本投影,排序同一 StepLaunchLess),行装配与研判卡历史快照共用
// panoramaRows。最新研判卡的实时数据面(历史轮次快照仍归 overmindRounds);
// 读面只走投影直查(P4-58:在跑/排队步骤 + covers 资产标题 + 前置状态,
// Store.PanoramaFlight,不再全图重建),供面板 tasks 主题事件驱动高频
// 刷新,不扫事件流。
func (s *Server) handlePanoramaLive(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	pl, err := s.Store.PanoramaFlight(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"panorama": panoramaRows(pl.Steps, pl.Covers, pl.Deps, pl.AssetTitle, s.polishedNodeTitleOf(r.Context(), taskID)),
	})
}

// OvermindRound 一轮研判:五问结论 + 派发决策 + 本轮收割 + 在途全景。
// 收割与派发从"本轮研判所读版本 → 下一轮研判所读版本"窗口内的账本
// 变化派生(末轮开到当前):该窗口正是本轮研判之后、下一轮研判之前
// 的全部落账——新增步骤(意图 / 反证 / 验证)、候选事实转正、声明仲裁
// 裁决、假设收割裁决。全景(P4-11 ②)按本轮研判所读版本回放定格。
type OvermindRound struct {
	At             string                `json:"at"`
	SessionID      string                `json:"session_id,omitempty"`
	EvaluatedRev   int64                 `json:"evaluated_rev,omitempty"`
	Triggers       []string              `json:"triggers,omitempty"` // 触发来源(P4-48 ④:深入指令人话行;其余触发在账本可溯)
	QAccess        string                `json:"q_access,omitempty"`
	QPrivilege     string                `json:"q_privilege,omitempty"`
	QCombination   string                `json:"q_combination,omitempty"`
	QUnlock        string                `json:"q_unlock,omitempty"`
	QOverturn      string                `json:"q_overturn,omitempty"`
	Note           string                `json:"note,omitempty"`
	Queue          *rings.QueueWatermark `json:"queue,omitempty"`
	Dispatched     int                   `json:"dispatched,omitempty"`
	Combined       []string              `json:"combined,omitempty"`
	Steps          []RoundNode           `json:"steps,omitempty"`
	FactsConfirmed []string              `json:"facts_confirmed,omitempty"`
	ClaimsJudged   []RoundNode           `json:"claims_judged,omitempty"`
	Hypotheses     []RoundNode           `json:"hypotheses,omitempty"`
	Panorama       []PanoramaRow         `json:"panorama"`
}

// diveTriggerLines 本轮触发版本里的操作者深入指令行(P4-48 ④:研判记录
// 体现触发来源,确定性装配零 LLM);方向留空如实呈"留空"。
func diveTriggerLines(evs []rings.RawEvent, revs []int64) []string {
	want := map[int64]bool{}
	for _, r := range revs {
		want[r] = true
	}
	var out []string
	for _, ev := range evs {
		if !want[ev.Revision] || ev.Kind != rings.EventOperatorDive {
			continue
		}
		var p rings.OperatorDive
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		dir := "留空"
		if d := strings.TrimSpace(p.Direction); d != "" {
			dir = d
		}
		out = append(out, fmt.Sprintf("操作者深入指令：节点 %s，方向：%s", p.NodeID, dir))
	}
	return out
}

// overmindRounds 主宰研判轨迹:按账本顺序取全部 overmind.record
// (assessment),逐轮派生窗口内的派发与收割。P4-58:研判轨迹回放只
// 消费五类事件(overmind.record / operator.dive / node.added /
// node.state_changed / edge.added),按 kind 定向直查合并(revision
// 升序 = 账本权威序,回放语义不变),不再全量扫事件流。P4-65 ⑨⑩:
// 额外读 overmind.record_polish(研判叙述校对稿,按 rec_rev 锚定逐轮
// 匹配)与 node_polish 步骤标题(派发 chip 与全景行显示层优先;研判
// 记录与节点原文一字不动,会话原文下钻走原文域)。
func (s *Server) overmindRounds(r *http.Request, taskID string) ([]OvermindRound, error) {
	evs, err := s.Store.EventsByKinds(r.Context(), taskID,
		rings.EventOvermindRecord, rings.EventOperatorDive, rings.EventNodeAdded,
		rings.EventNodeStateChanged, rings.EventEdgeAdded, rings.EventOvermindPolish)
	if err != nil {
		return nil, err
	}
	// 研判叙述校对稿优先表(P4-65 ⑨:rec_rev → 校对稿;研判记录不可变
	// 故逐版本唯一,后到覆盖同键即为重放确定性)。
	assessPolish := map[int64]rings.OvermindPolish{}
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindPolish {
			continue
		}
		var p rings.OvermindPolish
		if json.Unmarshal(ev.Payload, &p) == nil && p.RecRev > 0 {
			assessPolish[p.RecRev] = p
		}
	}
	// 步骤意图标题校对稿优先表(P4-65 ⑩:与图节点卡/虫群列表同一口径)。
	stepTitlePolish := s.polishedNodeTitleOf(r.Context(), taskID)
	// 节点类型预建(全量:种子资产 / 虫群提交的声明与假设多在首轮研判
	// 前入账,窗口内的状态迁移要靠它认类型)+ 步骤当前投影。
	nodeType := map[string]rings.NodeType{}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded {
			continue
		}
		var p rings.NodeAdded
		if json.Unmarshal(ev.Payload, &p) == nil {
			nodeType[p.NodeID] = p.NodeType
		}
	}
	stepsNow := map[string]rings.NodeFull{}
	if list, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeStep, ""); err == nil {
		for _, st := range list {
			stepsNow[st.ID] = st
		}
	}
	var rounds []OvermindRound
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var rec rings.OvermindRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.OvermindRecAssessment {
			continue
		}
		round := OvermindRound{
			At:        ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
			SessionID: rec.SessionID, EvaluatedRev: rec.EvaluatedRev,
			Triggers: diveTriggerLines(evs, rec.TriggerRevisions),
			QAccess:  rec.QAccess, QPrivilege: rec.QPrivilege, QCombination: rec.QCombination,
			QUnlock: rec.QUnlock, QOverturn: rec.QOverturn, Note: rec.Note,
			Dispatched: rec.Dispatched, Combined: rec.Combined, Queue: rec.Queue,
		}
		// P4-65 ⑨:研判叙述读侧优先校对稿(字段级——未生成/未过闸的字段
		// 退研判记录原文)。
		if p, ok := assessPolish[ev.Revision]; ok {
			if p.QAccess != "" {
				round.QAccess = p.QAccess
			}
			if p.QPrivilege != "" {
				round.QPrivilege = p.QPrivilege
			}
			if p.QCombination != "" {
				round.QCombination = p.QCombination
			}
			if p.QUnlock != "" {
				round.QUnlock = p.QUnlock
			}
			if p.QOverturn != "" {
				round.QOverturn = p.QOverturn
			}
			if p.Note != "" {
				round.Note = p.Note
			}
		}
		rounds = append(rounds, round)
	}
	if len(rounds) == 0 {
		return []OvermindRound{}, nil
	}
	// 第二遍:窗口派生(派发的新增步骤 + 收割的状态迁移)+ 在途全景
	// 快照(P4-11 ②)。窗口 = (本轮研判版本, 下一轮研判版本];连续两轮
	// 读同一版本时窗口为空,推进逻辑按"事件版本越过窗口上界即进下一轮"
	// 处理。全景按回放状态在"事件版本越过该轮研判版本"前定格(已应用
	// 全部 ≤ 该版本的事件,版本号快照零 LLM)。
	i := 0
	rep := newPanoramaReplay()
	snap := make([]bool, len(rounds))
	for _, ev := range evs {
		for r := range rounds {
			if !snap[r] && ev.Revision > rounds[r].EvaluatedRev {
				rounds[r].Panorama = rep.snapshot(stepTitlePolish)
				snap[r] = true
			}
		}
		rep.apply(ev)
		for i < len(rounds)-1 && ev.Revision > rounds[i+1].EvaluatedRev {
			i++
		}
		if ev.Revision <= rounds[i].EvaluatedRev {
			continue
		}
		round := &rounds[i]
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			nodeType[p.NodeID] = p.NodeType
			if p.NodeType == rings.NodeStep {
				st := RoundNode{ID: p.NodeID, Title: p.Title}
				if t := stepTitlePolish[p.NodeID]; t != "" {
					st.Title = t
				}
				if now, ok := stepsNow[p.NodeID]; ok {
					st.Status = now.Status
					if st.Title == "" {
						st.Title = now.Title
					}
				}
				round.Steps = append(round.Steps, st)
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch nodeType[p.NodeID] {
			case rings.NodeFact:
				if p.Status == "confirmed" {
					round.FactsConfirmed = append(round.FactsConfirmed, p.NodeID)
				}
			case rings.NodeTestClaim:
				// 仲裁裁决迁移(confirmed / pending / awaiting_round);
				// 主宰派反证的 testing 迁移不在此列(反证步骤见 Steps)。
				if ev.Actor == "arbiter" {
					round.ClaimsJudged = append(round.ClaimsJudged,
						RoundNode{ID: p.NodeID, Status: p.Status})
				}
			case rings.NodeHypothesis:
				if ev.Actor == "overmind" {
					round.Hypotheses = append(round.Hypotheses,
						RoundNode{ID: p.NodeID, Status: p.Status})
				}
			}
		}
	}
	// 尾部:研判版本 ≥ 账本最新版本的轮(末轮常态),用终态回放定格。
	for r := range rounds {
		if !snap[r] {
			rounds[r].Panorama = rep.snapshot(stepTitlePolish)
		}
	}
	return rounds, nil
}

// ---- 会话信息条(主宰 / 单位,口径同牧者:token 独立表求和 + 最近请求) ----

// sessionInfoBar 会话顶部信息条数据(主宰研判会话与虫群单位会话共用;
// 字段口径与牧者 / 插话完全一致,四项与 token 独立表同源对账)。
type sessionInfoBar struct {
	Model         string  `json:"model,omitempty"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CacheTokens   int     `json:"cache_tokens"`
	ContextWindow int     `json:"context_window,omitempty"`
	ContextRatio  float64 `json:"context_ratio,omitempty"`
	Calls         int     `json:"calls"`
}

// agentInfoBy 按 token 行过滤条件聚合信息条(match 为 true 的行计入;
// 口径:一行 = 一次模型调用,比例 = 最近请求 input ÷ 窗口)。
func (s *Server) agentInfoBy(r *http.Request, taskID string, match func(rings.TokenUsage) bool) sessionInfoBar {
	var info sessionInfoBar
	rows, err := s.Store.TokenUsageRows(r.Context(), taskID)
	if err != nil {
		return info
	}
	lastModel, lastInput := "", 0
	for _, x := range rows {
		if !match(x) {
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

// overmindInfo 主宰信息条:agent=overmind 的全部研判 / 拆解会话合计
// (每次拆解独立编号会话,任务维度合计即主宰的会话成本)。
func (s *Server) overmindInfo(r *http.Request, taskID string) sessionInfoBar {
	return s.agentInfoBy(r, taskID, func(x rings.TokenUsage) bool { return x.Agent == "overmind" })
}
