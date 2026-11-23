package overmind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

const AssetProposalPrefix = "ad-"

var ErrAssetProposalDecided = errors.New("资产加入请求已处理或不存在")

type AssetProposal struct {
	FactID string `json:"fact_id"`
	Class  string `json:"class"`
	Title  string `json:"title"`
	Parent string `json:"parent,omitempty"`
}

// proposedAssetEvents keeps the observation in the ledger and asks about
// inventory membership. It neither creates a formal asset nor blocks a unit.
// A repeated observation of the same identity reuses the recorded decision.
func proposedAssetEvents(ctx context.Context, store *rings.Store, task string, fact rings.NodeFull, decl *swarm.NewAssetDecl) ([]rings.Event, error) {
	class, title, key, err := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
	if err != nil {
		return nil, nil
	} // background stays in the confirmed observation
	assets, err := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		_, _, existing, e := rings.AssetIdentity(a.AssetClass, a.Title)
		if e == nil && existing == key && a.Status != "retired" {
			return discoveredAssetEvents(ctx, store, task, fact, &swarm.NewAssetDecl{Class: string(class), Title: a.Title})
		}
	}
	h := sha256.Sum256([]byte(key))
	rid := AssetProposalPrefix + hex.EncodeToString(h[:12])
	rows, err := store.ApprovalByRequest(ctx, task, rid)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		return nil, nil
	}
	proposal := AssetProposal{FactID: fact.ID, Class: string(class), Title: title, Parent: decl.Parent}
	args, err := json.Marshal(proposal)
	if err != nil {
		return nil, err
	}
	return []rings.Event{{Kind: rings.EventApprovalRecord, Actor: ActorOvermind, Payload: rings.ApprovalRecord{
		RequestID: rid, Type: rings.ApprovalRecAssetWait, UnitID: factUnitOf(fact),
		Intent: fact.Title, ToolArgs: args, Note: "是否将此对象加入资产和后续规划"}}}, nil
}

// DecideAssetProposal uses the existing ledger revision guard: simultaneous
// decisions, repeated discovery and a harvest racing this decision converge to
// one recorded outcome. Declining never touches RoE or execution state.
func DecideAssetProposal(ctx context.Context, store *rings.Store, task, rid string, added bool, reason string) (string, error) {
	if !strings.HasPrefix(rid, AssetProposalPrefix) {
		return "", ErrAssetProposalDecided
	}
	for ctx.Err() == nil {
		rev, err := store.CurrentRevision(ctx, task)
		if err != nil {
			return "", err
		}
		rows, err := store.ApprovalByRequest(ctx, task, rid)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 || rows[len(rows)-1].Type != rings.ApprovalRecAssetWait {
			return "", ErrAssetProposalDecided
		}
		row := rows[len(rows)-1]
		var p AssetProposal
		if json.Unmarshal([]byte(row.ToolArgs), &p) != nil || p.FactID == "" {
			return "", fmt.Errorf("资产加入请求内容无效")
		}
		fact, err := store.Node(ctx, task, p.FactID)
		if err != nil || fact.Type != rings.NodeFact || fact.Status != "confirmed" {
			return "", fmt.Errorf("资产来源事实不可用")
		}
		typ := rings.ApprovalRecAssetDeclined
		events := []rings.Event{}
		if added {
			typ = rings.ApprovalRecAssetAdded
			events, err = discoveredAssetEvents(ctx, store, task, *fact, &swarm.NewAssetDecl{Class: p.Class, Title: p.Title, Parent: p.Parent})
			if err != nil {
				return "", err
			}
			assetID := ""
			for _, ev := range events {
				if n, ok := ev.Payload.(rings.NodeAdded); ok && n.NodeType == rings.NodeAsset {
					assetID = n.NodeID
				}
				if edge, ok := ev.Payload.(rings.EdgeAdded); ok && edge.EdgeType == rings.EdgeAbout && edge.FromNode == fact.ID {
					assetID = edge.ToNode
				}
			}
			facts, err := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
			if err != nil {
				return "", err
			}
			others := []rings.NodeFull{}
			for _, other := range facts {
				if other.ID != fact.ID {
					others = append(others, other)
				}
			}
			bindings, err := store.BindObservedAssets(ctx, task, []rings.NodeAdded{{NodeID: assetID, AssetClass: rings.AssetClass(p.Class), Title: p.Title}}, others, ActorOvermind)
			if err != nil {
				return "", err
			}
			events = append(events, bindings...)

		}
		events = append(events, rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: rid, Type: typ, UnitID: row.UnitID, Intent: row.Intent, ToolArgs: json.RawMessage(row.ToolArgs),
			DecidedBy: "creator", Reason: strings.TrimSpace(reason)}})
		_, err = store.AppendAtRevision(ctx, task, rev, events...)
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		return p.Title, err
	}
	return "", ctx.Err()
}
