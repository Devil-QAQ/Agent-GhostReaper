package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
)

// ---- P2-08c:测试连接 / 从 API 拉模型列表 / 立即恢复 / 视图 v2 ----

// TestModelTestConnection 测试连接:真请求经配置实际参数,回复原文与
// 用量呈现;key 不出现在任何响应里;坏端点如实报错。
func TestModelTestConnection(t *testing.T) {
	srv, h := newModelsTestServer(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"收到,链路正常"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":6}}`))
	}))
	t.Cleanup(endpoint.Close)

	// 库内配置测试(name 引用,key 不回传)。
	saved := map[string]any{}
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"t1","format":"openai","base_url":"`+endpoint.URL+`",
		"api_key":"sk-plain-123456","model":"m-1","thinking":"enabled","thinking_effort":"low"}`,
		http.StatusOK, &saved)
	var res map[string]any
	post(t, h, http.MethodPost, "/api/models/test", `{"name":"t1"}`, http.StatusOK, &res)
	if res["ok"] != true || res["text"] != "收到,链路正常" || res["model"] != "m-1" {
		t.Fatalf("测试连接应呈现回复原文: %+v", res)
	}
	usage, _ := res["usage"].(map[string]any)
	if usage == nil || usage["output_tokens"] != float64(6) {
		t.Fatalf("用量应呈现: %+v", res["usage"])
	}
	if v, _ := res["latency_ms"].(float64); v < 0 {
		t.Fatalf("延迟应非负: %v", res["latency_ms"])
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "sk-plain-123456") {
		t.Fatal("测试响应不得含明文 key")
	}

	// 草稿测试(未保存的编辑值;key 沿用库内同名)。
	post(t, h, http.MethodPost, "/api/models/test", `{
		"name":"t1","config":{"format":"openai","base_url":"`+endpoint.URL+`","api_key":"","model":"m-draft"}}`,
		http.StatusOK, &res)
	if res["model"] != "m-draft" {
		t.Fatalf("草稿应以编辑值为准: %+v", res)
	}

	// 坏端点:如实报错(不 200)。
	post(t, h, http.MethodPost, "/api/models/test", `{"name":"nope"}`, http.StatusBadRequest, &res)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"auth"}}`))
	}))
	t.Cleanup(bad.Close)
	post(t, h, http.MethodPost, "/api/models/test", `{
		"config":{"format":"openai","base_url":"`+bad.URL+`","api_key":"k","model":"m"}}`,
		http.StatusBadGateway, &res)
	if !strings.Contains(res["error"].(string), "测试失败") {
		t.Fatalf("坏端点应如实报错: %+v", res)
	}
	_ = srv
}

