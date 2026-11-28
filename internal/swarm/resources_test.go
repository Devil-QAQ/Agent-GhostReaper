package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/capability"
	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

func resourceFixture(t *testing.T, root string) (*capability.Catalog, string) {
	t.Helper()
	c, e := capability.NewCatalog(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for p, body := range map[string]string{"root/odd/helper.sh": "printf 'RESOURCE_FILE_USED\\n'\n", "root/odd/notes.md": "CVE-2099-12345 示例参考材料，不是目标证据。"} {
		f, _ := w.CreateFormFile("files", p)
		f.Write([]byte(body))
	}
	w.Close()
	if e = c.ImportFolder(t.Context(), "", "自主资料样本", multipart.NewReader(&b, w.Boundary())); e != nil {
		t.Fatal(e)
	}
	return c, c.List()[0].ID
}

func resourceTask(t *testing.T, s *rings.Store, id, intent string) {
	t.Helper()
	_, e := s.Append(t.Context(), id, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: intent}})
	if e != nil {
		t.Fatal(e)
	}
	seedGraph(t, s, id, intent)
}
func TestResourceToolsLifecycle(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	resourceTask(t, s, "resources", "本地资料核对")
	c, sel := resourceFixture(t, filepath.Join(dir, "library"))
	ex := NewExecutor(s, nil, dir, Budget{})
	ex.Capabilities = c
	env := &unitEnv{Executor: ex, TaskID: "resources", StepID: "s1", UnitID: "u1"}
	for _, retest := range []bool{false, true} {
		for _, entry := range ToolCatalogFor(RoE{}, retest) {
			if entry.Name == "resource_take" {
				t.Fatal("retired copy tool remains in catalog")
			}
		}
	}
	read := env.resourceReadTool()
	if !read.IsReadOnly(nil) {
		t.Fatal("动作性质错误")
	}
	call := func(a any) string {
		b, _ := json.Marshal(a)
		r := callTool(t, read, string(b))
		if r.IsError {
			t.Fatal(r.Flatten())
		}
		return r.Flatten()
	}
	call(map[string]any{"action": "discover"})
	browse := call(map[string]any{"action": "browse", "source_id": sel, "path": "odd"})
	if !strings.Contains(browse, "helper.sh") {
		t.Fatal(browse)
	}
	l, _ := c.Get(sel)
	v, _ := l.Effective()
	token := ""
	for _, f := range v.Files {
		if f.Path == "odd/helper.sh" {
			token = f.Token
		}
	}
	call(map[string]any{"action": "read", "source_id": sel, "path": "odd/helper.sh", "content_version": token})
	if e := c.Mutate(sel, capability.Mutation{Action: "save", Path: "odd/helper.sh", Text: "changed", Version: v.Commit, Revision: v.Revision}); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"action": "read", "source_id": sel, "path": "odd/helper.sh", "content_version": token})
	if r := callTool(t, read, string(raw)); !r.IsError {
		t.Fatal("旧版本未拒绝")
	}
	if e := c.Delete(sel); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(map[string]any{"action": "read", "source_id": sel, "path": "odd/helper.sh"})
	if r := callTool(t, read, string(raw)); !r.IsError {
		t.Fatal("已删除来源返回原文")
	}
}

