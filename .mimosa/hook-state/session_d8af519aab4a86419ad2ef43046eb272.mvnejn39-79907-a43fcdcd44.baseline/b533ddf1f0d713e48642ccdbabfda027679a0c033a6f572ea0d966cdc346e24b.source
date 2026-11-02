package rings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAppend(t *testing.T, s *Store, taskID string, events ...Event) []int64 {
	t.Helper()
	revs, err := s.Append(context.Background(), taskID, events...)
	if err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	if len(revs) != len(events) {
		t.Fatalf("返回 revision 数 %d 与事件数 %d 不符", len(revs), len(events))
	}
	return revs
}

func na(nt NodeType, id, title, status string, class AssetClass, detail map[string]any) Event {
	return Event{Kind: EventNodeAdded, Actor: "test",
		Payload: NodeAdded{NodeID: id, NodeType: nt, Title: title, Status: status, AssetClass: class, Detail: detail}}
}

func ea(et EdgeType, from, to string) Event {
	return Event{Kind: EventEdgeAdded, Actor: "test", Payload: EdgeAdded{EdgeType: et, FromNode: from, ToNode: to}}
}

func st(id, status string) Event {
	return Event{Kind: EventNodeStateChanged, Actor: "test", Payload: NodeStateChanged{NodeID: id, Status: status}}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("统计 %s 行数失败: %v", table, err)
	}
	return n
}

// --- 基建:WAL 与 schema 版本 ---

func TestOpenSetsWALAndSchemaVersion(t *testing.T) {
	s := openTestStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, 期望 wal", mode)
	}
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("查询 user_version 失败: %v", err)
	}
	if v != len(migrations) {
		t.Fatalf("user_version = %d, 期望 %d", v, len(migrations))
	}
}

// --- 完成标准 1:追加 N 事件后投影查询结果正确 ---

func TestAppendAndDualGraphProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-scan"

	// 种子资产:单位→域名→子域/主机→服务,层级全部经 sub_of 类型化边
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
			NodeID: "g1", NodeType: NodeGoal, Title: "完成对 example.com 的授权测试", Status: "active"}},
		na(NodeAsset, "org1", "Example 单位", "active", AssetOrganization, nil),      // r2
		na(NodeAsset, "dom1", "example.com", "active", AssetDomain, nil),           // r3
		ea(EdgeSubOf, "dom1", "org1"),                                              // r4
		na(NodeAsset, "www1", "www.example.com", "active", AssetSubdomain, nil),    // r5
		ea(EdgeSubOf, "www1", "dom1"),                                              // r6
		na(NodeAsset, "host1", "10.0.0.5", "active", AssetHost, nil),               // r7
		ea(EdgeSubOf, "host1", "dom1"),                                             // r8
		na(NodeAsset, "svc1", "http://10.0.0.5:8080", "active", AssetService, nil), // r9
		ea(EdgeSubOf, "svc1", "host1"),                                             // r10
	)
	// 意图派发:Step 覆盖子域
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "s1", NodeType: NodeStep, Title: "探测 www.example.com 的解析与服务",
			Status: "pending", Detail: map[string]any{"intent_class": "只读探测"}}}, // r11
		ea(EdgeCovers, "s1", "www1"), // r12
	)
	// 虫群执行回流:状态推进、事实产出、直觉假设
	mustAppend(t, s, task,
		Event{Kind: EventNodeStateChanged, Actor: "swarm:u1", Payload: NodeStateChanged{NodeID: "s1", Status: "done"}}, // r13
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f1", NodeType: NodeFact, Title: "www.example.com 解析到 10.0.0.5", Status: "active",
			Detail: map[string]any{"evidence": map[string]any{"mirror_ref": "sha256:ab12", "tool": "dns_resolve"}}}}, // r14
		ea(EdgeProduces, "s1", "f1"), // r15
		ea(EdgeAbout, "f1", "www1"),  // r16
		na(NodeHypothesis, "h1", "10.0.0.5:8080 疑似旧版管理后台", "candidate", "", nil), // r17
		ea(EdgeAbout, "h1", "svc1"), // r18
	)
	// 主宰收割:候选漏洞声明 + 反证意图
	mustAppend(t, s, task,
		na(NodeTestClaim, "tc1", "www.example.com 反射型 XSS", "candidate", "", nil), // r19
		ea(EdgeDerivedFrom, "tc1", "f1"),                                          // r20
		na(NodeStep, "s2", "对 tc1 执行单轮反证", "pending", "", nil),                    // r21
		ea(EdgeDependsOn, "s2", "s1"),                                             // r22
		ea(EdgeCovers, "s2", "www1"),                                              // r23
	)

	// 事件流:23 条,revision 1..23 连续,信封字段可回读
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(evs) != 23 {
		t.Fatalf("事件数 = %d, 期望 23", len(evs))
	}
	for i, e := range evs {
		if e.Revision != int64(i+1) {
			t.Fatalf("事件 %d 的 revision = %d, 期望 %d", i, e.Revision, i+1)
		}
		if e.TaskID != task {
			t.Fatalf("事件 %d 的 task_id = %q", i, e.TaskID)
		}
	}
	if evs[0].Actor != "creator" || evs[10].Actor != "overmind" || evs[12].Actor != "swarm:u1" {
		t.Fatalf("actor 归属不符: %q / %q / %q", evs[0].Actor, evs[10].Actor, evs[12].Actor)
	}
	var www NodeAdded
	if err := json.Unmarshal(evs[4].Payload, &www); err != nil {
		t.Fatalf("解码 r5 负载失败: %v", err)
	}
	if www.NodeID != "www1" || www.NodeType != NodeAsset || www.AssetClass != AssetSubdomain {
		t.Fatalf("r5 负载回读不符: %+v", www)
	}
	rev, err := s.CurrentRevision(ctx, task)
	if err != nil || rev != 23 {
		t.Fatalf("CurrentRevision = (%d, %v), 期望 23", rev, err)
	}

	// 资产图投影
	ag, err := s.AssetGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询资产图失败: %v", err)
	}
	asset := func(id string) AssetNodeView {
		t.Helper()
		for _, a := range ag.Assets {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("资产 %s 不在资产图中", id)
		return AssetNodeView{}
	}
	if len(ag.Assets) != 5 {
		t.Fatalf("资产数 = %d, 期望 5", len(ag.Assets))
	}
	if a := asset("www1"); !a.Covered || a.FactCount != 1 || a.HypothesisCount != 0 ||
		a.AssetClass != AssetSubdomain || a.Title != "www.example.com" || a.CreatedRev != 5 || a.UpdatedRev != 5 {
		t.Fatalf("www1 投影不符: %+v", a)
	}
	if a := asset("svc1"); a.Covered || a.FactCount != 0 || a.HypothesisCount != 1 {
		t.Fatalf("svc1 投影不符: %+v", a)
	}
	for _, id := range []string{"org1", "dom1", "host1"} {
		if a := asset(id); a.Covered || a.FactCount != 0 || a.HypothesisCount != 0 {
			t.Fatalf("资产 %s 不应有覆盖或证据计数: %+v", id, a)
		}
	}
	wantHierarchy := []EdgeView{
		{Type: EdgeSubOf, From: "dom1", To: "org1", CreatedRev: 4},
		{Type: EdgeSubOf, From: "www1", To: "dom1", CreatedRev: 6},
		{Type: EdgeSubOf, From: "host1", To: "dom1", CreatedRev: 8},
		{Type: EdgeSubOf, From: "svc1", To: "host1", CreatedRev: 10},
	}
	if !reflect.DeepEqual(ag.Hierarchy, wantHierarchy) {
		t.Fatalf("层级边不符:\n得到 %+v\n期望 %+v", ag.Hierarchy, wantHierarchy)
	}

	// 进度图投影
	nv := func(id string, nt NodeType, title, status string, created, updated int64) NodeView {
		return NodeView{ID: id, Type: nt, Title: title, Status: status, CreatedRev: created, UpdatedRev: updated}
	}
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	wantGoals := []NodeView{nv("g1", NodeGoal, "完成对 example.com 的授权测试", "active", 1, 1)}
	wantSteps := []NodeView{
		nv("s1", NodeStep, "探测 www.example.com 的解析与服务", "done", 11, 13),
		nv("s2", NodeStep, "对 tc1 执行单轮反证", "pending", 21, 21),
	}
	wantFacts := []NodeView{nv("f1", NodeFact, "www.example.com 解析到 10.0.0.5", "active", 14, 14)}
	wantClaims := []NodeView{nv("tc1", NodeTestClaim, "www.example.com 反射型 XSS", "candidate", 19, 19)}
	wantLineage := []EdgeView{
		{Type: EdgeProduces, From: "s1", To: "f1", CreatedRev: 15},
		{Type: EdgeDerivedFrom, From: "tc1", To: "f1", CreatedRev: 20},
	}
	if !reflect.DeepEqual(pg.Goals, wantGoals) || !reflect.DeepEqual(pg.Steps, wantSteps) ||
		!reflect.DeepEqual(pg.Facts, wantFacts) || !reflect.DeepEqual(pg.TestClaims, wantClaims) {
		t.Fatalf("进度图节点不符:\ngoals  %+v\nsteps  %+v\nfacts  %+v\nclaims  %+v",
			pg.Goals, pg.Steps, pg.Facts, pg.TestClaims)
	}
	if !reflect.DeepEqual(pg.Lineage, wantLineage) {
		t.Fatalf("血缘边不符:\n得到 %+v\n期望 %+v", pg.Lineage, wantLineage)
	}

	// 追加状态推进事件后投影随之变化(仲裁者确认 tc1)
	mustAppend(t, s, task,
		Event{Kind: EventNodeStateChanged, Actor: "arbiter", Payload: NodeStateChanged{NodeID: "tc1", Status: "confirmed"}}) // r24
	pg2, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("再次查询进度图失败: %v", err)
	}
	if pg2.TestClaims[0].Status != "confirmed" || pg2.TestClaims[0].UpdatedRev != 24 {
		t.Fatalf("tc1 状态推进未反映: %+v", pg2.TestClaims[0])
	}

	// 投影=事件流确定性重放:重建前后双图与事件流完全一致
	agBefore, _ := s.AssetGraph(ctx, task)
	pgBefore, _ := s.ProgressGraph(ctx, task)
	evsBefore, _ := s.Events(ctx, task, 0, 0)
	nodesBefore, edgesBefore := countRows(t, s, "nodes"), countRows(t, s, "edges")
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	agAfter, _ := s.AssetGraph(ctx, task)
	pgAfter, _ := s.ProgressGraph(ctx, task)
	evsAfter, _ := s.Events(ctx, task, 0, 0)
	if !reflect.DeepEqual(agBefore, agAfter) || !reflect.DeepEqual(pgBefore, pgAfter) {
		t.Fatalf("重建前后投影不一致:\n资产图 %+v → %+v\n进度图 %+v → %+v", agBefore, agAfter, pgBefore, pgAfter)
	}
	if !reflect.DeepEqual(evsBefore, evsAfter) {
		t.Fatal("重建不得改动事件流")
	}
	if n := countRows(t, s, "nodes"); n != nodesBefore {
		t.Fatalf("重建后 nodes = %d, 期望 %d", n, nodesBefore)
	}
	if n := countRows(t, s, "edges"); n != edgesBefore {
		t.Fatalf("重建后 edges = %d, 期望 %d", n, edgesBefore)
	}
	if nodesBefore != 11 || edgesBefore != 11 {
		t.Fatalf("投影规模: nodes=%d edges=%d, 期望 11/11", nodesBefore, edgesBefore)
	}
}

