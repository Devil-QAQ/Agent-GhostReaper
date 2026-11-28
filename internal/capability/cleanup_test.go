package capability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func importCleanupFixture(t *testing.T, c *Catalog, id string, files map[string]string) *Library {
	t.Helper()
	if err := c.ImportFolder(context.Background(), id, "资料", folderReader(t, files)); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		id = c.List()[len(c.List())-1].ID
	}
	l, err := c.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func changeForCleanup(t *testing.T, c *Catalog, id, name, action, text string) {
	t.Helper()
	s, err := c.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Mutate(id, Mutation{Path: name, Action: action, Text: text, Version: s.Version.Commit, Revision: s.LocalRevision}); err != nil {
		t.Fatal(err)
	}
}

func assertGone(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatalf("retained %s: %v", name, err)
	}
}

func TestDeletionRemovesHistoricalCopiesAndCollectsOnlyUnusedEdits(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := importCleanupFixture(t, c, "", map[string]string{"root/a.txt": "old-a", "root/b.txt": "old-b", "root/dir/x.txt": "old-x"})
	id := c.List()[0].ID
	first := l.Status().Version.Commit
	importCleanupFixture(t, c, id, map[string]string{"root/a.txt": "new-a", "root/b.txt": "new-b", "root/dir/x.txt": "new-x"})
	second := l.Status().Version.Commit
	changeForCleanup(t, c, id, "a.txt", "save", "shared edit")
	changeForCleanup(t, c, id, "b.txt", "save", "shared edit")
	l.mu.RLock()
	key := l.local.Changes["a.txt"].Content
	l.mu.RUnlock()
	changeForCleanup(t, c, id, "a.txt", "delete", "")
	if s := waitUpdate(t, l); s.Error != "" || s.CleanupPending {
		t.Fatalf("cleanup %+v", s)
	}
	for _, version := range []string{first, second} {
		assertGone(t, filepath.Join(l.snapshotDir(version), "a.txt"))
	}
	if _, err := os.Stat(filepath.Join(l.root, "local-content", key)); err != nil {
		t.Fatal("shared edit used by b.txt was deleted")
	}
	if _, _, err := l.Open("a.txt", first); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("historical deleted path still opens")
	}
	p, err := l.Preview("b.txt", "")
	if err != nil || p.Text != "shared edit" {
		t.Fatalf("sibling edit lost %+v %v", p, err)
	}
	changeForCleanup(t, c, id, "b.txt", "delete", "")
	if s := waitUpdate(t, l); s.Error != "" {
		t.Fatal(s.Error)
	}
	assertGone(t, filepath.Join(l.root, "local-content", key))
	changeForCleanup(t, c, id, "dir", "delete", "")
	if s := waitUpdate(t, l); s.Error != "" {
		t.Fatal(s.Error)
	}
	for _, version := range []string{first, second} {
		assertGone(t, filepath.Join(l.snapshotDir(version), "dir"))
	}
	// Re-import changed contents with identical paths. Neither changed bytes nor
	// new descendants may recreate an explicitly deleted path on disk.
	importCleanupFixture(t, c, id, map[string]string{"root/a.txt": "different digest", "root/dir/new.txt": "new descendant", "root/other/a.txt": "same basename, keep"})
	active := l.Status().Version.Commit
	assertGone(t, filepath.Join(l.snapshotDir(active), "a.txt"))
	assertGone(t, filepath.Join(l.snapshotDir(active), "dir"))
	p, err = l.Preview("other/a.txt", "")
	if err != nil || p.Text != "same basename, keep" {
		t.Fatal("deletion matched a basename instead of the full path")
	}
	changes, err := l.Changes()
	if err != nil || len(changes.Changes) != 3 {
		t.Fatalf("missing deletion records %+v %v", changes, err)
	}
	for _, change := range changes.Changes {
		if !change.Deleted || change.DeletedAt.IsZero() {
			t.Fatal("missing deletion time")
		}
	}
}

func TestCleanupFailureIsVisibleAndRetryableWithoutBlockingReads(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := importCleanupFixture(t, c, "", map[string]string{"root/remove.txt": "delete", "root/keep.txt": "keep"})
	id := c.List()[0].ID
	c.cleanupSlots <- struct{}{}
	locked := true
	defer func() {
		if locked {
			<-c.cleanupSlots
		}
	}()
	changeForCleanup(t, c, id, "remove.txt", "delete", "")
	s, _ := c.Status(id)
	if !s.Updating || !s.CleanupPending || s.Stage != "删除文件" {
		t.Fatalf("not queued %+v", s)
	}
	p, err := l.Preview("keep.txt", "")
	if err != nil || p.Text != "keep" {
		t.Fatal("queued deletion blocked reading")
	}
	if err := c.Save(Source{Name: "其他库", Kind: "github", URL: SourceURL}); err != nil {
		t.Fatal("queued deletion blocked unrelated configuration")
	}
	if _, err := c.StartUpdate(id); err != ErrSourceBusy {
		t.Fatalf("pending cleanup allowed an update: %v", err)
	}
	// Make final metadata publication fail deterministically, even when tests
	// run with elevated filesystem permissions.
	metadata := filepath.Join(l.root, "local.json")
	if err := os.Rename(metadata, metadata+".pending"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(metadata, 0755); err != nil {
		t.Fatal(err)
	}
	<-c.cleanupSlots
	locked = false
	s2 := waitUpdate(t, l)
	if s2.Error == "" || !s2.CleanupPending {
		t.Fatalf("cleanup failure hidden %+v", s2)
	}
	assertGone(t, filepath.Join(l.snapshotDir(s.Version.Commit), "remove.txt"))
	if err := os.Remove(metadata); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(metadata+".pending", metadata); err != nil {
		t.Fatal(err)
	}
	if err := c.RetryCleanup(id); err != nil {
		t.Fatal(err)
	}
	if s := waitUpdate(t, l); s.Error != "" || s.CleanupPending {
		t.Fatalf("retry failed %+v", s)
	}
}

func TestPendingAndLegacyFileDeletionsResumeAfterRestart(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "legacy"}[legacy], func(t *testing.T) {
			c, err := NewCatalog(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			l := importCleanupFixture(t, c, "", map[string]string{"root/all.txt": "remove"})
			id := c.List()[0].ID
			state := localState{Revision: 1, Changes: map[string]LocalChange{"all.txt": {Deleted: true}}, CleanupPending: !legacy}
			if err := writeRecord(filepath.Join(l.root, "local.json"), state); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewCatalog(c.root)
			if err != nil {
				t.Fatal(err)
			}
			loaded, _ := restarted.Get(id)
			if s := waitUpdate(t, loaded); !s.Ready || s.Error != "" || s.CleanupPending || s.Version.Files != 0 {
				t.Fatalf("resume %+v", s)
			}
			assertGone(t, filepath.Join(l.snapshotDir(l.Status().Version.Commit), "all.txt"))
			again, err := NewCatalog(c.root)
			if err != nil {
				t.Fatal(err)
			}
			status, _ := again.Status(id)
			if !status.Ready || status.Version.Files != 0 || status.Updating {
				t.Fatalf("empty library damaged %+v", status)
			}
		})
	}
}

