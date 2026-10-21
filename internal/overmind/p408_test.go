package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// P4-08 确定性单测:① 反证优先级提档(两处派发都 high)+ 同级调度
// tie-break(high 探索与反证并存时反证先领名额);③ 组装侧去同型
// (保留规则 + 变体全量入账);④ 描述生成的解析/回退(scripted 模型
// 走真实 agentcore 会话)。

// TestDispatchFalsifyPriorityHigh ①:首轮收割与重开轮派发的反证步骤
// priority 都落 high(P4-08 前缺省 normal,模型标 high 的探索会插队)。
func TestDispatchFalsifyPriorityHigh(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p408-prio"
	mkTask(t, store, task)

	seed := func(claimID, status string) {
		t.Helper()
		if _, err := store.Append(ctx, task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
				NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: "s1", NodeType: rings.NodeStep, Title: "发现步骤", Status: "done",
				Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
			rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
				NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "声明 " + claimID, Status: status,
				Detail: map[string]any{"unit_id": "u1", "step_id": "s1",
					"standard": arbiter.DefaultClaimStandard()}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: "web1"}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: "s1"}},
		); err != nil {
			t.Fatalf("铺声明 %s 失败: %v", claimID, err)
		}
	}

	// 首轮:候选收割派发。
	seed("tc-a", rings.TestClaimCandidate)
	if _, err := HarvestClaims(ctx, store, task); err != nil {
		t.Fatalf("收割声明失败: %v", err)
	}
	steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatalf("查步骤失败: %v", err)
	}
	found := false
	for _, s := range steps {
		if s.Detail["kind"] != "falsify" || s.Detail["testclaim_id"] != "tc-a" {
			continue
		}
		found = true
		if s.Detail["priority"] != PriorityHigh {
			t.Fatalf("① 首轮反证步骤 priority 应为 high: %+v", s.Detail)
		}
	}
	if !found {
		t.Fatalf("应派出 tc-a 的反证步骤: %+v", steps)
	}

	// 重开轮:Confirmed 声明重开后的反证步骤同样 high。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-a", Status: rings.TestClaimConfirmed}}); err != nil {
		t.Fatalf("置 confirmed 失败: %v", err)
	}
	if err := ReopenClaim(ctx, store, task, "tc-a", "manual", "面板反驳复验", "creator"); err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	steps, err = store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatalf("查步骤失败: %v", err)
	}
	found = false
	for _, s := range steps {
		if s.Detail["kind"] != "falsify" || !strings.Contains(s.Title, "重开") {
			continue
		}
		found = true
		if s.Detail["priority"] != PriorityHigh {
			t.Fatalf("① 重开轮反证步骤 priority 应为 high: %+v", s.Detail)
		}
	}
	if !found {
		t.Fatalf("应派出重开轮反证步骤: %+v", steps)
	}
}

// TestStepLaunchLess ①:领取次序全序——优先级降序;同级急活 > 反证 >
// 其余(探索稍让位是预期取舍);再按创建版本;ID 兜底全序。
func TestStepLaunchLess(t *testing.T) {
	mk := func(id string, rev int64, priority, kind string) rings.NodeFull {
		d := map[string]any{}
		if priority != "" && priority != PriorityNormal {
			d["priority"] = priority
		}
		if kind != "" {
			if kind == "falsify" {
				d["kind"] = "falsify"
			} else if kind == "urgent" {
				d["urgent"] = true
			}
		}
		return rings.NodeFull{NodeView: rings.NodeView{ID: id, CreatedRev: rev}, Detail: d}
	}
	cases := []struct {
		name   string
		a, b   rings.NodeFull
		aFirst bool // 期望 a 先领
	}{
		{"high 反证 先于 更早创建的 high 探索", mk("s-f", 9, "high", "falsify"), mk("s-e", 5, "high", ""), true},
		{"high 急活 先于 high 反证", mk("s-u", 9, "high", "urgent"), mk("s-f", 5, "high", "falsify"), true},
		{"high 探索 先于 normal 反证", mk("s-e", 9, "high", ""), mk("s-f", 5, "normal", "falsify"), true},
		{"normal 反证 先于 normal 探索", mk("s-f", 9, "normal", "falsify"), mk("s-e", 5, "normal", ""), true},
		{"low 沉底", mk("s-n", 9, "normal", ""), mk("s-l", 5, "low", ""), true},
		{"同级同类按创建版本", mk("s-a", 3, "high", ""), mk("s-b", 7, "high", ""), true},
	}
	for _, c := range cases {
		if got := StepLaunchLess(c.a, c.b); got != c.aFirst {
			t.Fatalf("%s: StepLaunchLess(a,b)=%v 期望 %v", c.name, got, c.aFirst)
		}
		if got := StepLaunchLess(c.b, c.a); got == StepLaunchLess(c.a, c.b) && c.a.ID != c.b.ID {
			// 非并列时全序必须反对称(同 rev 同类不同 ID 时按 ID 破并列)。
			t.Fatalf("%s: 全序须反对称", c.name)
		}
	}
	// 同 rev 同类:ID 兜底(两侧必有一真,确定性)。
	x, y := mk("s-x", 4, "high", ""), mk("s-y", 4, "high", "")
	if StepLaunchLess(x, y) == StepLaunchLess(y, x) {
		t.Fatal("同 rev 同类须按 ID 破并列(恰一侧为真)")
	}
}