// --- 完成标准 2:revision 重复被拒 ---

func TestRevisionDuplicateRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-rev"
	mustAppend(t, s, task, na(NodeAsset, "a1", "a.example.com", "active", AssetDomain, nil)) // r1
	mustAppend(t, s, task, na(NodeAsset, "a2", "b.example.com", "active", AssetDomain, nil)) // r2
	// 显式指定跳到 r5(单调递增允许留空隙)
	mustAppend(t, s, task, Event{Revision: 5, Kind: EventNodeAdded, Actor: "test",
		Payload: NodeAdded{NodeID: "a3", NodeType: NodeAsset, Title: "c.example.com", AssetClass: AssetDomain}})

	// 已占用的 revision 被拒
	_, err := s.Append(ctx, task, Event{Revision: 1, Kind: EventNodeAdded, Actor: "test",
		Payload: NodeAdded{NodeID: "a4", NodeType: NodeAsset, AssetClass: AssetDomain}})
	if !errors.Is(err, ErrRevisionNotIncreasing) {
		t.Fatalf("重复 revision 期望 ErrRevisionNotIncreasing, 得到 %v", err)
	}
	// 回退到空闲槽位同样被拒(单调性,UNIQUE 约束本身拦不住这种)
	_, err = s.Append(ctx, task, Event{Revision: 3, Kind: EventNodeAdded, Actor: "test",
		Payload: NodeAdded{NodeID: "a4", NodeType: NodeAsset, AssetClass: AssetDomain}})
	if !errors.Is(err, ErrRevisionNotIncreasing) {
		t.Fatalf("回退 revision 期望 ErrRevisionNotIncreasing, 得到 %v", err)
	}
	// 同批内重复:整批回滚,不留部分写入
	_, err = s.Append(ctx, task,
		Event{Revision: 7, Kind: EventNodeAdded, Actor: "test",
			Payload: NodeAdded{NodeID: "a4", NodeType: NodeAsset, AssetClass: AssetDomain}},
		Event{Revision: 7, Kind: EventNodeAdded, Actor: "test",
			Payload: NodeAdded{NodeID: "a5", NodeType: NodeAsset, AssetClass: AssetDomain}})
	if !errors.Is(err, ErrRevisionNotIncreasing) {
		t.Fatalf("同批重复 revision 期望 ErrRevisionNotIncreasing, 得到 %v", err)
	}
	// 物理兜底:绕过 Store 直插已占用的 (task_id, revision),UNIQUE 约束拒绝
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(
		`INSERT INTO events (task_id, revision, kind, actor, payload, occurred_at, recorded_at)
		 VALUES (?, 1, ?, ?, '{}', ?, ?)`, task, string(EventNodeAdded), "intruder", now, now); err == nil {
		t.Fatal("直插重复 revision 期望被 UNIQUE 约束拒绝, 实际成功")
	}

	// 失败不留痕:仍是 3 事件、3 节点
	if evs, _ := s.Events(ctx, task, 0, 0); len(evs) != 3 {
		t.Fatalf("失败尝试后事件数 = %d, 期望 3", len(evs))
	}
	if n := countRows(t, s, "nodes"); n != 3 {
		t.Fatalf("失败尝试后节点数 = %d, 期望 3", n)
	}
	// 自动分配从当前最大值继续
	revs := mustAppend(t, s, task, na(NodeAsset, "a6", "f.example.com", "active", AssetDomain, nil))
	if revs[0] != 6 {
		t.Fatalf("自动分配 revision = %d, 期望 6", revs[0])
	}
}

// --- 完成标准 3:task_id 隔离生效 ---

func TestTaskIsolation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const taskA, taskB = "t-alpha", "t-beta"

	// 两任务使用完全相同的节点 id,互不冲突,各自 revision 独立
	for _, task := range []string{taskA, taskB} {
		title := task + " 的资产"
		mustAppend(t, s, task,
			na(NodeAsset, "www1", title, "active", AssetSubdomain, nil), // r1
			na(NodeStep, "s1", task+" 的意图", "pending", "", nil),         // r2
			ea(EdgeCovers, "s1", "www1"),                                // r3
			na(NodeFact, "f1", task+" 的事实", "active", "", nil),          // r4
			ea(EdgeAbout, "f1", "www1"),                                 // r5
		)
	}

	// 各任务只见自己的数据
	for _, task := range []string{taskA, taskB} {
		ag, err := s.AssetGraph(ctx, task)
		if err != nil {
			t.Fatalf("查询 %s 资产图失败: %v", task, err)
		}
		if len(ag.Assets) != 1 || ag.Assets[0].Title != task+" 的资产" || !ag.Assets[0].Covered || ag.Assets[0].FactCount != 1 {
			t.Fatalf("%s 资产图不符: %+v", task, ag.Assets)
		}
		if evs, _ := s.Events(ctx, task, 0, 0); len(evs) != 5 {
			t.Fatalf("%s 事件数 = %d, 期望 5", task, len(evs))
		}
		if rev, _ := s.CurrentRevision(ctx, task); rev != 5 {
			t.Fatalf("%s 当前 revision = %d, 期望 5", task, rev)
		}
	}

	// 端点查找同样是任务内的:B 任务的节点对 A 任务不可见
	_, err := s.Append(ctx, taskA, ea(EdgeAbout, "f1", "nope-b"))
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("跨任务端点期望 ErrNodeNotFound, 得到 %v", err)
	}
	// 两任务各自创建同名节点 www1 均已成功(见上方播种);再向 B 追加新节点,A 不被污染
	mustAppend(t, s, taskB, na(NodeAsset, "extra1", "B 的追加资产", "active", AssetHost, nil))
	agA, _ := s.AssetGraph(ctx, taskA)
	if len(agA.Assets) != 1 || agA.Assets[0].Title != taskA+" 的资产" {
		t.Fatalf("任务 B 追加后 A 的资产图被污染: %+v", agA.Assets)
	}
	if rev, _ := s.CurrentRevision(ctx, taskA); rev != 5 {
		t.Fatalf("任务 B 追加后 A 的 revision 变为 %d", rev)
	}
	if rev, _ := s.CurrentRevision(ctx, taskB); rev != 6 {
		t.Fatalf("任务 B revision = %d, 期望 6", rev)
	}
}

// --- 单写者排队:并发追加串行化,revision 无重无漏 ---

func TestConcurrentAppendSingleWriter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-conc"
	const workers = 8
	const perWorker = 25

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				evt := na(NodeFact, fmt.Sprintf("f-%d-%d", w, i), fmt.Sprintf("事实 %d-%d", w, i), "active", "", nil)
				if _, err := s.Append(ctx, task, evt); err != nil {
					errs <- fmt.Errorf("worker %d 事件 %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发追加失败: %v", err)
	}

	total := workers * perWorker
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(evs) != total {
		t.Fatalf("事件数 = %d, 期望 %d", len(evs), total)
	}
	for i, e := range evs {
		if e.Revision != int64(i+1) {
			t.Fatalf("并发下 revision 出现重漏: 第 %d 条 = %d", i, e.Revision)
		}
	}
	if rev, _ := s.CurrentRevision(ctx, task); rev != int64(total) {
		t.Fatalf("CurrentRevision = %d, 期望 %d", rev, total)
	}
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.Facts) != total {
		t.Fatalf("事实节点数 = %d, 期望 %d", len(pg.Facts), total)
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("并发写入后重放重建失败: %v", err)
	}
	if pg2, _ := s.ProgressGraph(ctx, task); len(pg2.Facts) != total {
		t.Fatalf("重建后事实节点数 = %d, 期望 %d", len(pg2.Facts), total)
	}
}

