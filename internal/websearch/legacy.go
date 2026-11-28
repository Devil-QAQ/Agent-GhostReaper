package websearch

import (
	"context"
	"errors"
	"strings"

	"ghostreaper/internal/rings"
)

// Only upgrade conversion knows the retired provider types. Runtime execution
// uses the same editable HTTP request/response configuration for every service.
var legacyKeys = []string{"search.backend", "search.brave.key", "search.tavily.key", "search.proxy"}

func migrateLegacy(ctx context.Context, store *rings.Store) ([]Service, error) {
	entries, err := store.SettingsWithPrefix(ctx, "search.")
	if err != nil {
		return nil, errors.New("旧搜索配置无法读取")
	}
	old := map[string]string{}
	for _, entry := range entries {
		old[entry.Key] = strings.TrimSpace(entry.Value)
	}
	selected := old["search.backend"]
	list := []Service{}
	// No explicitly saved backend or credential means a fresh empty catalog.
	for _, kind := range []string{"ddgs", "brave", "tavily"} {
		key := old["search."+kind+".key"]
		if selected != kind && key == "" {
			continue
		}
		cfg := Config{Method: "GET", Encoding: "json", Headers: map[string]string{}, Params: map[string]any{"q": "{{query}}"}, ResponseMode: "json", ResultsPath: "results", TitlePath: "title", URLPath: "url", SnippetPath: "content", APIKey: key, Proxy: old["search.proxy"]}
		name := ""
		switch kind {
		case "ddgs":
			name = "DuckDuckGo"
			cfg.Endpoint = "https://html.duckduckgo.com/html/"
			cfg.Method = "POST"
			cfg.Encoding = "form"
			cfg.ResponseMode = "text"
		case "brave":
			name = "Brave"
			cfg.Endpoint = "https://api.search.brave.com/res/v1/web/search"
			cfg.Headers["X-Subscription-Token"] = "{{api_key}}"
			cfg.Params["count"] = MaxResults
			cfg.ResultsPath = "web.results"
			cfg.SnippetPath = "description"
		case "tavily":
			name = "Tavily"
			cfg.Endpoint = "https://api.tavily.com/search"
			cfg.Method = "POST"
			cfg.Params = map[string]any{"query": "{{query}}", "max_results": MaxResults}
			cfg.Headers["Authorization"] = "Bearer {{api_key}}"
		}
		id, err := serviceID()
		if err != nil {
			return nil, err
		}
		list = append(list, Service{ID: id, Name: name, Enabled: selected == kind, Config: cfg})
	}
	return list, nil
}
