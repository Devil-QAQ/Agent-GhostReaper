package report

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-30 ③④(问题清单 #51):复核标记不纳入报告的判断退出报告——md
// 正文(清单/计数/级别分布)、zip 分文件、csv、json 全格式零信息性条目;
// 一条汇总行如实交代;卷宗携带标记与理由(最新为准)。

// seedExclusion 给 tc-1(已确认)落一条复核标记。
func seedExclusion(t *testing.T, store *rings.Store, task, reason string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "overmind",
		Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecReportReview, Reason: reason,
		},
	}); err != nil {
		t.Fatalf("铺复核标记失败: %v", err)
	}
}

func TestRenderExcludesReviewedClaims(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-p430"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	seedExclusion(t, store, task, "备份列举只是环境特性,无未授权访问或边界突破")
	ctx := context.Background()

	md, err := Render(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	// 已确认清单与级别分布零信息性条目:tc-1 整体退出。
	if strings.Contains(md, "tc-1") {
		t.Fatalf("复核标记的判断不得出现在报告正文:\n%s", md)
	}
	if strings.Contains(md, "high 1") {
		t.Fatalf("级别分布不得计入被标记判断:\n%s", md)
	}
	if !strings.Contains(md, "已确认发现:**0** 条") {
		t.Fatalf("聚合计数须排除被标记判断:\n%s", md)
	}
	// 一条汇总行如实交代(R4)。
	if !strings.Contains(md, "不纳入报告:1 条判断经复核为信息性观察") {
		t.Fatalf("报告须如实交代被排除条数:\n%s", md)
	}
	// 未标记的待确认判断照常呈现。
	if !strings.Contains(md, "tc-2") || !strings.Contains(md, "待确认清单") {
		t.Fatalf("未标记判断照常进报告:\n%s", md)
	}
}

func TestRenderExcludesReviewedPending(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-p430p"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	// 标记待确认判断 tc-2:同样退出待确认清单与计数。
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "overmind",
		Payload: rings.TestClaimRecord{
			ClaimID: "tc-2", Type: rings.TestClaimRecReportReview, Reason: "版本号暴露属无害观察",
		},
	}); err != nil {
		t.Fatalf("铺复核标记失败: %v", err)
	}
	md, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	if strings.Contains(md, "tc-2") {
		t.Fatalf("被标记的待确认判断不得出现在报告正文:\n%s", md)
	}
	if !strings.Contains(md, "待确认:0 条") {
		t.Fatalf("待确认计数须排除被标记判断:\n%s", md)
	}
}

func TestClaimCaseCarriesReviewState(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-p430c"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	ctx := context.Background()
	seedExclusion(t, store, task, "第一条理由")
	seedExclusion(t, store, task, "第二条理由(取代)")
	cs, err := ClaimCaseOf(ctx, store, blobs, task, "tc-1")
	if err != nil {
		t.Fatalf("装配卷宗失败: %v", err)
	}
	if !cs.ReportExcluded || cs.ReportExcludedReason != "第二条理由(取代)" {
		t.Fatalf("卷宗须携带最新复核标记(取代语义): %+v", cs)
	}
	// 复核标记不进反证时间线(报告治理轴与锤炼轨迹分列)。
	for _, e := range cs.Timeline {
		if strings.Contains(e.Kind, "report") {
			t.Fatalf("复核标记不得进反证时间线: %+v", e)
		}
	}
}

func TestExportsExcludeReviewedClaims(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-p430x"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	seedExclusion(t, store, task, "信息性观察,不纳入报告")
	ctx := context.Background()

	csvBody, err := findingsCSV(ctx, store, task, nil)
	if err != nil {
		t.Fatalf("csv 导出失败: %v", err)
	}
	if strings.Contains(string(csvBody), "tc-1") {
		t.Fatalf("csv 不得含被标记判断")
	}
	if !strings.Contains(string(csvBody), "tc-2") {
		t.Fatalf("csv 须保留未标记判断")
	}

	jsonBody, err := reportJSON(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("json 导出失败: %v", err)
	}
	if strings.Contains(string(jsonBody), "tc-1") {
		t.Fatalf("json 不得含被标记判断")
	}

	zipBody, err := taskZip(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("zip 导出失败: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBody), int64(len(zipBody)))
	if err != nil {
		t.Fatalf("读 zip 失败: %v", err)
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "tc-1") {
			t.Fatalf("zip 分文件不得含被标记判断: %s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("读 zip 条目失败: %v", err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		if strings.Contains(string(body), "### C-1") {
			t.Fatalf("zip 内报告正文不得含被标记判断(条目 %s)", f.Name)
		}
	}
}
