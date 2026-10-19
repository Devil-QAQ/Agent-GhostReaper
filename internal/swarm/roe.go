package swarm

import "ghostreaper/internal/rings"

// RoE 工具发现边界(Q7:弱化预定义 ≠ 零定义):虫群可用工具全集经
// RoE 过滤后进入会话——黑名单优先移除,白名单非空则仅保留白名单。
// ApprovalThreshold 是审批门槛(P2-06b,第四章 §3 v2):该级及以上的
// 意图分级弹门等待面板批准,其余放行留痕。IntentRules 是任务级拦截
// 规则(P2-08e,第四章 §4 v2.1):资产范围 × 动作类型 × 处置。数据
// 从账本 roe.updated 事件流读当前值(overmind/swarm 不自持副本)。
type RoE struct {
	AllowedTools      []string
	DeniedTools       []string
	ApprovalThreshold rings.ActionLevel
	IntentRules       []rings.IntentRule
}

// Allows 判定工具是否在边界内。
func (r RoE) Allows(toolName string) bool {
	for _, d := range r.DeniedTools {
		if d == toolName {
			return false
		}
	}
	if len(r.AllowedTools) > 0 {
		for _, a := range r.AllowedTools {
			if a == toolName {
				return true
			}
		}
		return false
	}
	return true
}
