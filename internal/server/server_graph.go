package server

import (
	"fmt"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
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
	// 发现型资产的出生血缘(P4-12:发现步骤/单位/发现事实,收割入图时冻结
	// 进 detail;非发现资产零值)。链路 UI 据此呈现"谁发现的它"。
	BirthStep  string `json:"birth_step,omitempty"`
	BirthUnit  string `json:"birth_unit,omitempty"`
	BirthFact  string `json:"birth_fact,omitempty"`
	CreatedRev int64  `json:"created_rev"`
	UpdatedRev int64  `json:"updated_rev"`
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
	// P4-64 ⑥:事实/假设标题优先短句校对稿(node_polish)——年轮图与
	// 进度图节点卡、链路图共用本端点,一处替换全站生效;判断标题优先
	// 链在各自列表/详情装配(P4-59 既有)。P4-65 ⑩:步骤意图标题同样
	// 优先校对稿(拆解派发落账后异步补写;任务包与机器消费用节点原文)。
	// 空表零变化(节点原文)。
	titlePolish := s.polishedNodeTitleOf(r.Context(), taskID)
	v := struct {
		Queue overmind.QueueWatermark `json:"queue"`
		Nodes []graphNodeJSON         `json:"nodes"`
		Edges []graphEdgeJSON         `json:"edges"`
	}{Nodes: []graphNodeJSON{}, Edges: []graphEdgeJSON{}}
	var steps []rings.NodeFull
	for _, n := range g.Nodes {
		if n.Type == rings.NodeStep {
			steps = append(steps, rings.NodeFull{NodeView: rings.NodeView{ID: n.ID, Status: n.Status}})
		}
	}
	v.Queue = overmind.QueueWatermarkOf(steps, g.Edges, overmind.QueueLimit(r.Context(), s.Store))
	for _, n := range g.Nodes {
		title := n.Title
		if t := titlePolish[n.ID]; t != "" && (n.Type == rings.NodeFact || n.Type == rings.NodeHypothesis || n.Type == rings.NodeStep) {
			title = t
		}
		v.Nodes = append(v.Nodes, graphNodeJSON{
			ID: n.ID, Type: string(n.Type), Title: title, Status: n.Status,
			AssetClass: string(n.AssetClass), Covered: n.Covered,
			FactCount: n.FactCount, HypothesisCount: n.HypothesisCount,
			Source: n.Source, Excluded: excluded[n.ID],
			BirthStep: n.BirthStep, BirthUnit: n.BirthUnit, BirthFact: n.BirthFact,
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
