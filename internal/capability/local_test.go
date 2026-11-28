package capability

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestLocalChangesSurviveImportAndConflicts(t *testing.T) {
	c, e := NewCatalog(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	upload := func(files map[string]string) {
		t.Helper()
		id := ""
		if len(c.List()) > 0 {
			id = c.List()[0].ID
		}
		if e := c.ImportFolder(context.Background(), id, "test", folderReader(t, files)); e != nil {
			t.Fatal(e)
		}
	}
	upload(map[string]string{"root/a.txt": "original", "root/unwanted/a.yaml": "ignore"})
	id := c.List()[0].ID
	mutate := func(p, action, text string) {
		t.Helper()
		l, _ := c.Get(id)
		s := l.Status()
		if e := c.Mutate(id, Mutation{Path: p, Action: action, Text: text, Version: s.Version.Commit, Revision: s.LocalRevision}); e != nil {
			t.Fatal(e)
		}
		if action == "delete" {
			if s := waitUpdate(t, l); s.Error != "" {
				t.Fatal(s.Error)
			}
		}
	}
	mutate("a.txt", "save", "my edit")
	mutate("unwanted", "delete", "")
	upload(map[string]string{"root/a.txt": "upstream edit", "root/unwanted/a.yaml": "changed", "root/unwanted/new.yaml": "new"})
	l, _ := c.Get(id)
	p, e := l.Preview("a.txt", "")
	if e != nil || p.Text != "my edit" || !p.Conflict {
		t.Fatalf("local edit lost: %+v %v", p, e)
	}
	up, e := l.PreviewUpstream("a.txt", "")
	if e != nil || up.Text != "upstream edit" {
		t.Fatal("upstream unavailable")
	}
	if f, _, e := l.Open("unwanted/new.yaml", ""); e == nil {
		f.Close()
		t.Fatal("deleted directory revived")
	}
	ls, e := l.List("", "", 1)
	if e != nil || ls.Total != 1 {
		t.Fatalf("listing %+v %v", ls, e)
	}
	if e := c.Mutate(id, Mutation{Path: "a.txt", Action: "save", Text: "stale", Version: p.Commit, Revision: p.Revision - 1}); !errors.Is(e, ErrStale) {
		t.Fatal("accepted stale edit")
	}
	mutate("a.txt", "keep", "")
	l, _ = c.Get(id)
	p, _ = l.Preview("a.txt", "")
	if p.Conflict {
		t.Fatal("keep failed")
	}
	fresh, e := NewCatalog(c.root)
	if e != nil {
		t.Fatal(e)
	}
	l, _ = fresh.Get(id)
	p, _ = l.Preview("a.txt", "")
	if p.Text != "my edit" {
		t.Fatal("restart lost changes")
	}
	mutate("a.txt", "restore", "")
	s := l.Status()
	if err := c.Mutate(id, Mutation{Path: "unwanted", Action: "restore", Version: s.Version.Commit, Revision: s.LocalRevision}); err == nil {
		t.Fatal("physically deleted contents were offered for restoration")
	}
	l, _ = c.Get(id)
	p, _ = l.Preview("a.txt", "")
	if p.Text != "upstream edit" {
		t.Fatal("restore failed")
	}
	upload(map[string]string{"root/a.txt": "upstream edit"})
	mutate("a.txt", "save", "orphan")
	upload(map[string]string{"root/other.txt": "new"})
	l, _ = c.Get(id)
	p, e = l.Preview("a.txt", "")
	if e != nil || p.Text != "orphan" || !p.Conflict {
		t.Fatal("upstream removal lost local edit")
	}
	ls, _ = l.List("", "a.txt", 1)
	if ls.Total != 1 {
		t.Fatal("orphan edit invisible")
	}
	mutate("a.txt", "restore", "")
	if _, _, e = l.Open("a.txt", ""); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("upstream removal not adopted")
	}
}
