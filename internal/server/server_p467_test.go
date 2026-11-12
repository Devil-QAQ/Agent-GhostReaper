package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-67(问题清单 #86 A②,RoE 机制重构批次第二层):信息源清单与
// 攻击行为卡的面板 API 侧 ----
// RoE 信息源区读面(host+依据摘要)、撤销端点(与牧者标记同一账本
// 动作)、攻击行为卡待决读面(Kind=attack)、研判留痕 Note 人话化。

// p467ServerEnv 面板后端 + 渗透任务(种子 www.example.com)。
func p467ServerEnv(t *testing.T) (*Server, *httptest.Server, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const task = "p467-srv"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
	); err != nil {
		t.Fatal(err)
	}
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	gate := swarm.NewLedgerGate(store)
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, store
}

// TestP467InfoSourceRevokeEndpoint 完成标准④(面板侧):信息源区读面
// 可见(roe.info_sources 带依据摘要)→ 撤销端点 → 清单消、tg- 缓存
// 作废(revoked 留痕)→ 重复撤销如实 404。
func TestP467InfoSourceRevokeEndpoint(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	ctx := context.Background()
	const task = "p467-srv"
	if err := swarm.GrantInfoSource(ctx, store, task, "gitee.com", "代码托管,与目标无归属", "swarm:u1"); err != nil {
		t.Fatal(err)
	}
	// 读面:roe.info_sources 带依据。
	body := p467Get(t, ts, "/api/tasks/"+task+"/roe")
	var roe struct {
		InfoSources []rings.RoeInfoSource `json:"info_sources"`
	}
	if err := json.Unmarshal(body, &roe); err != nil {
		t.Fatal(err)
	}
	if len(roe.InfoSources) != 1 || roe.InfoSources[0].Host != "gitee.com" ||
		!strings.Contains(roe.InfoSources[0].Basis, "代码托管") {
		t.Fatalf("信息源读面不符: %+v", roe.InfoSources)
	}
	// 撤销(面板通道)。
	p467Post(t, ts, "/api/tasks/"+task+"/roe/infosources/revoke", map[string]string{"host": "gitee.com"})
	st, _ := store.CurrentRoE(ctx, task)
	if len(st.InfoSources) != 0 {
		t.Fatalf("撤销后清单应空: %+v", st.InfoSources)
	}
	if v, _ := swarm.TriageVerdictOf(ctx, store, task, "gitee.com"); v != swarm.TriageRevoked {
		t.Fatalf("撤销应落粘性标记,得 %q", v)
	}
	// 重复撤销:如实 404。
	resp, err := http.Post(ts.URL+"/api/tasks/"+task+"/roe/infosources/revoke",
		"application/json", strings.NewReader(`{"host":"gitee.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("重复撤销应 404,得 %d", resp.StatusCode)
	}
}

// TestP467ApprovalsAttackCardView 攻击行为卡待决读面:Kind=attack、
// Target=host[:port]、判定/留痕类型词不上界面(triage 记录 Note 人话化)。
func TestP467ApprovalsAttackCardView(t *testing.T) {
	srv, ts, store := p467ServerEnv(t)
	ctx := context.Background()
	const task = "p467-srv"
	// 经审批门登记攻击行为卡(单位侧同一入口)。
	ticket, err := srv.Gate.Enter(swarm.ApprovalRequest{
		TaskID: task, StepID: "s1", UnitID: "u1", Intent: "对信息源发起扫描",
		ActionLevel: "主动测试", ToolName: "bash",
		ToolArgs:     `{"command":"nmap -p 1-1000 gitee.com"}`,
		Note:         "攻击行为卡:gitee.com 是信息源,本次动作是主动攻击",
		AttackTarget: "gitee.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Gate.Expire(task, ticket.Req.ID, "测试收尾") }()
	// 研判留痕一笔(读面 Note 人话化验证)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
		Payload: swarm.BuildTriageRecord("gitee.com", swarm.TriageMethodical, "公开代码托管服务", rings.ApprovalRecord{}),
	}); err != nil {
		t.Fatal(err)
	}
	body := p467Get(t, ts, "/api/tasks/"+task+"/approvals")
	var out struct {
		Pending []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Target string `json:"target"`
		} `json:"pending"`
		Records []struct {
			Type string `json:"type"`
			Note string `json:"note"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	var attack int
	for _, p := range out.Pending {
		if p.Kind == "attack" && p.Target == "gitee.com" {
			attack++
		}
	}
	if attack != 1 {
		t.Fatalf("应恰 1 张攻击行为卡待决(Kind=attack, Target=gitee.com),得 %+v", out.Pending)
	}
	var triageNote int
	for _, r := range out.Records {
		if r.Type == "triage" {
			triageNote++
			if !strings.Contains(r.Note, "方法性访问") || !strings.Contains(r.Note, "公开代码托管服务") {
				t.Fatalf("研判留痕 Note 应人话化(结论+依据),得 %q", r.Note)
			}
			if strings.Contains(r.Note, "verdict=") || strings.Contains(r.Note, "basis=") {
				t.Fatalf("机器元数据不得上界面: %q", r.Note)
			}
		}
	}
	if triageNote != 1 {
		t.Fatalf("应恰 1 笔研判留痕记录,得 %d", triageNote)
	}
}

// p467Get GET JSON 断言 200。
func p467Get(t *testing.T, ts *httptest.Server, path string) []byte {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// p467Post POST JSON 断言 200。
func p467Post(t *testing.T, ts *httptest.Server, path string, payload any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b2, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, resp.StatusCode, string(b2))
	}
	return b2
}
