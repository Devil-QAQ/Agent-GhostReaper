package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ghostreaper/internal/capability"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/testmodel"
)

// Validate recall and model-side selection through the real production engine.
// The task describes the need, without prescribing its search query.
func TestResourceLiveShortQueryAndSelection(t *testing.T) {
	provider := testmodel.Provider(t)
	dir := os.Getenv("GHOSTREAPER_SEARCH_GUIDANCE_EVIDENCE")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store := openStore(t, dir)
	catalog, err := capability.NewCatalog(filepath.Join(dir, "capability-library"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for path, body := range map[string]string{
		"root/proxy.md":  "Stalwart v1.0.0 proxy authentication. This reference explains forwarded user configuration checks. Applicable only to version 1.0.0; verify the actual target configuration before using any method.",
		"root/legacy.md": "Stalwart v0.6.0 legacy reference. This method is not applicable to version 1.0.0.",
		"root/other.md":  "OtherProduct proxy authentication forwarded user reference. This is not Stalwart documentation.",
	} {
		part, e := writer.CreateFormFile("files", path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = part.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = catalog.ImportFolder(t.Context(), "", "检索筛选样本", multipart.NewReader(&buf, writer.Boundary())); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); catalog.RunIndex(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, 3*time.Second, "样本原文索引", func() bool { r, e := catalog.Search(ctx, "Stalwart", "", 1); return e == nil && r.Total == 3 })
	intent := "本轮专门验证 resource_read 的 search 动作，请使用该动作检索本地资料。目标已识别为 Stalwart v1.0.0，需要 proxy authentication forwarded user 相关方法。请查询本地资料并阅读原文，自行筛选产品和版本适用的文件。用 bash 打印 SELECTED_PATH=所选文件名 VERSION=匹配版本，然后结束。只读资料并输出选择，不执行 PoC、不访问任何目标或网络、不提交漏洞。"
	resourceTask(t, store, "search-selection", intent)
	executor := NewExecutor(store, provider, filepath.Join(dir, "tasks"), Budget{MaxTurns: 20})
	executor.Capabilities = catalog
	executor.Mirror = mirror.New(store, executor.Blobs, filepath.Join(dir, "tasks"))
	result, err := executor.RunStep(ctx, "search-selection", "s1")
	if err != nil || result.Reason != "completed" {
		t.Fatal(result, err)
	}
	shortQuery, readSelected, selected := false, false, false
	for _, rec := range swarmRecords(t, store, "search-selection") {
		if rec.Type != rings.SwarmRecToolCall || rec.IsError {
			continue
		}
		if rec.ToolName == "resource_read" {
			var args struct {
				Action string `json:"action"`
				Q      string `json:"q"`
				Path   string `json:"path"`
			}
			if json.Unmarshal(rec.ToolArgs, &args) != nil {
				t.Fatal("invalid tool arguments")
			}
			if args.Action == "search" && args.Q != "" && utf8.RuneCountInString(args.Q) <= 32 {
				shortQuery = true
				t.Logf("actual search q=%q", args.Q)
			}
			if args.Action == "read" && args.Path == "proxy.md" {
				readSelected = true
			}
		}
		if rec.ToolName == "bash" {
			for _, line := range strings.Split(rec.ResultHead, "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "SELECTED_PATH=proxy.md" && strings.HasPrefix(fields[1], "VERSION=") && strings.TrimPrefix(strings.TrimPrefix(fields[1], "VERSION="), "v") == "1.0.0" {
					selected = true
				}
			}
		}
	}
	if !shortQuery || !readSelected || !selected {
		t.Fatalf("query/read/selection missing: %v %v %v", shortQuery, readSelected, selected)
	}
	t.Logf("short query, actual original read and correct product/version selection passed; unit=%s", result.UnitID)
}
