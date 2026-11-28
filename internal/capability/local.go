package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrStale = errors.New("内容已变化，请重新打开后操作")

type LocalChange struct {
	Path      string    `json:"path"`
	Deleted   bool      `json:"deleted"`
	Content   string    `json:"content,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Base      string    `json:"base,omitempty"`
	Conflict  bool      `json:"conflict"`
	DeletedAt time.Time `json:"deleted_at,omitzero"`
}
type localState struct {
	Revision         uint64                 `json:"revision"`
	Changes          map[string]LocalChange `json:"changes"`
	CleanupPending   bool                   `json:"cleanup_pending,omitempty"`
	PhysicalDeletion bool                   `json:"physical_deletion,omitempty"`
}
type Mutation struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Revision uint64 `json:"revision"`
	Action   string `json:"action"`
	Text     string `json:"text"`
}

func (l *Library) loadLocal() {
	l.local.Changes = map[string]LocalChange{}
	b, err := os.ReadFile(filepath.Join(l.root, "local.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || json.Unmarshal(b, &l.local) != nil {
		l.localErr = errors.New("本地改动记录无法读取")
		return
	}
	for p, c := range l.local.Changes {
		if !validPath(p) || (c.Content != "" && !commitPattern.MatchString(c.Content)) {
			l.localErr = ErrPath
		}
	}
	if l.local.Changes == nil {
		l.local.Changes = map[string]LocalChange{}
	}
	for p, change := range l.local.Changes {
		if change.Deleted && !l.local.PhysicalDeletion {
			l.local.CleanupPending = true
		}
		if !change.Deleted && l.hidden(p) {
			delete(l.local.Changes, p)
		}
	}
}
func (l *Library) hidden(p string) bool {
	for p != "." {
		if change, ok := l.local.Changes[p]; ok && change.Deleted {
			return true
		}
		p = filepath.ToSlash(filepath.Dir(p))
	}
	return false
}
func (l *Library) hasDeletedPaths() bool {
	for _, change := range l.local.Changes {
		if change.Deleted {
			return true
		}
	}
	return false
}
func (l *Library) effectiveEntries(s *snapshot) []Entry {
	out := make([]Entry, 0, len(s.entries))
	seen := map[string]bool{}
	for _, e := range s.entries {
		seen[e.Path] = true
		if l.hidden(e.Path) {
			continue
		}
		if c, ok := l.local.Changes[e.Path]; ok && !c.Deleted {
			e.Size = c.Size
			e.Fingerprint = c.Content
		}
		out = append(out, e)
	}
	for p, c := range l.local.Changes {
		if c.Content == "" || seen[p] || l.hidden(p) {
			continue
		}
		out = append(out, Entry{Path: p, Name: filepath.Base(p), Size: c.Size, Fingerprint: c.Content})
		seen[p] = true
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if !seen[parent] {
				out = append(out, Entry{Path: parent, Name: filepath.Base(parent), Directory: true})
				seen[parent] = true
			}
		}
	}
	return out
}
func (l *Library) rawOpen(name, commit string) (*os.File, error) {
	root, err := os.OpenRoot(l.snapshotDir(commit))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(filepath.FromSlash(name))
}
func (l *Library) baseHash(name string) string {
	if l.current == nil {
		return "missing"
	}
	for _, e := range l.current.entries {
		if e.Path == name && e.Fingerprint != "" {
			return e.Fingerprint
		}
	}
	f, err := l.rawOpen(name, l.current.version.Commit)
	if err != nil {
		return "missing"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "missing"
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (l *Library) conflict(name string, c LocalChange) bool {
	if c.Deleted {
		return false
	}
	h := l.baseHash(name)
	return h != c.Base && h != c.Content
}

type ChangesView struct {
	Changes  []LocalChange `json:"changes"`
	Revision uint64        `json:"revision"`
	Version  string        `json:"version"`
}

func (l *Library) Changes() (ChangesView, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.localErr != nil {
		return ChangesView{}, l.localErr
	}
	out := make([]LocalChange, 0, len(l.local.Changes))
	for p, c := range l.local.Changes {
		c.Path = p
		c.Conflict = l.conflict(p, c)
		c.Content = ""
		c.Base = ""
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	v := ""
	if l.current != nil {
		v = l.current.version.Commit
	}
	return ChangesView{Changes: out, Revision: l.local.Revision, Version: v}, nil
}
func (c *Catalog) Mutate(id string, m Mutation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, i := range c.items {
		if i.source.ID == id {
			if c.importing[id] {
				return ErrSourceBusy
			}
			if err := i.library.mutate(m); err != nil {
				return err
			}
			if m.Action == "delete" {
				if err := c.pruneIndexLocked(i); err != nil {
					return err
				}
				c.startFileCleanup(i.library)
			}
			return nil
		}
	}
	return ErrSourceMissing
}
func (l *Library) mutate(m Mutation) error {
	if !validPath(m.Path) {
		return ErrPath
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.localErr != nil {
		return l.localErr
	}
	if l.updating || l.local.CleanupPending || l.deletingRepository {
		return ErrSourceBusy
	}
	if l.current == nil {
		return ErrNotReady
	}
	if m.Version != l.current.version.Commit || m.Revision != l.local.Revision {
		return ErrStale
	}
	next := localState{Revision: l.local.Revision + 1, Changes: make(map[string]LocalChange)}
	next.PhysicalDeletion = l.local.PhysicalDeletion
	for p, c := range l.local.Changes {
		next.Changes[p] = c
	}
	switch m.Action {
	case "restore":
		if ch, ok := next.Changes[m.Path]; !ok {
			return os.ErrNotExist
		} else if ch.Deleted {
			return errors.New("文件已删除，无法恢复")
		}
		delete(next.Changes, m.Path)
	case "keep":
		ch, ok := next.Changes[m.Path]
		if !ok || ch.Deleted {
			return os.ErrNotExist
		}
		ch.Base = l.baseHash(m.Path)
		next.Changes[m.Path] = ch
	case "delete", "save":
		var entry *Entry
		for _, e := range l.effectiveEntries(l.current) {
			if e.Path == m.Path {
				x := e
				entry = &x
				break
			}
		}
		if entry == nil {
			if ch, ok := l.local.Changes[m.Path]; ok && ch.Content != "" {
				entry = &Entry{Path: m.Path, Size: ch.Size}
			}
		}
		if entry == nil || l.hidden(m.Path) {
			return os.ErrNotExist
		}
		if m.Action == "delete" {
			for p := range next.Changes {
				if p == m.Path || strings.HasPrefix(p, m.Path+"/") {
					delete(next.Changes, p)
				}
			}
			next.Changes[m.Path] = LocalChange{Deleted: true, DeletedAt: time.Now().UTC()}
			next.CleanupPending = true
			break
		}
		if entry.Directory || len(m.Text) > maxPreview || !utf8.ValidString(m.Text) || strings.ContainsRune(m.Text, 0) {
			return errors.New("仅支持编辑 256 KiB 以内的文本文件")
		}
		// Do not allow a truncated or binary original to be replaced through the editor.
		f, err := l.rawOpen(m.Path, l.current.version.Commit)
		if err != nil && l.local.Changes[m.Path].Content == "" {
			return err
		}
		var b []byte
		if f != nil {
			b, err = io.ReadAll(io.LimitReader(f, maxPreview+1))
			f.Close()
		} else {
			err = nil
		}
		if err != nil || len(b) > maxPreview || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
			return errors.New("此文件不支持文本编辑")
		}
		digest := sha256.Sum256([]byte(m.Text))
		key := hex.EncodeToString(digest[:])
		dir := filepath.Join(l.root, "local-content")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dir, key), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			_, we := out.Write([]byte(m.Text))
			ce := out.Close()
			if we != nil || ce != nil {
				os.Remove(filepath.Join(dir, key))
				return errors.New("编辑内容写入失败")
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		next.Changes[m.Path] = LocalChange{Content: key, Size: int64(len(m.Text)), Base: l.baseHash(m.Path)}
	default:
		return errors.New("操作无效")
	}
	return l.saveLocal(next)
}

func (l *Library) saveLocal(next localState) error {
	if err := writeRecord(filepath.Join(l.root, "local.json"), next); err != nil {
		return errors.New("本地改动无法保存")
	}
	l.local = next
	return nil
}
