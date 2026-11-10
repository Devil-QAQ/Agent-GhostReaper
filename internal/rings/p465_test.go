package rings

import (
	"context"
	"strings"
	"testing"
)

// P4-65(问题清单 #82 A⑨⑩)界面层包装族的账本与投影链路类级测试:
//   - overmind.record_polish:物理校验拒空(缺锚定版本/六字段全空)、
//     合法负载落账、不进 nodes 投影(纯审计流侧表);
//   - step.title:物理校验拒空(缺步骤/空标题)、node_polish title 列
//     latest-wins、与 fact.text 共表互不串列、步骤节点原文一字不动;
//   - 重放确定性与存量兼容(无新事件零变化)。

func p465SeedStep(t *testing.T, s *Store, task, stepID, title string) {
	t.Helper()
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
		NodeID: stepID, NodeType: NodeStep, Title: title, Status: "dispatched",
		Detail: map[string]any{"action_level": "只读探测"},
	}})
}

func TestP465OvermindPolishValidate(t *testing.T) {
	s := openTestStore(t)
	const task = "p465-om-val"
	p465SeedStep(t, s, task, "s1", "对 10.0.0.5:8080 做目录枚举")
	// 物理校验拒空(多形态):缺锚定版本、六字段全空、RecRev 非正。
	for _, bad := range []OvermindPolish{
		{QAccess: "有内容但缺锚定版本"},
		{RecRev: 7},
		{RecRev: -1, Note: "负版本"},
	} {
		if _, err := s.Append(ctxOf(), task, Event{Kind: EventOvermindPolish, Actor: ActorOvermindSafe, Payload: bad}); err == nil {
			t.Fatalf("空负载应拒收: %+v", bad)
		}
	}
	// 合法负载(部分字段)落账;重放同序再校验一致(纯校验无投影,
	// 重放确定性=同序事件同结果)。
	if _, err := s.Append(ctxOf(), task, Event{Kind: EventOvermindPolish, Actor: ActorOvermindSafe,
		Payload: OvermindPolish{RecRev: 9, QAccess: "凭口令可以登录 10.0.0.5:8080 的管理后台", Note: "下一轮验证通用性"}}); err != nil {
		t.Fatalf("合法负载应落账: %v", err)
	}
	// 不进 nodes/edges 投影(审计流侧表,同 overmind.record)。
	if nodes, err := s.NodesByType(context.Background(), task, NodeStep, ""); err != nil || len(nodes) != 1 {
		t.Fatalf("步骤投影应不受影响: %d %v", len(nodes), err)
	}
}

func TestP465StepTitleValidate(t *testing.T) {
	s := openTestStore(t)
	const task = "p465-st-val"
	p465SeedStep(t, s, task, "s1", "对 10.0.0.5:8080 做目录枚举")
	for _, bad := range []StepTitlePolish{
		{StepID: "s1"},
		{Title: "有标题但缺步骤"},
		{StepID: "s1", Title: "   "},
	} {
		if _, err := s.Append(ctxOf(), task, Event{Kind: EventStepTitle, Actor: ActorOvermindSafe, Payload: bad}); err == nil {
			t.Fatalf("空负载应拒收: %+v", bad)
		}
	}
}

func TestP465StepTitleNodePolish(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p465-st-pol"
	p465SeedStep(t, s, task, "s1", "对 10.0.0.5:8080 做目录枚举的原始意图原文")
	mustAppend(t, s, task,
		Event{Kind: EventStepTitle, Actor: ActorOvermindSafe,
			Payload: StepTitlePolish{StepID: "s1", Title: "对 10.0.0.5:8080 做目录枚举,找未授权入口"}},
		Event{Kind: EventFactText, Actor: ActorOvermindSafe,
			Payload: FactTextPolish{FactID: "f-1", Title: "10.0.0.5 的备份无需登录即可下载", Statement: "描述"}},
	)
	np, err := s.NodePolish(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if np["s1"].Title != "对 10.0.0.5:8080 做目录枚举,找未授权入口" || np["s1"].Statement != "" {
		t.Fatalf("步骤行只占 title 列: %+v", np["s1"])
	}
	if np["f-1"].Statement != "描述" || np["f-1"].Title != "10.0.0.5 的备份无需登录即可下载" {
		t.Fatalf("事实行不应被步骤事件串列: %+v", np["f-1"])
	}
	// 节点原文一字不动(机器消费面)。
	n, err := s.Node(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "对 10.0.0.5:8080 做目录枚举的原始意图原文" {
		t.Fatalf("步骤节点标题应保持原文: %q", n.Title)
	}
	// latest-wins:同步骤新校对稿覆盖旧稿。
	mustAppend(t, s, task, Event{Kind: EventStepTitle, Actor: ActorOvermindSafe,
		Payload: StepTitlePolish{StepID: "s1", Title: "对 10.0.0.5:8080 重做目录枚举,确认未授权入口"}})
	np, err = s.NodePolish(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(np["s1"].Title, "重做目录枚举") {
		t.Fatalf("latest-wins 应覆盖: %+v", np["s1"])
	}
}

func TestP465StockCompat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p465-stock"
	p465SeedStep(t, s, task, "s1", "存量步骤意图原文")
	np, err := s.NodePolish(ctx, task)
	if err != nil || len(np) != 0 {
		t.Fatalf("存量无包装事件零变化: %d %v", len(np), err)
	}
}
