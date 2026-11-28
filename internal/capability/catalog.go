package capability

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const DefaultSourceID = "vulnerability-wiki-poc"

var ErrSourceMissing = errors.New("仓库不存在")
var ErrSourceBusy = errors.New("仓库正在处理文件，完成后再修改")
var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Source struct {
	Instance string `json:"instance,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

type SourceStatus struct {
	Source
	Status
}
type catalogItem struct {
	source  Source
	library *Library
}

// Catalog owns configuration; each source has an independent library.
type Catalog struct {
	index        *contentIndex
	root         string
	mu           sync.RWMutex
	items        []catalogItem
	importing    map[string]bool
	cleanupSlots chan struct{}
}

func NewCatalog(root string) (*Catalog, error) {
	c := &Catalog{root: root, importing: make(map[string]bool), cleanupSlots: make(chan struct{}, 1)}
	data, err := os.ReadFile(filepath.Join(root, "sources.json"))
	var sources []Source
	if errors.Is(err, os.ErrNotExist) {
		sources = []Source{}
		if _, err := os.Stat(filepath.Join(root, "current.json")); err == nil {
			sources = append(sources, Source{ID: DefaultSourceID, Name: "Vulnerability-Wiki-PoC", Kind: "github", URL: SourceURL})
		}
		if err := c.persist(sources); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, errors.New("仓库配置无法读取")
	} else if json.Unmarshal(data, &sources) != nil {
		return nil, errors.New("仓库配置格式无效")
	}
	instanceChanged := false
	seen := map[string]bool{}
	for _, raw := range sources {
		s, repo, err := normalizeSource(raw)
		if err != nil || s.ID == "" || seen[s.ID] {
			return nil, errors.New("仓库配置无效")
		}
		if s.Instance == "" {
			s.Instance, err = newSourceID()
			if err != nil {
				return nil, err
			}
			instanceChanged = true
		}
		seen[s.ID] = true
		dir := c.libraryDir(s, repo)
		deletionDir, deleting, err := c.readDeletion(s, repo)
		if err != nil {
			return nil, err
		}
		if deleting {
			dir = deletionDir
		}
		l := newLibrary(dir, repo, s.Branch)
		l.deletingRepository = deleting
		c.items = append(c.items, catalogItem{s, l})
	}
	if instanceChanged {
		if err := c.persist(itemsSources(c.items)); err != nil {
			return nil, err
		}
	}
	c.openIndex()
	for _, item := range c.items {
		if item.library.deletingRepository {
			c.startRepositoryCleanup(item)
		} else if item.library.local.CleanupPending && item.library.localErr == nil {
			c.startFileCleanup(item.library)
		}
	}
	return c, nil
}

func normalizeSource(s Source) (Source, string, error) {
	s.Name, s.URL, s.Branch = strings.TrimSpace(s.Name), strings.TrimSpace(s.URL), strings.TrimSpace(s.Branch)
	if s.Name == "" || utf8.RuneCountInString(s.Name) > 80 || strings.IndexFunc(s.Name, unicode.IsControl) >= 0 {
		return s, "", errors.New("名称须为 1 至 80 个字符")
	}
	if s.ID != "" && !repositoryPart.MatchString(s.ID) {
		return s, "", errors.New("仓库编号无效")
	}
	if s.Kind == "folder" {
		s.URL, s.Branch = "", ""
		return s, "", nil
	}
	if s.Kind != "github" {
		return s, "", errors.New("请选择 GitHub 链接或文件夹")
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return s, "", errors.New("填写 GitHub 仓库的 HTTPS 链接")
	}
	repo := strings.TrimSuffix(strings.Trim(strings.TrimSpace(u.Path), "/"), ".git")
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return s, "", errors.New("链接须指向仓库，不包含文件或目录")
	}
	for _, p := range parts {
		if !repositoryPart.MatchString(p) || p == "." || p == ".." {
			return s, "", errors.New("仓库链接无效")
		}
	}
	if len(s.Branch) > 200 || strings.IndexFunc(s.Branch, unicode.IsControl) >= 0 {
		return s, "", errors.New("分支名称无效")
	}
	s.URL = "https://github.com/" + repo
	return s, repo, nil
}

func (c *Catalog) libraryDir(s Source, repo string) string {
	if s.Kind == "folder" {
		return filepath.Join(c.root, "folders", s.ID)
	}
	// Keep the original single-source download in place during migration.
	if strings.EqualFold(repo, Repository) && s.Branch == "" {
		if _, err := os.Stat(filepath.Join(c.root, "current.json")); err == nil {
			return c.root
		}
	}
	key := sha256.Sum256([]byte(strings.ToLower(repo) + "\n" + s.Branch))
	return filepath.Join(c.root, "repositories", hex.EncodeToString(key[:]))
}

func (c *Catalog) List() []SourceStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]SourceStatus, 0, len(c.items))
	for _, i := range c.items {
		out = append(out, c.sourceStatus(i))
	}
	return out
}

func (c *Catalog) sourceStatus(i catalogItem) SourceStatus {
	s := i.library.Status()
	return SourceStatus{i.source, s}
}

func (c *Catalog) Status(id string) (SourceStatus, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, i := range c.items {
		if i.source.ID == id {
			return c.sourceStatus(i), nil
		}
	}
	return SourceStatus{}, ErrSourceMissing
}

func (c *Catalog) Get(id string) (*Library, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, i := range c.items {
		if i.source.ID == id {
			return i.library, nil
		}
	}
	return nil, ErrSourceMissing
}

func (c *Catalog) StartUpdate(id string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, i := range c.items {
		if i.source.ID == id {
			if c.importing[id] || i.library.Status().CleanupPending {
				return false, ErrSourceBusy
			}
			if i.source.Kind != "github" {
				return false, errors.New("文件夹来源请重新导入")
			}
			return i.library.StartUpdate(), nil
		}
	}
	return false, ErrSourceMissing
}

func newSourceID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("仓库编号生成失败")
	}
	return hex.EncodeToString(b[:]), nil
}

func (c *Catalog) Save(s Source) error {
	s, repo, err := normalizeSource(s)
	if err != nil {
		return err
	}
	if s.Kind != "github" {
		return errors.New("文件夹来源请使用导入入口")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	index := -1
	for n, i := range c.items {
		if i.source.ID == s.ID {
			index = n
		}
		if i.source.ID != s.ID && i.source.Kind == s.Kind && strings.EqualFold(i.source.URL, s.URL) && i.source.Branch == s.Branch {
			return errors.New("该仓库和分支已添加")
		}
	}
	if s.ID != "" && index < 0 {
		return ErrSourceMissing
	}
	if index >= 0 && (c.items[index].library.Status().Updating || c.items[index].library.Status().CleanupPending || c.importing[s.ID]) {
		return ErrSourceBusy
	}
	if index >= 0 && c.items[index].source.Kind != "github" {
		return errors.New("文件夹来源请使用导入入口")
	}
	if s.ID == "" {
		s.ID, err = newSourceID()
		if err != nil {
			return err
		}
	}
	next := append([]catalogItem(nil), c.items...)
	dir := c.libraryDir(s, repo)
	var l *Library
	if index >= 0 && c.items[index].library.root == dir {
		l = c.items[index].library
	} else {
		l = newLibrary(dir, repo, s.Branch)
	}
	if index >= 0 && c.items[index].library == l {
		s.Instance = c.items[index].source.Instance
	} else {
		s.Instance, err = newSourceID()
		if err != nil {
			return err
		}
	}
	if index >= 0 {
		next[index] = catalogItem{s, l}
	} else {
		next = append(next, catalogItem{s, l})
	}
	if err := c.persist(itemsSources(next)); err != nil {
		return err
	}
	c.items = next
	return nil
}

func (c *Catalog) RenameFolder(id, name string) error {
	s, _, err := normalizeSource(Source{ID: id, Name: name, Kind: "folder"})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := append([]catalogItem(nil), c.items...)
	for n, i := range next {
		if i.source.ID == id {
			if c.importing[id] || i.library.Status().CleanupPending {
				return ErrSourceBusy
			}
			if i.source.Kind != "folder" {
				return errors.New("来源类型不匹配")
			}
			s.Instance = i.source.Instance
			next[n].source = s
			if err := c.persist(itemsSources(next)); err != nil {
				return err
			}
			c.items = next
			return nil
		}
	}
	return ErrSourceMissing
}

func (c *Catalog) Delete(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, i := range c.items {
		if i.source.ID == id {
			if i.library.Status().Updating || c.importing[id] {
				return ErrSourceBusy
			}
			rel, err := filepath.Rel(c.root, i.library.root)
			if err != nil {
				return ErrPath
			}
			if err := writeRecord(c.deletionRecord(id), repositoryDeletion{Directory: filepath.ToSlash(rel)}); err != nil {
				return errors.New("删除记录无法保存，未开始删除")
			}
			c.startRepositoryCleanup(i)
			return nil
		}
	}
	return ErrSourceMissing
}

func itemsSources(items []catalogItem) []Source {
	out := make([]Source, 0, len(items))
	for _, i := range items {
		out = append(out, i.source)
	}
	return out
}

func (c *Catalog) persist(sources []Source) error {
	if err := os.MkdirAll(c.root, 0755); err != nil {
		return errors.New("仓库配置目录无法写入")
	}
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.root, ".sources-*.json")
	if err != nil {
		return errors.New("仓库配置无法保存")
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("仓库配置写入失败")
	}
	if err := os.Rename(f.Name(), filepath.Join(c.root, "sources.json")); err != nil {
		return errors.New("仓库配置切换失败")
	}
	return nil
}
