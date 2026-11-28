package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func folderReader(t *testing.T, files map[string]string) *multipart.Reader {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for name, text := range files {
		p, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(p, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(bytes.NewReader(b.Bytes()), w.Boundary())
}

func TestCatalogMigrationAndCRUD(t *testing.T) {
	root := t.TempDir()
	commit := strings.Repeat("e", 40)
	legacy := filepath.Join(root, "versions", commit)
	os.MkdirAll(legacy, 0755)
	os.WriteFile(filepath.Join(legacy, "README.md"), []byte("existing"), 0644)
	b, _ := json.Marshal(Version{Commit: commit})
	os.WriteFile(filepath.Join(root, "current.json"), b, 0644)
	c, err := NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	rows := c.List()
	if len(rows) != 1 || !rows[0].Ready || rows[0].Version.Files != 1 {
		t.Fatalf("migration %+v", rows)
	}
	old, _ := c.Get(DefaultSourceID)
	if err := c.Save(Source{ID: DefaultSourceID, Kind: "github", Name: "改名", URL: SourceURL}); err != nil {
		t.Fatal(err)
	}
	renamed, _ := c.Get(DefaultSourceID)
	if renamed != old {
		t.Fatal("rename replaced running library")
	}
	if err := c.Save(Source{Kind: "github", Name: "Nuclei", URL: "https://github.com/projectdiscovery/nuclei-templates.git/", Branch: "feature/test"}); err != nil {
		t.Fatal(err)
	}
	rows = c.List()
	if len(rows) != 2 || rows[1].URL != "https://github.com/projectdiscovery/nuclei-templates" || rows[1].Ready {
		t.Fatalf("second source %+v", rows)
	}
	secondLibrary, _ := c.Get(rows[1].ID)
	siblingFile := filepath.Join(secondLibrary.root, "versions", strings.Repeat("f", 40), "keep.txt")
	if err := os.MkdirAll(filepath.Dir(siblingFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingFile, []byte("keep sibling data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(Source{Kind: "github", Name: "duplicate", URL: rows[1].URL, Branch: rows[1].Branch}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, link := range []string{"http://github.com/a/b", "https://example.com/a/b", "https://github.com/a/b/tree/main", "https://user:pass@github.com/a/b", "https://github.com/a/b?x=y"} {
		if err := c.Save(Source{Kind: "github", Name: "invalid", URL: link}); err == nil {
			t.Fatalf("accepted %s", link)
		}
	}
	if err := c.Delete(DefaultSourceID); err != nil {
		t.Fatal(err)
	}
	if s := waitUpdate(t, old); s.Error != "" {
		t.Fatal(s.Error)
	}
	if _, err := os.Stat(filepath.Join(legacy, "README.md")); !os.IsNotExist(err) {
		t.Fatal("delete retained source files")
	}
	if data, err := os.ReadFile(siblingFile); err != nil || string(data) != "keep sibling data" {
		t.Fatal("legacy deletion removed another library's data")
	}
	reloaded, err := NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 1 || reloaded.List()[0].ID == DefaultSourceID {
		t.Fatal("deleted source revived")
	}
	remainingID := reloaded.List()[0].ID
	remaining, _ := reloaded.Get(remainingID)
	if err := reloaded.Delete(remainingID); err != nil {
		t.Fatal(err)
	}
	if s := waitUpdate(t, remaining); s.Error != "" {
		t.Fatal(s.Error)
	}
	reloaded, err = NewCatalog(root)
	if err != nil || len(reloaded.List()) != 0 {
		t.Fatal("empty catalog not preserved")
	}
}

func TestFolderImportReplacementAndIsolation(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 {
		t.Fatal("new deployment must start without another person's sources")
	}
	files := map[string]string{"我的资料/说明.md": "original", "我的资料/templates/check.yaml": "id: sample", "我的资料/assets/example.bin": "\x00\x01"}
	if err := c.ImportFolder(context.Background(), "", "本地库", folderReader(t, files)); err != nil {
		t.Fatal(err)
	}
	rows := c.List()
	source := rows[0]
	if source.Kind != "folder" || source.Version.Files != 3 || !source.Ready {
		t.Fatalf("import %+v", source)
	}
	l, _ := c.Get(source.ID)
	p, err := l.Preview("templates/check.yaml", "")
	if err != nil || p.Text != "id: sample" {
		t.Fatalf("nested path %+v %v", p, err)
	}
	first := source.Version.Commit
	importedAt := source.Version.UpdatedAt
	if err := c.ImportFolder(context.Background(), source.ID, source.Name, folderReader(t, files)); err != nil {
		t.Fatal(err)
	}
	checked, _ := c.Status(source.ID)
	if checked.Version.Commit != first || !checked.Version.UpdatedAt.Equal(importedAt) || checked.Version.CheckedAt.Before(importedAt) {
		t.Fatal("unchanged folder should only update check time")
	}
	if _, err := c.StartUpdate(source.ID); err == nil {
		t.Fatal("folder allowed remote update")
	}
	if err := c.RenameFolder(source.ID, "改名本地库"); err != nil {
		t.Fatal(err)
	}
	if err := c.ImportFolder(context.Background(), source.ID, "改名本地库", folderReader(t, map[string]string{"新的目录/说明.md": "replacement"})); err != nil {
		t.Fatal(err)
	}
	l, _ = c.Get(source.ID)
	p, err = l.Preview("说明.md", "")
	if err != nil || p.Text != "replacement" {
		t.Fatal("replacement not active")
	}
	p, err = l.Preview("说明.md", first)
	if err != nil || p.Text != "original" {
		t.Fatal("old contents lost")
	}
	active := l.Status().Version.Commit
	if err := c.ImportFolder(context.Background(), source.ID, "bad", folderReader(t, map[string]string{"folder/../escape": "invalid"})); err == nil {
		t.Fatal("accepted unsafe import")
	}
	l, _ = c.Get(source.ID)
	if l.Status().Version.Commit != active {
		t.Fatal("failed import replaced current")
	}
	if err := c.ImportFolder(context.Background(), "", "另一个库", folderReader(t, map[string]string{"folder/说明.md": "separate"})); err != nil {
		t.Fatal(err)
	}
	other, _ := c.Get(c.List()[1].ID)
	p, err = other.Preview("说明.md", "")
	if err != nil || p.Text != "separate" {
		t.Fatal("sources mixed")
	}
	if f, _, err := other.Open("说明.md", active); err == nil {
		f.Close()
		t.Fatal("read other source version")
	}
	reloaded, err := NewCatalog(c.root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := reloaded.Status(source.ID)
	if err != nil || status.Name != "改名本地库" || status.Version.Commit != active {
		t.Fatalf("reload %+v %v", status, err)
	}
}

func TestSourceMutationBlockedDuringUpdate(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(Source{Name: "sample", Kind: "github", URL: SourceURL}); err != nil {
		t.Fatal(err)
	}
	id := c.List()[0].ID
	l, _ := c.Get(id)
	// Hold an in-flight update while testing configuration mutations.
	l.mu.Lock()
	l.updating = true
	l.mu.Unlock()
	if err := c.Delete(id); err != ErrSourceBusy {
		t.Fatalf("delete during update: %v", err)
	}
	s := c.List()[0].Source
	s.Name = "changed"
	if err := c.Save(s); err != ErrSourceBusy {
		t.Fatalf("edit during update: %v", err)
	}
}
