package server

import "ghostreaper/internal/rings"

// panoFocusMembers follows original provenance endpoints, then collapses them to
// display cards. An alias is never an instruction to traverse all of its facts.
// Claim focus shows its production inputs. A referenced earlier claim is an
// input boundary, not an instruction to expand its other paths or later outputs.
func panoFocusMembers(v *panoSkeletonView, requested, source, target string) panoLinksView {
	id := v.Aliases[requested][0]
	index := map[string]panoNodeRow{}
	for _, node := range v.Nodes {
		index[node.ID] = node
	}
	result := panoLinksView{Node: index[id], IDs: []string{}, Edges: []panoEdgeRow{}, Sources: []string{}, Targets: []string{}, Results: map[string][]panoFactRow{}}
	sources, targets := map[string]bool{}, map[string]bool{}
	for _, edge := range v.Edges {
		if edge.To == id {
			sources[edge.From] = true
		}
		if edge.From == id {
			targets[edge.To] = true
		}
	}
	for _, node := range v.Nodes {
		if sources[node.ID] {
			result.Sources = append(result.Sources, node.ID)
		}
		if targets[node.ID] {
			result.Targets = append(result.Targets, node.ID)
		}
	}
	type arc struct{ from, to, ref string }
	in, out := map[string][]arc{}, map[string][]arc{}
	add := func(from, to, ref string) {
		if len(v.Aliases[from]) == 0 || len(v.Aliases[to]) == 0 {
			return
		}
		a := arc{from, to, ref}
		in[to], out[from] = append(in[to], a), append(out[from], a)
	}
	for _, edge := range v.rawEdges {
		switch edge.Type {
		case rings.EdgeSourcedFrom, rings.EdgeDerivedFrom:
			add(edge.To, edge.From, edge.To)
		case rings.EdgeProduces:
			add(edge.From, edge.To, edge.To)

		}
	}
	// Assignment entries are selected from the overview, with the same refs
	// and direction. A shared asset is still a traversal boundary.
	for _, edge := range v.Edges {
		if edge.Rel != "covers" {
			continue
		}
		for _, ref := range edge.Refs {
			add(ref, edge.To, ref)
			if index[id].T == "asset" && ref != requested && len(v.Aliases[ref]) > 0 && v.Aliases[ref][0] == id {
				add(requested, edge.To, ref)
			}
		}
	}

	for _, node := range v.rawNodes {
		if node.Type == rings.NodeTestClaim {
			if step, _ := node.Detail["step_id"].(string); step != "" {
				add(step, node.ID, step)
			}
		}
		if node.Type == rings.NodeAsset {
			if step, _ := node.Detail["step_id"].(string); step != "" {
				add(step, node.ID, node.ID)
			} else if fact, _ := node.Detail["discovered_fact"].(string); fact != "" {
				add(fact, node.ID, fact)
			}
		}
	}
	if v.rawNodes[requested].Type == rings.NodeFact {
		// The display card may own other facts with unrelated destinations.
		// Only this selected fact supplies downstream branch choices.
		targets = map[string]bool{}
		for _, edge := range out[requested] {
			for _, alias := range v.Aliases[edge.to] {
				if alias != id {
					targets[alias] = true
				}
			}
		}
		result.Targets = []string{}
		for _, node := range v.Nodes {
			if targets[node.ID] {
				result.Targets = append(result.Targets, node.ID)
			}
		}
	}
	included, facts := map[string]bool{id: true}, map[string]bool{}
	refs := map[string]map[string]bool{}
	mark := func(raw string) {
		for _, alias := range v.Aliases[raw] {
			included[alias] = true
		}
		if v.rawNodes[raw].Type == rings.NodeFact {
			facts[raw] = true
		}
	}
	mark(requested)
	claimFocus := index[id].T == "claim"
	for direction, adjacency := range []map[string][]arc{in, out} {
		if direction == 1 && claimFocus && target == "" {
			continue
		}
		gate := source
		if direction == 1 {
			gate = target
		}
		pending, seen := []string{requested}, map[string]bool{}
		for i := 0; i < len(pending); i++ {
			current := pending[i]
			if seen[current] {
				continue
			}
			seen[current] = true
			// Intent/fact inputs can have several complete production paths.
			// Walking their ancestors never walks a sibling's descendants. A
			// reached output claim, or shared asset, closes that direction.
			if current != requested && (v.rawNodes[current].Type == rings.NodeAsset || v.rawNodes[current].Type == rings.NodeTestClaim && (direction == 1 || claimFocus)) {
				continue
			}
			for _, edge := range adjacency[current] {
				next := edge.from
				if direction == 1 {
					next = edge.to
				}
				allowed := false
				for _, a := range v.Aliases[current] {
					for _, b := range v.Aliases[next] {
						if a == id && b != id && gate != "" && b != gate {
							continue
						}
						allowed = true
						from, to := b, a
						if direction == 1 {
							from, to = a, b
						}
						key := from + "\x00" + to
						if refs[key] == nil {
							refs[key] = map[string]bool{}
						}
						refs[key][edge.ref] = true
					}
				}
				if allowed {
					mark(next)
					pending = append(pending, next)
				}
			}
		}
	}
	for _, edge := range v.Edges {
		allowed := refs[edge.From+"\x00"+edge.To]
		if !included[edge.From] || !included[edge.To] || len(allowed) == 0 {
			continue
		}
		next := edge
		next.Refs = nil
		for _, ref := range edge.Refs {
			if allowed[ref] {
				next.Refs = append(next.Refs, ref)
			}
		}
		if len(next.Refs) > 0 {
			result.Edges = append(result.Edges, next)
		}
	}
	for _, node := range v.Nodes {
		if !included[node.ID] {
			continue
		}
		result.IDs = append(result.IDs, node.ID)
		if node.T == "step" {
			result.Results[node.ID] = []panoFactRow{}
			for _, fact := range node.Results {
				if facts[fact.ID] {
					result.Results[node.ID] = append(result.Results[node.ID], fact)
				}
			}
		}
	}
	return result
}
