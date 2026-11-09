package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-10:报告 API 与总验收 ----

// TestServerReportAPI 报告端点:text/markdown、内容确定性(两次请求
// 字节级一致)、不存在任务 404。
func TestServerReportAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	ctx := context.Background()

	// 铺一份带 Confirmed 发现与完成终态的账本。
	if _, err := store.Append(ctx, "rep-api",
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "报告 API 验证"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "备份目录匿名可读", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: "web1"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "completed", Detail: map[string]any{"reason": "全部目标经仲裁者判定达成"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	getMd := func() (int, string, string) {
		rec := doReq(t, h, http.MethodGet, "/api/tasks/rep-api/report", "")
		return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
	}
	code, ctype, body := getMd()
	if code != http.StatusOK {
		t.Fatalf("GET report = %d, want 200(body %s)", code, body)
	}
	if !strings.Contains(ctype, "text/markdown") {
		t.Fatalf("Content-Type 应为 text/markdown: %s", ctype)
	}
	for _, want := range []string{
		"# 渗透测试报告 · 报告 API 验证",
		"任务状态:已完成(completed)",
		"### C-1 备份目录匿名可读 · 信息暴露",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("报告缺 %q:\n%s", want, body)
		}
	}
	_, _, body2 := getMd()
	if body2 != body {
		t.Fatalf("相同账本状态两次请求必须字节级一致")
	}

	// 不存在任务 404。
	var errBody map[string]string
	get(t, h, "/api/tasks/no-such/report", http.StatusNotFound, &errBody)
}

