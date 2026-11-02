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
}

// hypListView 输出 view 或 nil(列表项可复用)。
func (s *Server) hypothesisViewOf(h rings.NodeFull,
	abouts, produces, verifies []rings.EdgeView, evs []rings.RawEvent) *hypothesisView {
	v := &hypothesisView{
		ID: h.ID, Status: h.Status, Statement: h.Title, CreatedRev: h.CreatedRev,
	}
	v.Basis, _ = h.Detail["basis"].(string)
	v.Discoverer, _ = h.Detail["unit_id"].(string)
	v.DiscoverStep, _ = h.Detail["step_id"].(string)
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
	var latestEval *rings.HypothesisRecord
	var latestResult *rings.HypothesisRecord
	for i := range evs {
		if evs[i].Kind != rings.EventHypothesisRecord {
			continue
		}
		var r rings.HypothesisRecord
		if json.Unmarshal(evs[i].Payload, &r) != nil || r.HypothesisID != h.ID {
			continue
		}
		switch r.Type {
		case rings.HypothesisRecEvaluation:
			latestEval = &r
			if r.StepID != "" {
				v.VerifyStep = r.StepID
			}
		case rings.HypothesisRecResult:
			latestResult = &r
			v.VerifyStep, v.Verifier = r.StepID, r.UnitID
		}
	}
	if latestEval != nil {
		v.Decision, v.Reason = latestEval.Decision, latestEval.Reason
	}
	if latestResult != nil {
		v.Verdict = latestResult.Verdict
	}
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
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]*hypothesisView, 0, len(hyps))
	for _, h := range hyps {
		out = append(out, s.hypothesisViewOf(h, abouts, produces, verifies, evs))
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
type hypDetailView struct {
	Hypothesis hypothesisView     `json:"hypothesis"`
	Timeline   []hypTimelineEntry `json:"timeline"`
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
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	v := hypDetailView{Hypothesis: *s.hypothesisViewOf(*node, abouts, produces, verifies, evs)}
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
