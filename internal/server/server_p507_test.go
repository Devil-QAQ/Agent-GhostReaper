package server

// P5-07 类级验收(规则 15):意图台账投影/事实页可见口径/事实追问线/
// 影响列点贯通/报告与导出可见口径——多形态样本集断言,不以单一个案
// 替代类级覆盖。

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func p507Seed(t *testing.T, h http.Handler, store *rings.Store) (task string) {
	t.Helper()
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],
		"title":"P5-07 台账","assets":[{"class":"host","title":"192.0.2.1"}]}`,
		http.StatusOK, &created)
	task = created["id"]

	// 步骤(意图:提出即落页,不限状态)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "枚举备份文件", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "验证后台弱口令", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "旁路探测", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s4", NodeType: rings.NodeStep, Title: "无关意图", Status: "dispatched"}},
		// 机器拉起的补考/重验步骤(kind=retake)不是意图,不入台账。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "arbiter", Payload: rings.NodeAdded{
			NodeID: "s9", NodeType: rings.NodeStep, Title: "补做证据", Status: "dispatched",
			Detail: map[string]any{"kind": "retake"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "arbiter", Payload: rings.NodeAdded{
			NodeID: "s9b", NodeType: rings.NodeStep, Title: "重验", Status: "dispatched",
			Detail: map[string]any{"kind": "retake", "reverify": true}}},
		// 事实:s1 产出一确认一候选(候选未确认不标)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "备份文件含后台账号 admin", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-2", NodeType: rings.NodeFact, Title: "疑似内网映射", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-2"}},

		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f-3", NodeType: rings.NodeFact, Title: "旁站返回相同服务标识", Status: "confirmed",
			Detail: map[string]any{"evidence": "两处响应含相同服务标识"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u3", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s3", ToNode: "f-3"}},
		// 漏洞:tc-1(candidate 起步、裁决确认——真实形态,确认时刻落
		// nodes 投影)、tc-2(边血缘,证据不足)、tc-3(被推翻,不标)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "后台弱口令可登录", Status: rings.TestClaimCandidate,
			Detail: map[string]any{"step_id": "s2", "unit_id": "u2"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-1", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-2", NodeType: rings.NodeTestClaim, Title: "注入可拖库", Status: rings.TestClaimInsufficient,
			Detail: map[string]any{"unit_id": "u2"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-2", ToNode: "s2"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-3", NodeType: rings.NodeTestClaim, Title: "老版本 RCE", Status: rings.TestClaimRefuted,
			Detail: map[string]any{"step_id": "s2", "unit_id": "u2"}}},
	)
	return task
}

func TestP507IntentsLedger(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)

	var rows []struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		AddedAt    string `json:"added_at"`
		CreatedRev int64  `json:"created_rev"`
		Outcomes   []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Word string `json:"word"`
		} `json:"outcomes"`
	}
	get(t, h, "/api/tasks/"+task+"/intents", http.StatusOK, &rows)

	if len(rows) != 4 {
		t.Fatalf("意图应 4 条(提出即落页,不限状态),得 %d: %+v", len(rows), rows)
	}
	byStep := map[string]int{}
	for i, r := range rows {
		byStep[r.ID] = i
		if r.Title == "" || r.AddedAt == "" || r.CreatedRev == 0 {
			t.Errorf("意图 %s 行元素缺失(编号/摘要/时刻): %+v", r.ID, r)
		}
	}
	// 补考/重验步骤(kind=retake)不是意图方向,不入台账。
	if _, ok := byStep["s9"]; ok {
		t.Error("补考步骤 s9 不应入意图台账(机器拉起,非主宰意图)")
	}
	if _, ok := byStep["s9b"]; ok {
		t.Error("重验步骤 s9b 不应入意图台账(机器拉起,非主宰意图)")
	}
	mark := func(step string) map[string]string {
		out := map[string]string{}
		for _, o := range rows[byStep[step]].Outcomes {
			out[o.ID] = o.Word + "/" + o.Kind
		}
		return out
	}
	// s1:已确认事实标记;候选事实 f-2 不标。
	if m := mark("s1"); len(m) != 1 || m["f-1"] != "事实/fact" {
		t.Errorf("s1 下文应恰一条(→ 事实 f-1;候选不标): %+v", m)
	}
	// s2:两种血缘(字段/边)各归位;证据不足标记、被推翻不标。
	m2 := mark("s2")
	if len(m2) != 2 || m2["tc-1"] != "漏洞/testclaim" || m2["tc-2"] != "漏洞/testclaim" {
		t.Errorf("s2 下文不符(tc-1 字段血缘+tc-2 边血缘;被推翻 tc-3 不标): %+v", m2)
	}
	if m := mark("s3"); len(m) != 1 || m["f-3"] != "事实/fact" {
		t.Errorf("s3 下文应经假设链归位(→ 事实 f-3): %+v", m)
	}
	// s4:无产出不携带 outcomes 字段。
	if out := rows[byStep["s4"]].Outcomes; len(out) != 0 {
		t.Errorf("s4 无产出应零标记(前端渲染淡灰不可点的\"—\"),得 %+v", out)
	}

	// 404:任务不存在。
	get(t, h, "/api/tasks/t-none/intents", http.StatusNotFound, &struct{}{})
}

// TestP507ClaimsListLandedTime claims 列表落页时刻口径:已确认声明带
// confirmed_at(漏洞页排序键);证据不足无确认时刻(退立账时刻,前端
// 归一)。
func TestP507ClaimsListLandedTime(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)

	var rows []struct {
		ID          string `json:"id"`
		AddedAt     string `json:"added_at"`
		ConfirmedAt string `json:"confirmed_at"`
	}
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &rows)
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.ID] = i
	}
	if r := rows[byID["tc-1"]]; r.AddedAt == "" || r.ConfirmedAt == "" {
		t.Errorf("已确认声明应带立账+确认时刻: %+v", r)
	}
	if r := rows[byID["tc-2"]]; r.AddedAt == "" || r.ConfirmedAt != "" {
		t.Errorf("证据不足声明应有立账时刻、无确认时刻(前端退立账): %+v", r)
	}
}

func TestP507FactsVisibleOnly(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)

	var body struct {
		Facts []struct {
			ID string `json:"id"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)
	saw := map[string]bool{}
	for _, f := range body.Facts {
		saw[f.ID] = true
	}
	if len(body.Facts) != 2 || !saw["f-1"] || !saw["f-3"] {
		t.Fatalf("事实页应恰 2 条已确认(f-1/f-3),得 %+v", body.Facts)
	}
	for _, banned := range []string{"f-2"} {
		if saw[banned] {
			t.Errorf("候选事实 %s 不应入列(未确认,账本留痕)", banned)
		}
	}
}

