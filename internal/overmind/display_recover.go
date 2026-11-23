package overmind

import (
	"context"
	"encoding/json"

	"ghostreaper/internal/rings"
)

// Recover only missing presentation from existing business records. There is
// no second durable job/state table: an unchanged source plus its display
// event is the completion receipt. Overflow retains IDs in the ledger rather
// than unbounded contexts, closures or model requests in memory.
func (l *Loop) recoverDisplay(ctx context.Context) {
	defer l.syncRecordedAssets(ctx)
	if l.Provider == nil || ctx.Err() != nil {
		return
	}
	l.polishRecoverMu.Lock()
	defer l.polishRecoverMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	sources, err := l.Store.NodeTextSourceRevs(ctx, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	texts, err := l.Store.NodePolish(ctx, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	for _, typ := range []rings.NodeType{rings.NodeFact, rings.NodeStep} {
		nodes, err := l.Store.NodesByType(ctx, l.TaskID, typ, "")
		if err != nil {
			l.setErr(err)
			return
		}
		var facts []string
		var steps []stepTitleMaterial
		for _, n := range nodes {
			if old, ok := texts[n.ID]; ok && (old.SourceRev == 0 || old.SourceRev == sources[n.ID]) {
				continue
			}
			if typ == rings.NodeFact {
				if n.Status == "confirmed" {
					facts = append(facts, n.ID)
				}
			} else if kind, _ := n.Detail["kind"].(string); kind == "" {
				steps = append(steps, stepTitleMaterial{StepID: n.ID, Intent: n.Title})
			}
		}
		l.queueFactTexts(ctx, facts, sources)
		for start := 0; start < len(steps); start += 8 {
			l.queueStepTitles(ctx, steps[start:min(start+8, len(steps))], sources)
		}
	}
	events, err := l.Store.EventsByKinds(ctx, l.TaskID, rings.EventOvermindRecord, rings.EventOvermindPolish, rings.EventClaimText)
	if err != nil {
		l.setErr(err)
		return
	}
	assessDone := map[int64]bool{}
	claimDone := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case rings.EventOvermindPolish:
			var p rings.OvermindPolish
			if json.Unmarshal(event.Payload, &p) == nil {
				assessDone[p.RecRev] = true
			}
		case rings.EventClaimText:
			var p rings.ClaimTextPolish
			if json.Unmarshal(event.Payload, &p) == nil {
				claimDone[displayKey("claim", p.ClaimID, p.SourceRev)] = true
			}
		}
	}
	for _, event := range events {
		if event.Kind != rings.EventOvermindRecord || assessDone[event.Revision] {
			continue
		}
		var rec rings.OvermindRecord
		if json.Unmarshal(event.Payload, &rec) == nil && rec.Type == rings.OvermindRecAssessment {
			l.polishAssessmentAsync(ctx, event.Revision, rec)
		}
	}
	claims, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeTestClaim, "")
	if err != nil {
		l.setErr(err)
		return
	}
	for _, n := range claims {
		if old, _ := n.Detail["combined"].(bool); old {
			continue
		}
		source, err := l.Store.ClaimTextSourceRev(ctx, l.TaskID, n.ID)
		if err != nil {
			l.setErr(err)
			continue
		}
		if claimDone[displayKey("claim", n.ID, source)] {
			continue
		}
		if n.Status == rings.TestClaimConfirmed || n.Status == rings.TestClaimFixed {
			row, ok, err := l.Store.ClaimCaseOf(ctx, l.TaskID, n.ID)
			if err != nil || !ok || row.Repro == nil || row.Repro.StepsIncomplete != "流程步骤整理中" || len(row.Repro.ProofSeqs) == 0 {
				continue
			}
			event, err := l.Store.EventByRevision(ctx, l.TaskID, row.Repro.Rev)
			if err != nil {
				l.setErr(err)
				continue
			}
			var rec rings.TestClaimRecord
			if json.Unmarshal(event.Payload, &rec) != nil {
				continue
			}
			var verdict rings.TestClaimRecord
			if event, err := l.Store.EventByRevision(ctx, l.TaskID, row.VerdictRev); err == nil {
				_ = json.Unmarshal(event.Payload, &verdict)
			}
			l.polishClaimPackageAsync(ctx, n.ID, source, rec, verdict.Conditions, rec.ProofSeqs)
		} else if n.Status == rings.TestClaimCandidate || n.Status == rings.TestClaimInsufficient || n.Status == rings.TestClaimRefuted {
			l.polishClaimTextAsync(ctx, n.ID)
		}
	}
}
