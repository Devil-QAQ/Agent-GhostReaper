package failover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// P4-03 ③④:内置对照表、三段式窗口决策、平台 × 格式默认端点。
// 对照表数值与官方规格核对(2026-10):glm 系 docs.bigmodel.cn 模型页、
// deepseek 系端点自报、claude/gpt 系官方公开规格。

func TestLookupContextWindow(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{
		{"glm-5.3", 1000000},
		{"glm-5.3-flash", 1000000},  // 5.3 前缀(官方 1M)
		{"glm-5.3-flashx", 1000000}, // 同上
		{"glm-5.2", 1000000},
		{"glm-5.1", 200000},
		{"glm-5", 200000},
		{"glm-5-turbo", 200000}, // glm-5 前缀(无独立规格页,近似)
		{"glm-4.7", 200000},
		{"glm-4.6", 200000},
		{"glm-4.5-air", 128000},
		{"glm-4.5", 128000},
		{"GLM-5.3", 1000000}, // 大小写不敏感
		{"deepseek-flash", 1048576},
		{"deepseek-v4-pro", 1048576},
		{"deepseek-chat", 128000},
		{"deepseek-reasoner", 128000},
		{"claude-sonnet-4-5", 200000},
		{"claude-opus-4-5-20260101", 200000}, // 带日期后缀同样命中
		{"gpt-5.2", 400000},
		{"gpt-5", 400000},
		{"gpt-4.1", 1000000},
		{"gpt-4o", 128000},
		{"gpt-4o-mini", 128000},
		{"big-pickle", 0}, // opencode 免费别名:不匹配(实测形态)
		{"mimo-v2.6-flash-free", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := LookupContextWindow(c.id); got != c.want {
			t.Errorf("LookupContextWindow(%q) = %d, want %d", c.id, got, c.want)
		}
	}
}

func TestSuggestedContextWindow(t *testing.T) {
	if w, src := suggestedContextWindow("glm-5.3", 0); w != 1000000 || src != "table" {
		t.Fatalf("table 段: got %d/%s, want 1000000/table", w, src)
	}
	if w, src := suggestedContextWindow("m-b", 262144); w != 262144 || src != "endpoint" {
		t.Fatalf("endpoint 段: got %d/%s, want 262144/endpoint", w, src)
	}
	// 端点值优先于对照表(端点最准)。
	if w, src := suggestedContextWindow("glm-5.3", 131072); w != 131072 || src != "endpoint" {
		t.Fatalf("endpoint 优先: got %d/%s, want 131072/endpoint", w, src)
	}
	if w, src := suggestedContextWindow("big-pickle", 0); w != DefaultContextWindow || src != "default" {
		t.Fatalf("default 段: got %d/%s, want %d/default", w, src, DefaultContextWindow)
	}
}

// TestFetchModelListThreeStages 拉列表逐模型三段式:一个端点带 context
// 字段、一个 glm 命中对照表、一个未知模型走保守默认(三类同一响应内
// 各走一例;端点真实形态:GLM 无 context 字段、DeepSeek 带)。
func TestFetchModelListThreeStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "deepseek-flash", "context_window": 1048576, "max_output_tokens": 393216},
				{"id": "glm-5.3"},
				{"id": "big-pickle"},
			},
		})
	}))
	defer srv.Close()

	list, err := FetchModelList(context.Background(), ModelConfig{
		Format: "openai", BaseURL: srv.URL, APIKey: "k",
	})
	if err != nil {
		t.Fatalf("FetchModelList: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, m := range list {
		byID[m.ID] = m
	}
	if m := byID["deepseek-flash"]; m.ContextWindow != 1048576 || m.WindowSource != "endpoint" || m.MaxOutput != 393216 {
		t.Fatalf("endpoint 段: %+v", m)
	}
	if m := byID["glm-5.3"]; m.ContextWindow != 1000000 || m.WindowSource != "table" {
		t.Fatalf("table 段: %+v", m)
	}
	if m := byID["big-pickle"]; m.ContextWindow != 200000 || m.WindowSource != "default" {
		t.Fatalf("default 段: %+v", m)
	}
}

func TestPlatformPresets(t *testing.T) {
	cases := []struct{ platform, format, want string }{
		{"glm", "anthropic", "https://open.bigmodel.cn/api/anthropic"},
		{"glm", "openai", "https://open.bigmodel.cn/api/paas/v4"},
		{"deepseek", "anthropic", "https://api.deepseek.com/anthropic"},
		{"deepseek", "openai", "https://api.deepseek.com"},
		{"opencode", "anthropic", "https://opencode.ai/inference/go/anthropic"}, // P4-10:默认形态 Go 订阅(不带 /v1)
		{"opencode", "openai", "https://opencode.ai/inference/go/openai/v1"},
		{"glm", "openai-responses", ""}, // 非预设组合:纯手动
		{"", "openai", ""},              // 自定义平台
		{"unknown", "openai", ""},
		{"GLM", "Anthropic", "https://open.bigmodel.cn/api/anthropic"}, // 归一
	}
	for _, c := range cases {
		if got := PlatformDefaultURL(c.platform, c.format); got != c.want {
			t.Errorf("PlatformDefaultURL(%q,%q) = %q, want %q", c.platform, c.format, got, c.want)
		}
	}
	if !IsKnownPlatform("glm") || IsKnownPlatform("") || IsKnownPlatform("x") {
		t.Fatal("IsKnownPlatform 词表判定异常")
	}
	// 平台清单含自定义项且顺序稳定。
	opts := PlatformOptions()
	if len(opts) != 4 || opts[3].Value != "" || opts[3].Label != "自定义" {
		t.Fatalf("PlatformOptions: %+v", opts)
	}
	// 预设快照与主表一致(面板下发不漏格式)。
	presets := PlatformPresetURLs()
	if len(presets) != len(platformPresets) || len(presets["glm"]) != 2 {
		t.Fatalf("PlatformPresetURLs: %+v", presets)
	}
}
