package overmind

import (
	"context"
	"encoding/json"

	"ghostreaper/internal/rings"
)

// claimEvidenceSteps adds only explicitly cited, confirmed provenance to the
// current submission's execution scope. An asset is context, never permission
// to collect every other unit's traffic. Historical campaign timing stays in
// ClaimFalsifySteps; evidence reuse must not inflate that timing.
func claimEvidenceSteps(ctx context.Context, store *rings.Store, task, claim string) (map[string]rings.NodeFull, error) {
	campaign, err := claimCampaignRev(ctx, store, task, claim)
	if err != nil {
		return nil, err
	}
	current, err := claimCampaignStepIDs(ctx, store, task, claim, campaign)
	if err != nil {
		return nil, err
	}
	nodes := map[string]rings.NodeFull{}
	for _, typ := range []rings.NodeType{rings.NodeStep, rings.NodeFact, rings.NodeTestClaim} {
		rows, err := store.NodesByType(ctx, task, typ, "")
		if err != nil {
			return nil, err
		}
		for _, n := range rows {
			nodes[n.ID] = n
		}
	}
	sources, err := store.Edges(ctx, task, rings.EdgeSourcedFrom)
	if err != nil {
		return nil, err
	}
	produces, err := store.Edges(ctx, task, rings.EdgeProduces)
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	for _, e := range sources {
		refs[e.From] = append(refs[e.From], e.To)
	}
	producers := map[string][]string{}
	for _, e := range produces {
		producers[e.To] = append(producers[e.To], e.From)
	}
	out := map[string]rings.NodeFull{}
	queue := []string{claim}
	for id := range current {
		queue = append(queue, id)
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		n, ok := nodes[id]
		if !ok {
			continue
		}
		switch n.Type {
		case rings.NodeStep:
			out[id] = n
			queue = append(queue, refs[id]...)
		case rings.NodeFact:
			if n.Status == "confirmed" {
				queue = append(queue, producers[id]...)
			}
		case rings.NodeTestClaim:
			if id == claim {
				queue = append(queue, refs[id]...)
				continue
			}
			if n.Status != rings.TestClaimConfirmed && n.Status != rings.TestClaimFixed {
				continue
			}
			row, found, err := store.ClaimCaseOf(ctx, task, id)
			if err != nil {
				return nil, err
			}
			if found && row.Repro != nil {
				// The package records actual proof steps; do not infer them from
				// shared assets or historical combined-candidate metadata.
				for _, step := range row.Repro.Steps {
					queue = append(queue, step.StepID)
				}
			}
		}
	}
	return out, nil
}

// toolReferencesURL uses whole URL tokens, so /proof does not match /proof-old.
func toolReferencesURL(r rings.SwarmRecord, ref string) bool {
	var args struct {
		URL     string `json:"url"`
		Command string `json:"command"`
	}
	if json.Unmarshal(r.ToolArgs, &args) != nil {
		return false
	}
	if r.ToolName == "http_probe" {
		return args.URL == ref
	}
	for _, u := range extractEvidenceURLs([]string{args.Command}) {
		if u == ref {
			return true
		}
	}
	return false
}
