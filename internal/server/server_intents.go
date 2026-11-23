package server

import (
	"fmt"
	"net/http"

	"ghostreaper/internal/rings"
)

// intentOutcome 意图行尾的一个下文标记:产出节点引用 + 台账词。
type intentOutcome struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // fact|testclaim(抽屉分发键)
	Word string `json:"word"` // 事实|漏洞(行尾标记词)
}

// intentRow 意图页签条目(节点投影 + 下文标记)。AddedAt=提出时刻
// (nodes 投影 added_at,写侧同事务维护),面板按其倒序。
type intentRow struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Assets     []factAssetView `json:"assets,omitempty"`
	AddedAt    string          `json:"added_at,omitempty"`
	CreatedRev int64           `json:"created_rev"`
	Outcomes   []intentOutcome `json:"outcomes,omitempty"`
}

// handleIntentsList 意图页签列表(GET /api/tasks/{id}/intents):全部
// 步骤(每一个意图,不限状态);入账序返回,面板落页时间倒序取读。
func (s *Server) handleIntentsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// 漏洞下文:漏洞页可见显示键(confirmed/fixed/insufficient)的声明,
	// 归属意图 = 交换单的发现步骤(detail.step_id 或 derived_from 边)。
	claims, err := s.Store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	derived, err := s.Store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	claimStep := map[string]string{} // claimID → stepID
	for _, c := range claims {
		key, _ := rings.ClaimDisplay(c.Status)
		switch key {
		case rings.ClaimDisplayConfirmed, rings.ClaimDisplayFixed, rings.ClaimDisplayInsufficient:
		default:
			continue
		}
		stepID, _ := c.Detail["step_id"].(string)
		if stepID == "" {
			for _, e := range derived {
				if e.From == c.ID {
					if n, nerr := s.Store.Node(ctx, taskID, e.To); nerr == nil && n.Type == rings.NodeStep {
						stepID = e.To
						break
					}
				}
			}
		}
		if stepID != "" {
			claimStep[c.ID] = stepID
		}
	}

	facts, err := s.Store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	produces, err := s.Store.Edges(ctx, taskID, rings.EdgeProduces)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	factStep := map[string]string{} // factID → stepID
	for _, f := range facts {
		for _, e := range produces {
			if e.To == f.ID {
				if n, nerr := s.Store.Node(ctx, taskID, e.From); nerr == nil && n.Type == rings.NodeStep {
					factStep[f.ID] = e.From
					break
				}
			}
		}
		if _, ok := factStep[f.ID]; ok {
			continue
		}
	}

	covered, err := s.Store.Edges(ctx, taskID, rings.EdgeCovers)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	assetGraph, err := s.Store.AssetGraph(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	addresses := map[string]string{}
	for _, asset := range assetGraph.Assets {
		addresses[asset.ID] = asset.Title
	}
	assets := map[string][]factAssetView{}
	for _, e := range covered {
		assets[e.From] = append(assets[e.From], factAssetView{ID: e.To, Title: addresses[e.To]})
	}
	// 与图、虫群列表共用已入账显示稿；执行原文留在节点与任务包。
	titles := s.polishedNodeTitleOf(ctx, taskID)
	out := make([]intentRow, 0, len(steps))
	seen := map[string]map[string]bool{}
	for _, st := range steps {
		// 机器拉起的补考/重验步骤(kind=retake)不是主宰提出的意图方向,
		// 不入台账(其产出经交卷单血缘归位到原意图)。
		if k, _ := st.Detail["kind"].(string); k == "retake" {
			continue
		}
		row := intentRow{ID: st.ID, Title: st.Title, Assets: assets[st.ID], AddedAt: st.AddedAt, CreatedRev: st.CreatedRev}
		if title := titles[st.ID]; title != "" {
			row.Title = title
		}
		for claimID, sid := range claimStep {
			if sid != st.ID {
				continue
			}
			if seen[st.ID] == nil {
				seen[st.ID] = map[string]bool{}
			}
			if !seen[st.ID]["c:"+claimID] {
				seen[st.ID]["c:"+claimID] = true
				row.Outcomes = append(row.Outcomes, intentOutcome{ID: claimID, Kind: "testclaim", Word: "漏洞"})
			}
		}
		for factID, sid := range factStep {
			if sid != st.ID {
				continue
			}
			if seen[st.ID] == nil {
				seen[st.ID] = map[string]bool{}
			}
			if !seen[st.ID]["f:"+factID] {
				seen[st.ID]["f:"+factID] = true
				row.Outcomes = append(row.Outcomes, intentOutcome{ID: factID, Kind: "fact", Word: "事实"})
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}
