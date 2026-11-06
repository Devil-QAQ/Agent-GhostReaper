package server

// P4-46 ① 在途实时化与状态显示一致化的服务端测试:实时全景端点
// (/api/tasks/{id}/panorama,当前投影)与研判轮快照(overmindRounds 回放)
// 两视图分开——实时随事件跟随、快照按轮定格;② 观测页任务当前状态词
// 对齐任务状态七词口径。

import (
	"context"
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP446PanoramaLive 实时全景:行装配与快照同语义(领取次序、等前置
// 标注、covers 资产归属),数据取当前投影;轮次快照不随后续事件漂移
// ——同一时刻两视图可不同,误读绝迹(表头各自标注实时/截至时刻)。
func TestP446PanoramaLive(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	ctx := context.Background()
	const task = "p446-panorama"

	revs := func(ns ...int64) int64 { return ns[len(ns)-1] }
	// 批一:资产 a1 + 在途步骤 s1(排队,covers a1)、s2(排队,前置 s3)、
	// s3(high 反证,凭优先级排最前)。
	b1, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "实时全景"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, Title: "演示站", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "对 10.0.0.5:8080 做目录枚举,找未授权入口", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "用 s3 结果核对后台路径", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s3", NodeType: rings.NodeStep,
				Title: "反证(第 1 轮): 未授权访问", Status: "dispatched",
				Detail: map[string]any{"kind": "falsify", "priority": "high", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s2", ToNode: "s3"}},
	)
	if err != nil {
		t.Fatalf("铺批一失败: %v", err)
	}
	// 轮一(唯一轮):研判所读版本 = 批一末。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
		Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-p446-d1",
			EvaluatedRev: revs(b1...), QAccess: "首轮"}}); err != nil {
		t.Fatalf("铺轮一失败: %v", err)
	}

	rowsOf := func() []map[string]any {
		t.Helper()
		code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/panorama", "")
		if code != http.StatusOK {
			t.Fatalf("实时全景应 200: %d %v", code, out)
		}
		var rows []map[string]any
		pan, _ := out["panorama"].([]any)
		for _, p := range pan {
			m, _ := p.(map[string]any)
			rows = append(rows, m)
		}
		return rows
	}

	// 实时视图一(批一末态):三行在途,次序 = 领取次序(high 反证 s3 →
	// 同级创建版本 s1 → s2);s2 等前置;s1 带 covers 资产标题。
	rows := rowsOf()
	if len(rows) != 3 {
		t.Fatalf("实时全景应 3 行(排队 3),实际 %d: %v", len(rows), rows)
	}
	wantOrder := []string{"s3", "s1", "s2"}
	for i, w := range wantOrder {
		if rows[i]["id"] != w {
			t.Fatalf("实时全景次序应与领取次序一致 %v: %v", wantOrder, rows)
		}
	}
	if rows[0]["waiting"] == true {
		t.Fatalf("s3 无前置,不应带等前置标注: %v", rows[0])
	}
	if rows[2]["waiting"] != true {
		t.Fatalf("s2 应带等前置标注: %v", rows[2])
	}
	assets, _ := rows[1]["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("s1 应带 1 个目标归属资产: %v", rows[1])
	}
	if a, _ := assets[0].(map[string]any); a["id"] != "a1" || a["title"] != "演示站" {
		t.Fatalf("s1 目标归属应为 a1 演示站: %v", assets[0])
	}

	// 批二:s1 被领取(排队→执行中)、s3 反证收完 done(s2 前置完成)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s3", Status: "done"}},
	); err != nil {
		t.Fatalf("铺批二失败: %v", err)
	}

	// 实时视图二(当前投影):s1 执行中、s2 排队且不再等前置、s3 已完
	// 不进表——新事件落地即跟随。
	rows = rowsOf()
	if len(rows) != 2 {
		t.Fatalf("实时全景应 2 行(执行 1 + 排队 1),实际 %d: %v", len(rows), rows)
	}
	if rows[0]["id"] != "s1" || rows[0]["status"] != "running" {
		t.Fatalf("实时全景 s1 应 running: %v", rows)
	}
	if rows[1]["id"] != "s2" || rows[1]["waiting"] == true {
		t.Fatalf("实时全景 s2 应排队且前置已完成不再等: %v", rows)
	}

	// 轮次快照(该轮历史卡数据面)不随后续事件漂移:最新轮 panorama 仍
	// 定格批一(s1 dispatched)——与实时视图分开,前端表头各自标注。
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != http.StatusOK {
		t.Fatalf("主宰状态应 200: %d", code)
	}
	rounds, _ := out["rounds"].([]any)
	if len(rounds) != 1 {
		t.Fatalf("应恰 1 轮研判: %v", rounds)
	}
	pan, _ := rounds[0].(map[string]any)["panorama"].([]any)
	frozen := map[string]string{}
	for _, p := range pan {
		m, _ := p.(map[string]any)
		frozen[m["id"].(string)] = m["status"].(string)
	}
	if frozen["s1"] != "dispatched" || frozen["s3"] != "dispatched" || len(frozen) != 3 {
		t.Fatalf("轮次快照应定格批一(s1/s3 仍 dispatched、3 行),实际: %v", frozen)
	}
}

