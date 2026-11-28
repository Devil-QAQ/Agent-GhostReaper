package capability

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"
)

// EffectiveView is the single published view, including local edits and deletions.
// A file token changes only when its input changes; unrelated edits do not invalidate it.
type EffectiveFile struct {
	Entry
	Token    string `json:"token"`
	Conflict bool   `json:"conflict"`
}
type EffectiveView struct {
	Commit   string          `json:"commit"`
	Revision uint64          `json:"revision"`
	Files    []EffectiveFile `json:"files"`
}

func (l *Library) Effective() (EffectiveView, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.effectiveLocked()
}
func (l *Library) effectiveLocked() (EffectiveView, error) {
	if l.localErr != nil {
		return EffectiveView{}, l.localErr
	}
	if l.deletingRepository {
		return EffectiveView{}, ErrSourceBusy
	}
	if l.current == nil {
		return EffectiveView{}, ErrNotReady
	}
	v := EffectiveView{Commit: l.current.version.Commit, Revision: l.local.Revision, Files: []EffectiveFile{}}
	for _, e := range l.effectiveEntries(l.current) {
		if e.Directory {
			continue
		}
		token := e.Fingerprint
		if token == "" {
			token = v.Commit + ":" + e.Path
		}
		conflict := false
		if ch, ok := l.local.Changes[e.Path]; ok {
			conflict = l.conflict(e.Path, ch)
		}
		v.Files = append(v.Files, EffectiveFile{e, token, conflict})
	}
	sort.Slice(v.Files, func(i, j int) bool { return v.Files[i].Path < v.Files[j].Path })
	return v, nil
}

