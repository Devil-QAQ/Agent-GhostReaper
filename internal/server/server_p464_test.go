package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

func p464Setup(t *testing.T) (http.Handler, *rings.Store, string, string) {
	t.Helper()
	h, store, task, hostID := factsSetup(t)
	// 事实文本包装(f-cred 与 f-data;f-plain 不包 → 退原文对照)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{
			FactID: "f-cred", Title: "192.0.2.2 的备份文件里带有后台账号与明文口令",
			Statement: "备份文件没有访问限制,里面的后台口令拿到就能直接登录"}},
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{
			FactID: "f-data", Title: "192.0.2.2 的用户数据可以整表导出",
			Statement: "用户表可以未登录整表导出,共 812 条记录"}},
	)
	return h, store, task, hostID
}

func TestP464FactsListPrefersNodePolish(t *testing.T) {
	h, _, task, _ := p464Setup(t)
	var body struct {
		Facts []struct {
			ID        string `json:"id"`
			Statement string `json:"statement"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)
	got := map[string]string{}
	for _, f := range body.Facts {
		got[f.ID] = f.Statement
	}
	if !startsWith(got["f-cred"], "备份文件没有访问限制") {
		t.Fatalf("事实描述应优先确认批次包装: %q", got["f-cred"])
	}
	if !startsWith(got["f-data"], "用户表可以未登录整表导出") {
		t.Fatalf("f-data 应读自身包装: %q", got["f-data"])
	}
	if got["f-plain"] != "8080 端口运行 nginx 1.18.0" {
		t.Fatalf("未包装事实应退节点原文: %q", got["f-plain"])
	}
}

func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

func TestP464GraphNodeTitlePolish(t *testing.T) {
	h, _, task, _ := p464Setup(t)
	var g struct {
		Nodes []struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"nodes"`
	}
	get(t, h, "/api/tasks/"+task+"/graph", http.StatusOK, &g)
	titles := map[string]string{}
	for _, n := range g.Nodes {
		titles[n.ID] = n.Title
	}
	if titles["f-cred"] != "192.0.2.2 的备份文件里带有后台账号与明文口令" {
		t.Fatalf("全景图/任务投影节点卡事实标题应为短句校对稿: %q", titles["f-cred"])
	}
	if titles["f-plain"] != "8080 端口运行 nginx 1.18.0" {
		t.Fatalf("未包装节点标题应保持原文: %q", titles["f-plain"])
	}
}

// (TestP464ClaimDetailRoundHumansAndRetestNote 的逐轮人话断言已随链路
// 退役删除,P5-03③;复测备注校对稿链路保留(p464 rings 侧覆盖)。)

func TestP464NodeDetailSupplements(t *testing.T) {
	h, store, task, hostID := p464Setup(t)
	// 步骤 + 单位会话(投影行)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-sup", NodeType: rings.NodeStep, Title: "探测管理入口", Status: "done",
			Detail: map[string]any{"goal_id": "g1", "expected_observation": "入口清单",
				"action_level": "只读探测", "priority": "high", "kind": "falsify",
				"raw_extra": "未映射键"}}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u7", Payload: rings.SwarmRecord{
			Type: rings.SwarmRecSessionStarted, StepID: "s-sup", UnitID: "u7", TranscriptID: "tr-sup"}},
	)
	var sv struct {
		UnitID       string         `json:"unit_id"`
		TranscriptID string         `json:"transcript_id"`
		Detail       map[string]any `json:"detail"`
	}
	get(t, h, "/api/tasks/"+task+"/nodes/s-sup", http.StatusOK, &sv)
	if sv.UnitID != "u7" || sv.TranscriptID != "tr-sup" {
		t.Fatalf("步骤应补发执行单位与会话记录: %+v", sv)
	}
	if sv.Detail["raw_extra"] != "未映射键" {
		t.Fatalf("detail 原文应随行: %+v", sv.Detail)
	}
	// 事实:关联资产 + 文本包装。
	var fv struct {
		Assets            []map[string]string `json:"assets"`
		PolishedTitle     string              `json:"polished_title"`
		PolishedStatement string              `json:"polished_statement"`
	}
	get(t, h, "/api/tasks/"+task+"/nodes/f-cred", http.StatusOK, &fv)
	if len(fv.Assets) != 1 || fv.Assets[0]["id"] != hostID || fv.Assets[0]["title"] != "192.0.2.2" {
		t.Fatalf("事实应补发关联资产: %+v", fv.Assets)
	}
	if fv.PolishedTitle == "" || fv.PolishedStatement == "" {
		t.Fatalf("事实应补发文本包装: %+v", fv)
	}
}