// --- 受控入口校验:节点/边/事件类型的合法性 ---

func TestAppendValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-valid"
	mustAppend(t, s, task,
		na(NodeAsset, "www1", "www.example.com", "active", AssetSubdomain, nil), // r1
		na(NodeStep, "s1", "意图", "pending", "", nil),                            // r2
		na(NodeFact, "f1", "事实", "active", "", nil),                             // r3
		na(NodeTestClaim, "tc1", "声明", "candidate", "", nil),                    // r4
		na(NodeGoal, "g1", "目标", "active", "", nil),                             // r5
		ea(EdgeCovers, "s1", "www1"),                                            // r6
	)

	cases := []struct {
		name  string
		event Event
		want  error
	}{
		{"未知节点类型", na(NodeType("vuln"), "v1", "", "", "", nil), ErrUnknownNodeType},
		{"节点 id 为空", na(NodeFact, "", "", "", "", nil), ErrNodeIDEmpty},
		{"节点重复", na(NodeAsset, "www1", "再来一次", "active", AssetSubdomain, nil), ErrNodeExists},
		{"未知资产类", na(NodeAsset, "x1", "", "active", AssetClass("vpn"), nil), ErrUnknownAssetClass},
		{"未知边类型", ea(EdgeType("relates_to"), "f1", "www1"), ErrUnknownEdgeType},
		{"边端点为空", ea(EdgeAbout, "", "www1"), ErrEdgeEndpointsEmpty},
		{"自环", ea(EdgeSubOf, "www1", "www1"), ErrEdgeSelfLoop},
		{"端点缺失", ea(EdgeSubOf, "www1", "nope"), ErrNodeNotFound},
		{"类型不兼容 Fact─sub_of→Asset", ea(EdgeSubOf, "f1", "www1"), ErrEdgeRuleViolation},
		{"类型不兼容 Step─about→Asset", ea(EdgeAbout, "s1", "www1"), ErrEdgeRuleViolation},
		{"类型不兼容 Goal─derived_from→Fact", ea(EdgeDerivedFrom, "g1", "f1"), ErrEdgeRuleViolation},
		{"边重复", ea(EdgeCovers, "s1", "www1"), ErrEdgeExists},
		{"状态变更目标缺失", st("nope", "done"), ErrNodeNotFound},
		{"未知事件类型", Event{Kind: EventKind("node.deleted"), Actor: "test", Payload: "{}"}, ErrUnknownKind},
	}
	for _, c := range cases {
		if _, err := s.Append(ctx, task, c.event); !errors.Is(err, c.want) {
			t.Fatalf("%s: 期望 %v, 得到 %v", c.name, c.want, err)
		}
	}

	// 校验失败不落任何账
	if evs, _ := s.Events(ctx, task, 0, 0); len(evs) != 6 {
		t.Fatalf("校验失败后事件数 = %d, 期望 6", len(evs))
	}

	// 空资产类兜底为 other(未知资产不丢弃)
	mustAppend(t, s, task, na(NodeAsset, "odd1", "不明资产", "active", "", nil))
	ag, err := s.AssetGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询资产图失败: %v", err)
	}
	for _, a := range ag.Assets {
		if a.ID == "odd1" && a.AssetClass != AssetOther {
			t.Fatalf("空资产类应兜底为 other, 实际 %q", a.AssetClass)
		}
	}

	// 批内原子性:两个合法事件 + 一个非法事件 → 整批回滚
	before := countRows(t, s, "events")
	_, err = s.Append(ctx, task,
		na(NodeFact, "f2", "合法新事实", "active", "", nil),
		na(NodeFact, "f3", "另一个合法新事实", "active", "", nil),
		ea(EdgeAbout, "f2", "ghost-asset"),
	)
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("批内非法事件期望 ErrNodeNotFound, 得到 %v", err)
	}
	if n := countRows(t, s, "events"); n != before {
		t.Fatalf("批内原子性破坏: events %d → %d", before, n)
	}

	// 空任务 id 直接拒绝
	if _, err := s.Append(ctx, "", na(NodeFact, "x", "", "", "", nil)); !errors.Is(err, ErrEmptyTaskID) {
		t.Fatalf("空 task id 期望 ErrEmptyTaskID, 得到 %v", err)
	}
}

// --- P1-04:任务生命周期 / RoE / 虫群原始记录 三类受控事件 ---

func TestTaskLifecycleProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-lifecycle"

	mustAppend(t, s, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created", Title: "授权测试任务 A"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "started"}},
	)
	// 任务在另一 task_id 名下无投影行(隔离)
	if _, err := s.Node(ctx, task, "nope"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("跨名查找期望 ErrNodeNotFound, 得到 %v", err)
	}
	// 重复 created 拒绝
	if _, err := s.Append(ctx, task, Event{Kind: EventTaskLifecycle, Actor: "manager",
		Payload: TaskLifecycle{Action: "created", Title: "again"}}); !errors.Is(err, ErrTaskExists) {
		t.Fatalf("重复 created 期望 ErrTaskExists, 得到 %v", err)
	}
	// 未知生命周期动作拒绝
	if _, err := s.Append(ctx, task, Event{Kind: EventTaskLifecycle, Actor: "manager",
		Payload: TaskLifecycle{Action: "exploded"}}); !errors.Is(err, ErrUnknownTaskAction) {
		t.Fatalf("未知动作期望 ErrUnknownTaskAction, 得到 %v", err)
	}
	// 未创建先变更 → 任务不存在
	if _, err := s.Append(ctx, "t-ghost", Event{Kind: EventTaskLifecycle, Actor: "manager",
		Payload: TaskLifecycle{Action: "paused"}}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("幽灵任务期望 ErrTaskNotFound, 得到 %v", err)
	}
	// stopped 带细节(熔断理由),title 保持创建时的值
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "stopped", Detail: map[string]any{"reason": "熔断验证"}}})

	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatalf("查询任务列表失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("期望 1 个任务, 得到 %d", len(tasks))
	}
	got := tasks[0]
	if got.ID != task || got.Title != "授权测试任务 A" || got.Status != "stopped" {
		t.Fatalf("任务投影不符: %+v", got)
	}
	if got.Detail["reason"] != "熔断验证" {
		t.Fatalf("生命周期细节不符: %+v", got.Detail)
	}
	if got.CreatedRev != 1 || got.UpdatedRev != 3 {
		t.Fatalf("revision 踪迹不符: created=%d updated=%d", got.CreatedRev, got.UpdatedRev)
	}
}

// TestTaskLifecycleCompleted P1-10 完成终态动作:completed 进白名单、
// 投影更新、重放确定性;白名单仍拒未知动作。
func TestTaskLifecycleCompleted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-completed"

	mustAppend(t, s, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created", Title: "完成终态验证"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "started"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "completed", Detail: map[string]any{"reason": "全部目标经仲裁者判定达成"}}},
	)
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("查询任务列表失败: %v %+v", err, tasks)
	}
	if tasks[0].Status != "completed" || tasks[0].Title != "完成终态验证" ||
		tasks[0].Detail["reason"] != "全部目标经仲裁者判定达成" {
		t.Fatalf("completed 投影不符: %+v", tasks[0])
	}

	// 重放重建后仍一致(增量维护与全量重放同一确定性)。
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	tasks2, _ := s.Tasks(ctx)
	if len(tasks2) != 1 || tasks2[0].Status != "completed" {
		t.Fatalf("重放后 completed 投影不符: %+v", tasks2)
	}
}

func TestRoeProjectionAndCurrentRoE(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-roe"

	// 无事件时零值(边界从宽起步)
	r0, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询零值 RoE 失败: %v", err)
	}
	if len(r0.AllowedTools) != 0 || len(r0.DeniedTools) != 0 || r0.UpdatedRev != 0 {
		t.Fatalf("无事件时 RoE 应为零值, 得到 %+v", r0)
	}

	mustAppend(t, s, task,
		Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
			AllowedTools: []string{"bash", "port_probe"},
			DeniedTools:  []string{"nmap"},
			Note:         "初版约束"}},
	)
	r1, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE 失败: %v", err)
	}
	if len(r1.DeniedTools) != 1 || r1.DeniedTools[0] != "nmap" || r1.UpdatedRev != 1 {
		t.Fatalf("RoE 投影不符: %+v", r1)
	}
	// 覆盖语义:第二次变更整体替换当前值
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		DeniedTools: []string{"bash"},
		Note:        "收紧:禁 shell"}})
	r2, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE 第二次失败: %v", err)
	}
	if len(r2.AllowedTools) != 0 || len(r2.DeniedTools) != 1 || r2.DeniedTools[0] != "bash" || r2.UpdatedRev != 2 {
		t.Fatalf("RoE 应为覆盖语义: %+v", r2)
	}
	// 跨任务隔离:另一任务读不到
	r3, err := s.CurrentRoE(ctx, "t-roe-other")
	if err != nil || r3.UpdatedRev != 0 {
		t.Fatalf("跨任务 RoE 隔离失败: %+v err=%v", r3, err)
	}
}

