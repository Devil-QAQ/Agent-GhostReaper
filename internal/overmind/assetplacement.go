package overmind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"strings"
)

// Placement is passive: it consumes already recorded facts and declarations.
// It never invokes a model, dispatches work, probes targets, or infers a parent.
func (l *Loop) syncRecordedAssets(ctx context.Context) {
	if err := l.reconcileAssetChoices(ctx); err != nil {
		l.setErr(err)
		return
	}
	facts, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeFact, "confirmed")
	if err != nil {
		l.setErr(err)
		return
	}
	for _, fact := range facts {
		observations, err := rings.ObservedAssets(fact.Detail)
		if err != nil {
			l.setErr(err)
			continue
		}
		if decl := factNewAsset(fact.Detail); decl != nil {
			_, _, key, e := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
			exists := false
			for _, a := range observations.Assets {
				_, _, other, ea := rings.AssetIdentity(a.Class, a.Title)
				exists = exists || e == nil && ea == nil && key == other
			}
			if !exists {
				observations.Assets = append(observations.Assets, rings.AssetObservation{Class: rings.AssetClass(decl.Class), Title: decl.Title, Evidence: fact.Title})
			}
		}
		if len(observations.Assets) == 0 && len(observations.Relations) == 0 {
			continue
		}
		if err := l.recordFactAssets(ctx, fact, observations); err != nil {
			l.setErr(err)
		}
	}
}

type factAssetObservation struct {
	FactID string `json:"fact_id"`
	rings.AssetObservations
}

func (l *Loop) recordFactAssets(ctx context.Context, fact rings.NodeFull, result rings.AssetObservations) error {
	prior, err := rings.ObservedAssets(fact.Detail)
	if err != nil {
		return err
	}
	observed := map[string]bool{}
	merged := rings.AssetObservations{}
	for _, a := range append(prior.Assets, result.Assets...) {
		key := string(a.Class) + "\n" + a.Title
		if !observed[key] {
			observed[key] = true
			merged.Assets = append(merged.Assets, a)
		}
	}
	links := map[string]bool{}
	for _, r := range append(prior.Relations, result.Relations...) {
		_, _, from, ef := rings.AssetIdentity(r.FromClass, r.From)
		_, _, to, et := rings.AssetIdentity(r.ToClass, r.To)
		if ef == nil && et == nil && from == to {
			continue
		}
		key := string(r.FromClass) + r.From + string(r.ToClass) + r.To + r.Kind
		if !links[key] {
			links[key] = true
			merged.Relations = append(merged.Relations, r)
		}
	}
	result = merged
	raw, _ := json.Marshal(result)
	hash := sha256.Sum256(append([]byte("asset-placement/v1\n"+fact.Title+"\n"), raw...))
	source := hex.EncodeToString(hash[:])
	if fact.Detail["asset_discovery_source"] == source {
		return nil
	}
	evidence, _ := fact.Detail["evidence"].(string)
	if ref, _ := fact.Detail["evidence_ref"].(string); ref != "" && l.Swarm != nil && l.Swarm.Blobs != nil {
		b, err := l.Swarm.Blobs.Read(ref)
		if err != nil {
			return err
		}
		evidence = string(b)
	}
	if _, err := rings.ObservedAssets(map[string]any{"asset_observations": result}); err != nil {
		return err
	}
	for _, relation := range result.Relations {
		if !strings.Contains(fact.Title+"\n"+evidence, relation.Evidence) {
			return errors.New("资产关系缺少原始事实依据")
		}
	}
	for ctx.Err() == nil {
		rev, err := l.Store.CurrentRevision(ctx, l.TaskID)
		if err != nil {
			return err
		}
		current, err := l.Store.Node(ctx, l.TaskID, fact.ID)
		if err != nil {
			return err
		}
		if current.Status != "confirmed" || current.Title != fact.Title || current.Detail["asset_discovery_source"] == source {
			return nil
		}

		var events []rings.Event
		assets, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeAsset, "")
		if err != nil {
			return err
		}
		roe, err := l.Store.CurrentRoE(ctx, l.TaskID)
		if err != nil {
			return err
		}
		observations := result.Assets
		result.Assets = observations
		fact.Detail["asset_observations"] = result
		seen := map[string]bool{}
		for _, a := range observations {
			if strings.TrimSpace(a.Evidence) == "" || !strings.Contains(fact.Title+"\n"+evidence, a.Evidence) {
				return errors.New("资产识别缺少原始证据")
			}
			class, title, key, e := rings.AssetIdentity(a.Class, a.Title)
			if e != nil {
				return errors.New("资产识别对象无效")
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			decl := &swarm.NewAssetDecl{Class: string(class), Title: title}
			var ev []rings.Event
			automatic := rings.AutomaticallyInventory(class, title, assets, roe)
			if (class == rings.AssetApp || class == rings.AssetMiniProgram) && !strings.Contains(title, "://") {
				automatic = true
			}
			if automatic {
				ev, e = automaticAssetEvents(ctx, l.Store, l.TaskID, fact, decl)
			} else if class != rings.AssetAPI {
				ev, e = proposedAssetEvents(ctx, l.Store, l.TaskID, fact, decl)
			}
			for _, event := range ev {
				if n, ok := event.Payload.(rings.NodeAdded); ok && n.NodeType == rings.NodeAsset {
					assets = append(assets, rings.NodeFull{NodeView: rings.NodeView{ID: n.NodeID, Type: n.NodeType, Title: n.Title, AssetClass: n.AssetClass, Status: n.Status}, Detail: n.Detail})
				}
			}
			if e != nil {
				return e
			}
			events = append(events, ev...)
		}
		events = append(events, rings.Event{Kind: rings.EventNodeAmended, Actor: ActorOvermind, Payload: rings.NodeAmended{NodeID: fact.ID, Detail: map[string]any{"asset_discovery_source": source, "asset_observations": result}}})
		_, err = l.Store.AppendAtRevision(ctx, l.TaskID, rev, uniqueAssetEvents(events)...)
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		return err
	}
	return ctx.Err()
}
