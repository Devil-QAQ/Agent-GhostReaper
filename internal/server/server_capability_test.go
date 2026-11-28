package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/capability"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/swarm"
)

func TestCapabilityLibraryAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(store, mgr, swarm.NewLedgerGate(store))
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/capability-library", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status %d", rr.Code)
	}
	root := filepath.Join(dir, "library")
	commit := strings.Repeat("a", 40)
	files := filepath.Join(root, "versions", commit, "中间件")
	if err := os.MkdirAll(files, 0755); err != nil {
		t.Fatal(err)
	}
	text := "# PoC 中文\n<script>alert('text only')</script>\n"
	if err := os.WriteFile(filepath.Join(files, "示例.md"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(capability.Version{Commit: commit})
	if err := os.WriteFile(filepath.Join(root, "current.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	srv.Capabilities, err = capability.NewCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/capability-library/repositories/" + capability.DefaultSourceID
	var status capability.Status
	get(t, h, base, http.StatusOK, &status)
	if !status.Ready || status.Version.Files != 1 {
		t.Fatalf("status %+v", status)
	}
	var list capability.Listing
	get(t, h, base+"/files?q="+url.QueryEscape("示例"), http.StatusOK, &list)
	if list.Total != 1 || list.Commit != commit {
		t.Fatalf("listing %+v", list)
	}
	fileURL := base + "/file?path=" + url.QueryEscape("中间件/示例.md") + "&version=" + commit
	var preview capability.Preview
	get(t, h, fileURL, http.StatusOK, &preview)
	if preview.Text != text {
		t.Fatalf("preview %+v", preview)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, fileURL+"&download=1", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != text || !strings.HasPrefix(rr.Header().Get("Content-Disposition"), "attachment;") || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v", rr.Code, rr.Header())
	}
	get(t, h, base+"/file?path=..%2Fcurrent.json", http.StatusBadRequest, nil)
	get(t, h, base+"/file?path=missing", http.StatusNotFound, nil)
	v, err := func() (capability.EffectiveView, error) {
		l, e := srv.Capabilities.Get(capability.DefaultSourceID)
		if e != nil {
			return capability.EffectiveView{}, e
		}
		return l.Effective()
	}()
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	contentURL := base + "/content?path=" + url.QueryEscape("中间件/示例.md") + "&content_version=" + url.QueryEscape(v.Files[0].Token)
	get(t, h, contentURL, http.StatusOK, &content)
	if content["text"] != text || content["complete"] != true {
		t.Fatal(content)
	}
	get(t, h, contentURL+"&offset=6&limit=1", http.StatusOK, &content)
	if content["text"] != "中" || content["next_offset"] != float64(9) || content["binary"] != false {
		t.Fatal("small UTF-8 range did not advance intact", content)
	}
	get(t, h, contentURL+"&offset=-1", http.StatusBadRequest, nil)
	get(t, h, base+"/content?path="+url.QueryEscape("中间件/示例.md")+"&content_version=old", http.StatusConflict, nil)
	raw, _ := json.Marshal(capability.Mutation{Path: "中间件/示例.md", Action: "save", Text: "local text", Version: commit, Revision: 0})
	post(t, h, http.MethodPost, base+"/file", string(raw), http.StatusOK, &status)
	get(t, h, fileURL, http.StatusOK, &preview)
	if preview.Text != "local text" {
		t.Fatal("API edit not applied")
	}
	post(t, h, http.MethodPost, base+"/file", string(raw), http.StatusConflict, nil)
	raw, _ = json.Marshal(capability.Mutation{Path: "中间件", Action: "delete", Version: commit, Revision: 1})
	post(t, h, http.MethodPost, base+"/file", string(raw), http.StatusAccepted, &status)
	get(t, h, fileURL, http.StatusNotFound, nil)
	var changes capability.ChangesView
	get(t, h, base+"/changes", http.StatusOK, &changes)
	if changes.Revision != 2 || len(changes.Changes) != 1 || changes.Changes[0].DeletedAt.IsZero() {
		t.Fatal("change list incomplete")
	}
	status = waitCapabilityIdle(t, h, base)
	if _, err := os.Stat(filepath.Join(files, "示例.md")); !os.IsNotExist(err) {
		t.Fatal("API deletion did not remove disk contents")
	}
	raw, _ = json.Marshal(capability.Mutation{Path: "中间件", Action: "restore", Version: commit, Revision: 2})
	post(t, h, http.MethodPost, base+"/file", string(raw), http.StatusBadRequest, nil)
	var catalog struct {
		Repositories []capability.SourceStatus `json:"repositories"`
	}
	post(t, h, http.MethodPost, "/api/capability-library/repositories", `{"name":"Nuclei","kind":"github","url":"https://github.com/projectdiscovery/nuclei-templates"}`, http.StatusOK, &catalog)
	if len(catalog.Repositories) != 2 {
		t.Fatalf("add %+v", catalog)
	}
	id := catalog.Repositories[1].ID
	post(t, h, http.MethodPost, "/api/capability-library/repositories/"+id, `{"name":"模板","kind":"github","url":"https://github.com/projectdiscovery/nuclei-templates","branch":"main"}`, http.StatusOK, &catalog)
	if catalog.Repositories[1].Name != "模板" {
		t.Fatal("edit failed")
	}
	var upload bytes.Buffer
	mw := multipart.NewWriter(&upload)
	f, err := mw.CreateFormFile("files", "本地资料/测试.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("id: local-sample"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/capability-library/import?name="+url.QueryEscape("本地资料"), &upload)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("import %d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Repositories) != 3 || catalog.Repositories[2].Kind != "folder" {
		t.Fatalf("import response %+v", catalog)
	}
	folderBase := "/api/capability-library/repositories/" + catalog.Repositories[2].ID
	deadline := time.Now().Add(5 * time.Second)
	for {
		get(t, h, folderBase, http.StatusOK, &status)
		if !status.Updating {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background import did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if !status.Ready || status.Error != "" || status.Version.Files != 1 {
		t.Fatalf("completed import %+v", status)
	}
	get(t, h, folderBase+"/file?path="+url.QueryEscape("测试.yaml"), http.StatusOK, &preview)
	if preview.Text != "id: local-sample" {
		t.Fatalf("imported preview %+v", preview)
	}
	post(t, h, http.MethodDelete, "/api/capability-library/repositories/"+id, `{}`, http.StatusAccepted, &catalog)
	deadline = time.Now().Add(5 * time.Second)
	for {
		get(t, h, "/api/capability-library", http.StatusOK, &catalog)
		if len(catalog.Repositories) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("repository deletion did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if len(catalog.Repositories) != 2 {
		t.Fatal("delete failed")
	}
	get(t, h, "/api/capability-library/repositories/"+id, http.StatusNotFound, nil)
}

func waitCapabilityIdle(t *testing.T, h http.Handler, base string) capability.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var s capability.Status
		get(t, h, base, http.StatusOK, &s)
		if !s.Updating {
			if s.Error != "" {
				t.Fatal(s.Error)
			}
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("capability operation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRetiredCapabilityOrganizingEndpoints(t *testing.T) {
	store := openSStore(t, t.TempDir())
	mgr, err := manager.New(manager.Config{Store: store, RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(store, mgr, swarm.NewLedgerGate(store))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/capability-library/take", "/api/capability-library/organizing", "/api/capability-library/organizing/check", "/api/capability-library/organizing/control", "/api/capability-library/repositories/sample/organizing", "/api/capability-library/repositories/sample/detail"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, httptest.NewRequest(method, endpoint, strings.NewReader(`{}`)))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("retired endpoint %s %s: %d", method, endpoint, rr.Code)
			}
		}
	}
}
