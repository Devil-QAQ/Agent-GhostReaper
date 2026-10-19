package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-06e 模型库 API / Key 打码 / 自动重开 / 恢复全部 / 受阻徽标 ----

func newModelsTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	return srv, srv.Handler()
}

// seedBlockedStep 在任务图上直接落一条 blocked 步骤(测试内等价书写:
// 真实路径由执行器重跑耗尽产生,见 swarm 包模型故障测试)。
func seedBlockedStep(t *testing.T, store *rings.Store, task, stepID, reason string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "探测意图", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: "blocked"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: stepID, UnitID: "u1", Type: rings.SwarmRecSessionEnd,
			Note: "reason=" + reason + " turns=1 status=blocked billable=0.0s gate_wait=0.0s block_wait=0.5s"}},
	); err != nil {
		t.Fatalf("种 blocked 步骤失败: %v", err)
	}
}

// TestModelsAPIKeyMasking 模型库写读:GET 永不回显明文 key(打码形态
// sk-…后4);编辑不重填 key 保留旧值;删除后激活项清除。
func TestModelsAPIKeyMasking(t *testing.T) {
	srv, h := newModelsTestServer(t)
	_ = srv

	var saved map[string]any
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"main","format":"openai","base_url":"http://127.0.0.1:8000/v1",
		"api_key":"sk-demo-plaintext-987654321","model":"demo-model"}`,
		http.StatusOK, &saved)
	lib, _ := json.Marshal(saved)
	if strings.Contains(string(lib), "sk-demo-plaintext-987654321") {
		t.Fatalf("保存响应不得回显明文 key: %s", lib)
	}

	var view struct {
		Library []struct {
			Name       string `json:"name"`
			APIKeyMask string `json:"api_key_mask"`
			HasKey     bool   `json:"has_key"`
			UpdatedBy  string `json:"updated_by"`
		} `json:"library"`
	}
	get(t, h, "/api/models", http.StatusOK, &view)
	if len(view.Library) != 1 || view.Library[0].Name != "main" {
		t.Fatalf("库清单不符: %+v", view.Library)
	}
	if view.Library[0].APIKeyMask != "sk-…4321" || !view.Library[0].HasKey {
		t.Fatalf("key 应打码呈现: %+v", view.Library[0])
	}
	if view.Library[0].UpdatedBy != "creator" {
		t.Fatalf("写入审计应带 creator: %+v", view.Library[0])
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "sk-demo-plaintext-987654321") {
		t.Fatal("GET 响应不得含明文 key")
	}

	// 编辑不重填 key:留空保留旧值(打码不变)。
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"main","format":"openai","base_url":"http://127.0.0.1:9000/v1",
		"api_key":"","model":"demo-model-v2"}`,
		http.StatusOK, &saved)
	get(t, h, "/api/models", http.StatusOK, &view)
	if view.Library[0].APIKeyMask != "sk-…4321" {
		t.Fatalf("空 key 编辑应保留旧 key: %+v", view.Library[0])
	}
	// 服务端读侧仍是明文(构建 Provider 用)。
	cfg, ok, err := failover.ModelConfigByName(context.Background(), srv.Store, "main")
	if err != nil || !ok || cfg.APIKey != "sk-demo-plaintext-987654321" || cfg.Model != "demo-model-v2" {
		t.Fatalf("服务端配置不符: ok=%v err=%v cfg=%+v", ok, err, cfg)
	}

	// 坏输入受控拒绝:新配置缺 key。
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"nokey","format":"openai","base_url":"http://x","api_key":"","model":"m"}`,
		http.StatusBadRequest, &saved)

	// 激活 + 删除:激活项随删除清除。
	var act map[string]any
	post(t, h, http.MethodPost, "/api/models/activate", `{"primary":"main"}`, http.StatusOK, &act)
	post(t, h, http.MethodDelete, "/api/models/main", "", http.StatusOK, &act)
	get(t, h, "/api/models", http.StatusOK, &view)
	if len(view.Library) != 0 {
		t.Fatalf("删除后库应空: %+v", view.Library)
	}
	if p, ok, _ := failover.ActiveName(context.Background(), srv.Store, failover.RolePrimary); ok {
		t.Fatalf("删除被激活配置后激活项应清除: %s", p)
	}
}

// TestRecoverAllAndAutoReopen 恢复两层:① 激活切换(配置写)自动重开
// blocked 步骤;② 恢复全部一键重开全部受阻任务;终态任务两者都不碰。
func TestRecoverAllAndAutoReopen(t *testing.T) {
	srv, h := newModelsTestServer(t)
	store := srv.Store

	// 三个任务:运行中(受阻)、终态(熔断,含 blocked 步骤)、排队外
	// 再来一个运行中受阻——恢复全部应覆盖多个任务。
	for _, task := range []struct {
		id      string
		blocked bool
	}{
		{"t-live-1", true}, {"t-dead", true}, {"t-live-2", false},
	} {
		if _, err := mgrCreate(srv, task.id); err != nil {
			t.Fatalf("建任务 %s: %v", task.id, err)
		}
		if task.blocked {
			seedBlockedStep(t, store, task.id, "s1", "模型连续失败(3 次): 503")
		}
	}
	if err := srv.Manager.StopBy("t-dead", map[string]any{"reason": "测试熔断"}); err != nil {
		t.Fatalf("熔断 t-dead: %v", err)
	}

	// ① 自动重开:保存并激活一个模型配置(配置写路径)→ blocked 步骤
	// 重开(非终态任务),终态任务不动。
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"r","format":"openai","base_url":"http://127.0.0.1:1","api_key":"k","model":"m"}`,
		http.StatusOK, new(map[string]any))
	post(t, h, http.MethodPost, "/api/models/activate", `{"primary":"r"}`, http.StatusOK, new(map[string]any))
	if st := stepStatusOf(t, store, "t-live-1", "s1"); st != "dispatched" {
		t.Fatalf("配置切换后受阻步骤应自动重开: %s", st)
	}
	if st := stepStatusOf(t, store, "t-dead", "s1"); st != "blocked" {
		t.Fatalf("终态任务的受阻步骤不得重开: %s", st)
	}

	// 再制造一个受阻步骤(运行中任务 2),走恢复全部。
	seedBlockedStep(t, store, "t-live-2", "s9", "模型连续失败(3 次): 402")
	var rec map[string]int
	post(t, h, http.MethodPost, "/api/tasks/recover-all", "{}", http.StatusOK, &rec)
	if rec["tasks"] != 1 || rec["steps"] != 1 {
		t.Fatalf("恢复全部应恰重开 1 任务 1 步骤(终态除外): %+v", rec)
	}
	if st := stepStatusOf(t, store, "t-live-2", "s9"); st != "dispatched" {
		t.Fatalf("恢复全部后步骤应回 dispatched: %s", st)
	}
	if st := stepStatusOf(t, store, "t-dead", "s1"); st != "blocked" {
		t.Fatalf("终态任务恢复全部也不碰: %s", st)
	}
	// 幂等:无受阻任务时恢复全部回零。
	post(t, h, http.MethodPost, "/api/tasks/recover-all", "{}", http.StatusOK, &rec)
	if rec["tasks"] != 0 || rec["steps"] != 0 {
		t.Fatalf("无受阻时恢复全部应回零: %+v", rec)
	}
}

