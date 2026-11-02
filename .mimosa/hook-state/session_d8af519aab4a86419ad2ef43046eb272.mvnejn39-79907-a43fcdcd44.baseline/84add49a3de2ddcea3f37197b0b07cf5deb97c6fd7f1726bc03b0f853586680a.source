package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P3-13b ② 会话管理:重命名 / 备注 / 置顶 ----

// seedUnitSession 造一只虫群单位的起飞记录(session_started,会话锚
// swarm-u1)。
func seedUnitSession(t *testing.T, srv *Server, task, step, unit, sid string) {
	t.Helper()
	_, err := srv.Store.Append(t.Context(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit,
		Payload: rings.SwarmRecord{StepID: step, UnitID: unit, TranscriptID: sid, Type: rings.SwarmRecSessionStarted},
	})
	if err != nil {
		t.Fatalf("seed 单位起飞失败: %v", err)
	}
}

func TestSessionMetaAPI(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-p313b"
	mustAppend(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "会话管理验证"}})
	seedUnitSession(t, srv, task, "s1", "u1", "swarm-u1")
	seedUnitSession(t, srv, task, "s2", "u2", "swarm-u2")

	// 重命名 + 备注 + 置顶一次提交。
	code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/swarm-u1/meta",
		`{"title":"SQL 注入验证那条","note":"影响面证据充分","pinned":true}`)
	if code != 200 {
		t.Fatalf("POST meta = %d: %v", code, out)
	}
	if out["title"] != "SQL 注入验证那条" || out["pinned"] != true || out["note"] != "影响面证据充分" {
		t.Fatalf("返回标签不符: %v", out)
	}

	// 单位列表携带标签。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != 200 {
		t.Fatalf("GET units = %d", code)
	}
	units := out["units"].([]any)
	bySid := map[string]map[string]any{}
	for _, u := range units {
		m := u.(map[string]any)
		bySid[m["session_id"].(string)] = m
	}
	meta := bySid["swarm-u1"]["meta"].(map[string]any)
	if meta["title"] != "SQL 注入验证那条" || meta["pinned"] != true {
		t.Fatalf("单位列表标签不符: %v", meta)
	}
	meta2 := bySid["swarm-u2"]["meta"].(map[string]any)
	if meta2["title"] != nil || meta2["pinned"] != nil {
		t.Fatalf("未标记的单位应为零值标签: %v", meta2)
	}

	// 清除名称(空串 = 回默认名),置顶取消。
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/swarm-u1/meta",
		`{"title":"","pinned":false}`)
	if code != 200 {
		t.Fatalf("POST meta 清除 = %d: %v", code, out)
	}
	if out["title"] != nil || out["pinned"] != nil || out["note"] != "影响面证据充分" {
		t.Fatalf("清除后标签不符: %v", out)
	}

	// 空提交被拒(至少携带一项)。
	if code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/swarm-u1/meta", `{}`); code != 400 {
		t.Fatalf("空提交应 400,得到 %d", code)
	}
	// 非法会话锚被拒(rings 白名单;含空格的锚不匹配词表)。
	if code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/evil%20sid/meta", `{"title":"x"}`); code != 400 {
		t.Fatalf("非法会话锚应 400,得到 %d", code)
	}

	// 入账留痕:session.meta 事件在账本里。
	evs, err := srv.Store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	metaEvents := 0
	for _, ev := range evs {
		if ev.Kind == rings.EventSessionMeta {
			metaEvents++
			var p rings.SessionMeta
			if json.Unmarshal(ev.Payload, &p) != nil || p.SessionID != "swarm-u1" {
				t.Fatalf("session.meta 事件负载不符: %s", ev.Payload)
			}
		}
	}
	if metaEvents != 2 {
		t.Fatalf("session.meta 事件应有 2 条,得到 %d", metaEvents)
	}
}

func TestSessionMetaOnShepherdAndOvermind(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-p313b-so"
	mustAppend(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "牧者与主宰标记"}})

	// 牧者会话标记(锚 shepherd-<taskID>)。
	code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/shepherd-"+task+"/meta",
		`{"title":"DVWA 总对话","note":"主入口"}`)
	if code != 200 || out["title"] != "DVWA 总对话" {
		t.Fatalf("牧者标记 = %d: %v", code, out)
	}
	// 主宰面板会话标记(锚 overmind)。
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/sessions/overmind/meta",
		`{"title":"研判主线"}`)
	if code != 200 || out["title"] != "研判主线" {
		t.Fatalf("主宰标记 = %d: %v", code, out)
	}

	// 两个读视图同源携带。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/shepherd", "")
	if code != 200 || out["meta"].(map[string]any)["note"] != "主入口" {
		t.Fatalf("牧者视图标签不符: %v", out["meta"])
	}
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != 200 || out["meta"].(map[string]any)["title"] != "研判主线" {
		t.Fatalf("主宰视图标签不符: %v", out["meta"])
	}
}

// ---- P3-13b ① 成果溯源 ----

