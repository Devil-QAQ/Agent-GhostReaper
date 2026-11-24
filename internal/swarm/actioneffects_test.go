package swarm

import (
	"encoding/json"
	"ghostreaper/internal/rings"
	"testing"
)

func TestBusinessEffectsReusePermissionAcrossInstances(t *testing.T) {
	p := rings.PurposePermission{Actions: []string{"create", "update"}, Resource: "短信发送", Targets: []string{"https://example.test:8083"}, Purpose: "已批准目的", Effects: "已批准影响"}
	boundary := ActionBoundary{Approvals: []rings.ApprovalRow{{RequestID: "approved-sms", Type: rings.ApprovalRecApproved, DecidedBy: "creator", Permission: &p}}}
	for _, resource := range []string{"短信发送", "短信验证码下发"} {
		for i := 0; i < 30; i++ {
			change := p
			change.Resource = resource
			change.Actions = []string{"create"}
			approved := ""
			if resource != p.Resource {
				approved = "approved-sms"
			}
			raw, _ := json.Marshal(actionEffects{Reason: "同一业务影响，实例字段变化不撤销许可", Changes: []businessChange{{Permission: change, ApprovedBy: approved}}})
			result, err := parseActionEffects(string(raw), boundary)
			if err != nil || result.Decision != ActionAllow {
				t.Fatalf("%+v %v", result, err)
			}
		}
	}
	// Real changes outside the recorded grant still need approval.
	for _, target := range []string{"https://another.test", "https://example.test:8084"} {
		change := p
		change.Targets = []string{target}
		raw, _ := json.Marshal(actionEffects{Reason: "新的业务影响", Changes: []businessChange{{Permission: change}}})
		result, err := parseActionEffects(string(raw), boundary)
		if err != nil || result.Decision != ActionApproval {
			t.Fatal(result, err)
		}
	}
	// A grant reference cannot transfer permission to another system or operation.
	changed := p
	changed.Actions = []string{"delete"}
	raw, _ := json.Marshal(actionEffects{Reason: "不适用的批准", Changes: []businessChange{{Permission: changed, ApprovedBy: "approved-sms"}}})
	if _, err := parseActionEffects(string(raw), boundary); err == nil {
		t.Fatal("inapplicable grant accepted")
	}
	// Withdrawal removes the capability regardless of previous tool variants.
	raw, _ = json.Marshal(actionEffects{Reason: "原批准已撤回", Changes: []businessChange{{Permission: p, ApprovedBy: "approved-sms"}}})
	if _, err := parseActionEffects(string(raw), ActionBoundary{}); err == nil {
		t.Fatal("withdrawn grant accepted")
	}
}

func TestBusinessEffectsHaveOneDecisionSource(t *testing.T) {
	for _, raw := range []string{
		`{"reason":"只读查询","changes":[]}`,
		`{"reason":"登录只有伴随日志和会话变化","changes":[]}`,
		`{"reason":"本地工作文件","changes":[]}`,
	} {
		result, err := parseActionEffects(raw, ActionBoundary{})
		if err != nil || result.Decision != ActionAllow {
			t.Fatal(result, err)
		}
	}
	if _, err := parseActionEffects(`{"reason":"沿用已有许可直接放行","decision":"approval","changes":[]}`, ActionBoundary{}); err == nil {
		t.Fatal("contradictory second approval switch accepted")
	}
	if _, err := parseActionEffects(`{"reason":"不知道效果"}`, ActionBoundary{}); err == nil {
		t.Fatal("missing effect judgment accepted")
	}
	result, err := parseActionEffects(`{"reason":"操作者禁止删除","prohibition":"实际删除命中操作者禁止项","changes":[]}`, ActionBoundary{})
	if err != nil || result.Decision != ActionDeny {
		t.Fatal(result, err)
	}
}

func TestBusinessEffectsPreserveImpactClassAndOperatorAuthority(t *testing.T) {
	p := rings.PurposePermission{Actions: []string{"create"}, Resource: "用户", Purpose: "创建普通测试用户", Effects: "普通用户身份", Targets: []string{"https://example.test"}}
	b := ActionBoundary{Notes: "允许创建普通测试用户，无需再次审批。", Approvals: []rings.ApprovalRow{{RequestID: "ordinary-user", Type: rings.ApprovalRecApproved, DecidedBy: "creator", Permission: &p}}}
	elevated := p
	elevated.Resource = "管理员账户权限"
	elevated.Purpose = "建立管理员立足点"
	elevated.Effects = "取得管理员权限"
	raw, _ := json.Marshal(actionEffects{Reason: "真实业务影响不同", Changes: []businessChange{{Permission: elevated}}})
	result, err := parseActionEffects(string(raw), b)
	if err != nil || result.Decision != ActionApproval {
		t.Fatal("same object class erased a different purpose or impact", result, err)
	}
	b.Approvals = nil
	raw, _ = json.Marshal(actionEffects{Reason: "沿用操作者的明文授权", OperatorPermission: b.Notes, Changes: []businessChange{{Permission: p}}})
	result, err = parseActionEffects(string(raw), b)
	if err != nil || result.Decision != ActionAllow {
		t.Fatal(result, err)
	}
	b.Notes = ""
	if _, err := parseActionEffects(string(raw), b); err == nil {
		t.Fatal("invented operator authorization accepted")
	}
}

func TestBusinessEffectsReuseEvidenceBackedSystemAliasesOnly(t *testing.T) {
	p := rings.PurposePermission{Actions: []string{"create"}, Resource: "上传文件", Purpose: "上传测试文件", Effects: "新增测试文件", Targets: []string{"https://example.test:1122"}}
	b := ActionBoundary{Assets: []AssetBrief{{ID: "one", Class: "service", Title: "https://example.test:1122"}, {ID: "two", Class: "service", Title: "https://example.test:1123"}}, Subjects: map[string][]string{"one": {"one"}, "two": {"one"}}, Approvals: []rings.ApprovalRow{{RequestID: "upload-grant", Type: rings.ApprovalRecApproved, DecidedBy: "creator", Permission: &p}}}
	change := p
	change.Targets = []string{"https://example.test:1123"}
	raw, _ := json.Marshal(actionEffects{Reason: "已确认同系统第二入口", Changes: []businessChange{{Permission: change, ApprovedBy: "upload-grant"}}})
	result, err := parseActionEffects(string(raw), b)
	if err != nil || result.Decision != ActionAllow {
		t.Fatal(result, err)
	}
	b.Subjects["two"] = []string{"two"}
	if _, err := parseActionEffects(string(raw), b); err == nil {
		t.Fatal("shared hostname transferred a system grant")
	}
}
