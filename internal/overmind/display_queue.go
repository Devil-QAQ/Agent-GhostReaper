package overmind

import (
	"context"
	"fmt"
)

const displayWorkers = 2
const displayBacklog = 16

type displayJob struct {
	keys   map[string]bool
	parent context.Context
	run    func(context.Context, map[string]bool)
}

// queueDisplay bounds both running model jobs and retained material. Missing
// display work stays recoverable from its existing business/display records.
// A key names a content version; repeated notifications never start it twice.
func (l *Loop) queueDisplay(parent context.Context, keys []string, run func(context.Context, map[string]bool)) bool {
	if l.Provider == nil {
		return false
	}
	l.polishMu.Lock()
	defer l.polishMu.Unlock()
	if l.polishCtx != nil {
		parent = l.polishCtx
	}
	if parent.Err() != nil {
		return false
	}
	if l.polishKnown == nil {
		l.polishKnown = map[string]bool{}
	}
	unique := map[string]bool{}
	for _, key := range keys {
		if !l.polishKnown[key] {
			unique[key] = true
		}
	}
	if len(unique) == 0 {
		return false
	}
	if len(l.polishQueue) >= displayBacklog {
		l.polishDeferred = true
		return false
	}
	for key := range unique {
		l.polishKnown[key] = true
	}
	l.polishQueue = append(l.polishQueue, displayJob{keys: unique, parent: parent, run: run})
	l.polishWG.Add(1)
	if l.polishActive < displayWorkers {
		l.polishActive++
		go l.runDisplayJobs()
	}
	return true
}

func (l *Loop) runDisplayJobs() {
	for {
		l.polishMu.Lock()
		if len(l.polishQueue) == 0 {
			l.polishActive--
			l.polishMu.Unlock()
			return
		}
		job := l.polishQueue[0]
		l.polishQueue[0] = displayJob{}
		l.polishQueue = l.polishQueue[1:]
		l.polishMu.Unlock()
		ctx, cancel := context.WithTimeout(job.parent, polishAsyncTimeout)
		job.run(ctx, job.keys)
		cancel()
		l.polishMu.Lock()
		if job.parent.Err() != nil {
			for key := range job.keys {
				delete(l.polishKnown, key)
			}
		}
		deferred := l.polishDeferred && job.parent.Err() == nil
		l.polishDeferred = false
		l.polishMu.Unlock()
		if deferred {
			l.recoverDisplay(job.parent)
		}
		l.polishWG.Done()
	}
}

func displayKey(kind, id string, source int64) string {
	return fmt.Sprintf("%s:%s:%d", kind, id, source)
}
