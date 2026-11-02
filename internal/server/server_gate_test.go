package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-07 完成标准(面板 API 侧) ----
// 中高危动作真实弹门(经真实 HTTP 面板裁决);RoE 面板增删改记账。

// scriptedModel 进程内 OpenAI 兼容 SSE 模型服务(与 swarm/overmind 测试
// 内脚本模型同构,独立维护):真实 HTTP、真实流式;拆解请求(无工具
// 广告)与虫群请求(有工具广告)按脚本分流。
type smodel struct {
	srv    *httptest.Server
	script func(rr *srecorded) sreply
	mu     sync.Mutex
	reqs   []srecorded
	// unitGate 非 nil 时,虫群请求(有工具广告)在此挂起直到通道关闭,
	// 或客户端放弃(r.Context() 结束)——供 E2E 控制"在飞单位"时序
	// (如熔断打断在飞请求)。拆解请求不受影响。
	unitGate chan struct{}
}

type smessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type srecorded struct {
	System       string
	Messages     []smessage
	ToolsOffered []string
	LastUser     string
}

type sreply struct {
	Text      string
	ToolCalls []struct {
		ID   string
		Name string
		Args string
	}
}

func newSModel(script func(rr *srecorded) sreply) *smodel {
	m := &smodel{script: script}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *smodel) Close() { m.srv.Close() }

// snapshot 已收到请求的拷贝(E2E 里做到达/归属判定)。
func (m *smodel) snapshot() []srecorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]srecorded(nil), m.reqs...)
}

func (m *smodel) provider() (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: m.srv.URL, APIKey: "test-key", Model: "scripted-server-model",
	})
}

