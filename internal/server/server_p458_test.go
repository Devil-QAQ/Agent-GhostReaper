package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/interject"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-58 全站全量扫描清剿:读侧等价对账 + 大账本秒开断言 ----

// p458Server 开测试后端(同 newInterjectTestServer 装配,带插话服务)。
func p458Server(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr, err := manager.New(manager.Config{Store: store, RootDir: dir})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, swarm.NewLedgerGate(store))
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	srv.RootDir = dir
	srv.Swarm = swarm.NewExecutor(store, nil, dir, swarm.Budget{})
	srv.Interject = &interject.Service{Store: store, RootDir: dir, Helm: srv.Swarm.Helm}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

// TestP458PanoramaLiveParity 在途全景实时视图等价对账:改后(投影直查)
// 与旧路径(NodesByType 全部步骤 + Graph 全图边,测试内复刻)对同一账本
// 产出完全一致的行集——显示内容不变是硬约束。
func TestP458PanoramaLiveParity(t *testing.T) {
	srv, ts := p458Server(t)
	ctx := t.Context()
	task := "p458-parity"
	if _, err := srv.Manager.CreateTask(ctx, task, "等价对账任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatal(err)
	}
	evs := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, AssetClass: rings.AssetHost, Title: "资产一"}},
		{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a2", NodeType: rings.NodeAsset, AssetClass: rings.AssetHost, Title: "资产二"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s-done", NodeType: rings.NodeStep, Title: "已完成步骤"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", Payload: rings.NodeStateChanged{NodeID: "s-done", Status: "done"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s-run", NodeType: rings.NodeStep, Title: "在跑步骤", Status: "running",
			Detail: map[string]any{"priority": "high", "kind": "falsify"}}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s-queue", NodeType: rings.NodeStep, Title: "排队步骤", Status: "dispatched"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s-wait", NodeType: rings.NodeStep, Title: "等前置步骤", Status: "dispatched"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s-run", ToNode: "a1"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s-queue", ToNode: "a2"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s-wait", ToNode: "a1"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s-wait", ToNode: "s-run"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s-queue", ToNode: "s-done"}},
	}
	if _, err := srv.Store.Append(ctx, task, evs...); err != nil {
		t.Fatal(err)
	}

	// 旧路径复刻(P4-58 改前实现:全量步骤 + 全图)。
	list, err := srv.Store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]rings.NodeFull{}
	for _, st := range list {
		steps[st.ID] = st
	}
	g, err := srv.Store.Graph(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	assetTitle := map[string]string{}
	covers, deps := map[string][]string{}, map[string][]string{}
	for _, n := range g.Nodes {
		if n.Type == rings.NodeAsset {
			assetTitle[n.ID] = n.Title
		}
	}
	for _, e := range g.Edges {
		switch e.Type {
		case rings.EdgeCovers:
			covers[e.From] = append(covers[e.From], e.To)
		case rings.EdgeDependsOn:
			deps[e.From] = append(deps[e.From], e.To)
		}
	}
	want := panoramaRows(steps, covers, deps, assetTitle, nil)

	resp, err := http.Get(ts.URL + "/api/tasks/" + task + "/panorama")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panorama 状态 = %d", resp.StatusCode)
	}
	var body struct {
		Panorama []PanoramaRow `json:"panorama"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body.Panorama, want) {
		t.Fatalf("全景行不一致:\n直查 = %+v\n旧路径 = %+v", body.Panorama, want)
	}
}

// p458SeedLarge 确定性灌入 5 万+事件的大账本任务(事件配比参照真实
// 任务:工具调用与流量留痕为主体,图事件/审计流按比例)。
func p458SeedLarge(t *testing.T, srv *Server, task string) {
	t.Helper()
	ctx := t.Context()
	if _, err := srv.Manager.CreateTask(ctx, task, "大账本任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatal(err)
	}
	var batch []rings.Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if _, err := srv.Store.Append(ctx, task, batch...); err != nil {
			t.Fatalf("灌入 %d 条: %v", len(batch), err)
		}
		batch = batch[:0]
	}
	push := func(ev rings.Event) {
		batch = append(batch, ev)
		if len(batch) >= 1000 {
			flush()
		}
	}
	push(rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "started"}})

	// 供流量详情直查的交换原文 blob(内容寻址写入,引用入账)。
	exBlobRef, err := srv.blobStore().Write([]byte(
		`{"request":{"method":"GET","url":"http://target.local/p0-0"},"response":{"status":200}}`))
	if err != nil {
		t.Fatal(err)
	}

	const nSteps = 2500
	// 资产 40(其中 10 个被覆盖)。
	for i := 0; i < 40; i++ {
		push(rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: fmt.Sprintf("a%d", i), NodeType: rings.NodeAsset, AssetClass: rings.AssetHost,
			Title: fmt.Sprintf("资产 %d", i)}})
	}
	// 步骤:dispatched → running → done(尾部留 8 个在途:4 running +
	// 4 dispatched);每步 1 条 added + 2 条状态迁移。
	for i := 0; i < nSteps; i++ {
		id := fmt.Sprintf("s%d", i)
		status := "dispatched"
		push(rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeStep, Title: fmt.Sprintf("意图 %d", i), Status: status,
			Detail: map[string]any{"goal_id": "g0"}}})
		if i >= nSteps-8 {
			if i%2 == 0 {
				push(rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
					Payload: rings.NodeStateChanged{NodeID: id, Status: "running"}})
			}
			continue // 在途:不收尾。
		}
		push(rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: id, Status: "running"}})
		push(rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: id, Status: "done"}})
		// 会话起止(投影 unit_sessions)。
		push(rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: id, UnitID: fmt.Sprintf("u%d", i%7), TranscriptID: fmt.Sprintf("tr-%s", id),
			Type: rings.SwarmRecSessionStarted}})
		// 每步 8 次工具调用。
		for k := 0; k < 8; k++ {
			push(rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
				StepID: id, UnitID: fmt.Sprintf("u%d", i%7), TranscriptID: fmt.Sprintf("tr-%s", id),
				Type: rings.SwarmRecToolCall, ToolName: "http_probe",
				ToolArgs:   json.RawMessage(`{"url":"http://target.local/path?i=` + fmt.Sprint(i) + `"}`),
				ResultHead: strings.Repeat("r", 96)}})
		}
		// 每步 6 条流量留痕(第一步第一条留已知名供详情直查)。
		for k := 0; k < 6; k++ {
			exID := fmt.Sprintf("ex-%s-%d", id, k)
			blob := fmt.Sprintf("sha256:%s-%d", id, k)
			if i == 0 && k == 0 {
				exID, blob = "ex-p458-probe", exBlobRef
			}
			push(rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rings.MirrorRecord{
				ExchangeID: exID, Type: rings.MirrorRecHTTP, StepID: id, UnitID: fmt.Sprintf("u%d", i%7),
				Method: "GET", URL: fmt.Sprintf("http://target.local/p%d-%d", i, k), Status: 200,
				ReqBytes: 120, RespBytes: 2048, ElapsedMS: 45, BlobRef: blob}})
		}
		push(rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: id, UnitID: fmt.Sprintf("u%d", i%7), Type: rings.SwarmRecSessionEnd,
			Note: "turns=8 status=completed"}})
		// 每 5 步覆盖一个资产 / 产出一个事实。
		if i%5 == 0 {
			push(rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: id, ToNode: fmt.Sprintf("a%d", i%10)}})
			fid := fmt.Sprintf("f%d", i)
			push(rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: fid, NodeType: rings.NodeFact, Title: fmt.Sprintf("事实 %d", i), Status: "candidate"}})
			push(rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: id, ToNode: fid}})
			push(rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
				Payload: rings.NodeStateChanged{NodeID: fid, Status: "confirmed"}})
		}
		// 每 25 步一条黑匣子心跳 + 一对舵杆 + 一条审批。
		if i%25 == 0 {
			push(rings.Event{Kind: rings.EventBlackboxRecord, Actor: "blackbox",
				Payload: rings.BlackboxRecord{Type: rings.BlackboxHeartbeat}})
			push(rings.Event{Kind: rings.EventSteerRecord, Actor: "creator", Payload: rings.SteerRecord{
				SteerID: fmt.Sprintf("st-%d", i), StepID: id, Command: rings.SteerCommandSteer,
				State: rings.SteerStateQueued, Message: "纠偏"}})
			push(rings.Event{Kind: rings.EventSteerRecord, Actor: "swarm:u1", Payload: rings.SteerRecord{
				SteerID: fmt.Sprintf("st-%d", i), StepID: id, Command: rings.SteerCommandSteer,
				State: rings.SteerStateDelivered, Message: "纠偏"}})
			push(rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
				RequestID: fmt.Sprintf("ap-%d", i), StepID: id, UnitID: "u1",
				Type: rings.ApprovalRecRequested, ActionLevel: rings.ActionActiveTest,
				Intent:   fmt.Sprintf("意图 %d", i),
				ToolName: "http_probe", ToolArgs: json.RawMessage(`{"url":"http://t/x"}`)}})
			push(rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
				RequestID: fmt.Sprintf("ap-%d", i), StepID: id, UnitID: "u1",
				Type: rings.ApprovalRecApproved, ActionLevel: rings.ActionActiveTest, ToolName: "http_probe",
				DecidedBy: "creator", WaitMillis: 3000}})
		}
		// 每 10 步一轮研判。
		if i%10 == 0 {
			push(rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind", Payload: rings.OvermindRecord{
				Type: rings.OvermindRecAssessment, SessionID: fmt.Sprintf("om-%d", i),
				EvaluatedRev: int64(i * 3), QAccess: "无新增", Dispatched: 1}})
		}
	}
	// 假设 300 个 + 评估/结论记录与验证边。
	for i := 0; i < 300; i++ {
		hid := fmt.Sprintf("h%d", i)
		push(rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: hid, NodeType: rings.NodeHypothesis, Title: fmt.Sprintf("假设 %d", i), Status: "candidate"}})
		push(rings.Event{Kind: rings.EventHypothesisRecord, Actor: "overmind", Payload: rings.HypothesisRecord{
			HypothesisID: hid, Type: rings.HypothesisRecEvaluation,
			Decision: rings.HypothesisVerify, Reason: "值得跟"}})
		if i == 0 {
			// 供详情直查的验证步骤 + 验证边 + 结论。
			push(rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: "sv0", NodeType: rings.NodeStep, Title: "验证假设 0", Status: "done"}})
			push(rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeVerifies, FromNode: "sv0", ToNode: hid}})
			push(rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u9", Payload: rings.SwarmRecord{
				StepID: "sv0", UnitID: "u9", TranscriptID: "tr-sv0", Type: rings.SwarmRecSessionStarted}})
			push(rings.Event{Kind: rings.EventHypothesisRecord, Actor: "swarm:u9", Payload: rings.HypothesisRecord{
				HypothesisID: hid, Type: rings.HypothesisRecResult, StepID: "sv0", UnitID: "u9",
				Verdict: "supported", EvidenceHead: "证据截头"}})
		}
	}
	flush()
	total, err := srv.Store.CurrentRevision(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if total < 50000 {
		t.Fatalf("灌入事件 %d 条,不足 5 万", total)
	}
}

// TestP458LargeLedgerFast 大账本(5 万+事件)下清剿清单各接口秒开:
// 每个改轻量接口实测耗时断言 < 1s(秒开口径;P4-58 完成标准 ②)。
func TestP458LargeLedgerFast(t *testing.T) {
	srv, ts := p458Server(t)
	task := "p458-large"
	p458SeedLarge(t, srv, task)

	cases := []struct{ name, path string }{
		{"在途全景", "/api/tasks/" + task + "/panorama"},
		{"研判轨迹(会话页主宰)", "/api/tasks/" + task + "/overmind"},
		{"舵杆列表", "/api/tasks/" + task + "/steers"},
		{"系统事件(会话页)", "/api/tasks/" + task + "/blackbox"},
		{"假设列表(发现页)", "/api/tasks/" + task + "/hypotheses"},
		{"假设详情(发现页)", "/api/tasks/" + task + "/hypotheses/h0"},
		{"观测·工具流水", "/api/obs/tools?task=" + task + "&limit=200"},
		{"观测·吞吐", "/api/obs/throughput?task=" + task},
		{"观测·拦截命中", "/api/obs/intercepts?task=" + task},
		{"流量·单条详情", "/api/mirror/exchanges/ex-p458-probe?task=" + task},
		{"统一图投影(进度图/年轮图/工作台)", "/api/tasks/" + task + "/graph"},
		{"插话视图(会话页子窗)", "/api/tasks/" + task + "/interject?target=overmind"},
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, tc := range cases {
		start := time.Now()
		resp, err := client.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: 状态 = %d", tc.name, resp.StatusCode)
		}
		n := 0
		buf := make([]byte, 32*1024)
		for {
			k, err := resp.Body.Read(buf)
			n += k
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		elapsed := time.Since(start)
		t.Logf("%s: %d 字节, 全程 %v", tc.name, n, elapsed)
		if n == 0 {
			t.Fatalf("%s: 空响应", tc.name)
		}
		// 秒开断言(P4-58 完成标准 ②):确定性灌入下清剿清单接口各 < 1s。
		if elapsed >= time.Second {
			t.Fatalf("%s: 耗时 %v ≥ 1s,未达秒开", tc.name, elapsed)
		}
	}
}

// TestP458ReadPathBeforeAfter 改前/改后读路径耗时对照(留证用):同一
// 5 万+事件账本上,复刻改前读逻辑(全量 Events(0,0) + 过滤 / 全图重建)
// 与改后读逻辑(kind 定向直查 / 投影直查)分别计时,数据面同源对账。
// 各计 3 轮取中位,弱化单次抖动。
func TestP458ReadPathBeforeAfter(t *testing.T) {
	srv, _ := p458Server(t)
	task := "p458-large"
	p458SeedLarge(t, srv, task)
	ctx := t.Context()

	median := func(ds []time.Duration) time.Duration {
		sorted := append([]time.Duration(nil), ds...)
		for i := 1; i < len(sorted); i++ {
			for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
				sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
			}
		}
		return sorted[len(sorted)/2]
	}
	run := func(name string, before, after func() int) {
		var bs, as []time.Duration
		var nb, na int
		for i := 0; i < 3; i++ {
			s := time.Now()
			nb = before()
			bs = append(bs, time.Since(s))
			s = time.Now()
			na = after()
			as = append(as, time.Since(s))
		}
		if nb != na {
			t.Errorf("%s: 前后数据面不一致(%d vs %d)", name, nb, na)
		}
		t.Logf("%s: 改前中位 %v → 改后中位 %v(行数 %d)", name, median(bs), median(as), na)
	}

	fullScan := func(kinds ...rings.EventKind) func() int {
		want := map[rings.EventKind]bool{}
		for _, k := range kinds {
			want[k] = true
		}
		return func() int {
			evs, err := srv.Store.Events(ctx, task, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, ev := range evs {
				if want[ev.Kind] {
					n++
				}
			}
			return n
		}
	}
	kindScan := func(kinds ...rings.EventKind) func() int {
		return func() int {
			evs, err := srv.Store.EventsByKinds(ctx, task, kinds...)
			if err != nil {
				t.Fatal(err)
			}
			return len(evs)
		}
	}

	run("系统事件(blackbox)", fullScan(rings.EventBlackboxRecord), kindScan(rings.EventBlackboxRecord))
	run("舵杆列表(steer)", fullScan(rings.EventSteerRecord), kindScan(rings.EventSteerRecord))
	run("流量单条(mirror)", fullScan(rings.EventMirrorRecord), kindScan(rings.EventMirrorRecord))
	run("观测·吞吐(图事件)", fullScan(rings.EventNodeAdded, rings.EventNodeStateChanged),
		kindScan(rings.EventNodeAdded, rings.EventNodeStateChanged))
	run("观测·工具流水(swarm+shepherd)", fullScan(rings.EventSwarmRecord, rings.EventShepherdRecord),
		kindScan(rings.EventSwarmRecord, rings.EventShepherdRecord))
	run("拦截命中(五类合并)", fullScan(rings.EventApprovalRecord, rings.EventNodeAdded,
		rings.EventNodeStateChanged, rings.EventTaskLifecycle, rings.EventSwarmRecord),
		kindScan(rings.EventApprovalRecord, rings.EventNodeAdded,
			rings.EventNodeStateChanged, rings.EventTaskLifecycle, rings.EventSwarmRecord))
	run("假设详情(四类合并)", fullScan(rings.EventNodeAdded, rings.EventNodeStateChanged,
		rings.EventHypothesisRecord, rings.EventSwarmRecord),
		kindScan(rings.EventNodeAdded, rings.EventNodeStateChanged,
			rings.EventHypothesisRecord, rings.EventSwarmRecord))
	run("研判轨迹(五类合并)", fullScan(rings.EventOvermindRecord, rings.EventOperatorDive,
		rings.EventNodeAdded, rings.EventNodeStateChanged, rings.EventEdgeAdded),
		kindScan(rings.EventOvermindRecord, rings.EventOperatorDive,
			rings.EventNodeAdded, rings.EventNodeStateChanged, rings.EventEdgeAdded))
	run("冬眠抵扣(生命周期)", fullScan(rings.EventTaskLifecycle), kindScan(rings.EventTaskLifecycle))

	// 在途全景:改前 = 全部步骤 + 全图重建(Graph);改后 = 投影直查。
	beforePano := func() int {
		list, err := srv.Store.NodesByType(ctx, task, rings.NodeStep, "")
		if err != nil {
			t.Fatal(err)
		}
		steps := map[string]rings.NodeFull{}
		for _, st := range list {
			steps[st.ID] = st
		}
		g, err := srv.Store.Graph(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		assetTitle := map[string]string{}
		covers, deps := map[string][]string{}, map[string][]string{}
		for _, n := range g.Nodes {
			if n.Type == rings.NodeAsset {
				assetTitle[n.ID] = n.Title
			}
		}
		for _, e := range g.Edges {
			switch e.Type {
			case rings.EdgeCovers:
				covers[e.From] = append(covers[e.From], e.To)
			case rings.EdgeDependsOn:
				deps[e.From] = append(deps[e.From], e.To)
			}
		}
		return len(panoramaRows(steps, covers, deps, assetTitle, nil))
	}
	afterPano := func() int {
		pl, err := srv.Store.PanoramaFlight(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		return len(panoramaRows(pl.Steps, pl.Covers, pl.Deps, pl.AssetTitle, nil))
	}
	run("在途全景(投影直查)", beforePano, afterPano)
}