func TestSwarmRecordNoProjectionButReplayable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-swarmrec"

	// 先派发一个 step,否则 swarm.record 语义上无锚点(校验只查自身字段)
	mustAppend(t, s, task, na(NodeStep, "s1", "探测意图", "dispatched", "", nil))
	mustAppend(t, s, task, Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{
		StepID: "s1", UnitID: "u1", TranscriptID: "tr-1", Type: SwarmRecSessionStarted, GraphVersion: 1}})
	mustAppend(t, s, task, Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{
		StepID: "s1", UnitID: "u1", Type: SwarmRecToolCall, ToolName: "bash",
		ToolArgs: json.RawMessage(`{"command":"id"}`), IsError: false,
		ResultRef: "sha256:abc", ResultHead: "uid=0(root)"}})

	// 未知类型与缺失字段拒绝
	if _, err := s.Append(ctx, task, Event{Kind: EventSwarmRecord, Actor: "swarm:u1",
		Payload: SwarmRecord{StepID: "s1", UnitID: "u1", Type: "daydream"}}); !errors.Is(err, ErrUnknownSwarmRecType) {
		t.Fatalf("未知记录类型期望 ErrUnknownSwarmRecType, 得到 %v", err)
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventSwarmRecord, Actor: "swarm:u1",
		Payload: SwarmRecord{StepID: "", UnitID: "u1", Type: SwarmRecToolCall, ToolName: "bash"}}); !errors.Is(err, ErrSwarmRecMissingField) {
		t.Fatalf("缺 StepID 期望 ErrSwarmRecMissingField, 得到 %v", err)
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventSwarmRecord, Actor: "swarm:u1",
		Payload: SwarmRecord{StepID: "s1", UnitID: "u1", Type: SwarmRecToolCall}}); !errors.Is(err, ErrSwarmRecMissingField) {
		t.Fatalf("tool_call 缺 ToolName 期望 ErrSwarmRecMissingField, 得到 %v", err)
	}

	// 不进双图投影(nodes/edges 不变),但事件流可查、负载可解码
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.Steps) != 1 || pg.Steps[0].Status != "dispatched" {
		t.Fatalf("swarm.record 不应改动图节点: %+v", pg.Steps)
	}
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件流失败: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("期望 3 条事件, 得到 %d", len(evs))
	}
	var rec SwarmRecord
	if err := json.Unmarshal(evs[2].Payload, &rec); err != nil {
		t.Fatalf("解码 swarm.record 负载失败: %v", err)
	}
	if rec.ToolName != "bash" || rec.ResultRef != "sha256:abc" || string(rec.ToolArgs) != `{"command":"id"}` {
		t.Fatalf("swarm.record 负载往返不符: %+v", rec)
	}
	if evs[2].Actor != "swarm:u1" {
		t.Fatalf("actor 应为虫群单位, 得到 %q", evs[2].Actor)
	}
}

func TestNewKindsRebuildDeterministic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-rebuild"
	mustAppend(t, s, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created", Title: "重放任务"}},
		Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{DeniedTools: []string{"bash"}}},
		na(NodeStep, "s1", "意图", "done", "", nil),
		Event{Kind: EventSwarmRecord, Actor: "swarm:u9", Payload: SwarmRecord{StepID: "s1", UnitID: "u9", Type: SwarmRecSessionEnd, Note: "completed"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "paused"}},
	)
	snapTasks, _ := s.Tasks(ctx)
	snapRoe, _ := s.CurrentRoE(ctx, task)
	snapEvents, _ := s.Events(ctx, task, 0, 0)

	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	tasks2, _ := s.Tasks(ctx)
	roe2, _ := s.CurrentRoE(ctx, task)
	events2, _ := s.Events(ctx, task, 0, 0)
	if !reflect.DeepEqual(snapTasks, tasks2) {
		t.Fatalf("重放后 tasks 投影漂移:\n%+v\n%+v", snapTasks, tasks2)
	}
	if !reflect.DeepEqual(snapRoe, roe2) {
		t.Fatalf("重放后 RoE 投影漂移:\n%+v\n%+v", snapRoe, roe2)
	}
	if !reflect.DeepEqual(snapEvents, events2) {
		t.Fatalf("事件流在重建中被改动")
	}
}

func TestArbiterVerdictNoProjectionButReplayable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-verdict"

	mustAppend(t, s, task, na(NodeGoal, "g1", "目标", "active", "", nil))
	verdict := ArbiterVerdict{
		GoalID: "g1", Op: "all_of", Satisfied: true, EvaluatedRev: 1,
		Conditions: []ArbiterConditionResult{{
			Kind: "fact_confirmed", AssetID: "a1", Required: 1, Actual: 2, Satisfied: true,
		}},
	}
	mustAppend(t, s, task,
		Event{Kind: EventArbiterVerdict, Actor: "arbiter", Payload: verdict},
		st("g1", "achieved"),
	)

	// 校验:缺 goal_id 拒;"判达成"缺条件拒(裁决必须可审计到依据)。
	if _, err := s.Append(ctx, task, Event{Kind: EventArbiterVerdict, Actor: "arbiter",
		Payload: ArbiterVerdict{Satisfied: true, Conditions: verdict.Conditions}}); !errors.Is(err, ErrArbiterVerdictMissingField) {
		t.Fatalf("缺 GoalID 期望 ErrArbiterVerdictMissingField, 得到 %v", err)
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventArbiterVerdict, Actor: "arbiter",
		Payload: ArbiterVerdict{GoalID: "g1", Satisfied: true}}); !errors.Is(err, ErrArbiterVerdictMissingField) {
		t.Fatalf("判达成缺条件期望 ErrArbiterVerdictMissingField, 得到 %v", err)
	}

	// 不进双图投影(Goal 状态迁移走 node.state_changed,本事件是审计流)。
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.Goals) != 1 || pg.Goals[0].Status != "achieved" {
		t.Fatalf("verdict 事件不应直接改图;状态迁移经 state_changed: %+v", pg.Goals)
	}
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil || len(evs) != 3 {
		t.Fatalf("期望 3 条事件, 得到 %d (err=%v)", len(evs), err)
	}
	var got ArbiterVerdict
	if err := json.Unmarshal(evs[1].Payload, &got); err != nil {
		t.Fatalf("解码 arbiter.verdict 负载失败: %v", err)
	}
	if !reflect.DeepEqual(got, verdict) {
		t.Fatalf("verdict 负载往返不符:\n%+v\n%+v", got, verdict)
	}
	if evs[1].Actor != "arbiter" {
		t.Fatalf("actor 应为 arbiter, 得到 %q", evs[1].Actor)
	}

	// 重放确定性:含 verdict 的事件流重建后投影不变。
	snapGoals, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	goals2, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(snapGoals, goals2) {
		t.Fatalf("重放后进度图漂移:\n%+v\n%+v", snapGoals, goals2)
	}
}

func TestApprovalRecordValidationAndReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-approval"

	// 合法三连:requested → approved / denied。
	mustAppend(t, s, task,
		Event{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: "ap-1", StepID: "s1", UnitID: "u1", Type: ApprovalRecRequested,
			ActionLevel: ActionActiveTest, Intent: "主动验证", ToolName: "bash",
			ToolArgs: json.RawMessage(`{"command":"id"}`)}},
		Event{Kind: EventApprovalRecord, Actor: "creator", Payload: ApprovalRecord{
			RequestID: "ap-1", StepID: "s1", UnitID: "u1", Type: ApprovalRecApproved,
			ActionLevel: ActionActiveTest, ToolName: "bash", DecidedBy: "creator", Reason: "允许"}},
		Event{Kind: EventApprovalRecord, Actor: "swarm:u2", Payload: ApprovalRecord{
			RequestID: "ap-2", StepID: "s2", UnitID: "u2", Type: ApprovalRecDenied,
			ActionLevel: ActionModifyPersist, ToolName: "bash",
			Reason: "意图分级 修改与持久化 超出 RoE 分级上限 利用深入"}},
	)

	// fail-safe 拒绝留痕:分级缺失/非法的 denied 记录必须可落账
	//(被拒对象正是坏分级,记录不受词汇表约束)。
	mustAppend(t, s, task, Event{Kind: EventApprovalRecord, Actor: "swarm:u3", Payload: ApprovalRecord{
		RequestID: "ap-3", StepID: "s3", UnitID: "u3", Type: ApprovalRecDenied,
		ActionLevel: "", ToolName: "bash", Reason: "意图分级缺失或非法"}})

	// 受控入口:未知类型 / 缺字段 / requested 带坏分级 / approved 带空分级,一律拒。
	bad := []Event{
		{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: "ap-x", StepID: "s1", UnitID: "u1", Type: "maybe", ActionLevel: ActionReadOnly, ToolName: "bash"}},
		{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			StepID: "s1", UnitID: "u1", Type: ApprovalRecRequested, ActionLevel: ActionReadOnly, ToolName: "bash"}},
		{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: "ap-x", StepID: "s1", UnitID: "u1", Type: ApprovalRecRequested,
			ActionLevel: "随便写", ToolName: "bash"}},
		{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: "ap-x", StepID: "s1", UnitID: "u1", Type: ApprovalRecApproved,
			ActionLevel: "", ToolName: "bash"}},
		{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: "ap-x", StepID: "s1", UnitID: "u1", Type: ApprovalRecRequested,
			ActionLevel: ActionReadOnly, ToolName: ""}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法审批记录 #%d 应被拒", i+1)
		}
	}

	// 不进双图投影(审计流);负载往返;重放确定性。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil || len(evs) != 4 {
		t.Fatalf("期望 4 条事件, 得到 %d (err=%v)", len(evs), err)
	}
	var got ApprovalRecord
	if err := json.Unmarshal(evs[0].Payload, &got); err != nil {
		t.Fatalf("解码 approval.record 失败: %v", err)
	}
	if got.RequestID != "ap-1" || got.Type != ApprovalRecRequested || got.ActionLevel != ActionActiveTest ||
		string(got.ToolArgs) != `{"command":"id"}` || got.Intent != "主动验证" {
		t.Fatalf("approval 负载往返不符: %+v", got)
	}
	pg, _ := s.ProgressGraph(ctx, task)
	ag, _ := s.AssetGraph(ctx, task)
	if len(pg.Steps) != 0 && len(ag.Assets) != 0 {
		t.Fatalf("审批记录不得进双图投影")
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	evs2, _ := s.Events(ctx, task, 0, 0)
	if len(evs2) != 4 {
		t.Fatalf("重放后事件流不得变化: %d", len(evs2))
	}
}

