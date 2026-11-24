package overmind

import (
	"context"
)

// Only deterministic ledger processing is serialized here. Planning and
// display models never hold this mutex or own the incoming result channel.
func (l *Loop) processResults(ctx context.Context) error {
	l.resultsMu.Lock()
	defer l.resultsMu.Unlock()
	if l.ckptArmed() || (l.Hiber != nil && l.Hiber.Active(l.TaskID)) {
		return nil
	}
	if task, err := l.Store.Task(ctx, l.TaskID); err == nil {
		switch task.Status {
		case "hibernated", "checkpointed", "paused", "stopped", "completed":
			return nil
		}
	}
	harvest, err := Harvest(ctx, l.Store, l.TaskID)
	if err != nil {
		return err
	}
	l.polishConfirmedFactsAsync(ctx, harvest.Confirmed)
	for _, id := range harvest.Confirmed {
		if fact, err := l.Store.Node(ctx, l.TaskID, id); err == nil {
			if claim, _ := fact.Detail["claim_id"].(string); claim != "" {
				// Reuse recovery's package/summary choice. New facts must not
				// replace an in-flight proof package with summary-only work.
				l.recoverDisplay(ctx)
				break
			}
		}
	}
	l.syncRecordedAssets(ctx)
	_, err = l.checkPendingClaims(ctx)
	return err
}

func (l *Loop) polishConfirmedFactsAsync(parent context.Context, ids []string) {
	if l.Provider == nil || len(ids) == 0 {
		return
	}
	sources, err := l.Store.NodeTextSourceRevs(parent, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	l.queueFactTexts(parent, ids, sources)
}

func (l *Loop) queueFactTexts(parent context.Context, ids []string, sources map[string]int64) {
	for start := 0; start < len(ids); start += 8 {
		batch := append([]string(nil), ids[start:min(start+8, len(ids))]...)
		var keys []string
		for _, id := range batch {
			keys = append(keys, displayKey("fact", id, sources[id]))
		}
		l.queueDisplay(parent, keys, func(ctx context.Context, accepted map[string]bool) {
			var selected []string
			for _, id := range batch {
				if accepted[displayKey("fact", id, sources[id])] {
					selected = append(selected, id)
				}
			}
			l.polishFactsAt(ctx, selected, sources)
		})
	}
}
