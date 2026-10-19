package report

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// P2-13 格式矩阵与漏洞级导出:md / zip(逐发现分文件 + 证据包)/ csv /
// json,范围过滤,字节级确定性,跨任务合并。

func TestTaskExportMatrix(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "exp-matrix"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	ctx := context.Background()

	// md:全文与 Render 同源,含复现步骤与反证链。
	out, err := TaskExport(ctx, store, blobs, task, FormatMD, nil)
	if err != nil {
		t.Fatalf("md 导出失败: %v", err)
	}
	if out.Filename != "report-"+task+".md" || !strings.Contains(out.MimeType, "text/markdown") {
		t.Fatalf("md 文件名/类型不符: %+v", out)
	}
	for _, want := range []string{"反证记录(年轮版本刻度):", "复现步骤(收割定稿", "GET http://10.10.0.5:8080/old-backup/ HTTP/1.1"} {
		if !strings.Contains(string(out.Body), want) {
			t.Fatalf("md 缺 %q", want)
		}
	}

	// 范围过滤:只导 tc-2 时,概要注明范围且正文无 tc-1 小节。
	scoped, err := TaskExport(ctx, store, blobs, task, FormatMD, []string{"tc-2"})
	if err != nil {
		t.Fatalf("范围导出失败: %v", err)
	}
	body := string(scoped.Body)
	if !strings.Contains(body, "本次导出范围:1 / 2 条发现") || strings.Contains(body, "`tc-1`") {
		t.Fatalf("范围过滤不符:\n%s", body)
	}

	// zip:报告 + 逐发现分文件 + 证据包;两次导出字节级一致。
	zipOnce := func() []byte {
		out, err := TaskExport(ctx, store, blobs, task, FormatZip, nil)
		if err != nil {
			t.Fatalf("zip 导出失败: %v", err)
		}
		if out.Filename != "report-"+task+".zip" {
			t.Fatalf("zip 文件名不符: %s", out.Filename)
		}
		return out.Body
	}
	z1, z2 := zipOnce(), zipOnce()
	if !bytes.Equal(z1, z2) {
		t.Fatalf("相同账本状态两次 zip 导出必须字节级一致")
	}
	zr, err := zip.NewReader(bytes.NewReader(z1), int64(len(z1)))
	if err != nil {
		t.Fatalf("开 zip 失败: %v", err)
	}
	names := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("开 zip 条目 %s 失败: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		names[f.Name] = string(b)
	}
	for _, name := range []string{
		"report-" + task + ".md",
		"claims/C1-tc-1.md",
		"claims/P1-tc-2.md",
		"evidence/tc-1/step-1.txt",
	} {
		if _, ok := names[name]; !ok {
			t.Fatalf("zip 缺条目 %s;实有条目 %v", name, keysOf(names))
		}
	}
	if !strings.Contains(names["claims/C1-tc-1.md"], "# 漏洞报告 · `tc-1`") ||
		!strings.Contains(names["claims/C1-tc-1.md"], "复现步骤(收割定稿") {
		t.Fatalf("分文件应为单条发现完整报告:\n%s", names["claims/C1-tc-1.md"])
	}
	// 证据包为全文(不截断)且带内容寻址引用头。
	ev := names["evidence/tc-1/step-1.txt"]
	if !strings.Contains(ev, "# 证据 · 步骤 1 · GET http://10.10.0.5:8080/old-backup/ → 200") ||
		!strings.Contains(ev, "mock backup archive listing") {
		t.Fatalf("证据包内容不符:\n%s", ev)
	}

	// csv:BOM + 表头 + 行;范围过滤生效。
	csvOut, err := TaskExport(ctx, store, blobs, task, FormatCSV, nil)
	if err != nil {
		t.Fatalf("csv 导出失败: %v", err)
	}
	if csvOut.Filename != "findings-"+task+".csv" {
		t.Fatalf("csv 文件名不符: %s", csvOut.Filename)
	}
	rows := readCSV(t, csvOut.Body)
	if len(rows) != 3 {
		t.Fatalf("csv 应表头 + 2 行,实际 %d 行", len(rows))
	}
	if rows[0][1] != "发现ID" || rows[0][10] != "复现步骤数" {
		t.Fatalf("csv 表头不符: %v", rows[0])
	}
	// 级别序:tc-1(high)在前,tc-2(未定级)沉底。
	if rows[1][1] != "tc-1" || rows[1][3] != "high" || rows[1][10] != "1" {
		t.Fatalf("tc-1 行不符: %v", rows[1])
	}
	if rows[2][1] != "tc-2" {
		t.Fatalf("tc-2 行不符: %v", rows[2])
	}
	csvSel, err := TaskExport(ctx, store, blobs, task, FormatCSV, []string{"tc-2"})
	if err != nil {
		t.Fatalf("csv 范围导出失败: %v", err)
	}
	if rows := readCSV(t, csvSel.Body); len(rows) != 2 || rows[1][1] != "tc-2" {
		t.Fatalf("csv 范围过滤不符: %v", rows)
	}

	// json:结构化全量,时间线与定稿在位;确定性。
	j1, err := TaskExport(ctx, store, blobs, task, FormatJSON, nil)
	if err != nil {
		t.Fatalf("json 导出失败: %v", err)
	}
	j2, err := TaskExport(ctx, store, blobs, task, FormatJSON, nil)
	if err != nil {
		t.Fatalf("json 二次导出失败: %v", err)
	}
	if !bytes.Equal(j1.Body, j2.Body) {
		t.Fatalf("json 导出必须字节级一致")
	}
	var doc struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
		Findings []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
			Timeline []struct {
				Kind string `json:"kind"`
			} `json:"timeline"`
			Repro struct {
				Steps []struct {
					Seq int    `json:"seq"`
					Raw string `json:"raw"`
				} `json:"steps"`
			} `json:"repro"`
			Remediation []string `json:"remediation"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(j1.Body, &doc); err != nil {
		t.Fatalf("解码 json 失败: %v", err)
	}
	if doc.Task.ID != task || len(doc.Findings) != 2 {
		t.Fatalf("json 任务/发现数不符: %+v", doc)
	}
	f0 := doc.Findings[0]
	if f0.ID != "tc-1" || f0.Severity != "high" || len(f0.Timeline) == 0 {
		t.Fatalf("json tc-1 卷宗不符: %+v", f0)
	}
	if len(f0.Repro.Steps) != 1 || !strings.Contains(f0.Repro.Steps[0].Raw, "HTTP/1.1 200 OK") {
		t.Fatalf("json 复现原文不符: %+v", f0.Repro)
	}
	if len(f0.Remediation) == 0 {
		t.Fatalf("json 应带查表修复建议")
	}

	// 非法格式拒绝。
	if _, err := TaskExport(ctx, store, blobs, task, "exe", nil); err == nil {
		t.Fatalf("非法格式应报错")
	}
}

// TestClaimExport 漏洞级导出:md 单份 + zip 附证据包。
func TestClaimExport(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "exp-claim"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	ctx := context.Background()

	md, err := ClaimExport(ctx, store, blobs, task, "tc-1", FormatMD)
	if err != nil {
		t.Fatalf("漏洞级 md 失败: %v", err)
	}
	if md.Filename != "finding-tc-1.md" {
		t.Fatalf("文件名规约不符: %s", md.Filename)
	}
	for _, want := range []string{"# 漏洞报告 · `tc-1`", "反证记录(年轮版本刻度):", "复现步骤(收割定稿", "操作方法:`bash curl"} {
		if !strings.Contains(string(md.Body), want) {
			t.Fatalf("漏洞级 md 缺 %q:\n%s", want, md.Body)
		}
	}

	zout, err := ClaimExport(ctx, store, blobs, task, "tc-1", FormatZip)
	if err != nil {
		t.Fatalf("漏洞级 zip 失败: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zout.Body), int64(len(zout.Body)))
	if err != nil {
		t.Fatalf("开 zip 失败: %v", err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["finding-tc-1.md"] || !got["evidence/tc-1/step-1.txt"] {
		t.Fatalf("漏洞级 zip 条目不符: %v", got)
	}

	// csv/json 不在漏洞级格式白名单;不存在发现报错。
	if _, err := ClaimExport(ctx, store, blobs, task, "tc-1", FormatCSV); err == nil {
		t.Fatalf("漏洞级仅支持 md/zip")
	}
	if _, err := ClaimExport(ctx, store, blobs, task, "no-such", FormatMD); err == nil {
		t.Fatalf("不存在发现应报错")
	}
}

// TestCrossTaskExport 跨任务导出:md 顺排 / zip 分目录 / csv 合并 /
// json 数组;空任务清单拒绝。
func TestCrossTaskExport(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const a, b = "exp-cross-a", "exp-cross-b"
	seedReportScenario(t, store, a)
	seedRepro(t, store, blobs, a)
	seedReportScenario(t, store, b)
	ctx := context.Background()

	md, err := CrossTaskExport(ctx, store, blobs, FormatMD, []string{a, b})
	if err != nil {
		t.Fatalf("跨任务 md 失败: %v", err)
	}
	if md.Filename != "reports.md" ||
		strings.Count(string(md.Body), "# 渗透测试报告 ·") != 2 {
		t.Fatalf("跨任务 md 应含两份报告: %s", md.Filename)
	}

	zout, err := CrossTaskExport(ctx, store, blobs, FormatZip, []string{a, b})
	if err != nil {
		t.Fatalf("跨任务 zip 失败: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zout.Body), int64(len(zout.Body)))
	if err != nil {
		t.Fatalf("开 zip 失败: %v", err)
	}
	for _, want := range []string{a + "/report-" + a + ".md", b + "/report-" + b + ".md", a + "/claims/C1-tc-1.md"} {
		found := false
		for _, f := range zr.File {
			if f.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("跨任务 zip 缺 %s", want)
		}
	}

	csvOut, err := CrossTaskExport(ctx, store, blobs, FormatCSV, []string{a, b})
	if err != nil {
		t.Fatalf("跨任务 csv 失败: %v", err)
	}
	rows := readCSV(t, csvOut.Body)
	if len(rows) != 5 { // 表头 + 2×2
		t.Fatalf("跨任务 csv 应 5 行,实际 %d", len(rows))
	}
	for _, r := range rows[1:] {
		if r[0] != a && r[0] != b {
			t.Fatalf("csv 行任务列不符: %v", r)
		}
	}

	jout, err := CrossTaskExport(ctx, store, blobs, FormatJSON, []string{a, b})
	if err != nil {
		t.Fatalf("跨任务 json 失败: %v", err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(jout.Body, &arr); err != nil || len(arr) != 2 {
		t.Fatalf("跨任务 json 应为 2 元素数组: %v", err)
	}

	if _, err := CrossTaskExport(ctx, store, blobs, FormatMD, nil); err == nil {
		t.Fatalf("空任务清单应拒绝")
	}
}

// readCSV 解析(BOM 剥离)。
func readCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	b := bytes.TrimPrefix(body, []byte("\uFEFF"))
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("解析 csv 失败: %v", err)
	}
	return rows
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
