// Package server 的事实列表 API(P2-10,8.3 发现域·事实页签):已确认
// 事实的一等列表——成果类(凭据/数据/权限/立足点,report.LootClass
// 确定性查表)、资产锚点、入账/确认时刻一次发齐,面板按类型/资产/时间
// 筛选;与图上事实节点经跳转协议互跳(?focus= 定位)。

package server

import (
	"fmt"
	"net/http"

	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
)

// factAssetView 事实条目上的资产锚点(about 边 Fact→Asset)。
type factAssetView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// factRow 事实页签条目:节点投影 + 成果类 + 资产锚点 + 时刻(自审计流
// 取 occurred_at,确定性口径同报告——不取读侧墙钟)。
type factRow struct {
	ID           string          `json:"id"`
	Statement    string          `json:"statement"`
	Loot         string          `json:"loot,omitempty"` // credential|data|permission|foothold(空=普通观察)
	Assets       []factAssetView `json:"assets"`
	UnitID       string          `json:"unit_id,omitempty"`
	EvidenceHead string          `json:"evidence_head,omitempty"`
	EvidenceRef  string          `json:"evidence_ref,omitempty"`
	CreatedRev   int64           `json:"created_rev"`
	ConfirmedRev int64           `json:"confirmed_rev"`
	CreatedAt    string          `json:"created_at,omitempty"`
	ConfirmedAt  string          `json:"confirmed_at,omitempty"`
}

// handleFactsList 已确认事实列表(GET /api/tasks/{id}/facts):只收
// confirmed(候选经主宰收割转正后才入此清单);入账序返回,面板自上
// 而下取最新即倒序读(与虫群单位列表同口径)。
// P4-36:入账/确认时刻读 nodes 投影列(added_at/confirmed_at 由写侧
// 同事务维护)——不再扫描事件流。
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

	out := make([]factRow, 0, len(facts))
	for _, f := range facts {
		row := factRow{
			ID: f.ID, Statement: f.Title, Loot: report.LootClass(f.Title),
			CreatedRev: f.CreatedRev,
			Assets:     []factAssetView{},
		}
		row.CreatedAt = f.AddedAt
		if f.ConfirmedAt != "" {
			row.ConfirmedAt, row.ConfirmedRev = f.ConfirmedAt, f.ConfirmedRev
		} else {
			row.ConfirmedAt, row.ConfirmedRev = row.CreatedAt, f.CreatedRev
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
