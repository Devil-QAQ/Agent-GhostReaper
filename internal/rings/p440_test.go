package rings

// P4-40 ⑥ 特权清除的行清零对账:该任务在库内一切带 task_id 的表行
// 全部删除(events/nodes/edges/tasks/roe_state/token_usage 与 P4-36
// 读侧投影表),其他任务不受影响;无行再清幂等返回 0。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPurgeTaskClearsAllTaskRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 任务 t-a(清除对象):created + 目标节点 + RoE 变更 + token 行,
	// events/tasks/nodes/roe_state/token_usage 各有行。
	mustAppend(t, s, "t-a",
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "created", Title: "清除对象"}},
		na(NodeGoal, "g1", "拿到管理权限", "", "", nil),
		Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{Note: "初始约束"}},
	)
	if err := s.RecordTokens(ctx, TokenUsage{TaskID: "t-a", Agent: "swarm:u1", InputTokens: 12}); err != nil {
		t.Fatalf("写入 token 行失败: %v", err)
	}
	// 任务 t-b(对照,不受影响)。
	mustAppend(t, s, "t-b", Event{Kind: EventTaskLifecycle, Actor: "manager",
		Payload: TaskLifecycle{Action: "created", Title: "对照"}})

	rows, err := s.PurgeTask(ctx, "t-a")
	if err != nil {
		t.Fatalf("特权清除失败: %v", err)
	}
	// 至少:3 事件 + 1 任务行 + 1 目标节点 + 1 RoE 行 + 1 token 行。
	if rows < 5 {
		t.Fatalf("删除行数 %d 少于预期(至少 5)", rows)
	}
	for _, table := range []string{
		"events", "nodes", "edges", "tasks", "roe_state", "token_usage",
		"unit_sessions", "session_labels", "approval_records", "claim_cases",
		"hyp_states", "task_extras", "closure_states", "claim_qa", "mirror_briefs",
	} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE task_id = 't-a'`).Scan(&n); err != nil {
			t.Fatalf("统计 %s 失败: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("特权清除后 %s 仍剩 %d 行", table, n)
		}
	}
	// 对照任务完好。
	if evs, err := s.Events(ctx, "t-b", 0, 0); err != nil || len(evs) != 1 {
		t.Fatalf("对照任务事件应保持 1 条: %v %d", err, len(evs))
	}
	if row, err := s.Task(ctx, "t-b"); err != nil || row.ID != "t-b" {
		t.Fatalf("对照任务行丢失: %v", err)
	}
	// 清除后任务不可见(读侧如实报不存在)。
	if _, err := s.Task(ctx, "t-a"); err == nil {
		t.Fatalf("清除对象仍可读")
	}
	// 幂等:无行再清返回 0。
	n2, err := s.PurgeTask(ctx, "t-a")
	if err != nil || n2 != 0 {
		t.Fatalf("重复清除应幂等返回 0: %d %v", n2, err)
	}
	// 拒收闸:清除后迟到的异步写入(终态总结等)不得让数据复活——
	// 事件拒收、token 行丢弃。
	if _, err := s.Append(ctx, "t-a", Event{Kind: EventTaskLifecycle, Actor: "manager",
		Payload: TaskLifecycle{Action: "stopped"}}); !errors.Is(err, ErrTaskPurged) {
		t.Fatalf("清除后追加应拒收 ErrTaskPurged: %v", err)
	}
	if err := s.RecordTokens(ctx,
		TokenUsage{TaskID: "t-a", Agent: "swarm:u1", InputTokens: 5},
		TokenUsage{TaskID: "t-b", Agent: "swarm:u1", InputTokens: 7},
	); err != nil {
		t.Fatalf("token 写入失败: %v", err)
	}
	var nA, nB int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM token_usage WHERE task_id = 't-a'`).Scan(&nA); err != nil {
		t.Fatalf("统计 t-a token 行失败: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM token_usage WHERE task_id = 't-b'`).Scan(&nB); err != nil {
		t.Fatalf("统计 t-b token 行失败: %v", err)
	}
	if nA != 0 || nB != 1 {
		t.Fatalf("清除任务的 token 行应丢弃、他任务照常: t-a=%d t-b=%d", nA, nB)
	}
}

// TestReferencedBlobHashes P4-40 续做 ①:全部任务事件负载里的 blob 引用
// 全集——跨任务收集、无引用行不误收、多任务同引用去重。
func TestReferencedBlobHashes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	h1 := strings.Repeat("a1", 32)
	h2 := strings.Repeat("b2", 32)
	mustAppend(t, s, "t-a",
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "created", Title: "带引用"}},
		Event{Kind: EventShepherdRecord, Actor: "swarm:u1",
			Payload: ShepherdRecord{MessageID: "sn-1", Type: ShepherdRecSystemNote,
				Text: "截头…全文见 sha256:" + h1}},
	)
	mustAppend(t, s, "t-b",
		Event{Kind: EventShepherdRecord, Actor: "swarm:u2",
			Payload: ShepherdRecord{MessageID: "sn-2", Type: ShepherdRecSystemNote,
				Text: "共享同一 blob sha256:" + h1 + " 与独占 sha256:" + h2}},
		Event{Kind: EventTaskLifecycle, Actor: "manager",
			Payload: TaskLifecycle{Action: "created", Title: "无引用事件"}},
	)
	got, err := s.ReferencedBlobHashes(ctx)
	if err != nil {
		t.Fatalf("扫引用失败: %v", err)
	}
	if len(got) != 2 || !got[h1] || !got[h2] {
		t.Fatalf("引用全集不符: %+v", got)
	}
}
