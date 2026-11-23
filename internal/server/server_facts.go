package server

import (
	"fmt"
	"net/http"

	"ghostreaper/internal/rings"
)

// factAssetView 事实条目上的资产锚点(about 边 Fact→Asset)。
type factAssetView struct {
	Address string `json:"address,omitempty"`
	ID      string `json:"id"`
	Title   string `json:"title"`
}

// factRow 事实页签条目:节点投影 + 成果类 + 资产锚点 + 确认时刻(自
// 审计流取 occurred_at,确定性口径同报告——不取读侧墙钟)。
type factRow struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title,omitempty"`
	Statement    string                 `json:"statement"`
	ResultTypes  []rings.FactResultType `json:"result_types"`
	Assets       []factAssetView        `json:"assets"`
	UnitID       string                 `json:"unit_id,omitempty"`
	EvidenceHead string                 `json:"evidence_head,omitempty"`
	EvidenceRef  string                 `json:"evidence_ref,omitempty"`
	CreatedRev   int64                  `json:"created_rev"`
	ConfirmedRev int64                  `json:"confirmed_rev"`
	CreatedAt    string                 `json:"created_at,omitempty"`
	ConfirmedAt  string                 `json:"confirmed_at,omitempty"`
}

// handleFactsList 事实页签列表(GET /api/tasks/{id}/facts):已确认事实
// 一等列表;入账序返回,面板按确认时刻倒序取读(与虫群单位列表同口径)。
// P4-36:确认时刻读 nodes 投影列(added_at/confirmed_at 由写侧同事务
// 维护)——不再扫描事件流。
func (s *Server) handleFactsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	facts, err := s.Store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	assetTitle := map[string]string{}
	if ag, err := s.Store.AssetGraph(ctx, taskID); err == nil {
		for _, a := range ag.Assets {
			assetTitle[a.ID] = a.Title
		}
	}
	// 定稿校对后的事实描述优先表(P4-59 ①,问题清单 #80①③):反查自
	// 各判断的声明摘要(claim_cases.summary 小列),后定稿覆盖先定稿;
	// 未被任何确认判断携带的事实无校对稿,退节点原文(其陈述受生成时
	// 结构化字段约束)。
	// P4-64 ③(P4-64 ⑥ 同族):事实确认批次文本(node_polish)优先于判断
	// 摘要反查——收割确认时生成,不限于已定稿判断链上的事实;结果类型
	// 另读事实自身的结构化记录，显示稿不改变分类。
	polishedFact := map[string]string{}
	if digests, derr := s.Store.ClaimDigests(ctx, taskID); derr == nil {
		polishedFact = polishedFactMapOf(digests)
	}
	s.overlayFactPolish(ctx, taskID, polishedFact)

	titles := s.polishedNodeTitleOf(ctx, taskID)
	out := make([]factRow, 0, len(facts))
	for _, f := range facts {
		row := factRow{
			ID: f.ID, Title: titles[f.ID], Statement: f.Title, ResultTypes: rings.FactResultsOf(f),
			CreatedRev: f.CreatedRev, Assets: []factAssetView{},
		}
		if p := polishedFact[f.ID]; p != "" {
			row.Statement = p
		}
		row.CreatedAt = f.AddedAt
		row.ConfirmedAt = f.AddedAt
		if f.ConfirmedAt != "" {
			row.ConfirmedAt, row.ConfirmedRev = f.ConfirmedAt, f.ConfirmedRev
		}
		row.UnitID, _ = f.Detail["unit_id"].(string)
		row.EvidenceHead, _ = f.Detail["evidence_head"].(string)
		row.EvidenceRef, _ = f.Detail["evidence_ref"].(string)
		if row.EvidenceHead == "" {
			row.EvidenceHead, _ = f.Detail["evidence"].(string)
		}
		for _, e := range abouts {
			if e.From == f.ID {
				row.Assets = append(row.Assets, factAssetView{ID: e.To, Title: assetTitle[e.To]})
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"facts": out})
}
