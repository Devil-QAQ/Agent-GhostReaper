package capability

import (
	"fmt"
	"testing"
	"time"
)

func TestUnifiedQueryTenThousand(t *testing.T) {
	c, e := NewCatalog(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	files := map[string]string{}
	for n := 0; n < 10000; n++ {
		files[fmt.Sprintf("root/file-%05d.md", n)] = "规模正文 漏洞查询"
	}
	if e = c.ImportFolder(t.Context(), "", "规模", folderReader(t, files)); e != nil {
		t.Fatal(e)
	}
	c.reconcileIndex(t.Context())
	started := time.Now()
	r, e := c.Search(t.Context(), "漏洞查询", "", 300)
	t.Logf("10000 files query: %s, total=%d, page=%d", time.Since(started), r.Total, r.Page)
	if e != nil || r.Total != 10000 || len(r.Hits) != 30 || r.Page != 300 {
		t.Fatal(r, e)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("query blocked")
	}
}

func TestQueryFailureDoesNotBlockRawFiles(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.ImportFolder(t.Context(), "", "原文", folderReader(t, map[string]string{"root/dir/a.md": "原文"})); err != nil {
		t.Fatal(err)
	}
	id := c.List()[0].ID
	l, _ := c.Get(id)
	c.reconcileIndex(t.Context())
	if err := c.index.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(t.Context(), "dir", id, 1); err == nil {
		t.Fatal("database failure became no-hit")
	}
	v, err := l.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.List("", "", 1); err != nil {
		t.Fatal("directory blocked by index", err)
	}
	if _, err = l.ReadEffective(v.Files[0].Path, v.Files[0].Token, 0, 256); err != nil {
		t.Fatal("read blocked by index", err)
	}
}

func TestShortKeywordRecallsCandidatesForReading(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.ImportFolder(t.Context(), "", "候选", folderReader(t, map[string]string{
		"root/proxy.md":  "Stalwart v1.0.0 proxy authentication. Read forwarded user configuration before testing.",
		"root/legacy.md": "Stalwart v0.6.0 unrelated legacy method. Not applicable to version 1.0.0.",
		"root/other.md":  "OtherProduct proxy authentication forwarded user.",
	})); err != nil {
		t.Fatal(err)
	}
	c.reconcileIndex(t.Context())
	long, err := c.Search(t.Context(), "Stalwart proxy authentication forwarded user", "", 1)
	if err != nil || long.Total != 0 {
		t.Fatal(long, err)
	}
	short, err := c.Search(t.Context(), "Stalwart", "", 1)
	if err != nil || short.Total != 2 {
		t.Fatal(short, err)
	}
	l, _ := c.Get(c.List()[0].ID)
	for _, hit := range short.Hits {
		if hit.Path == "other.md" {
			t.Fatal("unmatched product returned")
		}
		text, err := l.ReadEffective(hit.Path, hit.Token, 0, 8192)
		if err != nil || text["complete"] != true {
			t.Fatal(text, err)
		}
	}
}
