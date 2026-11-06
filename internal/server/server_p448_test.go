package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-48(问题清单 #67):操作者深入指令(面板侧) ----
// ① POST /dive:记账(actor=creator)并唤醒;锚定限发现/事实;任务
//    终态拒收(409 人话);同一节点可多次深入;
// ② 发现详情与事实详情(节点详情)带深入禁用态(终态 = "任务已结束");
// ③ 研判卡触发行:深入指令确定性装配为"操作者深入指令:节点 x,方向:y"。

// p448Server 建面板后端与一个带事实/发现铺垫的任务(未终态)。
func p448Server(t *testing.T) (http.Handler, *rings.Store, string) {
	t.Helper()
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
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"深入验证"}`, http.StatusOK, &created)
	task := created["id"]
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "事实:登录口未锁", Status: "confirmed",
			Detail: map[string]any{"evidence": "实测", "unit_id": "u1"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "发现:SQL 注入", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "impact": "可读库", "vulnclass": "SQL注入"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-1", ToNode: "s1"}},
	)
	return h, store, task
}

// TestOperatorDiveAPI 深入指令端点:记账+唤醒面、锚定类型限发现/事实、
// 终态拒收、多次深入、详情禁用态、研判卡触发行。
func TestOperatorDiveAPI(t *testing.T) {
	h, store, task := p448Server(t)

	// 好路径:事实带方向 → 200,事件 actor=creator 落账。
	var resp map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"f-1","direction":"继续暴力破解"}`, http.StatusOK, &resp)
	if resp["node_id"] != "f-1" {
		t.Fatalf("回执锚定节点错误: %v", resp)
	}
	// 同一节点第二次深入:每次新事件,各自可溯。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"f-1"}`, http.StatusOK, &resp)
	// 成果锚定 + 方向留空。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"tc-1"}`, http.StatusOK, &resp)

	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var dives int
	for _, ev := range evs {
		if ev.Kind != rings.EventOperatorDive {
			continue
		}
		dives++
		if ev.Actor != "creator" {
			t.Fatalf("深入指令 actor 应为 creator,实际 %s", ev.Actor)
		}
		var p rings.OperatorDive
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.NodeID != "f-1" && p.NodeID != "tc-1" {
			t.Fatalf("锚定节点异常: %s", p.NodeID)
		}
	}
	if dives != 3 {
		t.Fatalf("应记 3 条深入指令,实际 %d", dives)
	}

	// 校验面:任务不存在 / 节点不存在 / 锚定非发现事实 / 缺节点 ID。
	post(t, h, http.MethodPost, "/api/tasks/no-such/dive", `{"node_id":"f-1"}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"f-404"}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"s1"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{}`, http.StatusBadRequest, nil)

	// 详情禁用态:未终态任务两处详情均为空(可用)。
	var claimDetail map[string]any
	get(t, h, "/api/tasks/"+task+"/claims/tc-1", http.StatusOK, &claimDetail)
	if v, ok := claimDetail["dive_disabled"]; ok && v != "" {
		t.Fatalf("未终态任务发现详情不应禁用深入: %v", v)
	}
	var nodeDetail map[string]any
	get(t, h, "/api/tasks/"+task+"/nodes/f-1", http.StatusOK, &nodeDetail)
	if v, ok := nodeDetail["dive_disabled"]; ok && v != "" {
		t.Fatalf("未终态任务事实详情不应禁用深入: %v", v)
	}

	// 研判卡触发行:落一条引用深入指令 revision 的研判记录,触发行
	// 确定性装配(方向留空如实呈"留空")。
	diveRev := int64(0)
	for _, ev := range evs {
		if ev.Kind == rings.EventOperatorDive {
			var p rings.OperatorDive
			if json.Unmarshal(ev.Payload, &p) == nil && p.Direction != "" {
				diveRev = ev.Revision
			}
		}
	}
	if diveRev == 0 {
		t.Fatal("找不到带方向的深入指令")
	}
	appendEvents(t, store, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
		Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, EvaluatedRev: diveRev - 1,
			TriggerRevisions: []int64{diveRev}, QAccess: "无"}})
	var omStatus map[string]any
	get(t, h, "/api/tasks/"+task+"/overmind", http.StatusOK, &omStatus)
	found := false
	rounds, _ := omStatus["rounds"].([]any)
	for _, r := range rounds {
		rm, _ := r.(map[string]any)
		for _, tr := range toStrSlice(rm["triggers"]) {
			if tr == "操作者深入指令：节点 f-1，方向：继续暴力破解" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("研判卡触发行缺失: %+v", omStatus["rounds"])
	}

	// 终态:停止任务后 409"任务已结束"、两处详情禁用态逐字。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/stop", `{}`, http.StatusOK, nil)
	var errBody map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/dive", `{"node_id":"f-1"}`, http.StatusConflict, &errBody)
	if errBody["error"] != "任务已结束" {
		t.Fatalf("终态拒收文案应为 任务已结束: %v", errBody)
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-1", http.StatusOK, &claimDetail)
	if claimDetail["dive_disabled"] != "任务已结束" {
		t.Fatalf("终态发现详情禁用态错误: %v", claimDetail["dive_disabled"])
	}
	get(t, h, "/api/tasks/"+task+"/nodes/f-1", http.StatusOK, &nodeDetail)
	if nodeDetail["dive_disabled"] != "任务已结束" {
		t.Fatalf("终态事实详情禁用态错误: %v", nodeDetail["dive_disabled"])
	}
}

// toStrSlice any → []string(triggers 断言用)。
func toStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