// ReadEffective pins the exact current file under the library lock. Long files
// are read by byte range, independently of preview/editor limits.
func (l *Library) ReadEffective(name, token string, offset int64, limit int) (map[string]any, error) {
	if offset < 0 || limit < 1 || limit > 1<<20 {
		return nil, ErrPath
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, err := l.effectiveLocked()
	if err != nil {
		return nil, err
	}
	var file *EffectiveFile
	for _, e := range v.Files {
		if e.Path == name {
			x := e
			file = &x
			break
		}
	}
	if file == nil {
		return nil, os.ErrNotExist
	}
	if token != "" && token != file.Token {
		return nil, ErrStale
	}
	f, _, err := l.openLocked(name, v.Commit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Read enough lookahead to finish a UTF-8 rune at a tiny range boundary.
	// A range may exceed limit only to return its first complete rune.
	b := make([]byte, limit+utf8.UTFMax-1)
	n, err := f.ReadAt(b, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	b = b[:n]
	end := 0
	for end < len(b) && end < limit {
		_, size := utf8.DecodeRune(b[end:])
		if end > 0 && end+size > limit {
			break
		}
		end += size
	}
	// Invalid bytes are retained (and marked binary), never trimmed away.
	n = end
	b = b[:n]
	return map[string]any{"path": name, "token": file.Token, "commit": v.Commit, "revision": v.Revision, "conflict": file.Conflict, "offset": offset, "next_offset": offset + int64(n), "complete": offset+int64(n) >= file.Size, "text": string(b), "binary": !utf8.Valid(b) || strings.ContainsRune(string(b), 0)}, nil
}

type contentIndex struct {
	db  *sql.DB
	mu  sync.Mutex
	err string
}
type Coverage struct {
	SourceID     string `json:"source_id"`
	Name         string `json:"name"`
	Files        int    `json:"files"`
	Indexed      int    `json:"indexed"`
	Pending      int    `json:"pending"`
	Unsearchable int    `json:"unsearchable"`
	Error        string `json:"error,omitempty"`
}
type SearchHit struct {
	Instance    string `json:"instance"`
	Match       string `json:"match"`
	Score       int    `json:"score"`
	SourceID    string `json:"source_id"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	Commit      string `json:"commit"`
	Revision    uint64 `json:"revision"`
	Conflict    bool   `json:"conflict"`
	Snippet     string `json:"snippet"`
	Reason      string `json:"reason,omitempty"`
}
type SearchResult struct {
	Query    string      `json:"query"`
	SourceID string      `json:"source_id"`
	Hits     []SearchHit `json:"hits"`
	Coverage []Coverage  `json:"coverage"`
	Total    int         `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func (c *Catalog) openIndex() {
	idx := &contentIndex{}
	c.index = idx
	for attempt := 0; attempt < 2; attempt++ {
		db, err := sql.Open("sqlite", filepath.Join(c.root, "index.sqlite"))
		if err == nil {
			db.SetMaxOpenConns(1)
			var version int
			err = db.QueryRow("PRAGMA user_version").Scan(&version)
			if err == nil && version > 5 {
				db.Close()
				idx.setError("原文检索版本高于当前程序")
				return
			}
			if err == nil {
				err = migrateIndex(db)
			}
		}
		if err == nil {
			idx.db = db
			return
		}
		if db != nil {
			db.Close()
		}
		var sqliteErr *sqlite.Error
		if attempt == 0 && errors.As(err, &sqliteErr) && (sqliteErr.Code() == 11 || sqliteErr.Code() == 26) {
			if os.Remove(filepath.Join(c.root, "index.sqlite")) == nil {
				continue
			}
		}
		idx.setError("原文检索无法打开，重启后重试")
		return
	}

}

// RunIndex belongs to the application lifecycle. Current manifests are durable
// work requests, so a crash between publication and notification loses no work.
func (c *Catalog) RunIndex(ctx context.Context) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		c.reconcileIndex(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (c *Catalog) Close() error {
	if c == nil || c.index == nil {
		return nil
	}
	c.index.mu.Lock()
	defer c.index.mu.Unlock()
	if c.index.db != nil {
		return c.index.db.Close()
	}
	return nil
}
func (c *Catalog) indexItems() []catalogItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]catalogItem(nil), c.items...)
}
func (c *Catalog) reconcileIndex(ctx context.Context) {
	idx := c.index
	if idx == nil {
		return
	}
	if idx.db == nil {
		return
	}
	idx.setError("")
	items := c.indexItems()
	live := map[string]bool{}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		v, err := item.library.Effective()
		if err != nil {
			if errors.Is(err, ErrSourceBusy) || errors.Is(err, ErrNotReady) {
				continue
			}
			idx.setError("当前文件清单无法读取")
			continue
		}
		live[item.source.ID] = true
		existing := map[string]string{}
		fingerprints := map[string]string{}
		hydrated := map[string]string{}
		rows, err := idx.db.QueryContext(ctx, "SELECT path,token,fingerprint FROM files WHERE source=?", item.source.ID)
		if err != nil {
			idx.setError("原文检索读取失败")
			return
		}
		for rows.Next() {
			var p, t, h string
			rows.Scan(&p, &t, &h)
			fingerprints[p] = h
			if t == v.Commit+":"+p {
				hydrated[p] = h
			}
			existing[p] = t
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return
		}
		for _, e := range v.Files {
			if existing[e.Path] != e.Token && fingerprints[e.Path] == e.Token {
				if _, err := idx.db.ExecContext(ctx, "UPDATE files SET token=? WHERE source=? AND path=?", e.Token, item.source.ID, e.Path); err != nil {
					idx.setError("原文检索保存失败")
					return
				}
				existing[e.Path] = e.Token
			}
			if existing[e.Path] == e.Token {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			// Pin only the open, then read outside the lock. Commit is checked again below.
			l := item.library
			l.mu.RLock()
			if l.current == nil || l.current.version.Commit != v.Commit || l.local.Revision != v.Revision || l.deletingRepository {
				l.mu.RUnlock()
				break
			}
			f, _, err := l.openLocked(e.Path, v.Commit)
			l.mu.RUnlock()
			if err != nil {
				idx.setError("部分原文读取失败")
				continue
			}
			h := sha256.New()
			var b []byte
			reason := ""
			if e.Size > 16<<20 {
				_, err = io.Copy(h, f)
				reason = "文件超过 16 MiB，仅可检索路径"
			} else {
				b, err = io.ReadAll(io.TeeReader(f, h))
			}
			f.Close()
			if err != nil {
				idx.setError("部分原文读取失败")
				continue
			}
			digest := hex.EncodeToString(h.Sum(nil))
			if e.Fingerprint != "" && digest != e.Fingerprint {
				idx.setError("原文与文件清单不一致")
				continue
			}
			if reason == "" && (!utf8.Valid(b) || strings.ContainsRune(string(b), 0) || !strings.HasPrefix(http.DetectContentType(b), "text/")) {
				reason = "非文本文件，仅可检索路径"
				b = nil
			}
			l.mu.RLock()
			matches := !l.deletingRepository && l.current != nil && l.current.version.Commit == v.Commit && l.local.Revision == v.Revision
			if !matches {
				current, ve := l.effectiveLocked()
				if ve == nil {
					for _, now := range current.Files {
						if now.Path == e.Path && now.Token == e.Token {
							matches = true
							break
						}
					}
				}
			}
			if matches {
				_, err = idx.db.ExecContext(ctx, `INSERT INTO files VALUES(?,?,?,?,?,?,?) ON CONFLICT(source,path) DO UPDATE SET token=excluded.token,fingerprint=excluded.fingerprint,body=excluded.body,folded=excluded.folded,reason=excluded.reason`, item.source.ID, e.Path, e.Token, digest, string(b), strings.ToLower(string(b)), reason)
			}
			l.mu.RUnlock()
			if matches && err == nil && e.Fingerprint == "" {
				hydrated[e.Path] = digest
			}
			if err != nil {
				idx.setError("原文检索保存失败")
				return
			}
		}
		// Backfill legacy manifests once, without rereading unchanged bodies.
		l := item.library
		l.mu.Lock()
		if l.current != nil && l.current.version.Commit == v.Commit && !l.deletingRepository {
			next := &snapshot{version: l.current.version, entries: append([]Entry(nil), l.current.entries...)}
			changed := false
			for n, e := range next.entries {
				if e.Directory || e.Fingerprint != "" {
					continue
				}
				digest := hydrated[e.Path]
				if _, local := l.local.Changes[e.Path]; digest != "" && !local {
					next.entries[n].Fingerprint = digest
					changed = true
				}
			}
			if changed {
				if err := writeRecord(l.snapshotDir(v.Commit)+".manifest.json", next.entries); err == nil {
					l.current = next
				} else {
					idx.setError("文件清单保存失败")
				}
			}
		}
		l.mu.Unlock()
		// Always compare against the newest view before removing rows.
		latest, err := item.library.Effective()
		if err == nil {
			valid := map[string]bool{}
			for _, e := range latest.Files {
				valid[e.Path] = true
			}
			for p := range existing {
				if !valid[p] {
					if _, err = idx.db.ExecContext(ctx, "DELETE FROM files WHERE source=? AND path=?", item.source.ID, p); err != nil {
						idx.setError("旧原文清理失败")
						return
					}
				}
			}
		}
	}
	rows, err := idx.db.QueryContext(ctx, "SELECT DISTINCT source FROM files")
	if err != nil {
		return
	}
	var removed []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		if !live[id] {
			removed = append(removed, id)
		}
	}
	rows.Close()
	for _, id := range removed {
		if _, err = idx.db.ExecContext(ctx, "DELETE FROM files WHERE source=?", id); err != nil {
			idx.setError("旧原文清理失败")
			return
		}
	}
}
func (c *Catalog) Search(ctx context.Context, q, source string, page int) (SearchResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := SearchResult{Hits: []SearchHit{}, Coverage: []Coverage{}, Page: page, PageSize: 30}
	if out.Page < 1 {
		out.Page = 1
	}
	out.Query = strings.TrimSpace(q)
	out.SourceID = source
	q = strings.ToLower(strings.TrimSpace(q))
	idx := c.index
	if idx == nil {
		return out, fmt.Errorf("原文检索未启动")
	}
	for _, item := range c.items {
		if source != "" && item.source.ID != source {
			continue
		}
		v, err := item.library.Effective()
		cov := Coverage{SourceID: item.source.ID, Name: item.source.Name, Error: idx.errorText()}
		if err != nil {
			if !errors.Is(err, ErrNotReady) && !errors.Is(err, ErrSourceBusy) {
				cov.Error = "当前文件无法读取"
			}
			out.Coverage = append(out.Coverage, cov)
			continue
		}
		cov.Files = len(v.Files)
		type record struct {
			token, hash, body, reason string
			matched                   bool
		}
		records := map[string]record{}
		if idx.db != nil {
			rows, err := idx.db.QueryContext(ctx, `SELECT path,token,fingerprint,CASE WHEN ?='' THEN '' ELSE substr(body,max(1,instr(folded,?)-60),240) END,reason,instr(folded,?)>0 FROM files WHERE source=?`, q, q, q, item.source.ID)
			if err != nil {
				return out, err
			}
			for rows.Next() {
				var p string
				var r record
				if err = rows.Scan(&p, &r.token, &r.hash, &r.body, &r.reason, &r.matched); err != nil {
					rows.Close()
					return out, err
				}
				records[p] = r
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return out, err
			}
		}
		item.library.mu.RLock()
		v, err = item.library.effectiveLocked()
		if err != nil {
			item.library.mu.RUnlock()
			continue
		}
		cov.Files = len(v.Files)
		var fileHits []SearchHit
		for _, e := range v.Files {
			r, ok := records[e.Path]
			current := ok && r.token == e.Token
			if current {
				cov.Indexed++
				if r.reason != "" {
					cov.Unsearchable++
				}
			} else {
				cov.Pending++
			}
			if q == "" || (!strings.Contains(strings.ToLower(e.Path), q) && !(current && r.matched)) {
				continue
			}
			snippet, reason := r.body, r.reason
			if !current {
				r.hash = ""
				snippet = ""
				reason = "正文待建立检索"
			}
			match, score := "原文", 10
			if strings.Contains(strings.ToLower(e.Path), q) {
				match, score = "路径", 20
			}
			fileHits = append(fileHits, SearchHit{SourceID: item.source.ID, Instance: item.source.Instance, Source: item.source.Name, Path: e.Path, Token: e.Token, Fingerprint: r.hash, Commit: v.Commit, Revision: v.Revision, Conflict: e.Conflict, Snippet: snippet, Reason: reason, Match: match, Score: score})
		}
		out.Hits = append(out.Hits, fileHits...)
		item.library.mu.RUnlock()
		out.Coverage = append(out.Coverage, cov)
	}
	sort.SliceStable(out.Hits, func(i, j int) bool {
		a, b := out.Hits[i], out.Hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.Path < b.Path
	})
	out.Total = len(out.Hits)
	last := (out.Total + out.PageSize - 1) / out.PageSize
	if last < 1 {
		last = 1
	}
	if out.Page > last {
		out.Page = last
	}
	start := (out.Page - 1) * out.PageSize
	end := start + out.PageSize
	if end > out.Total {
		end = out.Total
	}
	out.Hits = out.Hits[start:end]
	return out, nil
}

func (i *contentIndex) setError(s string) { i.mu.Lock(); defer i.mu.Unlock(); i.err = s }
func (i *contentIndex) errorText() string { i.mu.Lock(); defer i.mu.Unlock(); return i.err }
