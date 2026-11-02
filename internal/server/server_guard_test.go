package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-08e 面板 API:硬规则管理 + RoE 任务级规则存取与计数视图 ----

// TestGuardRulesAPI 硬规则端点:启动播种(New 内)、清单读取、编辑
// (启停)、删除内置记墓碑、校验拒绝。
func TestGuardRulesAPI(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	// New 已播种:清单可见,外泄管道组全部禁用。
	var list struct {
		Rules []swarm.HardRule `json:"rules"`
	}
	get(t, h, "/api/guard/rules", http.StatusOK, &list)
	if len(list.Rules) < 19 {
		t.Fatalf("播种清单应约 19 条以上,得到 %d", len(list.Rules))
	}
	exfilOn, bypassOn := 0, 0
	for _, r := range list.Rules {
		if r.Group == swarm.HardGroupExfilPipe && r.Enabled {
			exfilOn++
		}
		if r.Group == swarm.HardGroupProxyBypass && r.Enabled {
			bypassOn++
		}
		if !strings.HasPrefix(r.Name, swarm.HardRuleBuiltinPrefix) && isBuiltinHardRuleIDForTest(r.ID) {
			t.Fatalf("内置条目应带内置前缀: %+v", r)
		}
	}
	if exfilOn != 0 {
		t.Fatalf("外泄管道组应播种为禁用")
	}
	if bypassOn != 0 {
		t.Fatalf("代理旁路组应播种为禁用(P4-21)")
	}

	// 编辑内置条目:禁用 HTTP 删除方法请求(同 ID 覆盖)。
	post(t, h, "POST", "/api/guard/rules", `{
		"id": "hr-http-delete", "group": "HTTP 破坏请求", "name": "[内置] HTTP 删除方法请求",
		"pattern": "(?i)\\b(curl|wget)\\b[^|;&]*(-X\\s*|--request\\s*|--method\\s*)DELETE\\b",
		"tool_scope": "bash", "enabled": false}`, http.StatusOK, &struct{}{})
	rules, _ := swarm.ListHardRules(ctx, store)
	for _, r := range rules {
		if r.ID == "hr-http-delete" && r.Enabled {
			t.Fatalf("编辑后应禁用")
		}
	}
	// 播种幂等不覆盖用户改动。
	if err := swarm.SeedHardRules(ctx, store); err != nil {
		t.Fatalf("重播种失败: %v", err)
	}
	rules, _ = swarm.ListHardRules(ctx, store)
	for _, r := range rules {
		if r.ID == "hr-http-delete" && r.Enabled {
			t.Fatalf("播种覆盖了用户禁用")
		}
	}

	// 校验拒绝:坏正则 / 冒用内置前缀。
	post(t, h, "POST", "/api/guard/rules", `{"group":"系统破坏命令","name":"x","pattern":"(","tool_scope":"bash","enabled":true}`,
		http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/guard/rules", `{"group":"系统破坏命令","name":"[内置] 冒名","pattern":"a","tool_scope":"bash","enabled":true}`,
		http.StatusBadRequest, &struct{}{})

	// 自建规则 + 删除:自建直接删;内置删除记墓碑(重启播种不复活)。
	var saved struct {
		ID string `json:"id"`
	}
	post(t, h, "POST", "/api/guard/rules", `{"group":"批量清空路径","name":"自建 清理临时目录","pattern":"\\bclean-tmp\\b","tool_scope":"bash","enabled":true}`,
		http.StatusOK, &saved)
	if saved.ID == "" {
		t.Fatalf("自建应返回 ID")
	}
	// 删除内置条目。
	post(t, h, "DELETE", "/api/guard/rules/hr-mkfs", "", http.StatusOK, &struct{}{})
	rules, _ = swarm.ListHardRules(ctx, store)
	for _, r := range rules {
		if r.ID == "hr-mkfs" {
			t.Fatalf("删除后不应在清单")
		}
	}
	// 重启播种:墓碑挡住复活。
	if err := swarm.SeedHardRules(ctx, store); err != nil {
		t.Fatalf("重播种失败: %v", err)
	}
	rules, _ = swarm.ListHardRules(ctx, store)
	for _, r := range rules {
		if r.ID == "hr-mkfs" {
			t.Fatalf("删除的内置规则被复活")
		}
	}
	// 删除不存在的规则:404。
	post(t, h, "DELETE", "/api/guard/rules/hr-none", "", http.StatusNotFound, &struct{}{})
}

