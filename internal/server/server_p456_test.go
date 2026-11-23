package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func p456Setup(t *testing.T) (http.Handler, *rings.Store, string) {
	t.Helper()
	h, store, task, _ := factsSetup(t)

	appendEvents(t, store, task,
		// tc-cand:候选 → 等待反证。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-cand", NodeType: rings.NodeTestClaim, Title: "候选判断", Status: rings.TestClaimCandidate}},
		// tc-run:testing + 反证步骤在跑 → 反证中。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-run", NodeType: rings.NodeTestClaim, Title: "反证中判断", Status: "testing"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "反证", Status: "running",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-run", "round": 1.0}}},
		// tc-pause:testing + 反证步骤等范围批准 → 反证暂停+等待审批。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-pause", NodeType: rings.NodeTestClaim, Title: "暂停判断", Status: "testing"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-pause", NodeType: rings.NodeStep, Title: "反证", Status: rings.StepScopeWait,
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-pause", "round": 1.0}}},
		// tc-kill:testing + 反证步骤被舵杆杀 → 等待反证+等待主宰判定去留。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-kill", NodeType: rings.NodeTestClaim, Title: "待判定判断", Status: "testing"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-kill", NodeType: rings.NodeStep, Title: "反证", Status: "aborted",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-kill", "round": 1.0}}},
		rings.Event{Kind: rings.EventSteerRecord, Actor: "creator", Payload: rings.SteerRecord{
			SteerID: "st-kill", StepID: "s-kill", UnitID: "u9", Command: rings.SteerCommandKill,
			State: rings.SteerStateDelivered, Reason: "演练杀单位"}},
		// tc-insufficient:挂起 → 证据不足。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-ins", NodeType: rings.NodeTestClaim, Title: "证据不足判断", Status: "pending"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-done", NodeType: rings.NodeTestClaim, Title: "终局中断判断", Status: "pending"}},
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

		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-ver", NodeType: rings.NodeStep, Title: "验证", Status: "running",
			Detail: map[string]any{"action_level": "只读探测"}}},
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
		ID       string `json:"id"`
		Status   string `json:"status"`
		Display  string `json:"display"`
		FactRefs []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"fact_refs"`
	}
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	byID := map[string]int{}
	for i, c := range claims {
		byID[c.ID] = i
	}
	// P5-02⑤ 三态显示键:候选/已确认/证据不足;refuted 与 discarded
	// 机器态保留分立(不成立/已废弃);旧过程态(testing 等)原样退回
	//(P5-03③:无悬停细分字段)。
	want := map[string]string{
		"tc-weak":  rings.ClaimDisplayConfirmed, // factsSetup 既有确认判断(详情同源测试用)
		"tc-cand":  rings.ClaimDisplayCandidate,
		"tc-run":   "testing", // 存量过程态原样退回
		"tc-pause": "testing", // 存量过程态原样退回
		"tc-kill":  "testing", // 存量过程态原样退回
		"tc-ins":   "pending", // 存量挂起词面原样退回(映射归 P5-07⑧)
		"tc-done":  "pending", // 存量挂起词面原样退回(映射归 P5-07⑧)
		"tc-refu":  rings.ClaimDisplayRefuted,
		"tc-disc":  rings.ClaimDisplayDiscarded,
		"tc-win":   rings.ClaimDisplayConfirmed,
	}
	if len(claims) != len(want) {
		t.Fatalf("判断应 %d 条,得 %d(%+v)", len(want), len(claims), claims)
	}
	for id, w := range want {
		c := claims[byID[id]]
		if c.Display != w {
			t.Errorf("%s: display=%q, want %q", id, c.Display, w)
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
			Display  string `json:"display"`
			FactRefs []struct {
				ID string `json:"id"`
			} `json:"fact_refs"`
		} `json:"claim"`
		Timeline []json.RawMessage `json:"timeline"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-kill", http.StatusOK, &body)
	if body.Claim.Display != "testing" {
		t.Fatalf("存量过程态详情应原样退回: display=%q", body.Claim.Display)
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-win", http.StatusOK, &body)
	if len(body.Claim.FactRefs) != 1 || body.Claim.FactRefs[0].ID != "f-src" {
		t.Fatalf("详情血缘引用事实不符: %+v", body.Claim.FactRefs)
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