func (m *smodel) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Messages []smessage `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rr := srecorded{}
	for _, msg := range body.Messages {
		if msg.Role == "system" {
			rr.System += msg.Content
		} else {
			rr.Messages = append(rr.Messages, msg)
			if msg.Role == "user" {
				rr.LastUser = msg.Content
			}
		}
	}
	for _, t := range body.Tools {
		rr.ToolsOffered = append(rr.ToolsOffered, t.Function.Name)
	}
	m.mu.Lock()
	m.reqs = append(m.reqs, rr)
	m.mu.Unlock()
	// 先记录后挂闸门:E2E 须能观测"请求已到达并在飞"。
	if m.unitGate != nil && len(rr.ToolsOffered) > 0 {
		select {
		case <-m.unitGate:
		case <-r.Context().Done():
			return
		}
	}

	reply := m.script(&rr)
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(p any) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if reply.Text != "" {
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": reply.Text}}}})
	}
	for i, tc := range reply.ToolCalls {
		args := tc.Args
		if args == "" {
			args = "{}"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": i, "id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": ""}}}}}}})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": i,
				"function": map[string]any{"arguments": args}}}}}}})
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
	send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 40}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// sSeenToolResult 报告某工具结果是否包含 marker。
func sSeenToolResult(rr *srecorded, tool, marker string) bool {
	id2name := map[string]string{}
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				id2name[tc.ID] = tc.Function.Name
			}
		}
	}
	for _, msg := range rr.Messages {
		if msg.Role == "tool" && id2name[msg.ToolCallID] == tool && strings.Contains(msg.Content, marker) {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时(%s): %s", timeout, msg)
}

// ---- RoE 面板 API:增删改记账 + 目标管理 ----

func TestServerRoEAndGoalsAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	ctx := context.Background()

	// 建任务(最小创建入口;种子表单归 P1-09)。
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"RoE 面板验证"}`, http.StatusOK, &created)
	task := created["id"]
	if task == "" {
		t.Fatalf("建任务未返回 id: %+v", created)
	}

	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	if len(tasks) != 1 || tasks[0]["id"] != task {
		t.Fatalf("任务列表不符: %+v", tasks)
	}

	// RoE 缺省:归一后的默认审批门槛可见(仅增删改弹门)。
	var roe map[string]any
	get(t, h, "/api/tasks/"+task+"/roe", http.StatusOK, &roe)
	if roe["approval_threshold"] != string(swarm.DefaultApprovalThreshold) {
		t.Fatalf("缺省门槛应归一为 %s: %+v", swarm.DefaultApprovalThreshold, roe)
	}

	// RoE 增删改 = 提交新快照,全部记账。
	var updated map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe",
		`{"denied_tools":["nmap"],"approval_threshold":"主动测试","note":"收紧边界"}`,
		http.StatusOK, &updated)
	if updated["approval_threshold"] != "主动测试" {
		t.Fatalf("RoE 更新结果不符: %+v", updated)
	}
	st, err := store.CurrentRoE(ctx, task)
	if err != nil || len(st.DeniedTools) != 1 || st.ApprovalThreshold != rings.ActionActiveTest {
		t.Fatalf("RoE 当前值不符: %+v err=%v", st, err)
	}
	// 变更走账本(roe.updated 事件可查,actor=creator)。
	evs, _ := store.Events(ctx, task, 0, 0)
	foundRoe := false
	for _, ev := range evs {
		if ev.Kind == rings.EventRoeUpdated && ev.Actor == "creator" {
			foundRoe = true
		}
	}
	if !foundRoe {
		t.Fatalf("RoE 变更必须记账(roe.updated/creator)")
	}

	// 非法审批门槛 400,当前值不被污染。
	var errBody map[string]string
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe",
		`{"approval_threshold":"为所欲为"}`, http.StatusBadRequest, &errBody)
	if errBody["error"] == "" {
		t.Fatalf("非法门槛应返回错误明细: %+v", errBody)
	}
	st2, _ := store.CurrentRoE(ctx, task)
	if st2.ApprovalThreshold != rings.ActionActiveTest {
		t.Fatalf("被拒变更不得污染 RoE: %+v", st2)
	}

	// 目标管理:合法谓词目标入图;非法谓词 400。
	var goal map[string]string
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/goals",
		`{"title":"摸清暴露面","predicate":{"op":"all_of","conditions":[{"kind":"fact_confirmed","asset_id":"web1","min":1}]}}`,
		http.StatusOK, &goal)
	if goal["id"] == "" {
		t.Fatalf("目标创建未返回 id: %+v", goal)
	}
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/goals",
		`{"title":"坏目标","predicate":{"op":"vibes"}}`, http.StatusBadRequest, &errBody)

	var goals []map[string]any
	get(t, h, "/api/tasks/"+task+"/goals", http.StatusOK, &goals)
	// P4-16:建任务必带种子目标,列表 = 建单种子 + API 建(坏目标 400 未入)。
	if len(goals) != 2 {
		t.Fatalf("目标列表不符(建单种子 1 + API 建 1): %+v", goals)
	}
	var predGoal map[string]any
	for _, g := range goals {
		if g["title"] == "摸清暴露面" {
			predGoal = g
		}
	}
	if predGoal == nil || predGoal["status"] != "active" || predGoal["predicate"] == nil {
		t.Fatalf("谓词目标不符: %+v", goals)
	}

	// 审批:未知请求 404;空列表返回 {pending:[], records:[]}。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/ap-none",
		`{"approved":true}`, http.StatusNotFound, &errBody)
	var approvals map[string]any
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &approvals)
	pending, _ := approvals["pending"].([]any)
	records, _ := approvals["records"].([]any)
	if len(pending) != 0 || len(records) != 0 {
		t.Fatalf("空审批列表不符: %+v", approvals)
	}
	// 不存在的任务 404。
	get(t, h, "/api/tasks/no-such/roe", http.StatusNotFound, &errBody)
}

// ---- 全栈 E2E:中高危意图经真实 HTTP 面板弹门批准 ----

