// Package server 的操作者深入指令 API(P4-48,问题清单 #67):成果/
// 事实详情"深入"按钮的落点——从具体节点出发推一把的节点锚定指令。
// 确定即记账(operator.dive,actor=creator,R7 无旁路)并经既有唤醒通道
// 立即唤醒主宰;纳谏语义在主宰侧(立即规划是硬承诺,方向采纳由主宰
// 裁决,研判记录说明理由),本端只记账与唤醒,不代替裁决。

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/rings"
)

// handleOperatorDive 下达深入指令(POST /api/tasks/{id}/dive,body
// {node_id, direction}):① 任务与锚定节点须在账本(节点限发现/事实——
// 深入的锚定面,P4-48 ①);② 任务终态(已停止/已完成)拒收——主宰循环
// 已收场,指令无处执行;③ 适用一切节点状态(已确认/待确认/不成立/
// 锤炼中各过程态,已废弃/被反驳的同样可深入——以此为线索让主宰重新
// 探索);④ 同一节点可多次深入,每次一条事件各自可溯。落账后唤醒
// (wake):未在跑任务的唤醒落进容量 1 的通道,拉起后消费,不丢。
func (s *Server) handleOperatorDive(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		NodeID    string `json:"node_id"`
		Direction string `json:"direction"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("缺少锚定节点"))
		return
	}
	ctx := r.Context()
	n, err := s.Store.Node(ctx, taskID, nodeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("节点 %s 不存在", nodeID))
		return
	}
	if n.Type != rings.NodeTestClaim && n.Type != rings.NodeFact {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("深入指令只锚定发现或事实,%s 不是", nodeWordOf(n.Type)))
		return
	}
	if s.taskEnded(ctx, taskID) {
		writeErr(w, http.StatusConflict, fmt.Errorf("任务已结束"))
		return
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventOperatorDive, Actor: "creator",
		Payload: rings.OperatorDive{NodeID: nodeID, Direction: strings.TrimSpace(req.Direction)},
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "direction": strings.TrimSpace(req.Direction)})
}

// taskEnded 任务是否终态(已停止/已完成)——读账本生命周期状态(重启
// 后仍可靠),深入指令的按钮置灰与落账拒收共用本判定。
func (s *Server) taskEnded(ctx context.Context, taskID string) bool {
	row, err := s.Store.Task(ctx, taskID)
	if err != nil {
		return false
	}
	return row.Status == "stopped" || row.Status == "completed"
}

// diveDisabledOf 深入按钮的禁用态(空 = 可用;终态任务返回悬停文案
// "任务已结束")。发现详情与事实详情(节点详情)共用同源判定。
func (s *Server) diveDisabledOf(ctx context.Context, taskID string) string {
	if s.taskEnded(ctx, taskID) {
		return "任务已结束"
	}
	return ""
}

// nodeWordOf 节点类型的人话词(错误消息用,与全站术语一致)。
func nodeWordOf(t rings.NodeType) string {
	switch t {
	case rings.NodeTestClaim:
		return "发现"
	case rings.NodeFact:
		return "事实"
	case rings.NodeAsset:
		return "资产"
	case rings.NodeGoal:
		return "目标"
	case rings.NodeStep:
		return "步骤"
	case rings.NodeHypothesis:
		return "假设"
	}
	return string(t)
}
