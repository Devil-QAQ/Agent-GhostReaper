// Package capability manages downloaded reference material. It never executes it.
package capability

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const Repository = "Devil-QAQ/Vulnerability-Wiki-PoC"
const SourceURL = "https://github.com/" + Repository
const maxArchive = 512 << 20
const maxExtracted = 2 << 30
const maxPreview = 256 << 10

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
var ErrNotReady = errors.New("能力库尚未下载")
var ErrPath = errors.New("文件路径无效")

type Version struct {
	Branch     string    `json:"branch"`
	Commit     string    `json:"commit"`
	UpdatedAt  time.Time `json:"updated_at"`
	CheckedAt  time.Time `json:"checked_at"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
	AddedFiles *int      `json:"added_files,omitempty"`
}

type Status struct {
	CleanupPending bool      `json:"cleanup_pending,omitempty"`
	Deleting       bool      `json:"deleting,omitempty"`
	LocalRevision  uint64    `json:"local_revision"`
	Repository     string    `json:"repository"`
	SourceURL      string    `json:"source_url"`
	Ready          bool      `json:"ready"`
	Updating       bool      `json:"updating"`
	Stage          string    `json:"stage,omitempty"`
	Error          string    `json:"error,omitempty"`
	Version        *Version  `json:"version,omitempty"`
	Progress       *Progress `json:"progress,omitempty"`
}

type Progress struct {
	Bytes      int64 `json:"bytes"`
	TotalBytes int64 `json:"total_bytes,omitempty"`
	Files      int   `json:"files"`
	TotalFiles int   `json:"total_files,omitempty"`
}

type Entry struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Directory   bool   `json:"directory"`
	Size        int64  `json:"size"`
}

type Listing struct {
	Revision uint64  `json:"revision"`
	Entries  []Entry `json:"entries"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Commit   string  `json:"commit"`
}

