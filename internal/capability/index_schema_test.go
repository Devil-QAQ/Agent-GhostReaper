package capability

import (
	"strings"
	"testing"
)

func TestRetiredOrganizerMigrationPreservesOriginalIndex(t *testing.T) {
	root := t.TempDir()
	c, err := NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ImportFolder(t.Context(), "", "原文", folderReader(t, map[string]string{"root/sample.yaml": "name: TestProduct\ncve: CVE-2099-12345\ntags: 中文,C++"})); err != nil {
		t.Fatal(err)
	}
	c.reconcileIndex(t.Context())
	id := c.List()[0].ID
	l, _ := c.Get(id)
	view, _ := l.Effective()
	const tables = "organizing_settings organizing_calls extraction_control extraction_units extraction_members extraction_requests extraction_manifest extraction_sources extraction_cache"
	for _, table := range strings.Fields(tables) {
		if _, err = c.index.db.Exec("CREATE TABLE " + table + "(old TEXT)"); err != nil {
			t.Fatal(err)
		}
		if _, err = c.index.db.Exec("INSERT INTO " + table + " VALUES('retired model output')"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = c.index.db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var version int
	if err = c.index.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal(version, err)
	}
	for _, table := range strings.Fields(tables) {
		var n int
		if err = c.index.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	for _, q := range []string{"TestProduct", "CVE-2099-12345", "中文", "C++", "sample.yaml"} {
		result, err := c.Search(t.Context(), q, id, 1)
		if err != nil || result.Total != 1 {
			t.Fatal(q, result, err)
		}
	}
	l, _ = c.Get(id)
	text, err := l.ReadEffective("sample.yaml", view.Files[0].Token, 0, 8192)
	if err != nil || !strings.Contains(text["text"].(string), "TestProduct") {
		t.Fatal(text, err)
	}
}

func TestTextIndexLeavesDocumentsAndImagesPathOnly(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	files := map[string]string{"root/plain.custom": "正文独有词", "root/readme.md": "# 正文独有词", "root/data.json": `{"name":"正文独有词"}`, "root/data.xml": `<?xml version="1.0"?><name>正文独有词</name>`, "root/check.sh": "#!/bin/sh\nprintf 正文独有词", "root/code.py": "print('正文独有词')", "root/data.yaml": "name: 正文独有词", "root/manual.pdf": "%PDF-1.5\n正文独有词", "root/image.png": "\x89PNG\r\n\x1a\n正文独有词", "root/document.docx": "PK\x03\x04正文独有词"}
	if err = c.ImportFolder(t.Context(), "", "格式", folderReader(t, files)); err != nil {
		t.Fatal(err)
	}
	c.reconcileIndex(t.Context())
	result, err := c.Search(t.Context(), "正文独有词", "", 1)
	if err != nil || result.Total != 7 || result.Coverage[0].Unsearchable != 3 {
		t.Fatal(result, err)
	}
	for _, q := range []string{"manual.pdf", "image.png", "document.docx"} {
		result, err = c.Search(t.Context(), q, "", 1)
		if err != nil || result.Total != 1 || result.Hits[0].Reason == "" {
			t.Fatal(q, result, err)
		}
	}
}