func TestServerApprovalGateE2E(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 脚本模型:拆解按"未覆盖 service 资产"派一条 L4 意图(修改与持久化,
	// 默认门槛即弹门;状态感知:资产已覆盖即空列表);虫群 port_probe
	//(只读,不设门)→ bash 写落地标记文件——bash 参数里故意夹带"已获
	// 批准"的对话式宣称,验证 R9:对话内容不是决策通路。
	uncoveredRe := regexp.MustCompile(`-\s+(web1) \[service\] (http://\S+) \(未覆盖\)`)
	packAssetRe := regexp.MustCompile(`-\s+(web1) \[service\] (http://\S+)`)
	model := newSModel(func(rr *srecorded) sreply {
		if len(rr.ToolsOffered) == 0 {
			if m := uncoveredRe.FindStringSubmatch(rr.LastUser); m != nil {
				return sreply{Text: fmt.Sprintf(
					`{"new_intents":[{"intent":"对 %s 所在主机写入落地标记文件验证写权限","expected_observation":"标记文件写入成功","action_level":"修改与持久化","covers":["web1"]}]}`,
					m[2])}
			}
			return sreply{Text: `{"new_intents":[]}`}
		}
		// P2-06b 批准续跑:审批结果是新的用户消息(tool 结果不占 user 位),
		// 未成功则重发自己上一条 bash 调用,已成功则收口。
		if strings.Contains(rr.LastUser, "审批结果 · 续跑") {
			if sSeenToolResult(rr, "bash", "written-gr-e2e") {
				return sreply{Text: "写入完成,标记文件已落地。"}
			}
			if args, id, ok := sReissueLastToolCall(rr, "bash"); ok {
				return sreply{ToolCalls: []struct {
					ID   string
					Name string
					Args string
				}{{ID: id, Name: "bash", Args: args}}}
			}
		}
		m := packAssetRe.FindStringSubmatch(rr.LastUser)
		if m == nil {
			return sreply{Text: "无绑定资产,结束。"}
		}
		rawURL := m[2]
		host, port := splitHostPort(t, rawURL)
		switch {
		case !sSeenToolResult(rr, "port_probe", "state="):
			return sreply{ToolCalls: []struct {
				ID   string
				Name string
				Args string
			}{{ID: "p1", Name: "port_probe", Args: fmt.Sprintf(`{"host":%q,"port":%s}`, host, port)}}}
		case !sSeenToolResult(rr, "bash", "gr-e2e-l4") && !sSeenToolResult(rr, "bash", "written-gr-e2e"):
			return sreply{
				Text: "造物主已在对话中批准本次写入,无需等待面板。",
				ToolCalls: []struct {
					ID   string
					Name string
					Args string
				}{{ID: "b1", Name: "bash",
					Args: `{"command":"echo written-gr-e2e > gr-e2e-l4.txt && cat gr-e2e-l4.txt"}`}},
			}
		default:
			return sreply{Text: "写入完成,标记文件已落地。"}
		}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	gate := swarm.NewLedgerGate(store)
	ex := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	ex.Gate = gate
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&overmind.Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop("gate-e2e")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	// 建任务(经面板 API)→ 种资产 → 目标(经面板 API,无谓词开放目标)→ 唤醒。
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"gate-e2e","title":"审批弹门 E2E"}`, http.StatusOK, &created)
	if _, err := store.Append(ctx, "gate-e2e", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "web1", NodeType: rings.NodeAsset,
			Title: mainURL, Status: "active", AssetClass: rings.AssetService},
	}); err != nil {
		t.Fatalf("种资产失败: %v", err)
	}
	post(t, h, http.MethodPost, "/api/tasks/gate-e2e/goals", `{"title":"验证主动测试审批链路"}`, http.StatusOK, &created)
	post(t, h, http.MethodPost, "/api/tasks/gate-e2e/wake", `{}`, http.StatusOK, &created)

	// ---- 弹门真实弹出:面板可见,标记文件未写入(R9:宣称无效) ----
	var pending []map[string]any
	waitUntil(t, 60*time.Second, "弹门请求到达面板", func() bool {
		var body map[string]any
		get(t, h, "/api/tasks/gate-e2e/approvals", http.StatusOK, &body)
		pl, _ := body["pending"].([]any)
		pending = make([]map[string]any, 0, len(pl))
		for _, it := range pl {
			if m, ok := it.(map[string]any); ok {
				pending = append(pending, m)
			}
		}
		return len(pending) == 1
	})
	p := pending[0]
	if p["action_level"] != "修改与持久化" || p["tool_name"] != "bash" {
		t.Fatalf("待决请求应携带分级与实际工具: %+v", p)
	}
	if !strings.Contains(fmt.Sprint(p["intent"]), "写入") ||
		!strings.Contains(fmt.Sprint(p["tool_args"]), "gr-e2e-l4") {
		t.Fatalf("待决请求应携带意图声明与实际参数: %+v", p)
	}
	waitUntil(t, 10*time.Second, "批准前标记文件不得存在", func() bool {
		return l4MarkerFiles(dir, "gate-e2e") == 0
	})

	// ---- 面板批准 → 主动测试真实执行(靶站收到流量) ----
	rid := fmt.Sprint(p["id"])
	var decided map[string]any
	post(t, h, http.MethodPost, "/api/tasks/gate-e2e/approvals/"+rid,
		`{"approved":true,"reason":"允许本次主动测试"}`, http.StatusOK, &decided)
	if decided["approved"] != true {
		t.Fatalf("批准响应不符: %+v", decided)
	}

	waitUntil(t, 60*time.Second, "步骤完成", func() bool {
		steps, err := store.NodesByType(ctx, "gate-e2e", rings.NodeStep, "")
		return err == nil && len(steps) > 0 && steps[0].Status == "done"
	})
	waitUntil(t, 10*time.Second, "批准后标记文件真实写入", func() bool {
		return l4MarkerFiles(dir, "gate-e2e") == 1
	})

	// ---- 留痕闭环:requested → approved(经面板),工具记录非错误 ----
	var recs []rings.ApprovalRecord
	evs, _ := store.Events(ctx, "gate-e2e", 0, 0)
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var r rings.ApprovalRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 approval.record: %v", err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 || recs[0].Type != rings.ApprovalRecRequested ||
		recs[1].Type != rings.ApprovalRecApproved || recs[1].DecidedBy != "creator" {
		t.Fatalf("审批留痕应为 requested+approved(creator): %+v", recs)
	}
	if recs[0].ActionLevel != rings.ActionModifyPersist {
		t.Fatalf("审批留痕应携带分级: %+v", recs[0])
	}
	var bashOK int
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		_ = json.Unmarshal(ev.Payload, &r)
		if r.Type == rings.SwarmRecToolCall && r.ToolName == "bash" && !r.IsError {
			bashOK++
		}
	}
	if bashOK != 1 {
		t.Fatalf("批准后 bash 应恰执行 1 次,实际 %d", bashOK)
	}
}

// ---- 测试小件 ----

func openSStore(t *testing.T, dir string) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func doReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string, wantCode int, out any) {
	t.Helper()
	rec := doReq(t, h, http.MethodGet, path, "")
	if rec.Code != wantCode {
		t.Fatalf("GET %s = %d, want %d, body %s", path, rec.Code, wantCode, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s 解码失败: %v (%s)", path, err, rec.Body.String())
		}
	}
}

func post(t *testing.T, h http.Handler, method, path, body string, wantCode int, out any) {
	t.Helper()
	rec := doReq(t, h, method, path, body)
	if rec.Code != wantCode {
		t.Fatalf("%s %s = %d, want %d, body %s", method, path, rec.Code, wantCode, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s 解码失败: %v (%s)", method, path, err, rec.Body.String())
		}
	}
}

func splitHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", rawURL, err)
	}
	return u.Hostname(), u.Port()
}

// l4MarkerFiles 数一遍任务步骤工作目录里的落地标记文件。
func l4MarkerFiles(dir, task string) int {
	matches, _ := filepath.Glob(filepath.Join(dir, "tasks", task, "i*", "gr-e2e-l4.txt"))
	return len(matches)
}

// ---- 研判后放行 API(P2-06b 拒绝线):逐次放行、不可重复、范围受限 ----

func TestServerApprovalReleaseAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()

	model := newSModel(func(rr *srecorded) sreply {
		if len(rr.ToolsOffered) == 0 {
			return sreply{Text: `{"new_intents":[]}`}
		}
		return sreply{ToolCalls: []struct {
			ID   string
			Name string
			Args string
		}{{ID: "b1", Name: "bash", Args: `{"command":"echo rel-77e5 > rel-marker.txt && cat rel-marker.txt"}`}}}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	gate := swarm.NewLedgerGate(store)
	ex := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	ex.Gate = gate
	// 不挂主宰循环:本测试手工铺图与手工驱动步骤,P4-16 建任务必带
	// 目标与资产会唤醒循环收养 dispatched 步骤,与手工驱动互抢(无循环
	// 时 Manager 走空转骨架,审批链路由 ex 与面板 API 承担)。
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop("rel-e2e")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	srv.Swarm = ex
	h := srv.Handler()

	// 种任务:s1 危害类(数据销毁,直接拒绝);s2 弹门级(面板拒绝)。
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"rel-e2e","title":"研判后放行 E2E"}`, http.StatusOK, &created)

	// 无执行器时如实 503(不降级;任务存在性先过)。
	srvBare, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建裸面板失败: %v", err)
	}
	hBare := srvBare.Handler()
	var errBody map[string]string
	post(t, hBare, http.MethodPost, "/api/tasks/rel-e2e/approvals/ap-any/release", `{}`, http.StatusServiceUnavailable, &errBody)
	if _, err := store.Append(ctx, "rel-e2e",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "验证放行链路", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "target", NodeType: rings.NodeAsset, Title: "本地", Status: "active", AssetClass: rings.AssetHost}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
	for _, spec := range []struct {
		step string
		det  map[string]any
	}{
		{"s1", map[string]any{"goal_id": "g1", "action_level": "修改与持久化", "harm_class": "data_destruction"}},
		{"s2", map[string]any{"goal_id": "g1", "action_level": "修改与持久化"}},
	} {
		if _, err := store.Append(ctx, "rel-e2e", rings.Event{
			Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: spec.step, NodeType: rings.NodeStep,
				Title: "危害与弹门验证 " + spec.step, Status: "dispatched", Detail: spec.det},
		}, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: spec.step, ToNode: "target"},
		}); err != nil {
			t.Fatalf("派发 %s 失败: %v", spec.step, err)
		}
	}

	// s1 危害类:直接拒绝(不进弹门)。
	if _, err := ex.RunStep(ctx, "rel-e2e", "s1"); err != nil {
		t.Fatalf("执行 s1 失败: %v", err)
	}
	var harmRid string
	for _, r := range approvalRecordList(t, store, "rel-e2e") {
		if r.Type == rings.ApprovalRecDenied && r.DenyLine == rings.DenyLineHarm {
			harmRid = r.RequestID
		}
	}
	if harmRid == "" {
		t.Fatalf("应留下危害类拒绝记录")
	}

	// s2 弹门级:面板拒绝。
	var wg sync.WaitGroup
	var res2 *swarm.RunResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		res2, _ = ex.RunStep(ctx, "rel-e2e", "s2")
	}()
	waitUntil(t, 30*time.Second, "s2 弹门登记", func() bool { return len(gate.Pending("rel-e2e")) == 1 })
	panelRid := gate.Pending("rel-e2e")[0].ID
	var decided map[string]any
	post(t, h, http.MethodPost, "/api/tasks/rel-e2e/approvals/"+panelRid,
		`{"approved":false,"reason":"不批准"}`, http.StatusOK, &decided)
	wg.Wait()
	if res2.StepStatus != swarm.StepApprovalDenied {
		t.Fatalf("面板拒绝应熔断单位: %+v", res2)
	}

	// ---- 放行:危害类可放行,执行一次并全记账 ----
	var released map[string]any
	post(t, h, http.MethodPost, "/api/tasks/rel-e2e/approvals/"+harmRid+"/release",
		`{"reason":"复核后允许本次销毁演练"}`, http.StatusOK, &released)
	if released["released"] != true || !strings.Contains(fmt.Sprint(released["result"]), "rel-77e5") {
		t.Fatalf("放行执行结果不符: %+v", released)
	}
	// 重复放行 409(逐次,不累计授权)。
	post(t, h, http.MethodPost, "/api/tasks/rel-e2e/approvals/"+harmRid+"/release", `{}`, http.StatusConflict, &errBody)
	// 面板拒绝的记录不可放行 400。
	post(t, h, http.MethodPost, "/api/tasks/rel-e2e/approvals/"+panelRid+"/release", `{}`, http.StatusBadRequest, &errBody)
	// 未知请求 404。
	post(t, h, http.MethodPost, "/api/tasks/rel-e2e/approvals/ap-none/release", `{}`, http.StatusNotFound, &errBody)

	// 记录列表:拒绝线记录 releasable=false(已放行),forced 记录可见。
	var approvals map[string]any
	get(t, h, "/api/tasks/rel-e2e/approvals", http.StatusOK, &approvals)
	recs, _ := approvals["records"].([]any)
	var sawForced, sawHarmDenied bool
	for _, it := range recs {
		if m, ok := it.(map[string]any); ok {
			if m["type"] == "forced" {
				sawForced = true
			}
			if m["type"] == "denied" && m["deny_line"] == "harm" && m["releasable"] == false {
				sawHarmDenied = true
			}
		}
	}
	if !sawForced || !sawHarmDenied {
		t.Fatalf("审批记录应含 forced 与已放行的拒绝线记录: %+v", approvals)
	}
}

// approvalRecordList 解码事件流里的审批记录(测试助手)。
func approvalRecordList(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.ApprovalRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var r rings.ApprovalRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 approval.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}
