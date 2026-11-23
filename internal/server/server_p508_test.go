package server

// P5-08 两层未读提醒的服务端类级测试(规则 15,多形态样本集):
// ① 页面级数字——三页签各自独立已读线:从未看过全计、进页盖戳只消本
// 页、盖戳后新落页再计(停留期间同口径)、意图补考步骤(retake)不计、
// 漏洞侧证据不足/被推翻/候选不计(非"新确认";证据不足落页由条目级
// 红点承载)、事实侧候选/被推翻不计、fixed 在档计。
// ② 条目级已看集合——追加幂等、三 kind 互不串、记已看不动已读线、
// 坏 kind / 超长 item 拒绝。
// ③ 任务列表三未读字段与 unread 端点同源。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// p508Intent 铺一条意图(step);retake=true 铺机器补考步骤(不入台账)。
func p508Intent(t *testing.T, srv *Server, task, id string, at time.Time, retake bool) {
	t.Helper()
	detail := map[string]any{}
	if retake {
		detail["kind"] = "retake"
	}
	if _, err := srv.Store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at,
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeStep, Title: "意图 " + id,
				Status: "queued", Detail: detail}},
	); err != nil {
		t.Fatalf("铺 %s 失败: %v", id, err)
	}
}

// p508Claim 铺一条交换单并落终态:confirmed → 落页时刻=确认;fixed 走
// 真实链路(先 confirmed 落确认时刻再转 fixed);insufficient/refuted/
// candidate 直接置终态(不上漏洞页或不计新确认)。
func p508Claim(t *testing.T, srv *Server, task, id, final string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", OccurredAt: at.Add(-time.Minute),
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeTestClaim, Title: "声明 " + id,
				Status: rings.TestClaimCandidate}},
	}
	switch final {
	case "fixed":
		events = append(events,
			rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", OccurredAt: at,
				Payload: rings.NodeStateChanged{NodeID: id, Status: rings.TestClaimConfirmed}},
			rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", OccurredAt: at.Add(time.Minute),
				Payload: rings.NodeStateChanged{NodeID: id, Status: rings.TestClaimFixed}})
	case "candidate":
		// 停在交卷瞬态,无状态迁移。
	default: // confirmed / insufficient / refuted
		events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			OccurredAt: at, Payload: rings.NodeStateChanged{NodeID: id, Status: final}})
	}
	if _, err := srv.Store.Append(ctx, task, events...); err != nil {
		t.Fatalf("铺 %s 失败: %v", id, err)
	}
}

// p508Fact 铺一条事实并落终态(confirmed 带确认时刻;candidate/refuted
// 不上事实页)。
func p508Fact(t *testing.T, srv *Server, task, id, final string, at time.Time) {
	t.Helper()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", OccurredAt: at.Add(-time.Minute),
			Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeFact, Title: "事实 " + id,
				Status: "candidate"}},
	}
	if final == "confirmed" {
		events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			OccurredAt: at, Payload: rings.NodeStateChanged{NodeID: id, Status: "confirmed"}})
	} else {
		events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			OccurredAt: at, Payload: rings.NodeStateChanged{NodeID: id, Status: final}})
	}
	if _, err := srv.Store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("铺 %s 失败: %v", id, err)
	}
}

// p508Counts 解包 unread 响应的三计数。
func p508Counts(t *testing.T, body map[string]any) (intents, claims, facts int) {
	t.Helper()
	return int(body["intents"].(float64)), int(body["claims"].(float64)), int(body["facts"].(float64))
}

