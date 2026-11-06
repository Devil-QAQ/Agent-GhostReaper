package report

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P4-35(问题清单 #57 ①):最小复现集与记录瘦身——① 新口径定稿(三层
// 结构 + Process 引用清单)的报告渲染与证据包全量;② 旧口径记录(存量
// 不动,Variants 过程流量)渲染回归与新旧同收;③ 计数与词形单测。

// seedP435Repro 铺新口径定稿:tc-1 最小集 1 步(三层齐全)+ Process
// 3 条引用(候选与未选中的对比交换全量);交换原文入 blob。replace 的
// 取代语义保证最新 repro 生效(seedRepro 先铺的旧式记录被覆盖)。
func seedP435Repro(t *testing.T, store *rings.Store, blobs *swarm.BlobStore, task string) {
	t.Helper()
	writeEx := func(id, method, url string, status int, body string) string {
		ex := mirror.Exchange{
			ID: id, Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s2", UnitID: "u2",
			Request: &mirror.Request{Method: method, URL: url, Proto: "HTTP/1.1", Host: "10.10.0.5:8080"},
			Response: &mirror.Response{
				Proto: "HTTP/1.1", Status: status, StatusText: "OK",
				Headers: []mirror.Header{{Name: "Content-Type", Value: "application/json"}},
				Body:    []byte(body),
			},
		}
		doc, err := json.Marshal(ex)
		if err != nil {
			t.Fatalf("序列化交换失败: %v", err)
		}
		ref, err := blobs.Write(doc)
		if err != nil {
			t.Fatalf("写入 blob 失败: %v", err)
		}
		return ref
	}
	// 候选:最终接口(最小集选中)与两条对比交换(未选中,只进过程清单)。
	finalRef := writeEx("ms-final", "GET", "http://10.10.0.5:8080/api/users", 200, `{"users":["admin"]}`)
	contrastRef := writeEx("ms-contrast", "GET", "http://10.10.0.5:8080/api/users?cb=1", 200, `{"users":["admin"]}`)
	baselineRef := writeEx("ms-baseline", "GET", "http://10.10.0.5:8080/api/users", 401, `{"error":"unauthorized"}`)
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
				Short:   "拿用户列表",
				Desc:    "向 10.10.0.5:8080 发 GET /api/users,无需凭据;返回 200 且响应含用户列表即复现",
				Purpose: "直接复现接口未授权访问",
				Op:      "http_probe {\"method\":\"GET\",\"url\":\"http://10.10.0.5:8080/api/users\"}",
				Method:  "GET", URL: "http://10.10.0.5:8080/api/users", Status: 200,
				BlobRef: finalRef, ExchangeID: "ms-final",
			}},
			Process: []rings.ReproProcessRef{
				{ExchangeID: "ms-baseline", BlobRef: baselineRef},
				{ExchangeID: "ms-contrast", BlobRef: contrastRef},
				{ExchangeID: "ms-final", BlobRef: finalRef},
			},
		}},
	); err != nil {
		t.Fatalf("铺新口径定稿失败: %v", err)
	}
}

// seedP435OldRepro 铺旧口径定稿(存量形态:无三层新字段,过程流量在
// Variants)——验证读侧渲染回归与新旧同收(旧记录零改动仍可导出)。
func seedP435OldRepro(t *testing.T, store *rings.Store, blobs *swarm.BlobStore, task string) {
	t.Helper()
	writeEx := func(id string) string {
		ex := mirror.Exchange{
			ID: id, Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s2", UnitID: "u2",
			Request:  &mirror.Request{Method: "GET", URL: "http://10.10.0.5:8080/old-backup/", Proto: "HTTP/1.1", Host: "10.10.0.5:8080"},
			Response: &mirror.Response{Proto: "HTTP/1.1", Status: 200, StatusText: "OK"},
		}
		doc, _ := json.Marshal(ex)
		ref, err := blobs.Write(doc)
		if err != nil {
			t.Fatalf("写入 blob 失败: %v", err)
		}
		return ref
	}
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
				Desc:   "请求 /old-backup,目标返回 200",
				Method: "GET", URL: "http://10.10.0.5:8080/old-backup/", Status: 200,
				BlobRef: writeEx("old-main"),
			}},
			Variants: []rings.ReproStep{
				{Seq: 0, StepID: "s2", UnitID: "u2", Method: "GET",
					URL: "http://10.10.0.5:8080/old-backup/?cb=1", Status: 200, BlobRef: writeEx("old-v1"), ExchangeID: "old-v1"},
				{Seq: 0, StepID: "s2", UnitID: "u2", Method: "GET",
					URL: "http://10.10.0.5:8080/old-backup/?cb=2", Status: 200, BlobRef: writeEx("old-v2"), ExchangeID: "old-v2"},
			},
		}},
	); err != nil {
		t.Fatalf("铺旧口径定稿失败: %v", err)
	}
}

