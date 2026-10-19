package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-09 完成标准(面板成品) ----
// ① 建任务种子表单(12 类资产输入,"资产同步"v1)与资产树读投影;
// ② 熔断账本可审计(谁在何时熔断了哪个任务);
// ③ 面板同时跑两个任务、熔断一个、另一个不受影响(全栈 E2E)。

// TestServerTaskSeedFormAndAssetTree 种子表单 → 资产入图(12 类 + sub_of
// 层级)→ 资产树 API 读投影;坏输入 400 且零残留;覆盖标志随图生长翻转。
func TestServerTaskSeedFormAndAssetTree(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	ctx := context.Background()

	// 种子表单:四层链(domain → subdomain → host → service)+ 独立
	// leak_info,共 5 资产 3 条 sub_of。
	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{
		"id":"seed-form","title":"资产同步 v1 验证",
		"assets":[
			{"class":"domain","title":"example.com"},
			{"class":"subdomain","title":"www.example.com","parent":"example.com"},
			{"class":"host","title":"web-01","parent":"www.example.com"},
			{"class":"service","title":"http://10.10.0.5:8080","parent":"web-01"},
			{"class":"leak_info","title":"git 仓库暴露"}
		]}`, http.StatusOK, &created)
	if created["id"] != "seed-form" {
		t.Fatalf("建任务响应不符: %+v", created)
	}

	// 资产树 API:节点字段 + 层级边(child → parent)。
	type node struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Class      string `json:"class"`
		Status     string `json:"status"`
		Covered    bool   `json:"covered"`
		FactCount  int    `json:"fact_count"`
		Hypothesis int    `json:"hypothesis_count"`
	}
	var tree struct {
		Assets []node                      `json:"assets"`
		Tree   []struct{ From, To string } `json:"hierarchy"`
	}
	get(t, h, "/api/tasks/seed-form/assets", http.StatusOK, &tree)
	if len(tree.Assets) != 5 || len(tree.Tree) != 3 {
		t.Fatalf("资产树不符: %+v", tree)
	}
	byTitle := map[string]node{}
	for _, a := range tree.Assets {
		byTitle[a.Title] = a
	}
	for _, want := range []struct{ title, class string }{
		{"example.com", "domain"}, {"www.example.com", "subdomain"},
		{"web-01", "host"}, {"http://10.10.0.5:8080", "service"},
		{"git 仓库暴露", "leak_info"},
	} {
		got := byTitle[want.title]
		if got.ID == "" || got.Class != want.class || got.Status != "active" {
			t.Fatalf("资产 %q 投影不符: %+v", want.title, got)
		}
		if got.Covered || got.FactCount != 0 || got.Hypothesis != 0 {
			t.Fatalf("新种子资产应未覆盖零计数: %+v", got)
		}
	}
	subOf := map[string]string{} // child → parent title
	titleOf := map[string]string{}
	for _, a := range tree.Assets {
		titleOf[a.ID] = a.Title
	}
	for _, e := range tree.Tree {
		subOf[titleOf[e.From]] = titleOf[e.To]
	}
	if subOf["www.example.com"] != "example.com" || subOf["web-01"] != "www.example.com" ||
		subOf["http://10.10.0.5:8080"] != "web-01" {
		t.Fatalf("sub_of 层级不符: %+v", subOf)
	}

	// 账本:种子入账 actor=creator(node.added × 5 + edge.added × 3)。
	evs, _ := store.Events(ctx, "seed-form", 0, 0)
	var nodeEv, edgeEv int
	for _, ev := range evs {
		if ev.Actor != "creator" {
			continue
		}
		switch ev.Kind {
		case rings.EventNodeAdded:
			nodeEv++
		case rings.EventEdgeAdded:
			edgeEv++
		}
	}
	if nodeEv != 5 || edgeEv != 3 {
		t.Fatalf("种子入账不符: node.added=%d edge.added=%d", nodeEv, edgeEv)
	}

	// 覆盖生长:一个 Step covers 服务资产后,投影 covered 翻转、计数跟进。
	svc := byTitle["http://10.10.0.5:8080"].ID
	if _, err := store.Append(ctx, "seed-form",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测服务", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": rings.ActionReadOnly}},
		}, rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: svc}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "服务存活", Status: "candidate"},
		}, rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: svc}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "f1", Status: "confirmed"}},
	); err != nil {
		t.Fatalf("图生长记账失败: %v", err)
	}
	var tree2 struct {
		Assets []node `json:"assets"`
	}
	get(t, h, "/api/tasks/seed-form/assets", http.StatusOK, &tree2)
	for _, a := range tree2.Assets {
		if a.Title == "http://10.10.0.5:8080" {
			if !a.Covered || a.FactCount != 1 {
				t.Fatalf("覆盖生长后资产树应反映: %+v", a)
			}
		}
	}

	// 坏输入 400 且零残留(校验全部前置,任务不建)。
	bad := []string{
		`{"title":"坏类","assets":[{"class":"planet","title":"x"}]}`,                                                      // 未知类
		`{"title":"空类","assets":[{"class":"","title":"x"}]}`,                                                            // 表单受限输入,类必填
		`{"title":"空标识","assets":[{"class":"host","title":" "}]}`,                                                       // 空标识
		`{"title":"父不存在","assets":[{"class":"host","title":"x","parent":"无"}]}`,                                         // 父不在表单内
		`{"title":"自引","assets":[{"class":"host","title":"x","parent":"x"}]}`,                                           // 父=自身
		`{"title":"成环","assets":[{"class":"host","title":"a","parent":"b"},{"class":"host","title":"b","parent":"a"}]}`, // 父链成环
		`{"title":"重复标识","assets":[{"class":"host","title":"a"},{"class":"host","title":"a"}]}`,                         // 标识重复
		`{"title":"  "}`, // 空标题
	}
	var errBody map[string]string
	for _, body := range bad {
		post(t, h, http.MethodPost, "/api/tasks", body, http.StatusBadRequest, &errBody)
	}
	rows, _ := store.Tasks(ctx)
	if len(rows) != 1 {
		t.Fatalf("坏输入不得残留任务: %d", len(rows))
	}
}

// TestServerTaskStopLedger 熔断按钮 API:面板状态翻转 + 账本可审计
// ("谁在何时熔断了哪个任务":actor + detail.operator + 时刻)。
func TestServerTaskStopLedger(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(mgr.Close)
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	ctx := context.Background()

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{"id":"fuse-ledger","title":"熔断审计"}`, http.StatusOK, &created)

	// 不存在的任务 404。
	var errBody map[string]string
	post(t, h, http.MethodPost, "/api/tasks/no-such/stop", `{"reason":"x"}`, http.StatusNotFound, &errBody)

	// 熔断(带理由)→ 面板状态 stopped。
	var stopped map[string]string
	post(t, h, http.MethodPost, "/api/tasks/fuse-ledger/stop",
		`{"reason":"范围变更,停止本任务"}`, http.StatusOK, &stopped)
	if stopped["status"] != "stopped" {
		t.Fatalf("熔断响应不符: %+v", stopped)
	}
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	if tasks[0]["status"] != "stopped" {
		t.Fatalf("面板任务列表应显示 stopped: %+v", tasks[0])
	}

	// 账本回答"谁在何时熔断了哪个任务"。
	evs, _ := store.Events(ctx, "fuse-ledger", 0, 0)
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle || !strings.Contains(string(ev.Payload), `"stopped"`) {
			continue
		}
		found = true
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action != "stopped" {
			continue
		}
		if ev.Actor != "manager" || p.Detail["operator"] != "creator" || p.Detail["reason"] != "范围变更,停止本任务" {
			t.Fatalf("stopped 事件审计字段不符: actor=%s detail=%+v", ev.Actor, p.Detail)
		}
		if ev.OccurredAt.IsZero() || ev.RecordedAt.IsZero() {
			t.Fatalf("stopped 事件须携带时刻: %+v", ev)
		}
	}
	if !found {
		t.Fatalf("账本应有 stopped 事件")
	}

	// 幂等:已停任务再按仍 200,不重复记账。
	post(t, h, http.MethodPost, "/api/tasks/fuse-ledger/stop", `{}`, http.StatusOK, &stopped)
	evs2, _ := store.Events(ctx, "fuse-ledger", 0, 0)
	stopCount := 0
	for _, ev := range evs2 {
		if ev.Kind == rings.EventTaskLifecycle && strings.Contains(string(ev.Payload), `"stopped"`) {
			stopCount++
		}
	}
	if stopCount != 1 {
		t.Fatalf("重复熔断应幂等(恰 1 条 stopped): %d", stopCount)
	}
}

