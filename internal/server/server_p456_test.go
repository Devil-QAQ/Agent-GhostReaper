package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-56(问题清单 #77)发现区四页签数据面 ----
//
// 锤炼/假设列表的显示键推导(机器状态+步骤现值)、悬停细分、锤炼条目
// 点回事实(FactRefs)、事实页迁入(成立假设带"由假设升级")与详情头
// 同词。种子账本确定性验证,零模型。

// p456Setup 种子账本:一任务下铺齐各形态判断(候选/反证中/反证暂停/
// 操作者终止待判定/证据不足/终局中断挂起/被推翻/摘除连坐/已确认)与
// 假设(验证中/等待验证/成立)、事实(已确认/新进)。
func p456Setup(t *testing.T) (http.Handler, *rings.Store, string) {
	t.Helper()
	h, store, task, _ := factsSetup(t)

	appendEvents(t, store, task,
		// tc-cand:候选 → 等待反证。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-cand", NodeType: rings.NodeTestClaim, Title: "候选判断", Status: rings.TestClaimCandidate}},
		// tc-run:testing + 反证步骤在跑 → 反证中。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-run", NodeType: rings.NodeTestClaim, Title: "反证中判断", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "反证", Status: "running",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-run", "round": 1.0}}},
		// tc-pause:testing + 反证步骤等范围批准 → 反证暂停+等待审批。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-pause", NodeType: rings.NodeTestClaim, Title: "暂停判断", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-pause", NodeType: rings.NodeStep, Title: "反证", Status: rings.StepScopeWait,
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-pause", "round": 1.0}}},
		// tc-kill:testing + 反证步骤被舵杆杀 → 等待反证+等待主宰判定去留。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-kill", NodeType: rings.NodeTestClaim, Title: "待判定判断", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-kill", NodeType: rings.NodeStep, Title: "反证", Status: "aborted",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-kill", "round": 1.0}}},
		rings.Event{Kind: rings.EventSteerRecord, Actor: "creator", Payload: rings.SteerRecord{
			SteerID: "st-kill", StepID: "s-kill", UnitID: "u9", Command: rings.SteerCommandKill,
			State: rings.SteerStateDelivered, Reason: "演练杀单位"}},
		// tc-insufficient:挂起 → 证据不足。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-ins", NodeType: rings.NodeTestClaim, Title: "证据不足判断", Status: rings.TestClaimPending}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-done", NodeType: rings.NodeTestClaim, Title: "终局中断判断", Status: rings.TestClaimPending}},
		// tc-refu / tc-disc:终了档。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-refu", NodeType: rings.NodeTestClaim, Title: "被推翻判断", Status: rings.TestClaimRefuted}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-disc", NodeType: rings.NodeTestClaim, Title: "连坐判断", Status: rings.TestClaimDiscarded}},
		// tc-win:确认(成果页) + 血缘引用事实(锤炼条目点回事实)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-src", NodeType: rings.NodeFact, Title: "登录接口无需凭据", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-win", NodeType: rings.NodeTestClaim, Title: "确认判断", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-win", ToNode: "f-src"}},
		// 假设:验证中(验证步骤在跑)与等待验证;成立迁入事实页。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h-run", NodeType: rings.NodeHypothesis, Title: "验证中假设", Status: rings.HypothesisTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-ver", NodeType: rings.NodeStep, Title: "验证", Status: "running",
			Detail: map[string]any{"kind": "verify", "hypothesis_id": "h-run"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h-wait", NodeType: rings.NodeHypothesis, Title: "等待验证假设", Status: rings.HypothesisCandidate}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h-win", NodeType: rings.NodeHypothesis, Title: "成立假设", Status: rings.HypothesisConfirmed}},
	)
	// 挂起理由走卷宗投影(受控入口):tc-done 理由为终局清点口径。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-done", Type: rings.TestClaimRecVerdict, Decision: rings.VerdictPending,
			PendingReason: "任务完成中断反证，可被唤醒",
			Conditions:    []rings.ArbiterConditionResult{{Kind: "existence_not_refuted", Required: 1}}}},
	)
	return h, store, task
}

