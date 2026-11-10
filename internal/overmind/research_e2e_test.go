package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-06 完成标准 ----
// mock 场景下五问驱动研判可从账本/日志验证;组合发现能把多条 Fact
// 合并成一个链式 TestClaim 且血缘正确;派发策略完整(优先级/并发/依赖)。

// researchScriptedModel P2-06 E2E 的进程内脚本模型(与 claim E2E 同构,
// 真实 HTTP/SSE 驱动引擎全链路)。分支:
//   - 拆解(无工具):
//     第 1 轮派三条意图——探测管理后台(high)、探测备份目录(low)、
//     读备份凭据(depends_on 备份目录步骤),并带五问研判
//     (首轮:变化集=全部历史,如实研判);
//     第 2 轮(已确认事实 ≥3 时):五问第③问组合 → combined_claims
//     引用三条 Fact 组成攻击链;之后空列表收口;
//   - 发现单位(领到探测/读取意图):按意图内容真实探测 mock 靶站,
//     逐条 submit_candidate_fact;
//   - 反证单位(八、反证任务,含攻击链环节):独立重放整条链
//     (备份→取凭据→带凭据登录后台)→ submit_falsify_result(解释
//     全排除 + 影响面 high)。
type researchScriptedModel struct {
	srv   *httptest.Server
	main  string
	admin string
}

func newResearchScriptedModel(main, admin string) *researchScriptedModel {
	m := &researchScriptedModel{main: main, admin: admin}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *researchScriptedModel) Close() { m.srv.Close() }

func (m *researchScriptedModel) provider() (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: m.srv.URL, APIKey: "test-key", Model: "scripted-research-model",
	})
}

