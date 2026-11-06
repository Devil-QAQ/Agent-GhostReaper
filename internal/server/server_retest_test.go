package server

// P3-06 复测端点测试:发起守卫(404/400/409/503)、异步驱动起飞
// (session_started 入账、活动登记、列表/详情视图的复测字段)、
// 驱动失败后如实收场(登记注销,状态不动)。

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestServerClaimRetest 复测发起端点与视图字段(P3-06 完成标准的
// server 侧:入口点亮、并发互斥、状态自动更新走 swarm 侧 E2E)。
func TestServerClaimRetest(t *testing.T) {
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

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"复测端点验证"}`, http.StatusOK, &created)
	task := created["id"]

	// 铺垫:资产 + 候选声明(先停在 candidate 验证 400,再转 confirmed)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active",
			AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-r1", NodeType: rings.NodeTestClaim, Title: "备份目录可匿名列举",
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "impact": "备份可被未授权下载",
				"vulnclass": "信息暴露", "evidence": "curl 200 备份清单"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-r1", ToNode: "web1"}},
	)

	// 404:任务 / 声明不存在。
	post(t, h, http.MethodPost, "/api/tasks/no-such/claims/tc-r1/retest", `{}`, http.StatusNotFound, nil)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-nope/retest", `{}`, http.StatusNotFound, nil)
	// 503:执行器未装配(模型/镜缺失)。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-r1/retest", `{}`, http.StatusServiceUnavailable, nil)

	// 装配执行器:持连模型(原始 TCP 收下连接不回包——会话保持运行,
	// 409 与视图字段的确定性来源;测试尾统一断连,快失败收场)。
	hang := newHoldConns(t)
	prov, err := llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: "http://" + hang.addr(), APIKey: "test-key", Model: "hold-model",
		MaxRetries: -1, // 断连后不重试(收场断言限时内的确定性)
	})
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	exec := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	// P4-27 ① 长退避测试旋钮:毫秒级梯子与深阈值,断连后快速落
	// blocked 收场(限期内确定性)。
	exec.ModelRetryLadder = []time.Duration{5 * time.Millisecond}
	exec.BlockedDeepAfter = 20 * time.Millisecond
	exec.Mirror = mirror.New(store, exec.Blobs, t.TempDir())
	srv.AttachRuntime(exec)

	// 400:锤炼中(candidate)不可复测(装配在场,状态拒绝)。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-r1/retest", `{}`, http.StatusBadRequest, nil)

	// 转锤炼完成(confirmed)后发起:200 + 轮次/会话锚。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-r1", Status: rings.TestClaimConfirmed}})
	var started map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-r1/retest", `{}`, http.StatusOK, &started)
	if started["round"].(float64) != 1 || started["session_id"] != "retest-tc-r1-r1" {
		t.Fatalf("发起响应不符: %v", started)
	}
	// 并发互斥:同声明同时至多一轮。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/claims/tc-r1/retest", `{}`, http.StatusConflict, nil)

	// 列表视图:复测中 + 可复测标记。
	var list []map[string]any
	post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims", "", http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("应恰 1 条声明: %v", list)
	}
	if list[0]["retest_running"] != true || list[0]["retestable"] != true {
		t.Fatalf("列表复测字段不符: %v", list[0])
	}
	// 详情视图:复测历史空、运行中标记。
	var detail struct {
		Claim   map[string]any   `json:"claim"`
		Retests []map[string]any `json:"retests"`
	}
	post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims/tc-r1", "", http.StatusOK, &detail)
	if detail.Claim["retest_running"] != true || len(detail.Retests) != 0 {
		t.Fatalf("详情复测字段不符: %v %v", detail.Claim, detail.Retests)
	}

	// 收场:统一断连 → 模型请求即错 → 零重试落地(登记注销;无结论,
	// 状态不动——结论只经受限工具写回)。
	hang.release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var rows []map[string]any
		post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims", "", http.StatusOK, &rows)
		if rows[0]["retest_running"] == false {
			if rows[0]["status"] != "confirmed" {
				t.Fatalf("失败会话不得迁移状态: %v", rows[0])
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("复测会话未在限期内收场")
}

// holdConns 持连替身:收下全部连接不回包(模型请求挂起);release 统一
// 断连,挂起的请求立刻以连接错误收场。原始 TCP,无 HTTP 服务清理语义。
type holdConns struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	done  bool
}

func newHoldConns(t *testing.T) *holdConns {
	t.Helper()
	hc := &holdConns{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起持连监听失败: %v", err)
	}
	hc.ln = ln
	t.Cleanup(hc.release)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hc.mu.Lock()
			if hc.done {
				hc.mu.Unlock()
				c.Close()
				return
			}
			hc.conns = append(hc.conns, c)
			hc.mu.Unlock()
		}
	}()
	return hc
}

func (hc *holdConns) addr() string { return hc.ln.Addr().String() }

func (hc *holdConns) release() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.done {
		return
	}
	hc.done = true
	for _, c := range hc.conns {
		_ = c.Close()
	}
	_ = hc.ln.Close()
}

// TestServerClaimRetestViewFieldsAfterVerdict 结论落账后的视图字段
// (列表徽标数据与详情历史):retest 记录 + fixed 状态直写账本后,
// 列表给最新结论,详情给完整历史。
func TestServerClaimRetestViewFieldsAfterVerdict(t *testing.T) {
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

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"复测视图验证"}`, http.StatusOK, &created)
	task := created["id"]

	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active",
			AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-v1", NodeType: rings.NodeTestClaim, Title: "接口未授权可读",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "impact": "可读全部用户",
				"vulnclass": "信息暴露", "evidence": "GET 200"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-v1", ToNode: "web1"}},
		// 第 1 轮复测:unfixed(记录,状态不变)→ 第 2 轮:fixed(迁状态)。
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:rt-a", Payload: rings.TestClaimRecord{
			ClaimID: "tc-v1", StepID: "retest-tc-v1-r1", UnitID: "rt-a",
			Type: rings.TestClaimRecRetest, Round: 1, RetestVerdict: rings.RetestVerdictUnfixed,
			EvidenceHead: "curl 200 全量用户", Note: "漏洞仍在"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:rt-b", Payload: rings.TestClaimRecord{
			ClaimID: "tc-v1", StepID: "retest-tc-v1-r2", UnitID: "rt-b",
			Type: rings.TestClaimRecRetest, Round: 2, RetestVerdict: rings.RetestVerdictFixed,
			EvidenceHead: "curl 403 已鉴权拦截", Note: "重放不再可触发"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:rt-b", Payload: rings.NodeStateChanged{
			NodeID: "tc-v1", Status: rings.TestClaimFixed}},
	)

	var list []map[string]any
	post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims", "", http.StatusOK, &list)
	if list[0]["status"] != "fixed" || list[0]["retest_verdict"] != "fixed" ||
		list[0]["retest_round"].(float64) != 2 || list[0]["retest_running"] != false {
		t.Fatalf("列表字段不符: %v", list[0])
	}
	raw, _ := json.Marshal(list[0]["retest_at"])
	if len(raw) < 4 {
		t.Fatalf("缺复测时刻: %v", list[0]["retest_at"])
	}

	var detail struct {
		Claim   map[string]any   `json:"claim"`
		Retests []map[string]any `json:"retests"`
	}
	post(t, h, http.MethodGet, "/api/tasks/"+task+"/claims/tc-v1", "", http.StatusOK, &detail)
	if len(detail.Retests) != 2 {
		t.Fatalf("详情应含两轮历史: %v", detail.Retests)
	}
	if detail.Retests[0]["verdict"] != "unfixed" || detail.Retests[1]["verdict"] != "fixed" {
		t.Fatalf("历史轮次序/结论不符: %v", detail.Retests)
	}
	if detail.Retests[1]["session_id"] != "retest-tc-v1-r2" || detail.Retests[1]["evidence_head"] == "" {
		t.Fatalf("历史条目缺会话锚/证据: %v", detail.Retests[1])
	}
}
