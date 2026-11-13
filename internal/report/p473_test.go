package report

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestP473FlowIncompleteRender P4-73 ④:挑选失败仍出种子包——报告复现区
// 呈现步骤与"流程步骤未生成（挑选失败）"如实标注(与面板同句);旧记录
// 无标注零渲染。
func TestP473FlowIncompleteRender(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "p473-report"
	seedReportScenario(t, store, task)
	seedP435Repro(t, store, blobs, task)
	// 新定稿(取代语义):证明交换种子在包内、流程步骤补全失败。
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			StepsIncomplete: "流程步骤未生成（挑选失败）",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
				Short: "拿用户列表", Desc: "向 10.10.0.5:8080 发 GET /api/users,无需凭据;返回 200 且响应含用户列表即复现",
				Method: "GET", URL: "http://10.10.0.5:8080/api/users", Status: 200,
				BlobRef: p473FirstStepBlob(t, blobs), ExchangeID: "ms-final",
			}},
		},
	}); err != nil {
		t.Fatalf("铺新定稿失败: %v", err)
	}

	md, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	if !strings.Contains(md, "流程步骤未生成（挑选失败）") {
		t.Fatalf("报告复现区应呈现流程步骤未生成标注:\n%s", md)
	}
	if !strings.Contains(md, "① 拿用户列表") {
		t.Fatalf("证明交换种子步骤应照常呈现:\n%s", md)
	}
	if strings.Contains(md, "无可用复现包") {
		t.Fatalf("有包不得呈现无复现包原因:\n%s", md)
	}
	// 零步骤时不渲染标注(那是无复现包语义)。
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, NoReproReason: "无可用复现包：池内交换均不能证明该问题成立。",
		},
	}); err != nil {
		t.Fatalf("铺空定稿失败: %v", err)
	}
	md2, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	if strings.Contains(md2, "流程步骤未生成") || !strings.Contains(md2, "无可用复现包") {
		t.Fatalf("空包定稿只呈现无复现包原因:\n%s", md2)
	}
}

// p473FirstStepBlob 写一份可回放的证明交换文档(渲染只要求 blob 可读)。
func p473FirstStepBlob(t *testing.T, blobs *swarm.BlobStore) string {
	t.Helper()
	doc := `{"id":"ms-final","type":"http","task_id":"p473-report","step_id":"s2","unit_id":"u2",` +
		`"request":{"method":"GET","url":"http://10.10.0.5:8080/api/users","proto":"HTTP/1.1","host":"10.10.0.5:8080"},` +
		`"response":{"proto":"HTTP/1.1","status":200,"status_text":"OK","body":"eyJ1c2VycyI6WyJhZG1pbiJdfQ=="}}`
	ref, err := blobs.Write([]byte(doc))
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	return ref
}