// TestAssembleReproDedupShapes ③:去同型——同方法+同路径+同状态的重复
// 交换(查询串值差异/换 UA/近似 body 的反证变体流量)只保留该型首条;
// 状态不同自成型不合并(401→绕过→200 式叙事保留),不同路径的多步
// 流程原样保留;变体全量进 Variants(证据包 zip 维持全量),保留步骤
// 重编 1..n。
func TestAssembleReproDedupShapes(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p408-dedup"
	mkTask(t, store, task)
	seedScopeAssets(t, store, task, "http://k3.local")

	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "manager", Payload: rings.NodeAdded{
			NodeID: "s-v", NodeType: rings.NodeStep, Status: "done",
			Detail: map[string]any{"testclaim_id": "tc-x", "round": 1},
		},
	}); err != nil {
		t.Fatalf("铺反证步骤失败: %v", err)
	}
	recs := []rings.MirrorRecord{
		// 型 GET /status 500:单条(与 200 不同状态,自成型保留)。
		{ExchangeID: "m0", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/status", Status: 500, BlobRef: "b0"},
		// 型 GET /status 200:首条保留;同型变体(换查询串值 / 换 UA / 防缓存参数)全收起。
		{ExchangeID: "m1", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/status?cb=1", Status: 200, BlobRef: "b1"},
		{ExchangeID: "m2", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/status?cb=2", Status: 200, BlobRef: "b2"},
		{ExchangeID: "m3", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/status", Status: 200, BlobRef: "b3"},
		{ExchangeID: "m4", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/status?ts=999", Status: 200, BlobRef: "b4"},
		// 型 POST /login 200:两条近似 body 保留首条。
		{ExchangeID: "m5", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "POST", URL: "http://k3.local/login", Status: 200, BlobRef: "b5", ReqBytes: 24},
		{ExchangeID: "m6", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "POST", URL: "http://k3.local/login", Status: 200, BlobRef: "b6", ReqBytes: 31},
		// 多步流程(不同路径):原样保留,不误伤。
		{ExchangeID: "m7", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "http://k3.local/portal", Status: 302, BlobRef: "b7"},
		{ExchangeID: "m8", Type: rings.MirrorRecHTTPS, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "https://k3.local/admin", Status: 200, BlobRef: "b8"},
	}
	for _, rec := range recs {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rec,
		}); err != nil {
			t.Fatalf("铺镜留痕失败: %v", err)
		}
	}

	got, err := AssembleRepro(ctx, store, task, "tc-x", "high")
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(got.Steps) != 5 {
		t.Fatalf("去同型后应恰 5 步(500×1 + status200×1 + login×1 + portal + admin): %+v", got.Steps)
	}
	if len(got.Variants) != 4 {
		t.Fatalf("变体应恰 4 条(m2/m3/m4/m6): %+v", got.Variants)
	}
	want := []string{"m0", "m1", "m5", "m7", "m8"}
	for i, st := range got.Steps {
		if st.ExchangeID != want[i] {
			t.Fatalf("步骤 %d 应保留 %s(得到 %s): %+v", i+1, want[i], st.ExchangeID, st)
		}
		if st.Seq != i+1 {
			t.Fatalf("保留步骤应重编 1..n: %+v", st)
		}
		if st.Desc == "" {
			t.Fatalf("保留步骤应带描述(确定性兜底): %+v", st)
		}
	}
	for _, v := range got.Variants {
		if v.Desc != "" {
			t.Fatalf("变体不进复现步骤,无描述: %+v", v)
		}
		if v.BlobRef == "" || v.ExchangeID == "" {
			t.Fatalf("变体须携带审计归因(blob/交换 ID): %+v", v)
		}
	}
	// 同型保留首条(m1),后续同型变体(m2/m3/m4)全收起。
	if got.Steps[1].ExchangeID != "m1" || got.Steps[1].Status != 200 {
		t.Fatalf("保留规则 = 该型首条: %+v", got.Steps[1])
	}
}

// TestGenReproDescs ④:scripted 模型走真实 agentcore 会话——指令式
// 描述覆盖确定性描述并冻结入记录;模型坏回复回退确定性描述(裁决
// 不被描述生成阻塞);漏 seq 只回退该步。
func TestGenReproDescs(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p408-desc"
	mkTask(t, store, task)

	mkRec := func(fixed string) *rings.TestClaimRecord {
		return &rings.TestClaimRecord{
			ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{
				{Seq: 1, StepID: "s1", UnitID: "u2", Desc: "请求 /admin,目标返回 200",
					Method: "GET", URL: "http://k3.local/admin", Status: 200, BlobRef: "b1"},
				{Seq: 2, StepID: "s1", UnitID: "u2", Desc: "提交 /login(请求体 24 字节),目标返回 200",
					Method: "POST", URL: "http://k3.local/login", Status: 200, BlobRef: "b5"},
			},
		}
	}

	t.Run("正常生成", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "```json\n{\"steps\":[{\"seq\":1,\"desc\":\"向 k3.local 发 GET /admin,无需凭据;返回 200 即复现\"},{\"seq\":2,\"desc\":\"向 k3.local 发 POST /login,无需凭据;返回 200 即复现\"}]}\n```", Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := mkRec("")
		l.genReproDescs(ctx, "tc-x", rec)
		if !strings.HasPrefix(rec.Steps[0].Desc, "向 k3.local 发 GET /admin") ||
			!strings.HasPrefix(rec.Steps[1].Desc, "向 k3.local 发 POST /login") {
			t.Fatalf("④ 描述应为模型生成的指令式人话: %+v", rec.Steps)
		}
		// 事实包确实喂了模型(围栏/杂文宽容解析同样覆盖)。
		reqs := model.requests()
		if len(reqs) == 0 || !strings.Contains(reqs[0].LastUser, "k3.local") {
			t.Fatalf("应向模型提供步骤事实包: %+v", reqs)
		}
	})

	t.Run("坏回复回退", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "这不是 JSON", Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := mkRec("")
		l.genReproDescs(ctx, "tc-x", rec)
		if rec.Steps[0].Desc != "请求 /admin,目标返回 200" {
			t.Fatalf("坏回复应回退确定性描述: %+v", rec.Steps[0])
		}
	})

	t.Run("漏步骤部分回退", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"steps":[{"seq":1,"desc":"向 k3.local 发 GET /admin,无需凭据;返回 200 即复现"}]}`, Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := mkRec("")
		l.genReproDescs(ctx, "tc-x", rec)
		if !strings.HasPrefix(rec.Steps[0].Desc, "向 k3.local") {
			t.Fatalf("已生成步骤应冻结: %+v", rec.Steps[0])
		}
		if rec.Steps[1].Desc != "提交 /login(请求体 24 字节),目标返回 200" {
			t.Fatalf("漏 seq 的步骤应回退确定性描述: %+v", rec.Steps[1])
		}
	})

	t.Run("无Provider零外呼", func(t *testing.T) {
		l := &Loop{TaskID: task, Store: store}
		rec := mkRec("")
		l.genReproDescs(ctx, "tc-x", rec) // 不应 panic/改写
		if rec.Steps[0].Desc != "请求 /admin,目标返回 200" {
			t.Fatalf("无 Provider 应原样保留确定性描述: %+v", rec.Steps[0])
		}
	})
}
