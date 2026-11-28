package capability

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func archiveBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, text := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func waitUpdate(t *testing.T, l *Library) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := l.Status()
		if !s.Updating {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("update did not finish")
	return Status{}
}

func TestUpdatePreservesVersionsAndSurvivesFailure(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	archives := map[string][]byte{
		first:  archiveBytes(t, map[string]string{"repo/中间件/示例.md": "first proof", "repo/README.md": "readme"}),
		second: archiveBytes(t, map[string]string{"repo/中间件/示例.md": "second proof", "repo/PoC/check.py": "print('sample')"}),
	}
	var revision atomic.Value
	revision.Store(first)
	var downloads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+Repository:
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case strings.HasPrefix(r.URL.Path, "/repos/"+Repository+"/commits/"):
			json.NewEncoder(w).Encode(map[string]string{"sha": revision.Load().(string)})
		default:
			downloads.Add(1)
			if b, ok := archives[revision.Load().(string)]; ok {
				w.Write(b)
			} else {
				w.Write([]byte("not a zip"))
			}
		}
	}))
	defer ts.Close()
	l := newLibrary(t.TempDir(), Repository, "")
	l.apiURL, l.archiveURL = ts.URL, ts.URL
	if !l.StartUpdate() {
		t.Fatal("did not start")
	}
	s := waitUpdate(t, l)
	if !s.Ready || s.Error != "" || s.Version.Files != 2 {
		t.Fatalf("initial update: %+v", s)
	}
	if s.Version.AddedFiles == nil || *s.Version.AddedFiles != 2 {
		t.Fatal("initial download count")
	}
	preview, err := l.Preview("中间件/示例.md", first)
	if err != nil || preview.Text != "first proof" {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	list, err := l.List("", "示例", 1)
	if err != nil || list.Total != 1 || list.Entries[0].Path != "中间件/示例.md" {
		t.Fatalf("search: %+v %v", list, err)
	}
	if !l.StartUpdate() {
		t.Fatal("unchanged update did not start")
	}
	s = waitUpdate(t, l)
	if downloads.Load() != 1 || s.Error != "" {
		t.Fatalf("same version redownloaded: %+v", s)
	}
	if s.Version.AddedFiles == nil || *s.Version.AddedFiles != 2 {
		t.Fatal("unchanged check cleared last update count")
	}
	revision.Store(second)
	l.StartUpdate()
	s = waitUpdate(t, l)
	if s.Error != "" || s.Version.Commit != second {
		t.Fatalf("replacement: %+v", s)
	}
	if s.Version.AddedFiles == nil || *s.Version.AddedFiles != 1 {
		t.Fatal("added files must exclude modified files and directories")
	}
	preview, err = l.Preview("中间件/示例.md", first)
	if err != nil || preview.Text != "first proof" {
		t.Fatal("old snapshot changed")
	}
	preview, err = l.Preview("中间件/示例.md", "")
	if err != nil || preview.Text != "second proof" {
		t.Fatal("new snapshot not active")
	}
	revision.Store(strings.Repeat("c", 40))
	l.StartUpdate()
	s = waitUpdate(t, l)
	if s.Error == "" || s.Version.Commit != second || !s.Ready {
		t.Fatalf("failure lost current: %+v", s)
	}
	reloaded := newLibrary(l.root, Repository, "")
	if !reloaded.Status().Ready || reloaded.Status().Version.Commit != second {
		t.Fatal("restart lost version")
	}
	if n := reloaded.Status().Version.AddedFiles; n == nil || *n != 1 {
		t.Fatal("last update count lost on restart")
	}
	if _, _, err := reloaded.Open("../current.json", ""); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestUpdateCannotStartTwice(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.Error(w, "unavailable", 503)
	}))
	defer ts.Close()
	l := newLibrary(t.TempDir(), Repository, "")
	l.apiURL = ts.URL
	if !l.StartUpdate() {
		t.Fatal("not started")
	}
	<-entered
	if l.StartUpdate() {
		t.Fatal("duplicate update started")
	}
	close(release)
	if s := waitUpdate(t, l); s.Error == "" || s.Ready {
		t.Fatalf("failure state: %+v", s)
	}
}

func TestRemoteDownloadReportsProgressWithoutBlockingReads(t *testing.T) {
	archive := archiveBytes(t, map[string]string{"repo/a.md": strings.Repeat("sample", 10000)})
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			fmt.Fprintf(w, `{"sha":%q}`, strings.Repeat("f", 40))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		w.Write(archive[:len(archive)/2])
		w.(http.Flusher).Flush()
		close(started)
		<-release
		w.Write(archive[len(archive)/2:])
	}))
	defer func() { unblock.Do(func() { close(release) }); ts.Close() }()
	l := newLibrary(t.TempDir(), Repository, "main")
	l.apiURL, l.archiveURL = ts.URL, ts.URL
	l.StartUpdate()
	<-started
	deadline := time.Now().Add(time.Second)
	for {
		status := l.Status()
		if status.Progress != nil && status.Progress.Bytes > 0 {
			if !status.Updating || status.Stage != "下载文件" || status.Progress.TotalBytes != int64(len(archive)) {
				t.Fatalf("download progress %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download progress unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := l.List("", "", 1); err != ErrNotReady {
		t.Fatalf("read while downloading: %v", err)
	}
	unblock.Do(func() { close(release) })
	status := waitUpdate(t, l)
	if !status.Ready || status.Updating || status.Error != "" || status.Progress != nil || status.Version.Files != 1 {
		t.Fatalf("download result %+v", status)
	}
}

func TestExtractRejectsEscapesAndSymlinks(t *testing.T) {
	for _, name := range []string{"repo/../outside", "repo/a/../../outside", "repo/C:/outside", "repo/a\\outside", "repo//outside"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "bad.zip")
			os.WriteFile(p, archiveBytes(t, map[string]string{name: "bad"}), 0600)
			if err := extract(context.Background(), p, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "repo/link"}
	h.SetMode(os.ModeSymlink | 0777)
	f, _ := z.CreateHeader(h)
	f.Write([]byte("/etc/passwd"))
	z.Close()
	p := filepath.Join(t.TempDir(), "link.zip")
	os.WriteFile(p, b.Bytes(), 0600)
	if err := extract(context.Background(), p, t.TempDir()); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestPreviewBoundaryAndSnapshotContainment(t *testing.T) {
	root := t.TempDir()
	commit := strings.Repeat("d", 40)
	dir := filepath.Join(root, "versions", commit)
	os.MkdirAll(dir, 0755)
	// Cut a multibyte character at the preview boundary.
	os.WriteFile(filepath.Join(dir, "large.md"), []byte(strings.Repeat("a", maxPreview-1)+"中文"), 0644)
	os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0, 1, 2}, 0644)
	metadata, _ := json.Marshal(Version{Commit: commit})
	os.WriteFile(filepath.Join(root, "current.json"), metadata, 0644)
	l := newLibrary(root, Repository, "")
	p, err := l.Preview("large.md", "")
	if err != nil || !p.Truncated || p.Binary || len(p.Text) != maxPreview-1 {
		t.Fatalf("UTF-8 preview: %+v %v", p, err)
	}
	p, err = l.Preview("binary.bin", "")
	if err != nil || !p.Binary || p.Text != "" {
		t.Fatal("binary preview")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if f, _, err := l.Open("link", ""); err == nil {
		f.Close()
		t.Fatal("escaped snapshot using symlink")
	}
}
