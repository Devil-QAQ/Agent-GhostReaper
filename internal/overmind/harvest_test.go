package overmind

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHarvestRules(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "harvest-task"

	if err := SeedGoal(ctx, store, task, "g1", "目标", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if err := DispatchStep(ctx, store, task, "s1", "意图", DispatchOptions{GoalID: "g1", ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	// 三个候选:有内联证据 / 有 blob 引用证据 / 无证据
	for _, c := range []rings.NodeAdded{
		{NodeID: "f-ev", NodeType: rings.NodeFact, Title: "带内联证据的候选", Status: "candidate",
			Detail: map[string]any{"evidence": "port_probe 实测"}},
		{NodeID: "f-ref", NodeType: rings.NodeFact, Title: "带 blob 引用的候选", Status: "candidate",
			Detail: map[string]any{"evidence_ref": "sha256:" + "ab"}},
		{NodeID: "f-bare", NodeType: rings.NodeFact, Title: "无证据候选", Status: "candidate"},
	} {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: c,
		}); err != nil {
			t.Fatalf("种候选失败: %v", err)
		}
	}

	hv, err := Harvest(ctx, store, task)
	if err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	if len(hv.Confirmed) != 2 || len(hv.Skipped) != 1 {
		t.Fatalf("收割结果不符: confirmed=%v skipped=%+v", hv.Confirmed, hv.Skipped)
	}
	if hv.Skipped[0].FactID != "f-bare" || hv.Skipped[0].Reason == "" {
		t.Fatalf("跳过记录不符: %+v", hv.Skipped[0])
	}
	for _, id := range []string{"f-ev", "f-ref"} {
		n, err := store.Node(ctx, task, id)
		if err != nil || n.Status != "confirmed" {
			t.Fatalf("候选 %s 应转正: %+v err=%v", id, n, err)
		}
	}
	// 转正是主宰的事件:actor 归属可审计
	evs, _ := store.Events(ctx, task, 0, 0)
	confirmedByOvermind := 0
	for _, ev := range evs {
		if ev.Kind == rings.EventNodeStateChanged && ev.Actor == ActorOvermind {
			confirmedByOvermind++
		}
	}
	if confirmedByOvermind != 2 {
		t.Fatalf("转正事件应记在主宰名下,实际 %d 条", confirmedByOvermind)
	}
	// 幂等:再次收割无新转正;无证据候选仍在图上、每轮如实跳过
	hv2, err := Harvest(ctx, store, task)
	if err != nil || len(hv2.Confirmed) != 0 || len(hv2.Skipped) != 1 || hv2.Skipped[0].FactID != "f-bare" {
		t.Fatalf("二次收割应无新转正且如实跳过: %+v err=%v", hv2, err)
	}
	n, _ := store.Node(ctx, task, "f-bare")
	if n.Status != "candidate" {
		t.Fatalf("无证据候选应保持 candidate 不被丢弃: %+v", n)
	}
}

func TestDispatchStepBindsGraph(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "dispatch-task"
	// 资产先行(covers 边端点必须存在)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, AssetClass: rings.AssetHost, Title: "靶标"},
	}); err != nil {
		t.Fatalf("种资产失败: %v", err)
	}
	if err := DispatchStep(ctx, store, task, "s1", "探测靶标", DispatchOptions{
		GoalID: "g0", ExpectedObservation: "端口状态", ActionLevel: "只读探测", CoversAssets: []string{"a1"},
	}); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	n, err := store.Node(ctx, task, "s1")
	if err != nil {
		t.Fatalf("查 step 失败: %v", err)
	}
	if n.Status != "dispatched" || n.Detail["goal_id"] != "g0" ||
		n.Detail["expected_observation"] != "端口状态" || n.Detail["action_level"] != "只读探测" {
		t.Fatalf("step 节点不符: %+v", n)
	}
	covers, _ := store.Edges(ctx, task, rings.EdgeCovers)
	if len(covers) != 1 || covers[0].From != "s1" || covers[0].To != "a1" {
		t.Fatalf("covers 边不符: %+v", covers)
	}
}

// ---- P1-07:派发侧意图分级校验(Q8 落地的创建侧防线) ----