func isBuiltinHardRuleIDForTest(id string) bool {
	for _, r := range []string{
		"hr-rm-root", "hr-rm-home", "hr-mkfs", "hr-dd-disk", "hr-redir-disk",
		"hr-shutdown", "hr-forkbomb", "hr-chmod-root", "hr-chown-root",
		"hr-drop-table", "hr-drop-db", "hr-truncate", "hr-delete-nowhere", "hr-alter-drop",
		"hr-http-delete", "hr-http-purge",
		"hr-find-delete", "hr-find-exec-rm", "hr-shred", "hr-rm-star",
		"hr-devtcp", "hr-nc-exec", "hr-pipe-exfil",
	} {
		if id == r {
			return true
		}
	}
	return false
}

// TestRoEIntentRulesAPI RoE 快照携带任务级规则:提交校验(词表 / 资产
// 存在性)、读回带资产标题与已用计数。
func TestRoEIntentRulesAPI(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	// 建任务 + 资产。
	body := `{"title": "拦截规则任务", "goals": ["种子目标"], "roe": {"allowed_tools": [], "denied_tools": []}, "assets": [{"title": "http://t-a:8080", "class": "service"}]}`
	var created struct {
		ID string `json:"id"`
	}
	post(t, h, "POST", "/api/tasks", body, http.StatusOK, &created)
	taskID := created.ID
	if taskID == "" {
		t.Fatalf("建任务失败")
	}
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil || len(assets) != 1 {
		t.Fatalf("资产未落图: %v %d", err, len(assets))
	}
	assetID := assets[0].ID

	// 提交任务级规则(快照)。
	roeBody := `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"allowed_tools": [], "denied_tools": [], "approval_threshold": "",
		"intent_rules": [
			{"id": "ir-1", "action": "数据读取", "asset_ids": ["` + assetID + `"], "disposition": "forbid"},
			{"id": "ir-2", "action": "上传", "asset_ids": ["` + assetID + `"], "disposition": "allow", "quota": 2}
		], "note": "加拦截规则"}`
	var view struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe", roeBody, http.StatusOK, &view)
	if len(view.IntentRules) != 2 {
		t.Fatalf("应读回 2 条规则: %+v", view.IntentRules)
	}
	if view.IntentRules[0].AssetTitles[0] != "http://t-a:8080" || view.IntentRules[1].Used != 0 {
		t.Fatalf("规则视图不符: %+v", view.IntentRules)
	}

	// 校验拒绝:空动作描述 / 处置非法 / 资产不在图上 / 禁止携带上限
	//(动作描述为自由文本,"飞天"也合法——不填正则且非词表词时不生效)。
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe",
		`{"intent_rules": [{"id": "x1", "action": "  ", "disposition": "forbid"}]}`,
		http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe",
		`{"intent_rules": [{"id": "x2", "action": "上传", "disposition": "sometimes"}]}`,
		http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe",
		`{"intent_rules": [{"id": "x3", "action": "上传", "asset_ids": ["a-nope"], "disposition": "forbid"}]}`,
		http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe",
		`{"intent_rules": [{"id": "x4", "action": "上传", "disposition": "forbid", "quota": 3}]}`,
		http.StatusBadRequest, &struct{}{})

	// 计数视图:入两条 quota_count 后读回 Used=2。
	for i := 1; i <= 2; i++ {
		if _, err := store.Append(ctx, taskID, rings.Event{
			Kind: rings.EventGuardRecord, Actor: "swarm:u1",
			Payload: rings.GuardRecord{StepID: "s1", UnitID: "u1", Type: rings.GuardRecQuotaCount,
				RuleID: "ir-2", Action: "上传", Nth: i, ToolName: "bash"},
		}); err != nil {
			t.Fatalf("入计数失败: %v", err)
		}
	}
	var view2 struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	get(t, h, "/api/tasks/"+taskID+"/roe", http.StatusOK, &view2)
	for _, r := range view2.IntentRules {
		if r.ID == "ir-2" && r.Used != 2 {
			t.Fatalf("计数视图不符: %+v", r)
		}
		if r.ID == "ir-1" && r.Used != 0 {
			t.Fatalf("禁止规则不应有计数: %+v", r)
		}
	}

	// 增删随快照:提交空规则清单 = 全部删除。
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe", `{"intent_rules": []}`, http.StatusOK, &view2)
	if len(view2.IntentRules) != 0 {
		t.Fatalf("空快照应清空规则")
	}
}

