package overmind

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// These are synthetic input states for decision tests, not autonomous findings.
func workReviewFixture(t *testing.T, scenario string) (*Loop, map[string]rings.ExecutionOwner) {
	t.Helper()
	store := openLoopStore(t)
	task := "review-" + scenario
	title := "按既定120项清单分批核对本地材料，复用已完成项的实际记录"
	fact := "本次本地材料校验总计120项，第一批已实际完成49项，结果已确认；剩余71项正在按同一清单分批处理，没有重复检查已完成项"
	kind := rings.BlackboxHbBurnAnomaly
	if scenario == "repeat" {
		title = "按既定20项材料清单逐项核对本地文件，复用已取得结果"
		fact = "已确认20项材料清单，其中不存在missing.txt；其他材料仍可读取"
		kind = rings.BlackboxHbRepeat
	}
	if scenario == "invalid" {
		title = "仅核对唯一指定文件；该文件必须存在才能继续"
		fact = "操作者确认唯一指定文件已永久撤回，不恢复、不替换，也不调查其他材料"
		kind = rings.BlackboxHbStall
	}
	_, err := store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "人工合成执行复查案例"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "g1", NodeType: rings.NodeGoal, Title: "核对指定本地材料", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, AssetClass: rings.AssetDomain, Title: "fixture.example.test", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: title, Status: "running", Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionStarted, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "f-progress", NodeType: rings.NodeFact, Title: fact, Status: "confirmed", Detail: map[string]any{"unit_id": "u1", "evidence": "人工合成固定案例"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-progress"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "repeat" {
		for i := 0; i < 3; i++ {
			_, err = store.Append(t.Context(), task, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecToolCall, StepID: "s1", UnitID: "u1", ToolCallID: string(rune('a' + i)), ToolName: "read", ToolArgs: json.RawMessage(`{"path":"missing.txt"}`), IsError: true, ResultHead: "文件不存在；本次没有新内容，与前一次相同"}})
			if err != nil {
				t.Fatal(err)
			}
		}
	} else if scenario == "progress" {
		_, err = store.Append(t.Context(), task, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecToolCall, StepID: "s1", UnitID: "u1", ToolCallID: "batch-one", ToolName: "bash", ToolArgs: json.RawMessage(`{"command":"python3 verify_materials.py --batch 1 --total 120"}`), ResultHead: "第一批固定材料校验完成：49/120项，原始结果已写回f-progress；没有错误"}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Append(t.Context(), task, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecToolStarted, StepID: "s1", UnitID: "u1", ToolCallID: "long-local", ToolName: "bash", ToolArgs: json.RawMessage(`{"command":"python3 verify_materials.py --batch 2 --total 120"}`)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.Append(t.Context(), task, rings.Event{Kind: rings.EventBlackboxRecord, Actor: ActorOvermind, Payload: rings.BlackboxRecord{Type: kind, StepID: "s1", UnitID: "u1", Detail: map[string]any{"burned_tokens": 800000, "threshold": 400000, "repeat": 3, "tool": "read", "args": "missing.txt", "silent_seconds": 600}}})
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store, Swarm: swarm.NewExecutor(store, nil, filepath.Join(t.TempDir(), "tasks"), swarm.Budget{}), RootDir: t.TempDir()}
	_, _, _, owners, err := l.snapshotDetails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return l, owners
}

