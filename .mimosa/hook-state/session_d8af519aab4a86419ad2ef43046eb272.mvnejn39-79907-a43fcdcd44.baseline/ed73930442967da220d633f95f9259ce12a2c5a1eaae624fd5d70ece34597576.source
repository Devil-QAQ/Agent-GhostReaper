package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ghostreaper/internal/failover"
)

// ---- P4-16:资产一键优化整理(守恒红线确定性把关) ----

// TestParseAssetsOrganize 守恒对账:新增实体丢弃、丢失实体按原文补回、
// 同义合并(协议/www./末尾斜杠)与拆行被接受、乱类兜底启发式归类、
// 输出去重。
func TestParseAssetsOrganize(t *testing.T) {
	lines := []string{
		"主站 https://example.com/ 和 example.com 是同一个",
		"10.10.0.5:8080、10.10.0.6, api.example.com",
		"某科技有限公司",
	}
	// 模型回复:合并同形、拆行、归类;夹带一个原文里没有的新增实体。
	reply := `{"assets":[
		{"class":"domain","title":"example.com"},
		{"class":"service","title":"10.10.0.5:8080"},
		{"class":"host","title":"10.10.0.6"},
		{"class":"subdomain","title":"api.example.com"},
		{"class":"organization","title":"某科技有限公司"},
		{"class":"subdomain","title":"admin.example.com"},
		{"class":"乱类","title":"10.10.0.6"}
	]}`
	res := parseAssetsOrganize(reply, lines)

	byTitle := map[string]string{}
	for _, a := range res.Assets {
		byTitle[a.Title] = a.Class
	}
	// 输出应恰 5 条:原文 5 个实体(example.com 同义形合一),新增的
	// admin.example.com 派生不过被丢弃;重复输出(乱类那条)去重。
	if len(res.Assets) != 5 {
		t.Fatalf("应恰 5 条(同义合并+新增丢弃+输出去重): %+v", res.Assets)
	}
	if byTitle["example.com"] != "domain" || byTitle["10.10.0.5:8080"] != "service" ||
		byTitle["api.example.com"] != "subdomain" || byTitle["某科技有限公司"] != "organization" {
		t.Fatalf("归类不符: %+v", byTitle)
	}
	// 乱类兜底:同条目二现给乱类,首条 host 已收,去重后以首条为准。
	if byTitle["10.10.0.6"] != "host" {
		t.Fatalf("重复输出的乱类不应覆盖首条: %+v", byTitle)
	}
	// issues:新增实体被丢弃 1 条;覆盖齐全(同义形规范化后相等),无补回。
	if len(res.Issues) != 1 || !strings.Contains(res.Issues[0], "admin.example.com") {
		t.Fatalf("应记 1 条新增丢弃: %+v", res.Issues)
	}
}

// TestParseAssetsOrganizeConservationLoss 零删除红线:模型丢了原文实体,
// 守恒把关按原文补回并记 issue;非标识整行(组织名)同样受保护。
func TestParseAssetsOrganizeConservationLoss(t *testing.T) {
	lines := []string{
		"example.com、1.2.3.4、backup.example.com",
		"某某股份公司",
	}
	// 回复只给了两个实体,丢了 backup.example.com 与某某股份公司。
	reply := `{"assets":[{"class":"domain","title":"example.com"},{"class":"host","title":"1.2.3.4"}]}`
	res := parseAssetsOrganize(reply, lines)

	got := map[string]bool{}
	for _, a := range res.Assets {
		got[a.Title] = true
	}
	if !got["example.com"] || !got["1.2.3.4"] {
		t.Fatalf("模型输出实体应在: %+v", res.Assets)
	}
	if !got["backup.example.com"] || !got["某某股份公司"] {
		t.Fatalf("丢失实体应按原文补回: %+v", res.Assets)
	}
	if len(res.Issues) != 2 {
		t.Fatalf("应记 2 条按原文保留: %+v", res.Issues)
	}
	// 补回行的类走启发式(域名形 → subdomain;非标识行 → other)。
	byTitle := map[string]string{}
	for _, a := range res.Assets {
		byTitle[a.Title] = a.Class
	}
	if byTitle["backup.example.com"] != "subdomain" || byTitle["某某股份公司"] != "other" {
		t.Fatalf("补回行启发式归类不符: %+v", byTitle)
	}
}

// TestParseAssetsOrganizeBadReply 无 JSON / 解析失败:整理为空 + issues
// 指引,不崩不编造。
func TestParseAssetsOrganizeBadReply(t *testing.T) {
	res := parseAssetsOrganize("没有对象", []string{"example.com"})
	if len(res.Assets) != 0 || len(res.Issues) != 1 || !strings.Contains(res.Issues[0], "没有 JSON") {
		t.Fatalf("空回复应如实呈现: %+v", res)
	}
	// 覆盖检查在解析失败分支不追加(issues 已指明整理为空,可重试)。
	res2 := parseAssetsOrganize("```json {broken", []string{"example.com"})
	if len(res2.Assets) != 0 || len(res2.Issues) != 1 {
		t.Fatalf("坏 JSON 应如实呈现: %+v", res2)
	}
}

// TestAssetsOrganizeAPI 端点:未配模型 503 指引;脚本模型全链——回复经
// 守恒把关回填;空输入 400。
func TestAssetsOrganizeAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()

	// 未配模型:503 + 指引。
	post(t, h, "POST", "/api/tasks/assets-organize", `{"lines":["example.com"]}`,
		http.StatusServiceUnavailable, &struct{}{})

	// 空输入 400。
	post(t, h, "POST", "/api/tasks/assets-organize", `{"lines":["  ",""]}`,
		http.StatusBadRequest, &struct{}{})

	// 脚本模型:回复带围栏;含一次同义合并与一个新增实体(应被丢弃)。
	content := "```json\n" + `{"assets":[
		{"class":"domain","title":"example.com"},
		{"class":"subdomain","title":"admin.example.com"}]}` + "\n```"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconv.Quote(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":8}}`))
	}))
	t.Cleanup(endpoint.Close)
	if err := failover.SaveModelConfig(context.Background(), srv.Store, failover.ModelConfig{
		Name: "org", Format: "openai", BaseURL: endpoint.URL, APIKey: "k",
		Model: "m-org", StreamDisabled: true,
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := failover.SetActive(context.Background(), srv.Store, failover.RolePrimary, "org", "creator"); err != nil {
		t.Fatal(err)
	}
	srv.Chain = failover.NewChain(srv.Store, failover.EnvConfig{})

	var res organizeReply
	post(t, h, "POST", "/api/tasks/assets-organize",
		`{"lines":["https://example.com/","example.com"]}`, http.StatusOK, &res)
	if len(res.Assets) != 1 || res.Assets[0].Title != "example.com" || res.Assets[0].Class != "domain" {
		t.Fatalf("同义形应合并为一条: %+v", res.Assets)
	}
	if len(res.Issues) != 1 || !strings.Contains(res.Issues[0], "admin.example.com") {
		t.Fatalf("新增实体应被丢弃并记 issue: %+v", res.Issues)
	}
}
