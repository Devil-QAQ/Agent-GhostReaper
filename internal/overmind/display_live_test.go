package overmind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
)

// This opt-in test formats synthetic observations only. It does not run task
// tools or demonstrate autonomous discovery. The exact same file can run in
// the saved baseline to measure real auxiliary usage on identical material.
func TestDisplayLiveFixedSamples(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_DISPLAY_MODEL_FILE")
	if file == "" {
		t.Skip("real display-model configuration not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("cannot read private model configuration")
	}
	var cfg struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Format  string `json:"format"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private model configuration")
	}
	provider, err := llm.NewProvider(llm.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, Format: llm.Format(cfg.Format)})
	if err != nil {
		t.Fatal("cannot initialize display-model provider")
	}
	materials := []factTextMaterial{
		{FactID: "version", Statement: "服务名为 DemoBridge，公开提供的版本号为 2.4.1。", EvidenceHead: "固定样例响应列出 service=DemoBridge, version=2.4.1；公开信息，不是泄露。"},
		{FactID: "checks", Statement: "49次参数校验检查未发现异常，结果仅覆盖本次所测参数。", EvidenceHead: "固定样例：49项检查完成，异常数0；未检查其他入口。"},
		{FactID: "access", Statement: "页面返回了正常内容，但没有验证登录权限或其他身份的访问范围。", EvidenceHead: "固定样例：页面可返回；未执行权限验证，不能据此判断访问控制是否有效。"},
	}
	type sample struct {
		Round      int                             `json:"round"`
		Calls      int                             `json:"calls"`
		Input      int                             `json:"input_tokens"`
		Output     int                             `json:"output_tokens"`
		CacheRead  int                             `json:"cache_read_tokens"`
		CacheWrite int                             `json:"cache_write_tokens"`
		Unknown    int                             `json:"unknown_usage"`
		ElapsedMS  int64                           `json:"elapsed_ms"`
		Texts      map[string]rings.FactTextPolish `json:"texts"`
	}
	var report []sample
	store := openStore(t)
	for round := 1; round <= 3; round++ {
		task := "display-live-" + string(rune('0'+round))
		loop := &Loop{TaskID: task, Store: store, Provider: provider, Model: cfg.Model, RootDir: t.TempDir()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		start := time.Now()
		texts := loop.genFactTexts(ctx, materials)
		cancel()
		row := sample{Round: round, ElapsedMS: time.Since(start).Milliseconds(), Texts: texts}
		usage, err := store.TokenUsageRows(t.Context(), task)
		if err != nil {
			t.Fatal("cannot read isolated usage records")
		}
		row.Calls = len(usage)
		for _, u := range usage {
			row.Input += u.InputTokens
			row.Output += u.OutputTokens
			row.CacheRead += u.CacheReadTokens
			row.CacheWrite += u.CacheWriteTokens
			if u.UsageMissing {
				row.Unknown++
			}
		}
		report = append(report, row)
		if out := os.Getenv("GHOSTREAPER_DISPLAY_EVIDENCE"); out != "" {
			if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
				t.Fatal("cannot create isolated evidence directory")
			}
			body, _ := json.MarshalIndent(report, "", "  ")
			if err := os.WriteFile(out, body, 0600); err != nil {
				t.Fatal("cannot save sanitized display evidence")
			}
		}
		if len(texts) != len(materials) {
			t.Error("some fixed observations lost their display text; private errors withheld")
		}
		version := texts["version"].Title + texts["version"].Statement
		if !strings.Contains(version, "DemoBridge") || !strings.Contains(version, "2.4.1") {
			t.Error("actual service name or version lost")
		}
		checks := texts["checks"].Title + texts["checks"].Statement
		if !strings.Contains(checks, "49") || !strings.Contains(checks, "参数") {
			t.Error("exact check count or tested scope lost")
		}
		access := texts["access"].Title + texts["access"].Statement
		bounded := false
		for _, term := range []string{"未验证", "未检查", "没有验证", "尚未验证", "未确认", "不能证明", "不足以", "无法判断", "无法确认"} {
			bounded = bounded || strings.Contains(access, term)
		}
		if !bounded {
			t.Error("unverified access boundary was lost")
		}
		t.Logf("round=%d calls=%d input=%d output=%d cache_read=%d unknown=%d elapsed_ms=%d", round, row.Calls, row.Input, row.Output, row.CacheRead, row.Unknown, row.ElapsedMS)
	}
}