func TestP508UnreadPageLayer(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p508-page"
	if _, err := srv.Store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "两层未读"}}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	// 在页条目(计):意图×2、漏洞 confirmed/fixed、事实 confirmed。
	p508Intent(t, srv, task, "s-old1", old, false)
	p508Intent(t, srv, task, "s-old2", old.Add(-time.Minute), false)
	p508Claim(t, srv, task, "tc-old", "confirmed", old)
	p508Claim(t, srv, task, "tc-fixed", "fixed", old)
	p508Fact(t, srv, task, "f-old", "confirmed", old)
	// 不在页/不计条目(多形态):补考步骤、交卷瞬态、证据不足、被推翻、
	// 候选事实、被推翻事实。
	p508Intent(t, srv, task, "s-retake", old, true)
	p508Claim(t, srv, task, "tc-cand", "candidate", old)
	p508Claim(t, srv, task, "tc-insuf", "insufficient", old)
	p508Claim(t, srv, task, "tc-refut", "refuted", old)
	p508Fact(t, srv, task, "f-cand", "candidate", old)
	p508Fact(t, srv, task, "f-refut", "refuted", old)

	// ① 从未看过:三页各自全计在页条目。
	code, body := callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	intents, claims, facts := p508Counts(t, body)
	if code != 200 || intents != 2 || claims != 2 || facts != 1 {
		t.Fatalf("① 未看过计数 = %d/%d/%d(code %d),期望 2/2/1", intents, claims, facts, code)
	}
	if seen, ok := body["seen"].(map[string]any); !ok || len(seen) != 3 {
		t.Fatalf("① seen 应含三键,得 %v", body["seen"])
	}

	// ② 进意图页盖戳:只消意图,漏洞/事实不动。
	code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"intents"}`)
	intents, claims, facts = p508Counts(t, body)
	if code != 200 || intents != 0 || claims != 2 || facts != 1 {
		t.Fatalf("② 进意图页后 = %d/%d/%d(code %d),期望 0/2/1", intents, claims, facts, code)
	}

	// ③ 盖戳后新落页(停留期间同口径):新意图再计;补考仍不计。
	now := time.Now().UTC()
	p508Intent(t, srv, task, "s-new", now, false)
	p508Intent(t, srv, task, "s-retake2", now, true)
	code, body = callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	intents, _, _ = p508Counts(t, body)
	if code != 200 || intents != 1 {
		t.Fatalf("③ 新意图后计数 = %d(code %d),期望 1(retake 不计)", intents, code)
	}

	// ④ 进漏洞页与事实页:三线全消后全零。
	if code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"claims"}`); code != 200 {
		t.Fatalf("④ claims 盖戳 code %d", code)
	}
	code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"facts"}`)
	intents, claims, facts = p508Counts(t, body)
	if code != 200 || intents != 1 || claims != 0 || facts != 0 {
		t.Fatalf("④ 三线状态 = %d/%d/%d(code %d),期望 1/0/0(意图线只消本页)", intents, claims, facts, code)
	}
}

func TestP508UnreadItemLayer(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p508-item"
	if _, err := srv.Store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "条目红点"}}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	p508Intent(t, srv, task, "s-1", at, false)
	p508Claim(t, srv, task, "tc-1", "confirmed", at)
	p508Fact(t, srv, task, "f-1", "confirmed", at)

	// ① 点开详情记已看:集合追加;幂等(重复记不增)。
	code, body := callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"intents","item":"s-1"}`)
	if code != 200 {
		t.Fatalf("① 记已看 code %d: %v", code, body)
	}
	seen := body["seen"].(map[string]any)
	if got := seen["intents"].([]any); len(got) != 1 || got[0] != "s-1" {
		t.Fatalf("① 已看集合 = %v,期望 [s-1]", got)
	}
	if code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"intents","item":"s-1"}`); code != 200 {
		t.Fatalf("① 幂等复记 code %d", code)
	}
	if got := body["seen"].(map[string]any)["intents"].([]any); len(got) != 1 {
		t.Fatalf("① 幂等复记后集合 = %v,期望仍 [s-1]", got)
	}

	// ② 三 kind 互不串:claims/facts 集合仍空,条目记已看不动已读线。
	code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"claims","item":"tc-1"}`)
	seen = body["seen"].(map[string]any)
	if code != 200 || len(seen["facts"].([]any)) != 0 || len(seen["claims"].([]any)) != 1 {
		t.Fatalf("② kind 互不串失败: %v(code %d)", seen, code)
	}
	if _, _, facts := p508Counts(t, body); facts != 1 {
		t.Fatalf("② 条目记已看不应动已读线,facts = %d 期望 1", facts)
	}

	// ③ 跨请求持久(服务端键值表,换浏览器一致的数据面)。
	code, body = callJSON(t, mux, "GET", "/api/tasks/"+task+"/unread", "")
	if code != 200 {
		t.Fatalf("③ 复读 code %d", code)
	}
	seen = body["seen"].(map[string]any)
	if len(seen["intents"].([]any)) != 1 || len(seen["claims"].([]any)) != 1 || len(seen["facts"].([]any)) != 0 {
		t.Fatalf("③ 复读已看集合 = %v,期望 intents/claims 各 1、facts 0", seen)
	}

	// ④ 坏形态拒绝:坏 kind、超长 item、不存在任务。
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"hypos"}`); code != 400 {
		t.Fatalf("④ 坏 kind 应 400,实得 %d", code)
	}
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen",
		`{"kind":"facts","item":"`+strings.Repeat("x", 129)+`"}`); code != 400 {
		t.Fatalf("④ 超长 item 应 400,实得 %d", code)
	}
	if code, _ = callJSON(t, mux, "POST", "/api/tasks/no-such/seen", `{"kind":"facts"}`); code != 404 {
		t.Fatalf("④ 不存在任务应 404,实得 %d", code)
	}
}

func TestP508TaskRowSummary(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p508-summary"
	if _, err := srv.Store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "汇总红点"}}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	p508Intent(t, srv, task, "s-1", at, false)
	p508Intent(t, srv, task, "s-2", at, false)
	p508Claim(t, srv, task, "tc-1", "confirmed", at)
	p508Fact(t, srv, task, "f-1", "confirmed", at)
	p508Fact(t, srv, task, "f-2", "confirmed", at)
	// 只进漏洞页:任务行应 intents=2 facts=2 claims=0(任一 >0 即亮红点)。
	if code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/seen", `{"kind":"claims"}`); code != 200 {
		t.Fatalf("claims 盖戳失败")
	}

	r := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 {
		t.Fatalf("任务列表解码失败(code %d): %v", w.Code, err)
	}
	var found bool
	for _, row := range rows {
		if row["id"] == task {
			found = true
			if row["unread_intents"] != float64(2) || row["unread_claims"] != float64(0) ||
				row["unread_facts"] != float64(2) {
				t.Fatalf("任务行未读字段 = 意图 %v 漏洞 %v 事实 %v,期望 2/0/2",
					row["unread_intents"], row["unread_claims"], row["unread_facts"])
			}
		}
	}
	if !found {
		t.Fatalf("任务列表未见任务 %s", task)
	}
}