func TestRepositoryDeletionResumesAndPreservesOtherLibraries(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := importCleanupFixture(t, c, "", map[string]string{"root/old.txt": "remove"})
	id := c.List()[0].ID
	importCleanupFixture(t, c, id, map[string]string{"root/new.txt": "remove"})
	other := importCleanupFixture(t, c, "", map[string]string{"root/keep.txt": "keep"})
	rel, err := filepath.Rel(c.root, l.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(c.deletionRecord(id), repositoryDeletion{Directory: filepath.ToSlash(rel)}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewCatalog(c.root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Get(id)
	if err == nil {
		if s := waitUpdate(t, loaded); s.Error != "" {
			t.Fatal(s.Error)
		}
	}
	if _, err := restarted.Get(id); err != ErrSourceMissing {
		t.Fatalf("deleted configuration survived: %v", err)
	}
	assertGone(t, l.root)
	assertGone(t, c.deletionRecord(id))
	p, err := other.Preview("keep.txt", "")
	if err != nil || p.Text != "keep" {
		t.Fatal("sibling library was deleted")
	}
	if len(restarted.List()) != 1 {
		t.Fatal("unexpected remaining configuration")
	}
}

func TestRemoteUpdatesDoNotRecreatePhysicallyDeletedPaths(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	archives := map[string][]byte{
		first:  archiveBytes(t, map[string]string{"root/unwanted/a.yaml": "old", "root/keep/a.yaml": "keep"}),
		second: archiveBytes(t, map[string]string{"root/unwanted/a.yaml": "changed digest", "root/unwanted/new.yaml": "new", "root/keep/a.yaml": "same basename"}),
	}
	var version atomic.Value
	version.Store(first)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := version.Load().(string)
		if strings.Contains(r.URL.Path, "/commits/") {
			fmt.Fprintf(w, `{"sha":%q}`, current)
		} else {
			w.Write(archives[current])
		}
	}))
	defer ts.Close()
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(Source{Name: "远程库", Kind: "github", URL: SourceURL, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	id := c.List()[0].ID
	l, _ := c.Get(id)
	l.apiURL, l.archiveURL = ts.URL, ts.URL
	c.StartUpdate(id)
	if s := waitUpdate(t, l); s.Error != "" {
		t.Fatal(s.Error)
	}
	changeForCleanup(t, c, id, "unwanted", "delete", "")
	if s := waitUpdate(t, l); s.Error != "" {
		t.Fatal(s.Error)
	}
	assertGone(t, filepath.Join(l.snapshotDir(first), "unwanted"))
	version.Store(second)
	c.StartUpdate(id)
	if s := waitUpdate(t, l); s.Error != "" || s.Version.Files != 1 {
		t.Fatalf("update %+v", s)
	}
	assertGone(t, filepath.Join(l.snapshotDir(second), "unwanted"))
	p, err := l.Preview("keep/a.yaml", "")
	if err != nil || p.Text != "same basename" {
		t.Fatal("same filename in another directory was excluded")
	}
}

func TestPhysicalDeletionCannotFollowSymlinksOutsideLibrary(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := importCleanupFixture(t, c, "", map[string]string{"root/dir/file.txt": "copy"})
	id := c.List()[0].ID
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "file.txt"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(l.snapshotDir(l.Status().Version.Commit), "dir")
	if err := os.RemoveAll(inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, inside); err != nil {
		t.Fatal(err)
	}
	changeForCleanup(t, c, id, "dir/file.txt", "delete", "")
	if s := waitUpdate(t, l); s.Error == "" || !s.CleanupPending {
		t.Fatal("unsafe path did not stop cleanup")
	}
	data, err := os.ReadFile(filepath.Join(out, "file.txt"))
	if err != nil || string(data) != "outside" {
		t.Fatal("cleanup escaped the library")
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := c.RetryCleanup(id); err != nil {
		t.Fatal(err)
	}
	if s := waitUpdate(t, l); s.Error != "" || s.CleanupPending {
		t.Fatalf("retry %+v", s)
	}
}
