// Package server 的四页签显示态推导(P4-56,问题清单 #77):锤炼/假设
// 对象的显示键由"机器状态 + 当前反证/验证步骤现值 + 操作者终止判定"
// 推导(rings.ClaimDisplay/HypDisplay 单一来源),本文件只做步骤投影的
// 一次性装配——每请求一次 NodesByType(NodeStep) 投影直查 + 一次按
// kind 定向的舵杆记录查询(v13 索引),零事件流全量扫描(P4-36 列表
// 纪律)。事实页的迁入条目(已成立假设带"由假设升级")与"新"标记
// (候选事实)同属本阶段的显示层口径:账本机器状态一字不动。

package server

import (
	"context"

	"ghostreaper/internal/rings"
)

// stepKindVerify 验证类 Step 的 detail.kind(与 overmind.StepDetailKindVerify
// 同值;server 不依赖 overmind,词面在此钉死,漂移由单测对表)。
const stepKindVerify = "verify"

// displayCtx 一次请求的显示态推导上下文:反证步骤按声明分组、验证步骤
// 按假设分组、舵杆终止步骤集合。stepsOnly=跳过舵杆查询(纯机器态推导
// 的场合,如报告导出在 store 侧自带判定)。
type displayCtx struct {
	falsifyByClaim map[string][]rings.NodeFull
	verifyByHyp    map[string][]rings.NodeFull
	steerKills     map[string]bool
}

// loadDisplayCtx 装配显示态推导上下文(步骤一次投影直查 + 舵杆 kind
// 定向查询;任一步失败如实返回可用部分,显示退粗词不误标)。
func (s *Server) loadDisplayCtx(ctx context.Context, taskID string) *displayCtx {
	dc := &displayCtx{
		falsifyByClaim: map[string][]rings.NodeFull{},
		verifyByHyp:    map[string][]rings.NodeFull{},
		steerKills:     s.Store.SteerKilledSteps(ctx, taskID),
	}
	steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return dc
	}
	for _, st := range steps {
		kind, _ := st.Detail["kind"].(string)
		switch kind {
		case "falsify":
			if cid, _ := st.Detail["testclaim_id"].(string); cid != "" {
				dc.falsifyByClaim[cid] = append(dc.falsifyByClaim[cid], st)
			}
		case stepKindVerify:
			if hid, _ := st.Detail["hypothesis_id"].(string); hid != "" {
				dc.verifyByHyp[hid] = append(dc.verifyByHyp[hid], st)
			}
		}
	}
	return dc
}

// claimFlightOf 声明的当前反证轮飞行情报(推导输入);无步骤返回 nil
// (rings.ClaimFlightOfSteps 单一来源,同词同拍)。
func claimFlightOf(dc *displayCtx, claimID string) *rings.ClaimFlight {
	return rings.ClaimFlightOfSteps(dc.falsifyByClaim[claimID], dc.steerKills)
}

// hypFlightOf 假设的验证步骤飞行情报;无步骤返回 nil(取创建序最新
// 的验证步骤——验证无轮次概念,latest 即当前)。
func hypFlightOf(dc *displayCtx, hypID string) *rings.HypFlight {
	var cur rings.NodeFull
	found := false
	for _, st := range dc.verifyByHyp[hypID] {
		if !found || st.CreatedRev > cur.CreatedRev {
			cur, found = st, true
		}
	}
	if !found {
		return nil
	}
	return &rings.HypFlight{
		Status:            cur.Status,
		OperatorCancelled: cur.Status == "cancelled",
		SteerKilled:       dc.steerKills[cur.ID],
	}
}

// ClaimDisplayFor 声明显示键(带步骤现值);pendingReason 为 pending
// 投影理由(终局清点兜底悬停判定)。
func ClaimDisplayFor(dc *displayCtx, claimID, status, pendingReason string) (key, hint string) {
	return rings.ClaimDisplay(status, claimFlightOf(dc, claimID), pendingReason)
}

// HypDisplayFor 假设显示键(带验证步骤现值)。
func HypDisplayFor(dc *displayCtx, hypID, status string) string {
	return rings.HypDisplay(status, hypFlightOf(dc, hypID))
}
