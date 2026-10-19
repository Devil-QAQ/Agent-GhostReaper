package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
	"ghostreaper/internal/swarm"
)

// ---- P2-12 观测 API:Token 仪表盘 / 工具流水 / 吞吐指标 ----
//
// 对账口径:面板数字必须与原始记账一致——token 聚合 = token 独立表逐行
// 求和;工具流水 = 账本 tool_call 事件计数;吞吐 = 事件流状态机重放。

// obsSetup 建两任务并按显式时刻铺垫步骤状态机 / 工具调用 / token 行。
func obsSetup(t *testing.T) (http.Handler, *rings.Store, string, string) {
	t.Helper()
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

	mkTask := func(title string) string {
		var created map[string]string
		post(t, h, http.MethodPost, "/api/tasks", `{"title":"`+title+`"}`, http.StatusOK, &created)
		return created["id"]
	}
	taskA, taskB := mkTask("观测任务甲"), mkTask("观测任务乙")

	// 步骤状态机(显式时刻,跨任务交错):
	//   甲 s1 @+0s 派发 → @+8s 领取(等待 8s)→ @+60s 完成
	//   乙 s1 @+1s 派发 → @+30s 领取(等待 29s)→ 完成
	//   甲 s2 @+2s 派发 → @+9s 领取(等待 7s)→ 完成
	//   甲 s3 @+3s 派发 → 无人领取(仍在队列)
	// 期望队列深度:1,2,3,4(峰),3,2,1(终)。
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	appendEvents(t, store, taskA,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(0), Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测甲站", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", OccurredAt: at(8), Payload: rings.NodeStateChanged{
			NodeID: "s1", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", OccurredAt: at(60), Payload: rings.NodeStateChanged{
			NodeID: "s1", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(2), Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "探测乙路径", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", OccurredAt: at(9), Payload: rings.NodeStateChanged{
			NodeID: "s2", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", OccurredAt: at(40), Payload: rings.NodeStateChanged{
			NodeID: "s2", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(3), Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "候补意图", Status: "dispatched"}},
		// 工具调用:甲 s1 两次(http_get 成功 / bash 失败)。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", OccurredAt: at(20), Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall,
			ToolName: "http_get", ToolArgs: []byte(`{"url":"http://mock/"}`),
			ResultHead: "200 OK index", ResultRef: "sha256:aa11"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", OccurredAt: at(26), Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall,
			ToolName: "bash", ToolArgs: []byte(`{"cmd":"ls"}`), IsError: true,
			ResultHead: "exit 1"}},
	)
	appendEvents(t, store, taskB,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(1), Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测乙站", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3", OccurredAt: at(30), Payload: rings.NodeStateChanged{
			NodeID: "s1", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3", OccurredAt: at(50), Payload: rings.NodeStateChanged{
			NodeID: "s1", Status: "done"}},
		// 工具调用:乙一次(虫群)+ 牧者一次。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3", OccurredAt: at(35), Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u3", Type: rings.SwarmRecToolCall,
			ToolName: "http_get", ToolArgs: []byte(`{"url":"http://mock/b"}`),
			ResultHead: "200 OK b"}},
		rings.Event{Kind: rings.EventShepherdRecord, Actor: shepherd.ActorShepherd, OccurredAt: at(45), Payload: rings.ShepherdRecord{
			MessageID: "m1", Type: rings.ShepherdRecToolCall,
			ToolName: "task_snapshot", ResultHead: "3 步骤"}},
	)

	// token 独立表:两任务 / 两模型 / 三角色,合计已知。
	seedTokens := []rings.TokenUsage{
		{TaskID: taskA, Agent: "swarm:u1", Model: "glm-4.7", Turn: 1, InputTokens: 1000, OutputTokens: 100, OccurredAt: at(10)},
		{TaskID: taskA, Agent: "swarm:u1", Model: "glm-4.7", Turn: 2, InputTokens: 2000, OutputTokens: 200, CacheReadTokens: 500, OccurredAt: at(20)},
		{TaskID: taskA, Agent: "overmind", Model: "glm-4.7", Turn: 1, InputTokens: 300, OutputTokens: 30, OccurredAt: at(31)},
		{TaskID: taskB, Agent: "swarm:u3", Model: "gpt-4o", Turn: 1, InputTokens: 4000, OutputTokens: 400, CacheWriteTokens: 800, OccurredAt: at(36)},
		{TaskID: taskB, Agent: "shepherd", Model: "gpt-4o", Turn: 1, InputTokens: 700, OutputTokens: 70, OccurredAt: at(46)},
	}
	if err := store.RecordTokens(context.Background(), seedTokens...); err != nil {
		t.Fatalf("铺垫 token 行失败: %v", err)
	}
	return h, store, taskA, taskB
}

