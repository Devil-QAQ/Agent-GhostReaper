package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ImportFolder receives browser-selected files without executing or converting
// their contents. A failed upload leaves the currently visible version intact.
func (c *Catalog) ImportFolder(ctx context.Context, id, name string, reader *multipart.Reader) error {
	return c.importFolder(ctx, id, name, reader, false)
}

// Receive the complete upload before returning; publish independently of the
// request so navigation or a closed tab cannot interrupt the final switch.
func (c *Catalog) ImportFolderAsync(ctx context.Context, id, name string, reader *multipart.Reader) error {
	return c.importFolder(ctx, id, name, reader, true)
}

func (c *Catalog) importFolder(ctx context.Context, id, name string, reader *multipart.Reader, background bool) error {
	s, _, err := normalizeSource(Source{ID: id, Name: name, Kind: "folder"})
	if err != nil {
		return err
	}
	if s.ID == "" {
		s.ID, err = newSourceID()
		if err != nil {
			return err
		}
	}
	c.mu.Lock()
	index := -1
	for n, i := range c.items {
		if i.source.ID == s.ID {
			index = n
		}
	}
	if id != "" && index < 0 {
		c.mu.Unlock()
		return ErrSourceMissing
	}
	if index >= 0 && c.items[index].source.Kind != "folder" {
		c.mu.Unlock()
		return errors.New("来源类型不匹配")
	}
	if c.importing[s.ID] || (index >= 0 && (c.items[index].library.Status().Updating || c.items[index].library.Status().CleanupPending)) {
		c.mu.Unlock()
		return ErrSourceBusy
	}
	if index >= 0 {
		s.Instance = c.items[index].source.Instance
	} else {
		s.Instance, err = newSourceID()
		if err != nil {
			c.mu.Unlock()
			return err
		}
	}
	var l *Library
	if index >= 0 {
		l = c.items[index].library
	} else {
		l = newLibrary(c.libraryDir(s, ""), "", "")
		next := append(append([]catalogItem(nil), c.items...), catalogItem{s, l})
		if err := c.persist(itemsSources(next)); err != nil {
			c.mu.Unlock()
			return err
		}
		c.items = next
	}
	c.importing[s.ID] = true
	l.mu.Lock()
	l.updating, l.stage, l.lastError = true, "上传文件", ""
	l.progress = Progress{}
	l.mu.Unlock()
	c.mu.Unlock()
	finish := func(err error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.importing, s.ID)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.updating, l.stage = false, ""
		if err != nil {
			l.lastError = err.Error()
		}
	}
	stage, err := os.MkdirTemp(c.root, ".import-")
	if err != nil {
		err = errors.New("导入目录无法创建")
		finish(err)
		return err
	}
	next, err := receiveFolder(ctx, reader, stage, l.setProgress, l.hidden)
	if err != nil {
		os.RemoveAll(stage)
		finish(err)
		return err
	}
	l.setStage("整理文件")
	complete := func() error {
		err := c.publishFolder(s, l, next, stage)
		os.RemoveAll(stage)
		finish(err)
		return err
	}
	if background {
		go complete()
		return nil
	}
	return complete()
}

func (c *Catalog) publishFolder(s Source, l *Library, received *snapshot, stage string) error {
	l.mu.RLock()
	old := l.current
	l.mu.RUnlock()
	commit, entries, size := received.version.Commit, received.entries, received.version.Bytes
	dest := l.snapshotDir(commit)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return errors.New("导入目录无法写入")
	}
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, dest); err != nil {
			return errors.New("导入文件无法保存")
		}
	} else if err != nil {
		return errors.New("导入目录无法读取")
	} else if entries, size, err = scan(dest); err != nil {
		return errors.New("导入文件无法读取")
	}
	now := time.Now().UTC()
	added := addedFiles(old, entries)
	nextVersion := Version{Commit: commit, UpdatedAt: now, CheckedAt: now, Files: fileCount(entries), Bytes: size, AddedFiles: &added}
	if old != nil && old.version.Commit == commit {
		nextVersion = old.version
		nextVersion.CheckedAt = now
	}
	if err := l.publish(&snapshot{version: nextVersion, entries: entries}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := append([]catalogItem(nil), c.items...)
	// Other sources may have changed while the upload was in progress.
	found := false
	for n, i := range next {
		if i.source.ID == s.ID {
			next[n] = catalogItem{s, l}
			found = true
			break
		}
	}
	if !found {
		next = append(next, catalogItem{s, l})
	}
	if err := c.persist(itemsSources(next)); err != nil {
		if old != nil {
			_ = l.publish(old)
		} else {
			_ = os.Remove(filepath.Join(l.root, "current.json"))
			l.mu.Lock()
			l.current = nil
			l.mu.Unlock()
		}
		return err
	}
	c.items = next
	return nil
}

