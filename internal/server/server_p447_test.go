package server

// P4-47 A 未读红点的服务端测试:成果与事实子页签独立已读线(键值表
// 服务端盖戳)、判定口径与页签一致(成果=confirmed/fixed,事实=
// confirmed)、看过之后再确认算新未读(含停留期间)、任务列表行汇总
// 字段与 unread 端点同源。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// p447ConfirmClaim 铺一条 testclaim 并在指定时刻转 confirmed;fixed 走
// 真实链路(先 confirmed 落确认时刻,再复测转 fixed——确认时刻保留)。
func p447ConfirmClaim(t *testing.T, srv *Server, task, id string, at time.Time, fixed bool) {
	t.Helper()
	ctx := context.Background()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", OccurredAt: at.Add(-time.Minute),
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeTestClaim, Title: "声明 " + id,
				Status: rings.TestClaimCandidate}},
		{Kind: rings.EventNodeStateChanged, Actor: "arbiter", OccurredAt: at,
			Payload: rings.NodeStateChanged{NodeID: id, Status: rings.TestClaimConfirmed}},
	}
	if fixed {
		events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			OccurredAt: at.Add(time.Minute),
			Payload:    rings.NodeStateChanged{NodeID: id, Status: rings.TestClaimFixed}})
	}
	if _, err := srv.Store.Append(ctx, task, events...); err != nil {
		t.Fatalf("铺 %s 失败: %v", id, err)
	}
}

// p447ConfirmFact 铺一条 fact 并在指定时刻转 confirmed。
func p447ConfirmFact(t *testing.T, srv *Server, task, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := srv.Store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at.Add(-time.Minute),
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeFact, Title: "事实 " + id,
				Status: "candidate"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", OccurredAt: at,
			Payload: rings.NodeStateChanged{NodeID: id, Status: "confirmed"}},
	); err != nil {
		t.Fatalf("铺 %s 失败: %v", id, err)
	}
}

func TestP447UnreadRedDot(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p447-unread"
	if _, err := srv.Store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "未读红点"}}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	p447ConfirmClaim(t, srv, task, "tc-old", old, false)
	p447ConfirmFact(t, srv, task, "f-old", old)

	// ① 从未看过:全部已确认计为未读。
	code, body := callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	if code != 200 || body["claims"] != float64(1) || body["facts"] != float64(1) {
		t.Fatalf("① 未看过计数 = %v(code %d),期望 claims=1 facts=1", body, code)
	}

	// ② 点成果子页签:成果线盖戳,只消成果、事实仍亮。
	code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"claims"}`)
	if code != 200 || body["claims"] != float64(0) || body["facts"] != float64(1) {
		t.Fatalf("② 看完成果计数 = %v(code %d),期望 claims=0 facts=1", body, code)
	}
	code, body = callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	if code != 200 || body["claims"] != float64(0) || body["facts"] != float64(1) {
		t.Fatalf("② 复读计数 = %v,期望 claims=0 facts=1", body)
	}

	// ③ 停留期间(看过之后)新确认成果 + 已修复(fixed)成果与新事实:算新未读。
	// (确认时刻取当下:测试顺序执行,必然晚于 ② 的已读线、早于 ④ 的。)
	now := time.Now().UTC()
	p447ConfirmClaim(t, srv, task, "tc-new", now, false)
	p447ConfirmClaim(t, srv, task, "tc-fixed", now, true)
	p447ConfirmFact(t, srv, task, "f-new", now)
	code, body = callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	if code != 200 || body["claims"] != float64(2) || body["facts"] != float64(2) {
		t.Fatalf("③ 新增后计数 = %v,期望 claims=2(fixed 在档) facts=2", body)
	}

	// ④ 点事实子页签:事实线独立盖戳,成果未读不受影响。
	code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"facts"}`)
	if code != 200 || body["claims"] != float64(2) || body["facts"] != float64(0) {
		t.Fatalf("④ 看完事实计数 = %v,期望 claims=2 facts=0", body)
	}

	// ⑤ 任务列表汇总字段同源(任一未读即数据在;顶层为数组)。
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 {
		t.Fatalf("⑤ 任务列表解码失败(code %d): %v", w.Code, err)
	}
	var found bool
	for _, row := range rows {
		if row["id"] == task {
			found = true
			if row["unread_claims"] != float64(2) || row["unread_facts"] != float64(0) {
				t.Fatalf("⑤ 任务行未读字段 = claims %v facts %v,期望 2/0",
					row["unread_claims"], row["unread_facts"])
			}
		}
	}
	if !found {
		t.Fatalf("⑤ 任务列表未见任务 %s", task)
	}

	// ⑥ 坏 kind 拒绝;不存在的任务 404。
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"hypos"}`); code != 400 {
		t.Fatalf("⑥ 坏 kind 应 400,实得 %d", code)
	}
	if code, _ = callJSON(t, mux, "GET", "/api/tasks/no-such/unread", ""); code != 404 {
		t.Fatalf("⑥ 不存在任务应 404,实得 %d", code)
	}
}