// TestRoERuleCRUDAPI 任务级规则逐条增删改(审核迭代):每笔变更落一笔
// roe.updated 快照、唤醒主宰、返回新视图;非法词表 / 资产不在图 /
// 未知规则 ID 受控拒绝。
func TestRoERuleCRUDAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	var created struct {
		ID string `json:"id"`
	}
	post(t, h, "POST", "/api/tasks", `{"goals":["种子目标"],"title": "逐条规则任务", "assets": [{"title": "http://t-a:8080", "class": "service"}, {"title": "http://t-b:90", "class": "service"}]}`,
		http.StatusOK, &created)
	taskID := created.ID
	assets, _ := srv.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if len(assets) != 2 {
		t.Fatalf("资产未落图: %d", len(assets))
	}
	aID := assets[0].ID

	// 新增(自由文本动作描述 + 判定正则)。
	var view struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"读后台数据","asset_ids":["`+aID+`"],"disposition":"forbid",
		  "patterns":["(?i)\\bcurl\\b[^|;&]*\\s-G\\b"],"note":"别读A"}`,
		http.StatusOK, &view)
	if len(view.IntentRules) != 1 || view.IntentRules[0].Action != "读后台数据" ||
		view.IntentRules[0].AssetTitles[0] != "http://t-a:8080" ||
		len(view.IntentRules[0].Patterns) != 1 {
		t.Fatalf("新增后视图不符: %+v", view.IntentRules)
	}
	rid := view.IntentRules[0].ID
	if rid == "" {
		t.Fatalf("服务端应生成规则 ID")
	}

	// 编辑(同 ID 覆盖:改为允许 + 次数上限,去正则走词表兼容)。
	// 注意每次解码用新变量:json.Unmarshal 复用既有 slice 底层数组,
	// JSON 缺省字段会保留旧值(踩过)。
	var viewEdit struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules/"+rid,
		`{"action":"上传","asset_ids":["`+aID+`"],"disposition":"allow","quota":3}`,
		http.StatusOK, &viewEdit)
	if len(viewEdit.IntentRules) != 1 || viewEdit.IntentRules[0].Action != "上传" || viewEdit.IntentRules[0].Quota != 3 ||
		len(viewEdit.IntentRules[0].Patterns) != 0 {
		t.Fatalf("编辑后视图不符: %+v", viewEdit.IntentRules)
	}

	// 删除。
	var viewDel struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	post(t, h, "DELETE", "/api/tasks/"+taskID+"/roe/rules/"+rid, "", http.StatusOK, &viewDel)
	if len(viewDel.IntentRules) != 0 {
		t.Fatalf("删除后应清空: %+v", viewDel.IntentRules)
	}

	// 三笔变更 = 三笔 roe.updated 事件(R7:每笔增删改各自留痕)。
	evs, err := srv.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	roeUps := 0
	for _, ev := range evs {
		if ev.Kind == rings.EventRoeUpdated {
			roeUps++
		}
	}
	if roeUps != 3 {
		t.Fatalf("应恰 3 笔 roe.updated(新增/编辑/删除),得到 %d", roeUps)
	}

	// 受控拒绝:空描述 / 坏正则 / 资产不在图 / 未知规则 ID。
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"  ","disposition":"forbid"}`, http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"自定义动作","disposition":"forbid","patterns":["("]}`, http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"飞天的动作","disposition":"forbid"}`, http.StatusOK, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"上传","asset_ids":["a-nope"],"disposition":"forbid"}`, http.StatusBadRequest, &struct{}{})
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules/ir-none",
		`{"action":"上传","disposition":"forbid"}`, http.StatusNotFound, &struct{}{})
	post(t, h, "DELETE", "/api/tasks/"+taskID+"/roe/rules/ir-none", "", http.StatusNotFound, &struct{}{})
}

// TestRoERulePatternsAPI 描述→正则生成(审核迭代三轮):单发模型调用,
// 回填可编译正则;未配模型 503(指引手写/内置词);脏回复如实空清单;
// 生成不落库。CRUD 的正则编译校验在 TestRoERuleCRUDAPI。
func TestRoERulePatternsAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	var created struct {
		ID string `json:"id"`
	}
	post(t, h, "POST", "/api/tasks", `{"goals":["种子目标"],"title": "生成任务", "assets": [{"title": "http://t-a:8080", "class": "service"}]}`,
		http.StatusOK, &created)
	taskID := created.ID

	// 未配模型:503 + 指引。
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules/patterns",
		`{"text":"上传文件"}`, http.StatusServiceUnavailable, &struct{}{})

	// 脚本模型:返回正则数组(含一条编译不过的,应被丢弃)。
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := `["(?i)\\bcurl\\b[^|;&]*(\\s-T\\b|--upload-file)", "("]`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + strconv.Quote(content) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":8}}`))
	}))
	t.Cleanup(endpoint.Close)
	if err := failover.SaveModelConfig(ctx, srv.Store, failover.ModelConfig{
		Name: "ex", Format: "openai", BaseURL: endpoint.URL, APIKey: "k",
		Model: "m-ex", StreamDisabled: true,
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := failover.SetActive(ctx, srv.Store, failover.RolePrimary, "ex", "creator"); err != nil {
		t.Fatal(err)
	}
	srv.Chain = failover.NewChain(srv.Store, failover.EnvConfig{})

	var res struct {
		Patterns []string `json:"patterns"`
	}
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules/patterns",
		`{"text":"上传文件到目标"}`, http.StatusOK, &res)
	if len(res.Patterns) != 1 || !strings.Contains(res.Patterns[0], "-T") {
		t.Fatalf("应只回填可编译正则: %+v", res.Patterns)
	}
	// 生成不落库。
	roe, _ := srv.Store.CurrentRoE(ctx, taskID)
	if len(roe.IntentRules) != 0 {
		t.Fatalf("生成不得落库")
	}
	// 空描述 400。
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules/patterns",
		`{"text":"  "}`, http.StatusBadRequest, &struct{}{})
}

