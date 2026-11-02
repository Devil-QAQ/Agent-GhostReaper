package swarm

import (
	"strings"
	"testing"
)

// P4-30 ①②(问题清单 #51):立判危害门槛与定级校准锚的共用句纪律——
// 工具说明、虫群铁律、反证任务包三处引用同一份常量,防漂移。

func TestClaimHarmGateSharedClauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"submit_test_claim 工具说明", toolDescSubmitTestClaim},
		{"虫群铁律 B 段", workerSafetyRules},
	} {
		if !strings.Contains(tc.body, ClaimHarmGate) {
			t.Fatalf("%s 须逐字携带立判危害门槛(P4-30 ①)", tc.name)
		}
	}
	for _, want := range []string{
		"未授权访问数据/功能、可执行代码、越过鉴权、拒绝服务、泄露敏感信息",
		"纯前置条件(如验证码可解)、环境特性(如端口持续响应)、无害观察一律不立判断",
	} {
		if !strings.Contains(ClaimHarmGate, want) {
			t.Fatalf("立判危害门槛缺关键句 %q", want)
		}
	}
}

func TestSeverityAnchorSharedClauses(t *testing.T) {
	if !strings.Contains(toolDescSubmitFalsifyResult, SeverityAnchor) {
		t.Fatalf("submit_falsify_result 工具说明须逐字携带定级校准锚(P4-30 ②)")
	}
	for _, want := range []string{
		"critical=系统级权限或大量敏感数据",
		"high=管理员或敏感数据访问",
		"medium=普通业务越权或有限敏感信息",
		"low=需苛刻前提才有影响",
		"无直接危害的信息性观察不定级也不立判断",
	} {
		if !strings.Contains(SeverityAnchor, want) {
			t.Fatalf("定级校准锚缺档位定义 %q", want)
		}
	}
	// 反证任务包(定稿与反证流程共用的呈现面)同样携带。
	pack := &TaskPack{Falsify: &FalsifyBrief{
		ClaimID: "tc-1", Statement: "存在性判断", Impact: "影响面判断",
		Alternatives: []AltBrief{{ID: "alt1", Label: "缓存污染"}},
	}}
	rendered := pack.Render()
	if !strings.Contains(rendered, SeverityAnchor) {
		t.Fatalf("反证任务包须携带定级校准锚:\n%s", rendered)
	}
}