// TestServerTaskFuseIsolationE2E 完成标准主判据:面板同时跑两个任务
// (各自拆解/虫群在飞)→ 熔断一个 → 另一个不受影响跑完全链路;
// 熔断账本可审计。全栈:mock 靶站 ×2 + 脚本模型(带闸门)+ Manager
// (每任务主宰循环)+ 面板 HTTP。
func TestServerTaskFuseIsolationE2E(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()

	targetA, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起靶站 A 失败: %v", err)
	}
	t.Cleanup(targetA.Close)
	targetB, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起靶站 B 失败: %v", err)
	}
	t.Cleanup(targetB.Close)
	urlA, urlB := targetA.MainURL(), targetB.MainURL()

	// 脚本模型:拆解按"未覆盖 service"派一条 L1 只读意图(状态感知:
	// 无未覆盖即空);虫群 port_probe 一次 → submit_candidate_fact → 结束。
	// 两任务按各自靶站 URL 自然分流(task_id 隔离,态势各看各的图)。
	uncoveredRe := regexp.MustCompile(`-\s+(\S+) \[service\] (\S+) \(未覆盖\)`)
	packRe := regexp.MustCompile(`-\s+(\S+) \[service\] (\S+)`)
	model := newSModel(func(rr *srecorded) sreply {
		if len(rr.ToolsOffered) == 0 {
			if m := uncoveredRe.FindStringSubmatch(rr.LastUser); m != nil {
				return sreply{Text: fmt.Sprintf(
					`{"new_intents":[{"intent":"对 %s 做只读端口探测","expected_observation":"state=open","action_level":"只读探测","covers":["%s"]}]}`,
					m[2], m[1])}
			}
			return sreply{Text: `{"new_intents":[]}`}
		}
		m := packRe.FindStringSubmatch(rr.LastUser)
		if m == nil {
			return sreply{Text: "无绑定资产,结束。"}
		}
		host, port := splitHostPort(t, m[2])
		switch {
		case !sSeenToolResult(rr, "port_probe", "state="):
			return sreply{ToolCalls: []struct {
				ID, Name, Args string
			}{{ID: "p1", Name: "port_probe", Args: fmt.Sprintf(`{"host":%q,"port":%s}`, host, port)}}}
		case !sSeenToolResult(rr, "submit_candidate_fact", "已入候选事实"):
			return sreply{ToolCalls: []struct {
				ID, Name, Args string
			}{{ID: "c1", Name: "submit_candidate_fact", Args: fmt.Sprintf(
				`{"statement":"%s 端口开放服务存活","evidence":"port_probe state=open","asset_id":%q}`, m[2], m[1])}}}
		default:
			return sreply{Text: "探测完成,候选事实已提交。"}
		}
	})
	// 闸门:虫群请求挂起在飞,供熔断打断"正在执行"的单位。
	model.unitGate = make(chan struct{})
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
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	// 经面板建两个任务(种子表单,各带一个 service 资产,建后自动唤醒);
	// 默认并发上限 2,两任务同时拉起。
	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", fmt.Sprintf(
		`{"id":"fuse-a","title":"任务 A(将被熔断)","assets":[{"class":"service","title":%q}]}`, urlA),
		http.StatusOK, &created)
	post(t, h, http.MethodPost, "/api/tasks", fmt.Sprintf(
		`{"id":"fuse-b","title":"任务 B(须不受影响)","assets":[{"class":"service","title":%q}]}`, urlB),
		http.StatusOK, &created)

	// 各自补一个带谓词目标(硬终止):该资产上有 confirmed 事实即达成。
	for _, tc := range []struct{ task, url string }{{"fuse-a", urlA}, {"fuse-b", urlB}} {
		var tree struct {
			Assets []struct {
				ID string `json:"id"`
			} `json:"assets"`
		}
		get(t, h, "/api/tasks/"+tc.task+"/assets", http.StatusOK, &tree)
		if len(tree.Assets) != 1 {
			t.Fatalf("任务 %s 种子资产不符: %+v", tc.task, tree)
		}
		post(t, h, http.MethodPost, "/api/tasks/"+tc.task+"/goals", fmt.Sprintf(
			`{"title":"摸清服务存活","predicate":{"op":"all_of","conditions":[{"kind":"fact_confirmed","asset_id":%q,"min":1}]}}`,
			tree.Assets[0].ID), http.StatusOK, &created)
		post(t, h, http.MethodPost, "/api/tasks/"+tc.task+"/wake", `{}`, http.StatusOK, &created)
	}

	// 两任务同时在跑:面板两 started + 两任务各有拆解请求 + 两虫群请求
	// 已挂起在飞(闸门)。
	waitUntil(t, 60*time.Second, "两任务的虫群请求都到达并挂起", func() bool {
		units := 0
		for _, rr := range model.snapshot() {
			if len(rr.ToolsOffered) > 0 {
				units++
			}
		}
		return units >= 2
	})
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	statusOf := map[string]string{}
	for _, tk := range tasks {
		statusOf[fmt.Sprint(tk["id"])] = fmt.Sprint(tk["status"])
	}
	if statusOf["fuse-a"] != "started" || statusOf["fuse-b"] != "started" {
		t.Fatalf("熔断前两任务应同时 started: %+v", statusOf)
	}
	decomposeOf := func(marker string) int {
		n := 0
		for _, rr := range model.snapshot() {
			if len(rr.ToolsOffered) == 0 && strings.Contains(rr.LastUser, marker) {
				n++
			}
		}
		return n
	}
	if decomposeOf(urlA) < 1 || decomposeOf(urlB) < 1 {
		t.Fatalf("两任务应各有真实拆解活动: A=%d B=%d", decomposeOf(urlA), decomposeOf(urlB))
	}

	// ---- 熔断 A(面板按钮);B 的在飞单位仍挂在闸门上 ----
	var stopped map[string]string
	post(t, h, http.MethodPost, "/api/tasks/fuse-a/stop",
		`{"reason":"演练:熔断 A 验证隔离"}`, http.StatusOK, &stopped)

	// 放行闸门:B 的在飞单位继续执行;A 的请求已被任务 ctx 取消。
	close(model.unitGate)

	// B 不受影响,跑完全链路:步骤 done → 候选收割 confirmed → 目标达成。
	waitUntil(t, 60*time.Second, "任务 B 目标经仲裁者判定达成", func() bool {
		goals, err := store.NodesByType(ctx, "fuse-b", rings.NodeGoal, "")
		return err == nil && len(goals) == 1 && goals[0].Status == "achieved"
	})
	steps, _ := store.NodesByType(ctx, "fuse-b", rings.NodeStep, "")
	if len(steps) == 0 || steps[0].Status != "done" {
		t.Fatalf("B 的步骤应完成: %+v", steps)
	}
	facts, _ := store.NodesByType(ctx, "fuse-b", rings.NodeFact, "confirmed")
	if len(facts) != 1 {
		t.Fatalf("B 应有 confirmed 事实: %+v", facts)
	}
	// B 不受影响,跑完全链路后进入完成终态(P1-10:全部目标经仲裁者
	// 判定达成,任务 completed——比"仍 started"更强的隔离证明:B 是自己
	// 干完收尾的,不是被 A 的熔断波及)。
	waitUntil(t, 60*time.Second, "任务 B 进入完成终态", func() bool {
		get(t, h, "/api/tasks", http.StatusOK, &tasks)
		for _, tk := range tasks {
			if tk["id"] == "fuse-b" {
				return tk["status"] == "completed"
			}
		}
		return false
	})

	// A 冻结:无 confirmed 事实、目标保持 active、拆解请求数不再增长。
	factsA, _ := store.NodesByType(ctx, "fuse-a", rings.NodeFact, "confirmed")
	if len(factsA) != 0 {
		t.Fatalf("被熔断任务不得再产出 confirmed 事实: %+v", factsA)
	}
	goalsA, _ := store.NodesByType(ctx, "fuse-a", rings.NodeGoal, "")
	if len(goalsA) != 1 || goalsA[0].Status != "active" {
		t.Fatalf("被熔断任务的目标应保持 active: %+v", goalsA)
	}
	before := decomposeOf(urlA)
	time.Sleep(1 * time.Second)
	if after := decomposeOf(urlA); after != before {
		t.Fatalf("被熔断任务不得继续拆解: before=%d after=%d", before, after)
	}

	// 账本可审计:谁(operator=creator)、何时(occurred_at)、哪个任务
	// (task_id=fuse-a)熔断,理由留痕。
	evs, _ := store.Events(ctx, "fuse-a", 0, 0)
	audited := false
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Action != "stopped" {
			continue
		}
		if ev.Actor == "manager" && p.Detail["operator"] == "creator" &&
			p.Detail["reason"] == "演练:熔断 A 验证隔离" && ev.TaskID == "fuse-a" && !ev.OccurredAt.IsZero() {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("账本应能回答谁在何时熔断了哪个任务")
	}
}