// rmMsg / rmReply 与 claim E2E 的脚本消息结构同构。
type rmMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (m *researchScriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Messages []rmMsg `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	lastUser, tools := "", map[string]bool{}
	id2name := map[string]string{}
	for _, msg := range body.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				id2name[tc.ID] = tc.Function.Name
			}
		}
		if msg.Role == "user" {
			lastUser = msg.Content
		}
	}
	seen := func(tool, marker string) bool {
		for _, msg := range body.Messages {
			if msg.Role == "tool" && id2name[msg.ToolCallID] == tool && strings.Contains(msg.Content, marker) {
				return true
			}
		}
		return false
	}
	called := func(tool, marker string) bool {
		for _, msg := range body.Messages {
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name == tool && strings.Contains(tc.Function.Arguments, marker) {
					return true
				}
			}
		}
		return false
	}
	for _, t := range body.Tools {
		tools[t.Function.Name] = true
	}

	var text string
	var toolName, toolID, args, finish string
	tool := func(id, name, a string) {
		toolID, toolName, args = id, name, a
		finish = "tool_calls"
	}
	// 快照里的已确认事实行:  - f-xxxx: 陈述 (资产 [web1])
	factLineRe := regexp.MustCompile(`(?m)^\s*- (f-[0-9a-f]+): (.+)$`)

	switch {
	case len(tools) == 0: // 主宰拆解(五问研判 + 意图 + 组合声明)
		if strings.Contains(lastUser, "复现步骤事实") {
			// P4-62 A:定稿最小集挑选会话(候选池事实包→选择 JSON)——
			// 如实挑 seq 1(字段留空退确定性兜底)。
			text = `{"steps":[{"seq":1}]}`
			break
		}
		var facts []string
		for _, mm := range factLineRe.FindAllStringSubmatch(lastUser, -1) {
			facts = append(facts, mm[1])
		}
		switch {
		case len(facts) >= 3 && !strings.Contains(lastUser, "新声明 tc-chain-"):
			// 第 2 轮:三条线索已确认,五问第③问组合成攻击链声明。
			chain := ""
			roles := []string{"入口", "凭据", "目标"}
			for i, f := range facts[:3] {
				chain += fmt.Sprintf(`{"fact_id":%q,"role":%q},`, f, roles[i])
			}
			chain = strings.TrimSuffix(chain, ",")
			text = fmt.Sprintf(`{"assessment":{"q_access":"备份目录暴露的 admin 凭据是新取得的身份,可触及资产 web2(管理后台)","q_privilege":"取得的后台凭据使原本仅 401 的管理后台变为可达,提升了任何涉及后台的既有观察的影响","q_combination":"备份可未授权读取、备份含明文凭据、后台需认证——三条单独影响有限的线索组合成未授权管理访问","q_unlock":"无受阻步骤被解锁","q_overturn":"无旧判断被推翻"},"new_intents":[],"combined_claims":[{"statement":"备份文件泄露的管理凭据可未授权登录管理后台(攻击链:备份可读→凭据泄露→后台可入)","impact":"未授权取得管理后台完整访问权(登录态 200),可执行管理面操作","rationale":"入口(备份未授权可读)+凭据(config.bak 明文 admin 密码)+目标(后台仅凭据防护)三环相扣","chain":[%s],"asset_id":"web1","vulnclass":"未授权访问","alternatives":["缓存污染:备份内容可能来自中间缓存","凭据失效:config.bak 内凭据可能已过期或为蜜罐","归属错误:后台可能另有独立凭据体系"]}]}`,
				chain)
		case strings.Contains(lastUser, "步骤:\n  (无)"):
			// 首轮(图上尚无步骤):三条意图——高优先级(后台)、低优先级
			//(备份)、依赖第 2 条(读备份凭据,派发次序确定故依赖 s2)。
			text = `{"assessment":{"q_access":"首轮研判:尚无新身份,种子资产 web1/web2 可达","q_privilege":"无既有漏洞,无权限变化","q_combination":"尚无已确认线索,暂无可组合项","q_unlock":"无受阻步骤","q_overturn":"无旧判断"},"new_intents":[` +
				`{"intent":"探测管理后台 web2 形态与认证要求","expected_observation":"后台认证质询形态","action_level":"只读探测","covers":["web2"],"priority":"high"},` +
				`{"intent":"审计主站备份目录暴露面","expected_observation":"备份文件清单","action_level":"只读探测","covers":["web1"],"priority":"low"},` +
				`{"intent":"读取备份目录中 config.bak 的内容","expected_observation":"配置文件内容(含敏感项则如实记录)","action_level":"只读探测","covers":["web1"],"priority":"normal","depends_on":["s2"]}` +
				`]}`
		default:
			text = `{"new_intents":[]}`
		}
	case strings.Contains(lastUser, "八、反证任务"):
		// 反证单位:独立重放整条攻击链(取凭据 → 无凭据对照 → 带凭据登录)。
		switch {
		case !seen("bash", "admin_pass=s3cret"):
			tool("rc1", "bash", fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/config.bak"}`, m.main))
		case !seen("bash", "authorization required"):
			tool("rc2", "bash", fmt.Sprintf(`{"command":"curl -sS -i -m 5 %s/"}`, m.admin))
		case !seen("bash", "logged in as admin"):
			tool("rc3", "bash", fmt.Sprintf(`{"command":"curl -sS -m 5 -u admin:s3cret -i %s/"}`, m.admin))
		default:
			claimIDRe := regexp.MustCompile(`判断 (tc-[0-9a-z-]+)`)
			cm := claimIDRe.FindStringSubmatch(lastUser)
			claimID := ""
			if cm != nil {
				claimID = cm[1]
			}
			tool("rf1", "submit_falsify_result", fmt.Sprintf(`{"claim_id":%q,"existence_refuted":false,"impact_supported":true,"impact_level":"high","alternatives":[{"id":"alt1","refuted":true},{"id":"alt2","refuted":true},{"id":"alt3","refuted":true}],"evidence":"独立重放:GET /old-backup/config.bak 200 得 admin:s3cret;同后台无凭据 401、带 admin:s3cret 200 logged in as admin——组合效果真实存在且凭据有效","note":"逐环节独立重放,非采信发现者陈述"}`, claimID))
		}
	case strings.Contains(lastUser, "运行预算即将耗尽"):
		text = "收尾:无可保留发现未提交。"
	default: // 发现单位
		// 分支只看"二、本步骤意图"行:任务包"前序尝试"节列有其他步骤
		// 标题,整包 Contains 会误匹配(曾致三个单位重复交同一事实)。
		own := ""
		if mm := regexp.MustCompile(`二、本步骤意图\(只做这一条\)\n\s+(.+)`).FindStringSubmatch(lastUser); mm != nil {
			own = mm[1]
		}
		switch {
		case strings.Contains(own, "探测管理后台") && !called("submit_candidate_fact", "管理后台"):
			if !seen("bash", "mock admin console") {
				tool("du1", "bash", fmt.Sprintf(`{"command":"curl -sS -i -m 5 %s/"}`, m.admin))
			} else {
				tool("df1", "submit_candidate_fact", `{"statement":"web2 管理后台需 Basic 认证,未授权访问被 401 拒绝","evidence":"curl -i 实测 / 返回 401 + WWW-Authenticate: Basic realm=mock-admin","asset_id":"web2"}`)
			}
		case strings.Contains(own, "审计主站备份目录") && !called("submit_candidate_fact", "备份目录"):
			if !seen("bash", "config.bak") {
				tool("db1", "bash", fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, m.main))
			} else {
				tool("df2", "submit_candidate_fact", `{"statement":"主站备份目录 /old-backup/ 可未授权读取,清单含 config.bak","evidence":"curl 实测 /old-backup/ 200 返回清单 mock backup archive listing: db-2026-09-01.sql.gz, config.bak","asset_id":"web1"}`)
			}
		case strings.Contains(own, "读取备份目录中 config.bak") && !called("submit_candidate_fact", "明文凭据"):
			if !seen("bash", "admin_pass=") {
				tool("dc1", "bash", fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/config.bak"}`, m.main))
			} else {
				tool("df3", "submit_candidate_fact", `{"statement":"备份文件 config.bak 含管理后台明文凭据(admin/s3cret)","evidence":"curl 实测 /old-backup/config.bak 200,内容含 admin_user=admin 与 admin_pass=s3cret","asset_id":"web1"}`)
			}
		default:
			text = "本意图执行完毕,候选已提交。"
		}
	}
	m.writeSSE(w, text, toolName, toolID, args, finish)
}

