package server

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
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

// ---- P3-13b ④ 诊断包 + P4-32 导出增强 ----

// TestLogLevelEndpointsRemoved P4-32 ①:级别设置端点整体移除,
// 读写均 404(日志固定全量记录)。
func TestLogLevelEndpointsRemoved(t *testing.T) {
	_, mux, _ := newInterjectTestServer(t)
	if code, _ := callJSON(t, mux, "GET", "/api/config/log-level", ""); code != 404 {
		t.Fatalf("GET log-level 应 404,得到 %d", code)
	}
	if code, _ := callJSON(t, mux, "POST", "/api/config/log-level", `{"level":"debug"}`); code != 404 {
		t.Fatalf("POST log-level 应 404,得到 %d", code)
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
		files, _, err := getZipWithHeader(t, mux, path)
		if err != nil {
			t.Fatal(err)
		}
		return files
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

// getZipWithHeader 拉诊断包并解出全部内文件与 Content-Disposition
// (文件名范围标注断言用)。非 200 以 error 返回。
func getZipWithHeader(t *testing.T, mux *http.ServeMux, path string) (map[string]string, string, error) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		return nil, "", fmt.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		return nil, "", fmt.Errorf("解 zip: %w", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, "", fmt.Errorf("开包内文件 %s: %w", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out, rec.Header().Get("Content-Disposition"), nil
}

// TestDiagnosticsLogKindFilter P4-32 ②:按类型过滤只含命中行、文件名
// 带范围标注、缺省全量。
func TestDiagnosticsLogKindFilter(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-diag-kinds"
	mustAppend(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "类型过滤"}})

	// 开真实运行日志并落各型样例行(NoStderr 防测试输出噪声)。
	logDir := filepath.Join(t.TempDir(), "logs")
	lg, err := runlog.Open(logDir, runlog.Options{NoStderr: true})
	if err != nil {
		t.Fatalf("开运行日志: %v", err)
	}
	t.Cleanup(func() { lg.Close() })
	runlog.Errorf("工具执行异常: 任务 %s 单位 u1 工具 http_probe: boom", task)
	runlog.Infof("任务受阻: %s(模型全不通)", task)
	runlog.Infof("任务恢复: %s(模型调用恢复,受阻解除)", task)
	runlog.Warnf(failover.LogMsgBreakerTripped + ": 配置 m1 第 1 次开闸,原因 连续失败,冷却至 12:00:00")
	runlog.Infof("虫群单位起飞: u1 任务 %s 步骤 s1", task)
	runlog.Infof("虫群单位干完: u1 任务 %s 步骤 s1 结束 completed", task)
	runlog.Infof("配置修改: search(by creator)")

	// 类型筛选:error+units —— 只含命中行。
	files, disp, err := getZipWithHeader(t, mux,
		"/api/diagnostics?tasks="+task+"&kinds=error,units&content=logs")
	if err != nil {
		t.Fatal(err)
	}
	body := files["logs/ghostreaper.log"]
	if body == "" {
		t.Fatalf("筛选包缺日志;包内: %v", keysOf(files))
	}
	for _, want := range []string{"[ERROR]", "虫群单位起飞", "虫群单位干完"} {
		if !strings.Contains(body, want) {
			t.Fatalf("筛选日志缺命中行 %q:\n%s", want, body)
		}
	}
	for _, banned := range []string{"任务受阻", "任务恢复", "配置修改", "==== ghostreaper"} {
		if strings.Contains(body, banned) {
			t.Fatalf("筛选日志含未命中行 %q:\n%s", banned, body)
		}
	}
	// 文件名带范围标注(按既定序)。
	if !strings.Contains(disp, "错误+单位起落") {
		t.Fatalf("文件名缺范围标注: %s", disp)
	}
	// 诊断说明同步注明类型。
	if !strings.Contains(files["诊断说明.txt"], "日志类型: 错误+单位起落") {
		t.Fatalf("诊断说明缺日志类型:\n%s", files["诊断说明.txt"])
	}

	// 全部日志(缺省):各型行与日志头全量在。
	files, disp, err = getZipWithHeader(t, mux, "/api/diagnostics?tasks="+task)
	if err != nil {
		t.Fatal(err)
	}
	body = files["logs/ghostreaper.log"]
	for _, want := range []string{"任务受阻", "任务恢复", "配置修改", "==== ghostreaper 运行日志 ===="} {
		if !strings.Contains(body, want) {
			t.Fatalf("全量日志缺 %q:\n%s", want, body)
		}
	}
	if strings.Contains(disp, "错误+") || strings.Contains(disp, "单位起落") {
		t.Fatalf("全量导出不应带类型标注: %s", disp)
	}

	// 零命中档不进包:只选审批与拦截时本批日志无命中行。
	files, _, err = getZipWithHeader(t, mux,
		"/api/diagnostics?tasks="+task+"&kinds=approval&content=logs")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["logs/ghostreaper.log"]; ok {
		t.Fatal("零命中的日志档不应进包")
	}

	// 未知类型 id 拒绝。
	req := httptest.NewRequest("GET", "/api/diagnostics?kinds=bogus", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("未知类型应 400,得到 %d", rec.Code)
	}

	// 受阻与恢复型:受阻/恢复/熔断行命中,其余不进。
	files, _, err = getZipWithHeader(t, mux,
		"/api/diagnostics?tasks="+task+"&kinds=blocked&content=logs")
	if err != nil {
		t.Fatal(err)
	}
	body = files["logs/ghostreaper.log"]
	if body == "" {
		t.Fatalf("受阻筛选包缺日志;包内: %v", keysOf(files))
	}
	for _, want := range []string{"任务受阻", "任务恢复", failover.LogMsgBreakerTripped} {
		if !strings.Contains(body, want) {
			t.Fatalf("受阻筛选缺命中行 %q:\n%s", want, body)
		}
	}
	for _, banned := range []string{"[ERROR]", "虫群单位起飞", "配置修改"} {
		if strings.Contains(body, banned) {
			t.Fatalf("受阻筛选含未命中行 %q:\n%s", banned, body)
		}
	}
}

// TestDiagnosticsContentDB P4-32 ③:加勾账本数据库进包,键值表敏感值
// 脱敏、非敏感行原样。
func TestDiagnosticsContentDB(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	srv.Config.System.DBPath = filepath.Join(t.TempDir(), "rings.db")
	ctx := t.Context()
	mustAppend(t, srv, "t-diag-db",
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "库快照"}})

	// 敏感键(model.library.* / search.* / cred.* / mirror.ca.key)与
	// 登录凭据行(auth.*)各落一条(测试库未启用静态加密,明文入库——
	// 脱敏不看密文)。
	for k, v := range map[string]string{
		"model.library.m1":       `{"name":"m1","api_key":"sk-live-123456"}`,
		"search.config":          `{"backend":"brave","brave_key":"BSAKEY"}`,
		"cred.c1":                `{"name":"后台","username":"admin","secret":"P@ss"}`,
		"mirror.ca.key":          "-----BEGIN PRIVATE KEY-----",
		"auth.password":          `{"algo":"pbkdf2-sha256","salt":"abc"}`,
		"auth.token.abc":         `{"created_at":"2026-10-26T00:00:00Z"}`,
		"swarm.budget.max_turns": "600",
	} {
		if err := srv.Store.SetSetting(ctx, k, v, "creator"); err != nil {
			t.Fatalf("写设置 %s: %v", k, err)
		}
	}

	files, _, err := getZipWithHeader(t, mux, "/api/diagnostics?content=logs,db")
	if err != nil {
		t.Fatal(err)
	}
	dbName := "db/" + filepath.Base(srv.Config.System.DBPath)
	raw, ok := files[dbName]
	if !ok {
		t.Fatalf("包缺账本库 %s;包内: %v", dbName, keysOf(files))
	}
	// 快照字节落到临时文件后开库对账。
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(snap, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", snap)
	if err != nil {
		t.Fatalf("开快照: %v", err)
	}
	defer db.Close()
	values := map[string]string{}
	rrows, err := db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		t.Fatal(err)
	}
	for rrows.Next() {
		var k, v string
		_ = rrows.Scan(&k, &v)
		values[k] = v
	}
	rrows.Close()
	for _, sens := range []string{"model.library.m1", "search.config", "cred.c1", "mirror.ca.key", "auth.password", "auth.token.abc"} {
		if values[sens] != "(脱敏)" {
			t.Fatalf("敏感键 %s 未脱敏: %q", sens, values[sens])
		}
	}
	if values["swarm.budget.max_turns"] != "600" {
		t.Fatalf("非敏感行被改动: %q", values["swarm.budget.max_turns"])
	}
	// 事件数据随快照进包(诊断价值所在)。
	var evs int
	if err := db.QueryRow(`SELECT count(*) FROM events`).Scan(&evs); err != nil || evs == 0 {
		t.Fatalf("快照缺事件数据: count=%d err=%v", evs, err)
	}

	// 不勾 db(缺省 content):库不进包。
	files, _, err = getZipWithHeader(t, mux, "/api/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[dbName]; ok {
		t.Fatal("缺省包不应含账本库")
	}
}

