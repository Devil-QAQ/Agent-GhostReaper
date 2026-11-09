package rings

import (
	"context"
	"fmt"
	"strings"
)

// 摘除连坐废弃(P4-55 审核迭代,造物主 2026-11-08 定案:动"对象"摘资产
// → 锚定其上的声明/假设程序确定性全废弃,不问主宰、不挂起;系统故障
// 才自动重派,动"意图"/动"人"交主宰裁决——程序禁止连坐)。

// DiscardReasonExcluded 摘除连坐废弃的固定理由(逐字入账)。
const DiscardReasonExcluded = "目标已摘除"

// claimDiscardable 锚定摘除覆盖资产后仍可废弃的声明状态(锤炼管线内;
// 终态——已确认/不成立/待确认/已修复——不是悬在途工作,不动)。
var claimDiscardable = map[string]bool{
	TestClaimCandidate: true, TestClaimTesting: true, TestClaimAwaitingRound: true,
}

// hypoDiscardable 同上,假设侧(挂账是休眠不是悬空,同废——摘除资产上
// 的假设已无验证通道,留着只会是永久死重)。
var hypoDiscardable = map[string]bool{
	HypothesisCandidate: true, HypothesisTesting: true, HypothesisHeld: true,
}

// ExcludedCoveredAssetIDs 摘除清单当前覆盖的资产 ID 集:精确命中 +
// 通配/端口写法覆盖(按资产锚点判定)+ 无锚点资产的排除标识子串回退
// (与收割派发守卫同口径同覆盖面——派发不派的地方,废弃同样连坐)。
func ExcludedCoveredAssetIDs(assets []NodeFull, excluded map[string]bool) map[string]bool {
	covered := map[string]bool{}
	for id := range excluded {
		covered[id] = true
	}
	for _, a := range assets {
		if covered[a.ID] {
			continue
		}
		if anchor, ok := ScopeAnchorOf(a.Title); ok {
			if ExcludedCoversTarget(assets, excluded, anchor.Host, anchor.Port) {
				covered[a.ID] = true
			}
			continue
		}
		// 无主机锚点的资产(收割入图的描述型标题):按排除锚点子串回退
		// ——标题里出现被摘除资产的主机标识(host,端口限定摘除只认
		// host:port 形态)即视为同一目标的呈现。
		if excludedAnchorInTitle(assets, excluded, a.Title) {
			covered[a.ID] = true
		}
	}
	return covered
}

// excludedAnchorInTitle 无锚点资产的摘除子串回退判定。
func excludedAnchorInTitle(assets []NodeFull, excluded map[string]bool, title string) bool {
	for _, a := range assets {
		if !excluded[a.ID] {
			continue
		}
		anchor, ok := ScopeAnchorOf(a.Title)
		if !ok {
			continue
		}
		if anchor.Port != "" {
			if strings.Contains(title, anchor.Host+":"+anchor.Port) {
				return true
			}
			continue
		}
		if strings.Contains(title, anchor.Host) {
			return true
		}
	}
	return false
}

// DiscardAnchoredFindings 摘除连坐废弃本体:锚定在摘除覆盖资产上的
// 非终态声明与假设批量转已废弃——testclaim.record(discard,理由) +
// node.state_changed(discarded)/假设评估记录(discard) 同批一事务落账。
// 幂等(可重入:终态跳过、零命中零落笔);终态只追加、历史不涂改
// (R1);勾回资产不自动复活(再验走操作者深入)。返回本次废弃的声明
// 与假设 ID(调用方回执与留痕用)。
func DiscardAnchoredFindings(ctx context.Context, store *Store, taskID, reason, actor string) ([]string, []string, error) {
	roe, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, nil, fmt.Errorf("rings: 摘除废弃读范围: %w", err)
	}
	excluded := roe.ExcludedAssetSet()
	if len(excluded) == 0 {
		return nil, nil, nil
	}
	assets, err := store.NodesByType(ctx, taskID, NodeAsset, "")
	if err != nil {
		return nil, nil, fmt.Errorf("rings: 摘除废弃查资产: %w", err)
	}
	covered := ExcludedCoveredAssetIDs(assets, excluded)
	abouts, err := store.Edges(ctx, taskID, EdgeAbout)
	if err != nil {
		return nil, nil, fmt.Errorf("rings: 摘除废弃查锚点: %w", err)
	}
	anchored := func(id string) bool {
		for _, e := range abouts {
			if e.From == id && covered[e.To] {
				return true
			}
		}
		return false
	}

	claims, err := store.NodesByType(ctx, taskID, NodeTestClaim, "")
	if err != nil {
		return nil, nil, fmt.Errorf("rings: 摘除废弃查判断: %w", err)
	}
	var claimIDs []string
	for _, c := range claims {
		if claimDiscardable[c.Status] && anchored(c.ID) {
			claimIDs = append(claimIDs, c.ID)
		}
	}
	hypos, err := store.NodesByType(ctx, taskID, NodeHypothesis, "")
	if err != nil {
		return nil, nil, fmt.Errorf("rings: 摘除废弃查假设: %w", err)
	}
	var hypoIDs []string
	for _, h := range hypos {
		if hypoDiscardable[h.Status] && anchored(h.ID) {
			hypoIDs = append(hypoIDs, h.ID)
		}
	}
	if len(claimIDs) == 0 && len(hypoIDs) == 0 {
		return nil, nil, nil
	}

	var events []Event
	for _, id := range claimIDs {
		events = append(events,
			Event{Kind: EventTestClaimRecord, Actor: actor,
				Payload: TestClaimRecord{ClaimID: id, Type: TestClaimRecDiscard, Reason: reason}},
			Event{Kind: EventNodeStateChanged, Actor: actor,
				Payload: NodeStateChanged{NodeID: id, Status: TestClaimDiscarded, Note: reason}},
		)
	}
	for _, id := range hypoIDs {
		events = append(events,
			Event{Kind: EventHypothesisRecord, Actor: actor,
				Payload: HypothesisRecord{HypothesisID: id, Type: HypothesisRecEvaluation,
					Decision: HypothesisDiscard, Reason: reason}},
			Event{Kind: EventNodeStateChanged, Actor: actor,
				Payload: NodeStateChanged{NodeID: id, Status: HypothesisDiscarded, Note: reason}},
		)
	}
	if _, err := store.Append(ctx, taskID, events...); err != nil {
		return claimIDs, hypoIDs, fmt.Errorf("rings: 摘除废弃落账: %w", err)
	}
	return claimIDs, hypoIDs, nil
}