// TestServerP1ExitCriteriaE2E 总计划 P1 退出标准的全栈试跑(授权目标 =
// 本地 mock 靶站,通用规则 6):一次任务完整跑通六条——
//  1. 资产树生长(种子未覆盖 → covers 翻转、事实计数);
//  2. 漏洞进列表(一 Confirmed 一待确认,面板 claims API 可见);
//  3. 危险操作弹门(L2 意图的 bash 弹门,面板裁决,批准前零流量);
//  4. Goal 经仲裁者判定完成(裁决事件 + achieved 迁移归 arbiter);
//  5. 任务进入完成终态(面板 completed + 账本 completed 事件);
//  6. 成功导出 md 报告(端点内容含发现、搁置理由、完成态、审批审计)。
func TestServerP1ExitCriteriaE2E(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()
	const task = "trial-run"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()
	u, _ := url.Parse(mainURL)

	// 脚本模型:
	//   拆解:审计与主动测试两条意图未齐时派发,齐则空(状态感知);
	//   反证包:按"存在性声明:"行分支(备份=影响面成立/版本=不足);
	//   主动测试包(own intent 含"畸形请求"):一次 bash 打 /fuzz-probe
	//     (L2 → 弹门)→ 收口;
	//   审计包:port_probe → robots/备份/版本 → 两条 submit_test_claim
	//     → submit_candidate_fact → 收口。
	ownIntentRe := regexp.MustCompile(`(?m)^二、本步骤意图[^\n]*\n\s+([^\n]+)`)
	claimIDRe := regexp.MustCompile(`判断 (tc-[0-9a-f]+)`)
	stmtBackupRe := regexp.MustCompile(`(?m)^\s*存在性判断: .*备份目录`)
	model := newSModel(func(rr *srecorded) sreply {
		// P4-13 来龙去脉叙述会话(零工具,系统段带"来龙去脉"):
		// 回一段叙述 JSON(坏回复路径由 overmind 单测覆盖)。
		if len(rr.ToolsOffered) == 0 && strings.Contains(rr.System, "来龙去脉") {
			return sreply{Text: `{"narrative":"单位 u1 执行「审计主站敏感路径」时,在 /old-backup/ 的 200 响应正文里发现备份列举;u3 独立反证 1 轮排除竞争解释,仲裁者判定成立。"}`}
		}
		// P4-56 ⑤ 反证时间线人话会话(零工具,系统段带"人话三段"):
		// 回三段 JSON(素材包逐轮给出,坏回复路径由 overmind 单测覆盖)。
		if len(rr.ToolsOffered) == 0 && strings.Contains(rr.System, "人话三段") {
			return sreply{Text: `{"rounds":[{"round":1,"doubt":"会不会是 /old-backup/ 的列举来自共享缓存,而不是目标真实文件?","how":"绕开缓存直接请求 /old-backup/,连续 2 次都返回同样的备份文件列举","conclusion":"已排除"}]}`}
		}
		// P4-59 ① 判断摘要会话(零工具,系统段带"判断文本"):回报告级
		// 锚口径的摘要 JSON(坏回复路径由 overmind 单测覆盖)。
		if len(rr.ToolsOffered) == 0 && strings.Contains(rr.System, "判断文本") {
			return sreply{Text: `{"statement":"主站备份目录没有登录校验,任何知道地址的人都能下载备份文件","impact":"无需任何账号口令,可下载 1 个数据库备份压缩包","facts":[]}`}
		}
		if len(rr.ToolsOffered) == 0 {
			hasAudit := strings.Contains(rr.LastUser, "审计主站敏感路径")
			hasActive := strings.Contains(rr.LastUser, "畸形请求")
			if hasAudit && hasActive {
				return sreply{Text: `{"new_intents":[]}`}
			}
			return sreply{Text: fmt.Sprintf(
				`{"new_intents":[`+
					`{"intent":"审计主站敏感路径暴露(robots/备份目录/版本接口)","expected_observation":"敏感路径清单与内容证据","action_level":"只读探测","covers":[%q]},`+
					`{"intent":"发送畸形请求探测主站错误处理行为","expected_observation":"错误响应形态","action_level":"主动测试","covers":[%q]}]}`,
				assetArg(t, rr.LastUser), assetArg(t, rr.LastUser))}
		}
		if strings.Contains(rr.LastUser, "八、反证任务") {
			m := claimIDRe.FindStringSubmatch(rr.LastUser)
			if m == nil {
				return sreply{Text: "反证任务包缺判断 ID。"}
			}
			claimID := m[1]
			if stmtBackupRe.MatchString(rr.LastUser) {
				switch {
				case !sSeenToolResult(rr, "bash", "mock backup archive listing"):
					return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
						ID: "fb", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, mainURL)}}}
				case !sSeenToolResult(rr, "submit_falsify_result", "已记录"):
					return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
						ID: "fr1", Name: "submit_falsify_result",
						Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":true,"impact_level":"high","alternatives":[{"id":"alt1","refuted":true},{"id":"alt2","refuted":true},{"id":"alt3","refuted":true},{"id":"alt4","refuted":true}],"evidence":"独立重放 /old-backup/ 仍返回备份文件列举,影响面成立"}`, claimID)}}}
				default:
					return sreply{Text: "反证完成:存在性未推翻,影响面证据充分。"}
				}
			}
			switch {
			case !sSeenToolResult(rr, "bash", `"version":"2.4.1"`):
				return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
					ID: "fv", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, mainURL)}}}
			case !sSeenToolResult(rr, "submit_falsify_result", "已记录"):
				return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
					ID: "fr2", Name: "submit_falsify_result",
					Args: fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":false,"alternatives":[{"id":"alt1","refuted":true},{"id":"alt2","refuted":true},{"id":"alt3","refuted":true},{"id":"alt4","refuted":true}],"evidence":"独立重放仅返回版本号,影响面证据不足"}`, claimID)}}}
			default:
				return sreply{Text: "反证完成:存在性未推翻,但影响面证据不足。"}
			}
		}
		// P2-06b 批准续跑:审批结果是新的用户消息(tool 结果不占 user 位,
		// 续跑消息在余下会话里恒为 LastUser)——未成功则重发自己上一条
		// bash 调用(单次放行配额匹配后真实执行),已成功则收口。
		if strings.Contains(rr.LastUser, "审批结果 · 续跑") {
			if sSeenToolResult(rr, "bash", "404") {
				return sreply{Text: "主动测试完成:畸形请求的响应形态已取得。"}
			}
			if args, id, ok := sReissueLastToolCall(rr, "bash"); ok {
				return sreply{ToolCalls: []struct{ ID, Name, Args string }{{ID: id, Name: "bash", Args: args}}}
			}
		}
		own := ownIntentRe.FindStringSubmatch(rr.LastUser)
		if own != nil && strings.Contains(own[1], "畸形请求") {
			// L2 主动测试单位:bash 一次(弹门),批准后原会话续跑真实执行
			//(P2-06b:挂起暂拒不是终答,重试同一调用直到拿到真实结果)。
			switch {
			case !sSeenToolResult(rr, "bash", "404"):
				return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
					ID: "fz", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/fuzz-probe"}`, mainURL)}}}
			default:
				return sreply{Text: "主动测试完成:畸形请求的响应形态已取得。"}
			}
		}
		// 审计单位(L1)。
		switch {
		case !sSeenToolResult(rr, "port_probe", "state="):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "pp", Name: "port_probe", Args: fmt.Sprintf(`{"host":%q,"port":%s}`, u.Hostname(), u.Port())}}}
		case !sSeenToolResult(rr, "bash", "Disallow: /old-backup"):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/robots.txt"}`, mainURL)}}}
		case !sSeenToolResult(rr, "bash", "mock backup archive listing"):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d2", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, mainURL)}}}
		case !sSeenToolResult(rr, "bash", `"version":"2.4.1"`):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d3", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, mainURL)}}}
		case !hasSVarToolCall(rr, "submit_test_claim", "备份目录可公开匿名列举"):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d4", Name: "submit_test_claim",
				Args: fmt.Sprintf(`{"statement":"/old-backup/ 备份目录可公开匿名列举","impact":"备份文件可被未授权下载,存在敏感数据泄露","evidence":"curl 实测返回列举","asset_id":%q,"vulnclass":"信息暴露"}`, assetArg(t, rr.LastUser))}}}
		case !hasSVarToolCall(rr, "submit_test_claim", "公开暴露服务版本号"):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d5", Name: "submit_test_claim",
				Args: fmt.Sprintf(`{"statement":"/api/version 公开暴露服务版本号","impact":"版本号可支撑定向利用","evidence":"curl 实测返回 2.4.1","asset_id":%q,"vulnclass":"信息暴露"}`, assetArg(t, rr.LastUser))}}}
		case !sSeenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return sreply{ToolCalls: []struct{ ID, Name, Args string }{{
				ID: "d6", Name: "submit_candidate_fact",
				Args: fmt.Sprintf(`{"statement":"主站服务在线,HTTP 指纹已确认","evidence":"port_probe state=open;robots 已实测","asset_id":%q}`, assetArg(t, rr.LastUser))}}}
		default:
			return sreply{Text: "审计完成:两条声明与一条候选事实已提交。"}
		}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	gate := swarm.NewLedgerGate(store)
	ex := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	ex.Gate = gate
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&overmind.Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop(task)
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	// ---- 经面板建任务(种子表单,service 资产)+ 补谓词目标 ----
	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", fmt.Sprintf(
		`{"task_type":"redteam","goals":["种子目标"],"id":%q,"title":"P1 总验收试跑","assets":[{"class":"service","title":%q}]}`, task, mainURL),
		http.StatusOK, &created)
	var tree struct {
		Assets []struct {
			ID      string `json:"id"`
			Covered bool   `json:"covered"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	if len(tree.Assets) != 1 || tree.Assets[0].Covered {
		t.Fatalf("种子资产应恰 1 个且未覆盖: %+v", tree)
	}
	assetID := tree.Assets[0].ID
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/goals", fmt.Sprintf(
		`{"title":"摸清主站暴露面","predicate":{"op":"all_of","conditions":[`+
			`{"kind":"fact_confirmed","asset_id":%q,"min":1},`+
			`{"kind":"tool_observed","asset_id":%q,"tool":"port_probe","result_contains":"state=open","min":1}]}}`,
		assetID, assetID), http.StatusOK, &created)
	// P2-06b 三级处置 v2:默认门槛(仅增删改弹门)下 L2 主动测试自动放行;
	// 本节验证门槛收紧——先把审批门槛收到"主动测试及以上弹门"再唤醒。
	var roeUpdated map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe",
		`{"approval_threshold":"主动测试","note":"总验收收紧门槛"}`, http.StatusOK, &roeUpdated)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/wake", `{}`, http.StatusOK, &created)

	// ---- ③ 危险操作弹门:待决可见,批准前主动测试零触达 ----
	var pending []map[string]any
	waitUntil(t, 60*time.Second, "L2 主动测试弹门到达面板", func() bool {
		var body map[string]any
		get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
		pl, _ := body["pending"].([]any)
		pending = make([]map[string]any, 0, len(pl))
		for _, it := range pl {
			if m, ok := it.(map[string]any); ok {
				pending = append(pending, m)
			}
		}
		return len(pending) == 1
	})
	p := pending[0]
	if p["action_level"] != "主动测试" || p["tool_name"] != "bash" ||
		!strings.Contains(fmt.Sprint(p["tool_args"]), "/fuzz-probe") {
		t.Fatalf("待决请求应携带 L2 分级与实际参数: %+v", p)
	}
	if got := target.Hits("main", "/fuzz-probe"); got != 0 {
		t.Fatalf("面板批准前主动测试不得触达靶站,实际 %d 次", got)
	}
	rid := fmt.Sprint(p["id"])
	var decided map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+rid,
		`{"approved":true,"reason":"允许本次畸形请求测试"}`, http.StatusOK, &decided)
	waitUntil(t, 30*time.Second, "批准后主动测试真实触达靶站", func() bool {
		return target.Hits("main", "/fuzz-probe") >= 1
	})

	// ---- ⑤ 任务进入完成终态(反证轮收尾后) ----
	// P4-16:建任务必带目标——谓词目标由仲裁者判定达成后,种子目标
	//(无谓词,机器不可判)经面板人工标记达成,两目标齐达成后收场。
	waitUntil(t, 60*time.Second, "谓词目标经仲裁者判定达成", func() bool {
		for _, g := range goalNodes(t, store, task) {
			if strings.Contains(g.Title, "摸清主站暴露面") && g.Status == "achieved" {
				return true
			}
		}
		return false
	})
	for _, g := range goalNodes(t, store, task) {
		if g.Title == "种子目标" {
			post(t, h, http.MethodPost, "/api/tasks/"+task+"/goals/"+g.ID+"/achieve",
				`{"reason":"总验收目标随谓词目标一同收场"}`, http.StatusOK, &created)
		}
	}
	waitUntil(t, 90*time.Second, "任务进入完成终态", func() bool {
		var tasks []map[string]any
		get(t, h, "/api/tasks", http.StatusOK, &tasks)
		for _, tk := range tasks {
			if tk["id"] == task {
				return tk["status"] == "completed"
			}
		}
		return false
	})

	// ---- ① 资产树生长:覆盖翻转、事实计数 ----
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	if len(tree.Assets) != 1 || !tree.Assets[0].Covered {
		t.Fatalf("任务完成后资产应已覆盖: %+v", tree)
	}

	// ---- ② 漏洞进列表:一 Confirmed 一待确认(带搁置理由) ----
	var claims []map[string]any
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	if len(claims) != 2 {
		t.Fatalf("应恰 2 条发现: %+v", claims)
	}
	confirmedN, pendingN := 0, 0
	for _, c := range claims {
		switch c["status"] {
		case "confirmed":
			confirmedN++
		case "pending":
			pendingN++
			// R4 不变式 = 待确认必带搁置理由。理由有两个设计内来源(P4-54
			// 放宽特指):正常路径是轮末评估的"证据不足以支撑申报的影响";
			// 完成清点竞态路径是 P4-43 终局清点的"任务完成中断反证,可被
			// 唤醒"(完成时反证在途、锤炼未完,如实挂起)。两者都满足 R4,
			// 全量并行负载下两态均合法出现。
			reason := fmt.Sprint(c["pending_reason"])
			if !strings.Contains(reason, "证据不足以支撑申报的影响") &&
				!strings.Contains(reason, "任务完成中断反证") {
				t.Fatalf("待确认必须带搁置理由(R4): %+v", c)
			}
		}
	}
	if confirmedN != 1 || pendingN != 1 {
		t.Fatalf("应一 Confirmed 一待确认: confirmed=%d pending=%d", confirmedN, pendingN)
	}

	// ---- ④ Goal 经仲裁者判定完成 + ③ 弹门留痕 + ⑤ completed 落账 ----
	evs, _ := store.Events(ctx, task, 0, 0)
	achievedByArbiter, completedEvent, approvalTrail := false, false, 0
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeStateChanged:
			var pl rings.NodeStateChanged
			_ = json.Unmarshal(ev.Payload, &pl)
			if pl.Status == "achieved" && ev.Actor == "arbiter" {
				achievedByArbiter = true
			}
		case rings.EventTaskLifecycle:
			completedEvent = completedEvent || strings.Contains(string(ev.Payload), `"completed"`)
		case rings.EventApprovalRecord:
			approvalTrail++
		}
	}
	if !achievedByArbiter {
		t.Fatalf("目标达成迁移必须归 arbiter(机器终审)")
	}
	if !completedEvent {
		t.Fatalf("账本应有 completed 完成终态事件")
	}
	if approvalTrail != 2 {
		t.Fatalf("弹门留痕应恰 requested+approved 2 条,实际 %d", approvalTrail)
	}
	// 靶站真实命中(发现与反证两轮 HTTP,非账本自说自话)。
	if target.Hits("main", "/old-backup/") < 2 || target.Hits("main", "/api/version") < 2 {
		t.Fatalf("靶站应被发现与反证两轮真实命中: backup=%d version=%d",
			target.Hits("main", "/old-backup/"), target.Hits("main", "/api/version"))
	}

	// ---- ⑥ 成功导出 md 报告 ----
	rec1 := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("GET report = %d: %s", rec1.Code, rec1.Body.String())
	}
	md := rec1.Body.String()
	for _, want := range []string{
		"# 渗透测试报告 · P1 总验收试跑",
		"任务状态:已完成(completed)",
		"已确认发现:**1** 条;证据不足:1 条",
		// P4-59 ①:标题/存在性判断优先定稿校对稿(报告级锚),节点原文
		// 不再进标题;待确认发现无定稿,退节点原文。
		"### C-1 主站备份目录没有登录校验,任何知道地址的人都能下载备份文件",
		"### P-1 /api/version 公开暴露服务版本号",
		"证据不足:",
		"资产锚点:`" + assetID,
		"需批准:请求 1 · 批准 1 · 拒绝 0",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("导出报告缺 %q:\n%s", want, md)
		}
	}
	rec2 := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report", "")
	if rec2.Body.String() != md {
		t.Fatalf("报告导出必须字节级可重现")
	}
}