// TestTasksBlockedBadge 任务列表受阻徽标:存在 blocked 步骤的非终态任务
// blocked=true + 原因;终态任务不标。
func TestTasksBlockedBadge(t *testing.T) {
	srv, h := newModelsTestServer(t)
	store := srv.Store
	if _, err := mgrCreate(srv, "t-badge"); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	seedBlockedStep(t, store, "t-badge", "s1", "模型连续失败(3 次): 503 upstream")
	if err := srv.Manager.StopBy("t-dead2", map[string]any{}); err == nil {
		t.Fatal("不存在的任务应报错")
	}
	if _, err := mgrCreate(srv, "t-fine"); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	seedBlockedStep(t, store, "t-fine", "s1", "x")
	if err := srv.Manager.StopBy("t-fine", map[string]any{"reason": "熔断"}); err != nil {
		t.Fatalf("熔断: %v", err)
	}

	var list []struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Blocked       bool   `json:"blocked"`
		BlockedReason string `json:"blocked_reason"`
	}
	get(t, h, "/api/tasks", http.StatusOK, &list)
	byID := map[string]struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Blocked       bool   `json:"blocked"`
		BlockedReason string `json:"blocked_reason"`
	}{}
	for _, v := range list {
		byID[v.ID] = v
	}
	if !byID["t-badge"].Blocked || !strings.Contains(byID["t-badge"].BlockedReason, "模型连续失败") {
		t.Fatalf("受阻任务应带徽标与原因: %+v", byID["t-badge"])
	}
	if byID["t-fine"].Blocked {
		t.Fatalf("终态任务不标受阻: %+v", byID["t-fine"])
	}
}

