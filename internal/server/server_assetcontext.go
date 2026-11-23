package server

import (
	"context"
	"ghostreaper/internal/rings"
)

// Provenance reads original bindings, including assets later removed from the
// current inventory. An organization name or narrative is never an address.
func (s *Server) assetContexts(ctx context.Context, task string) (map[string]factAssetView, error) {
	nodes, err := s.Store.NodesByType(ctx, task, rings.NodeAsset, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]factAssetView, len(nodes))
	for _, a := range nodes {
		row := factAssetView{ID: a.ID, Title: a.Title}
		if _, address, _, err := rings.NetworkAssetIdentity(a.AssetClass, a.Title); err == nil {
			row.Address = address
		}
		out[a.ID] = row
	}
	return out, nil
}
func claimAssetContexts(ids []string, contexts map[string]factAssetView) ([]factAssetView, []string) {
	assets := []factAssetView{}
	addresses := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		a, ok := contexts[id]
		if !ok {
			a = factAssetView{ID: id}
		}
		assets = append(assets, a)
		if a.Address != "" && !seen[a.Address] {
			addresses = append(addresses, a.Address)
			seen[a.Address] = true
		}
	}
	return assets, addresses
}