func (m *researchScriptedModel) writeSSE(w http.ResponseWriter, text, toolName, id, args, finish string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(p any) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if text != "" {
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
	}
	if toolName != "" {
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": toolName, "arguments": args}}}}}}})
	}
	if finish == "" {
		finish = "stop"
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
	send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 40}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// overmindRecords 取任务内全部 overmind.record(按类型过滤,空为全部)。
func overmindRecords(t *testing.T, store *rings.Store, task, typ string) []rings.OvermindRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.OvermindRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var r rings.OvermindRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 overmind.record: %v", err)
		}
		if typ == "" || r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

// stepSessionStartRev 步骤 session_started 事件的 revision(并发/优先级/
// 依赖时序的观测锚点);未起飞返回 0。
func stepSessionStartRev(t *testing.T, store *rings.Store, task, stepID string) int64 {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &r) != nil {
			continue
		}
		if r.StepID == stepID && r.Type == rings.SwarmRecSessionStarted {
			return ev.Revision
		}
	}
	return 0
}

// stepDoneRev 步骤落 done 终态的 state_changed 事件 revision;未完成返回 0。
func stepDoneRev(t *testing.T, store *rings.Store, task, stepID string) int64 {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) != nil || p.NodeID != stepID || p.Status != "done" {
			continue
		}
		if cur := stepSessionStartRev(t, store, task, stepID); cur == 0 || ev.Revision > cur {
			return ev.Revision // 终态须在起飞之后(排除 dispatched 初始态)
		}
	}
	return 0
}