// TestModelFetchListEndpoint 从 API 拉模型列表:草稿(未保存)与库内
// 配置皆可;响应只含清单。
func TestModelFetchListEndpoint(t *testing.T) {
	_, h := newModelsTestServer(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-list" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m-b"},{"id":"m-a","context_window":262144}]}`))
	}))
	t.Cleanup(endpoint.Close)

	var res map[string]any
	post(t, h, http.MethodPost, "/api/models/fetch-list", `{
		"config":{"format":"openai","base_url":"`+endpoint.URL+`","api_key":"sk-list"}}`,
		http.StatusOK, &res)
	models, _ := res["models"].([]any)
	first, _ := models[0].(map[string]any)
	if res["count"] != float64(2) || len(models) != 2 || first["id"] != "m-a" {
		t.Fatalf("拉列表应排序返回: %+v", res)
	}
	if first["context_window"] != float64(262144) || first["window_source"] != "endpoint" {
		t.Fatalf("上下文窗口应随模型带回(端点段): %+v", first)
	}
	// P4-03 ③:端点不带窗口的模型按三段式带回建议值与来源(m-b 不在
	// 对照表,走保守默认)。
	second, _ := models[1].(map[string]any)
	if second["context_window"] != float64(200000) || second["window_source"] != "default" {
		t.Fatalf("默认段建议值不符: %+v", second)
	}
	// 缺端点草稿受控拒绝。
	post(t, h, http.MethodPost, "/api/models/fetch-list", `{"config":{"format":"openai","api_key":"k"}}`,
		http.StatusBadRequest, &res)
	// 端点回 401:如实报 502。
	post(t, h, http.MethodPost, "/api/models/fetch-list", `{
		"config":{"format":"openai","base_url":"`+endpoint.URL+`","api_key":"wrong"}}`,
		http.StatusBadGateway, &res)
}

// TestModelRecoverEndpoint 立即恢复:链上成员熔断开冷却后,面板一键恢复
// (closed、梯度归零);未命中名与未装配链如实报错。
func TestModelRecoverEndpoint(t *testing.T) {
	srv, h := newModelsTestServer(t)
	ctx := context.Background()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(dead.Close)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(ok.Close)
	_ = failover.SaveModelConfig(ctx, srv.Store, failover.ModelConfig{
		Name: "lead", Format: "openai", BaseURL: dead.URL, APIKey: "k", Model: "m-lead"}, "creator")
	_ = failover.SaveModelConfig(ctx, srv.Store, failover.ModelConfig{
		Name: "bak", Format: "openai", BaseURL: ok.URL, APIKey: "k", Model: "m-bak",
		PollDisabled: true}, "creator") // 隔离:无备,主挂即冷却
	_ = failover.SetActive(ctx, srv.Store, failover.RolePrimary, "lead", "creator")
	chain := failover.NewChain(srv.Store, failover.EnvConfig{})
	chain.CooldownLadder = []time.Duration{time.Hour, time.Hour, time.Hour}
	srv.Chain = chain

	// 打开主用熔断(真实路径:经链发一次失败请求,零交付重放耗尽)。
	for range 3 {
		for _, err := range chain.Stream(ctx, llm.CompletionRequest{MaxTokens: 8}) {
			_ = err
			break
		}
	}
	if chain.State().State != "open" {
		t.Fatalf("主用应已熔断: %+v", chain.State())
	}

	var res map[string]any
	post(t, h, http.MethodPost, "/api/models/lead/recover", "", http.StatusOK, &res)
	if res["recovered"] != "lead" {
		t.Fatalf("恢复响应: %+v", res)
	}
	if st := chain.State(); st.State != "closed" || st.Opens != 0 {
		t.Fatalf("立即恢复应关闭且梯度归零: %+v", st)
	}
	post(t, h, http.MethodPost, "/api/models/nope/recover", "", http.StatusBadRequest, &res)
}

// TestModelsGetPresets P4-03 ④:/api/models 随行下发平台 × 格式默认端点
// 与平台清单(建模型预填数据源);P4-10 ①:opencode 默认形态改 Go 订阅
// 网关,接入方式双形态随行下发;P4-57 ①:思考回传规则预设与下拉随行。
func TestModelsGetPresets(t *testing.T) {
	_, h := newModelsTestServer(t)
	var res struct {
		Presets          map[string]map[string]string `json:"presets"`
		OCModes          map[string]map[string]string `json:"opencode_modes"`
		PassbackPresets  map[string]map[string]string `json:"passback_presets"`
		PassbackOptions  []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"passback_options"`
		Platforms []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"platforms"`
	}
	get(t, h, "/api/models", http.StatusOK, &res)
	if res.Presets["glm"]["anthropic"] != "https://open.bigmodel.cn/api/anthropic" ||
		res.Presets["deepseek"]["openai"] != "https://api.deepseek.com" ||
		res.Presets["opencode"]["openai"] != "https://opencode.ai/inference/go/openai/v1" ||
		res.Presets["openai"]["openai"] != "https://api.openai.com/v1" {
		t.Fatalf("平台预设不符: %+v", res.Presets)
	}
	if res.OCModes["go"]["anthropic"] != "https://opencode.ai/inference/go/anthropic" ||
		res.OCModes["api"]["openai"] != "https://opencode.ai/zen/v1" ||
		res.OCModes["api"]["anthropic"] != "https://opencode.ai/zen" {
		t.Fatalf("opencode 接入方式双形态不符: %+v", res.OCModes)
	}
	// 思考回传规则预设(P4-57 ①):平台 × 格式映射与三值下拉随行下发。
	if res.PassbackPresets["glm"]["openai"] != "passback" ||
		res.PassbackPresets["deepseek"]["openai"] != "strip" ||
		res.PassbackPresets["deepseek"]["anthropic"] != "passback" ||
		res.PassbackPresets["openai"]["openai"] != "none" {
		t.Fatalf("思考回传规则预设不符: %+v", res.PassbackPresets)
	}
	if len(res.PassbackOptions) != 3 ||
		res.PassbackOptions[0].Value != "passback" || res.PassbackOptions[0].Label != "传回" ||
		res.PassbackOptions[1].Value != "strip" || res.PassbackOptions[1].Label != "剔除" ||
		res.PassbackOptions[2].Value != "none" || res.PassbackOptions[2].Label != "不干预" {
		t.Fatalf("思考回传规则下拉不符: %+v", res.PassbackOptions)
	}
	if len(res.Platforms) != 5 || res.Platforms[3].Value != "openai" || res.Platforms[4].Value != "" {
		t.Fatalf("平台清单不符: %+v", res.Platforms)
	}
}
