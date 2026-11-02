package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
)

// ---- P2-07 牧者会话面板 API:对话流 / 信息条 / 节点详情 ----

func newShepherdTestServer(t *testing.T) (*Server, *http.ServeMux, *shepherd.Service) {
	t.Helper()
	store, err := rings.Open(t.TempDir() + "/rings.db")
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr, err := manager.New(manager.Config{Store: store, RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, swarm.NewLedgerGate(store))
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	shep := &shepherd.Service{Store: store, RootDir: t.TempDir()}
	srv.Shepherd = shep
	return srv, srv.Handler(), shep
}

// TestShepherdAPI 对话端点:POST 入队(已排队)→ GET 视图(消息/
// 状态/信息条结构);空消息与未知任务如实拒绝。
func TestShepherdAPI(t *testing.T) {
	srv, mux, _ := newShepherdTestServer(t)
	const task = "api-sh-task"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "API 任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	// 空消息拒。
	r := httptest.NewRequest("POST", "/api/tasks/"+task+"/shepherd/messages", strings.NewReader(`{"text":"  "}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空消息应 400,实为 %d", w.Code)
	}

	// 正常入队。
	r = httptest.NewRequest("POST", "/api/tasks/"+task+"/shepherd/messages", strings.NewReader(`{"text":"进度?"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("入队应 200,实为 %d: %s", w.Code, w.Body.String())
	}
	var posted struct {
		MessageID string `json:"message_id"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &posted); err != nil || posted.MessageID == "" || posted.State != "queued" {
		t.Fatalf("入队响应不符: %s", w.Body.String())
	}

	// 未配模型:异步落失败态,GET 可见(送达状态机如实呈现)。
	deadline := time.Now().Add(4 * time.Second)
	var view struct {
		Messages []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Note  string `json:"note"`
		} `json:"messages"`
		Info struct {
			Model         string  `json:"model"`
			ContextWindow int     `json:"context_window"`
			ContextRatio  float64 `json:"context_ratio"`
			Calls         int     `json:"calls"`
		} `json:"info"`
		ProviderOn bool `json:"provider_on"`
	}
	for time.Now().Before(deadline) {
		r = httptest.NewRequest("GET", "/api/tasks/"+task+"/shepherd", nil)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET 应 200,实为 %d", w.Code)
		}
		_ = json.Unmarshal(w.Body.Bytes(), &view)
		if len(view.Messages) == 1 && view.Messages[0].State == "failed" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(view.Messages) != 1 || view.Messages[0].ID != posted.MessageID ||
		view.Messages[0].State != "failed" || !strings.Contains(view.Messages[0].Note, "模型未配置") {
		t.Fatalf("失败态视图不符: %+v", view.Messages)
	}
	if view.ProviderOn {
		t.Fatalf("provider_on 应为 false")
	}
	if view.Info.Calls != 0 || view.Info.ContextRatio != 0 {
		t.Fatalf("未发生模型调用时信息条计数应为零: %+v", view.Info)
	}

	// 未知任务 404。
	r = httptest.NewRequest("GET", "/api/tasks/ghost/shepherd", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知任务应 404,实为 %d", w.Code)
	}
}

// TestNodeDetailAPI 节点详情端点:fact# 等引用的点击核对面(存在与
// 不存在两路)。
func TestNodeDetailAPI(t *testing.T) {
	srv, mux, _ := newShepherdTestServer(t)
	const task = "api-node-task"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "节点任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if _, err := srv.Store.Append(t.Context(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: "f-1", NodeType: rings.NodeFact,
			Title: "标题页含版本泄露", Status: "confirmed"},
	}); err != nil {
		t.Fatalf("种节点失败: %v", err)
	}

	r := httptest.NewRequest("GET", "/api/tasks/"+task+"/nodes/f-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("节点详情应 200,实为 %d", w.Code)
	}
	var node map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil ||
		node["type"] != "fact" || node["id"] != "f-1" {
		t.Fatalf("节点详情不符: %s", w.Body.String())
	}

	r = httptest.NewRequest("GET", "/api/tasks/"+task+"/nodes/f-404", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在节点应 404,实为 %d", w.Code)
	}
}

// TestShepherdInfoBarFromTokens 信息条对账:手工落 token 行(agent=
// shepherd,transcript 锚会话)后,四项与 token 独立表逐行一致;
// 比例 = 最近请求 input ÷ 窗口。
func TestShepherdInfoBarFromTokens(t *testing.T) {
	srv, _, shep := newShepherdTestServer(t)
	const task = "api-info-task"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "信息条任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	sid := shepherd.SessionID(task)
	if err := srv.Store.RecordTokens(t.Context(),
		rings.TokenUsage{TaskID: task, Agent: shepherd.ActorShepherd, Model: "glm-flash",
			TranscriptID: sid, Turn: 1, InputTokens: 1000, OutputTokens: 50},
		rings.TokenUsage{TaskID: task, Agent: shepherd.ActorShepherd, Model: "glm-flash",
			TranscriptID: sid, Turn: 2, InputTokens: 3000, OutputTokens: 80, CacheReadTokens: 200},
		// 别的会话/别的 agent 的行不进信息条。
		rings.TokenUsage{TaskID: task, Agent: "swarm:u1", Model: "glm-flash",
			TranscriptID: "swarm-u1", Turn: 1, InputTokens: 9999},
	); err != nil {
		t.Fatalf("落 token 行失败: %v", err)
	}
	if err := failover.SaveModelConfig(t.Context(), srv.Store, failover.ModelConfig{
		Name: "glm", Format: "openai", BaseURL: "http://x", APIKey: "sk-abc1234567",
		Model: "glm-flash", ContextWindow: 10000,
	}, "creator"); err != nil {
		t.Fatalf("存模型配置失败: %v", err)
	}
	_ = shep
	info := srv.shepherdInfo(httptest.NewRequest("GET", "/api/tasks/"+task+"/shepherd", nil), task)
	if info.Model != "glm-flash" || info.InputTokens != 4000 || info.OutputTokens != 130 ||
		info.CacheTokens != 200 || info.Calls != 2 {
		t.Fatalf("信息条 token 三项对账不符: %+v", info)
	}
	if info.ContextWindow != 10000 {
		t.Fatalf("窗口应从模型库解析: %+v", info)
	}
	if info.ContextRatio < 0.299 || info.ContextRatio > 0.301 {
		t.Fatalf("比例应 = 最近请求 input(3000) ÷ 窗口(10000) = 0.3,实为 %f", info.ContextRatio)
	}
}
