package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-64(问题清单 #82)读侧优先链:事实文本(node_polish 优先于判断摘要
// 反查)、假设文本与最新评估理由、图节点卡标题(年轮图/进度图/链路图
// 共用端点)、发现详情逐轮人话下发、复测备注校对稿、节点详情端点的
// 定制字段数据面(步骤单位/会话记录、事实关联资产/文本包装)。

func p464Setup(t *testing.T) (http.Handler, *rings.Store, string, string) {
	t.Helper()
	h, store, task, hostID := factsSetup(t)
	// 事实文本包装(f-cred 与 f-data;f-plain 不包 → 退原文对照)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{
			FactID: "f-cred", Title: "backup-01 的备份文件里带有后台账号与明文口令",
			Statement: "备份文件没有访问限制,里面的后台口令拿到就能直接登录"}},
		rings.Event{Kind: rings.EventFactText, Actor: "overmind", Payload: rings.FactTextPolish{
			FactID: "f-data", Title: "backup-01 的用户数据可以整表导出",
			Statement: "用户表可以未登录整表导出,共 812 条记录"}},
		// 假设文本包装(h-pol)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h-pol", NodeType: rings.NodeHypothesis, Title: "管理后台可能有弱口令",
			Status: rings.HypothesisHeld, Detail: map[string]any{"basis": "登录页无验证码"}}},
		rings.Event{Kind: rings.EventHypoText, Actor: "overmind", Payload: rings.HypoTextPolish{
			HypothesisID: "h-pol", Title: "backup-01:8080 的管理后台可能存在弱口令",
			Basis: "登录页没有验证码,口令策略宽松", Reason: "依据尚不足以判断价值,待新证据唤醒"}},
		// 假设评估记录(时间线最新评估理由替换的锚点)。
		rings.Event{Kind: rings.EventHypothesisRecord, Actor: "overmind", Payload: rings.HypothesisRecord{
			HypothesisID: "h-pol", Type: rings.HypothesisRecEvaluation,
			Decision: rings.HypothesisHold, Reason: "依据尚不足以判断价值"}},
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

func TestP464HypothesesPreferPolish(t *testing.T) {
	h, _, task, _ := p464Setup(t)
	var hypos []struct {
		ID        string `json:"id"`
		Statement string `json:"statement"`
		Basis     string `json:"basis"`
		Reason    string `json:"reason"`
	}
	get(t, h, "/api/tasks/"+task+"/hypotheses", http.StatusOK, &hypos)
	found := false
	for _, hv := range hypos {
		if hv.ID != "h-pol" {
			continue
		}
		found = true
		if hv.Statement != "backup-01:8080 的管理后台可能存在弱口令" {
			t.Fatalf("假设标题应优先包装: %q", hv.Statement)
		}
		if hv.Basis != "登录页没有验证码,口令策略宽松" {
			t.Fatalf("假设依据应优先包装: %q", hv.Basis)
		}
		if hv.Reason != "依据尚不足以判断价值,待新证据唤醒" {
			t.Fatalf("评估理由应优先包装: %q", hv.Reason)
		}
	}
	if !found {
		t.Fatalf("h-pol 应在假设列表")
	}
	// 详情:时间线最新评估条目的理由替换为包装稿。
	var detail struct {
		Hypothesis struct {
			Statement string `json:"statement"`
		} `json:"hypothesis"`
		Timeline []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"timeline"`
		RawDetail map[string]any `json:"raw_detail"`
	}
	get(t, h, "/api/tasks/"+task+"/hypotheses/h-pol", http.StatusOK, &detail)
	if detail.Hypothesis.Statement != "backup-01:8080 的管理后台可能存在弱口令" {
		t.Fatalf("详情假设标题应优先包装: %q", detail.Hypothesis.Statement)
	}
	replaced := false
	for _, e := range detail.Timeline {
		if e.Kind == "evaluation" {
			if e.Reason != "依据尚不足以判断价值,待新证据唤醒" {
				t.Fatalf("最新评估条目理由应为包装稿: %q", e.Reason)
			}
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("时间线应有评估条目")
	}
	if detail.RawDetail["basis"] != "登录页无验证码" {
		t.Fatalf("raw_detail 应带节点 detail 原文: %+v", detail.RawDetail)
	}
}

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
	if titles["f-cred"] != "backup-01 的备份文件里带有后台账号与明文口令" {
		t.Fatalf("年轮图/进度图节点卡事实标题应为短句校对稿: %q", titles["f-cred"])
	}
	if titles["h-pol"] != "backup-01:8080 的管理后台可能存在弱口令" {
		t.Fatalf("节点卡假设标题应为短句校对稿: %q", titles["h-pol"])
	}
	if titles["f-plain"] != "8080 端口运行 nginx 1.18.0" {
		t.Fatalf("未包装节点标题应保持原文: %q", titles["f-plain"])
	}
}

func TestP464ClaimDetailRoundHumansAndRetestNote(t *testing.T) {
	h, store, task, _ := p464Setup(t)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventClaimRoundHuman, Actor: "overmind", Payload: rings.ClaimRoundHuman{
			ClaimID: "tc-weak", Round: 1, Doubt: "会不会是口令已被改过?", How: "用备份里的口令直接登录了一次", Conclusion: "已排除"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:rt-1", Payload: rings.TestClaimRecord{
			ClaimID: "tc-weak", StepID: "rt-s1", UnitID: "rt-1", Type: rings.TestClaimRecRetest,
			RetestVerdict: "unfixed", Round: 1, Note: "机器记录原文备注",
			EvidenceHead: "GET /login → 200 登录成功"}},
		rings.Event{Kind: rings.EventClaimRetextNote, Actor: "overmind", Payload: rings.RetestNotePolish{
			ClaimID: "tc-weak", Round: 1, Note: "按复现步骤重发了请求,弱口令仍然可以登录,说明该漏洞未修复"}},
	)
	var v struct {
		RoundHumans []struct {
			Round      int    `json:"round"`
			Doubt      string `json:"doubt"`
			Conclusion string `json:"conclusion"`
		} `json:"round_humans"`
		Retests []struct {
			Round int    `json:"round"`
			Note  string `json:"note"`
		} `json:"retests"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-weak", http.StatusOK, &v)
	if len(v.RoundHumans) != 1 || v.RoundHumans[0].Doubt == "" || v.RoundHumans[0].Conclusion != "已排除" {
		t.Fatalf("逐轮人话应随详情下发: %+v", v.RoundHumans)
	}
	if len(v.Retests) != 1 || v.Retests[0].Note != "按复现步骤重发了请求,弱口令仍然可以登录,说明该漏洞未修复" {
		t.Fatalf("复测备注应优先校对稿: %+v", v.Retests)
	}
}

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
	if len(fv.Assets) != 1 || fv.Assets[0]["id"] != hostID || fv.Assets[0]["title"] != "backup-01" {
		t.Fatalf("事实应补发关联资产: %+v", fv.Assets)
	}
	if fv.PolishedTitle == "" || fv.PolishedStatement == "" {
		t.Fatalf("事实应补发文本包装: %+v", fv)
	}
}
