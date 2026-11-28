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

// Only an explicit observation of the same system can admit a new entry.
// Address sharing, DNS, redirects and deployment are not that assertion.
func observedSameSystem(fact rings.NodeFull, class rings.AssetClass, title string, assets []rings.NodeFull, roe rings.RoeState) bool {
	_, _, key, err := rings.AssetIdentity(class, title)
	if err != nil {
		return false
	}
	members := rings.InventoryAssetIDs(assets, roe)
	joined := map[string]bool{}
	for _, a := range assets {
		if !members[a.ID] {
			continue
		}
		_, _, k, e := rings.AssetIdentity(a.AssetClass, a.Title)
		if e == nil {
			joined[k] = true
		}
	}
	observations, err := rings.ObservedAssets(fact.Detail)
	if err != nil {
		return false
	}
	for _, r := range observations.Relations {
		if r.Kind != "same_system" {
			continue
		}
		_, _, from, ef := rings.AssetIdentity(r.FromClass, r.From)
		_, _, to, et := rings.AssetIdentity(r.ToClass, r.To)
		if ef == nil && et == nil && (from == key && joined[to] || to == key && joined[from]) {
			return true
		}
	}
	relations, err := rings.AssetRelationsFromDetail(fact.Detail)
	if err != nil {
		return false
	}
	for _, r := range relations {
		if r.Kind != "same_system" {
			continue
		}
		other := ""
		if r.From == "$new" {
			other = r.To
		}
		if r.To == "$new" {
			other = r.From
		}
		if other != "" && members[other] {
			return true
		}
	}
	return false
}

type AssetProposal struct {
	FactID      string `json:"fact_id"`
	Class       string `json:"class"`
	Title       string `json:"title"`
	Parent      string `json:"parent,omitempty"`
	Independent bool   `json:"independent,omitempty"`
}

// proposedAssetEvents keeps the observation in the ledger and asks about
// inventory membership. It neither creates a formal asset nor blocks a unit.
// A repeated observation of the same identity reuses the recorded decision.
func proposedAssetEvents(ctx context.Context, store *rings.Store, task string, fact rings.NodeFull, decl *swarm.NewAssetDecl) ([]rings.Event, error) {
	class, title, key, err := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
	if err != nil {
		return nil, nil
	} // background stays in the confirmed observation
	autoJoin, err := store.ApprovalAutomationFlag(ctx, task, "auto_join")
	if err != nil {
		return nil, err
	}
	if autoJoin {
		return automaticAssetEvents(ctx, store, task, fact, decl, autoJoin)
	}
	assets, err := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.AssetClass == rings.AssetAPI {
			continue
		}
		_, _, existing, e := rings.AssetIdentity(a.AssetClass, a.Title)
		if e == nil && existing == key && a.Status != "retired" {
			return discoveredAssetEvents(ctx, store, task, fact, &swarm.NewAssetDecl{Class: string(class), Title: a.Title, Independent: rings.IndependentAsset(a)})
		}
	}

	if (class == rings.AssetApp || class == rings.AssetMiniProgram) && !decl.Independent {
		return nil, nil
	}
	roe, err := store.CurrentRoE(ctx, task)
	if err != nil {
		return nil, err
	}
	if rings.AutomaticallyInventory(class, title, assets, roe) || observedSameSystem(fact, class, title, assets, roe) {
		return automaticAssetEvents(ctx, store, task, fact, decl, autoJoin)
	}
	parent := decl.Parent

	h := sha256.Sum256([]byte(key))
	rid := AssetProposalPrefix + hex.EncodeToString(h[:12])
	rows, err := store.ApprovalByRequest(ctx, task, rid)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		return nil, nil
	}
	proposal := AssetProposal{FactID: fact.ID, Class: string(class), Title: title, Parent: parent, Independent: decl.Independent}
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
			events, err = discoveredAssetEvents(ctx, store, task, *fact, &swarm.NewAssetDecl{Class: p.Class, Title: p.Title, Parent: p.Parent, Independent: p.Independent})
			if err != nil {
				return "", err
			}
			for i := range events {
				if node, ok := events[i].Payload.(rings.NodeAdded); ok && node.NodeType == rings.NodeAsset {
					node.Detail["operator_target"] = true
					events[i].Payload = node
				}
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
			children, err := ObservedChildAssetEvents(ctx, store, task, []rings.NodeAdded{{AssetClass: rings.AssetClass(p.Class), Title: p.Title}})
			if err != nil {
				return "", err
			}
			events = append(events, children...)

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

func automaticAssetEvents(ctx context.Context, store *rings.Store, task string, fact rings.NodeFull, decl *swarm.NewAssetDecl, autoJoin bool) ([]rings.Event, error) {
	_, _, key, err := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(key))
	rid := AssetProposalPrefix + hex.EncodeToString(h[:12])
	rows, err := store.ApprovalByRequest(ctx, task, rid)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 && rows[len(rows)-1].Type == rings.ApprovalRecAssetDeclined && rows[len(rows)-1].DecidedBy == "creator" {
		return nil, nil
	}
	events, err := discoveredAssetEvents(ctx, store, task, fact, decl)
	if err != nil {
		return nil, err
	}
	if autoJoin {
		for i, ev := range events {
			if n, ok := ev.Payload.(rings.NodeAdded); ok && n.NodeType == rings.NodeAsset {
				n.Detail["operator_target"] = true
				events[i].Payload = n
			}
		}
		if len(rows) == 0 {
			raw, _ := json.Marshal(AssetProposal{FactID: fact.ID, Class: decl.Class, Title: decl.Title, Parent: decl.Parent, Independent: decl.Independent})
			events = append(events, rings.Event{Kind: rings.EventApprovalRecord, Actor: "automation", Payload: rings.ApprovalRecord{RequestID: rid, Type: rings.ApprovalRecAssetAdded, UnitID: factUnitOf(fact), ToolArgs: raw, DecidedBy: "automation", Reason: "资产自动加入"}})
		}
	}
	if len(rows) > 0 && rows[len(rows)-1].Type == rings.ApprovalRecAssetWait {
		row := rows[len(rows)-1]
		events = append(events, rings.Event{Kind: rings.EventApprovalRecord, Actor: ActorOvermind, Payload: rings.ApprovalRecord{RequestID: rid, Type: rings.ApprovalRecAssetAdded, UnitID: row.UnitID, Intent: row.Intent, ToolArgs: json.RawMessage(row.ToolArgs), DecidedBy: "model", Reason: "目标内发现自动归档"}})
	}
	return events, nil
}

