package server

import (
	"context"
	"fmt"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"net/http"
)

// 完整关系供正式详情按需读取；工作台摘要与它共用账本投影。

type graphNodeJSON struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	AssetClass string `json:"asset_class,omitempty"`
	Covered    bool   `json:"covered,omitempty"`
	FactCount  int    `json:"fact_count,omitempty"`
	Source     string `json:"source,omitempty"`   // 资产来源三态(P4-05)
	Excluded   bool   `json:"excluded,omitempty"` // 资产已从授权范围摘除(P4-05)
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

// taskGraphJSON 保留节点详情所需的完整来源与状态。
type taskGraphJSON struct {
	Queue overmind.QueueWatermark `json:"queue"`
	Nodes []graphNodeJSON         `json:"nodes"`
	Edges []graphEdgeJSON         `json:"edges"`
}

// Both read surfaces share the existing graph projection and presentation fields.
func (s *Server) taskGraphOf(ctx context.Context, taskID string) (*taskGraphJSON, error) {
	g, err := s.Store.Graph(ctx, taskID)
	if err != nil {
		return nil, err
	}
	var excluded map[string]bool
	if roe, err := s.Store.CurrentRoE(ctx, taskID); err == nil {
		excluded = roe.ExcludedAssetSet()
	}
	titlePolish := s.polishedNodeTitleOf(ctx, taskID)
	v := taskGraphJSON{Nodes: []graphNodeJSON{}, Edges: []graphEdgeJSON{}}
	var steps []rings.NodeFull
	for _, n := range g.Nodes {
		if n.Type == rings.NodeStep {
			steps = append(steps, rings.NodeFull{NodeView: rings.NodeView{ID: n.ID, Status: n.Status}})
		}
	}
	v.Queue = overmind.QueueWatermarkOf(steps, g.Edges, overmind.QueueLimit(ctx, s.Store))
	for _, n := range g.Nodes {
		title := n.Title
		if t := titlePolish[n.ID]; t != "" && (n.Type == rings.NodeFact || n.Type == rings.NodeStep) {
			title = t
		}
		v.Nodes = append(v.Nodes, graphNodeJSON{
			ID: n.ID, Type: string(n.Type), Title: title, Status: n.Status,
			AssetClass: string(n.AssetClass), Covered: n.Covered, FactCount: n.FactCount,
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
	return &v, nil
}

func (s *Server) handleTaskGraph(w http.ResponseWriter, r *http.Request) {
	s.writeTaskGraph(w, r, false)
}

func (s *Server) handleTaskWorkbench(w http.ResponseWriter, r *http.Request) {
	s.writeTaskGraph(w, r, true)
}

func (s *Server) writeTaskGraph(w http.ResponseWriter, r *http.Request, compact bool) {
	task := r.PathValue("id")
	if !s.taskExists(task) {
		writeErr(w, 404, fmt.Errorf("任务不存在"))
		return
	}
	view, err := s.taskGraphOf(r.Context(), task)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if compact {
		nodes, edges := []graphNodeJSON{}, []graphEdgeJSON{}
		for _, n := range view.Nodes {
			if n.Type == "goal" || n.Type == "step" || n.Type == "asset" {
				nodes = append(nodes, n)
			}
		}
		for _, e := range view.Edges {
			if e.Type == "goal" {
				edges = append(edges, e)
			}
		}
		writeJSON(w, 200, struct {
			Nodes []graphNodeJSON `json:"nodes"`
			Edges []graphEdgeJSON `json:"edges"`
		}{nodes, edges})
		return
	}
	writeJSON(w, 200, view)
}

// handleTaskWatermark 返回工作台的六个水位数字。
func (s *Server) handleTaskWatermark(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	steps, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeStep, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	edges, err := s.Store.Edges(r.Context(), taskID, rings.EdgeDependsOn)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK,
		overmind.QueueWatermarkOf(steps, edges, overmind.QueueLimit(r.Context(), s.Store)))
}
