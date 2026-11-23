package swarm

import (
	"context"
	"ghostreaper/internal/rings"
)

// Callback accounting is per execution segment. Closing admission before Wait
// prevents a callback from being added while its segment is being drained.
func (env *unitEnv) beginCallback(ctx context.Context) bool {
	env.callsMu.Lock()
	defer env.callsMu.Unlock()
	if ctx.Err() != nil || env.callsClosed || env.executionClosed {
		return false
	}
	env.calls.Add(1)
	return true
}
func (env *unitEnv) endCallback() { env.calls.Done() }
func (env *unitEnv) openSegment(cancel context.CancelFunc) {
	env.callsMu.Lock()
	env.callsClosed = false
	env.callbackCancel = cancel
	env.callsMu.Unlock()
	env.auditMu.Lock()
	env.auditedResults = nil
	env.auditMu.Unlock()
	env.reviewMu.Lock()
	env.segCancel = cancel
	env.park = nil
	env.reviewMu.Unlock()
}
func (env *unitEnv) drainCallbacks(final bool) {
	env.callsMu.Lock()
	env.callsClosed = true
	if final {
		env.executionClosed = true
	}
	cancel := env.callbackCancel
	env.callsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	env.calls.Wait()
}
func (env *unitEnv) appendBusiness(ctx context.Context, events ...rings.Event) ([]int64, error) {
	if env.owner == nil {
		return env.Executor.Store.Append(ctx, env.TaskID, events...)
	}
	return env.Executor.Store.AppendForExecution(ctx, env.TaskID, *env.owner, events...)
}