// TestResearchFullChainE2E P2-06 主宰完整研判 E2E(镜已装配,引擎链路全真,
// 并发上限 1 使派发次序可观测):
//
//	第 1 轮:五问研判(首轮全量变化集)→ 三意图派发(优先级 high/low +
//	依赖 depends_on)→ 发现单位真实探测,三条候选事实收割转正;
//	第 2 轮:五问研判(变化集=三条新事实)→ 第③问组合 → 组合声明
//	(链式 TestClaim,derived_from×3 + about)→ 收割派反证(默认只读
//	级别)→ 反证单位独立重放整条链 → 解释全排除+影响面 high →
//	仲裁者 Confirmed(R12:发现者=overmind,反证单位必不同)→
//	收割定稿(复现步骤=反证轮镜留痕)。
func TestResearchFullChainE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "research-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL, adminURL := target.MainURL(), target.AdminURL()

	model := newResearchScriptedModel(mainURL, adminURL)
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := dir
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov, RootDir: root,
		MaxConcurrentUnits: 1, Model: "scripted-research-model"}
	m, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, _ string, wake <-chan struct{}) { loop.Run(tctx, wake) },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(task)
		m.Close()
	})
	if _, err := m.CreateTask(ctx, task, "P2-06 主宰完整研判 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "审计 mock 靶站,组合线索形成攻击链级发现", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1", "web2"}, map[string]string{"web1": mainURL, "web2": adminURL})
	tk := m.Task(task)
	tk.Wake()

	// ---- 阶段一:组合声明走完状态机到 Confirmed ----
	ok := waitForSoft(90*time.Second, "组合声明 Confirmed", func() bool {
		claims, err := store.NodesByType(context.Background(), task, rings.NodeTestClaim, "")
		if err != nil {
			return false
		}
		for _, c := range claims {
			if combined, _ := c.Detail["combined"].(bool); combined && c.Status == rings.TestClaimConfirmed {
				return true
			}
		}
		return false
	})
	if !ok {
		debugDumpLedger(t, store, task)
		t.Fatalf("组合声明未到 Confirmed(账本快照见上)")
	}

	claims, err := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
	if err != nil {
		t.Fatalf("查声明失败: %v", err)
	}
	var combined *rings.NodeFull
	for i := range claims {
		if c, _ := claims[i].Detail["combined"].(bool); c {
			combined = &claims[i]
			break
		}
	}
	if combined == nil {
		t.Fatalf("未找到组合声明")
	}

	// ① 五问研判从账本可验证:研判记录 ≥2 条(首轮 + 组合轮),五问
	// 逐问结论非空;组合轮的 Combined 指向该声明。
	assessments := overmindRecords(t, store, task, rings.OvermindRecAssessment)
	if len(assessments) < 2 {
		t.Fatalf("研判记录应 ≥2 条(首轮+组合轮): %+v", assessments)
	}
	var combineAssess *rings.OvermindRecord
	for i := range assessments {
		a := assessments[i]
		if a.QAccess == "" || a.QPrivilege == "" || a.QCombination == "" || a.QUnlock == "" || a.QOverturn == "" {
			t.Fatalf("研判记录五问应逐问有结论(空=无可研判项也应如实陈述): %+v", a)
		}
		if a.EvaluatedRev == 0 || a.SessionID == "" {
			t.Fatalf("研判记录应携带研判锚点(账本版本+会话): %+v", a)
		}
		for _, cid := range a.Combined {
			if cid == combined.ID {
				combineAssess = &assessments[i]
			}
		}
	}
	if combineAssess == nil {
		t.Fatalf("组合轮研判记录应指向组合声明 %s: %+v", combined.ID, assessments)
	}
	if !strings.Contains(combineAssess.QCombination, "组合") {
		t.Fatalf("组合轮第③问结论应言明组合研判: %q", combineAssess.QCombination)
	}

	// ② 组合发现血缘正确:derived_from 挂到每条环节 Fact(≥3),about
	// 挂资产;链条 detail 与边一致;状态机 候选→反证中→Confirmed。
	derived, err := store.Edges(ctx, task, rings.EdgeDerivedFrom)
	if err != nil {
		t.Fatalf("查血缘边失败: %v", err)
	}
	var chainFacts []string
	for _, e := range derived {
		if e.From == combined.ID {
			n, err := store.Node(ctx, task, e.To)
			if err != nil || n.Type != rings.NodeFact {
				t.Fatalf("组合声明血缘应指向 Fact: %v %v", e.To, err)
			}
			if n.Status != "confirmed" {
				t.Fatalf("组合声明血缘环节应是已确认 Fact: %s %s", n.ID, n.Status)
			}
			chainFacts = append(chainFacts, n.ID)
		}
	}
	if len(chainFacts) != 3 {
		t.Fatalf("组合声明血缘应恰挂 3 条环节 Fact: %v", chainFacts)
	}
	// 环节陈述互异:组合发现的意义在"多条不同线索"(同题三连=脚本误匹配)。
	titles := map[string]bool{}
	for _, e := range derived {
		if e.From != combined.ID {
			continue
		}
		if n, err := store.Node(ctx, task, e.To); err == nil {
			if titles[n.Title] {
				t.Fatalf("链环节陈述重复(单位重复提交同一事实): %s", n.Title)
			}
			titles[n.Title] = true
		}
	}
	if len(titles) != 3 {
		t.Fatalf("链环节应恰 3 条互异陈述: %v", titles)
	}
	// 任务内 confirmed Fact 恰 3 条:每个发现单位只交自己那条。
	allFacts, err := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
	if err != nil || len(allFacts) != 3 {
		t.Fatalf("已确认事实应恰 3 条(无重复提交): %d %v", len(allFacts), err)
	}
	rawChain, _ := combined.Detail["chain"].([]any)
	if len(rawChain) != 3 {
		t.Fatalf("组合声明 detail.chain 应恰 3 环节: %+v", combined.Detail["chain"])
	}
	abouts, err := store.Edges(ctx, task, rings.EdgeAbout)
	if err != nil {
		t.Fatalf("查资产锚点失败: %v", err)
	}
	hasAnchor := false
	for _, e := range abouts {
		if e.From == combined.ID && e.To == "web1" {
			hasAnchor = true
		}
	}
	if !hasAnchor {
		t.Fatalf("组合声明应锚定资产 web1")
	}
	if got := claimStates(t, store, task, combined.ID); len(got) != 3 ||
		got[0] != rings.TestClaimCandidate || got[1] != rings.TestClaimTesting || got[2] != rings.TestClaimConfirmed {
		t.Fatalf("组合声明状态机轨迹不符: %v", got)
	}
	// 发现者=主宰(R12:反证必为虫群独立单位,机器核验自动通过)。
	if u, _ := combined.Detail["unit_id"].(string); u != ActorOvermind {
		t.Fatalf("组合声明发现者应记主宰: %q", u)
	}

	// ③ 裁决与定稿:Confirmed 裁决条件全满足(含验证者独立);收割
	// 定稿严重级别 high、复现步骤来自反证轮镜留痕。
	verdicts := testclaimRecords(t, store, task, rings.TestClaimRecVerdict)
	var v *rings.TestClaimRecord
	for i := range verdicts {
		if verdicts[i].ClaimID == combined.ID {
			v = &verdicts[i]
		}
	}
	if v == nil || v.Decision != rings.VerdictConfirm || !v.Satisfied {
		t.Fatalf("组合声明应有确认裁决: %+v", v)
	}
	for _, c := range v.Conditions {
		if !c.Satisfied {
			t.Fatalf("确认裁决条件须逐条满足: %+v", c)
		}
	}
	repros := testclaimRecords(t, store, task, rings.TestClaimRecRepro)
	var repro *rings.TestClaimRecord
	for i := range repros {
		if repros[i].ClaimID == combined.ID {
			repro = &repros[i]
		}
	}
	if repro == nil {
		t.Fatalf("组合声明确认应同批落收割定稿")
	}
	if repro.Severity != "high" {
		t.Fatalf("组合声明定稿级别应 high(末轮影响面定级): %q", repro.Severity)
	}
	if len(repro.Steps) == 0 {
		t.Fatalf("组合声明定稿应含复现步骤(反证轮镜留痕)")
	}
	for _, s := range repro.Steps {
		if s.BlobRef == "" {
			t.Fatalf("复现步骤应带原始数据引用: %+v", s)
		}
	}

	// ④ 反证单位确实独立重放了整条链(靶站真实命中,非账本自说自话)。
	if target.Hits("main", "/old-backup/config.bak") < 2 {
		t.Fatalf("备份凭据文件应被真实读取(发现+反证 ≥2 次)")
	}
	if target.Hits("admin", "/") < 3 {
		t.Fatalf("管理后台应被真实探测(发现 401 + 反证 401/200)")
	}

	// ⑤ 派发策略:优先级(high 的 s? 先于 low 的 s?)+ 依赖(依赖步骤
	// 在前置 done 之后才起飞)。
	steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatalf("查步骤失败: %v", err)
	}
	var adminStep, backupStep, credStep *rings.NodeFull
	for i := range steps {
		s := &steps[i]
		switch {
		case strings.Contains(s.Title, "探测管理后台"):
			adminStep = s
		case strings.Contains(s.Title, "审计主站备份目录"):
			backupStep = s
		case strings.Contains(s.Title, "读取备份目录中 config.bak"):
			credStep = s
		}
	}
	if adminStep == nil || backupStep == nil || credStep == nil {
		t.Fatalf("三条意图步骤应齐: %+v", steps)
	}
	if p, _ := adminStep.Detail["priority"].(string); p != "high" {
		t.Fatalf("管理后台步骤应带高优先级: %+v", adminStep.Detail)
	}
	if p, _ := backupStep.Detail["priority"].(string); p != "low" {
		t.Fatalf("备份目录步骤应带低优先级: %+v", backupStep.Detail)
	}
	// 依赖边:凭据步骤 depends_on 备份步骤。
	deps, err := store.Edges(ctx, task, rings.EdgeDependsOn)
	if err != nil {
		t.Fatalf("查依赖边失败: %v", err)
	}
	foundDep := false
	for _, e := range deps {
		if e.From == credStep.ID && e.To == backupStep.ID {
			foundDep = true
		}
	}
	if !foundDep {
		t.Fatalf("凭据步骤应依赖备份步骤(depends_on 边): %v", deps)
	}
	// 时序:高优先级先于低优先级起飞;依赖步骤在前置 done 之后起飞。
	if a, b := stepSessionStartRev(t, store, task, adminStep.ID), stepSessionStartRev(t, store, task, backupStep.ID); a == 0 || b == 0 || a > b {
		t.Fatalf("优先级时序不符: high 步骤起飞 rev=%d 应先于 low 步骤 rev=%d", a, b)
	}
	doneRev := stepDoneRev(t, store, task, backupStep.ID)
	startRev := stepSessionStartRev(t, store, task, credStep.ID)
	if doneRev == 0 || startRev == 0 || startRev < doneRev {
		t.Fatalf("依赖时序不符: 前置 done rev=%d 应先于依赖步骤起飞 rev=%d", doneRev, startRev)
	}

	// ⑥ 日志可验证:主宰会话转录 JSONL 落盘(含五问系统提示词与
	// 变化集快照的会话原文)。
	transcripts := transcriptFiles(t, root, task)
	if len(transcripts) == 0 {
		t.Fatalf("主宰会话转录应落盘(研判日志可验证)")
	}

	// ⑦ 干净运行:无 LastErr 残留。
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}