// TestP507FactFollowupLine 事实追问线:锚定条目问答端点受理事实
// (与漏洞同一线);非条目锚(步骤)拒收;事实详情带追问摘要。
func TestP507FactFollowupLine(t *testing.T) {
	srv, h, closer := p507ServerWithShepherd(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, srv.Store)

	// 事实锚定首问:入队即回执(已排队),读视图可见。
	var sent struct {
		MessageID string `json:"message_id"`
		Round     int    `json:"round"`
	}
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/f-1/messages",
		`{"text":"这条事实的凭据还能用吗"}`, http.StatusOK, &sent)
	if sent.MessageID == "" || sent.Round != 1 {
		t.Fatalf("事实追问入队回执不符: %+v", sent)
	}
	var view struct {
		Round    int `json:"round"`
		Messages []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	get(t, h, "/api/tasks/"+task+"/shepherd/claim/f-1", http.StatusOK, &view)
	if view.Round != 1 || len(view.Messages) == 0 || view.Messages[0].ID != sent.MessageID {
		t.Fatalf("事实追问读视图不符: %+v", view)
	}

	// 事实详情追问摘要(追问按钮数据面,与漏洞详情同源)。
	var node map[string]any
	get(t, h, "/api/tasks/"+task+"/nodes/f-1", http.StatusOK, &node)
	fu, _ := node["followup"].(map[string]any)
	if fu == nil || fu["round"].(float64) != 1 || fu["rounds"].(float64) != 1 || fu["questions"].(float64) != 1 {
		t.Fatalf("事实详情追问摘要不符: %+v", fu)
	}

	// 非条目锚(步骤)拒收:追问只锚定漏洞与事实。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/shepherd/claim/s1/messages",
		`{"text":"?"}`, http.StatusNotFound, &struct{}{})
}