func zipEntries(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("开 zip 失败: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("开 zip 条目 %s 失败: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

// TestP435ReproRenderNew 新口径定稿:报告三层结构 + 最小集与过程流量
// 计数 + 计数口径(ProcessUnlisted = Process − Steps)。
func TestP435ReproRenderNew(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "p435-new"
	seedReportScenario(t, store, task)
	seedP435Repro(t, store, blobs, task)

	md, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	for _, want := range []string{
		"复现步骤(检查归纳定稿 · 级别 high · 最小集 1 步 · 过程流量 2 条见证据包):",
		"① 拿用户列表",
		"做什么:向 10.10.0.5:8080 发 GET /api/users,无需凭据;返回 200 且响应含用户列表即复现",
		"作用:直接复现接口未授权访问",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺新口径片段 %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "轮反证 · 单位") {
		t.Fatalf("P4-35:轮次/单位机器元数据不进复现正文:\n%s", md)
	}
	// 计数口径:3 条过程 − 1 步最小集 = 2 条未列。
	cases, err := claimCases(context.Background(), store, blobs, task)
	if err != nil {
		t.Fatalf("装配卷宗失败: %v", err)
	}
	if n := cases["tc-1"].Repro.ProcessUnlisted(); n != 2 {
		t.Fatalf("未列过程计数应恰 2(3−1): %d", n)
	}
}

// TestP435ZipProcessNew 新口径证据包:最小集步骤 + 过程流量全量(引用
// 清单逐条回仓库取原文),两次导出字节级一致。
func TestP435ZipProcessNew(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "p435-zip"
	seedReportScenario(t, store, task)
	seedP435Repro(t, store, blobs, task)

	out1, err := TaskExport(context.Background(), store, blobs, task, FormatZip, nil)
	if err != nil {
		t.Fatalf("zip 导出失败: %v", err)
	}
	out2, err := TaskExport(context.Background(), store, blobs, task, FormatZip, nil)
	if err != nil {
		t.Fatalf("zip 二次导出失败: %v", err)
	}
	if !bytes.Equal(out1.Body, out2.Body) {
		t.Fatalf("相同账本两次 zip 导出必须字节级一致")
	}
	entries := zipEntries(t, out1.Body)
	for _, name := range []string{
		"evidence/tc-1/step-1.txt",
		"evidence/tc-1/process-1.txt",
		"evidence/tc-1/process-2.txt",
		"evidence/tc-1/process-3.txt",
	} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("zip 缺过程流量条目 %s;实有 %v", name, keysOf(entries))
		}
	}
	for _, name := range entries {
		if strings.Contains(name, "variant-") {
			t.Fatalf("新口径定稿不应再有 variant 条目: %v", keysOf(entries))
		}
	}
	// 过程条目:引用清单不带摘要,方法/URL/状态从仓库原文解出头行;
	// 正文全文保真(401 基线是未上面板的对比流量,证据包不丢)。
	if !strings.Contains(entries["evidence/tc-1/process-1.txt"],
		"# 证据 · 过程 1 · GET http://10.10.0.5:8080/api/users → 401") ||
		!strings.Contains(entries["evidence/tc-1/process-1.txt"], "unauthorized") {
		t.Fatalf("过程条目头行/全文不符:\n%s", entries["evidence/tc-1/process-1.txt"])
	}
	// 步骤条目仍带操作方法行(完整命令收证据包)。
	if !strings.Contains(entries["evidence/tc-1/step-1.txt"], "# 操作 http_probe") {
		t.Fatalf("步骤条目应带操作方法行:\n%s", entries["evidence/tc-1/step-1.txt"])
	}
}

// TestP435OldReproCompat 旧口径记录(存量不动)渲染回归:无短名退
// "步骤 N"、无作用行、过程流量计数走 Variants、zip 收 variant 条目。
func TestP435OldReproCompat(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "p435-old"
	seedReportScenario(t, store, task)
	seedP435OldRepro(t, store, blobs, task)

	md, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	for _, want := range []string{
		"最小集 1 步 · 过程流量 2 条见证据包",
		"步骤 1",
		"做什么:请求 /old-backup,目标返回 200",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("旧口径渲染缺 %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "作用:") {
		t.Fatalf("旧记录无作用字段,不编造:\n%s", md)
	}
	out, err := TaskExport(context.Background(), store, blobs, task, FormatZip, nil)
	if err != nil {
		t.Fatalf("zip 导出失败: %v", err)
	}
	entries := zipEntries(t, out.Body)
	for _, name := range []string{"evidence/tc-1/step-1.txt", "evidence/tc-1/variant-1.txt", "evidence/tc-1/variant-2.txt"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("旧口径 zip 缺条目 %s;实有 %v", name, keysOf(entries))
		}
	}
}

// TestP435StepHead 步骤标题词形:序号按列表位置,1~20 圈号,超出
// "N.",旧记录退"步骤 N"(存储 Seq 与呈现解耦)。
func TestP435StepHead(t *testing.T) {
	cases := []struct {
		pos   int
		short string
		want  string
	}{
		{1, "拿会话凭据", "① 拿会话凭据"},
		{2, "换票据", "② 换票据"},
		{20, "末步", "⑳ 末步"},
		{21, "超出", "21. 超出"},
		{3, "", "步骤 3"},
	}
	for _, c := range cases {
		got := reproStepHead(c.pos, ReproStepView{ReproStep: rings.ReproStep{Seq: c.pos, Short: c.short}})
		if got != c.want {
			t.Fatalf("pos=%d short=%q: 得 %q 期望 %q", c.pos, c.short, got, c.want)
		}
	}
}