// TestObsTokensReconcile Token 仪表盘与 token 独立表逐项对账。
func TestObsTokensReconcile(t *testing.T) {
	h, store, taskA, taskB := obsSetup(t)

	var body struct {
		Totals struct {
			Calls      int `json:"calls"`
			Tasks      int `json:"tasks"`
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cache_read"`
			CacheWrite int `json:"cache_write"`
		} `json:"totals"`
		ByTask []struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Calls   int    `json:"calls"`
			Input   int    `json:"input"`
			Models  int    `json:"models"`
			FirstAt string `json:"first_at"`
			LastAt  string `json:"last_at"`
		} `json:"by_task"`
		ByModel []struct {
			Name  string `json:"name"`
			Calls int    `json:"calls"`
			Input int    `json:"input"`
		} `json:"by_model"`
		ByAgent []struct {
			Name  string `json:"name"`
			Calls int    `json:"calls"`
		} `json:"by_agent"`
		Series []struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cache_read"`
			CacheWrite int `json:"cache_write"`
		} `json:"series"`
	}
	get(t, h, "/api/obs/tokens", http.StatusOK, &body)

	// 对账基准:token 独立表逐行求和(直接再读一遍表)。
	rows, err := store.AllTokenUsageRows(t.Context())
	if err != nil {
		t.Fatalf("读 token 表失败: %v", err)
	}
	var sumIn, sumOut, sumCR, sumCW int
	for _, r := range rows {
		sumIn += r.InputTokens
		sumOut += r.OutputTokens
		sumCR += r.CacheReadTokens
		sumCW += r.CacheWriteTokens
	}
	if body.Totals.Calls != len(rows) || body.Totals.Tasks != 2 ||
		body.Totals.Input != sumIn || body.Totals.Output != sumOut ||
		body.Totals.CacheRead != sumCR || body.Totals.CacheWrite != sumCW {
		t.Fatalf("总量与 token 表不符: got %+v want calls=%d input=%d output=%d cache_read=%d cache_write=%d",
			body.Totals, len(rows), sumIn, sumOut, sumCR, sumCW)
	}
	if sumIn != 8000 || sumOut != 800 || sumCR != 500 || sumCW != 800 {
		t.Fatalf("种子基准自验失败: %d/%d/%d/%d", sumIn, sumOut, sumCR, sumCW)
	}

	// 按任务:甲 3 次调用(2 模型之一 glm-4.7)、乙 2 次。
	byTask := map[string]struct {
		calls, input, models int
	}{}
	for _, x := range body.ByTask {
		byTask[x.Name] = struct{ calls, input, models int }{x.Calls, x.Input, x.Models}
	}
	if got := byTask[taskA]; got.calls != 3 || got.input != 3300 || got.models != 1 {
		t.Fatalf("任务甲聚合不符: %+v", got)
	}
	if got := byTask[taskB]; got.calls != 2 || got.input != 4700 || got.models != 1 {
		t.Fatalf("任务乙聚合不符: %+v", got)
	}
	// 按模型:glm-4.7 三次、gpt-4o 两次。
	byModel := map[string]int{}
	for _, m := range body.ByModel {
		byModel[m.Name] = m.Calls
	}
	if byModel["glm-4.7"] != 3 || byModel["gpt-4o"] != 2 {
		t.Fatalf("按模型聚合不符: %+v", byModel)
	}
	// 按角色:swarm 3 / overmind 1 / shepherd 1。
	byAgent := map[string]int{}
	for _, a := range body.ByAgent {
		byAgent[a.Name] = a.Calls
	}
	if byAgent["swarm"] != 3 || byAgent["overmind"] != 1 || byAgent["shepherd"] != 1 {
		t.Fatalf("按角色聚合不符: %+v", byAgent)
	}
	// 时间维度:分桶求和 = 总量(曲线不丢数)。
	var sIn, sOut int
	for _, b := range body.Series {
		sIn += b.Input
		sOut += b.Output
	}
	if sIn != sumIn || sOut != sumOut {
		t.Fatalf("时间分桶与总量不符: %d/%d", sIn, sOut)
	}
	_ = taskB
}

