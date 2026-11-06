package server

// P4-36 面板按需取数(问题清单 #57 ②)的服务端测试:复现步骤分段端点
// 的翻页/游标、详情响应不带步骤数组(轻量)、列表摘要走投影。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p436SeedRepro 造一条带 n 步定稿的判断(直接走账本 Append,投影写侧
// 同事务维护)。
func p436SeedRepro(t *testing.T, store *rings.Store, task string, claimID string, steps int) {
	t.Helper()
	ctx := context.Background()
	seed := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "未授权读取", Status: "confirmed",
			Detail: map[string]any{"unit_id": "u1", "step_id": "s-1"}}},
	}
	reproSteps := make([]rings.ReproStep, 0, steps)
	for i := 1; i <= steps; i++ {
		reproSteps = append(reproSteps, rings.ReproStep{
			Seq: i, StepID: "s-1", UnitID: "u1", Method: "GET",
			URL: "http://t/api", Status: 200,
			Short: "拿数据", Desc: "直接请求该接口", Purpose: "触发未授权读取",
		})
	}
	seed = append(seed, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
		ClaimID: claimID, Type: rings.TestClaimRecRepro, Severity: "high",
		Narrative: "在首页发现。", Steps: reproSteps,
		Process: []rings.ReproProcessRef{{ExchangeID: "ex-x", BlobRef: "sha256:px"}},
	}})
	if _, err := store.Append(ctx, task, seed...); err != nil {
		t.Fatalf("P4-36 造数失败: %v", err)
	}
}

// TestP436ReproPagination 复现步骤分段:offset/limit/has_more 翻页取齐,
// 步骤字段齐全;详情响应不带步骤数组(轻量),列表带 repro_steps 计数。
func TestP436ReproPagination(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"分段验证"}`, http.StatusOK, &created)
	task := created["id"]
	const total = 19
	p436SeedRepro(t, store, task, "tc-page", total)

	// 详情:轻量(无步骤数组),小字段与计数在(repro_steps 是列表侧字段)。
	var detail struct {
		Repro *struct {
			Steps        []any  `json:"steps"`
			ProcessCount int    `json:"process_count"`
			Severity     string `json:"severity"`
		} `json:"repro"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-page", http.StatusOK, &detail)
	if detail.Repro == nil || detail.Repro.Steps != nil || detail.Repro.ProcessCount != 1 || detail.Repro.Severity != "high" {
		t.Fatalf("详情应轻量(无步骤数组)且带计数: %+v", detail.Repro)
	}
	var list []struct {
		ID         string `json:"id"`
		ReproSteps int    `json:"repro_steps"`
	}
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &list)
	if len(list) != 1 || list[0].ReproSteps != total {
		t.Fatalf("列表计数应 %d: %+v", total, list)
	}

	// 分段:默认页(reproPageSize=8)翻页取齐 19 步。
	var page struct {
		Steps []struct {
			Seq     int    `json:"seq"`
			Short   string `json:"short"`
			Desc    string `json:"desc"`
			Purpose string `json:"purpose"`
		} `json:"steps"`
		Offset  int  `json:"offset"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	collected := 0
	offset := 0
	for i := 0; i < 10; i++ {
		get(t, h, "/api/tasks/"+task+"/claims/tc-page/repro?offset="+itoaP436(offset), http.StatusOK, &page)
		if page.Total != total {
			t.Fatalf("total 应 %d: %+v", total, page)
		}
		if page.Offset != offset {
			t.Fatalf("offset 回显不符: %+v", page)
		}
		collected += len(page.Steps)
		if !page.HasMore {
			break
		}
		if len(page.Steps) == 0 {
			t.Fatalf("has_more 但本页空: %+v", page)
		}
		offset += len(page.Steps)
	}
	if collected != total {
		t.Fatalf("翻页取齐应 %d 步,实得 %d", total, collected)
	}
	if page.Steps[0].Short != "拿数据" || page.Steps[0].Desc == "" || page.Steps[0].Purpose == "" {
		t.Fatalf("步骤三层字段应齐全: %+v", page.Steps[0])
	}

	// 显式 limit 与越界 offset。
	get(t, h, "/api/tasks/"+task+"/claims/tc-page/repro?offset=0&limit=3", http.StatusOK, &page)
	if len(page.Steps) != 3 || !page.HasMore {
		t.Fatalf("显式 limit=3 应 3 步且有后页: %+v", page)
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-page/repro?offset=999", http.StatusOK, &page)
	if len(page.Steps) != 0 || page.HasMore {
		t.Fatalf("越界 offset 应空页: %+v", page)
	}

	// 幽灵 404。
	get(t, h, "/api/tasks/"+task+"/claims/tc-404/repro", http.StatusNotFound, new(map[string]any))
}

// TestP436GiantRecordSlim 巨记录口径:定稿携带大 Variants(旧口径内嵌
// 原文形态的等比缩小)时,详情与列表响应不含变体大数组——打开不随
// 记录体积放大(活体验收的大账本场景由确定性构造覆盖同一路径)。
func TestP436GiantRecordSlim(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"巨件验证"}`, http.StatusOK, &created)
	task := created["id"]

	variants := make([]rings.ReproStep, 0, 64)
	big := strings.Repeat("curl -sS http://t/api?filler=", 400) // 每条变体内嵌大操作串
	for i := 0; i < 64; i++ {
		variants = append(variants, rings.ReproStep{
			Seq: i + 1, StepID: "s-1", UnitID: "u1", Op: big, Method: "GET", Status: 200,
		})
	}
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-giant", NodeType: rings.NodeTestClaim, Title: "未授权读取", Status: "confirmed",
			Detail: map[string]any{"unit_id": "u1", "step_id": "s-1"}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-giant", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{{Seq: 1, StepID: "s-1", UnitID: "u1", Method: "GET", Status: 200,
				Short: "拿数据", Desc: "直接请求", Purpose: "触发"}},
			Variants: variants,
		}},
	); err != nil {
		t.Fatalf("巨件造数失败: %v", err)
	}

	// 列表与详情响应体不随变体数组放大(投影只存计数)。
	rec := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/claims", "")
	if strings.Contains(rec.Body.String(), "filler=") {
		t.Fatalf("列表响应不应携带变体操作串(体积 %d)", rec.Body.Len())
	}
	rec = doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/claims/tc-giant", "")
	if strings.Contains(rec.Body.String(), "filler=") {
		t.Fatalf("详情响应不应携带变体操作串(体积 %d)", rec.Body.Len())
	}
	var detail struct {
		Repro *struct {
			VariantsCount int `json:"variants_count"`
		} `json:"repro"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("解码详情: %v", err)
	}
	if detail.Repro == nil || detail.Repro.VariantsCount != 64 {
		t.Fatalf("变体计数应为 64: %+v", detail.Repro)
	}
}

func itoaP436(n int) string { return strconv.Itoa(n) }
