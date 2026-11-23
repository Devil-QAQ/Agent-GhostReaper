package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// p509Server 测试装配(与 p507 同款)。
func p509Server(t *testing.T) (http.Handler, *rings.Store) {
	t.Helper()
	srv, h, _ := newShepherdTestServer(t)
	srv.Shepherd = nil
	return h, srv.Store
}

func p509Seed(t *testing.T, h http.Handler, store *rings.Store) (task string) {
	t.Helper()
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["摸清边界"],
		"title":"P5-09 全景图","assets":[{"class":"domain","title":"seed.example.com"}]}`,
		http.StatusOK, &created)
	task = created["id"]

	aggIDs := func() []string {
		out := []string{"s1"}
		for i := 0; i < 60; i++ {
			out = append(out, fmt.Sprintf("s1%02d", i))
		}
		return out
	}()
	var evs []rings.Event
	evs = append(evs,
		// 目标横幅:已达成一条(建任务种子目标为进行中)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g-1", NodeType: rings.NodeGoal, Title: "摸清边界", Status: "achieved"}},
		// 资产簇一:根 + 子资产(sub_of 子→父,子资产带出生血缘)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-root", NodeType: rings.NodeAsset, Title: "a.example.com", AssetClass: "domain"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "a-sub", NodeType: rings.NodeAsset, Title: "sub.a.example.com", AssetClass: "subdomain",
			Detail: map[string]any{
				"source": "discovered", "step_id": "s1", "discovered_fact": "f-1",
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeSubOf, FromNode: "a-sub", ToNode: "a-root"}},
		// 资产簇二。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-two", NodeType: rings.NodeAsset, Title: "web-02", AssetClass: "host"}},
	)
	// 意图:簇一 61 条(>50 → 聚合)。
	for _, sid := range aggIDs {
		title := "探测主域"
		if sid != "s1" {
			title = "簇一意图 " + sid
		}
		evs = append(evs,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: sid, NodeType: rings.NodeStep, Title: title, Status: "done"}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeCovers, FromNode: sid, ToNode: "a-root"}})
	}
	// 簇二意图 s2/s3 + 前置依赖 + 无锚意图 s9 + 补考步骤(不入图)。
	evs = append(evs,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "测 web-02 后台", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "web-02 深挖", Status: "done"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s2", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s3", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDependsOn, FromNode: "s3", ToNode: "s2"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s9", NodeType: rings.NodeStep, Title: "无锚意图", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "arbiter", Payload: rings.NodeAdded{
			NodeID: "s-retake", NodeType: rings.NodeStep, Title: "补做证据", Status: "dispatched",
			Detail: map[string]any{"kind": "retake"}}},
	)
	evs = append(evs,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "子域解析到 10.0.0.2", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f-1", ToNode: "a-root"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-2", NodeType: rings.NodeFact, Title: "疑似旁站", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-2"}},
	)
	// 漏洞:detail 血缘已确认(挂 s2)/边血缘已修复(挂 s3)/
	// 边缘事实血缘证据不足(组合声明,经产出步骤归位 s1)/候选与
	// 被推翻(不上图)/无血缘悬挂。
	evs = append(evs,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "后台弱口令", Status: rings.TestClaimCandidate,
			Detail: map[string]any{"step_id": "s2", "unit_id": "u2"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-1", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-2", NodeType: rings.NodeTestClaim, Title: "目录穿越", Status: rings.TestClaimFixed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-2", ToNode: "s3"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-3", NodeType: rings.NodeTestClaim, Title: "组合链拿数据", Status: rings.TestClaimInsufficient}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-3", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-4", NodeType: rings.NodeTestClaim, Title: "候选态", Status: rings.TestClaimCandidate}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-5", NodeType: rings.NodeTestClaim, Title: "被推翻", Status: rings.TestClaimRefuted}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u9", Payload: rings.NodeAdded{
			NodeID: "tc-6", NodeType: rings.NodeTestClaim, Title: "老任务遗留", Status: rings.TestClaimConfirmed}},
	)
	appendEvents(t, store, task, evs...)

	// 明确来源独立记账；其余只覆盖资产的旧意图不据此推造来源线。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s1", ToNode: "a-root"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s2", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s3", ToNode: "s2"}},
	)

	// 单位会话:s2 一段(session_started + 收场 done)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{
			StepID: "s2", UnitID: "u2", TranscriptID: "swarm-u2a", Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{
			StepID: "s2", UnitID: "u2", TranscriptID: "swarm-u2a", Type: rings.SwarmRecSessionEnd,
			Note: "status=done turns=3"}},
	)
	return task
}

func p509View(t *testing.T, h http.Handler, task string) *panoSkeletonView {
	t.Helper()
	var v panoSkeletonView
	get(t, h, "/api/tasks/"+task+"/pview/skeleton", 200, &v)
	return &v
}

func TestP509SkeletonClass(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	v := p509View(t, h, task)
	raw, _ := json.Marshal(v)
	for _, id := range []string{"h-1", "s-retake", "tc-4", "tc-5"} {
		if strings.Contains(string(raw), `"id":"`+id+`"`) {
			t.Fatalf("不可见节点 %s", id)
		}
	}
	ids := map[string]panoNodeRow{}
	assets, steps, claims := 0, 0, 0
	for _, n := range v.Nodes {
		ids[n.ID] = n
		switch n.T {
		case "asset":
			assets++
		case "step":
			steps++
		case "claim":
			claims++
		default:
			t.Fatalf("独立绘制了非正式图类型 %s", n.T)
		}
	}
	if assets != 3 || steps != 64 || claims != 4 {
		t.Fatalf("节点数: %d/%d/%d", assets, steps, claims)
	}
	if _, ok := ids["a-sub"]; ok {
		t.Fatal("非根资产另画了顶部卡")
	}
	if len(v.Aliases["a-sub"]) != 1 || v.Aliases["a-sub"][0] != "a-root" {
		t.Fatal("子资产失去正式归属")
	}
	if len(ids["s1"].Results) != 1 || ids["s1"].Results[0].ID != "f-1" {
		t.Fatalf("意图事实合并错误: %v", ids["s1"].Results)
	}
	if len(v.Aliases["f-1"]) != 1 || v.Aliases["f-1"][0] != "s1" {
		t.Fatal("事实搜索定位归属缺失")
	}
	for _, n := range v.Nodes {
		if n.T == "claim" && n.Y <= ids["s2"].Y {
			t.Fatal("漏洞未在意图下方")
		}
		for _, other := range v.Nodes {
			if n.ID != other.ID && n.Y == other.Y && n.X > other.X && n.X-other.X < panoCardW+16 {
				t.Fatalf("卡片重叠 %s/%s", n.ID, other.ID)
			}
		}
	}
	has := func(a, b, rel string) bool {
		for _, e := range v.Edges {
			if e.From == a && e.To == b && e.Rel == rel {
				return true
			}
		}
		return false
	}
	if !has("s1", "tc-3", "derived_from") || !has("s2", "tc-1", "produces") || !has("s2", "s3", "sourced_from") {
		t.Fatal("事实归并/既有真实血缘/明确来源关系缺失")
	}
	for _, e := range v.Edges {
		if e.Rel == "depends_on" {
			t.Fatal("将执行前置画成了来源线")
		}
		if e.Rel == "covers" && (e.From != "a-root" || !strings.HasPrefix(e.To, "s1") || len(e.Refs) != 1 || e.Refs[0] != "a-root") {
			t.Fatal("入口线没有实际资产派发依据，或重复覆盖变成了新入口")
		}
	}
	if len(v.Goals) != 2 || v.Bounds[2] <= 0 || v.Bounds[3] <= 0 {
		t.Fatal("横幅或边界缺失")
	}
}

func TestP509MixedOriginsAndDetails(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s-next", NodeType: rings.NodeStep, Title: "跨资产组合", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-next", ToNode: "tc-1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-next", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "s-next", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f-extra", NodeType: rings.NodeFact, Title: "第二条实际结果", Status: "confirmed", Detail: map[string]any{"evidence": "完整证据\n第二行"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-extra"}},
	)
	v := p509View(t, h, task)
	parents := map[string]bool{}
	for _, e := range v.Edges {
		if e.To == "s-next" && e.Rel == "sourced_from" {
			parents[e.From] = true
		}
	}
	for _, id := range []string{"tc-1", "s1", "a-two"} {
		if !parents[id] {
			t.Fatalf("真实混合来源丢失 %s", id)
		}
	}
	var detail map[string]any
	get(t, h, "/api/tasks/"+task+"/pview/details/s1", 200, &detail)
	intent := detail["intent"].(map[string]any)
	results := detail["results"].([]any)
	if intent["id"] != "s1" || len(results) != 3 {
		t.Fatalf("意图详情应有全部实际结果(含未确认及新增): %v/%d", intent["id"], len(results))
	}
	evidence := false
	for _, r := range results {
		m := r.(map[string]any)
		if m["id"] == "f-extra" && m["detail"].(map[string]any)["evidence"] == "完整证据\n第二行" {
			evidence = true
		}
	}
	if !evidence {
		t.Fatal("详情截断或遗漏事实证据")
	}
	for _, id := range []string{"s-next", "f-1", "a-root", "tc-1"} {
		var links panoLinksView
		get(t, h, "/api/tasks/"+task+"/pview/links/"+id, 200, &links)
		if links.Node.ID == "" || len(links.IDs) == 0 {
			t.Fatalf("关联整体为空 %s", id)
		}
		seen := map[string]bool{}
		for _, nid := range links.IDs {
			seen[nid] = true
		}
		for _, e := range links.Edges {
			if !seen[e.From] || !seen[e.To] {
				t.Fatal("关联边端点未纳入整体")
			}
		}
	}
	var links panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/s-next", 200, &links)
	set := map[string]bool{}
	for _, id := range links.IDs {
		set[id] = true
	}
	for _, id := range []string{"s-next", "s1", "tc-1", "s2", "a-root", "a-two"} {
		if !set[id] {
			t.Fatalf("多层来源缺失 %s", id)
		}
	}
}

func TestP509SearchAndErrors(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	for _, q := range []string{"s100", "web-02", "无锚", "f-1", "子域解析", "s1"} {
		var hits []panoSearchRow
		get(t, h, "/api/tasks/"+task+"/pview/search?q="+q, 200, &hits)
		if len(hits) == 0 {
			t.Fatalf("搜索未命中 %s", q)
		}
	}
	for _, q := range []string{"补做", "候选态", ""} {
		var hits []panoSearchRow
		get(t, h, "/api/tasks/"+task+"/pview/search?q="+q, 200, &hits)
		if len(hits) != 0 {
			t.Fatalf("搜索错误命中 %s: %v", q, hits)
		}
	}
	for _, suffix := range []string{"skeleton", "search?q=x", "links/s1", "details/s1"} {
		get(t, h, "/api/tasks/t-none/pview/"+suffix, 404, nil)
	}
	get(t, h, "/api/tasks/"+task+"/pview/links/n-none", 404, nil)
	get(t, h, "/api/tasks/"+task+"/pview/links/g-1", 400, nil)
	get(t, h, "/api/tasks/"+task+"/pview/details/a-root", 400, nil)
}

func TestP509EmptyAndCycleLayout(t *testing.T) {
	h, store := p509Server(t)
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","title":"空图","goals":["目标"],"assets":[{"class":"domain","title":"empty.example.test"}]}`, 200, &created)
	assets, err := store.NodesByType(t.Context(), created["id"], rings.NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		appendEvents(t, store, created["id"], rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: asset.ID, Status: rings.AssetStatusRetired}})
	}
	v := p509View(t, h, created["id"])
	if len(v.Nodes) != 0 || len(v.Edges) != 0 {
		t.Fatal("空图产生合成节点")
	}
	graph := &panoSkeletonView{Nodes: []panoNodeRow{{ID: "a", T: "asset"}, {ID: "s1", T: "step", Root: "a"}, {ID: "s2", T: "step", Root: "a"}}, Edges: []panoEdgeRow{{From: "s1", To: "s2"}, {From: "s2", To: "s1"}}}
	layoutPanorama(graph)
	if graph.Bounds[2] <= 0 {
		t.Fatal("历史异常环导致布局失败")
	}
}