type Preview struct {
	Revision  uint64 `json:"revision"`
	Conflict  bool   `json:"conflict"`
	Path      string `json:"path"`
	Text      string `json:"text"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
	Commit    string `json:"commit"`
}

type snapshot struct {
	version Version
	entries []Entry
}

type Library struct {
	local              localState
	localErr           error
	root               string
	client             *http.Client
	apiURL, archiveURL string
	repository, branch string
	mu                 sync.RWMutex
	current            *snapshot
	updating           bool
	stage, lastError   string
	progress           Progress
	deletingRepository bool
}

func newLibrary(root, repository, branch string) *Library {
	l := &Library{root: root, repository: repository, branch: branch, client: &http.Client{Timeout: 8 * time.Minute}, apiURL: "https://api.github.com", archiveURL: "https://codeload.github.com"}
	l.loadLocal()
	data, err := os.ReadFile(filepath.Join(root, "current.json"))
	if errors.Is(err, os.ErrNotExist) {
		return l
	}
	var v Version
	if err != nil || json.Unmarshal(data, &v) != nil || !commitPattern.MatchString(v.Commit) {
		l.lastError = "本地能力库记录读取失败，可重新下载"
		return l
	}
	entries, size, err := scan(l.snapshotDir(v.Commit))
	if err != nil || (len(entries) == 0 && !l.hasDeletedPaths()) {
		l.lastError = "本地能力库文件读取失败，可重新下载"
		return l
	}
	v.Files, v.Bytes = fileCount(entries), size
	l.current = &snapshot{version: v, entries: entries}
	return l
}

func (l *Library) Status() Status {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Status{LocalRevision: l.local.Revision, Repository: l.repository, Ready: l.current != nil, Updating: l.updating, Stage: l.stage, Error: l.lastError}
	s.CleanupPending = l.local.CleanupPending || l.deletingRepository
	s.Deleting = l.deletingRepository
	if l.updating {
		p := l.progress
		s.Progress = &p
	}
	if l.repository != "" {
		s.SourceURL = "https://github.com/" + l.repository
	}
	if l.current != nil {
		v := l.current.version
		if len(l.local.Changes) != 0 {
			effective := l.effectiveEntries(l.current)
			v.Files = fileCount(effective)
			v.Bytes = 0
			for _, e := range effective {
				if !e.Directory {
					v.Bytes += e.Size
				}
			}
		}
		s.Version = &v
	}
	return s
}

// StartUpdate is independent of the browser request: closing the page cannot
// leave a partial library active. Only a fully extracted snapshot is published.
func (l *Library) StartUpdate() bool {
	l.mu.Lock()
	if l.updating || l.local.CleanupPending || l.deletingRepository {
		l.mu.Unlock()
		return false
	}
	l.updating, l.stage, l.lastError = true, "检查更新", ""
	l.progress = Progress{}
	l.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := l.update(ctx)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.updating, l.stage = false, ""
		if err != nil {
			l.lastError = err.Error()
		}
	}()
	return true
}

func (l *Library) setStage(stage string)            { l.mu.Lock(); l.stage = stage; l.mu.Unlock() }
func (l *Library) setProgress(p Progress)           { l.mu.Lock(); l.progress = p; l.mu.Unlock() }
func (l *Library) snapshotDir(commit string) string { return filepath.Join(l.root, "versions", commit) }

func (l *Library) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("下载地址无效")
	}
	req.Header.Set("User-Agent", "GhostReaper-Capability-Library")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub 连接失败，请检查网络后重试")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			return nil, errors.New("GitHub 暂时限制访问，请稍后重试")
		}
		return nil, fmt.Errorf("GitHub 返回 HTTP %d，请检查网络后重试", resp.StatusCode)
	}
	return resp, nil
}

func (l *Library) getJSON(ctx context.Context, url string, out any) error {
	resp, err := l.get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("GitHub 返回内容无法读取，请重试")
	}
	return nil
}

func (l *Library) update(ctx context.Context) error {
	branch := l.branch
	if branch == "" {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := l.getJSON(ctx, l.apiURL+"/repos/"+l.repository, &repo); err != nil {
			return err
		}
		if repo.DefaultBranch == "" {
			return errors.New("未找到仓库默认分支")
		}
		branch = repo.DefaultBranch
	}
	var head struct {
		SHA string `json:"sha"`
	}
	if err := l.getJSON(ctx, l.apiURL+"/repos/"+l.repository+"/commits/"+url.PathEscape(branch), &head); err != nil {
		return err
	}
	if !commitPattern.MatchString(head.SHA) {
		return errors.New("仓库版本号无效")
	}
	l.mu.RLock()
	old := l.current
	l.mu.RUnlock()
	if old != nil && old.version.Commit == head.SHA {
		next := &snapshot{version: old.version, entries: old.entries}
		next.version.CheckedAt = time.Now().UTC()
		return l.publish(next)
	}
	if err := os.MkdirAll(filepath.Join(l.root, "versions"), 0o755); err != nil {
		return errors.New("能力库目录无法写入，请检查磁盘和权限")
	}
	stage, err := os.MkdirTemp(filepath.Join(l.root, "versions"), ".download-")
	if err != nil {
		return errors.New("下载目录无法创建，请检查磁盘和权限")
	}
	defer os.RemoveAll(stage)
	defer os.Remove(stage + ".manifest.json")
	l.setStage("下载文件")
	resp, err := l.get(ctx, l.archiveURL+"/"+l.repository+"/zip/"+head.SHA)
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp(l.root, ".archive-*.zip")
	if err != nil {
		resp.Body.Close()
		return errors.New("下载文件无法保存，请检查磁盘和权限")
	}
	defer os.Remove(archive.Name())
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	l.setProgress(Progress{TotalBytes: total})
	transfer := &progressReader{reader: resp.Body, report: func(n int64) { l.setProgress(Progress{Bytes: n, TotalBytes: total}) }}
	n, copyErr := io.Copy(archive, io.LimitReader(transfer, maxArchive+1))
	resp.Body.Close()
	closeErr := archive.Close()
	if copyErr != nil || closeErr != nil {
		return errors.New("文件下载未完成，请重试")
	}
	if n > maxArchive {
		return errors.New("仓库下载包超过 512 MiB，未替换本地内容")
	}
	l.setStage("整理文件")
	l.setProgress(Progress{})
	if err := extractWithProgress(ctx, archive.Name(), stage, l.setProgress, l.hidden); err != nil {
		return err
	}
	entries, size, err := scan(stage)
	if err != nil || (fileCount(entries) == 0 && !l.hasDeletedPaths()) {
		return errors.New("下载包没有可读取的文件，未替换本地内容")
	}
	dest := l.snapshotDir(head.SHA)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, dest); err != nil {
			return errors.New("能力库文件保存失败，请检查磁盘和权限")
		}
	} else if err != nil {
		return errors.New("能力库目录无法读取")
	} else {
		// Only an already existing snapshot needs a second scan.
		entries, size, err = scan(dest)
		if err != nil || (fileCount(entries) == 0 && !l.hasDeletedPaths()) {
			return errors.New("本地版本无法读取，未替换当前内容")
		}
	}
	now := time.Now().UTC()
	added := addedFiles(old, entries)
	return l.publish(&snapshot{version: Version{Branch: branch, Commit: head.SHA, UpdatedAt: now, CheckedAt: now, Files: fileCount(entries), Bytes: size, AddedFiles: &added}, entries: entries})
}

func addedFiles(previous *snapshot, entries []Entry) int {
	old := make(map[string]bool)
	if previous != nil {
		for _, e := range previous.entries {
			if !e.Directory {
				old[e.Path] = true
			}
		}
	}
	n := 0
	for _, e := range entries {
		if !e.Directory && !old[e.Path] {
			n++
		}
	}
	return n
}

func (l *Library) publish(next *snapshot) error {
	if err := writeRecord(l.snapshotDir(next.version.Commit)+".manifest.json", next.entries); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next.version, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(l.root, ".current-*.json")
	if err != nil {
		return errors.New("能力库记录无法保存，请检查磁盘和权限")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("能力库记录写入失败")
	}
	if err := os.Rename(f.Name(), filepath.Join(l.root, "current.json")); err != nil {
		return errors.New("能力库版本切换失败，原内容仍可使用")
	}
	l.mu.Lock()
	l.current = next
	l.mu.Unlock()
	return nil
}

func extract(ctx context.Context, archive, dest string) error {
	return extractWithProgress(ctx, archive, dest, nil, nil)
}

func extractWithProgress(ctx context.Context, archive, dest string, report func(Progress), excluded func(string) bool) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("下载包无法解压，请重试")
	}
	defer z.Close()
	if len(z.File) > 50000 {
		return errors.New("下载包文件数量超过上限")
	}
	hashes := map[string]string{}
	var total int64
	var files, totalFiles int
	for _, f := range z.File {
		if !f.FileInfo().IsDir() {
			totalFiles++
		}
	}
	lastReport := time.Time{}
	noteFile := func() {
		files++
		if report != nil && (files == totalFiles || time.Since(lastReport) >= 100*time.Millisecond) {
			report(Progress{Files: files, TotalFiles: totalFiles, Bytes: total})
			lastReport = time.Now()
		}
	}
	rootName := ""
	for _, f := range z.File {
		if ctx.Err() != nil {
			return errors.New("下载整理超时，请重试")
		}
		name := f.Name
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			return errors.New("下载包目录格式无效")
		}
		if rootName == "" {
			rootName = parts[0]
		}
		if parts[0] != rootName {
			return errors.New("下载包包含不同根目录")
		}
		rel := strings.TrimSuffix(parts[1], "/")
		if rel == "" {
			continue
		}
		if !validPath(rel) || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return errors.New("下载包包含无效文件路径")
		}
		if excluded != nil && excluded(rel) {
			if !f.FileInfo().IsDir() {
				noteFile()
			}
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return errors.New("解压目录无法写入")
			}
			continue
		}
		if f.UncompressedSize64 > uint64(maxExtracted-total) {
			return errors.New("解压内容超过 2 GiB，未替换本地内容")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return errors.New("解压目录无法写入")
		}
		src, err := f.Open()
		if err != nil {
			return errors.New("下载包文件损坏")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			src.Close()
			return errors.New("下载包文件重复或无法保存")
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, maxExtracted-total+1))
		hashes[rel] = hex.EncodeToString(h.Sum(nil))
		src.Close()
		closeErr := out.Close()
		total += n
		if copyErr != nil || closeErr != nil {
			return errors.New("下载包文件损坏或磁盘无法写入")
		}
		if total > maxExtracted {
			return errors.New("解压内容超过 2 GiB，未替换本地内容")
		}
		noteFile()
	}
	return writeRecord(dest+".manifest.json", hashes)
}

func validPath(p string) bool {
	return p != "" && p != "." && !strings.ContainsAny(p, "\\\x00:") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func scan(root string) ([]Entry, int64, error) {
	entries := make([]Entry, 0)
	hashes := map[string]string{}
	if data, err := os.ReadFile(root + ".manifest.json"); err == nil {
		var saved []Entry
		if json.Unmarshal(data, &saved) == nil {
			for _, e := range saved {
				hashes[e.Path] = e.Fingerprint
			}
		} else {
			_ = json.Unmarshal(data, &hashes)
		}
	}
	var size int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrPath
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return ErrPath
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		entries = append(entries, Entry{Path: rel, Name: path.Base(rel), Directory: d.IsDir(), Size: info.Size(), Fingerprint: hashes[rel]})
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return entries, size, err
}
func fileCount(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if !e.Directory {
			n++
		}
	}
	return n
}

func (l *Library) List(dir, query string, pageNumber int) (Listing, error) {
	if dir != "" && !validPath(dir) {
		return Listing{}, ErrPath
	}
	l.mu.RLock()
	current := l.current
	defer l.mu.RUnlock()
	if l.localErr != nil {
		return Listing{}, l.localErr
	}
	if l.deletingRepository {
		return Listing{}, ErrSourceBusy
	}
	if current == nil {
		return Listing{}, ErrNotReady
	}
	if dir != "" {
		found := false
		for _, e := range l.effectiveEntries(current) {
			if e.Path == dir && e.Directory {
				found = true
				break
			}
		}
		if !found {
			return Listing{}, os.ErrNotExist
		}
	}
	entries := make([]Entry, 0)
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, e := range l.effectiveEntries(current) {
		if needle == "" {
			parent := path.Dir(e.Path)
			if parent == "." {
				parent = ""
			}
			if parent != dir {
				continue
			}
		} else {
			if e.Directory || (dir != "" && !strings.HasPrefix(e.Path, dir+"/")) || !strings.Contains(strings.ToLower(e.Path), needle) {
				continue
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return entries[i].Path < entries[j].Path
	})
	const pageSize = 50
	if pageNumber < 1 {
		pageNumber = 1
	}
	lastPage := (len(entries) + pageSize - 1) / pageSize
	if lastPage < 1 {
		lastPage = 1
	}
	if pageNumber > lastPage {
		pageNumber = lastPage
	}
	start := (pageNumber - 1) * pageSize
	end := start + pageSize
	if end > len(entries) {
		end = len(entries)
	}
	return Listing{Revision: l.local.Revision, Entries: entries[start:end], Total: len(entries), Page: pageNumber, PageSize: pageSize, Commit: current.version.Commit}, nil
}

// Open reads a version snapshot, excluding explicit deletions in all versions.
// os.Root also prevents
// a local symlink replacement from escaping the library directory.
func (l *Library) Open(name, commit string) (*os.File, string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.openLocked(name, commit)
}
func (l *Library) openLocked(name, commit string) (*os.File, string, error) {
	if !validPath(name) {
		return nil, "", ErrPath
	}
	current := l.current
	if l.localErr != nil {
		return nil, "", l.localErr
	}
	if current == nil {
		return nil, "", ErrNotReady
	}
	if commit == "" {
		commit = current.version.Commit
	}
	if !commitPattern.MatchString(commit) {
		return nil, "", ErrPath
	}
	if l.deletingRepository {
		return nil, "", ErrSourceBusy
	}
	if l.hidden(name) {
		return nil, "", os.ErrNotExist
	}
	if commit == current.version.Commit {
		if ch, ok := l.local.Changes[name]; ok && ch.Content != "" {
			root, err := os.OpenRoot(filepath.Join(l.root, "local-content"))
			if err != nil {
				return nil, "", err
			}
			defer root.Close()
			f, err := root.Open(ch.Content)
			return f, commit, err
		}
	}
	root, err := os.OpenRoot(l.snapshotDir(commit))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, "", ErrPath
	}
	return f, commit, nil
}

func (l *Library) Preview(name, commit string) (Preview, error) {
	return l.preview(name, commit, false)
}
func (l *Library) PreviewUpstream(name, commit string) (Preview, error) {
	return l.preview(name, commit, true)
}
func (l *Library) preview(name, commit string, upstream bool) (Preview, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.current == nil {
		return Preview{}, ErrNotReady
	}
	if commit == "" {
		commit = l.current.version.Commit
	}
	if !validPath(name) || !commitPattern.MatchString(commit) {
		return Preview{}, ErrPath
	}
	if l.deletingRepository {
		return Preview{}, ErrSourceBusy
	}
	if l.hidden(name) {
		return Preview{}, os.ErrNotExist
	}
	var f *os.File
	var err error
	version := commit
	if upstream {
		f, err = l.rawOpen(name, commit)
	} else {
		f, version, err = l.openLocked(name, commit)
	}
	if err != nil {
		return Preview{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Preview{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPreview+1))
	if err != nil {
		return Preview{}, err
	}
	truncated := len(data) > maxPreview
	if truncated {
		data = data[:maxPreview]
		for removed := 0; removed < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); removed++ {
			data = data[:len(data)-1]
		}
	}
	binary := strings.ContainsRune(string(data), '\x00') || !utf8.Valid(data)
	text := ""
	if !binary {
		text = string(data)
	}
	revision := l.local.Revision
	conflict := false
	if ch, ok := l.local.Changes[name]; ok {
		conflict = l.conflict(name, ch)
	}
	return Preview{Revision: revision, Conflict: conflict, Path: name, Text: text, Binary: binary, Truncated: truncated, Size: info.Size(), Commit: version}, nil
}