// TestDiagnosticsContentTranscripts P4-32 ③:加勾会话转录,按已勾任务
// 进包、未勾任务不进。
func TestDiagnosticsContentTranscripts(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	taskA := "t-diag-ta"
	taskB := "t-diag-tb"
	mustAppend(t, srv, taskA,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "转录甲"}})
	mustAppend(t, srv, taskB,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "转录乙"}})

	// 在任务工作目录根下造 transcripts 文件(与执行器同一布局)。
	for task, unit := range map[string]string{taskA: "swarm-u1", taskB: "swarm-u9"} {
		dir := filepath.Join(srv.RootDir, task, "transcripts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf(`{"session":%q,"unit":%q}`, unit, unit)
		if err := os.WriteFile(filepath.Join(dir, unit+".jsonl"), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, _, err := getZipWithHeader(t, mux,
		"/api/diagnostics?tasks="+taskA+"&content=transcripts")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := files["transcripts/"+taskA+"/swarm-u1.jsonl"]
	if !ok {
		t.Fatalf("包缺已勾任务转录;包内: %v", keysOf(files))
	}
	if !strings.Contains(got, "swarm-u1") {
		t.Fatalf("转录内容不符: %s", got)
	}
	for name := range files {
		if strings.HasPrefix(name, "transcripts/"+taskB) {
			t.Fatalf("未勾任务的转录进包: %s", name)
		}
	}

	// 不勾 transcripts(缺省 content):转录不进包。
	files, _, err = getZipWithHeader(t, mux, "/api/diagnostics?tasks="+taskA)
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if strings.HasPrefix(name, "transcripts/") {
			t.Fatalf("缺省包不应含转录: %s", name)
		}
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
