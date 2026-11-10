package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-65(问题清单 #82 A⑨⑩)读侧优先链类级测试:
//   - ⑨ 研判卡叙述与五问行:overmind.record_polish 按 rec_rev 逐轮匹配,
//     字段级优先(未生成字段退研判记录原文),无校对稿的轮退原文;
//   - ⑩ 意图/步骤标题:node_polish 优先于节点标题——研判卡派发 chip、
//     在途全景(历史快照与实时视图同装配)、虫群列表与排队条目、图
//     节点卡(年轮图/进度图/链路图共用端点);
//   - 机器消费对账:节点原文与任务包不受显示稿影响。

func p465Setup(t *testing.T) (*Server, *http.ServeMux, string) {
	t.Helper()
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-p465"
	if _, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P4-65 读侧验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: "a-1", NodeType: rings.NodeAsset, Title: "10.0.0.5:8080", AssetClass: "host"}},
		// 第 1 轮研判(rev 3):带全部五问与叙述,校对稿部分字段覆盖。
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-t-p465-d1",
				EvaluatedRev: 3, QAccess: "新身份可达 a-1 的管理后台", QUnlock: "无新解锁",
				Note: "下一轮验证口令通用性", Dispatched: 1}},
		// 窗口:派发 s1(dispatched——排队条目与全景行同覆盖)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "对 10.0.0.5:8080 做目录探测的原始意图", Status: "dispatched"}},
		// 第 2 轮研判(rev 6):无校对稿——退原文对照。
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-t-p465-d2",
				EvaluatedRev: 6, QOverturn: "无新推翻"}},
	); err != nil {
		t.Fatalf("seed 任务失败: %v", err)
	}
	return srv, mux, task
}

// p465Polish 落第 1 轮研判记录的校对稿(锚定该记录的事件版本 rev 3)
// 与 s1 的标题校对稿。
func p465Polish(t *testing.T, srv *Server, task string, recRev int64) {
	t.Helper()
	if _, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventOvermindPolish, Actor: "overmind",
			Payload: rings.OvermindPolish{RecRev: recRev,
				QAccess: "凭 s2 拿到的口令可以登录 10.0.0.5:8080 的管理后台",
				Note:    "下一轮验证该口令在 10.0.0.6 的同一套后台是否通用"}},
		rings.Event{Kind: rings.EventStepTitle, Actor: "overmind",
			Payload: rings.StepTitlePolish{StepID: "s1", Title: "对 10.0.0.5:8080 做目录枚举,找未授权入口"}},
	); err != nil {
		t.Fatalf("落校对稿失败: %v", err)
	}
}

func TestP465OvermindRoundsPreferPolish(t *testing.T) {
	srv, mux, task := p465Setup(t)
	// 第 1 轮研判记录的事件版本 = rev 3(created/asset 在前)。
	p465Polish(t, srv, task, 3)
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != 200 {
		t.Fatalf("GET /overmind = %d: %v", code, out)
	}
	rounds, _ := out["rounds"].([]any)
	if len(rounds) != 2 {
		t.Fatalf("研判轮数 = %d, want 2", len(rounds))
	}
	r1 := rounds[0].(map[string]any)
	// ⑨ 字段级优先:校对稿字段覆盖、未生成字段(QUnlock)退原文。
	if r1["q_access"] != "凭 s2 拿到的口令可以登录 10.0.0.5:8080 的管理后台" {
		t.Fatalf("第 1 轮 q_access 应优先校对稿: %v", r1["q_access"])
	}
	if r1["q_unlock"] != "无新解锁" {
		t.Fatalf("未生成校对稿的字段应退研判记录原文: %v", r1["q_unlock"])
	}
	if r1["note"] != "下一轮验证该口令在 10.0.0.6 的同一套后台是否通用" {
		t.Fatalf("第 1 轮 note 应优先校对稿: %v", r1["note"])
	}
	// ⑩ 派发 chip 标题优先校对稿。
	steps1, _ := r1["steps"].([]any)
	if len(steps1) != 1 || steps1[0].(map[string]any)["title"] != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
		t.Fatalf("派发 chip 标题应优先校对稿: %v", steps1)
	}
	// 在途全景行:第 1 轮快照定格于该轮时刻(s1 尚未派发,空为历史语义);
	// 第 2 轮(EvaluatedRev=6,s1 已在图上)终态定格快照行标题优先校对稿。
	if pano, _ := r1["panorama"].([]any); len(pano) != 0 {
		t.Fatalf("第 1 轮快照不应吃到其后派发的步骤: %v", pano)
	}
	// 第 2 轮无校对稿:五问全退原文。
	r2 := rounds[1].(map[string]any)
	if r2["q_overturn"] != "无新推翻" {
		t.Fatalf("第 2 轮应退原文: %v", r2["q_overturn"])
	}
	pano2, _ := r2["panorama"].([]any)
	if len(pano2) == 0 || pano2[0].(map[string]any)["title"] != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
		t.Fatalf("第 2 轮全景行标题应优先校对稿: %v", pano2)
	}
}