// assetArg 从任务包态势/资产节提取资产 ID(面板生成的 a-* 随机 ID)。
func assetArg(t *testing.T, pack string) string {
	t.Helper()
	re := regexp.MustCompile(`-\s+(a-[0-9a-f]+) \[service\]`)
	m := re.FindStringSubmatch(pack)
	if m == nil {
		t.Fatalf("任务包缺 service 资产行:\n%s", pack)
	}
	return m[1]
}

// ---- P2-13:导出端点(格式矩阵 / 漏洞级 / 跨任务)+ 列表 repro_steps ----

// seedExportTask 铺一条带定稿复现的 Confirmed 发现(blob 写入与生产
// 装配同路径:Manager RootDir 下的 blobs 目录)。
func seedExportTask(t *testing.T, store *rings.Store, root, task string) {
	t.Helper()
	blobs := swarm.NewBlobStore(filepath.Join(root, "blobs"))
	ex := mirror.Exchange{
		ID: "ms-1", Type: "http", TaskID: task, StepID: "s2", UnitID: "u2",
		Request: &mirror.Request{Method: "GET", URL: "http://mock/old-backup/", Proto: "HTTP/1.1", Host: "mock"},
		Response: &mirror.Response{
			Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Body: []byte("mock backup archive listing"),
		},
	}
	doc, _ := json.Marshal(ex)
	ref, err := blobs.Write(doc)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "导出验证 " + task}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "备份目录匿名可读", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露",
				"impact": "备份可被未授权下载", "standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: "web1"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecFalsify,
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high", EvidenceHead: "独立重放仍返回列举"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecVerdict, Satisfied: true,
			Conditions: []rings.ArbiterConditionResult{
				{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "impact_supported", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
				Op: "bash curl -sS http://mock/old-backup/", Method: "GET",
				URL: "http://mock/old-backup/", Status: 200, BlobRef: ref,
			}},
		}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
}