func TestP509LivePviewTopic(t *testing.T) {
	srv, ts, _ := newLiveTestServer(t)
	createLiveTask(t, srv, "t-p5")
	c := dialWS(t, ts.URL)
	defer c.close()
	c.expectMsg(2*time.Second, map[string]any{"t": "ready"})
	// collect 收齐 want 全部主题;期间出现 forbid 即败(负向断言:
	// 一次落账的全部帧在同一批冲刷,顺序不保证但不会迟到下一批)。
	collect := func(want map[string]bool, forbid string) {
		got := map[string]bool{}
		for len(got) < len(want) {
			c.drainUntil(2*time.Second, func(m map[string]any) bool {
				k, _ := m["k"].(string)
				if k == forbid {
					c.t.Fatalf("不应标脏 %s(骨架无该变化)", forbid)
				}
				if k != "" && want[k] && !got[k] {
					got[k] = true
					return true
				}
				return false
			})
		}
	}

	// 资产新增:pview + tasks。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "a-1", NodeType: rings.NodeAsset, Title: "a.example.com"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")

	// 事实新增(不进骨架):只 tasks,无 pview。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "u1",
		Payload: rings.NodeAdded{NodeID: "f-1", NodeType: rings.NodeFact, Title: "无关"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")

	// 意图新增:pview。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: "s-1", NodeType: rings.NodeStep, Title: "意图"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")

	// 步骤执行态迁移(running→done):不标 pview。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
		Payload: rings.NodeStateChanged{NodeID: "s-1", Status: "running"},
	})
	collect(map[string]bool{"tasks": true}, "pview:t-p5")

	// 漏洞显示键迁移(确认):pview。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "u1",
		Payload: rings.NodeAdded{NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "发现", Status: "candidate"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: "confirmed"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true, "claims:t-p5": true}, "")

	// 定稿批(级别):pview(claims 同批)。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "arbiter",
		Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high"},
	})
	collect(map[string]bool{"pview:t-p5": true, "claims:t-p5": true}, "")

	// 目标达成(横幅进度):pview。
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "g-9", NodeType: rings.NodeGoal, Title: "目标"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")
	commitEvents(t, srv, "t-p5", rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "g-9", Status: "achieved"},
	})
	collect(map[string]bool{"pview:t-p5": true, "tasks": true}, "")
}
