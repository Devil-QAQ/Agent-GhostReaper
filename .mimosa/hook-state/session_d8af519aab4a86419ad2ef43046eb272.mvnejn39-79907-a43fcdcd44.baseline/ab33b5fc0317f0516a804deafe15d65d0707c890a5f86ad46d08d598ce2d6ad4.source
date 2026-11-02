package overmind

import (
	"context"
	"testing"
	"time"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// TestLoadDeadlineAppliesHibernateCredit 重启后拉起的新循环装载收尾线时
// 不丢冬眠时长抵扣(P2-08 审核迭代修复的回归锚点):历史冬眠 40 分钟、
// 基础线只剩 10 分钟——按原始墙钟已过线会误收尾,装载后应顺延到未来。
func TestLoadDeadlineAppliesHibernateCredit(t *testing.T) {
	store := openStore(t)
	const task = "credit-task"
	ctx := context.Background()
	created := time.Now().Add(-time.Hour) // 一小时前建任务
	appendAt := func(at time.Time, ev rings.Event) {
		t.Helper()
		ev.OccurredAt = at
		if _, err := store.Append(ctx, task, ev); err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	appendAt(created, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "抵扣回归",
			Detail: map[string]any{"timeout_seconds": 3600}},
	})
	appendAt(created.Add(5*time.Minute), rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "hibernated"},
	})
	appendAt(created.Add(45*time.Minute), rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "started", Detail: map[string]any{"woken": true}},
	})

	l := &Loop{TaskID: task, Store: store, Hiber: hiber.New()}
	l.loadDeadline(ctx)
	base := l.currentDeadline()
	// 基础线(created+600s)已过,顺延后(40 分钟抵扣)应在未来。
	if !time.Now().Before(base) {
		t.Fatalf("装载应收尾线顺延到未来(基础线 + 冬眠抵扣),实际 %v", base)
	}
	credit, _ := store.HibernateCredit(ctx, task)
	if credit < 39*time.Minute {
		t.Fatalf("账本冬眠时长应 ≥39 分钟: %v", credit)
	}
}