// Both explicit and automatic site joins use the same recovery of earlier
// endpoint observations. A restart can reconstruct it entirely from the ledger.
func ObservedChildAssetEvents(ctx context.Context, store *rings.Store, task string, sites []rings.NodeAdded) ([]rings.Event, error) {
	if len(sites) == 0 {
		return nil, nil
	}
	autoJoin, err := store.ApprovalAutomationFlag(ctx, task, "auto_join")
	if err != nil {
		return nil, err
	}
	facts, err := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	joined := []rings.NodeAdded{}
	events := []rings.Event{}
	for _, fact := range facts {
		observations, err := rings.ObservedAssets(fact.Detail)
		if err != nil {
			return nil, err
		}
		if decl := factNewAsset(fact.Detail); decl != nil {
			observations.Assets = append(observations.Assets, rings.AssetObservation{Class: rings.AssetClass(decl.Class), Title: decl.Title})
		}
		entries := append([]rings.AssetObservation{}, observations.Assets...)
		for _, a := range observations.Assets {
			if a.Class == rings.AssetAPI {
				address := rings.AssetEntryAddress(a.Title)
				entries = append([]rings.AssetObservation{{Class: rings.AssetService, Title: rings.AssetDisplayTitle(rings.AssetService, address)}}, entries...)
			}
		}
		for _, a := range entries {
			normalizedClass, _, key, ek := rings.AssetIdentity(a.Class, a.Title)
			if ek != nil || normalizedClass == rings.AssetDomain || normalizedClass == rings.AssetHost {
				continue
			}
			covered := autoJoin
			if !covered {
				for _, site := range sites {
					_, _, selected, _ := rings.AssetIdentity(site.AssetClass, site.Title)
					if selected == key {
						continue
					}
					joined := rings.NodeFull{NodeView: rings.NodeView{ID: site.NodeID, Type: rings.NodeAsset, Status: "active", AssetClass: site.AssetClass, Title: site.Title}, Detail: map[string]any{"source": rings.AssetSourceManual}}
					if joined.ID == "" {
						joined.ID = "joined:" + selected
					}
					if observedSameSystem(fact, a.Class, a.Title, []rings.NodeFull{joined}, rings.RoeState{}) {
						covered = true
						break
					}
				}

			}
			if ek != nil || !covered || seen[key] {
				continue
			}
			seen[key] = true
			ev, err := automaticAssetEvents(ctx, store, task, fact, &swarm.NewAssetDecl{Class: string(a.Class), Title: a.Title, Independent: a.Independent}, autoJoin)
			if err != nil {
				return nil, err
			}
			for _, event := range ev {
				if n, ok := event.Payload.(rings.NodeAdded); ok && n.NodeType == rings.NodeAsset {
					joined = append(joined, n)
				}
			}
			events = append(events, ev...)
		}
	}
	bindings, err := store.BindObservedAssets(ctx, task, joined, facts, ActorOvermind)
	if err != nil {
		return nil, err
	}
	return uniqueAssetEvents(append(events, bindings...)), nil
}

func uniqueAssetEvents(events []rings.Event) []rings.Event {
	seen := map[string]bool{}
	out := make([]rings.Event, 0, len(events))
	for _, event := range events {
		key := ""
		switch p := event.Payload.(type) {
		case rings.ApprovalRecord:
			key = "approval/" + p.RequestID + "/" + p.Type
		case rings.EdgeAdded:
			key = "edge/" + string(p.EdgeType) + "/" + p.FromNode + "/" + p.ToNode
		}
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, event)
	}
	return out
}

// AutoJoinPending reuses the asset decision path only. It does not grant any
// business action and does not reverse previous declines or exclusions.
func AutoJoinPending(ctx context.Context, store *rings.Store, task string) (int, error) {
	rows, err := store.PendingAssetProposals(ctx, task)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, row := range rows {
		enabled, err := store.ApprovalAutomationFlag(ctx, task, "auto_join")
		if err != nil {
			return handled, err
		}
		if !enabled {
			break
		}
		if _, err := DecideAssetProposal(ctx, store, task, row.RequestID, true, "资产自动加入"); err != nil {
			if errors.Is(err, ErrAssetProposalDecided) {
				continue
			}
			return handled, err
		}
		handled++
	}
	return handled, nil
}