func TestRoeApprovalThreshold(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-roe-level"

	// 空门槛 = 未设置(消费侧归一为默认宽:仅增删改弹门),落账合法。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		DeniedTools: []string{"nmap"}}})
	r1, err := s.CurrentRoE(ctx, task)
	if err != nil || r1.ApprovalThreshold != "" || r1.UpdatedRev != 1 {
		t.Fatalf("空门槛应原样存取: %+v err=%v", r1, err)
	}

	// 合法四级门槛落投影(P2-06b:收紧到主动测试及以上弹门)。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		ApprovalThreshold: ActionActiveTest, Note: "收紧审批门槛"}})
	r2, err := s.CurrentRoE(ctx, task)
	if err != nil || r2.ApprovalThreshold != ActionActiveTest || r2.UpdatedRev != 2 {
		t.Fatalf("门槛投影不符: %+v err=%v", r2, err)
	}

	// 非法门槛整批拒(受控入口)。
	if _, err := s.Append(ctx, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ApprovalThreshold: "为所欲为"}}); err == nil {
		t.Fatalf("非法审批门槛应被拒")
	}
	r3, _ := s.CurrentRoE(ctx, task)
	if r3.ApprovalThreshold != ActionActiveTest {
		t.Fatalf("被拒变更不得污染当前值: %+v", r3)
	}

	// 旧字段 max_action_level(P1-07 分级上限,v2 已废止):新写入不再
	// 落投影(消费侧无读者),与旧事件共存不炸。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		ApprovalThreshold: ActionExploit, MaxActionLevel: ActionModifyPersist, Note: "带旧字段"}})
	r5, _ := s.CurrentRoE(ctx, task)
	if r5.ApprovalThreshold != ActionExploit || r5.MaxActionLevel != "" {
		t.Fatalf("旧字段已废止,新写入不落投影: %+v", r5)
	}

	// 迁移 v5: RoeState 新列随事件流重放完整复原。
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	r4, _ := s.CurrentRoE(ctx, task)
	if r4.ApprovalThreshold != ActionExploit || r4.Note != "带旧字段" {
		t.Fatalf("重放后 RoE 不符: %+v", r4)
	}
}

// --- P1-08: TestClaim 状态机记录(testclaim.record)与资产锚点边 ---

func TestTestClaimRecordValidationAndReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-testclaim"

	// 铺垫:资产 + 发现 Step + 候选声明(about/derived_from 边从 P1-08 起对 TestClaim 开放)。
	mustAppend(t, s, task,
		na(NodeAsset, "web1", "http://mock", "active", AssetService, nil),
		na(NodeStep, "s1", "审计主站敏感路径", "done", "", map[string]any{"action_level": "只读探测"}),
		na(NodeTestClaim, "tc-1", "备份目录可公开列举", TestClaimCandidate, "", map[string]any{
			"unit_id": "u1", "impact": "备份文件可被未授权下载", "standard": map[string]any{
				"kind": "single_round_falsify", "require_impact": true}}),
		ea(EdgeAbout, "tc-1", "web1"),
		ea(EdgeDerivedFrom, "tc-1", "s1"),
	)

	// 合法两连:反证单位写回结构化结论 → 仲裁者裁决(Confirmed)。
	// P2-02:impact_supported=true 必带四级影响面定级;轮次与逐条竞争
	// 解释结论随写回落账。
	falsify := TestClaimRecord{
		ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: TestClaimRecFalsify,
		Round: 1, ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []AltResult{{ID: "alt1", Refuted: true}},
		EvidenceRef:  "sha256:ab", EvidenceHead: "curl 200 mock backup archive listing",
	}
	verdict := TestClaimRecord{
		ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: TestClaimRecVerdict,
		Satisfied: true, EvaluatedRev: 8,
		Conditions: []ArbiterConditionResult{
			{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
			{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
			{Kind: "impact_supported", Required: 1, Actual: 1, Satisfied: true},
			{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
		},
	}
	mustAppend(t, s, task,
		Event{Kind: EventTestClaimRecord, Actor: "swarm:u2", Payload: falsify},
		Event{Kind: EventTestClaimRecord, Actor: "arbiter", Payload: verdict},
		st("tc-1", TestClaimConfirmed),
	)

	// 待确认裁决必须带搁置理由(R4 账本物理强制);带理由的 pending 合法。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
			Conditions: verdict.Conditions}}); !errors.Is(err, ErrTestClaimPendingNoReason) {
		t.Fatalf("无理由待确认期望 ErrTestClaimPendingNoReason, 得到 %v", err)
	}
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
			PendingReason: "影响面证据不足", Conditions: verdict.Conditions}})

	// 受控入口:未知类型 / 缺 claim_id / falsify 缺 Step 或 Unit / verdict 缺
	// 条件,一律拒;P2-02 新增:影响面成立缺定级、定级超词汇、解释结论缺
	// ID、decision 不自洽、缺省 decision 与 satisfied 矛盾、reopen 缺触发词
	// 或理由、repro 严重级超词汇。
	bad := []Event{
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: "recount"}},
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{StepID: "s2", UnitID: "u2", Type: TestClaimRecFalsify}},
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{ClaimID: "tc-1", UnitID: "u2", Type: TestClaimRecFalsify}},
		{Kind: EventTestClaimRecord, Actor: "arbiter",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict, Satisfied: true}},
		// P2-02:falsify 的 impact_supported=true 缺定级。
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{ClaimID: "tc-2", StepID: "sx", UnitID: "u2",
				Type: TestClaimRecFalsify, ImpactSupported: true}},
		// P2-02:定级不在四级词汇内。
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{ClaimID: "tc-2", StepID: "sx", UnitID: "u2",
				Type: TestClaimRecFalsify, ImpactSupported: true, ImpactLevel: "严重"}},
		// P2-02:解释结论缺 ID。
		{Kind: EventTestClaimRecord, Actor: "swarm:u2",
			Payload: TestClaimRecord{ClaimID: "tc-2", StepID: "sx", UnitID: "u2",
				Type: TestClaimRecFalsify, Alternatives: []AltResult{{Refuted: true}}}},
		// P2-02:decision=confirm 但 satisfied=false(不自洽)。
		{Kind: EventTestClaimRecord, Actor: "arbiter",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
				Decision: VerdictConfirm, Conditions: verdict.Conditions}},
		// P2-02:advance_round 裁决缺理由。
		{Kind: EventTestClaimRecord, Actor: "arbiter",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
				Decision: VerdictAdvanceRound, Conditions: verdict.Conditions}},
		// P2-02:reopen 缺触发词 / 缺理由。
		{Kind: EventTestClaimRecord, Actor: "creator",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecReopen, Reason: "x"}},
		{Kind: EventTestClaimRecord, Actor: "creator",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecReopen, Trigger: ReopenManual}},
		// P2-02:repro 严重级超词汇。
		{Kind: EventTestClaimRecord, Actor: "overmind",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecRepro, Severity: "极高"}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法 testclaim 记录 #%d 应被拒", i+1)
		}
	}

	// P2-02 合法新记录:advance_round 带理由、reopen、repro 均可落账。
	mustAppend(t, s, task,
		Event{Kind: EventTestClaimRecord, Actor: "arbiter",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
				Decision: VerdictAdvanceRound, PendingReason: "1 条解释未排除,待下一轮",
				Conditions: verdict.Conditions}},
		Event{Kind: EventTestClaimRecord, Actor: "creator",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecReopen,
				Trigger: ReopenNewEvidence, Reason: "新证据显示可能是缓存污染"}},
		Event{Kind: EventTestClaimRecord, Actor: "overmind",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecRepro, Severity: "high",
				Steps: []ReproStep{{Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
					Op:     `bash {"command":"curl -sS http://mock/old-backup/"}`,
					Method: "GET", URL: "http://mock/old-backup/", Status: 200, BlobRef: "sha256:ab"}}}},
	)

	// 不进双图投影(审计流):TestClaim 节点本身在进度图,记录事件不增节点边。
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.TestClaims) != 1 || pg.TestClaims[0].Status != TestClaimConfirmed {
		t.Fatalf("进度图应恰 1 条已确认声明(状态迁移走 state_changed): %+v", pg.TestClaims)
	}
	if len(pg.Lineage) != 1 || pg.Lineage[0].Type != EdgeDerivedFrom {
		t.Fatalf("血缘应恰含 derived_from(tc-1→s1): %+v", pg.Lineage)
	}
	ag, _ := s.AssetGraph(ctx, task)
	if len(ag.Assets) != 1 {
		t.Fatalf("testclaim.record 不得进资产图: %+v", ag.Assets)
	}

	// 负载往返 + actor + 重放确定性。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件流失败: %v", err)
	}
	var gotF, gotV TestClaimRecord
	_ = json.Unmarshal(evs[5].Payload, &gotF)
	_ = json.Unmarshal(evs[6].Payload, &gotV)
	if !reflect.DeepEqual(gotF, falsify) || !reflect.DeepEqual(gotV, verdict) {
		t.Fatalf("testclaim.record 负载往返不符:\n%+v\n%+v", gotF, falsify)
	}
	if evs[5].Actor != "swarm:u2" || evs[6].Actor != "arbiter" {
		t.Fatalf("actor 不符: %q / %q", evs[5].Actor, evs[6].Actor)
	}
	snap, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	after, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(snap, after) {
		t.Fatalf("重放后进度图漂移:\n%+v\n%+v", snap, after)
	}
}

