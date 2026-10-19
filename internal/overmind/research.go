package overmind

// 主宰完整研判的研判侧件(P2-06,第四章 §1 主宰职责"态势研判"):
//
//   - 态势研判五问:新身份能访问哪些已有资产?新权限是否改变其他漏洞
//     的影响?两条单独影响有限的线索是否组合成新的边界问题?新的可达
//     关系是否解锁之前受阻的步骤?新证据是否推翻旧判断?五问是启发式
//     起点(保证下限),不是封闭问题清单(不设上限)——每轮拆解会话
//     按五问审视态势,结论落 overmind.record(assessment)审计流,
//     "研判发生过、研判了什么"从账本可验证;
//   - 组合发现:多条已确认 Fact 合并为攻击链级 TestClaim(链式声明)。
//     血缘正确 = derived_from 边挂到每条环节 Fact(第三章 §2:Vuln ─
//     derivedFrom → Step|Fact)+ about 资产锚点;声明进标准负向认证
//     状态机(candidate → 反证轮 → 仲裁者机器裁决),发现者记
//     ActorOvermind——反证由虫群独立单位执行,R12(发现者不得自证)
//     由仲裁者按"验证单位 ≠ 发现者"机器核验,主宰自己提交的组合
//     声明同样逃不过反证。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 研判五问的关键词表(快照与研判记录的固定锚点,测试与脚本模型据此
// 定位;五问本身是研判框架,不是状态机词汇)。
const (
	AssessQAccess      = "q_access"      // 新身份能访问哪些已有资产
	AssessQPrivilege   = "q_privilege"   // 新权限是否改变其他漏洞的影响
	AssessQCombination = "q_combination" // 两条单独影响有限的线索是否组合成新的边界问题
	AssessQUnlock      = "q_unlock"      // 新的可达关系是否解锁之前受阻的步骤
	AssessQOverturn    = "q_overturn"    // 新证据是否推翻旧判断
)

// Step 优先级词汇(P2-06 派发策略:并发满员时空出名额按优先级领取)。
// 三档受控词汇;缺省 normal(既有行为)。
const (
	PriorityHigh   = "high"
	PriorityNormal = "normal"
	PriorityLow    = "low"
)

// PriorityRank 优先级排序权重(high=3 > normal=2 > low=1;未知=0 拒绝
// 派发侧使用,排序侧按最低处理兜底)。
func PriorityRank(p string) int {
	switch strings.TrimSpace(p) {
	case PriorityHigh:
		return 3
	case PriorityNormal, "": // 缺省即常规
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

// ChainLink 组合声明的一节链条:环节引用的已确认 Fact + 环节角色
// (入口/凭据/横移/目标等,发现者语言)。
type ChainLink struct {
	FactID string
	Role   string
}

// MinChainLinks 组合声明的最少环节数(两条线索组合成新问题才是
// "组合发现",单线索走普通 submit_test_claim 通道)。
const MinChainLinks = 2

// SubmitCombinedClaim 组合声明受控入口(主宰研判第③问的产物):多条
// 已确认 Fact 合并为攻击链级 TestClaim。一批事务落账:
//
//	TestClaim 节点(candidate,detail 冻结:combined/chain/判定标准/
//	  竞争解释清单/发现者=overmind/影响面声明/组合理由)
//	+ derived_from 边 × N(声明 → 每条环节 Fact,血缘正确)
//	+ about 边(声明 → 资产锚点)
//
// 入口校验:存在性与影响面分开声明(与虫群 submit_test_claim 同纪律);
// 链条 ≥ MinChainLinks 且逐条是已确认 Fact(未验证线索不得作为组合
// 前提,R13);资产锚点必须存在。之后走 HarvestClaims 标准收割派反证。
func SubmitCombinedClaim(ctx context.Context, store *rings.Store, taskID, statement, impact, rationale string,
	chain []ChainLink, assetID, vulnclass string, alternatives []string) (string, error) {
	statement, impact = strings.TrimSpace(statement), strings.TrimSpace(impact)
	if statement == "" || impact == "" {
		return "", fmt.Errorf("overmind: 组合声明须同时具备存在性声明与影响面声明(分开声明,缺一不可)")
	}
	if len(chain) < MinChainLinks {
		return "", fmt.Errorf("overmind: 组合声明至少须 %d 条链环节(单线索走普通声明通道),得到 %d", MinChainLinks, len(chain))
	}
	if strings.TrimSpace(assetID) == "" {
		return "", fmt.Errorf("overmind: 组合声明必须锚定具体资产(asset_id)")
	}
	if _, err := store.Node(ctx, taskID, assetID); err != nil {
		return "", fmt.Errorf("overmind: 组合声明锚定资产 %s: %w", assetID, err)
	}
	rawChain := make([]any, 0, len(chain))
	for _, link := range chain {
		link.FactID = strings.TrimSpace(link.FactID)
		if link.FactID == "" {
			return "", fmt.Errorf("overmind: 链环节缺 fact_id")
		}
		f, err := store.Node(ctx, taskID, link.FactID)
		if err != nil {
			return "", fmt.Errorf("overmind: 链环节 %s: %w", link.FactID, err)
		}
		if f.Type != rings.NodeFact || f.Status != "confirmed" {
			return "", fmt.Errorf("overmind: 链环节 %s 不是已确认 Fact(状态 %s/%s)——未验证线索不得作为组合前提(R13)", link.FactID, f.Type, f.Status)
		}
		m := map[string]any{"fact_id": link.FactID}
		if role := strings.TrimSpace(link.Role); role != "" {
			m["role"] = role
		}
		rawChain = append(rawChain, m)
	}
	// 冻结竞争解释清单:与虫群 submit_test_claim 共用同一编译语义
	//(BuildAlternatives),发现者未列时兜底四条基线。
	alts := swarm.BuildAlternatives(alternatives)
	rawAlts := make([]any, 0, len(alts))
	for _, a := range alts {
		rawAlts = append(rawAlts, map[string]any{"id": a.ID, "label": a.Label})
	}
	id := "tc-chain-" + shortIDSuffix()
	detail := map[string]any{
		"combined":     true,
		"chain":        rawChain,
		"unit_id":      ActorOvermind, // 组合发现的主宰身份(R12:反证单位必为虫群,天然不同)
		"impact":       impact,
		"rationale":    strings.TrimSpace(rationale),
		"standard":     arbiter.DefaultClaimStandard(),
		"alternatives": rawAlts,
	}
	if vulnclass != "" {
		detail["vulnclass"] = strings.TrimSpace(vulnclass)
	}
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeTestClaim, Title: statement,
			Status: rings.TestClaimCandidate, Detail: detail,
		}},
	}
	for _, link := range chain {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: id, ToNode: link.FactID},
		})
	}
	events = append(events, rings.Event{
		Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
		Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: id, ToNode: assetID},
	})
	if _, err := store.Append(ctx, taskID, events...); err != nil {
		return "", fmt.Errorf("overmind: 组合声明入账: %w", err)
	}
	return id, nil
}

// shortIDSuffix 组合声明 ID 的随机后缀(同秒多条不撞 ID;与虫群侧
// shortID 同风格)。rand 失败退时间戳,不因 ID 生成失败丢研判产物。
func shortIDSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
