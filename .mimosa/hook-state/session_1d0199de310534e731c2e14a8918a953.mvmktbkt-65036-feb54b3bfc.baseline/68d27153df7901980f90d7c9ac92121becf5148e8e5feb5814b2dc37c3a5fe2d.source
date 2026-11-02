package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/websearch"
)

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

// TestServerSearchConfigAPI 搜索配置读写(P3-03):缺省 ddgs 免 key、
// 写后读回同源、key 永不回显明文(响应只含打码)、代理去凭据、
// 三态 key 语义(未提交=不改/空串=清除/非空=设置)、坏值 400 不落库。
func TestServerSearchConfigAPI(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	// 缺省读:后端 ddgs,无 key 无代理。
	var cfg ConfigView
	get(t, h, "/api/config", http.StatusOK, &cfg)
	if cfg.Search.Backend != websearch.BackendDDGS || cfg.Search.BraveHasKey || cfg.Search.ProxySet {
		t.Fatalf("缺省搜索配置不符: %+v", cfg.Search)
	}

	// 写:切 brave + key + 带凭据代理;响应与 GET 同源打码。
	var sv SearchView
	post(t, h, "POST", "/api/config/search",
		`{"backend":"brave","brave_key":"BSAverylongkey1234","proxy":"http://u:p@127.0.0.1:7890"}`,
		http.StatusOK, &sv)
	if sv.Backend != websearch.BackendBrave || !sv.BraveHasKey || sv.BraveKeyMask == "" ||
		strings.Contains(sv.BraveKeyMask, "longkey") || sv.BraveKeyMask == "BSAverylongkey1234" {
		t.Fatalf("brave key 应打码呈现: %+v", sv)
	}
	if !sv.ProxySet || sv.ProxyMask != "http://***@127.0.0.1:7890" {
		t.Fatalf("代理应去凭据呈现: %+v", sv)
	}
	get(t, h, "/api/config", http.StatusOK, &cfg)
	if cfg.Search.Backend != websearch.BackendBrave || cfg.Search.BraveKeyMask != sv.BraveKeyMask ||
		cfg.Search.ProxyMask != sv.ProxyMask {
		t.Fatalf("GET 应与写响应同源: %+v vs %+v", cfg.Search, sv)
	}

	// 明文永不进任何响应体(全量响应文本级核对)。
	rec := doReq(t, h, http.MethodGet, "/api/config", "")
	if strings.Contains(rec.Body.String(), "BSAverylongkey1234") {
		t.Fatalf("key 明文不得出现在响应体")
	}
	// 键值表存原文(工具运行时消费),带审计。
	e, ok, err := store.Setting(ctx, websearch.SettingKeyBraveKey)
	if err != nil || !ok || e.Value != "BSAverylongkey1234" || e.UpdatedBy != "creator" {
		t.Fatalf("键值表直查不符: ok=%v err=%v %+v", ok, err, e)
	}

	// 三态 key:未提交字段 = 不改;空串 = 清除(omitempty 字段用新变量
	// 解码,防上次响应残值)。
	post(t, h, "POST", "/api/config/search", `{"backend":"brave"}`, http.StatusOK, &sv)
	if !sv.BraveHasKey {
		t.Fatalf("未提交 brave_key 应保持已配置: %+v", sv)
	}
	var cleared SearchView
	post(t, h, "POST", "/api/config/search", `{"backend":"brave","brave_key":""}`, http.StatusOK, &cleared)
	if cleared.BraveHasKey || cleared.BraveKeyMask != "" {
		t.Fatalf("空串应清除 key: %+v", cleared)
	}

	// 坏值:未知后端 400、坏代理 400,不落库。
	post(t, h, "POST", "/api/config/search", `{"backend":"brav"}`, http.StatusBadRequest, nil)
	post(t, h, "POST", "/api/config/search", `{"backend":"ddgs","proxy":"ftp://x"}`, http.StatusBadRequest, nil)
	post(t, h, "POST", "/api/config/search", `not-json`, http.StatusBadRequest, nil)
	if e, ok, _ := store.Setting(ctx, websearch.SettingKeyBackend); !ok || e.Value != "brave" {
		t.Fatalf("非法请求不得改动后端: ok=%v %+v", ok, e)
	}

	// 切回 ddgs 默认:面板一次往返。
	post(t, h, "POST", "/api/config/search", `{"backend":"ddgs","proxy":""}`, http.StatusOK, &sv)
	if sv.Backend != websearch.BackendDDGS || sv.ProxySet {
		t.Fatalf("切回默认不符: %+v", sv)
	}
}