func TestDispatchStepRequiresActionLevel(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "dispatch-level"

	mkTask(t, store, task)
	// 空分级与未知分级一律拒绝。
	for i, level := range []string{"", "随便写", "read-only"} {
		if err := DispatchStep(ctx, store, task, fmt.Sprintf("s%d", i+1), "意图",
			DispatchOptions{ActionLevel: level}); err == nil {
			t.Fatalf("分级 %q 应被拒绝", level)
		}
	}
	// 四级合法,逐级可派发。
	for i, level := range []string{"只读探测", "主动测试", "利用深入", "修改与持久化"} {
		stepID := fmt.Sprintf("ok%d", i+1)
		if err := DispatchStep(ctx, store, task, stepID, "意图"+level,
			DispatchOptions{ActionLevel: level}); err != nil {
			t.Fatalf("合法分级 %s 不应被拒: %v", level, err)
		}
		n, err := store.Node(ctx, task, stepID)
		if err != nil || n.Detail["action_level"] != level {
			t.Fatalf("step %s 应携带分级 %s: %+v err=%v", stepID, level, n.Detail, err)
		}
	}
}

// mkTask 建任务行(生命周期投影前提)。
func mkTask(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: task},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
}

// TestAssembleReproIncludesHTTPS 定稿复现步骤收录解密的 https 交换
// (P3-01):与 http 同为可复现原文;tunnel 无原文,如实不收。P3-05b ①
// 起交换须落在种子资产授权范围内——本用例种子覆盖两个源。
func TestAssembleReproIncludesHTTPS(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "repro-https-task"
	mkTask(t, store, task)
	seedScopeAssets(t, store, task, "https://10.10.0.5:8443", "http://10.10.0.5:8080")

	// 声明的反证战役步骤(detail 绑定 testclaim_id)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "manager", Payload: rings.NodeAdded{
			NodeID: "s-v", NodeType: rings.NodeStep, Status: "done",
			Detail: map[string]any{"testclaim_id": "tc-x", "round": 1},
		},
	}); err != nil {
		t.Fatalf("铺反证步骤失败: %v", err)
	}
	// 镜留痕:https(解密)与 http 同收;tunnel 不收。
	for _, rec := range []rings.MirrorRecord{
		{ExchangeID: "ms-hs", Type: rings.MirrorRecHTTPS, StepID: "s-v", UnitID: "u2",
			Method: "GET", URL: "https://10.10.0.5:8443/admin", Status: 200, BlobRef: "sha256:hs"},
		{ExchangeID: "ms-hp", Type: rings.MirrorRecHTTP, StepID: "s-v", UnitID: "u2",
			Method: "POST", URL: "http://10.10.0.5:8080/login", Status: 302, BlobRef: "sha256:hp"},
		{ExchangeID: "ms-tn", Type: rings.MirrorRecTunnel, StepID: "s-v", UnitID: "u2",
			URL: "10.10.0.5:22", ReqBytes: 128, RespBytes: 256},
	} {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rec,
		}); err != nil {
			t.Fatalf("铺镜留痕失败: %v", err)
		}
	}

	got, _, err := AssembleRepro(ctx, store, nil, nil, task, "tc-x", "high")
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if got == nil || len(got.Steps) != 2 {
		t.Fatalf("应收录 http+https 两步(隧道无原文不收): %+v", got)
	}
	var sawHTTPS, sawHTTP bool
	for _, st := range got.Steps {
		switch st.BlobRef {
		case "sha256:hs":
			sawHTTPS = true
			if st.Method != "GET" || st.URL != "https://10.10.0.5:8443/admin" || st.Status != 200 {
				t.Fatalf("https 步骤摘要不符: %+v", st)
			}
		case "sha256:hp":
			sawHTTP = true
		}
		if st.Desc == "" {
			t.Fatalf("P3-05b ②:每步应带一句话描述: %+v", st)
		}
	}
	if !sawHTTPS || !sawHTTP {
		t.Fatalf("http/https 均应入复现步骤: %+v", got.Steps)
	}
}