// mgrCreate 测试内建任务(经主 Manager,与面板同路径)。
func mgrCreate(srv *Server, id string) (*struct{}, error) {
	_, err := srv.Manager.CreateTask(context.Background(), id, "模型故障测试任务 "+id, rings.RoeUpdated{}, nil)
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// stepStatus 读步骤状态(缺步骤返回空串)。
func stepStatusOf(t *testing.T, store *rings.Store, task, step string) string {
	t.Helper()
	n, err := store.Node(context.Background(), task, step)
	if err != nil {
		return ""
	}
	return n.Status
}

// TestModelNameFreeFormDuplicateAndReveal P3-05c ② 三件:名称放开自定义
// (点号/中文可存);新增意图重名被拒(该名称已存在),编辑自身不受影响;
// 已存 key 明文查看走受控端点(明文只在响应里,审计落设置键累计次数,
// GET 恒打码)。
func TestModelNameFreeFormDuplicateAndReveal(t *testing.T) {
	srv, h := newModelsTestServer(t)

	const name = "a.b 主力.01"
	// ① 名称放开:含点号与中文可存。
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"`+name+`","create":true,"format":"openai","base_url":"http://127.0.0.1:8000/v1",
		"api_key":"sk-plain-abcdef123456","model":"demo"}`,
		http.StatusOK, &struct{}{})

	// ② 新增意图重名被拒;编辑自身(create=false)可保存。
	var dup struct {
		Error string `json:"error"`
	}
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"`+name+`","create":true,"format":"openai","base_url":"http://127.0.0.1:8000/v1",
		"api_key":"sk-other","model":"demo"}`,
		http.StatusConflict, &dup)
	if !strings.Contains(dup.Error, "该名称已存在") {
		t.Fatalf("重名应提示该名称已存在,得到 %q", dup.Error)
	}
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"`+name+`","create":false,"format":"openai","base_url":"http://127.0.0.1:8001/v1",
		"api_key":"","model":"demo2"}`,
		http.StatusOK, &struct{}{})

	// ③ GET 恒打码(无明文)。
	var view struct {
		Library []failover.MaskedConfig `json:"library"`
	}
	post(t, h, http.MethodGet, "/api/models", "", http.StatusOK, &view)
	var found bool
	for _, c := range view.Library {
		if c.Name == name {
			found = true
			if strings.Contains(c.APIKeyMask, "sk-plain-abcdef123456") {
				t.Fatalf("GET 打码视图不得含明文: %s", c.APIKeyMask)
			}
		}
	}
	if !found {
		t.Fatalf("库内应含自定义名称配置: %+v", view.Library)
	}

	// ④ 明文查看:响应携带明文;审计累计;再查一次次数 +1。
	var reveal struct {
		Name   string `json:"name"`
		APIKey string `json:"api_key"`
	}
	esc := url.PathEscape(name)
	post(t, h, http.MethodPost, "/api/models/"+esc+"/reveal-key", "", http.StatusOK, &reveal)
	if reveal.APIKey != "sk-plain-abcdef123456" {
		t.Fatalf("受控查看应回明文 key,得到 %q", reveal.APIKey)
	}
	e, ok, err := srv.Store.Setting(context.Background(), "model.keyview."+name)
	if err != nil || !ok {
		t.Fatalf("查看审计应落设置键: %v %v", ok, err)
	}
	var audit keyViewAudit
	if err := json.Unmarshal([]byte(e.Value), &audit); err != nil || audit.Count != 1 || audit.LastAt == "" {
		t.Fatalf("审计应为 count=1 带时刻: %v %+v", err, audit)
	}
	post(t, h, http.MethodPost, "/api/models/"+esc+"/reveal-key", "", http.StatusOK, &reveal)
	e, _, _ = srv.Store.Setting(context.Background(), "model.keyview."+name)
	_ = json.Unmarshal([]byte(e.Value), &audit)
	if audit.Count != 2 {
		t.Fatalf("二次查看审计应 count=2,得到 %d", audit.Count)
	}

	// ⑤ 查看不存在的配置:404。
	post(t, h, http.MethodPost, "/api/models/nope/reveal-key", "", http.StatusNotFound, &struct{}{})
}
