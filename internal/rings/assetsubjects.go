package rings

import (
	"net/url"
	"sort"
)

// Subject aliases are a presentation projection. Shared address information
// supports multiple independent systems without asserting their containment.
func AssetSubjectAliases(assets []AssetNodeView, hierarchy []EdgeView, relations []AssetRelationView) map[string][]string {
	byID := map[string]AssetNodeView{}
	parent := map[string]string{}
	for _, a := range assets {
		byID[a.ID] = a
	}
	for _, e := range hierarchy {
		parent[e.From] = e.To
	}
	rootOf := func(id string) string {
		seen := map[string]bool{}
		for parent[id] != "" && !seen[id] {
			seen[id] = true
			id = parent[id]
		}
		return id
	}
	business := func(id string) bool {
		c := byID[id].AssetClass
		return c == AssetService || c == AssetApp || c == AssetMiniProgram
	}
	technical := func(id string) bool {
		c := byID[id].AssetClass
		return c == AssetHost || c == AssetDomain || c == AssetSubdomain
	}
	attached := map[string]map[string]bool{}
	attach := func(address, system string) bool {
		owner, root := rootOf(system), rootOf(address)
		if !technical(address) || !business(owner) {
			return false
		}
		if attached[root] == nil {
			attached[root] = map[string]bool{}
		}
		if attached[root][owner] {
			return false
		}
		attached[root][owner] = true
		return true
	}
	for _, r := range relations {
		if r.Kind == "address" || r.Kind == "hosted_on" {
			attach(r.To, r.From)
			attach(r.From, r.To)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, r := range relations {
			if r.Kind != "resolves_to" || !technical(r.From) || !technical(r.To) {
				continue
			}
			for owner := range attached[rootOf(r.From)] {
				changed = attach(r.To, owner) || changed
			}
			for owner := range attached[rootOf(r.To)] {
				changed = attach(r.From, owner) || changed
			}
		}
	}
	hasSystems := false
	for _, a := range assets {
		if business(rootOf(a.ID)) {
			hasSystems = true
			break
		}
	}
	out := map[string][]string{}
	for _, a := range assets {
		root := rootOf(a.ID)
		if len(attached[root]) == 0 {
			if hasSystems && technical(root) {
				out[a.ID] = []string{}
				continue
			}
			out[a.ID] = []string{root}
			continue
		}
		for owner := range attached[root] {
			out[a.ID] = append(out[a.ID], owner)
		}
		sort.Strings(out[a.ID])
	}
	return out
}

// A system is identified on the graph by a recognizable entry address; its
// original name and every alternate entry remain in the asset and evidence.
func AssetSubjectTitle(asset AssetNodeView, assets []AssetNodeView) string {
	entry := asset
	if asset.AssetClass == AssetApp || asset.AssetClass == AssetMiniProgram {
		entries := []AssetNodeView{}
		for _, a := range assets {
			if a.AssetClass != AssetService {
				continue
			}
			for _, subject := range a.SubjectIDs {
				if subject == asset.ID {
					entries = append(entries, a)
					break
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Source != entries[j].Source {
				return entries[i].Source == AssetSourceSeed || entries[i].Source == AssetSourceManual && entries[j].Source != AssetSourceSeed
			}
			if entries[i].CreatedRev != entries[j].CreatedRev {
				return entries[i].CreatedRev < entries[j].CreatedRev
			}
			return entries[i].ID < entries[j].ID
		})
		if len(entries) > 0 {
			entry = entries[0]
		}
	}
	title := AssetDisplayTitle(entry.AssetClass, entry.Title)
	if entry.AssetClass == AssetService || entry.AssetClass == AssetApp || entry.AssetClass == AssetMiniProgram {
		if u, e := url.Parse(title); e == nil && u.Host != "" {
			return u.Host
		}
	}
	return title
}
