package server

import (
	"fmt"
	"net/http"
)

// 统一图投影 API(P2-09,8.7 三视图共用):GET /api/tasks/{id}/graph。
// 账本 → 图数据的唯一定义在 rings.Store.Graph(全部六类节点 + 全部
// 类型化边 + goal 派生边),资产树 / 进度图 / 年轮图三视图共用本端点
// 各自投影;进度图(P2-09)为首个接入方。节点详情不在此载荷(大对象
// 走 /nodes/{nid} 与各类型详情端点,按需拉取)。

type graphNodeJSON struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	AssetClass      string `json:"asset_class,omitempty"`
	Covered         bool   `json:"covered,omitempty"`
	FactCount       int    `json:"fact_count,omitempty"`
	HypothesisCount int    `json:"hypothesis_count,omitempty"`
	Source          string `json:"source,omitempty"`   // 资产来源三态(P4-05)
	Excluded        bool   `json:"excluded,omitempty"` // 资产已从授权范围摘除(P4-05)
	CreatedRev      int64  `json:"created_rev"`
	UpdatedRev      int64  `json:"updated_rev"`
}

type graphEdgeJSON struct {
	Type       string `json:"type"`
	From       string `json:"from"`
	To         string `json:"to"`
	CreatedRev int64  `json:"created_rev,omitempty"`
}

// handleTaskGraph 统一图投影:一次返回任务全部节点与类型化边(含
// goal 派生边),三视图共用(树 / DAG / 全图各自在前端投影)。
func (s *Server) handleTaskGraph(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	g, err := s.Store.Graph(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var excluded map[string]bool
	if roe, err := s.Store.CurrentRoE(r.Context(), taskID); err == nil {
		excluded = roe.ExcludedAssetSet()
	}
	v := struct {
		Nodes []graphNodeJSON `json:"nodes"`
		Edges []graphEdgeJSON `json:"edges"`
	}{Nodes: []graphNodeJSON{}, Edges: []graphEdgeJSON{}}
	for _, n := range g.Nodes {
		v.Nodes = append(v.Nodes, graphNodeJSON{
			ID: n.ID, Type: string(n.Type), Title: n.Title, Status: n.Status,
			AssetClass: string(n.AssetClass), Covered: n.Covered,
			FactCount: n.FactCount, HypothesisCount: n.HypothesisCount,
			Source: n.Source, Excluded: excluded[n.ID],
			CreatedRev: n.CreatedRev, UpdatedRev: n.UpdatedRev,
		})
	}
	for _, e := range g.Edges {
		v.Edges = append(v.Edges, graphEdgeJSON{
			Type: string(e.Type), From: e.From, To: e.To, CreatedRev: e.CreatedRev,
		})
	}
	writeJSON(w, http.StatusOK, v)
}
