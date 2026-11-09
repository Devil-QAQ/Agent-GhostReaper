package swarm

// P4-52 ②(问题清单 #77⑤):批准附意见的确定性单测——意见框两路径。
// 留空＝直接继续原会话(续跑指令无附言段);非空＝放行前注入原会话
//(意见全文进续跑指令,转录可证;记录留痕由 LedgerGate 既有路径覆盖,
// 见 gate_test 的 Reason 断言)。

import (
	"strings"
	"testing"
)

func TestP452ApprovalResumePromptOpinion(t *testing.T) {
	req := ApprovalRequest{
		ID: "ap-abc", Intent: "在目标站注册一个测试账号",
		ActionLevel: "修改与持久化", ToolName: "bash",
		ToolArgs: `{"command":"curl -X POST http://target/register"}`,
	}
	// 留空:直接继续,无附言段。
	plain := approvalResumePrompt(req, GateDecision{Approved: true, By: "creator"})
	if strings.Contains(plain, "附言") {
		t.Fatalf("留空意见不得出现附言段: %q", plain)
	}
	if !strings.Contains(plain, "审批结果 · 续跑") || !strings.Contains(plain, "已批准") {
		t.Fatalf("续跑指令骨架应完整: %q", plain)
	}
	// 非空:意见全文以造物主指令语义注入。
	withNote := approvalResumePrompt(req, GateDecision{Approved: true, By: "creator", Reason: "用完删掉"})
	if !strings.Contains(withNote, "造物主批准附言(请遵照执行):用完删掉。") {
		t.Fatalf("意见全文应随放行注入原会话: %q", withNote)
	}
	// 拒绝路径不进续跑指令(拒绝即熔断,意见作为拒绝原因入记录——
	// executor 拒绝分支只走 deniedReason,不调本函数)。
	if reason := "审批拒绝终止: 面板拒绝" + reasonSuffix("用完删掉"); !strings.Contains(reason, "用完删掉") {
		t.Fatalf("拒绝原因应携带意见入记录: %q", reason)
	}
}
