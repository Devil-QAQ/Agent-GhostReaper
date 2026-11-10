// Package server 的假设列表与详情 API(P2-03,第四章 §5 虫群直觉回流):
// 面板可见假设节点与回流轨迹边——列表(节点 + 最近评估/验证结论)+
// 详情(完整时间线:提交 → 主宰评估三去向 → 验证结论 → 状态迁移;
// 回流轨迹:发现步骤 ─produces→ 假设 ─about→ 资产、验证 Step ─verifies→
// 假设)。全部读账本确定性组装,零 LLM(R17 同源)。

package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"ghostreaper/internal/rings"
)

// hypothesisView 假设列表条目:节点投影 + 类型化边(produces 血缘/about
// 锚点/verifies 验证)+ 评估与结论审计流补足。挂账/废弃的理由(R10)
// 与验证结论是面板可见面。
type hypothesisView struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"` // candidate|testing|held|confirmed|discarded
	Statement    string   `json:"statement"`
	Basis        string   `json:"basis,omitempty"`
	AssetIDs     []string `json:"asset_ids,omitempty"`       // 资产锚点(about 边)
	Discoverer   string   `json:"discoverer_unit,omitempty"` // 提出单位
	DiscoverStep string   `json:"discover_step,omitempty"`   // 提出步骤(produces 边)
	VerifyStep   string   `json:"verify_step,omitempty"`     // 最新验证步骤(verifies 边)
	Verifier     string   `json:"verifier_unit,omitempty"`   // 验证单位(≠提出者,R12)
	Decision     string   `json:"decision,omitempty"`        // 最近评估裁决(verify|hold|discard)
	Reason       string   `json:"reason,omitempty"`          // 最近评估理由
	Verdict      string   `json:"verdict,omitempty"`         // 最近验证结论(supported|refuted|inconclusive)
	CreatedRev   int64    `json:"created_rev"`
	// AddedAt 假设立账时刻(P4-61 排序口径:nodes 投影 added_at)——假设页
	// 三区内时间倒序的比较键。
	AddedAt string `json:"added_at,omitempty"`
	// Display 四页签显示键(P4-56:验证步骤现值推导,等待验证/验证中/
	// 验证暂停/搁置/已废弃;confirmed 显示层迁入事实页,假设页不留)。
	Display string `json:"display,omitempty"`
}

// hypothesisViewOf 假设列表条目:节点投影 + 类型化边(produces 血缘/about
// 锚点/verifies 验证)+ 评估与结论审计流补足。挂账/废弃的理由(R10)
// 与验证结论是面板可见面。
// np 为假设文本包装(P4-64 ④⑥⑧:评估批次冻结的标题/依据/评估理由,
// 评估过的假设读侧优先;未评估保持节点与评估记录原文)。
func (s *Server) hypothesisViewOf(h rings.NodeFull,
	abouts, produces, verifies []rings.EdgeView, st rings.HypStateRow, np *rings.NodePolishRow) *hypothesisView {
	v := &hypothesisView{
		ID: h.ID, Status: h.Status, Statement: h.Title, CreatedRev: h.CreatedRev,
		AddedAt: h.AddedAt,
	}
	v.Basis, _ = h.Detail["basis"].(string)
	v.Discoverer, _ = h.Detail["unit_id"].(string)
	v.DiscoverStep, _ = h.Detail["step_id"].(string)
	if np != nil {
		if np.Title != "" {
			v.Statement = np.Title
		}
		if np.Basis != "" {
			v.Basis = np.Basis
		}
		if np.Reason != "" {
			v.Reason = np.Reason
		}
	}
	for _, e := range abouts {
		if e.From == h.ID {
			v.AssetIDs = append(v.AssetIDs, e.To)
		}
	}
	if v.DiscoverStep == "" {
		for _, e := range produces {
			if e.To == h.ID { // Step ─produces→ 假设
				v.DiscoverStep = e.From
				break
			}
		}
	}
	v.VerifyStep, v.Verifier = st.VerifyStep, st.Verifier
	v.Decision = st.Decision
	if v.Reason == "" { // 包装稿(P4-64 ⑧)优先,空退评估记录原文
		v.Reason = st.Reason
	}
	v.Verdict = st.Verdict
	return v
}

