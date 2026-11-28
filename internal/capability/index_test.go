package capability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOriginalIndexLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c, err := NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close() }()
	importFiles := func(id string, files map[string]string) {
		t.Helper()
		if err := c.ImportFolder(ctx, id, "资料", folderReader(t, files)); err != nil {
			t.Fatal(err)
		}
	}
	importFiles("", map[string]string{"root/a.md": "中文 漏洞 C++ CVE-2026-1234 English", "root/long.txt": strings.Repeat("a", maxPreview+1) + "末尾命中", "root/b.bin": "\x00binary"})
	id := c.List()[0].ID
	l, _ := c.Get(id)
	search := func(q string, n int) SearchResult {
		t.Helper()
		r, e := c.Search(ctx, q, "", 1)
		if e != nil || r.Total != n {
			t.Fatalf("query %q: %+v %v", q, r, e)
		}
		return r
	}
	r := search("中文", 0)
	if r.Coverage[0].Pending != 3 {
		t.Fatal(r)
	}
	c.reconcileIndex(ctx)
	for _, q := range []string{"中文", "c++", "cve-2026-1234", "english", "末尾命中"} {
		search(q, 1)
	}
	if search("b.bin", 1).Coverage[0].Unsearchable != 1 {
		t.Fatal("binary coverage")
	}
	hit := search("中文", 1).Hits[0]
	if hit.Fingerprint == "" {
		t.Fatal("missing fingerprint")
	}
	mutate := func(action, text string) {
		t.Helper()
		v, _ := l.Effective()
		if err := c.Mutate(id, Mutation{Path: "a.md", Version: v.Commit, Revision: v.Revision, Action: action, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	mutate("save", "本地编辑 新内容")
	search("中文", 0)
	search("english", 0)
	if _, err := l.ReadEffective("a.md", hit.Token, 0, 100); !errors.Is(err, ErrStale) {
		t.Fatal("old read", err)
	}
	c.reconcileIndex(ctx)
	local := search("本地编辑", 1).Hits[0]
	// Upstream removal retains edited file and reports conflict.
	importFiles(id, map[string]string{"root/new.md": "无关文件"})
	r = search("本地编辑", 1)
	if r.Hits[0].Token != local.Token || !r.Hits[0].Conflict {
		t.Fatal(r)
	}
	c.reconcileIndex(ctx)
	search("末尾命中", 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	l, _ = c.Get(id)
	search("本地编辑", 1)
	mutate("delete", "")
	search("本地编辑", 0)
	waitUpdate(t, l)
	c.reconcileIndex(ctx)
	var count int
	if err := c.index.db.QueryRow("SELECT count(*) FROM files WHERE source=? AND path='a.md'", id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := c.Delete(id); err != nil {
		t.Fatal(err)
	}
	search("无关文件", 0)
	waitUpdate(t, l)
	c.reconcileIndex(ctx)
	if err := c.index.db.QueryRow("SELECT count(*) FROM files").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestIndexIncrementalAndConcurrent(t *testing.T) {
	ctx := context.Background()
	c, _ := NewCatalog(t.TempDir())
	defer c.Close()
	if err := c.ImportFolder(ctx, "", "并发", folderReader(t, map[string]string{"root/a": "first", "root/stable": "unchanged"})); err != nil {
		t.Fatal(err)
	}
	id := c.List()[0].ID
	l, _ := c.Get(id)
	c.reconcileIndex(ctx)
	// Removing a source file behind the manifest proves an unrelated change does
	// not reread every body. It is restored before resource operations continue.
	v, _ := l.Effective()
	name := filepath.Join(l.snapshotDir(v.Commit), "stable")
	b, _ := os.ReadFile(name)
	os.Remove(name)
	if err := c.ImportFolder(ctx, id, "并发", folderReader(t, map[string]string{"root/a": "second", "root/stable": "unchanged"})); err != nil {
		t.Fatal(err)
	}
	c.reconcileIndex(ctx)
	r, err := c.Search(ctx, "unchanged", "", 1)
	if err != nil || r.Total != 1 {
		t.Fatal(r, err)
	}
	os.WriteFile(name, b, 0644)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := 0; n < 25; n++ {
			c.reconcileIndex(ctx)
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 25; n++ {
			if _, err := c.Search(ctx, "second", "", 1); err != nil {
				t.Error(err)
			}
		}
	}()
	for n := 0; n < 10; n++ {
		v, _ := l.Effective()
		if err := c.Mutate(id, Mutation{Path: "a", Version: v.Commit, Revision: v.Revision, Action: "save", Text: "latest"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	c.reconcileIndex(ctx)
	r, err = c.Search(ctx, "second", "", 1)
	if err != nil || r.Total != 0 {
		t.Fatal(r, err)
	}
}

func TestLegacyManifestBackfill(t *testing.T) {
	root := t.TempDir()
	l := newLibrary(root, "", "")
	commit := strings.Repeat("a", 40)
	os.MkdirAll(l.snapshotDir(commit), 0755)
	os.WriteFile(filepath.Join(l.snapshotDir(commit), "old.md"), []byte("旧资料"), 0644)
	entries, _, _ := scan(l.snapshotDir(commit))
	l.publish(&snapshot{version: Version{Commit: commit}, entries: entries})
	c := &Catalog{root: root, items: []catalogItem{{Source{ID: "old", Name: "旧库"}, l}}}
	c.openIndex()
	defer c.Close()
	c.reconcileIndex(context.Background())
	c.reconcileIndex(context.Background())
	next := newLibrary(root, "", "")
	v, err := next.Effective()
	if err != nil || v.Files[0].Fingerprint == "" {
		t.Fatal(v, err)
	}
	r, err := c.Search(context.Background(), "旧资料", "", 1)
	if err != nil || r.Total != 1 || r.Coverage[0].Pending != 0 {
		t.Fatal(r, err)
	}
}

func TestIndexCorruptionAndTextRanges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.sqlite"), []byte("broken database"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.index.db == nil {
		t.Fatal(c.index.errorText())
	}
	text := strings.Repeat("中", 200)
	if err := c.ImportFolder(context.Background(), "", "范围", folderReader(t, map[string]string{"root/a": text})); err != nil {
		t.Fatal(err)
	}
	l, _ := c.Get(c.List()[0].ID)
	v, _ := l.Effective()
	offset := int64(0)
	result := ""
	for offset < int64(len(text)) {
		r, err := l.ReadEffective("a", v.Files[0].Token, offset, 100)
		if err != nil || r["binary"] == true {
			t.Fatal(r, err)
		}
		result += r["text"].(string)
		offset = r["next_offset"].(int64)
	}
	if result != text {
		t.Fatal("range lost content")
	}
}

func TestFailedLegacyReadCannotReuseOldFingerprint(t *testing.T) {
	c, _ := NewCatalog(t.TempDir())
	defer c.Close()
	ctx := context.Background()
	if err := c.ImportFolder(ctx, "", "旧库", folderReader(t, map[string]string{"root/a": "obsolete"})); err != nil {
		t.Fatal(err)
	}
	id := c.List()[0].ID
	l, _ := c.Get(id)
	c.reconcileIndex(ctx)
	// A newly published legacy manifest has no fingerprint; a read failure must
	// leave it pending, rather than borrowing a previous version's digest.
	commit := strings.Repeat("f", 40)
	os.MkdirAll(l.snapshotDir(commit), 0755)
	if err := l.publish(&snapshot{version: Version{Commit: commit}, entries: []Entry{{Path: "a", Name: "a", Size: 7}}}); err != nil {
		t.Fatal(err)
	}
	c.reconcileIndex(ctx)
	v, _ := l.Effective()
	if v.Files[0].Fingerprint != "" {
		t.Fatal("borrowed old digest", v)
	}
	r, err := c.Search(ctx, "obsolete", "", 1)
	if err != nil || r.Total != 0 || r.Coverage[0].Pending != 1 {
		t.Fatal(r, err)
	}
}

func TestReadEffectiveSmallRangesAlwaysAdvance(t *testing.T) {
	for _, text := range []string{"中文🙂末尾", "a\xffb\xfe尾"} {
		c, err := NewCatalog(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if err = c.ImportFolder(t.Context(), "", "分段", folderReader(t, map[string]string{"root/a": text})); err != nil {
			t.Fatal(err)
		}
		l, _ := c.Get(c.List()[0].ID)
		for limit := 1; limit <= 7; limit++ {
			offset, joined := int64(0), ""
			for offset < int64(len(text)) {
				r, err := l.ReadEffective("a", "", offset, limit)
				if err != nil {
					t.Fatal(err)
				}
				next := r["next_offset"].(int64)
				if next <= offset {
					t.Fatalf("limit=%d offset=%d: range did not advance: %+v", limit, offset, r)
				}
				joined += r["text"].(string)
				offset = next
			}
			if joined != text {
				t.Fatalf("limit=%d lost bytes: %q != %q", limit, joined, text)
			}
		}
	}
}