// TestRetestRecordValidation 复测结论记录(P3-06)的受控入口校验:
// 结论词受限、会话锚与单位必带、证据必带;fixed 状态词可经 state_changed
// 落投影;负载往返保真。
func TestRetestRecordValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-retest"

	mustAppend(t, s, task,
		na(NodeAsset, "web1", "http://mock", "active", AssetService, nil),
		na(NodeTestClaim, "tc-1", "备份目录可公开列举", TestClaimConfirmed, "", map[string]any{
			"unit_id": "u1", "impact": "备份文件可被未授权下载"}),
		ea(EdgeAbout, "tc-1", "web1"),
	)

	// 合法:fixed 结论(会话锚 + 单位 + 证据)→ 状态迁移 fixed 入投影。
	mustAppend(t, s, task,
		Event{Kind: EventTestClaimRecord, Actor: "swarm:rt-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "retest-tc-1-r1",
				UnitID: "rt-x1", Type: TestClaimRecRetest, Round: 1,
				RetestVerdict: RetestVerdictFixed,
				EvidenceRef:   "sha256:cd", EvidenceHead: "curl 403 漏洞不再可触发"}},
		st("tc-1", TestClaimFixed),
	)
	node, err := s.Node(ctx, task, "tc-1")
	if err != nil || node.Status != TestClaimFixed {
		t.Fatalf("fixed 状态应入投影: %s %v", node.Status, err)
	}

	// 非法:结论词超词汇 / 缺会话锚 / 缺单位 / 缺证据。
	bad := []Event{
		{Kind: EventTestClaimRecord, Actor: "swarm:rt-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "retest-tc-1-r2",
				UnitID: "rt-x1", Type: TestClaimRecRetest, RetestVerdict: "maybe",
				EvidenceHead: "x"}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rt-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", UnitID: "rt-x1",
				Type: TestClaimRecRetest, RetestVerdict: RetestVerdictUnfixed,
				EvidenceHead: "x"}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rt-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "retest-tc-1-r2",
				Type: TestClaimRecRetest, RetestVerdict: RetestVerdictUnfixed,
				EvidenceHead: "x"}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rt-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "retest-tc-1-r2",
				UnitID: "rt-x1", Type: TestClaimRecRetest, RetestVerdict: RetestVerdictUnfixed}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法 retest 记录 #%d 应被拒", i+1)
		}
	}

	// 负载往返 + 重放确定性。
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "swarm:rt-x2",
		Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "retest-tc-1-r2",
			UnitID: "rt-x2", Type: TestClaimRecRetest, Round: 2,
			RetestVerdict: RetestVerdictUnfixed, Note: "漏洞仍在",
			EvidenceHead: "curl 200 全量备份列举"}})
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var got TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != EventTestClaimRecord {
			continue
		}
		var r TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == TestClaimRecRetest {
			got = r
		}
	}
	if got.RetestVerdict != RetestVerdictUnfixed || got.Round != 2 ||
		got.StepID != "retest-tc-1-r2" || got.EvidenceHead == "" {
		t.Fatalf("retest 记录负载往返不符: %+v", got)
	}
	snap, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	after, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(snap, after) {
		t.Fatalf("重放后进度图漂移")
	}
}

func TestTestClaimAboutEdgeRule(t *testing.T) {
	s := openTestStore(t)
	const task = "t-tc-edge"

	// TestClaim ─about→ Asset 合法(P1-08 资产锚点);指向非资产仍拒。
	mustAppend(t, s, task,
		na(NodeAsset, "a1", "资产", "active", AssetHost, nil),
		na(NodeTestClaim, "tc-1", "声明", TestClaimCandidate, "", nil),
		ea(EdgeAbout, "tc-1", "a1"),
	)
	if _, err := s.Append(context.Background(), task, ea(EdgeAbout, "tc-1", "a1")); !errors.Is(err, ErrEdgeExists) {
		t.Fatalf("重复 about 边应被拒, 得到 %v", err)
	}
	mustAppend(t, s, task, na(NodeStep, "s1", "意图", "done", "", nil))
	if _, err := s.Append(context.Background(), task, ea(EdgeAbout, "tc-1", "s1")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("about 指向 Step 应违反类型规则, 得到 %v", err)
	}
}

func TestMirrorRecordValidationAndReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-mirror"

	// 铺垫:资产 + Step(镜留痕的归因端点,流量钉回图上执行记录)。
	mustAppend(t, s, task,
		na(NodeAsset, "web1", "http://mock", "active", AssetService, nil),
		na(NodeStep, "s1", "HTTP 探测主站", "dispatched", "", map[string]any{"action_level": "只读探测"}),
		ea(EdgeCovers, "s1", "web1"),
	)

	// 合法两连:http 全量留痕(带 blob 引用)→ CONNECT 隧道元数据留痕。
	httpRec := MirrorRecord{
		ExchangeID: "ms-abcd1234.1", Type: MirrorRecHTTP, StepID: "s1", UnitID: "u1",
		TranscriptID: "swarm-u1", Method: "GET", URL: "http://127.0.0.1:1/api/version",
		Status: 200, ReqBytes: 78, RespBytes: 44,
		BlobRef: "sha256:" + strings.Repeat("ab", 32), ElapsedMS: 12,
	}
	tunnelRec := MirrorRecord{
		ExchangeID: "ms-abcd1234.2", Type: MirrorRecTunnel, StepID: "s1", UnitID: "u1",
		URL: "secret.example.com:443", ReqBytes: 517, RespBytes: 8192, ElapsedMS: 230,
	}
	mustAppend(t, s, task,
		Event{Kind: EventMirrorRecord, Actor: "mirror", Payload: httpRec},
		Event{Kind: EventMirrorRecord, Actor: "mirror", Payload: tunnelRec},
	)

	// 受控入口:未知类型 / 缺归因(http 无主不可审计) / http 缺 blob 引用
	// ("全量留痕"意味着原文可回放,有引用才算留痕),一律拒。
	bad := []Event{
		{Kind: EventMirrorRecord, Actor: "mirror",
			Payload: MirrorRecord{ExchangeID: "x", Type: "peek", StepID: "s1", UnitID: "u1"}},
		{Kind: EventMirrorRecord, Actor: "mirror",
			Payload: MirrorRecord{Type: MirrorRecHTTP, StepID: "s1", UnitID: "u1",
				BlobRef: "sha256:" + strings.Repeat("cd", 32)}},
		{Kind: EventMirrorRecord, Actor: "mirror",
			Payload: MirrorRecord{ExchangeID: "x", Type: MirrorRecHTTP, UnitID: "u1",
				BlobRef: "sha256:" + strings.Repeat("cd", 32)}},
		{Kind: EventMirrorRecord, Actor: "mirror",
			Payload: MirrorRecord{ExchangeID: "x", Type: MirrorRecHTTP, StepID: "s1"}},
		{Kind: EventMirrorRecord, Actor: "mirror",
			Payload: MirrorRecord{ExchangeID: "x", Type: MirrorRecHTTP, StepID: "s1", UnitID: "u1"}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法 mirror 记录 #%d 应被拒", i+1)
		}
	}

	// 不进双图投影(审计流,同 swarm.record)。
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.Steps) != 1 || len(pg.Lineage) != 0 {
		t.Fatalf("mirror.record 不得进进度图: %+v", pg)
	}
	ag, _ := s.AssetGraph(ctx, task)
	if len(ag.Assets) != 1 {
		t.Fatalf("mirror.record 不得进资产图: %+v", ag.Assets)
	}

	// 负载往返 + actor + 重放确定性。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件流失败: %v", err)
	}
	var gotH, gotT MirrorRecord
	_ = json.Unmarshal(evs[3].Payload, &gotH)
	_ = json.Unmarshal(evs[4].Payload, &gotT)
	if !reflect.DeepEqual(gotH, httpRec) || !reflect.DeepEqual(gotT, tunnelRec) {
		t.Fatalf("mirror.record 负载往返不符:\n%+v\n%+v", gotH, httpRec)
	}
	if evs[3].Actor != "mirror" || evs[4].Actor != "mirror" {
		t.Fatalf("actor 不符: %q / %q", evs[3].Actor, evs[4].Actor)
	}
	snap, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败(含 mirror.record): %v", err)
	}
	after, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(snap, after) {
		t.Fatalf("重放后进度图漂移:\n%+v\n%+v", snap, after)
	}
}