// handleHypothesesList 假设列表 API(GET /api/tasks/{id}/hypotheses)。
func (s *Server) handleHypothesesList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	hyps, err := s.Store.NodesByType(ctx, taskID, rings.NodeHypothesis, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	produces, err := s.Store.Edges(ctx, taskID, rings.EdgeProduces)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	verifies, err := s.Store.Edges(ctx, taskID, rings.EdgeVerifies)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// P4-36:摘要读 hyp_states 投影,列表不再扫描事件流。
	hypStates, err := s.Store.HypStates(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// P4-64 ④⑥⑧:假设文本包装(评估批次冻结)优先表。
	nodePolish, err := s.Store.NodePolish(ctx, taskID)
	if err != nil {
		nodePolish = map[string]rings.NodePolishRow{} // 读包装失败如实退原文
	}
	// P4-56 显示键推导(验证步骤现值:验证中/验证暂停等细分)。
	dc := s.loadDisplayCtx(ctx, taskID)
	out := make([]*hypothesisView, 0, len(hyps))
	for _, h := range hyps {
		np := nodePolish[h.ID]
		v := s.hypothesisViewOf(h, abouts, produces, verifies, hypStates[h.ID], &np)
		v.Display = HypDisplayFor(dc, h.ID, h.Status)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// hypTimelineEntry 假设时间线单条(按账本 revision 排序;kind 决定字段)。
type hypTimelineEntry struct {
	Rev  int64  `json:"rev"`
	Kind string `json:"kind"` // created|state|evaluation|result

	// created
	UnitID string `json:"unit_id,omitempty"`
	// evaluation
	Decision     string `json:"decision,omitempty"`
	Reason       string `json:"reason,omitempty"`
	VerifyIntent string `json:"verify_intent,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	// result
	Verdict      string `json:"verdict,omitempty"`
	EvidenceHead string `json:"evidence_head,omitempty"`
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	Note         string `json:"note,omitempty"`
	// state
	Status string `json:"status,omitempty"`
}

// verifyStepView 回流轨迹上的验证 Step 条目。
type verifyStepView struct {
	StepID  string `json:"step_id"`
	Intent  string `json:"intent"`
	Status  string `json:"status"` // dispatched|running|done|exhausted|failed|aborted
	Unit    string `json:"unit,omitempty"`
	Verdict string `json:"verdict,omitempty"` // 该步骤写回的验证结论
}

// hypDetailView 假设详情:节点 + 完整时间线 + 回流轨迹(全程类型化边)。
// RawDetail 为节点 detail 原文(P4-64 ⑤:假设抽屉"原始数据"折叠的
// 数据面——映射键(依据/单位/步骤)之外与审计原文由此可达)。
type hypDetailView struct {
	Hypothesis hypothesisView     `json:"hypothesis"`
	Timeline   []hypTimelineEntry `json:"timeline"`
	RawDetail  map[string]any     `json:"raw_detail"`
	Trajectory struct {
		DiscoverStep string           `json:"discover_step,omitempty"` // 提出步骤(produces 边 From)
		Assets       []string         `json:"assets"`                  // 资产锚点(about 边)
		VerifySteps  []verifyStepView `json:"verify_steps"`            // 验证步骤(verifies 边,按落账序)
	} `json:"trajectory"`
}

// handleHypothesisDetail 假设详情 API(GET /api/tasks/{id}/hypotheses/{hid})。
func (s *Server) handleHypothesisDetail(w http.ResponseWriter, r *http.Request) {
	taskID, hypID := r.PathValue("id"), r.PathValue("hid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	node, err := s.Store.Node(ctx, taskID, hypID)
	if err != nil || node.Type != rings.NodeHypothesis {
		writeErr(w, http.StatusNotFound, fmt.Errorf("假设 %s 不存在", hypID))
		return
	}
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	produces, err := s.Store.Edges(ctx, taskID, rings.EdgeProduces)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	verifies, err := s.Store.Edges(ctx, taskID, rings.EdgeVerifies)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// P4-58:详情只消费四类事件(假设时间线 node.added/node.state_changed
	// + hypothesis.record 审计流 + swarm.record session_started 归属),
	// 按 kind 定向直查,不再全量扫事件流。
	evs, err := s.Store.EventsByKinds(ctx, taskID,
		rings.EventNodeAdded, rings.EventNodeStateChanged,
		rings.EventHypothesisRecord, rings.EventSwarmRecord)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	hypStates, err := s.Store.HypStates(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	nodePolish, err := s.Store.NodePolish(ctx, taskID)
	if err != nil {
		nodePolish = map[string]rings.NodePolishRow{} // 读包装失败如实退原文
	}
	np := nodePolish[hypID]
	v := hypDetailView{Hypothesis: *s.hypothesisViewOf(*node, abouts, produces, verifies, hypStates[hypID], &np)}
	v.RawDetail = node.Detail
	if v.RawDetail == nil {
		v.RawDetail = map[string]any{}
	}
	v.Trajectory.Assets = []string{}
	v.Trajectory.VerifySteps = []verifyStepView{}
	for _, e := range produces {
		if e.To == hypID {
			v.Trajectory.DiscoverStep = e.From
		}
	}
	for _, e := range abouts {
		if e.From == hypID {
			v.Trajectory.Assets = append(v.Trajectory.Assets, e.To)
		}
	}
	// 验证步骤(verifies 边按 created_rev 序)+ 各步骤的执行单位与结论。
	stepUnit := map[string]string{} // swarm.record session_started 归属
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var sr rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &sr) == nil && sr.Type == rings.SwarmRecSessionStarted {
			if _, ok := stepUnit[sr.StepID]; !ok {
				stepUnit[sr.StepID] = sr.UnitID
			}
		}
	}
	stepVerdict := map[string]string{}
	for _, ev := range evs {
		if ev.Kind != rings.EventHypothesisRecord {
			continue
		}
		var hr rings.HypothesisRecord
		if json.Unmarshal(ev.Payload, &hr) == nil && hr.Type == rings.HypothesisRecResult {
			stepVerdict[hr.StepID] = hr.Verdict
		}
	}
	for _, e := range verifies {
		if e.To != hypID {
			continue
		}
		sv := verifyStepView{StepID: e.From, Status: "?", Verdict: stepVerdict[e.From]}
		if n, err := s.Store.Node(ctx, taskID, e.From); err == nil {
			sv.Intent, sv.Status = n.Title, n.Status
		}
		sv.Unit = stepUnit[e.From]
		v.Trajectory.VerifySteps = append(v.Trajectory.VerifySteps, sv)
	}

	// 时间线:created/state + hypothesis.record 审计流,一遍扫。
	// P4-64 ⑧:最新一条评估条目的理由替换为包装稿(评估批次冻结)——
	// 历史评估条目保持原文(包装只跟随最新评估)。
	lastEvalRev := int64(0)
	for _, ev := range evs {
		if ev.Kind != rings.EventHypothesisRecord {
			continue
		}
		var p rings.HypothesisRecord
		if json.Unmarshal(ev.Payload, &p) == nil && p.HypothesisID == hypID && p.Type == rings.HypothesisRecEvaluation {
			lastEvalRev = ev.Revision
		}
	}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == hypID {
				v.Timeline = append(v.Timeline, hypTimelineEntry{
					Rev: ev.Revision, Kind: "created", UnitID: v.Hypothesis.Discoverer,
				})
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == hypID {
				v.Timeline = append(v.Timeline, hypTimelineEntry{Rev: ev.Revision, Kind: "state", Status: p.Status})
			}
		case rings.EventHypothesisRecord:
			var p rings.HypothesisRecord
			if err := json.Unmarshal(ev.Payload, &p); err != nil || p.HypothesisID != hypID {
				continue
			}
			e := hypTimelineEntry{Rev: ev.Revision}
			switch p.Type {
			case rings.HypothesisRecEvaluation:
				e.Kind = "evaluation"
				e.Decision, e.Reason, e.VerifyIntent, e.StepID = p.Decision, p.Reason, p.VerifyIntent, p.StepID
				if ev.Revision == lastEvalRev && np.Reason != "" {
					e.Reason = np.Reason
				}
			case rings.HypothesisRecResult:
				e.Kind = "result"
				e.Verdict, e.StepID, e.UnitID = p.Verdict, p.StepID, p.UnitID
				e.EvidenceHead, e.EvidenceRef, e.Note = p.EvidenceHead, p.EvidenceRef, p.Note
			}
			v.Timeline = append(v.Timeline, e)
		}
	}
	writeJSON(w, http.StatusOK, v)
}