// TestObsToolsFilters 工具流水:全量计数、任务 / 工具 / 状态过滤、下钻字段。
func TestObsToolsFilters(t *testing.T) {
	h, _, taskA, taskB := obsSetup(t)

	type row struct {
		TaskID  string `json:"task_id"`
		Agent   string `json:"agent"`
		StepID  string `json:"step_id"`
		Tool    string `json:"tool"`
		IsError bool   `json:"is_error"`
		At      string `json:"at"`
	}
	var body struct {
		Rows  []row `json:"rows"`
		Tools []struct {
			Name   string `json:"name"`
			Count  int    `json:"count"`
			Errors int    `json:"errors"`
		} `json:"tools"`
		Counts struct {
			Total int `json:"total"`
			Ok    int `json:"ok"`
			Error int `json:"error"`
			Shown int `json:"shown"`
		} `json:"counts"`
	}
	get(t, h, "/api/obs/tools", http.StatusOK, &body)
	if body.Counts.Total != 4 || body.Counts.Ok != 3 || body.Counts.Error != 1 {
		t.Fatalf("全量计数不符: %+v", body.Counts)
	}
	if len(body.Rows) != 4 {
		t.Fatalf("应有 4 行流水: %d", len(body.Rows))
	}
	// 新到旧:牧者 @45s 应居首。
	if body.Rows[0].Agent != "shepherd" || body.Rows[0].Tool != "task_snapshot" {
		t.Fatalf("流水应按时间倒序: %+v", body.Rows[0])
	}
	toolCount := map[string]int{}
	for _, tc := range body.Tools {
		toolCount[tc.Name] = tc.Count
	}
	if toolCount["http_get"] != 2 || toolCount["bash"] != 1 || toolCount["task_snapshot"] != 1 {
		t.Fatalf("工具计数不符: %+v", body.Tools)
	}

	// 按任务过滤。
	get(t, h, "/api/obs/tools?task="+taskA, http.StatusOK, &body)
	if body.Counts.Total != 2 || len(body.Rows) != 2 {
		t.Fatalf("任务甲应 2 行: %+v", body.Counts)
	}
	for _, x := range body.Rows {
		if x.TaskID != taskA {
			t.Fatalf("过滤后混入他任务: %+v", x)
		}
	}

	// 按工具过滤。
	get(t, h, "/api/obs/tools?tool=http_get", http.StatusOK, &body)
	if body.Counts.Total != 2 || len(body.Rows) != 2 {
		t.Fatalf("http_get 应 2 行: %+v", body.Counts)
	}

	// 按状态过滤(失败)。
	get(t, h, "/api/obs/tools?status=error", http.StatusOK, &body)
	if body.Counts.Total != 1 || len(body.Rows) != 1 {
		t.Fatalf("失败应 1 行: %+v", body.Counts)
	}
	if !body.Rows[0].IsError || body.Rows[0].Tool != "bash" {
		t.Fatalf("失败行不符: %+v", body.Rows[0])
	}

	// 组合过滤:任务乙 + 成功(http_get 与牧者 task_snapshot 两条均成功)。
	get(t, h, "/api/obs/tools?task="+taskB+"&status=ok", http.StatusOK, &body)
	if body.Counts.Total != 2 || len(body.Rows) != 2 {
		t.Fatalf("乙+成功应 2 行: %+v", body.Counts)
	}
	if body.Rows[0].Tool != "task_snapshot" || body.Rows[1].Tool != "http_get" {
		t.Fatalf("乙+成功行序不符: %+v", body.Rows)
	}
	// 组合过滤:任务甲 + 失败(仅 bash)。
	get(t, h, "/api/obs/tools?task="+taskA+"&status=error", http.StatusOK, &body)
	if body.Counts.Total != 1 || body.Rows[0].Tool != "bash" || !body.Rows[0].IsError {
		t.Fatalf("甲+失败应 1 行 bash: %+v %+v", body.Counts, body.Rows)
	}
}