// TestRoERuleNotifyTargeted 定向告知 E2E(审核迭代核心机制):规则变更只
// 投给"范围与步骤相交"的在飞单位——单位甲(covers 资产 A)收到舵杆
// 告知,单位乙(covers 资产 B)零感知(无任何 steer 记录)。判定层与
// 告知层互不依赖,本测试只证告知投递。
func TestRoERuleNotifyTargeted(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	var created struct {
		ID string `json:"id"`
	}
	post(t, h, "POST", "/api/tasks", `{"goals":["种子目标"],"title": "定向告知任务", "assets": [{"title": "http://t-a:8080", "class": "service"}, {"title": "http://t-b:90", "class": "service"}]}`,
		http.StatusOK, &created)
	taskID := created.ID
	assets, _ := srv.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if len(assets) != 2 {
		t.Fatalf("资产未落图")
	}
	byTitle := map[string]string{}
	for _, a := range assets {
		byTitle[a.Title] = a.ID
	}

	// 脚本模型:每个单位先跑一条 3 秒慢命令(挂着,保持单位在飞),
	// 之后收口。两步分别 covers 资产 A / B。
	model := newSModel(func(rr *srecorded) sreply {
		if sSeenToolResult(rr, "bash", "SLOW-OK") {
			return sreply{Text: "完成。"}
		}
		return sreply{ToolCalls: []struct {
			ID   string
			Name string
			Args string
		}{{
			ID: "b1", Name: "bash",
			Args: `{"command":"sleep 3 && echo SLOW-OK && cat note-slow.txt 2>/dev/null; echo SLOW-OK"}`,
		}}}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	root := srv.Manager.RootDir()
	ex := swarm.NewExecutor(srv.Store, prov, root, swarm.Budget{MaxTurns: 4, RunSeconds: 60, WrapupMaxTurns: 1})
	ex.Gate = srv.Gate
	ex.Helm = srv.Helm
	srv.Swarm = ex

	// 两步并行跑(covers A / covers B)。
	step := func(id, assetID string) {
		if _, err := srv.Store.Append(ctx, taskID,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: id, NodeType: rings.NodeStep, Title: "慢演练 " + id, Status: "dispatched",
				Detail: map[string]any{"goal_id": "g1", "action_level": "主动测试"}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeCovers, FromNode: id, ToNode: assetID}},
		); err != nil {
			t.Fatal(err)
		}
	}
	step("sa", byTitle["http://t-a:8080"])
	step("sb", byTitle["http://t-b:90"])
	for _, sid := range []string{"sa", "sb"} {
		go func(id string) { _, _ = ex.RunStep(ctx, taskID, id) }(sid)
	}

	// 等两单位都在飞。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(ex.RunningUnitSteps(taskID)) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := len(ex.RunningUnitSteps(taskID)); got != 2 {
		t.Fatalf("应 2 个在飞单位,得到 %d", got)
	}

	// 规则变更:只圈资产 A(禁止上传)。
	var view struct {
		IntentRules []intentRuleOut `json:"intent_rules"`
	}
	post(t, h, "POST", "/api/tasks/"+taskID+"/roe/rules",
		`{"action":"上传","asset_ids":["`+byTitle["http://t-a:8080"]+`"],"disposition":"forbid"}`,
		http.StatusOK, &view)

	// 等舵杆记账出现:sa 有,b 无。
	var saSteer, sbSteer bool
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !saSteer {
		evs, _ := srv.Store.Events(ctx, taskID, 0, 0)
		saSteer, sbSteer = false, false
		for _, ev := range evs {
			if ev.Kind != rings.EventSteerRecord {
				continue
			}
			var rec rings.SteerRecord
			if json.Unmarshal(ev.Payload, &rec) == nil {
				if rec.StepID == "sa" {
					saSteer = true
				}
				if rec.StepID == "sb" {
					sbSteer = true
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !saSteer {
		t.Fatalf("范围相交单位 sa 应收到定向告知(steer 排队)")
	}
	if sbSteer {
		t.Fatalf("范围外单位 sb 不得收到任何告知(零污染)")
	}
	// 等两单位自然收口(不熔断、不被打扰地跑完)。
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(ex.RunningUnitSteps(taskID)) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := func(id string) string {
		n, err := srv.Store.Node(ctx, taskID, id)
		if err != nil {
			return err.Error()
		}
		return n.Status
	}
	if st("sa") != "done" || st("sb") != "done" {
		t.Fatalf("两单位应不受影响跑完: sa=%s sb=%s", st("sa"), st("sb"))
	}
}