func TestP465OvermindRoundsStockFallback(t *testing.T) {
	_, mux, task := p465Setup(t)
	// 不落任何校对稿:全部退原文(存量兼容)。
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != 200 {
		t.Fatalf("GET /overmind = %d: %v", code, out)
	}
	rounds, _ := out["rounds"].([]any)
	r1 := rounds[0].(map[string]any)
	if r1["q_access"] != "新身份可达 a-1 的管理后台" {
		t.Fatalf("无校对稿应退研判记录原文: %v", r1["q_access"])
	}
	steps1, _ := r1["steps"].([]any)
	if steps1[0].(map[string]any)["title"] != "对 10.0.0.5:8080 做目录探测的原始意图" {
		t.Fatalf("无标题校对稿应退节点原文: %v", steps1)
	}
}

func TestP465UnitsAndGraphPreferTitlePolish(t *testing.T) {
	srv, mux, task := p465Setup(t)
	p465Polish(t, srv, task, 3)
	// 在途全景实时视图。
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/panorama", "")
	if code != 200 {
		t.Fatalf("GET /panorama = %d: %v", code, out)
	}
	pano, _ := out["panorama"].([]any)
	if len(pano) != 1 || pano[0].(map[string]any)["title"] != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
		t.Fatalf("实时全景行标题应优先校对稿: %v", pano)
	}
	// 虫群列表:running 单位行步骤标题优先(排队条目同装配路径,此处
	// 以 running 形态断言——UnitRow.StepTitle 同一字段)。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != 200 {
		t.Fatalf("GET /units = %d: %v", code, out)
	}
	units, _ := out["units"].([]any)
	found := false
	for _, u := range units {
		row := u.(map[string]any)
		if row["step_id"] == "s1" {
			found = true
			if row["step_title"] != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
				t.Fatalf("单位行步骤标题应优先校对稿: %v", row["step_title"])
			}
		}
	}
	if !found {
		t.Fatalf("s1 行应在单位列表(无会话时以排队条目形态,标题同字段): %v", units)
	}
	// 图节点卡(年轮图/进度图/链路图共用端点):step 节点标题优先。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/graph", "")
	if code != 200 {
		t.Fatalf("GET /graph = %d: %v", code, out)
	}
	nodes, _ := out["nodes"].([]any)
	for _, n := range nodes {
		nd := n.(map[string]any)
		if nd["id"] == "s1" {
			if nd["title"] != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
				t.Fatalf("图节点卡标题应优先校对稿: %v", nd["title"])
			}
		}
	}
	// 机器消费对账:节点原文一字不动(node 端点是审计面,标题为原文)。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/nodes/s1", "")
	if code != 200 {
		t.Fatalf("GET /nodes/s1 = %d: %v", code, out)
	}
	if out["title"] != "对 10.0.0.5:8080 做目录探测的原始意图" {
		t.Fatalf("节点详情(审计面)应保持节点原文: %v", out["title"])
	}
}
