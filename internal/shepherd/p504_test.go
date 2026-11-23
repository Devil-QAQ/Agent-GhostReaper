package shepherd

// P5-04 牧者配发流量查询三件(问题清单 #94):追问原始报文/请求方法/
// 响应原文时直接从镜里捞——不再"账本里没有"式答不上。与虫群同款
// 单一实现(swarm.Traffic*Tool),防慢三规矩随说明下发。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestP504ShepherdTrafficTools 牧者工具面挂三件且行为与虫群同源:
// 搜必须限定 host、摘要不带正文、原文按编号取到。
func TestP504ShepherdTrafficTools(t *testing.T) {
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const task = "p504-shep"

	blobs := swarm.NewBlobStore(filepath.Join(dir, "blobs"))
	ex := mirror.Exchange{
		ID: "ms-shep.1", Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s9", UnitID: "u9",
		Request:  &mirror.Request{Method: "POST", URL: "http://t.example.com/api/login", Proto: "HTTP/1.1"},
		Response: &mirror.Response{Proto: "HTTP/1.1", Status: 200, StatusText: "OK", Body: []byte(`{"token":"sk-live-889900"}`)},
	}
	raw, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	ref, err := blobs.Write(raw)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror",
		Payload: rings.MirrorRecord{
			ExchangeID: "ms-shep.1", Type: rings.MirrorRecHTTP, StepID: "s9", UnitID: "u9",
			Method: "POST", URL: "http://t.example.com/api/login", Status: 200, BlobRef: ref,
		},
	}); err != nil {
		t.Fatalf("铺镜留痕失败: %v", err)
	}

	s := &Service{Store: store, Blobs: blobs}
	names := map[string]bool{}
	for _, tl := range s.toolset(task, "sm-x") {
		names[tl.Name()] = true
	}
	for _, want := range []string{"traffic_search", "traffic_get", "traffic_blob"} {
		if !names[want] {
			t.Fatalf("牧者工具面缺 %s(与虫群同款配发)", want)
		}
	}
	for _, keep := range []string{"read_progress", "read_findings", "read_units"} {
		if !names[keep] {
			t.Fatalf("牧者既有工具 %s 不得被挤掉", keep)
		}
	}

	call := func(name, args string) string {
		t.Helper()
		for _, tl := range s.toolset(task, "sm-x") {
			if tl.Name() != name {
				continue
			}
			res, err := tl.Call(ctx, json.RawMessage(args), nil)
			if err != nil {
				t.Fatalf("%s 调用失败: %v", name, err)
			}
			var b strings.Builder
			for _, blk := range res.Content {
				if blk.Type == "text" {
					b.WriteString(blk.Text)
				}
			}
			return b.String()
		}
		t.Fatalf("工具 %s 不在面内", name)
		return ""
	}

	// 搜:host 必填语义同款(无 host 拒绝);命中给轻量摘要。
	if got := call("traffic_search", `{"host":"t.example.com"}`); !strings.Contains(got, "ms-shep.1") || !strings.Contains(got, "status=200") {
		t.Fatalf("牧者流量搜索应命中:\n%s", got)
	}
	if strings.Contains(call("traffic_search", `{"host":"t.example.com"}`), "sk-live-889900") {
		t.Fatalf("摘要不得带正文")
	}
	// 取:按编号捞原始报文(请求方法/响应原文当场作答)。
	got := call("traffic_blob", `{"id":"ms-shep.1"}`)
	for _, want := range []string{"POST /api/login HTTP/1.1", `{"token":"sk-live-889900"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("原文应含 %q:\n%s", want, got)
		}
	}
	// 元数据:方法与状态。
	if got := call("traffic_get", `{"id":"ms-shep.1"}`); !strings.Contains(got, "POST http://t.example.com/api/login status=200") {
		t.Fatalf("元数据应含请求行摘要:\n%s", got)
	}
}
