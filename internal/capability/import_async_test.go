package capability

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"testing"
	"time"
)

func TestFolderUploadRemainsReadableAndFailurePreservesOldVersion(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ImportFolder(context.Background(), "", "资料", folderReader(t, map[string]string{"root/a.md": "old"})); err != nil {
		t.Fatal(err)
	}
	source := c.List()[0]
	l, _ := c.Get(source.ID)
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	mw := multipart.NewWriter(w)
	result := make(chan error, 1)
	go func() {
		result <- c.ImportFolderAsync(context.Background(), source.ID, "更新", multipart.NewReader(r, mw.Boundary()))
	}()
	written := make(chan error, 1)
	go func() {
		part, err := mw.CreateFormFile("files", "root/a.md")
		if err == nil {
			_, err = io.WriteString(part, "replacement")
		}
		if err == nil {
			_, err = mw.CreateFormFile("files", "root/b.md")
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upload stalled")
	}
	// The uploader is now waiting for more data. Reads and unrelated changes
	// must remain available; the old snapshot is still the active one.
	reads := make(chan error, 1)
	go func() {
		s, err := c.Status(source.ID)
		if err == nil && (!s.Updating || !s.Ready || s.Stage != "上传文件" || s.Progress == nil || s.Progress.Bytes == 0) {
			err = errors.New("missing upload progress")
		}
		if err == nil {
			p, e := l.Preview("a.md", "")
			err = e
			if err == nil && p.Text != "old" {
				err = errors.New("partial upload became visible")
			}
		}
		if err == nil {
			err = c.Save(Source{Name: "其他库", Kind: "github", URL: SourceURL})
		}
		reads <- err
	}()
	select {
	case err := <-reads:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload blocked other operations")
	}
	if err := c.Delete(source.ID); err != ErrSourceBusy {
		t.Fatalf("delete during upload: %v", err)
	}
	if err := c.ImportFolderAsync(context.Background(), source.ID, source.Name, folderReader(t, map[string]string{"root/a.md": "other"})); err != ErrSourceBusy {
		t.Fatalf("duplicate upload: %v", err)
	}
	w.CloseWithError(errors.New("connection interrupted"))
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("incomplete upload accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed upload did not release state")
	}
	status, _ := c.Status(source.ID)
	if status.Updating || status.Error == "" || status.Version.Commit != source.Version.Commit || status.Name != source.Name {
		t.Fatalf("failed import %+v", status)
	}
	p, err := l.Preview("a.md", "")
	if err != nil || p.Text != "old" {
		t.Fatal("failed upload damaged active snapshot")
	}
}

func TestBackgroundFolderPublishOutlivesRequestAndRetainsLocalChanges(t *testing.T) {
	c, err := NewCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ImportFolder(context.Background(), "", "资料", folderReader(t, map[string]string{"root/a.md": "old"})); err != nil {
		t.Fatal(err)
	}
	s := c.List()[0]
	l, _ := c.Get(s.ID)
	if err := c.Mutate(s.ID, Mutation{Path: "a.md", Action: "save", Text: "local", Version: s.Version.Commit}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.ImportFolderAsync(ctx, s.ID, "新名称", folderReader(t, map[string]string{"root/a.md": "upstream", "root/b.md": "new"})); err != nil {
		t.Fatal(err)
	}
	cancel()
	status := waitUpdate(t, l)
	if !status.Ready || status.Error != "" || status.Version.Files != 2 || status.Progress != nil {
		t.Fatalf("background import %+v", status)
	}
	current, _ := c.Get(s.ID)
	if current != l {
		t.Fatal("import discarded the live library")
	}
	p, err := l.Preview("a.md", "")
	if err != nil || p.Text != "local" || !p.Conflict {
		t.Fatalf("local change lost: %+v %v", p, err)
	}
	reloaded, err := NewCatalog(c.root)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := reloaded.Status(s.ID)
	if stored.Name != "新名称" || stored.Version.Commit != status.Version.Commit {
		t.Fatalf("not persisted %+v", stored)
	}
}
