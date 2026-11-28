package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/websearch"
)

func TestServerAssetCards(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	var catalog struct {
		Services  []searchServiceView       `json:"services"`
		Providers []websearch.AssetProvider `json:"providers"`
	}
	get(t, h, "/api/search-services", http.StatusOK, &catalog)
	if len(catalog.Services) != 0 || len(catalog.Providers) != len(websearch.SiteProviders()) {
		t.Fatal("support definitions created configured services")
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"error": false, "results": []any{}, "consumed_fpoint": 2})
	}))
	defer api.Close()
	post(t, h, http.MethodPost, "/api/search-services", `{"name":"FOFA","kind":"fofa","enabled":true}`, http.StatusOK, &catalog)
	card := catalog.Services[0]
	if card.Available || card.HasKey || card.AssetQuota == nil {
		t.Fatal("missing-key status")
	}
	endpoint := "/api/search-services/" + card.ID
	post(t, h, http.MethodPost, endpoint+"/assets", `{"query":"domain=\"example.com\""}`, http.StatusBadRequest, nil)
	b, _ := json.Marshal(map[string]any{"name": "FOFA", "kind": "fofa", "enabled": true, "api_key": "private-assets-key", "url": api.URL, "per_minute": 1})
	post(t, h, http.MethodPost, endpoint, string(b), http.StatusOK, &catalog)
	if !catalog.Services[0].Available || !catalog.Services[0].HasKey {
		t.Fatal("configured service unavailable")
	}
	post(t, h, http.MethodPost, endpoint+"/assets", `{"query":"domain=\"example.com\""}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, endpoint+"/assets", `{"query":"domain=\"example.com\""}`, http.StatusBadRequest, nil)
	r := doReq(t, h, http.MethodGet, "/api/search-services", "")
	if strings.Contains(r.Body.String(), "private-assets-key") {
		t.Fatal("key exposed")
	}
	json.Unmarshal(r.Body.Bytes(), &catalog)
	if catalog.Services[0].AssetQuota.MinuteUsed != 1 || catalog.Services[0].AssetQuota.RetryAt.IsZero() {
		t.Fatal("panel quota missing")
	}
	post(t, h, http.MethodDelete, endpoint, `{}`, http.StatusOK, &catalog)
	if len(catalog.Services) != 0 {
		t.Fatal("delete failed")
	}
}

// newSettingsServer 构建带键值表的面板后端(设置页预算 API 测试共用)。
func newSettingsServer(t *testing.T) (*Server, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	return srv, store
}

// TestServerBudgetConfigAPIDeleted 旧设置页预算写端点已删(P4-01:正式
// 写入口唯一化到 /api/agents/params,旁路写清除):404,键不落库。
func TestServerBudgetConfigAPIDeleted(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	post(t, h, "POST", "/api/config/budget", `{"max_turns":5}`, http.StatusNotFound, nil)
	if _, ok, _ := store.Setting(context.Background(), swarm.SettingKeyMaxTurns); ok {
		t.Fatal("已删端点不得落库")
	}
}

func TestServerSearchServiceCards(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	var catalog struct {
		Services []searchServiceView `json:"services"`
	}
	get(t, h, "/api/search-services", http.StatusOK, &catalog)
	if len(catalog.Services) != 0 {
		t.Fatal("new installations must have no search services")
	}
	post(t, h, http.MethodPost, "/api/config/search", `{"backend":"ddgs"}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/search-services", `{"name":"资料搜索","url":"https://search.example/api","enabled":true,"api_key":"BSAverylongkey1234","proxy":"http://u:p@127.0.0.1:7890"}`, http.StatusOK, &catalog)
	if len(catalog.Services) != 1 {
		t.Fatal("service not added")
	}
	card := catalog.Services[0]
	if !card.HasKey || !card.ProxySet || card.KeyMask == "BSAverylongkey1234" || card.ProxyMask != "http://***@127.0.0.1:7890" {
		t.Fatalf("credentials not masked %+v", card)
	}
	response := doReq(t, h, http.MethodGet, "/api/search-services", "")
	if strings.Contains(response.Body.String(), "BSAverylongkey1234") || strings.Contains(response.Body.String(), "u:p") {
		t.Fatal("credentials returned in plaintext")
	}
	endpoint := "/api/search-services/" + card.ID
	post(t, h, http.MethodPost, endpoint, `{"name":"改名","url":"https://search.example/api","enabled":false}`, http.StatusOK, &catalog)
	if catalog.Services[0].Enabled || !catalog.Services[0].HasKey || !catalog.Services[0].ProxySet {
		t.Fatal("omitted credentials were lost")
	}
	configured, err := websearch.Services(t.Context(), store)
	if err != nil || configured[0].APIKey != "BSAverylongkey1234" {
		t.Fatal("service credentials not persisted")
	}
	post(t, h, http.MethodPost, endpoint, `{"name":"改名","url":"https://search.example/api","enabled":true,"api_key":"","proxy":""}`, http.StatusOK, &catalog)
	if catalog.Services[0].HasKey || catalog.Services[0].ProxySet {
		t.Fatal("explicit credential removal failed")
	}
	post(t, h, http.MethodPost, endpoint, `{"name":"invalid","url":"ftp://invalid","enabled":true}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, endpoint, `{"name":"invalid","enabled":true,"proxy":"ftp://x"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/search-services/missing", `{"name":"missing","enabled":true}`, http.StatusNotFound, nil)
	post(t, h, http.MethodDelete, endpoint, `{}`, http.StatusOK, &catalog)
	if len(catalog.Services) != 0 {
		t.Fatal("service deletion failed")
	}
	get(t, h, "/api/search-services", http.StatusOK, &catalog)
	if len(catalog.Services) != 0 {
		t.Fatal("deleted service returned")
	}
	_, err = websearch.ResolveService(t.Context(), store, "")
	if err != websearch.ErrNotConfigured {
		t.Fatalf("empty configuration has an implicit provider: %v", err)
	}
}

func TestServerGitHubCard(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	var catalog struct {
		Services []searchServiceView `json:"services"`
	}
	post(t, h, http.MethodPost, "/api/search-services", `{"name":"GitHub","kind":"github","enabled":true}`, http.StatusOK, &catalog)
	if len(catalog.Services) != 1 {
		t.Fatal("missing card")
	}
	card := catalog.Services[0]
	if card.PerMinute != 9 || card.PerHour != 54 || card.Quota == nil || card.HasKey {
		t.Fatalf("anonymous %+v", card)
	}
	endpoint := "/api/search-services/" + card.ID
	post(t, h, http.MethodPost, endpoint, `{"name":"GitHub","kind":"github","enabled":true,"api_key":"private-pat-for-test"}`, http.StatusOK, &catalog)
	if catalog.Services[0].PerMinute != 27 || catalog.Services[0].PerHour != 4500 || !catalog.Services[0].HasKey {
		t.Fatal("authenticated defaults")
	}
	r := doReq(t, h, http.MethodGet, "/api/search-services", "")
	if strings.Contains(r.Body.String(), "private-pat-for-test") {
		t.Fatal("token leaked")
	}
	entry, ok, err := store.Setting(t.Context(), websearch.SettingKeyServices)
	if err != nil || !ok || !strings.Contains(entry.Value, "private-pat-for-test") {
		t.Fatal("token not preserved through encrypted settings")
	}
	post(t, h, http.MethodPost, endpoint, `{"name":"GitHub","kind":"github","enabled":false,"custom_limits":true,"per_minute":2,"per_hour":20}`, http.StatusOK, &catalog)
	if !catalog.Services[0].HasKey || catalog.Services[0].PerMinute != 2 {
		t.Fatal("custom/save token")
	}
	post(t, h, http.MethodPost, endpoint+"/github", `{"query":"poc"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, endpoint, `{"name":"GitHub","kind":"github","enabled":true,"api_key":""}`, http.StatusOK, &catalog)
	if catalog.Services[0].HasKey || catalog.Services[0].PerHour != 54 {
		t.Fatal("clear token")
	}
	post(t, h, http.MethodPost, endpoint+"/github", `{"action":"read","repo":"../invalid","path":"x"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/search-services", `{"name":"duplicate","kind":"github","enabled":true}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodDelete, endpoint, `{}`, http.StatusOK, &catalog)
	if len(catalog.Services) != 0 {
		t.Fatal("delete")
	}
}