func TestWorkReviewSnapshotKeepsCurrentStatusAndProgress(t *testing.T) {
	l, owners := workReviewFixture(t, "progress")
	snapshot, rev, _, _, err := l.snapshotDetails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "unit_id=u1") || !strings.Contains(snapshot, "工具正在执行=bash") || !strings.Contains(snapshot, "已确认1条") {
		t.Fatal("review omitted actual execution/progress")
	}
	triggers, _, err := l.planningTriggers(t.Context())
	if err != nil || len(triggers) == 0 {
		t.Fatal("alert is not a planning trigger")
	}
	due, err := l.workReviewsDue(t.Context(), owners, rev)
	if err != nil || !due["s1"] {
		t.Fatal("current alert was omitted")
	}
	if err := validateWorkReviews(nil, owners, due); err == nil {
		t.Fatal("missing review counted as handled")
	}
	plan := workControlPlan{StepID: "s1", UnitID: "u1", Action: "continue", Reason: "f-progress显示有已确认新进展，长工具仍正常执行"}
	if err := validateWorkReviews([]workControlPlan{plan}, owners, due); err != nil {
		t.Fatal(err)
	}
	if err := l.applyWorkControl(t.Context(), plan, owners, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Store.Append(t.Context(), l.TaskID, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, _, _, _, err = l.snapshotDetails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot, "s1 [running]") || !strings.Contains(snapshot, "s1 [done]") {
		t.Fatal("historical alert still labels a completed step running")
	}
}

func TestWorkReviewQueueAndReceiptAreDistinct(t *testing.T) {
	l, owners := workReviewFixture(t, "repeat")
	rev, _ := l.Store.CurrentRevision(t.Context(), l.TaskID)
	plan := workControlPlan{StepID: "s1", UnitID: "u1", Action: "steer", Reason: "已确认missing.txt不存在，连续相同错误无新结果", Message: "停止重复读取已确认不存在的文件，按已有20项清单核对剩余本地材料；不扩大当前意图或授权"}
	if err := l.applyWorkControl(t.Context(), plan, owners, rev); err != nil {
		t.Fatal(err)
	}
	text, _, _, _, err := l.snapshotDetails(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "当前回执=queued") || strings.Contains(text, "当前回执=adopted") {
		t.Fatal("queued steer advertised as applied")
	}
	plan.UnitID = "old-unit"
	if err := l.applyWorkControl(t.Context(), plan, owners, rev); err == nil {
		t.Fatal("control accepted another execution")
	}
	plan.UnitID = "u1"
	if err := validateWorkReviews([]workControlPlan{plan, plan}, owners, map[string]bool{"s1": true}); err == nil {
		t.Fatal("duplicate controls accepted in one round")
	}
}

func TestHeartbeatResetsOldExecutionTrace(t *testing.T) {
	l, _ := workReviewFixture(t, "repeat")
	_, err := l.Store.Append(t.Context(), l.TaskID, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionStarted, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}}, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecToolCall, StepID: "s1", UnitID: "u1", ToolName: "read", ToolArgs: json.RawMessage(`{"path":"missing.txt"}`), IsError: true, ResultHead: "旧执行迟到结果"}})
	if err != nil {
		t.Fatal(err)
	}
	l.StallSeconds = 1
	findings, err := l.prescreen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.UnitID == "u2" && (f.Type == rings.BlackboxHbRepeat || f.Type == rings.BlackboxHbStall) {
			t.Fatal("new execution inherited old repetition or silence")
		}
	}
}

func TestWorkReviewUsesResumedExecutionNotTranscriptAggregate(t *testing.T) {
	l, _ := workReviewFixture(t, "repeat")
	_, err := l.Store.Append(t.Context(), l.TaskID,
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionEnd, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1", Note: "reason=checkpointed status=dispatched"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{Type: rings.SwarmRecSessionStarted, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, owners, err := l.snapshotDetails(t.Context())
	if err != nil || owners["s1"].UnitID != "u2" || owners["s1"].TranscriptID != "swarm-u1" || !strings.Contains(text, "unit_id=u2") {
		t.Fatalf("resumed owner omitted: %+v %v", owners, err)
	}
	owner, err := l.Store.CurrentExecutionOwner(t.Context(), l.TaskID, "s1")
	if err != nil || owner != owners["s1"] {
		t.Fatalf("operator/model execution mismatch: %+v %+v %v", owner, owners["s1"], err)
	}
}