// TestP446PanoramaLiveEmpty 无在途步骤与任务不存在:空数组与 404。
func TestP446PanoramaLiveEmpty(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	if _, err := srv.Store.Append(t.Context(), "p446-empty",
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "空实时全景"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "指纹识别", Status: "done"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	code, out := callJSON(t, mux, "GET", "/api/tasks/p446-empty/panorama", "")
	if code != http.StatusOK {
		t.Fatalf("实时全景应 200: %d", code)
	}
	if pan, _ := out["panorama"].([]any); len(pan) != 0 {
		t.Fatalf("无在途步骤时实时全景应为空数组: %v", out)
	}
	code, _ = callJSON(t, mux, "GET", "/api/tasks/p446-none/panorama", "")
	if code != http.StatusNotFound {
		t.Fatalf("任务不存在应 404: %d", code)
	}
}

// TestP446ObsTaskNowSevenWords 观测页任务当前状态词对齐任务状态七词
// 口径(P4-46 ②):paused 并入「冬眠中」——拒绝后任务暂停的走向视图
// 当前词为冬眠中(原「已暂停」旧词废除)。
func TestP446ObsTaskNowSevenWords(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p446-obs"
	if _, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "观测七词"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		denyEvent("ap-p446", "s1", "u1", "直连对照复测"),
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "paused"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	out := getIntercepts(t, mux, "?task="+task)
	list, _ := out["rules"].([]any)
	if len(list) != 1 {
		t.Fatalf("应恰一条规则聚合: %v", out["rules"])
	}
	follow := list[0].(map[string]any)["recent"].([]any)[0].(map[string]any)["follow"].(map[string]any)
	if follow["outcome"] != "paused" {
		t.Fatalf("走向应为 paused: %v", follow)
	}
	if follow["task_now"] != "冬眠中" {
		t.Fatalf("任务当前状态词应为 冬眠中(七词口径): %v", follow["task_now"])
	}
	// 词表逐格:9 后端态与七词取态一致(web/lib/task-status.mjs 同款)。
	for status, want := range map[string]string{
		"created": "排队中", "queued": "排队中", "started": "运行中", "resumed": "运行中",
		"paused": "冬眠中", "hibernated": "冬眠中", "checkpointed": "冬眠中",
		"settling": "收尾中", "stopped": "已停止", "completed": "已完成",
	} {
		if obsTaskNowWords[status] != want {
			t.Fatalf("obsTaskNowWords[%s] = %q,应为 %q", status, obsTaskNowWords[status], want)
		}
	}
}
