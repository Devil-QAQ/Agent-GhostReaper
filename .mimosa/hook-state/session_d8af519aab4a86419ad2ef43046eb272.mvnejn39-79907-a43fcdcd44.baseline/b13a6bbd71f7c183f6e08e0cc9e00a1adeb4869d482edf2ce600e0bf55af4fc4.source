package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-02 完成标准(面板侧):漏洞详情可见完整反证时间线与复现步骤数据;
// 重开为面板可操作入口(结论可反驳)。 ----

// seedFullClaim 铺一条走完"两轮反证 + 确认"的声明(与 P2-02 受控入口
// 产出的事件形态同构);复现步骤的 blob 由调用方写入 Manager 根下的
// blobs(与生产装配同路径)后补记 repro。
func seedFullClaim(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	altList := []any{
		map[string]any{"id": "alt1", "label": "缓存污染:内容可能来自中间缓存"},
		map[string]any{"id": "alt2", "label": "归属错误:可能来自其他资产"},
	}
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "审计主站", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-f", NodeType: rings.NodeTestClaim, Title: "/old-backup/ 备份目录可公开匿名列举",
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1",
				"impact": "备份文件可被未授权下载", "vulnclass": "信息暴露",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-f", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-f", ToNode: "s1"}},
		// 第 1 轮:排除 alt2,alt1 存活 → 轮末评估 advance_round。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf1", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-f", "round": 1}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", StepID: "sf1", UnitID: "u2", Type: rings.TestClaimRecFalsify, Round: 1,
			ImpactSupported: true, ImpactLevel: "high",
			Alternatives: []rings.AltResult{{ID: "alt1", Refuted: false}, {ID: "alt2", Refuted: true}},
			EvidenceHead: "第 1 轮:归属已确认,缓存未除"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", StepID: "sf1", Round: 1, Type: rings.TestClaimRecVerdict,
			Decision: rings.VerdictAdvanceRound, PendingReason: "第 1 轮反证完成,1 条竞争性替代解释未排除(alt1),待派下一轮反证",
			Conditions: []rings.ArbiterConditionResult{
				{Kind: arbiter.CondFalsifyCompleted, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondExistenceNotRefuted, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondAlternativesKilled, Required: 2, Actual: 1},
				{Kind: arbiter.CondImpactSupported, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondVerifierIndependent, Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimAwaitingRound}},
		// 第 2 轮:缓存穿透 → 全灭 → 确认。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf2", NodeType: rings.NodeStep, Title: "反证(第 2 轮)", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-f", "round": 2}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", StepID: "sf2", UnitID: "u3", Type: rings.TestClaimRecFalsify, Round: 2,
			ImpactSupported: true, ImpactLevel: "high",
			Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}},
			EvidenceHead: "第 2 轮:穿透重放仍返回列举"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", StepID: "sf2", Round: 2, Type: rings.TestClaimRecVerdict,
			Decision: rings.VerdictConfirm, Satisfied: true,
			Conditions: []rings.ArbiterConditionResult{
				{Kind: arbiter.CondFalsifyCompleted, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondExistenceNotRefuted, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondAlternativesKilled, Required: 2, Actual: 2, Satisfied: true},
				{Kind: arbiter.CondImpactSupported, Required: 1, Actual: 1, Satisfied: true},
				{Kind: arbiter.CondVerifierIndependent, Required: 1, Actual: 1, Satisfied: true},
			}}},
	)
	// 复现步骤的交换原文:真实写入 Manager 根下的内容寻址存储(与镜装配
	// 同路径),详情 API 从 blob 回放。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3", Payload: rings.SwarmRecord{
			StepID: "sf2", UnitID: "u3", Type: rings.SwarmRecToolCall, ToolName: "bash",
			ToolArgs:   json.RawMessage(`{"command":"curl -sS -m 5 http://mock/old-backup/?nocache=880011"}`),
			ResultHead: "mock backup archive listing"}},
	)
}

// writeExchangeBlob 向 Manager 根下的 blobs 写一份交换文档,返回引用。
func writeExchangeBlob(t *testing.T, mgrRoot string, ex *mirror.Exchange) string {
	t.Helper()
	blobs := swarm.NewBlobStore(filepath.Join(mgrRoot, "blobs"))
	doc, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("序列化交换失败: %v", err)
	}
	ref, err := blobs.Write(doc)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	return ref
}

// newClaimTestServer 构建测试后端(Manager 根 = dir/tasks,与 blob 写入
// 路径一致)。任务未挂 OnTaskRun:不注册 Close 清理——空跑任务 goroutine
// 会令 Close 永等,沿用 server_claims_test 的不关惯例。
func newClaimTestServer(t *testing.T, store *rings.Store, gate swarm.ApprovalGate, dir string) (*Server, *http.ServeMux) {
	t.Helper()
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	return srv, srv.Handler()
}