// transcriptFiles 列任务主宰转录文件名(四类记录分工:会话原文 JSONL)。
func transcriptFiles(t *testing.T, root, task string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, task, "transcripts"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "overmind-") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// waitForSoft waitFor 的软形态(超时返回 false 而非 Fatal,供失败路径
// 先 dump 账本再报警)。
func waitForSoft(timeout time.Duration, msg string, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

// debugDumpLedger 失败路径的账本快照(诊断用,E2E 排障)。
func debugDumpLedger(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
	for _, c := range claims {
		t.Logf("claim %s [%s] %s combined=%v", c.ID, c.Status, c.Title, c.Detail["combined"])
	}
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	for _, s := range steps {
		t.Logf("step %s [%s] %s priority=%v", s.ID, s.Status, s.Title, s.Detail["priority"])
	}
	facts, _ := store.NodesByType(ctx, task, rings.NodeFact, "")
	for _, f := range facts {
		t.Logf("fact %s [%s] %s", f.ID, f.Status, f.Title)
	}
	evs, _ := store.Events(ctx, task, 0, 0)
	for _, ev := range evs {
		clip := string(ev.Payload)
		if len(clip) > 220 {
			clip = clip[:220] + "…"
		}
		t.Logf("ev %d %s actor=%s %s", ev.Revision, ev.Kind, ev.Actor, clip)
	}
}