func receiveFolder(ctx context.Context, reader *multipart.Reader, dest string, report func(Progress), excluded func(string) bool) (*snapshot, error) {
	hashes := map[string]string{}
	entries := map[string]Entry{}
	var total int64
	var stored int64
	lastReport := time.Time{}
	rootName := ""
	for {
		if ctx.Err() != nil {
			return nil, errors.New("导入已中断，请重试")
		}
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("文件夹上传未完成，请重试")
		}
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil || part.FormName() != "files" {
			part.Close()
			return nil, errors.New("导入文件格式无效")
		}
		name := params["filename"]
		if !validPath(name) {
			part.Close()
			return nil, ErrPath
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 || !validPath(parts[1]) {
			part.Close()
			return nil, errors.New("请选择完整文件夹")
		}
		if rootName == "" {
			rootName = parts[0]
		}
		if parts[0] != rootName {
			part.Close()
			return nil, errors.New("每次只能导入一个文件夹")
		}
		rel := parts[1]
		if _, exists := hashes[rel]; exists {
			part.Close()
			return nil, errors.New("导入文件路径重复")
		}
		if len(hashes) >= 50000 {
			part.Close()
			return nil, errors.New("导入文件数量超过 50000 个")
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		skip := excluded(rel)
		var out *os.File
		if !skip {
			parent := filepath.ToSlash(filepath.Dir(rel))
			if _, known := entries[parent]; parent != "." && !known {
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					part.Close()
					return nil, errors.New("导入目录无法写入")
				}
			}
			out, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if err != nil {
				part.Close()
				return nil, errors.New("导入文件无法保存")
			}
		}
		h := sha256.New()
		before := total
		transfer := &progressReader{reader: part, report: func(n int64) {
			if time.Since(lastReport) >= 100*time.Millisecond {
				report(Progress{Files: len(hashes), Bytes: before + n})
				lastReport = time.Now()
			}
		}}
		var writer io.Writer = h
		if out != nil {
			writer = io.MultiWriter(out, h)
		}
		n, copyErr := io.Copy(writer, io.LimitReader(transfer, maxExtracted-total+1))
		var closeErr error
		if out != nil {
			closeErr = out.Close()
		}
		total += n
		if copyErr != nil || closeErr != nil {
			part.Close()
			return nil, errors.New("导入文件未完整写入")
		}
		if total > maxExtracted {
			part.Close()
			return nil, errors.New("导入内容超过 2 GiB")
		}
		if err := part.Close(); err != nil {
			return nil, errors.New("导入文件未完整上传")
		}
		hashes[rel] = hex.EncodeToString(h.Sum(nil))
		if !skip {
			stored += n
			entries[rel] = Entry{Path: rel, Name: filepath.Base(rel), Size: n, Fingerprint: hashes[rel]}
			for parent := filepath.ToSlash(filepath.Dir(rel)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
				if _, exists := entries[parent]; exists {
					break
				}
				entries[parent] = Entry{Path: parent, Name: filepath.Base(parent), Directory: true}
			}
		}
		if time.Since(lastReport) >= 100*time.Millisecond {
			report(Progress{Files: len(hashes), Bytes: total})
			lastReport = time.Now()
		}
	}
	if len(hashes) == 0 {
		return nil, errors.New("所选文件夹没有文件")
	}
	paths := make([]string, 0, len(hashes))
	for p := range hashes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		io.WriteString(h, p)
		h.Write([]byte{0})
		io.WriteString(h, hashes[p])
		h.Write([]byte{0})
	}
	all := make([]Entry, 0, len(entries))
	for _, e := range entries {
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	report(Progress{Files: len(hashes), Bytes: total})
	return &snapshot{version: Version{Commit: hex.EncodeToString(h.Sum(nil)), Files: fileCount(all), Bytes: stored}, entries: all}, nil
}
