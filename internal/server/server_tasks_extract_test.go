package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
)

// ---- P2-08f ①:建任务"从描述提取"(单发模型调用)+ goals/约束随建任务落库 ----

// TestTaskExtractAPI 提取端点:未配模型 503;脚本模型回复经校验回填
// (非法条目丢弃并记 issues,不静默丢);空描述/超长描述 400。
func TestTaskExtractAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()

	// 未配模型:503 + 指引。
	post(t, h, "POST", "/api/tasks/extract", `{"text":"授权描述"}`,
		http.StatusServiceUnavailable, &struct{}{})

	// 脚本模型:回复带围栏与杂文;含合法/非法条目混合;目标混排对象
	// (带判定口径)与裸字符串两种形态(P3-14b ④ 容错)。
	content := "以下是提取结果:\n```json\n" + `{"title":"某授权目标测试","assets":[
		{"class":"service","title":"http://10.10.0.5:8080"},
		{"class":"service","title":"10.10.0.6"},
		{"class":"乱类","title":"x.example.com"}],
	"goals":[{"title":"提取 users 表全部账号与口令哈希","pred":"拿到 users 表全部行的账号与哈希"},
		"确认登录面暴露情况"],
	"constraints":[
		{"action":"上传文件","disposition":"forbid","assets":["http://10.10.0.5:8080"],"patterns":["(?i)\\bcurl\\b[^|;&]*(\\s-T\\b|--upload-file)"],"basis":"不允许上传文件"},
		{"action":"爆破","disposition":"allow","quota":20,"basis":"爆破类操作最多 20 次"},
		{"action":"删库","disposition":"sometimes","basis":"坏处置"},
		{"action":"读取","disposition":"forbid","quota":3,"basis":"禁止带次数"},
		{"action":"探测","disposition":"forbid","patterns":["("],"basis":"坏正则"}]}
` + "```"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconv.Quote(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":8}}`))
	}))
	t.Cleanup(endpoint.Close)
	if err := failover.SaveModelConfig(context.Background(), srv.Store, failover.ModelConfig{
		Name: "ex", Format: "openai", BaseURL: endpoint.URL, APIKey: "k",
		Model: "m-ex", StreamDisabled: true,
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := failover.SetActive(context.Background(), srv.Store, failover.RolePrimary, "ex", "creator"); err != nil {
		t.Fatal(err)
	}
	srv.Chain = failover.NewChain(srv.Store, failover.EnvConfig{})

	var res extractReply
	post(t, h, "POST", "/api/tasks/extract", `{"text":"授权对 10.10.0.5 与 10.10.0.6 测试,不允许上传,爆破最多 20 次。"}`,
		http.StatusOK, &res)
	if res.Title != "某授权目标测试" {
		t.Fatalf("标题应回填: %+v", res)
	}
	if len(res.Assets) != 2 || res.Assets[0].Class != "service" || res.Assets[1].Class != "host" {
		// 注:脚本回复里第二资产类给的是 service,校验按模型输出原样收
		//(类在 12 词表内即收,不替模型改判);只丢弃词表外的乱类。
		if len(res.Assets) != 2 {
			t.Fatalf("应恰 2 条资产(乱类丢弃): %+v", res.Assets)
		}
	}
	if len(res.Goals) != 2 || !strings.Contains(res.Goals[0].Title, "users 表") ||
		!strings.Contains(res.Goals[0].Pred, "全部行") || !strings.Contains(res.Goals[1].Title, "登录面") {
		t.Fatalf("目标应回填(对象带判定口径,裸串按无口径收): %+v", res.Goals)
	}
	if len(res.Constraints) != 4 {
		t.Fatalf("应恰 4 条约束(坏处置丢弃;禁止带次数的去除次数后收;坏正则丢弃该条正则后收): %+v", res.Constraints)
	}
	upload := res.Constraints[0]
	if upload.Disposition != "forbid" || len(upload.Patterns) != 1 || !strings.Contains(upload.Basis, "不允许上传") {
		t.Fatalf("上传约束不符: %+v", upload)
	}
	brute := res.Constraints[1]
	if brute.Disposition != "allow" || brute.Quota != 20 {
		t.Fatalf("爆破约束不符: %+v", brute)
	}
	// 修正后仍收的条目:读取(次数已去除)、探测(正则已丢弃)。
	if res.Constraints[2].Action != "读取" || res.Constraints[2].Quota != 0 {
		t.Fatalf("禁止约束不得携带次数: %+v", res.Constraints[2])
	}
	if res.Constraints[3].Action != "探测" || len(res.Constraints[3].Patterns) != 0 {
		t.Fatalf("坏正则应整条丢弃只留约束: %+v", res.Constraints[3])
	}
	// issues:乱类资产 / 坏处置 / 禁止带次数 / 坏正则 = 4 条。
	if len(res.Issues) != 4 {
		t.Fatalf("应记 4 条 issues(不静默丢): %+v", res.Issues)
	}

	// 空描述 400;超长 400。
	post(t, h, "POST", "/api/tasks/extract", `{"text":"  "}`, http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/extract", `{"text":"`+strings.Repeat("长", 8001)+`"}`, http.StatusBadRequest, &struct{}{})
}

// TestTaskCreateWithGoalsAndRules 建任务携带种子目标与提取确认的约束:
// goals 落 Goal 节点;约束范围(表单内资产标识)映射为种子资产 ID;
// 表单外引用按全部资产处理;建任务即唤醒(有目标无资产也唤醒)。
func TestTaskCreateWithGoalsAndRules(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	var created struct {
		ID string `json:"id"`
	}
	body := `{"title": "提取确认任务", "goals": ["确认登录面暴露情况", "  "],
		"assets": [{"title": "http://t-a:8080", "class": "service"}, {"title": "10.0.0.9", "class": "host"}],
		"roe": {"intent_rules": [
			{"action": "上传文件", "asset_ids": ["http://t-a:8080"], "disposition": "forbid", "note": "从授权描述提取:不允许上传"},
			{"action": "爆破", "asset_ids": ["表单外标识"], "disposition": "allow", "quota": 20}
		]}}`
	post(t, h, "POST", "/api/tasks", body, http.StatusOK, &created)
	taskID := created.ID
	if taskID == "" {
		t.Fatalf("建任务失败")
	}

	// goals:空白项丢弃,恰 1 个 active Goal 节点。
	goals, err := srv.Store.NodesByType(ctx, taskID, rings.NodeGoal, "")
	if err != nil || len(goals) != 1 {
		t.Fatalf("应恰 1 个种子目标: %v %d", err, len(goals))
	}
	if goals[0].Status != "active" || !strings.Contains(goals[0].Title, "登录面") {
		t.Fatalf("目标节点不符: %+v", goals[0])
	}

	// 约束落 RoE:范围映射到表单内资产 ID;表单外引用退空(= 全部)。
	roe, err := srv.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roe.IntentRules) != 2 {
		t.Fatalf("应恰 2 条任务级规则: %+v", roe.IntentRules)
	}
	assets, _ := srv.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	byTitle := map[string]string{}
	for _, a := range assets {
		byTitle[a.Title] = a.ID
	}
	upload := roe.IntentRules[0]
	if upload.Action != "上传文件" || len(upload.AssetIDs) != 1 || upload.AssetIDs[0] != byTitle["http://t-a:8080"] {
		t.Fatalf("上传约束范围应映射为种子资产 ID: %+v", upload)
	}
	brute := roe.IntentRules[1]
	if len(brute.AssetIDs) != 0 || brute.Quota != 20 {
		t.Fatalf("表单外范围应退空(全部资产): %+v", brute)
	}

	// 有目标无资产的任务同样唤醒(不在此断言唤醒计数——唤醒路径由
	// CreateTask 主径测试覆盖;这里钉住"不因无资产而 500")。
	post(t, h, "POST", "/api/tasks", `{"title": "仅目标任务", "goals": ["只要目标"]}`,
		http.StatusOK, &struct {
			ID string `json:"id"`
		}{})
}