// TestObsThroughputMetrics 吞吐指标:等待时长与队列深度的重放正确性
// (跨任务归并与单任务过滤两口径)。
func TestObsThroughputMetrics(t *testing.T) {
	h, _, taskA, taskB := obsSetup(t)

	var body struct {
		Waits []struct {
			TaskID  string  `json:"task_id"`
			StepID  string  `json:"step_id"`
			Seconds float64 `json:"seconds"`
		} `json:"waits"`
		Summary struct {
			Claims     int     `json:"claims"`
			AvgSeconds float64 `json:"avg_seconds"`
			MaxSeconds float64 `json:"max_seconds"`
			WaitingNow int     `json:"waiting_now"`
			MaxDepth   int     `json:"max_depth"`
		} `json:"summary"`
		Queue []struct {
			T     string `json:"t"`
			Depth int    `json:"depth"`
		} `json:"queue"`
	}
	get(t, h, "/api/obs/throughput", http.StatusOK, &body)
	if body.Summary.Claims != 3 {
		t.Fatalf("应 3 次领取: %+v", body.Summary)
	}
	got := map[string]float64{}
	for _, w := range body.Waits {
		got[w.StepID+"@"+w.TaskID] = w.Seconds
	}
	if got["s1@"+taskA] != 8 || got["s2@"+taskA] != 7 || got["s1@"+taskB] != 29 {
		t.Fatalf("等待明细不符: %+v", got)
	}
	// 甲 s1=8s、甲 s2=7s、乙 s1=29s(甲乙前缀不同,按任务区分)。
	var sum float64
	for _, s := range []float64{8, 7, 29} {
		sum += s
	}
	if body.Summary.MaxSeconds != 29 {
		t.Fatalf("最长等待应 29s: %+v", body.Summary)
	}
	if d := body.Summary.AvgSeconds - sum/3; d > 0.001 || d < -0.001 {
		t.Fatalf("平均等待不符: %v", body.Summary.AvgSeconds)
	}
	if body.Summary.WaitingNow != 1 || body.Summary.MaxDepth != 4 {
		t.Fatalf("队列深度不符: %+v", body.Summary)
	}
	if len(got) != 3 {
		t.Fatalf("等待明细应 3 条: %+v", got)
	}
	// 队列深度阶跃:1,2,3,4,3,2,1。
	wantDepth := []int{1, 2, 3, 4, 3, 2, 1}
	if len(body.Queue) != len(wantDepth) {
		t.Fatalf("深度变化点应 %d 个: %+v", len(wantDepth), body.Queue)
	}
	for i, p := range body.Queue {
		if p.Depth != wantDepth[i] {
			t.Fatalf("深度序列不符 @%d: got %d want %d (%+v)", i, p.Depth, wantDepth[i], body.Queue)
		}
	}

	// 单任务口径:甲 = 2 次领取、峰值 3、终值 1(候补意图在队)。
	get(t, h, "/api/obs/throughput?task="+taskA, http.StatusOK, &body)
	if body.Summary.Claims != 2 || body.Summary.MaxDepth != 3 || body.Summary.WaitingNow != 1 {
		t.Fatalf("任务甲口径不符: %+v", body.Summary)
	}
	if len(body.Waits) != 2 {
		t.Fatalf("甲等待明细应 2 条: %+v", body.Waits)
	}
}
