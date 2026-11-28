package capability

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Small durable records precede destructive work. Bulk filesystem operations
// run outside the catalog/library locks and share one cleanup slot.
func writeRecord(name string, value any) error {
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".record-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), name)
}

type repositoryDeletion struct {
	Directory string `json:"directory"`
}

func (c *Catalog) deletionRecord(id string) string {
	return filepath.Join(c.root, ".deletions", id+".json")
}

func (c *Catalog) readDeletion(s Source, repo string) (string, bool, error) {
	b, err := os.ReadFile(c.deletionRecord(s.ID))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	var record repositoryDeletion
	if err != nil || json.Unmarshal(b, &record) != nil {
		return "", false, errors.New("删除记录无法读取")
	}
	dir := filepath.Join(c.root, filepath.FromSlash(record.Directory))
	// A journal must never become an arbitrary filesystem deletion request.
	if record.Directory == "." {
		if s.Kind != "github" || !strings.EqualFold(repo, Repository) || s.Branch != "" {
			return "", false, ErrPath
		}
	} else {
		expected, err := filepath.Rel(c.root, c.libraryDir(s, repo))
		if err != nil || record.Directory != filepath.ToSlash(expected) || !validPath(record.Directory) {
			return "", false, ErrPath
		}
	}
	return dir, true, nil
}

func (c *Catalog) startCleanup(l *Library, stage string, work func() error) {
	l.mu.Lock()
	l.updating, l.stage, l.lastError = true, stage, ""
	l.progress = Progress{}
	l.mu.Unlock()
	go func() {
		c.cleanupSlots <- struct{}{}
		err := work()
		<-c.cleanupSlots
		l.mu.Lock()
		l.updating, l.stage = false, ""
		if err != nil {
			l.lastError = err.Error()
		}
		l.mu.Unlock()
	}()
}

func (c *Catalog) startFileCleanup(l *Library) {
	c.startCleanup(l, "删除文件", l.purgeDeletedFiles)
}

func (c *Catalog) startRepositoryCleanup(item catalogItem) {
	l := item.library
	l.mu.Lock()
	l.deletingRepository = true
	l.mu.Unlock()
	c.startCleanup(l, "删除仓库", func() error {
		root, err := os.OpenRoot(c.root)
		if err != nil {
			return errors.New("资料目录无法读取，重试删除")
		}
		defer root.Close()
		rel, err := filepath.Rel(c.root, l.root)
		if err != nil {
			return ErrPath
		}
		if rel == "." {
			// The legacy single repository shares the catalog root with newer
			// repositories; never remove the catalog or its sibling libraries.
			for _, name := range []string{"versions", "local-content", "current.json", "local.json"} {
				if err := root.RemoveAll(name); err != nil {
					return errors.New("部分文件未删除，重试删除")
				}
			}
			entries, err := os.ReadDir(c.root)
			if err != nil {
				return errors.New("资料目录无法读取，重试删除")
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".archive-") {
					if err := root.RemoveAll(e.Name()); err != nil {
						return errors.New("下载文件未删除，重试删除")
					}
				}
			}
		} else {
			if !validPath(filepath.ToSlash(rel)) {
				return ErrPath
			}
			if err := root.RemoveAll(rel); err != nil {
				return errors.New("部分文件未删除，重试删除")
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		next := make([]catalogItem, 0, len(c.items))
		for _, i := range c.items {
			if i.source.ID != item.source.ID {
				next = append(next, i)
			}
		}
		if c.index != nil {
			if c.index.db == nil {
				return errors.New("原文索引未清理，重启后重试删除")
			}
			if err := c.deleteIndexSource(item.source.ID); err != nil {
				return errors.New("原文索引未删除，重试删除")
			}
		}
		if err := c.persist(itemsSources(next)); err != nil {
			return errors.New("文件已清理，配置保存失败，重试删除")
		}
		// Remove the journal before making the source available for re-addition.
		if err := os.Remove(c.deletionRecord(item.source.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("删除记录未完成，重试删除")
		}

		c.items = next
		l.mu.Lock()
		l.current = nil
		l.mu.Unlock()
		return nil
	})
}

func (c *Catalog) RetryCleanup(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range c.items {
		if item.source.ID != id {
			continue
		}
		status := item.library.Status()
		if status.Updating {
			return ErrSourceBusy
		}
		if !status.CleanupPending {
			return errors.New("没有待删除的文件")
		}
		if status.Deleting {
			c.startRepositoryCleanup(item)
		} else {
			c.startFileCleanup(item.library)
		}
		return nil
	}
	return ErrSourceMissing
}

func (l *Library) purgeDeletedFiles() error {
	l.mu.RLock()
	deleted := make([]string, 0)
	referenced := make(map[string]bool)
	for p, change := range l.local.Changes {
		if change.Deleted {
			deleted = append(deleted, p)
		}
		if change.Content != "" {
			referenced[change.Content] = true
		}
	}
	l.mu.RUnlock()
	root, err := os.OpenRoot(l.root)
	if err != nil {
		return errors.New("资料目录无法读取，重试删除")
	}
	defer root.Close()
	versions, err := os.ReadDir(filepath.Join(l.root, "versions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("历史版本无法读取，重试删除")
	}
	for _, v := range versions {
		if !commitPattern.MatchString(v.Name()) {
			continue
		}
		for _, p := range deleted {
			if err := root.RemoveAll(filepath.Join("versions", v.Name(), filepath.FromSlash(p))); err != nil {
				return errors.New("部分文件未删除，重试删除")
			}
		}
		manifest := filepath.Join(l.root, "versions", v.Name()) + ".manifest.json"
		if b, err := os.ReadFile(manifest); err == nil {
			var entries []Entry
			if json.Unmarshal(b, &entries) == nil {
				kept := make([]Entry, 0, len(entries))
				for _, e := range entries {
					remove := false
					for _, p := range deleted {
						if e.Path == p || strings.HasPrefix(e.Path, p+"/") {
							remove = true
							break
						}
					}
					if !remove {
						kept = append(kept, e)
					}
				}
				if err := writeRecord(manifest, kept); err != nil {
					return errors.New("文件清单清理失败，重试删除")
				}
			}
		}
	}
	// Edited content is shared by digest; collect only unreferenced copies.
	contents, err := os.ReadDir(filepath.Join(l.root, "local-content"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("编辑副本无法读取，重试删除")
	}
	for _, e := range contents {
		if commitPattern.MatchString(e.Name()) && !referenced[e.Name()] {
			if err := root.RemoveAll(filepath.Join("local-content", e.Name())); err != nil {
				return errors.New("编辑副本未删除，重试删除")
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	next := l.local
	next.CleanupPending, next.PhysicalDeletion = false, true
	return l.saveLocal(next)
}
