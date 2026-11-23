package rings

import (
	"context"
	"fmt"
	"strings"
)

// DiscardReasonExcluded 摘除连坐废弃的固定理由(逐字入账)。
const DiscardReasonExcluded = "目标已摘除"

// claimDiscardable 锚定摘除覆盖资产后仍可废弃的声明状态(P5-02⑤
// 状态集缩小:只剩候选——交卷到机器三查之间的秒级窗口与补考窗口;
// 终态——已确认/证据不足/被推翻/已修复——不是悬在途工作,不动)。
var claimDiscardable = map[string]bool{
	TestClaimCandidate: true,
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

// DiscardAnchoredFindings atomically discards nonterminal claims on excluded assets.
// Repeated calls are idempotent; confirmed findings and historical evidence remain.
func DiscardAnchoredFindings(ctx context.Context, store *Store, taskID, reason, actor string) ([]string, error) {
	roe, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 摘除废弃读范围: %w", err)
	}
	excluded := roe.ExcludedAssetSet()
	if len(excluded) == 0 {
		return nil, nil
	}
	assets, err := store.NodesByType(ctx, taskID, NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("rings: 摘除废弃查资产: %w", err)
	}
	covered := ExcludedCoveredAssetIDs(assets, excluded)
	abouts, err := store.Edges(ctx, taskID, EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("rings: 摘除废弃查锚点: %w", err)
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
		return nil, fmt.Errorf("rings: 摘除废弃查判断: %w", err)
	}
	var claimIDs []string
	for _, c := range claims {
		if claimDiscardable[c.Status] && anchored(c.ID) {
			claimIDs = append(claimIDs, c.ID)
		}
	}
	if len(claimIDs) == 0 {
		return nil, nil
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
	if _, err := store.Append(ctx, taskID, events...); err != nil {
		return claimIDs, fmt.Errorf("rings: 摘除废弃落账: %w", err)
	}
	return claimIDs, nil
}