// TestServerClaimDetail 发现详情 API:两维声明分开、冻结解释逐条存活
// 状态、完整反证时间线(候选→轮次→轮末评估→确认)、复现步骤含操作
// 方法与 Burp 风格原文、严重级别跟末轮影响面定级。
func TestServerClaimDetail(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"详情验证"}`, http.StatusOK, &created)
	task := created["id"]
	seedFullClaim(t, store, task)

	// 复现步骤的交换原文(镜留痕形态)与收割定稿。
	body := "mock backup archive listing: db-2026-09-01.sql.gz, config.bak\n"
	ex := &mirror.Exchange{
		ID: "ms-x.1", Type: rings.MirrorRecHTTP, TaskID: task, StepID: "sf2", UnitID: "u3",
		Request: &mirror.Request{
			Method: "GET", URL: "http://mock/old-backup/?nocache=880011", Proto: "HTTP/1.1",
			Host: "mock", Headers: []mirror.Header{{Name: "User-Agent", Value: "curl/8"}},
		},
		Response: &mirror.Response{
			Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Headers: []mirror.Header{
				{Name: "Server", Value: "gr-mock-main/2.4.1"},
				{Name: "Content-Type", Value: "text/plain; charset=utf-8"},
			},
			Body: []byte(body),
		},
		StartedAt: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	}
	ref := writeExchangeBlob(t, filepath.Join(dir, "tasks"), ex)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rings.MirrorRecord{
			ExchangeID: ex.ID, Type: rings.MirrorRecHTTP, StepID: "sf2", UnitID: "u3",
			Method: "GET", URL: ex.Request.URL, Status: 200, BlobRef: ref}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", Type: rings.TestClaimRecRepro, Severity: "high",
			Narrative: "单位 u1 执行「审计主站敏感路径」时,在 /old-backup/ 的 200 响应正文里发现备份列举;u3 独立反证 2 轮排除竞争解释,仲裁者判定成立。",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 2, StepID: "sf2", UnitID: "u3",
				Op:     `bash {"command":"curl -sS -m 5 http://mock/old-backup/?nocache=880011"}`,
				Method: "GET", URL: ex.Request.URL, Status: 200, BlobRef: ref,
			}}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimConfirmed}},
	)

	var d map[string]any
	get(t, h, "/api/tasks/"+task+"/claims/tc-f", http.StatusOK, &d)

	// 两维声明分开呈现。
	claim := d["claim"].(map[string]any)
	if claim["statement"] != "/old-backup/ 备份目录可公开匿名列举" ||
		claim["impact"] != "备份文件可被未授权下载" {
		t.Fatalf("两维声明应分开呈现: %+v", claim)
	}
	// 严重级别跟影响面证据(收割定稿)。
	if claim["severity"] != "high" || d["reopenable"] != true {
		t.Fatalf("定稿级别与可重开标记不符: %+v %v", claim, d["reopenable"])
	}

	// 冻结解释:alt1/alt2 均已排除(当前战役)。
	alts, _ := d["alternatives"].([]any)
	if len(alts) != 2 {
		t.Fatalf("冻结解释应恰 2 条: %+v", alts)
	}
	for _, a := range alts {
		m := a.(map[string]any)
		if m["killed"] != true {
			t.Fatalf("两轮反证后解释应全部标记已排除: %+v", m)
		}
	}

	// 完整时间线:created → state(testing) → falsify(R1) → verdict(advance)
	// → state(awaiting) → state(testing) → falsify(R2) → verdict(confirm)
	// → repro → state(confirmed)。
	tl, _ := d["timeline"].([]any)
	var kinds []string
	for _, e := range tl {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	want := "created,state,falsify,verdict,state,state,falsify,verdict,repro,state"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("时间线种类序列不符:\n得到 %s\n期望 %s", strings.Join(kinds, ","), want)
	}
	// R1 反证条目:逐条解释结论可见(alt1 未除/alt2 已除)。
	f1 := tl[2].(map[string]any)
	if f1["round"] != float64(1) || f1["unit_id"] != "u2" {
		t.Fatalf("第 1 轮反证条目不符: %+v", f1)
	}
	altRes, _ := f1["alt_results"].([]any)
	if len(altRes) != 2 || altRes[0].(map[string]any)["refuted"] != false ||
		altRes[1].(map[string]any)["refuted"] != true {
		t.Fatalf("逐条解释结论应随时间线呈现: %+v", altRes)
	}
	// 轮末评估:advance_round + 理由。
	v1 := tl[3].(map[string]any)
	if v1["decision"] != "advance_round" || !strings.Contains(v1["pending_reason"].(string), "alt1") {
		t.Fatalf("第 1 轮评估应为 advance_round: %+v", v1)
	}
	v2 := tl[7].(map[string]any)
	if v2["decision"] != "confirm" || v2["satisfied"] != true {
		t.Fatalf("第 2 轮评估应为 confirm: %+v", v2)
	}

	// 复现步骤:操作方法 + Burp 风格原文(请求行/响应头/响应体可核对);
	// 来龙去脉(P4-13):定稿冻结的叙述随 repro 视图下发(面板零 LLM)。
	repro := d["repro"].(map[string]any)
	if repro["severity"] != "high" {
		t.Fatalf("定稿严重级别不符: %+v", repro)
	}
	if !strings.Contains(repro["narrative"].(string), "/old-backup/ 的 200 响应正文") {
		t.Fatalf("来龙去脉叙述应随定稿视图下发: %+v", repro)
	}
	steps, _ := repro["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("应恰 1 步复现: %+v", steps)
	}
	st := steps[0].(map[string]any)
	if !strings.Contains(st["op"].(string), "curl -sS") || st["method"] != "GET" || st["status"] != float64(200) {
		t.Fatalf("复现步骤操作方法/交换摘要不符: %+v", st)
	}
	raw := st["raw"].(string)
	for _, want := range []string{
		"GET /old-backup/?nocache=880011 HTTP/1.1",
		"Host: mock",
		"User-Agent: curl/8",
		"HTTP/1.1 200 OK",
		"Server: gr-mock-main/2.4.1",
		"mock backup archive listing: db-2026-09-01.sql.gz",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("复现原文缺 %q:\n%s", want, raw)
		}
	}
	if st["blob_ref"] != ref {
		t.Fatalf("复现步骤应携带 blob 引用: %+v", st)
	}

	// 列表侧同步携带定稿级别。
	var list []map[string]any
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &list)
	if len(list) != 1 || list[0]["severity"] != "high" {
		t.Fatalf("列表应携带定稿级别: %+v", list)
	}

	// 幽灵声明 404。
	get(t, h, "/api/tasks/"+task+"/claims/tc-404", http.StatusNotFound, new(map[string]any))
}

// TestServerClaimReopen 重开 API:合法重开落 reopen 记录 + 反证 Step +
// 回 testing;非法触发词/空理由拒绝;非终态声明拒绝。
func TestServerClaimReopen(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)
	ctx := context.Background()

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"重开验证"}`, http.StatusOK, &created)
	task := created["id"]
	seedFullClaim(t, store, task)
	// 推到 Confirmed(裁决 + 定稿 + 状态迁移)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", Type: rings.TestClaimRecRepro, Severity: "high"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimConfirmed}},
	)

	// 非法触发词 / 空理由 → 400。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-f/reopen",
		`{"reason":"理由","trigger":" whim "}`, http.StatusBadRequest, new(map[string]any))
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-f/reopen",
		`{"reason":"  "}`, http.StatusBadRequest, new(map[string]any))

	// 合法重开:反驳结论。
	var resp map[string]string
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-f/reopen",
		`{"reason":"新证据显示可能是缓存污染","trigger":"new_evidence"}`, http.StatusOK, &resp)
	if resp["status"] != rings.TestClaimTesting {
		t.Fatalf("重开应回 testing: %+v", resp)
	}
	node, err := store.Node(ctx, task, "tc-f")
	if err != nil || node.Status != rings.TestClaimTesting {
		t.Fatalf("重开后声明应回反证中: %+v %v", node, err)
	}
	// reopen 记录与重开轮反证 Step 同批落账(轮次续号 3)。
	evs, _ := store.Events(ctx, task, 0, 0)
	var reopen *rings.TestClaimRecord
	stepRound := 0
	for _, ev := range evs {
		if ev.Kind == rings.EventTestClaimRecord {
			var r rings.TestClaimRecord
			_ = json.Unmarshal(ev.Payload, &r)
			if r.Type == rings.TestClaimRecReopen {
				reopen = &r
			}
		}
		if ev.Kind == rings.EventNodeAdded {
			var p rings.NodeAdded
			_ = json.Unmarshal(ev.Payload, &p)
			if p.NodeType == rings.NodeStep && p.Detail["testclaim_id"] == "tc-f" {
				if r, ok := p.Detail["round"].(float64); ok {
					stepRound = int(r)
				}
			}
		}
	}
	if reopen == nil || reopen.Trigger != rings.ReopenNewEvidence ||
		!strings.Contains(reopen.Reason, "缓存污染") {
		t.Fatalf("重开记录不符: %+v", reopen)
	}
	if stepRound != 3 {
		t.Fatalf("重开轮反证步骤应续号 round=3: %d", stepRound)
	}
	// 幽灵声明 404。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-404/reopen",
		`{"reason":"x"}`, http.StatusNotFound, new(map[string]any))
}
