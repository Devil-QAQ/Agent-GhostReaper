package rings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---- P4-48(问题清单 #67):操作者深入指令事件 ----
// 落账与投影、空锚定节点拒收、投影重放确定性。

func TestOperatorDiveEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p448-rings"

	if _, err := s.Append(ctx, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "created"}}); err != nil {
		t.Fatal(err)
	}
	revs, err := s.Append(ctx, task,
		Event{Kind: EventOperatorDive, Actor: "creator", Payload: OperatorDive{NodeID: "f-1", Direction: "继续暴力破解"}},
		Event{Kind: EventOperatorDive, Actor: "creator", Payload: OperatorDive{NodeID: "tc-2"}},
	)
	if err != nil {
		t.Fatalf("深入指令落账失败: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("应落两条事件,实际 %d", len(revs))
	}
	// 空锚定节点整批拒收(结构校验)。
	if _, err := s.Append(ctx, task, Event{Kind: EventOperatorDive, Actor: "creator",
		Payload: OperatorDive{Direction: "无节点"}}); err == nil || !strings.Contains(err.Error(), "锚定节点") {
		t.Fatalf("空锚定节点应拒收: %v", err)
	}
	// 事件流可读回(审计面),负载原样。
	evs, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var dives int
	for _, ev := range evs {
		if ev.Kind != EventOperatorDive {
			continue
		}
		dives++
		var p OperatorDive
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if ev.Actor != "creator" {
			t.Fatalf("actor 应为 creator: %s", ev.Actor)
		}
	}
	if dives != 2 {
		t.Fatalf("事件流应恰 2 条深入指令,实际 %d", dives)
	}
	// 投影重放确定性(指令是纯审计流,重放不改变投影)。
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("投影重放失败: %v", err)
	}
	evs2, err := s.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs2) != len(evs) {
		t.Fatalf("重放后事件数变化: %d → %d", len(evs), len(evs2))
	}
}