// TestHypothesisRecordValidationAndReplay P2-03:hypothesis.record 受控入口
// 校验(评估三去向词汇 + 理由必填;结论词汇受限 + 须绑验证 Step 与单位)
// 与假设边规则(produces Step→Hypothesis / verifies Step→Hypothesis)、
// 不进投影 + 重放确定性。
func TestHypothesisRecordValidationAndReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-hypo"

	// 铺垫:资产 + 发现 Step + 候选假设(produces/about 回流轨迹)。
	mustAppend(t, s, task,
		na(NodeAsset, "a1", "http://mock", "active", AssetService, nil),
		na(NodeStep, "s1", "发现", "done", "", map[string]any{"action_level": "只读探测"}),
		na(NodeHypothesis, "h-1", "疑似管理后台", HypothesisCandidate, "",
			map[string]any{"basis": "配置风格", "unit_id": "u1", "step_id": "s1"}),
		ea(EdgeProduces, "s1", "h-1"),
		ea(EdgeAbout, "h-1", "a1"),
	)

	// 合法评估(verify,带验证步骤绑定)+ 合法结论(supported)。
	evalRec := HypothesisRecord{
		HypothesisID: "h-1", StepID: "s2", Type: HypothesisRecEvaluation,
		Decision: HypothesisVerify, Reason: "可低成本证伪", VerifyIntent: "探测根路径",
	}
	resultRec := HypothesisRecord{
		HypothesisID: "h-1", StepID: "s2", UnitID: "u2", Type: HypothesisRecResult,
		Verdict: HypothesisSupported, EvidenceHead: "401 + console",
	}
	mustAppend(t, s, task,
		Event{Kind: EventHypothesisRecord, Actor: "overmind", Payload: evalRec},
		Event{Kind: EventHypothesisRecord, Actor: "swarm:u2", Payload: resultRec},
	)

	// 受控入口:未知类型 / 未知去向 / 未知结论 / 评估缺理由 / 结论缺
	// 验证步骤或单位,一律拒。
	bad := []Event{
		{Kind: EventHypothesisRecord, Actor: "overmind",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: "guess"}},
		{Kind: EventHypothesisRecord, Actor: "overmind",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: HypothesisRecEvaluation, Decision: "maybe"}},
		{Kind: EventHypothesisRecord, Actor: "overmind",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: HypothesisRecEvaluation,
				Decision: HypothesisDiscard}}, // 废案缺理由(R10)
		{Kind: EventHypothesisRecord, Actor: "swarm:u2",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: HypothesisRecResult, Verdict: "maybe"}},
		{Kind: EventHypothesisRecord, Actor: "swarm:u2",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: HypothesisRecResult,
				Verdict: HypothesisRefuted, UnitID: "u2"}}, // 缺验证步骤
		{Kind: EventHypothesisRecord, Actor: "swarm:u2",
			Payload: HypothesisRecord{HypothesisID: "h-1", Type: HypothesisRecResult,
				Verdict: HypothesisRefuted, StepID: "s2"}}, // 缺单位
		{Kind: EventHypothesisRecord, Actor: "overmind",
			Payload: HypothesisRecord{Type: HypothesisRecEvaluation,
				Decision: HypothesisHold, Reason: "r"}}, // 缺假设 ID
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法记录 #%d 应被拒", i)
		}
	}

	// 边规则:verifies 仅 Step→Hypothesis;Fact 不得 verifies。
	mustAppend(t, s, task,
		na(NodeStep, "s2", "验证假设", "dispatched", "",
			map[string]any{"kind": "verify", "hypothesis_id": "h-1"}),
		ea(EdgeVerifies, "s2", "h-1"),
	)
	mustAppend(t, s, task, na(NodeFact, "f1", "事实", "confirmed", "", nil))
	if _, err := s.Append(ctx, task, ea(EdgeVerifies, "f1", "h-1")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("Fact ─verifies→ Hypothesis 应违反类型规则, 得到 %v", err)
	}
	if _, err := s.Append(ctx, task, ea(EdgeVerifies, "s2", "f1")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("Step ─verifies→ Fact 应违反类型规则, 得到 %v", err)
	}
	if _, err := s.Append(ctx, task, ea(EdgeProduces, "h-1", "f1")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("Hypothesis ─produces→ Fact 应违反类型规则(产出边只从 Step 出), 得到 %v", err)
	}

	// hypothesis.record 不进双图投影;负载往返 + 重放确定性。
	pg, _ := s.ProgressGraph(ctx, task)
	for _, f := range pg.Facts {
		if f.ID == "h-1" {
			t.Fatalf("hypothesis.record 不得进进度图: %+v", pg)
		}
	}
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件流失败: %v", err)
	}
	var gotE, gotR HypothesisRecord
	_ = json.Unmarshal(evs[5].Payload, &gotE)
	_ = json.Unmarshal(evs[6].Payload, &gotR)
	if !reflect.DeepEqual(gotE, evalRec) || !reflect.DeepEqual(gotR, resultRec) {
		t.Fatalf("hypothesis.record 负载往返不符:\n%+v\n%+v", gotE, evalRec)
	}
	if evs[5].Actor != "overmind" || evs[6].Actor != "swarm:u2" {
		t.Fatalf("actor 不符: %q / %q", evs[5].Actor, evs[6].Actor)
	}
	snap, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败(含 hypothesis.record): %v", err)
	}
	after, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(snap, after) {
		t.Fatalf("重放后进度图漂移:\n%+v\n%+v", snap, after)
	}
}

// ---- P2-04:黑匣子通道 + token 独立表 + settling 生命周期 ----

func TestBlackboxRecordValidationAndReplay(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// 类型词汇受限:未知类型整批拒。
	if _, err := store.Append(ctx, "bb-task", Event{
		Kind: EventBlackboxRecord, Actor: "swarm:u1",
		Payload: BlackboxRecord{Type: "vibes"},
	}); err == nil {
		t.Fatalf("未知黑匣子类型应被拒")
	}
	// 合法记录:不进 nodes/edges 投影,事件流可回放,重放确定性。
	if _, err := store.Append(ctx, "bb-task",
		Event{Kind: EventBlackboxRecord, Actor: "swarm:u1",
			Payload: BlackboxRecord{Type: BlackboxUnitExhausted,
				StepID: "s1", UnitID: "u1", Detail: map[string]any{"turns": 3}}},
		Event{Kind: EventBlackboxRecord, Actor: "overmind",
			Payload: BlackboxRecord{Type: BlackboxTaskSettling,
				Detail: map[string]any{"grace_seconds": 90}}},
		Event{Kind: EventBlackboxRecord, Actor: "overmind",
			Payload: BlackboxRecord{Type: BlackboxTaskSettled,
				Detail: map[string]any{"achieved": true, "terminal": "completed"}}},
	); err != nil {
		t.Fatalf("合法黑匣子记录应入账: %v", err)
	}
	evs, err := store.Events(ctx, "bb-task", 0, 0)
	if err != nil || len(evs) != 3 {
		t.Fatalf("黑匣子事件应 3 条: %v %d", err, len(evs))
	}
	var kinds []string
	for _, ev := range evs {
		var r BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码: %v", err)
		}
		kinds = append(kinds, r.Type)
	}
	want := []string{BlackboxUnitExhausted, BlackboxTaskSettling, BlackboxTaskSettled}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("次序不符: %v", kinds)
		}
	}
	if err := store.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if after, err := store.Events(ctx, "bb-task", 0, 0); err != nil || len(after) != 3 {
		t.Fatalf("重放后事件应保持 3 条: %v %d", err, len(after))
	}
}

func TestTokenUsageIndependentTable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// 结构校验:无主用量拒收。
	if err := store.RecordTokens(ctx, TokenUsage{Agent: "swarm:u1"}); err == nil {
		t.Fatalf("缺任务的用量行应被拒")
	}
	if err := store.RecordTokens(ctx, TokenUsage{TaskID: "tk"}); err == nil {
		t.Fatalf("缺 agent 的用量行应被拒")
	}
	// 正常写入与读回:任务隔离、seq 升序、字段完整。
	rows := []TokenUsage{
		{TaskID: "tk-a", Agent: "swarm:u1", Model: "m1", TranscriptID: "swarm-u1",
			Turn: 1, InputTokens: 200, OutputTokens: 40, CacheWriteTokens: 10},
		{TaskID: "tk-a", Agent: "swarm:u1", Model: "m1", TranscriptID: "swarm-u1",
			Turn: 2, InputTokens: 300, OutputTokens: 60, CacheReadTokens: 200},
		{TaskID: "tk-a", Agent: "overmind", Model: "m1", TranscriptID: "overmind-x-d1", Turn: 1, InputTokens: 500},
		{TaskID: "tk-b", Agent: "swarm:u1", Model: "m1", Turn: 1, InputTokens: 1},
	}
	if err := store.RecordTokens(ctx, rows...); err != nil {
		t.Fatalf("写入 token 用量失败: %v", err)
	}
	got, err := store.TokenUsageRows(ctx, "tk-a")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("tk-a 应 3 行,实际 %d", len(got))
	}
	if got[0].Turn != 1 || got[1].Turn != 2 || got[2].Agent != "overmind" ||
		got[1].CacheReadTokens != 200 || got[0].CacheWriteTokens != 10 || got[2].InputTokens != 500 {
		t.Fatalf("行内容不符: %+v", got)
	}
	// 独立表不进事件流:token 行不产生 events(防账本膨胀的物理隔离)。
	evs, _ := store.Events(ctx, "tk-a", 0, 0)
	if len(evs) != 0 {
		t.Fatalf("token 用量不得进事件流: %d", len(evs))
	}
	// 重建投影不触碰独立表。
	if err := store.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if after, _ := store.TokenUsageRows(ctx, "tk-a"); len(after) != 3 {
		t.Fatalf("重放后 token 表应保持 3 行: %d", len(after))
	}
}

