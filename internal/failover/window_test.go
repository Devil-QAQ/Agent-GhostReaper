package failover

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(t.TempDir() + "/rings.db")
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestContextWindowFieldAndResolve 模型库上下文窗口字段:存取往返、
// 打码视图携带、负数拒绝;ContextWindowFor 按服务模型解析(优先
// Model 匹配,未命中退主用配置,再退 0)。
func TestContextWindowFieldAndResolve(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	if err := SaveModelConfig(ctx, store, ModelConfig{
		Name: "glm", Format: "openai", BaseURL: "http://x", APIKey: "sk-abc1234567",
		Model: "glm-flash", ContextWindow: 128000,
	}, "creator"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{
		Name: "ds", Format: "openai", BaseURL: "http://y", APIKey: "sk-def1234567",
		Model: "ds-flash", ContextWindow: 64000,
	}, "creator"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{
		Name: "bad", Format: "openai", BaseURL: "http://z", APIKey: "sk-xyz1234567",
		Model: "bad", ContextWindow: -1,
	}, "creator"); err == nil {
		t.Fatalf("负窗口应被拒绝")
	}

	// 存取往返 + 打码视图携带窗口(非敏感字段)。
	cfg, ok, err := ModelConfigByName(ctx, store, "glm")
	if err != nil || !ok || cfg.ContextWindow != 128000 {
		t.Fatalf("窗口字段存取不符: %+v ok=%v err=%v", cfg, ok, err)
	}
	masked := cfg.Masked()
	if masked.ContextWindow != 128000 || masked.APIKeyMask == "sk-abc1234567" {
		t.Fatalf("打码视图应带窗口不带明文 key: %+v", masked)
	}

	// 解析:按服务模型;未命中退主用;无匹配退 0。
	if err := SetActive(ctx, store, RolePrimary, "glm", "creator"); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	if w := ContextWindowFor(ctx, store, "ds-flash"); w != 64000 {
		t.Fatalf("按模型解析应得 64000,实际 %d", w)
	}
	if w := ContextWindowFor(ctx, store, "unknown-model"); w != 128000 {
		t.Fatalf("未命中应退主用窗口 128000,实际 %d", w)
	}
	if w := ContextWindowFor(ctx, store, ""); w != 128000 {
		t.Fatalf("空模型应退主用窗口 128000,实际 %d", w)
	}
}
