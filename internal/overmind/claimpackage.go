package overmind

import (
	"context"
	"encoding/json"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// A slow presentation model cannot hold the machine verdict. The sealed
// package is already durable; this existing display event can only select
// recorded steps and describe them, under the sealed business version.
func (l *Loop) queueClaimTextAt(parent context.Context, id string, source int64) {
	l.queueLatestDisplay(parent, "claim", id, source, func(ctx context.Context, _ map[string]bool) {
		l.polishClaimTextAt(ctx, id, source)
	})
}

func (l *Loop) claimDisplayCurrent(ctx context.Context, id string, source int64) bool {
	if ctx.Err() != nil {
		return false
	}
	current, err := l.Store.ClaimTextSourceRev(ctx, l.TaskID, id)
	return err == nil && current == source
}

// Read the sealed candidate material only after this object reaches the head
// of the queue. Supplemental facts can advance the source while the package
// is pending; they must not replace unfinished flow selection with summary-only
// work. Intermediate generations stop at their next model-call boundary.
func (l *Loop) polishClaimPackageAt(ctx context.Context, id string, source int64, row *rings.ClaimCaseProjection) {
	event, err := l.Store.EventByRevision(ctx, l.TaskID, row.Repro.Rev)
	if err != nil {
		l.setErr(err)
		return
	}
	var rec rings.TestClaimRecord
	if err := json.Unmarshal(event.Payload, &rec); err != nil {
		l.setErr(err)
		return
	}
	l.pickReproSteps(ctx, id, &rec, ReproPoolDiag{}, rec.ProofSeqs)
	if !l.claimDisplayCurrent(ctx, id, source) {
		return
	}
	if rec.StepsIncomplete == "流程步骤整理中" {
		rec.StepsIncomplete = ""
	}
	var verdict rings.TestClaimRecord
	if event, err := l.Store.EventByRevision(ctx, l.TaskID, row.VerdictRev); err == nil {
		_ = json.Unmarshal(event.Payload, &verdict)
	}
	l.genClaimNarrative(ctx, id, verdict.Conditions, &rec)
	if !l.claimDisplayCurrent(ctx, id, source) {
		return
	}
	l.genClaimSummary(ctx, id, &rec)
	if !l.claimDisplayCurrent(ctx, id, source) {
		return
	}
	p := rings.ClaimTextPolish{ClaimID: id, SourceRev: source, Title: rec.Title, Statement: rec.Statement, Impact: rec.Impact, Facts: rec.Facts,
		Repro: &rings.ReproDigest{Steps: rec.Steps, Narrative: rec.Narrative, NarrativeUnpolished: rec.NarrativeUnpolished, StepsIncomplete: rec.StepsIncomplete}}
	if _, err := l.Store.AppendDisplay(ctx, l.TaskID, rings.Event{Kind: rings.EventClaimText, Actor: ActorOvermind, Payload: p}); err != nil {
		runlog.Warnf("漏洞显示稿未保存: 任务 %s 判断 %s 原因 %s", l.TaskID, id, clipEval(err.Error()))
	}
}
