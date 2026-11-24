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
	object string
	source int64
}

// queueDisplay bounds both running model jobs and retained material. Missing
// display work stays recoverable from its existing business/display records.
// A key names a content version; repeated notifications never start it twice.
func (l *Loop) queueDisplay(parent context.Context, keys []string, run func(context.Context, map[string]bool)) bool {
	return l.enqueueDisplay(parent, keys, "", 0, run)
}

// Coalesce pending revisions of one object in the existing bounded queue.
// Running work for the same object is serialized; unrelated displays retain
// their normal concurrency. There is no timer or separate durable job table.
func (l *Loop) queueLatestDisplay(parent context.Context, kind, id string, source int64, run func(context.Context, map[string]bool)) bool {
	return l.enqueueDisplay(parent, []string{displayKey(kind, id, source)}, kind+":"+id, source, run)
}

func (l *Loop) enqueueDisplay(parent context.Context, keys []string, object string, source int64, run func(context.Context, map[string]bool)) bool {
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
	if object != "" {
		if running, ok := l.polishRunning[object]; ok && running >= source {
			return false
		}
		for i, pending := range l.polishQueue {
			if pending.object != object {
				continue
			}
			if pending.source >= source {
				return false
			}
			for key := range pending.keys {
				delete(l.polishKnown, key)
			}
			for key := range unique {
				l.polishKnown[key] = true
			}
			l.polishQueue[i] = displayJob{keys: unique, parent: parent, run: run, object: object, source: source}
			return true
		}
	}
	if len(l.polishQueue) >= displayBacklog {
		l.polishDeferred = true
		return false
	}
	for key := range unique {
		l.polishKnown[key] = true
	}
	l.polishQueue = append(l.polishQueue, displayJob{keys: unique, parent: parent, run: run, object: object, source: source})
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
		index := -1
		for i, job := range l.polishQueue {
			if _, running := l.polishRunning[job.object]; job.object == "" || !running {
				index = i
				break
			}
		}
		if index < 0 {
			l.polishActive--
			l.polishMu.Unlock()
			return
		}
		job := l.polishQueue[index]
		copy(l.polishQueue[index:], l.polishQueue[index+1:])
		l.polishQueue[len(l.polishQueue)-1] = displayJob{}
		l.polishQueue = l.polishQueue[:len(l.polishQueue)-1]
		if job.object != "" {
			if l.polishRunning == nil {
				l.polishRunning = map[string]int64{}
			}
			l.polishRunning[job.object] = job.source
		}
		l.polishMu.Unlock()
		ctx, cancel := context.WithTimeout(job.parent, polishAsyncTimeout)
		job.run(ctx, job.keys)
		cancel()
		l.polishMu.Lock()
		delete(l.polishRunning, job.object)
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
