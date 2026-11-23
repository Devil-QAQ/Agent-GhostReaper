package overmind

import (
	"context"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// A slow presentation model cannot hold the machine verdict. The sealed
// package is already durable; this existing display event can only select
// recorded steps and describe them, under the sealed business version.
func (l *Loop) polishClaimPackageAsync(parent context.Context, id string, source int64, rec rings.TestClaimRecord, conditions []rings.ArbiterConditionResult, seeds []int) {
	if l.Provider == nil {
		return
	}
	rec.Steps = append([]rings.ReproStep(nil), rec.Steps...)
	l.queueDisplay(parent, []string{displayKey("claim", id, source)}, func(ctx context.Context, _ map[string]bool) {
		if current, err := l.Store.ClaimTextSourceRev(ctx, l.TaskID, id); err != nil || current != source {
			return
		}
		l.pickReproSteps(ctx, id, &rec, ReproPoolDiag{}, seeds)
		if rec.StepsIncomplete == "流程步骤整理中" {
			rec.StepsIncomplete = ""
		}
		l.genClaimNarrative(ctx, id, conditions, &rec)
		l.genClaimSummary(ctx, id, &rec)
		p := rings.ClaimTextPolish{ClaimID: id, SourceRev: source, Title: rec.Title, Statement: rec.Statement, Impact: rec.Impact, Facts: rec.Facts,
			Repro: &rings.ReproDigest{Steps: rec.Steps, Narrative: rec.Narrative, NarrativeUnpolished: rec.NarrativeUnpolished, StepsIncomplete: rec.StepsIncomplete}}
		if _, err := l.Store.AppendDisplay(ctx, l.TaskID, rings.Event{Kind: rings.EventClaimText, Actor: ActorOvermind, Payload: p}); err != nil {
			runlog.Warnf("漏洞显示稿未保存: 任务 %s 判断 %s 原因 %s", l.TaskID, id, clipEval(err.Error()))
		}
	})
}
