package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-05 舵杆面板 API:steer_work / kill_work / 指令审计流 ----

// TestServerTaskCreateAnchorField 建任务表单的重锚定间隔随 created 事件
// 透出(事件流锚点,缺省默认 10);坏值 400 零残留。
func TestServerTaskCreateAnchorField(t *testing.T) {
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
	h := srv.Handler()
	ctx := context.Background()

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],
		"id":"anchor-opt","title":"重锚定间隔随任务透出","anchor_every_steps":3}`,
		http.StatusOK, &created)
	detail, err := store.CreatedDetail(ctx, "anchor-opt")
	if err != nil {
		t.Fatalf("读 created detail 失败: %v", err)
	}
	if n, _ := detail["anchor_every_steps"].(float64); int(n) != 3 {
		t.Fatalf("anchor_every_steps 应随 created 事件透出: %+v", detail)
	}

	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],
		"id":"anchor-default","title":"缺省走默认十步"}`,
		http.StatusOK, &created)
	detail, err = store.CreatedDetail(ctx, "anchor-default")
	if err != nil {
		t.Fatalf("读 created detail 失败: %v", err)
	}
	if _, ok := detail["anchor_every_steps"]; ok {
		t.Fatalf("缺省不应写入锚间隔(默认在 guard 归一): %+v", detail)
	}

	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],
		"id":"anchor-bad","title":"坏值","anchor_every_steps":1001}`,
		http.StatusBadRequest, &created)
	if _, err := store.Task(ctx, "anchor-bad"); err == nil {
		t.Fatalf("坏请求应零残留")
	}
}

// TestServerSteerEndpoints 舵杆端点契约:直连 POST /steer 已删(P3-14b ③,
// 面板插话 /interject/messages 唯一入口——404 证明);GET /steers 审计流
// 可见(插话改向内部经舵杆入账);kill-work 未接执行器时如实拒绝。
func TestServerSteerEndpoints(t *testing.T) {
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
	h := srv.Handler()
	ctx := context.Background()

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"steer-api","title":"舵杆端点契约"}`,
		http.StatusOK, &created)
	// 种一个 running 步骤(执行器未装配,直接事件置 running——端点
	// 只做投影校验与舵杆下达)。
	if _, err := store.Append(ctx, "steer-api",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测", Status: "running"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1", Type: rings.SwarmRecSessionStarted}},
	); err != nil {
		t.Fatalf("种步骤失败: %v", err)
	}

	// 直连 /steer 已删除:任何任务/参数形态一律 404(无此端点)。
	post(t, h, http.MethodPost, "/api/tasks/steer-api/steer", `{"step_id":"s1","message":"x"}`,
		http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/none/steer", `{"step_id":"s1","message":"x"}`,
		http.StatusNotFound, nil)

	// 审计流读侧不受删除影响:插话改向内部经舵杆入账,这里直接走同一
	// 舵杆实例种一条排队指令。
	if _, err := srv.helmOf().SteerWork(ctx, "steer-api", "s1", "改查 9090 端口", "creator"); err != nil {
		t.Fatalf("舵杆排队失败: %v", err)
	}
	var steers []map[string]any
	get(t, h, "/api/tasks/steer-api/steers", http.StatusOK, &steers)
	if len(steers) != 1 || steers[0]["command"] != "steer" || steers[0]["state"] != "queued" ||
		steers[0]["by"] != "creator" || !strings.Contains(steers[0]["message"].(string), "9090") {
		t.Fatalf("舵杆审计流不符: %+v", steers)
	}

	// kill:server 默认舵杆未接执行器(无止损钩子),如实拒绝。
	var killReply map[string]any
	post(t, h, http.MethodPost, "/api/tasks/steer-api/kill-work",
		`{"step_id":"s1","reason":"方向错误"}`, http.StatusBadRequest, &killReply)
	if !strings.Contains(killReply["error"].(string), "无在跑单位") {
		t.Fatalf("未接执行器的 kill 应如实拒绝: %+v", killReply)
	}
}

// TestServerConfigDeviation 设置页读模型含纠偏纵深装配(重锚定默认间隔
// 与心跳节奏——只读视图)。
func TestServerConfigDeviation(t *testing.T) {
	srv := &Server{Config: DefaultConfig()}
	if srv.Config.Deviation.AnchorEverySteps != swarm.DefaultAnchorEverySteps {
		t.Fatalf("重锚定默认间隔应透出: %+v", srv.Config.Deviation)
	}
	if srv.Config.Deviation.HeartbeatSeconds != 600 {
		t.Fatalf("心跳间隔默认应 600s: %+v", srv.Config.Deviation)
	}
}
