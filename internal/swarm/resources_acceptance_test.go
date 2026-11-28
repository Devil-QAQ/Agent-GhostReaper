package swarm

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/capability"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/testmodel"
)

// Opt-in acceptance uses a real worker model, a private HTTP
// target, and the production result entry. Credentials stay in memory only.
func TestResourceLiveImportedWorkflow(t *testing.T) {
	cfg := testmodel.Config(t)
	if binary := os.Getenv("GHOSTREAPER_ACCEPTANCE_BINARY"); binary != "" {
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{cfg.APIKey, "E_ACCEPTED", "RESOURCE_FILE_USED", "GhostFixture 参考"} {
			if forbidden != "" && bytes.Contains(data, []byte(forbidden)) {
				t.Fatal("acceptance data or credential found in release binary")
			}
		}
	}
	dir := os.Getenv("GHOSTREAPER_WORKFLOW_EVIDENCE")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	c, err := capability.NewCatalog(filepath.Join(dir, "capability-library"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for name, body := range map[string]string{
		"root/README.md": "GhostFixture 是隔离授权样例。健康接口 /health 返回 status=ready 和 check=E_ACCEPTED 时表示本轮健康核对通过，并不证明漏洞。check.sh 接受服务根地址参数并读取该接口。只解释实际响应，不把本说明当成目标事实。",
		"root/check.sh":  "#!/bin/sh\ncurl --fail --silent --show-error \"$1/health\"\n",
	} {
		f, e := w.CreateFormFile("files", name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.ImportFolder(t.Context(), "", "GhostFixture 参考", multipart.NewReader(&b, w.Boundary())); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	indexDone := make(chan struct{})
	go func() { defer close(indexDone); c.RunIndex(ctx) }()
	defer func() { cancel(); <-indexDone }()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ready","check":"E_ACCEPTED"}`)
	}))
	defer target.Close()
	intent := "查询并阅读本地 GhostFixture 参考资料，理解其中的核对方法，再自主核对授权隔离样例 GhostFixture 服务 " + target.URL + " 的健康接口，并按该服务资料解释实际响应；本地资料可按需参考。只访问这个样例，不访问其他地址，不探测漏洞。实际观察经正常结果入口写回，完成即停。方法由你决定。"
	_, err = s.Append(ctx, "e-workflow",
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "GhostFixture 健康核对"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "g1", NodeType: rings.NodeGoal, Title: "核对授权样例健康", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "target", NodeType: rings.NodeAsset, Title: target.URL, AssetClass: rings.AssetService, Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: intent, Status: "dispatched", Detail: map[string]any{"goal_id": "g1", "expected_observation": "实际脚本响应包含 status=ready 和 check=E_ACCEPTED；普通观察写回，不提交漏洞", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "target"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	p := testmodel.Provider(t)
	ex := NewExecutor(s, p, filepath.Join(dir, "tasks"), Budget{MaxTurns: 24})
	ex.Capabilities = c
	ex.Mirror = mirror.New(s, ex.Blobs, filepath.Join(dir, "tasks"))
	result, err := ex.RunStep(ctx, "e-workflow", "s1")
	if err != nil || result.Reason != "completed" {
		t.Fatalf("workflow incomplete: %s %v", result.Reason, err)
	}
	calls := map[string]int{}
	observed := false
	readOriginal := false
	for _, r := range swarmRecords(t, s, "e-workflow") {
		if r.Type == rings.SwarmRecToolCall && !r.IsError {
			calls[r.ToolName]++
			if r.ToolName == "resource_read" && strings.Contains(string(r.ToolArgs), `"read"`) {
				readOriginal = true
			}
			if r.ToolName == "bash" && strings.Contains(r.ResultHead, "E_ACCEPTED") {
				observed = true
			}
		}
	}
	if !readOriginal || !observed || calls["submit_candidate_fact"] == 0 {
		t.Fatalf("workflow missing actual resource use/observation/writeback: %v observed=%v", calls, observed)
	}
	facts, err := s.NodesByType(ctx, "e-workflow", rings.NodeFact, "")
	if err != nil || len(facts) == 0 {
		t.Fatal("no written observation", err)
	}
	for _, f := range facts {
		if !strings.Contains(fmt.Sprint(f.Detail), "E_ACCEPTED") {
			t.Fatal("result lacks actual target marker")
		}
	}
	t.Logf("real workflow completed unit=%s calls=%v facts=%d", result.UnitID, calls, len(facts))
}