func TestClaimOrigin(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-origin"
	claim := "tc-ori1"
	mustAppend(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "溯源验证"}},
		// 发现单位 u1 起飞(会话 swarm-u1)后提交声明。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1", Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: claim, NodeType: rings.NodeTestClaim, Title: "登录可联合查询",
				Status: rings.TestClaimCandidate, Detail: map[string]any{
					"unit_id": "u1", "step_id": "s1", "transcript_id": "swarm-u1"}}},
		// 反证单位 u2 起飞(会话 swarm-u2)后写回结论。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2",
			Payload: rings.SwarmRecord{StepID: "s2", UnitID: "u2", TranscriptID: "swarm-u2", Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2",
			Payload: rings.TestClaimRecord{ClaimID: claim, StepID: "s2", UnitID: "u2",
				Type: rings.TestClaimRecFalsify, Round: 1, ImpactSupported: true, ImpactLevel: "high"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			Payload: rings.NodeStateChanged{NodeID: claim, Status: rings.TestClaimConfirmed}},
	)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/claims/"+claim, "")
	if code != 200 {
		t.Fatalf("GET claim = %d: %v", code, out)
	}
	origin, _ := out["origin"].(map[string]any)
	disc, _ := origin["discoverer"].(map[string]any)
	if disc == nil || disc["unit_id"] != "u1" || disc["session_id"] != "swarm-u1" || disc["step_id"] != "s1" {
		t.Fatalf("发现单位不符: %v", origin["discoverer"])
	}
	fals, _ := origin["falsifiers"].([]any)
	if len(fals) != 1 {
		t.Fatalf("反证单位应 1 个: %v", origin["falsifiers"])
	}
	f := fals[0].(map[string]any)
	if f["unit_id"] != "u2" || f["session_id"] != "swarm-u2" || f["step_id"] != "s2" {
		t.Fatalf("反证单位不符: %v", f)
	}
	if f["round"] != float64(1) {
		t.Fatalf("反证轮次不符: %v", f)
	}
}

// ---- P3-13b ④ 诊断包与日志级别 ----

func TestLogLevelAPI(t *testing.T) {
	_, mux, _ := newInterjectTestServer(t)
	code, out := callJSON(t, mux, "POST", "/api/config/log-level", `{"level":"debug"}`)
	if code != 200 || out["level"] != "DEBUG" {
		t.Fatalf("写级别 = %d: %v", code, out)
	}
	code, out = callJSON(t, mux, "GET", "/api/config/log-level", "")
	if code != 200 || out["level"] != "DEBUG" {
		t.Fatalf("读级别 = %d: %v", code, out)
	}
	if code, _ := callJSON(t, mux, "POST", "/api/config/log-level", `{"level":"bogus"}`); code != 400 {
		t.Fatalf("非法级别应 400")
	}
	// 恢复 info,防影响同进程其他测试。
	if code, _ := callJSON(t, mux, "POST", "/api/config/log-level", `{"level":"info"}`); code != 200 {
		t.Fatal("恢复 info 失败")
	}
}

func TestDiagnosticsExport(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	taskA := "t-diag-a"
	taskB := "t-diag-b"
	mustAppend(t, srv, taskA,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "诊断甲"}})
	mustAppend(t, srv, taskB,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "诊断乙"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "achieved"}},
		rings.Event{Kind: rings.EventBlackboxRecord, Actor: "overmind",
			Payload: rings.BlackboxRecord{Type: rings.BlackboxUnitExhausted, StepID: "s1", UnitID: "u1",
				Detail: map[string]any{"reason": "轮次耗尽"}}},
	)

	getZip := func(path string) map[string]string {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatalf("解 zip: %v", err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("开包内文件 %s: %v", f.Name, err)
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(b)
		}
		return out
	}

	// 全选模式(缺省 tasks):两个任务摘要都在,说明/配置摘要齐备。
	all := getZip("/api/diagnostics")
	for _, name := range []string{"诊断说明.txt", "配置摘要.txt"} {
		if _, ok := all[name]; !ok {
			t.Fatalf("全选包缺 %s;包内: %v", name, keysOf(all))
		}
	}
	if _, ok := all["tasks/"+taskA+".txt"]; !ok {
		t.Fatalf("全选包缺任务 %s 摘要", taskA)
	}
	if !strings.Contains(all["tasks/"+taskB+".txt"], "blackbox 计数") && !strings.Contains(all["tasks/"+taskB+".txt"], "goal=1") {
		// 节点计数行按类型计(goal 1 个)。
		if !strings.Contains(all["tasks/"+taskB+".txt"], "goal: 1") {
			t.Fatalf("任务摘要缺节点计数:\n%s", all["tasks/"+taskB+".txt"])
		}
	}
	if !strings.Contains(all["tasks/"+taskB+".txt"], "unit_exhausted") {
		t.Fatalf("任务摘要缺近期系统事件:\n%s", all["tasks/"+taskB+".txt"])
	}
	if !strings.Contains(all["配置摘要.txt"], "日志级别") || !strings.Contains(all["配置摘要.txt"], "执行预算") {
		t.Fatalf("配置摘要缺段:\n%s", all["配置摘要.txt"])
	}

	// 按任务筛选模式:只含勾选任务。
	one := getZip("/api/diagnostics?tasks=" + taskA)
	if _, ok := one["tasks/"+taskB+".txt"]; ok {
		t.Fatal("筛选模式不应包含未勾选任务")
	}
	if _, ok := one["tasks/"+taskA+".txt"]; !ok {
		t.Fatal("筛选模式缺勾选任务摘要")
	}
	if !strings.Contains(one["诊断说明.txt"], "勾选任务 1 个") {
		t.Fatalf("诊断说明范围不符:\n%s", one["诊断说明.txt"])
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustAppend(t *testing.T, srv *Server, task string, evs ...rings.Event) {
	t.Helper()
	if _, err := srv.Store.Append(t.Context(), task, evs...); err != nil {
		t.Fatalf("seed 事件失败: %v", err)
	}
}
