package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/interject"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
)

// ---- P2-08 人机通道面板 API:单位列表 / 插话 / 控制 / 冬眠 ----

func newInterjectTestServer(t *testing.T) (*Server, *http.ServeMux, *hiber.Gate) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gate := hiber.New()
	mgr, err := manager.New(manager.Config{Store: store, RootDir: dir, Hiber: gate})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	ag := swarm.NewLedgerGate(store)
	ag.Hiber = gate
	srv, err := New(store, mgr, ag)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	srv.HiberGate = gate
	srv.Loops = overmind.NewRegistry()
	srv.RootDir = dir
	srv.Swarm = swarm.NewExecutor(store, nil, dir, swarm.Budget{})
	srv.Shepherd = &shepherd.Service{Store: store, RootDir: dir, Hiber: gate}
	srv.Interject = &interject.Service{Store: store, RootDir: dir, Helm: srv.Swarm.Helm, Hiber: gate}
	return srv, srv.Handler(), gate
}

func callJSON(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("{}")
	} else {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// seedIJUnit 造一只已结束单位(s1/swarm-u1)+ 一只在跑(s2/swarm-u2)。
func seedIJUnit(t *testing.T, srv *Server, task string) {
	t.Helper()
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "端点验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "已结束探测", Status: "done"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd, Note: "reason=completed turns=2 status=done billable=1s gate_wait=0s block_wait=0s pause_wait=0s"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
}

// TestUnitsAndInterjectAPI 单位列表投影 + 插话问/改端点基础契约。
func TestUnitsAndInterjectAPI(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "ij-api-task"
	seedIJUnit(t, srv, task)

	// 单位列表:已结束单位可见,状态 done。
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d %v", code, out)
	}
	units, _ := out["units"].([]any)
	if len(units) != 1 {
		t.Fatalf("应列 1 只单位: %v", out)
	}
	u := units[0].(map[string]any)
	if u["session_id"] != "swarm-u1" || u["status"] != "done" || u["interject_id"] != "u:swarm-u1" {
		t.Fatalf("单位行不符: %v", u)
	}

	// 问(已结束单位,未配模型 → 异步失败态,但入队 200)。
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/interject/messages",
		`{"target":"unit","unit_id":"u1","step_id":"s1","session_id":"swarm-u1","mode":"ask","text":"做了什么"}`)
	if code != http.StatusOK || out["message_id"] == "" {
		t.Fatalf("问应入队: %d %v", code, out)
	}
	// 改(已结束单位):舵杆拒(无生效点),400 透传。
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/interject/messages",
		`{"target":"unit","unit_id":"u1","step_id":"s1","session_id":"swarm-u1","mode":"steer","text":"改"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("对已结束单位改向应 400: %d %v", code, out)
	}
	// 改(对主宰):建议入账 200 带 av- 关联。
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/interject/messages",
		`{"target":"overmind","mode":"steer","text":"建议优先探测"}`)
	if code != http.StatusOK || !strings.HasPrefix(out["ref_id"].(string), "av-") {
		t.Fatalf("主宰建议应 200 带 av-*: %d %v", code, out)
	}
	// 读视图:消息可见。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/interject?target=overmind", "")
	if code != http.StatusOK {
		t.Fatalf("插话视图应 200: %d", code)
	}
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("应 1 条消息: %v", out)
	}
}

// TestHibernateWakeEndpoints 冬眠/唤醒端点:状态翻转 + 冬眠中单位控制
// 与插话冻结 + 审批列表 frozen 标记。
func TestHibernateWakeEndpoints(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "hib-api-task"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "冬眠端点", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/hibernate", "")
	if code != http.StatusOK || out["status"] != "hibernated" {
		t.Fatalf("冬眠应 200: %d %v", code, out)
	}
	if _, ok := out["pending_approvals"]; !ok {
		t.Fatalf("响应应含待裁决笔数: %v", out)
	}
	// 冬眠中:插话拒、单位控制拒、主宰控制拒、审批列表 frozen。
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/interject/messages",
		`{"target":"overmind","mode":"ask","text":"?"}`); code != http.StatusBadRequest {
		t.Fatalf("冬眠中插话应 400: %d", code)
	}
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/units/s1/pause", ""); code != http.StatusConflict {
		t.Fatalf("冬眠中单位控制应 409: %d", code)
	}
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/overmind/pause", ""); code != http.StatusConflict {
		t.Fatalf("冬眠中主宰控制应 409: %d", code)
	}
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/approvals", "")
	if code != http.StatusOK || out["frozen"] != true {
		t.Fatalf("审批列表应 frozen: %d %v", code, out)
	}
	// 牧者会话读视图带冬眠标记。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/shepherd", "")
	if code != http.StatusOK || out["hibernated"] != true {
		t.Fatalf("牧者视图应 hibernated: %d %v", code, out)
	}

	// 唤醒:状态恢复,插话恢复可用。
	if code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/hibernate-wake", ""); code != http.StatusOK || out["status"] != "awake" {
		t.Fatalf("唤醒应 200: %d %v", code, out)
	}
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/interject/messages",
		`{"target":"overmind","mode":"ask","text":"?"}`); code != http.StatusOK {
		t.Fatalf("唤醒后插话应恢复: %d", code)
	}
	_ = srv.Manager.StopBy(task, nil)
}

// TestUnitControlEndpoints 单位暂停/继续端点:对不在跑步骤如实 400;
// 主宰循环控制对无循环任务如实 409。
func TestUnitControlEndpoints(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "ctl-api-task"
	seedIJUnit(t, srv, task)
	// 单位控制:步骤已结束 → 400。
	if code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/units/s1/pause", ""); code != http.StatusBadRequest {
		t.Fatalf("对已结束步骤暂停应 400")
	}
	// 主宰控制:无在跑循环 → 409。
	if code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/overmind/pause", ""); code != http.StatusConflict {
		t.Fatalf("无循环的主宰控制应 409")
	}
}

// TestOvermindStatusEndpoint 循环状态读:终态任务无循环(running=false),
// 注册表有循环时如实呈现 paused;冬眠态一并透出。
func TestOvermindStatusEndpoint(t *testing.T) {
	srv, mux, gate := newInterjectTestServer(t)
	const task = "om-status-task"
	if _, err := srv.Manager.CreateTask(t.Context(), task, "状态端点", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	// 无注册循环(测试装配无 OnTaskRun):running=false。
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != http.StatusOK || out["running"] != false || out["paused"] != false {
		t.Fatalf("无循环应 running=false: %d %v", code, out)
	}
	if out["task_status"] != "started" {
		t.Fatalf("task_status 应透出: %v", out)
	}
	// 冬眠:hibernated=true。
	if err := srv.Manager.Hibernate(task, nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	_, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if out["hibernated"] != true {
		t.Fatalf("冬眠态应透出: %v", out)
	}
	_ = gate
	_ = srv.Manager.StopBy(task, nil)
}
