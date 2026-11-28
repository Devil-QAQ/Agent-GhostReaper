package websearch

import (
	"context"
	"errors"
	"ghostreaper/internal/rings"
)

type TestPreset struct {
	Query string `json:"query"`
	Size  int    `json:"size,omitempty"`
}

func TestPresets() map[string]TestPreset {
	return map[string]TestPreset{
		"ddpoc":  {"Apache", 1},
		"github": {"Hello-World", 1}, "fofa": {`ip="1.1.1.1"`, 1},
		"hunter": {`ip="1.1.1.1"`, 10}, "quake": {`ip:"1.1.1.1"`, 1},
		"zoomeye": {`ip="1.1.1.1"`, 1}, "daydaymap": {`ip="1.1.1.1"`, 1},
		"zerozone": {"1.1.1.1", 1}, "shodan": {"1.1.1.1", 100},
		"custom": {"example.com", 0},
		"bing":   {"example.com", 1}, "ddgs": {"example.com", 1}, "brave": {"example.com", 1}, "tavily": {"example.com", 1}, "deepseek": {"example.com", 1},
	}
}

// Tests use the form's draft, retain omitted saved secrets, and never save a card.
// Only quota records are persisted, through the same path used by agents.
func TestService(ctx context.Context, store *rings.Store, change ServiceChange) (any, error) {
	var previous Service
	if change.ID != "" {
		list, err := Services(ctx, store)
		if err != nil {
			return nil, err
		}
		found := false
		for _, s := range list {
			if s.ID == change.ID {
				previous = s
				found = true
				break
			}
		}
		if !found {
			return nil, ErrServiceMissing
		}
	}
	cfg, err := serviceConfig(change, previous)
	if err != nil {
		return nil, err
	}
	preset, ok := TestPresets()[cfg.Kind]
	if cfg.Kind == "" {
		preset = TestPresets()["custom"]
		ok = true
	}
	if !ok {
		return nil, errors.New("搜索站点类型无效")
	}
	var result any
	switch {
	case cfg.Kind == "github":
		result, err = githubRequest(ctx, store, cfg, GitHubQuery{Query: preset.Query, perPage: 1}, githubAPI)
	case IsNative(cfg.Kind):
		result, err = searchAsset(ctx, store, cfg, AssetQuery{Query: preset.Query, Size: preset.Size})
	default:
		result, err = Search(ctx, cfg, preset.Query)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"query": preset.Query, "result": result}, nil
}