// TestP456ClaimsDisplay 锤炼页数据面:显示键与悬停细分逐格对表;确认
// 判断带血缘引用事实(点回事实)。
func TestP456ClaimsDisplay(t *testing.T) {
	h, _, task := p456Setup(t)

	var claims []struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Display     string `json:"display"`
		DisplayHint string `json:"display_hint"`
		FactRefs    []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"fact_refs"`
	}
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	byID := map[string]int{}
	for i, c := range claims {
		byID[c.ID] = i
	}
	want := map[string][2]string{
		"tc-weak":  {rings.ClaimDisplayConfirmed, ""}, // factsSetup 既有确认判断(详情同源测试用)
		"tc-cand":  {rings.ClaimDisplayWaitFalsify, ""},
		"tc-run":   {rings.ClaimDisplayFalsifying, ""},
		"tc-pause": {rings.ClaimDisplayFalsifyPaused, ""},
		"tc-kill":  {rings.ClaimDisplayWaitFalsify, rings.ClaimHintRulingWait},
		"tc-ins":   {rings.ClaimDisplayInsufficient, ""},
		"tc-done":  {rings.ClaimDisplayInsufficient, rings.ClaimHintTaskDone},
		"tc-refu":  {rings.ClaimDisplayDiscarded, ""},
		"tc-disc":  {rings.ClaimDisplayDiscarded, ""},
		"tc-win":   {rings.ClaimDisplayConfirmed, ""},
	}
	if len(claims) != len(want) {
		t.Fatalf("判断应 %d 条,得 %d(%+v)", len(want), len(claims), claims)
	}
	for id, w := range want {
		c := claims[byID[id]]
		if c.Display != w[0] || c.DisplayHint != w[1] {
			t.Errorf("%s: display=(%q,%q), want (%q,%q)", id, c.Display, c.DisplayHint, w[0], w[1])
		}
	}
	// 锤炼条目点回事实:tc-win 的血缘引用 f-src(标题随行)。
	c := claims[byID["tc-win"]]
	if len(c.FactRefs) != 1 || c.FactRefs[0].ID != "f-src" || c.FactRefs[0].Title != "登录接口无需凭据" {
		t.Errorf("tc-win 血缘引用事实不符: %+v", c.FactRefs)
	}
}

// TestP456ClaimDetailDisplay 详情头与列表同词(display 同键下发)。
func TestP456ClaimDetailDisplay(t *testing.T) {
	h, _, task := p456Setup(t)
	var body struct {
		Claim struct {
			Display     string `json:"display"`
			DisplayHint string `json:"display_hint"`
			FactRefs    []struct {
				ID string `json:"id"`
			} `json:"fact_refs"`
		} `json:"claim"`
		Timeline []json.RawMessage `json:"timeline"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-kill", http.StatusOK, &body)
	if body.Claim.Display != rings.ClaimDisplayWaitFalsify || body.Claim.DisplayHint != rings.ClaimHintRulingWait {
		t.Fatalf("详情显示键不符: display=%q hint=%q", body.Claim.Display, body.Claim.DisplayHint)
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-win", http.StatusOK, &body)
	if len(body.Claim.FactRefs) != 1 || body.Claim.FactRefs[0].ID != "f-src" {
		t.Fatalf("详情血缘引用事实不符: %+v", body.Claim.FactRefs)
	}
}

// TestP456HypothesesDisplay 假设页数据面:验证步骤现值推导显示键;成立
// 条目仍在清单(前端按页签过滤,数据面不裁)。
func TestP456HypothesesDisplay(t *testing.T) {
	h, _, task := p456Setup(t)
	var hypos []struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Display string `json:"display"`
	}
	get(t, h, "/api/tasks/"+task+"/hypotheses", http.StatusOK, &hypos)
	byID := map[string]string{}
	for _, x := range hypos {
		byID[x.ID] = x.Display
	}
	if byID["h-run"] != rings.HypDisplayVerifying {
		t.Errorf("h-run 应 %s,得 %q", rings.HypDisplayVerifying, byID["h-run"])
	}
	if byID["h-wait"] != rings.HypDisplayWaitVerify {
		t.Errorf("h-wait 应 %s,得 %q", rings.HypDisplayWaitVerify, byID["h-wait"])
	}
	if byID["h-win"] != rings.HypDisplayConfirmed {
		t.Errorf("h-win 应 %s,得 %q", rings.HypDisplayConfirmed, byID["h-win"])
	}
}

// TestP456FactsMigrated 事实页迁入:成立假设以"由假设升级"入列
// (kind=hypothesis,点开假设抽屉);新进候选标"新"。
func TestP456FactsMigrated(t *testing.T) {
	h, store, task := p456Setup(t)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "f-new", NodeType: rings.NodeFact, Title: "新进候选事实", Status: "candidate"}})

	var body struct {
		Facts []struct {
			ID             string `json:"id"`
			Kind           string `json:"kind"`
			IsNew          bool   `json:"is_new"`
			FromHypothesis bool   `json:"from_hypothesis"`
			Statement      string `json:"statement"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)
	var sawWin, sawNew bool
	for _, f := range body.Facts {
		if f.ID == "h-win" {
			sawWin = true
			if f.Kind != "hypothesis" || !f.FromHypothesis || f.Statement != "成立假设" {
				t.Errorf("h-win 迁入形态不符: %+v", f)
			}
		}
		if f.ID == "f-new" {
			sawNew = true
			if !f.IsNew || f.Kind != "fact" {
				t.Errorf("f-new 应标新: %+v", f)
			}
		}
	}
	if !sawWin {
		t.Errorf("成立假设未迁入事实页: %+v", body.Facts)
	}
	if !sawNew {
		t.Errorf("新进候选未入列: %+v", body.Facts)
	}
}

// TestP456UnreadFactsIncludesHyp 事实未读含成立假设(P4-47 语义与页签
// 展示口径一致:迁入条目新确认即计)。
func TestP456UnreadFactsIncludesHyp(t *testing.T) {
	h, _, task := p456Setup(t)
	var body struct {
		Claims int `json:"claims"`
		Facts  int `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/unread", http.StatusOK, &body)
	if body.Claims != 2 {
		t.Errorf("成果未读应 2(tc-weak + tc-win),得 %d", body.Claims)
	}
	if body.Facts != 5 {
		t.Errorf("事实未读应 5(既有 3 事实 + f-src + h-win),得 %d", body.Facts)
	}
}

// TestP456NoLegacyWordsInViews 旧词清零(P4-56 ④):前端词表与视图源码
// 零退役词(与构建闸门同口径的双重断言,防闸门被绕)。
func TestP456NoLegacyWordsInViews(t *testing.T) {
	for _, rel := range []string{
		"web/lib/api.js",
		"web/views/findings-page.js",
		"web/views/node-sheets.js",
	} {
		b, err := os.ReadFile(rel)
		if err != nil {
			t.Skipf("前端源码不可读(仅仓库内运行): %v", err)
		}
		src := string(b)
		for _, w := range []string{"锤炼中", "挂账", "终审不成立"} {
			if strings.Contains(src, w) && !strings.Contains(src, "// ") {
				t.Errorf("%s: 退役词 %q 残留", rel, w)
			}
			if strings.Contains(src, "label: \""+w) || strings.Contains(src, "\"当前结论：暂不成立") {
				t.Errorf("%s: 退役词 %q 残留", rel, w)
			}
		}
	}
}