// TestAssembleReproTargetScopeFilter P3-05b ①:复现步骤只收目标流量——
// 请求 host 在种子资产授权范围内的交换入步骤,外网查证流量(别的 host)
// 不入;host 级种子(无端口)覆盖任意端口,host:port 精确种子不放过
// 同 host 异端口。
func TestAssembleReproTargetScopeFilter(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "repro-scope-task"
	mkTask(t, store, task)
	// 种子:一个精确源(带端口)、一个 host 级源(裸域名)。
	seedScopeAssets(t, store, task, "http://127.0.0.1:4280", "dvwa.local")

	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "manager", Payload: rings.NodeAdded{
			NodeID: "s-f", NodeType: rings.NodeStep, Status: "done",
			Detail: map[string]any{"testclaim_id": "tc-s", "round": 1},
		},
	}); err != nil {
		t.Fatalf("铺反证步骤失败: %v", err)
	}
	for _, rec := range []rings.MirrorRecord{
		// 目标流量:精确源命中 / host 级命中(任意端口)。
		{ExchangeID: "x1", Type: rings.MirrorRecHTTP, StepID: "s-f", UnitID: "u1",
			Method: "GET", URL: "http://127.0.0.1:4280/login.php", Status: 200, BlobRef: "b1"},
		{ExchangeID: "x2", Type: rings.MirrorRecHTTPS, StepID: "s-f", UnitID: "u1",
			Method: "GET", URL: "https://dvwa.local:9443/admin", Status: 200, BlobRef: "b2"},
		// 外网查证流量:不入步骤。
		{ExchangeID: "x3", Type: rings.MirrorRecHTTPS, StepID: "s-f", UnitID: "u1",
			Method: "GET", URL: "https://www.baidu.com/robots.txt", Status: 200, BlobRef: "b3"},
		// 同 host 异端口:精确种子不覆盖,不入。
		{ExchangeID: "x4", Type: rings.MirrorRecHTTP, StepID: "s-f", UnitID: "u1",
			Method: "GET", URL: "http://127.0.0.1:8080/", Status: 200, BlobRef: "b4"},
	} {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rec,
		}); err != nil {
			t.Fatalf("铺镜留痕失败: %v", err)
		}
	}

	got, _, err := AssembleRepro(ctx, store, nil, nil, task, "tc-s", "medium")
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if got == nil || len(got.Steps) != 2 {
		t.Fatalf("应恰收 2 条目标流量(外网与异端口不入): %+v", got)
	}
	if got.Steps[0].ExchangeID != "x1" || got.Steps[1].ExchangeID != "x2" {
		t.Fatalf("收录次序或对象不符: %+v", got.Steps)
	}
	for _, st := range got.Steps {
		if st.Desc == "" {
			t.Fatalf("每步应带一句话描述: %+v", st)
		}
	}
	// 描述口径抽查:GET 带 200 有响应字节时给出定性句。
	if d := got.Steps[0].Desc; !strings.Contains(d, "请求 /login.php") || !strings.Contains(d, "目标返回 200") {
		t.Fatalf("描述口径不符: %q", d)
	}
}

// TestReproStepDesc 描述生成的确定性口径(P3-05b ②):动作按方法、
// 请求体大小、跳转/异常定性;截断路径不越界。
func TestReproStepDesc(t *testing.T) {
	cases := []struct {
		rec  rings.MirrorRecord
		want []string // 子串断言
		bad  []string
	}{
		{rings.MirrorRecord{Method: "POST", URL: "http://h/login", Status: 302, ReqBytes: 64},
			[]string{"提交 /login(请求体 64 字节)", "目标 302 跳转"}, nil},
		{rings.MirrorRecord{Method: "GET", URL: "https://h:8443/a/b?x=1", Status: 200, RespBytes: 335},
			[]string{"请求 /a/b", "目标返回 200(335 字节)"}, nil},
		{rings.MirrorRecord{Method: "DELETE", URL: "http://h/item/1", Status: 204},
			[]string{"请求删除 /item/1", "目标返回 204"}, nil},
		// P4-62 B ①:error 交换只进过程清单,不再进候选步骤——确定性描述
		// 的"未获响应(错误原文)"分支已随直出路径删除;Status 0 无错误仍
		// 如实说"未获响应"。
		{rings.MirrorRecord{Method: "GET", URL: "http://h/x", Status: 0},
			[]string{"未获响应"}, []string{"connection refused"}},
		{rings.MirrorRecord{Method: "GET", URL: "http://h/" + strings.Repeat("p", 300), Status: 200},
			[]string{"…"}, nil},
	}
	for i, c := range cases {
		got := reproStepDesc(c.rec)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Fatalf("用例 %d 描述 %q 应含 %q", i, got, w)
			}
		}
		for _, b := range c.bad {
			if strings.Contains(got, b) {
				t.Fatalf("用例 %d 描述 %q 不应含 %q", i, got, b)
			}
		}
	}
}

// seedScopeAssets 铺种子资产节点(服务类,标识即目标源;P3-05b ① 定稿过滤$
// 的授权范围来源)。
func seedScopeAssets(t *testing.T, store *rings.Store, task string, titles ...string) {
	t.Helper()
	for _, title := range titles {
		if _, err := store.Append(context.Background(), task, rings.Event{
			Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
				NodeID: "a-" + title, NodeType: rings.NodeAsset, AssetClass: rings.AssetService,
				Title: title,
			},
		}); err != nil {
			t.Fatalf("铺种子资产失败: %v", err)
		}
	}
}