func TestSettlingLifecycleAction(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.Append(ctx, "st-task",
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "created", Title: "t"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "started"}},
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "settling",
				Detail: map[string]any{"reason": "任务整体超时,进入有序收尾"}}},
	); err != nil {
		t.Fatalf("settling 生命周期应可入账: %v", err)
	}
	row, err := store.Task(ctx, "st-task")
	if err != nil || row.Status != "settling" {
		t.Fatalf("任务投影应收尾中: %v %+v", err, row)
	}
	if row.Detail["reason"] != "任务整体超时,进入有序收尾" {
		t.Fatalf("detail 应留存: %+v", row.Detail)
	}
}

// TestOvermindRecordValidationAndReplay P2-06:overmind.record 受控入口
// 校验(类型词汇受限 + assessment 五问与五问外研判至少一项非空)、
// 不进投影 + 负载往返 + 重放确定性。
func TestOvermindRecordValidationAndReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-overmind"

	mustAppend(t, s, task,
		na(NodeGoal, "g1", "目标", "active", "", nil),
		na(NodeStep, "s1", "意图", "done", "", map[string]any{"action_level": "只读探测"}),
	)

	// 合法研判记录:五问逐问结论 + 派发摘要(五问是启发式起点,不设上限)。
	rec := OvermindRecord{
		Type: OvermindRecAssessment, SessionID: "overmind-t-overmind-d1",
		EvaluatedRev: 3,
		QAccess:      "无新身份(本轮无凭据类新事实)",
		QPrivilege:   "无",
		QCombination: "f-1(备份目录可未授权访问)与 f-2(管理后台需认证)可组合:备份泄露的凭据或可登录后台",
		QUnlock:      "无受阻步骤被解锁",
		QOverturn:    "无旧判断被推翻",
		Note:         "",
		Dispatched:   1,
		Combined:     []string{"tc-ab12cd34"},
	}
	mustAppend(t, s, task, Event{Kind: EventOvermindRecord, Actor: "overmind", Payload: rec})

	// 受控入口:未知类型 / 五问与 Note 全空(无信息量噪音),一律拒。
	bad := []Event{
		{Kind: EventOvermindRecord, Actor: "overmind",
			Payload: OvermindRecord{Type: "musing"}},
		{Kind: EventOvermindRecord, Actor: "overmind",
			Payload: OvermindRecord{Type: OvermindRecAssessment, Dispatched: 3}},
		{Kind: EventOvermindRecord, Actor: "overmind",
			Payload: OvermindRecord{Type: OvermindRecAssessment,
				QAccess: "  ", QPrivilege: "", QCombination: "\t", QUnlock: "", QOverturn: "", Note: " "}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法 overmind 记录 #%d 应被拒", i+1)
		}
	}

	// 不进双图投影(审计流)。
	pg, err := s.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查询进度图失败: %v", err)
	}
	if len(pg.Steps) != 1 {
		t.Fatalf("overmind.record 不得进进度图: %+v", pg.Steps)
	}

	// 负载往返 + actor + 重放确定性。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查询事件流失败: %v", err)
	}
	var got OvermindRecord
	if err := json.Unmarshal(evs[2].Payload, &got); err != nil {
		t.Fatalf("解码研判记录: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("overmind.record 负载往返不符:\n%+v\n%+v", got, rec)
	}
	if evs[2].Actor != "overmind" {
		t.Fatalf("actor 不符: %q", evs[2].Actor)
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败(含 overmind.record): %v", err)
	}
}

func TestReplayRecordValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-replay"

	mustAppend(t, s, task,
		na(NodeAsset, "web1", "http://mock", "active", AssetService, nil),
		na(NodeTestClaim, "tc-1", "用户接口未授权可读", TestClaimConfirmed, "", map[string]any{
			"unit_id": "u1", "impact": "可读全部用户数据"}),
		ea(EdgeAbout, "tc-1", "web1"),
		// 绑定证据:一条 http 留痕(blob 引用非空)。
		Event{Kind: EventMirrorRecord, Actor: "mirror", Payload: MirrorRecord{
			ExchangeID: "ms-aaa1", Type: MirrorRecHTTP, StepID: "s1", UnitID: "u1",
			Method: "GET", URL: "http://mock/api/user", Status: 200, BlobRef: "sha256:aa"}},
		Event{Kind: EventTestClaimRecord, Actor: "creator",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecEvidence,
				Exchanges: []string{"ms-aaa1"}, By: "creator"}},
	)

	// 合法:绑定交换的重放结论(锚 + 结论词 + 事实)。
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
		Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "replay-tc-1-r1",
			UnitID: "rp-x1", Type: TestClaimRecReplay, Round: 1,
			ReplayVerdict: ReplayVerdictReproduced,
			ReplayResults: []ReplayResult{{
				ExchangeID: "ms-aaa1", NewExchangeID: "ms-bbb1",
				Method: "GET", URL: "http://mock/api/user",
				OldStatus: 200, NewStatus: 200, BodySame: true, OldBytes: 40, NewBytes: 40,
			}}}})

	// 非法:结论词超词汇 / 缺锚 / 缺事实 / 未绑定交换 / 重复交换 / 非声明节点。
	bad := []Event{
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "replay-tc-1-r2",
				Type: TestClaimRecReplay, ReplayVerdict: "maybe",
				ReplayResults: []ReplayResult{{ExchangeID: "ms-aaa1"}}}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1",
				Type: TestClaimRecReplay, ReplayVerdict: ReplayVerdictDiverged,
				ReplayResults: []ReplayResult{{ExchangeID: "ms-aaa1"}}}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "replay-tc-1-r2",
				Type: TestClaimRecReplay, ReplayVerdict: ReplayVerdictDiverged}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "replay-tc-1-r2",
				Type: TestClaimRecReplay, ReplayVerdict: ReplayVerdictDiverged,
				ReplayResults: []ReplayResult{{ExchangeID: "ms-notbound"}}}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "tc-1", StepID: "replay-tc-1-r2",
				Type: TestClaimRecReplay, ReplayVerdict: ReplayVerdictDiverged,
				ReplayResults: []ReplayResult{{ExchangeID: "ms-aaa1"}, {ExchangeID: "ms-aaa1"}}}},
		{Kind: EventTestClaimRecord, Actor: "swarm:rp-x1",
			Payload: TestClaimRecord{ClaimID: "web1", StepID: "replay-tc-1-r2",
				Type: TestClaimRecReplay, ReplayVerdict: ReplayVerdictDiverged,
				ReplayResults: []ReplayResult{{ExchangeID: "ms-aaa1"}}}},
	}
	for i, ev := range bad {
		if _, err := s.Append(ctx, task, ev); err == nil {
			t.Fatalf("非法 replay 记录 #%d 应被拒", i+1)
		}
	}

	// 负载往返 + 不进双图投影 + 重放确定性。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var got TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != EventTestClaimRecord {
			continue
		}
		var r TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == TestClaimRecReplay {
			got = r
		}
	}
	if got.ReplayVerdict != ReplayVerdictReproduced || got.Round != 1 ||
		got.StepID != "replay-tc-1-r1" || len(got.ReplayResults) != 1 ||
		got.ReplayResults[0].NewExchangeID != "ms-bbb1" {
		t.Fatalf("replay 记录负载往返不符: %+v", got)
	}
	pg, _ := s.ProgressGraph(ctx, task)
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放重建失败: %v", err)
	}
	after, _ := s.ProgressGraph(ctx, task)
	if !reflect.DeepEqual(pg, after) {
		t.Fatalf("重放后进度图漂移")
	}
}

// TestHibernateDeduct P4-02 ④:冬眠墙钟合计(面板会话时长冻结口径)——
// 已闭合区间求和 + 进行中的开口区间截至查询时刻;与 HibernateCredit
// 的分工:开口区间此处计入(冬眠中就要冻结),那边不计(醒后抵扣)。
func TestHibernateDeduct(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	const task = "deduct-task"
	base := time.Now().UTC().Add(-2 * time.Hour)
	seed := func(at time.Time, action string) {
		t.Helper()
		if _, err := store.Append(ctx, task, Event{
			Kind: EventTaskLifecycle, Actor: "manager", OccurredAt: at,
			Payload: TaskLifecycle{Action: action, Title: "t"},
		}); err != nil {
			t.Fatalf("造 %s 失败: %v", action, err)
		}
	}
	seed(base, "created")
	seed(base.Add(10*time.Minute), "hibernated")
	seed(base.Add(40*time.Minute), "started") // 闭合区间 1:30 分钟
	seed(base.Add(60*time.Minute), "hibernated")
	// 当前仍在冬眠:开口区间 = base+60m → now。

	now := base.Add(75 * time.Minute)
	if got := store.HibernateDeduct(ctx, task, now); got != 45*time.Minute {
		t.Fatalf("抵扣应为 30m 闭合 + 15m 开口 = 45m: %v", got)
	}
	// 与 HibernateCredit 分工:开口不计,只 30m。
	if got, err := store.HibernateCredit(ctx, task); err != nil || got != 30*time.Minute {
		t.Fatalf("Credit 应只计闭合 30m: %v (%v)", got, err)
	}
	// 唤醒后:全闭合,两者一致。
	seed(base.Add(90*time.Minute), "started")
	if got := store.HibernateDeduct(ctx, task, base.Add(95*time.Minute)); got != 60*time.Minute {
		t.Fatalf("唤醒后抵扣应为 30m+30m=60m: %v", got)
	}
	// 无冬眠任务:0。
	if got := store.HibernateDeduct(ctx, "deduct-none", time.Now()); got != 0 {
		t.Fatalf("无冬眠任务应为 0: %v", got)
	}
}
