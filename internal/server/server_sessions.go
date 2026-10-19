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
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/failover"
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

// OvermindRound 一轮研判:五问结论 + 派发决策 + 本轮收割。收割与派发
// 从"本轮研判所读版本 → 下一轮研判所读版本"窗口内的账本变化派生
// (末轮开到当前):该窗口正是本轮研判之后、下一轮研判之前的全部
// 落账——新增步骤(意图 / 反证 / 验证)、候选事实转正、声明仲裁
// 裁决、假设收割裁决。
type OvermindRound struct {
	At             string      `json:"at"`
	SessionID      string      `json:"session_id,omitempty"`
	EvaluatedRev   int64       `json:"evaluated_rev,omitempty"`
	QAccess        string      `json:"q_access,omitempty"`
	QPrivilege     string      `json:"q_privilege,omitempty"`
	QCombination   string      `json:"q_combination,omitempty"`
	QUnlock        string      `json:"q_unlock,omitempty"`
	QOverturn      string      `json:"q_overturn,omitempty"`
	Note           string      `json:"note,omitempty"`
	Dispatched     int         `json:"dispatched,omitempty"`
	Combined       []string    `json:"combined,omitempty"`
	Steps          []RoundNode `json:"steps,omitempty"`
	FactsConfirmed []string    `json:"facts_confirmed,omitempty"`
	ClaimsJudged   []RoundNode `json:"claims_judged,omitempty"`
	Hypotheses     []RoundNode `json:"hypotheses,omitempty"`
}

// overmindRounds 主宰研判轨迹:按账本顺序取全部 overmind.record
// (assessment),逐轮派生窗口内的派发与收割。
func (s *Server) overmindRounds(r *http.Request, taskID string) ([]OvermindRound, error) {
	evs, err := s.Store.Events(r.Context(), taskID, 0, 0)
	if err != nil {
		return nil, err
	}
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
		rounds = append(rounds, OvermindRound{
			At:        ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
			SessionID: rec.SessionID, EvaluatedRev: rec.EvaluatedRev,
			QAccess: rec.QAccess, QPrivilege: rec.QPrivilege, QCombination: rec.QCombination,
			QUnlock: rec.QUnlock, QOverturn: rec.QOverturn, Note: rec.Note,
			Dispatched: rec.Dispatched, Combined: rec.Combined,
		})
	}
	if len(rounds) == 0 {
		return []OvermindRound{}, nil
	}
	// 第二遍:窗口派生(派发的新增步骤 + 收割的状态迁移)。窗口 =
	// (本轮研判版本, 下一轮研判版本];连续两轮读同一版本时窗口为空,
	// 推进逻辑按"事件版本越过窗口上界即进下一轮"处理。
	i := 0
	for _, ev := range evs {
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
