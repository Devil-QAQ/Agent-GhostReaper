package swarm

import (
	"strings"
	"testing"
)

// P4-30 ①②(问题清单 #51):立判危害门槛与定级校准锚的共用句纪律——
// 工具说明与虫群铁律引用同一份常量,防漂移;P5-01① 起定级锚随交卷
// 工具说明与铁律给虫群自报级别(反证任务包已退役)。

func TestClaimHarmGateSharedClauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"submit_claim 工具说明", toolDescSubmitClaim},
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
	if !strings.Contains(toolDescSubmitClaim, SeverityAnchor) {
		t.Fatalf("submit_claim 工具说明须逐字携带定级校准锚(P4-30 ②,P5-01① 自报级别)")
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
}