// TestP507ImpactNoteFlow 影响列点贯通(⑦b):交卷带多行列点 → 详情
// impact_note 原文 / 报告按列点逐行 / csv 影响列 / json impact 字段;
// 机器判定不受影响(确认裁决照常落)。
func TestP507ImpactNoteFlow(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)

	note := "admin 密码可直登后台\n可导出全部用户手机号\n(猜测)或可写 webshell"
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecSubmit,
			Summary: "后台弱口令可登录", Severity: "high",
			EvidenceRefs:  []string{"ms-x.1"},
			ProvingMarker: "admin", CountercheckNote: "重放核对一次",
			ImpactNote: note,
		}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecVerdict, Decision: rings.VerdictConfirm, Satisfied: true,
			Conditions: []rings.ArbiterConditionResult{{Kind: "evidence_traceable", Satisfied: true, Actual: 1, Required: 1}},
		}},
	)

	var detail struct {
		ImpactNote string `json:"impact_note"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-1", http.StatusOK, &detail)
	if detail.ImpactNote != note {
		t.Fatalf("详情影响列点应原文透出:\n got  %q\n want %q", detail.ImpactNote, note)
	}

	// 报告:多行列点逐行编号(单条形态另有覆盖——本例 3 行)。
	md := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report", "").Body.String()
	for _, want := range []string{
		"- 影响(交卷申报):",
		"  1. admin 密码可直登后台",
		"  2. 可导出全部用户手机号",
		"  3. (猜测)或可写 webshell",
		"- 判定:机器核验已确认(三查全部通过)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("报告缺影响列点行 %q", want)
		}
	}

	// csv/json 导出:影响列/impact 字段携带列点。
	csvBody := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report/export?format=csv", "").Body.String()
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvBody, "\ufeff"))).ReadAll()
	if err != nil || len(recs) < 2 {
		t.Fatalf("csv 解析失败: %v rows=%d", err, len(recs))
	}
	hit := false
	for _, row := range recs {
		if len(row) > 6 && strings.Contains(row[6], "admin 密码可直登后台") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("csv 影响列应含列点原文")
	}
	jsonBody := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report/export?format=json", "").Body.String()
	var jstruct struct {
		Findings []struct {
			ID     string `json:"id"`
			Impact string `json:"impact"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &jstruct); err != nil {
		t.Fatalf("json 解析失败: %v", err)
	}
	found := false
	for _, f := range jstruct.Findings {
		if f.ID == "tc-1" {
			found = true
			if f.Impact != note {
				t.Errorf("json impact 应为列点原文: %q", f.Impact)
			}
		}
	}
	if !found {
		t.Error("json 应含 tc-1")
	}
}

// TestP507ReportVisibleClaims 报告与导出可见口径类级:已确认(含已
// 修复)/证据不足进;被推翻/已废弃/瞬时候选不进(账本留痕);聚合行
// 只数已废弃(被推翻不出现)。
func TestP507ReportVisibleClaims(t *testing.T) {
	h, store, closer := p507Server(t)
	t.Cleanup(closer)
	task := p507Seed(t, h, store)

	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u3", Payload: rings.NodeAdded{
			NodeID: "tc-fly", NodeType: rings.NodeTestClaim, Title: "瞬时候选", Status: rings.TestClaimCandidate}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u3", Payload: rings.NodeAdded{
			NodeID: "tc-gone", NodeType: rings.NodeTestClaim, Title: "目标摘除连坐", Status: rings.TestClaimDiscarded}},
	)

	md := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report", "").Body.String()
	for _, banned := range []string{"tc-3", "tc-gone", "tc-fly"} {
		if strings.Contains(md, banned) {
			t.Errorf("报告不应出现 %s(被推翻/已废弃/瞬时候选不进,账本留痕)", banned)
		}
	}
	if !strings.Contains(md, "tc-1") || !strings.Contains(md, "tc-2") {
		t.Error("报告应含已确认与证据不足条目")
	}
	if !strings.Contains(md, "证据不足清单") {
		t.Error("报告应含证据不足清单节")
	}

	// csv/json 同口径。
	for _, format := range []string{"csv", "json"} {
		body := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report/export?format="+format, "").Body.String()
		for _, banned := range []string{"tc-3", "tc-gone", "tc-fly"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s 导出不应出现 %s", format, banned)
			}
		}
		if !strings.Contains(body, "tc-1") || !strings.Contains(body, "tc-2") {
			t.Errorf("%s 导出应含已确认与证据不足条目", format)
		}
	}
}

// p507Server P5-07 测试装配(无牧者)。
func p507Server(t *testing.T) (http.Handler, *rings.Store, func()) {
	t.Helper()
	srv, h, _ := newShepherdTestServer(t)
	srv.Shepherd = nil
	return h, srv.Store, func() {}
}

// p507ServerWithShepherd P5-07 测试装配(带牧者服务:追问线用;无
// Provider——入队即回执,应答侧活体另测)。
func p507ServerWithShepherd(t *testing.T) (*Server, http.Handler, func()) {
	t.Helper()
	srv, h, _ := newShepherdTestServer(t)
	return srv, h, func() {}
}