// TestServerReportExportAPI 导出端点:任务级格式矩阵(四格式 + 范围)、
// 漏洞级 md/zip、跨任务导出、列表 repro_steps 计数。
func TestServerReportExportAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	root := filepath.Join(dir, "tasks")
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: root})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	seedExportTask(t, store, root, "exp-a")

	// 任务级格式矩阵:Content-Type / Disposition / 内容特征。
	cases := []struct {
		format, ctype, name, want string
	}{
		{"md", "text/markdown", "report-exp-a.md", "复现步骤(检查归纳定稿"},
		{"zip", "application/zip", "report-exp-a.zip", "PK"},
		{"csv", "text/csv", "findings-exp-a.csv", "tc-1"},
		{"json", "application/json", "report-exp-a.json", "\"repro\""},
	}
	for _, c := range cases {
		rec := doReq(t, h, http.MethodGet, "/api/tasks/exp-a/report/export?format="+c.format, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 导出 = %d: %s", c.format, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), c.ctype) {
			t.Fatalf("%s Content-Type 不符: %s", c.format, rec.Header().Get("Content-Type"))
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, c.name) {
			t.Fatalf("%s Disposition 不符: %s", c.format, cd)
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("%s 内容缺 %q", c.format, c.want)
		}
	}
	// 范围过滤:空集时报告照常(零发现小节)。
	rec := doReq(t, h, http.MethodGet, "/api/tasks/exp-a/report/export?format=md&claims=no-such", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "本次导出范围:0 / 1 条发现") {
		t.Fatalf("范围导出不符: %d %s", rec.Code, rec.Body.String())
	}
	// 非法格式 400。
	rec = doReq(t, h, http.MethodGet, "/api/tasks/exp-a/report/export?format=exe", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法格式应 400,实际 %d", rec.Code)
	}

	// 漏洞级导出:md 与 zip(证据包)。
	rec = doReq(t, h, http.MethodGet, "/api/tasks/exp-a/claims/tc-1/export?format=md", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "finding-tc-1.md") ||
		!strings.Contains(rec.Body.String(), "# 漏洞报告 · 备份目录匿名可读") {
		t.Fatalf("漏洞级 md 不符: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, h, http.MethodGet, "/api/tasks/exp-a/claims/tc-1/export?format=zip", "")
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) < 4 || rec.Body.Bytes()[0] != 'P' {
		t.Fatalf("漏洞级 zip 不符: %d", rec.Code)
	}
	rec = doReq(t, h, http.MethodGet, "/api/tasks/exp-a/claims/tc-1/export?format=csv", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("漏洞级 csv 应 400,实际 %d", rec.Code)
	}

	// 跨任务导出 + 不存在任务 404。
	rec = doReq(t, h, http.MethodGet, "/api/report/export?tasks=exp-a&format=md", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "# 渗透测试报告") {
		t.Fatalf("跨任务导出不符: %d", rec.Code)
	}
	get(t, h, "/api/report/export?tasks=no-such&format=md", http.StatusNotFound, &struct{}{})

	// 列表 repro_steps 计数(折叠入口数据)。
	var claims []map[string]any
	get(t, h, "/api/tasks/exp-a/claims", http.StatusOK, &claims)
	if len(claims) != 1 {
		t.Fatalf("应恰 1 条发现: %+v", claims)
	}
	if claims[0]["repro_steps"] != float64(1) {
		t.Fatalf("repro_steps 应为 1: %+v", claims[0])
	}
}

