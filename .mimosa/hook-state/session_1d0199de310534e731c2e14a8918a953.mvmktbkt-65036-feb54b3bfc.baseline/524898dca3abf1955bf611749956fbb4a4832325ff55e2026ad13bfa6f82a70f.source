package uapolicy

import (
	"context"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

// openUStore 独立账本(uapolicy 测试用,不依赖 swarm 测试助手)。
func openUStore(t *testing.T, dir string) *rings.Store {
	t.Helper()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestPickThreeModes 三模式取值形态:random/tools 命中各自池内(fixed
// 精确原样);fixed 空值回退随机池(fail-safe 兜底)。
func TestPickThreeModes(t *testing.T) {
	// random:多次抽样全部命中浏览器池(覆盖池内多元素)。
	for i := 0; i < 50; i++ {
		if ua := (Config{Mode: ModeRandom}).Pick(); !inSlice(browserPool, ua) {
			t.Fatalf("random 模式 UA 应在浏览器池内: %q", ua)
		}
	}
	// tools:命中工具池。
	for i := 0; i < 50; i++ {
		if ua := (Config{Mode: ModeTools}).Pick(); !inSlice(toolPool, ua) {
			t.Fatalf("tools 模式 UA 应在工具池内: %q", ua)
		}
	}
	// fixed:精确原样。
	const mine = "UnitTest-Agent/9.9 (custom)"
	if ua := (Config{Mode: ModeFixed, Fixed: mine}).Pick(); ua != mine {
		t.Fatalf("fixed 模式 UA 应精确等于固定值: %q", ua)
	}
	// fixed 空值回退随机池(防直写库绕成空 UA)。
	if ua := (Config{Mode: ModeFixed}).Pick(); !inSlice(browserPool, ua) {
		t.Fatalf("fixed 空值应回退浏览器池: %q", ua)
	}
}

// TestNormalizeMode 归一:已知三值原样(大小写与空白容忍),未知/空
// 归 random。
func TestNormalizeMode(t *testing.T) {
	for in, want := range map[string]string{
		"random": ModeRandom, "RANDOM": ModeRandom, " random ": ModeRandom,
		"fixed": ModeFixed, "Fixed": ModeFixed,
		"tools": ModeTools, "TOOLS": ModeTools,
		"": "random", "brave": "random", "工具": "random",
	} {
		if got := NormalizeMode(in); got != want {
			t.Fatalf("NormalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadSaveRoundTrip 配置读写往返:Save 三态(mode/fixed)落库,
// Load 拼回;空 fixed 删键(恢复默认);读失败缺键按默认。
func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := openUStore(t, dir)
	ctx := context.Background()

	// 缺省:random、无固定值。
	cfg := Load(ctx, store)
	if cfg.Mode != ModeRandom || cfg.Fixed != "" {
		t.Fatalf("缺省应为 random/无固定值: %+v", cfg)
	}
	// 写 fixed 模式再读回。
	if err := Save(ctx, store, Config{Mode: ModeFixed, Fixed: "MyUA/1.0"}, "creator"); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	cfg = Load(ctx, store)
	if cfg.Mode != ModeFixed || cfg.Fixed != "MyUA/1.0" {
		t.Fatalf("往返不符: %+v", cfg)
	}
	// 清除:空值删键,fixed 回默认。
	if err := Save(ctx, store, Config{Mode: ModeTools, Fixed: ""}, "creator"); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	cfg = Load(ctx, store)
	if cfg.Mode != ModeTools || cfg.Fixed != "" {
		t.Fatalf("清除固定值后应 tools/无固定值: %+v", cfg)
	}
	// 直写未知模式值,Load 归默认(fail-safe)。
	if err := store.SetSetting(ctx, SettingKeyMode, "bogus", "creator"); err != nil {
		t.Fatalf("直写设置失败: %v", err)
	}
	if cfg := Load(ctx, store); cfg.Mode != ModeRandom {
		t.Fatalf("未知模式应归 random: %+v", cfg)
	}
	// nil store 不炸,回默认。
	if cfg := Load(ctx, nil); cfg.Mode != ModeRandom {
		t.Fatalf("nil store 应回默认: %+v", cfg)
	}
}

func inSlice(pool []string, s string) bool {
	for _, v := range pool {
		if v == s {
			return true
		}
	}
	return false
}