func TestResourceFormalSession(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	resourceTask(t, s, "resource-session", "读取本地资料并自主完成核对")
	c, sel := resourceFixture(t, filepath.Join(dir, "library"))
	indexCtx, stopIndex := context.WithCancel(t.Context())
	indexDone := make(chan struct{})
	go func() { defer close(indexDone); c.RunIndex(indexCtx) }()
	defer func() { stopIndex(); <-indexDone }()
	waitFor(t, 3*time.Second, "正文索引可查", func() bool {
		result, err := c.Search(t.Context(), "CVE-2099-12345", "", 1)
		return err == nil && result.Total == 1
	})

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		name, args := "", ""
		switch {
		case !seenToolResult(rr, "resource_read", "sources"):
			name, args = "resource_read", `{"action":"discover"}`
		case !seenToolResult(rr, "resource_read", "coverage"):
			name, args = "resource_read", `{"action":"search","q":"CVE-2099-12345"}`
		case !seenToolResult(rr, "resource_read", "entries"):
			name, args = "resource_read", `{"action":"browse","source_id":"`+sel+`","path":"odd"}`
		case !seenToolResult(rr, "resource_read", "RESOURCE_FILE_USED"):
			name, args = "resource_read", `{"action":"read","source_id":"`+sel+`","path":"odd/helper.sh"}`
		case !seenToolResult(rr, "bash", "RESOURCE_FILE_USED"):
			name, args = "bash", `{"command":"printf 'RESOURCE_FILE_USED\\n'"}`
		default:
			return scriptedReply{Text: "已读取资料并通过现有 bash 完成核对，参考材料不证明目标存在漏洞。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{ID: shortID("c"), Name: name, Args: args}}, Finish: "tool_calls"}
	})
	defer model.Close()
	p, e := model.provider()
	if e != nil {
		t.Fatal(e)
	}
	ex := NewExecutor(s, p, dir, Budget{MaxTurns: 12})
	ex.Capabilities = c
	ex.Mirror = mirror.New(s, ex.Blobs, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	r, e := ex.RunStep(ctx, "resource-session", "s1")
	if e != nil || r.Reason != "completed" {
		t.Fatal(r, e)
	}
	calls := map[string]int{}
	for _, r := range swarmRecords(t, s, "resource-session") {
		if r.Type == rings.SwarmRecToolCall && !r.IsError {
			calls[r.ToolName]++
		}
	}
	if calls["resource_read"] != 4 || calls["bash"] != 1 {
		t.Fatal(calls)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, r := range model.reviews {
		if strings.Contains(r.LastUser, `"tool":"resource_read"`) {
			t.Fatal("本地查询进入目标动作研判")
		}
	}
}

func TestResourceResumeUsesCurrentVersions(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	resourceTask(t, s, "resource-resume", "按需读取资料并完成本地核对")
	c, old := resourceFixture(t, filepath.Join(dir, "library"))
	l, _ := c.Get(old)
	oldView, _ := l.Effective()
	var oldToken string
	for _, f := range oldView.Files {
		if f.Path == "odd/helper.sh" {
			oldToken = f.Token
		}
	}
	gate := ckpt.New()
	stage := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		name, args := "", ""
		stage++
		switch stage {
		case 1:
			name, args = "resource_read", `{"action":"read","source_id":"`+old+`","path":"odd/helper.sh","content_version":"`+oldToken+`"}`
		case 2:
			gate.Arm("resource-resume")
			name, args = "bash", `{"command":"echo deferred"}`
		case 3:
			name, args = "resource_read", `{"action":"read","source_id":"`+old+`","path":"odd/helper.sh","content_version":"`+oldToken+`"}`
		case 4:
			name, args = "resource_read", `{"action":"read","source_id":"`+old+`","path":"odd/helper.sh"}`
		case 5:
			name, args = "bash", `{"command":"printf 'RESOURCE_RESUMED\\n'"}`
		default:
			return scriptedReply{Text: "续接后重新定位并使用当前资料。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{ID: shortID("r"), Name: name, Args: args}}, Finish: "tool_calls"}
	})
	defer model.Close()
	p, e := model.provider()
	if e != nil {
		t.Fatal(e)
	}
	first := NewExecutor(s, p, dir, Budget{})
	first.Capabilities = c
	first.Ckpt = gate
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r1, e := first.RunStep(ctx, "resource-resume", "s1")
	if e != nil || r1.Reason != "checkpointed" {
		t.Fatal(r1, e)
	}
	if e = c.Mutate(old, capability.Mutation{Action: "save", Path: "odd/helper.sh", Text: "printf 'RESOURCE_RESUMED\\n'\n", Version: oldView.Commit, Revision: oldView.Revision}); e != nil {
		t.Fatal(e)
	}
	second := NewExecutor(s, p, dir, Budget{})
	second.Capabilities = c
	r2, e := second.RunStep(ctx, "resource-resume", "s1")
	if e != nil || r2.Reason != "completed" || r2.TranscriptID != r1.TranscriptID {
		t.Fatal(r2, e)
	}
	rejected, used := false, false
	for _, r := range swarmRecords(t, s, "resource-resume") {
		rejected = rejected || (r.ToolName == "resource_read" && r.IsError)
		used = used || (r.ToolName == "bash" && strings.Contains(r.ResultHead, "RESOURCE_RESUMED"))
	}
	if !rejected || !used {
		t.Fatal("续接未拒绝旧版本或未使用当前原文", rejected, used)
	}
}

func TestResourceIndexFailureKeepsRawAccess(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	resourceTask(t, s, "index-failure", "资料查询故障回退")
	c, sel := resourceFixture(t, filepath.Join(dir, "library"))
	ex := NewExecutor(s, nil, dir, Budget{})
	ex.Capabilities = c
	env := &unitEnv{Executor: ex, TaskID: "index-failure", StepID: "s1", UnitID: "u1"}
	c.Close()
	r := callTool(t, env.resourceReadTool(), `{"action":"search","q":"CVE-2099-12345"}`)
	if !r.IsError {
		t.Fatal("索引故障冒充无命中")
	}
	for _, action := range []string{"browse", "read"} {
		a := map[string]any{"action": action, "source_id": sel, "path": "odd"}
		if action == "read" {
			a["path"] = "odd/helper.sh"
		}
		b, _ := json.Marshal(a)
		if r = callTool(t, env.resourceReadTool(), string(b)); r.IsError {
			t.Fatal(action, r.Flatten())
		}
	}
	if len(filterTools(env.toolset(), RoE{AllowedTools: []string{"resource_read"}, DeniedTools: []string{"resource_read"}})) != 0 {
		t.Fatal("资料工具绕过授权过滤")
	}
	entries := ToolCatalogFor(RoE{AllowedTools: []string{"resource_read"}}, true)
	if len(entries) != 2 || entries[1].Name != "submit_retest_result" {
		t.Fatal("复测目录与实际受限集不符", entries)
	}
}