// hasSVarToolCall 报告历史里是否已有某工具调用且参数含 marker(按调用
// 参数判重,结果文本相同的两次提交不串台)。
func hasSVarToolCall(rr *srecorded, tool, marker string) bool {
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == tool && strings.Contains(tc.Function.Arguments, marker) {
				return true
			}
		}
	}
	return false
}

// sReissueLastToolCall 从会话历史里找出该单位最近一次发起的同名工具
// 调用并原样重发(批准续跑的脚本模型行为;真实模型同理重试同一调用)。
func sReissueLastToolCall(rr *srecorded, tool string) (args, id string, ok bool) {
	var last struct{ ID, Args string }
	found := false
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == tool && tc.ID != "" {
				last.ID, last.Args, found = tc.ID, tc.Function.Arguments, true
			}
		}
	}
	if !found {
		return "", "", false
	}
	return last.Args, last.ID + "r", true
}

// goalNodes 任务的目标节点清单(P4-16 建任务必带目标,完成终态断言用)。
func goalNodes(t *testing.T, store *rings.Store, taskID string) []rings.NodeFull {
	t.Helper()
	goals, err := store.NodesByType(context.Background(), taskID, rings.NodeGoal, "")
	if err != nil {
		t.Fatalf("读目标清单: %v", err)
	}
	return goals
}
