package websearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const searchCooldown = 30 * time.Second

// serviceError distinguishes unavailable search from query errors and a valid
// empty result. Its message never includes request bodies, keys or proxy URLs.
type serviceError struct {
	message string
	cause   error
}

func (e *serviceError) Error() string { return e.message }
func (e *serviceError) Unwrap() error { return e.cause }
func unavailable(message string, cause error) error {
	return &serviceError{message: message, cause: cause}
}

type taskSearchState struct {
	config  [32]byte
	until   time.Time
	seen    time.Time
	probing bool
	reason  string
}

// TaskSearch shares only temporary service unavailability, never query results
// or target responses. Healthy calls remain parallel. Its zero value is ready.
type TaskSearch struct {
	mu     sync.Mutex
	tasks  map[string]*taskSearchState
	now    func() time.Time
	search func(context.Context, Config, string) ([]Result, error)
}

func (s *TaskSearch) Search(ctx context.Context, task string, cfg Config, query string) ([]Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("搜索词为空:填入关键词后重发")
	}
	// Inactive credentials do not affect the effective configuration identity.
	cfg.Backend = NormalizeBackend(cfg.Backend)
	if cfg.Backend != BackendBrave {
		cfg.BraveKey = ""
	}
	if cfg.Backend != BackendTavily {
		cfg.TavilyKey = ""
	}
	encoded, _ := json.Marshal(cfg)
	fingerprint := sha256.Sum256(encoded)
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	s.mu.Lock()
	if s.tasks == nil {
		s.tasks = map[string]*taskSearchState{}
	}
	for id, state := range s.tasks {
		if id != task && !state.probing && now.Sub(state.seen) > searchCooldown {
			delete(s.tasks, id)
		}
	}
	state := s.tasks[task]
	if state == nil || state.config != fingerprint {
		state = &taskSearchState{config: fingerprint}
		s.tasks[task] = state
	}
	state.seen = now
	if now.Before(state.until) {
		reason, until := state.reason, state.until
		s.mu.Unlock()
		return nil, fmt.Errorf("搜索服务暂不可用：%s；%s后可重试，期间不重复请求。可继续其他工作或更改搜索配置。", reason, until.Format("15:04:05"))
	}
	if state.probing {
		s.mu.Unlock()
		return nil, errors.New("搜索服务正在检查恢复，当前查询未出站；可继续其他工作")
	}
	if !state.until.IsZero() {
		state.probing = true
	}
	s.mu.Unlock()
	search := s.search
	if search == nil {
		search = Search
	}
	results, err := search(ctx, cfg, query)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks[task] != state {
		return results, err
	} // A newer configuration owns this task.
	state.probing = false
	if err == nil {
		state.until = time.Time{}
		state.reason = ""
		return results, nil
	}
	var service *serviceError
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && errors.As(err, &service) {
		at := time.Now()
		if s.now != nil {
			at = s.now()
		}
		state.until = at.Add(searchCooldown)
		state.reason = service.Error()
	}
	return results, err
}
