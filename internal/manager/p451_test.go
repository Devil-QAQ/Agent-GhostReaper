package manager

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP451SweepScopeCardsAtTerminal 终局清点收口遗留范围卡(P4-51:
// 任务终局无裁决通道,未决范围卡按拒绝结案——红点不留僵尸;已裁决的
// 卡不重复结案)。
func TestP451SweepScopeCardsAtTerminal(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	if _, err := mgr.CreateTask(context.Background(), "sw-p451", "范围卡终局结案", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedSweepGraph(t, store, "sw-p451")
	ctx := context.Background()
	// 一张未决卡 + 一张已批准卡(终局只结未决的)。
	if _, err := store.Append(ctx, "sw-p451",
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "sc-open.example.com", StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash", Intent: "探测",
		}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "sc-done.example.org", StepID: "s2", UnitID: "u2",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash", Intent: "探测",
		}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: "sc-done.example.org", StepID: "s2", UnitID: "u2",
			Type: rings.ApprovalRecApproved, ToolName: "bash", Intent: "探测", Reason: "加入资产",
		}},
	); err != nil {
		t.Fatal(err)
	}
	if err := mgr.sweepAtTerminal("sw-p451", StatusStopped); err != nil {
		t.Fatalf("清点失败: %v", err)
	}
	rows, err := store.ApprovalRows(ctx, "sw-p451")
	if err != nil {
		t.Fatal(err)
	}
	latest := map[string]string{}
	for _, r := range rows {
		latest[r.RequestID] = r.Type
	}
	if latest["sc-open.example.com"] != rings.ApprovalRecDenied {
		t.Fatalf("未决卡应按拒绝结案: %+v", latest)
	}
	if latest["sc-done.example.org"] != rings.ApprovalRecApproved {
		t.Fatalf("已裁决卡不得重复结案: %+v", latest)
	}
	// 结案理由与拒绝线可溯。
	for _, r := range rows {
		if r.RequestID == "sc-open.example.com" && r.Type == rings.ApprovalRecDenied {
			if r.DenyLine != rings.DenyLineCloseout || r.Reason == "" {
				t.Fatalf("结案记录不符: %+v", r)
			}
		}
	}
	// 幂等:重复清点零追加。
	if err := mgr.sweepAtTerminal("sw-p451", StatusStopped); err != nil {
		t.Fatalf("重复清点失败: %v", err)
	}
	rows2, _ := store.ApprovalRows(ctx, "sw-p451")
	if len(rows2) != len(rows) {
		t.Fatalf("重复清点应零追加: %d -> %d", len(rows), len(rows2))
	}
}
