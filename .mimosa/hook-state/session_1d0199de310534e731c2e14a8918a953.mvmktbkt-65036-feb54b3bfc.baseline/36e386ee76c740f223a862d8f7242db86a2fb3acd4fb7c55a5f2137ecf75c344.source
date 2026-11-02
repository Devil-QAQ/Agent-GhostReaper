package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-05 心跳巡检:600s 机器预筛(停滞/重复/预算异常)→ 黑匣子 ----

// blackboxRecords 解出任务的黑匣子记录。
func blackboxRecords(t *testing.T, store *rings.Store, task string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 blackbox.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// seedRunningStep 种一个在跑步骤:step(running)+ 会话启动记录(可指定
// 发生时刻)。prescreen 的确定性单测用,不经引擎。
func seedRunningStep(t *testing.T, store *rings.Store, task, stepID, unitID string, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "探测目标", Status: "running",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unitID,
			OccurredAt: startedAt,
			Payload: rings.SwarmRecord{
				StepID: stepID, UnitID: unitID, TranscriptID: "swarm-" + unitID,
				Type: rings.SwarmRecSessionStarted,
			}},
	); err != nil {
		t.Fatalf("种在跑步骤失败: %v", err)
	}
}

// seedToolCall 种一条工具调用记录(可指定发生时刻与参数)。
func seedToolCall(t *testing.T, store *rings.Store, task, stepID, unitID, args string, at time.Time) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:" + unitID, OccurredAt: at,
		Payload: rings.SwarmRecord{
			StepID: stepID, UnitID: unitID, Type: rings.SwarmRecToolCall,
			ToolName: "port_probe", ToolArgs: json.RawMessage(args),
		},
	}); err != nil {
		t.Fatalf("种工具调用失败: %v", err)
	}
}

// hasType 黑匣子里是否存在某类型记录。
func hasType(recs []rings.BlackboxRecord, typ string) bool {
	for _, r := range recs {
		if r.Type == typ {
			return true
		}
	}
	return false
}

// openLoopStore 测试用账本。
func openLoopStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(t.TempDir() + "/rings.db")
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPrescreenDetectors 机器预筛三类发现各自命中、健康路径零发现:
// 停滞(超窗无工具调用)、重复(末尾连续相同调用)、预算异常(单拍
// token 超阈)。全部确定性判定,零 LLM。
func TestPrescreenDetectors(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// ① 停滞:会话启动 400 秒前,零工具调用,窗口 300 秒。
	t.Run("停滞", func(t *testing.T) {
		store := openLoopStore(t)
		const task = "hb-stall"
		seedRunningStep(t, store, task, "s1", "u1", now.Add(-400*time.Second))
		l := &Loop{TaskID: task, Store: store} // 默认窗口 300s
		findings, err := l.prescreen(ctx)
		if err != nil {
			t.Fatalf("预筛失败: %v", err)
		}
		if len(findings) != 1 || findings[0].Type != rings.BlackboxHbStall ||
			findings[0].StepID != "s1" || findings[0].UnitID != "u1" {
			t.Fatalf("应命中一条停滞发现: %+v", findings)
		}
	})

	// ② 重复动作:末尾连续 3 次相同调用(近时,不触停滞)。
	t.Run("重复动作", func(t *testing.T) {
		store := openLoopStore(t)
		const task = "hb-repeat"
		seedRunningStep(t, store, task, "s1", "u1", now.Add(-10*time.Second))
		args := `{"host":"127.0.0.1","port":8080}`
		seedToolCall(t, store, task, "s1", "u1", args, now.Add(-6*time.Second))
		seedToolCall(t, store, task, "s1", "u1", args, now.Add(-4*time.Second))
		seedToolCall(t, store, task, "s1", "u1", args, now.Add(-2*time.Second))
		l := &Loop{TaskID: task, Store: store}
		findings, err := l.prescreen(ctx)
		if err != nil {
			t.Fatalf("预筛失败: %v", err)
		}
		if len(findings) != 1 || findings[0].Type != rings.BlackboxHbRepeat {
			t.Fatalf("应命中一条重复动作发现: %+v", findings)
		}
		if n, _ := findings[0].Detail["repeat"].(int); n != 3 {
			t.Fatalf("重复计数应为 3: %+v", findings[0].Detail)
		}
	})

	// ③ 预算消耗异常:单拍窗口 token 超阈(阈值压小到 1000)。
	t.Run("预算消耗异常", func(t *testing.T) {
		store := openLoopStore(t)
		const task = "hb-burn"
		seedRunningStep(t, store, task, "s1", "u1", now.Add(-10*time.Second))
		seedToolCall(t, store, task, "s1", "u1", `{"host":"127.0.0.1","port":1}`, now.Add(-2*time.Second))
		if err := store.RecordTokens(ctx, rings.TokenUsage{
			TaskID: task, Agent: "swarm:u1", Model: "m", Turn: 1,
			InputTokens: 900, OutputTokens: 200, OccurredAt: now,
		}); err != nil {
			t.Fatalf("记 token 失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, BurnAnomalyTokens: 1000}
		findings, err := l.prescreen(ctx)
		if err != nil {
			t.Fatalf("预筛失败: %v", err)
		}
		if len(findings) != 1 || findings[0].Type != rings.BlackboxHbBurnAnomaly {
			t.Fatalf("应命中一条消耗异常发现: %+v", findings)
		}
	})

	// ④ 健康路径:近期有 varied 调用、消耗正常 → 零发现。
	t.Run("健康零发现", func(t *testing.T) {
		store := openLoopStore(t)
		const task = "hb-clean"
		seedRunningStep(t, store, task, "s1", "u1", now.Add(-10*time.Second))
		seedToolCall(t, store, task, "s1", "u1", `{"host":"127.0.0.1","port":1}`, now.Add(-5*time.Second))
		seedToolCall(t, store, task, "s1", "u1", `{"host":"127.0.0.1","port":2}`, now.Add(-2*time.Second))
		if err := store.RecordTokens(ctx, rings.TokenUsage{
			TaskID: task, Agent: "swarm:u1", Model: "m", Turn: 1,
			InputTokens: 100, OutputTokens: 50, OccurredAt: now,
		}); err != nil {
			t.Fatalf("记 token 失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, BurnAnomalyTokens: 1000}
		findings, err := l.prescreen(ctx)
		if err != nil {
			t.Fatalf("预筛失败: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("健康路径应零发现: %+v", findings)
		}
	})
}

// TestHeartbeatMarksStallAndWakes 心跳对停滞任务的标记实测可见 + 有问题
// 才唤醒主宰:停滞步骤种入账本,Loop 心跳 80ms 一拍——黑匣子出现巡检拍
// 与停滞标记,且主宰被唤醒(拆解会话发生,快照含心跳标记节;无外部
// 唤醒,拆解只能来自 hbKick)。
func TestHeartbeatMarksStallAndWakes(t *testing.T) {
	store := openLoopStore(t)
	const task = "hb-wake"
	seedRunningStep(t, store, task, "s1", "u1", time.Now().Add(-5*time.Second))

	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	l := &Loop{
		TaskID: task, Store: store, Swarm: &swarm.Executor{}, Provider: prov,
		HeartbeatInterval: 80 * time.Millisecond, StallSeconds: 1,
	}
	go l.Run(ctx, wake)

	waitFor(t, 5*time.Second, "心跳拍与停滞标记入黑匣子", func() bool {
		recs := blackboxRecords(t, store, task)
		return hasType(recs, rings.BlackboxHeartbeat) && hasType(recs, rings.BlackboxHbStall)
	})
	// 唤醒证据:拆解请求发生,且快照带心跳标记节(主宰看得到为何被唤醒)。
	waitFor(t, 5*time.Second, "主宰被心跳唤醒(拆解发生)", func() bool {
		for _, rr := range model.requests() {
			if strings.Contains(rr.LastUser, "进度停滞") && strings.Contains(rr.LastUser, "心跳标记") {
				return true
			}
		}
		return false
	})
}

// TestHeartbeatWakesOnSchedule 到点必醒(P3-14a B):健康任务(无在跑
// 步骤、预筛零发现)心跳照拍(看门狗活性可见)且每拍无条件唤醒主宰——
// 拆解会话发生。旧语义"无事跳过省成本"废止(目标未完成不算任务结束,
// 空闲心跳成本造物主已确认接受)。
func TestHeartbeatWakesOnSchedule(t *testing.T) {
	store := openLoopStore(t)
	const task = "hb-clean-wake"
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "健康任务"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1) // 全程不投递:唯一唤醒源是心跳
	l := &Loop{
		TaskID: task, Store: store, Swarm: &swarm.Executor{}, Provider: prov,
		HeartbeatInterval: 60 * time.Millisecond,
	}
	go l.Run(ctx, wake)

	waitFor(t, 5*time.Second, "心跳拍入黑匣子", func() bool {
		return hasType(blackboxRecords(t, store, task), rings.BlackboxHeartbeat)
	})
	// 到点必醒:零发现也唤醒——拆解发生且随拍持续(多拍多次)。
	first := len(model.decomposeRequests())
	waitFor(t, 5*time.Second, "到点必醒(拆解发生)", func() bool {
		return len(model.decomposeRequests()) > first
	})
}

// TestHeartbeatWakesDeadWait P3-14 死等形态修复实测:目标未达成 + 零在跑/
// 待执行步骤 + awaiting_round 声明待派——事件驱动无唤醒源(旧预筛只查
// 在跑步骤,该形态永不触发),心跳到点必醒后主宰被唤醒研判,产出新方向
// (意图派发入图执行)。修复前该形态无限死等。
func TestHeartbeatWakesDeadWait(t *testing.T) {
	store := openLoopStore(t)
	const task = "hb-deadwait"
	host, port := seedTCPTarget(t, "banner\r\n")
	hostPort := net.JoinHostPort(host, strconv.Itoa(port))
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "死等形态"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "摸清暴露面", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: hostPort, Status: "active",
			AssetClass: rings.AssetHost}},
		// P3-14 死等形态的第三要素:声明停在 awaiting_round 待派下一轮反证。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u0", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "待下轮反证声明",
			Status: rings.TestClaimAwaitingRound,
			Detail: map[string]any{"unit_id": "u0", "step_id": "s0"}}},
	); err != nil {
		t.Fatalf("种死等形态失败: %v", err)
	}

	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			// 图上已有步骤即不再派发(防永续);首轮按硬底线产出新方向。
			if !strings.Contains(rr.LastUser, "步骤:\n  (无)") {
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
			return scriptedReply{Text: fmt.Sprintf(
				`{"new_intents":[{"intent":"探测 %s 的暴露面","expected_observation":"端口状态","action_level":"只读探测","covers":["a1"]}]}`,
				hostPort), Finish: "stop"}
		}
		return scriptedReply{Text: "意图完成。", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1) // 全程不投递:唯一唤醒源是心跳
	l := &Loop{
		TaskID: task, Store: store, Swarm: swarm.NewExecutor(store, prov, t.TempDir(), swarm.Budget{}),
		Provider: prov, HeartbeatInterval: 80 * time.Millisecond,
	}
	go l.Run(ctx, wake)

	// 无任何外部唤醒:仅凭心跳,新方向被产出并派发执行到 done。
	waitFor(t, 10*time.Second, "心跳唤醒主宰并派发执行新方向", func() bool {
		return countNodes(t, store, task, rings.NodeStep, "done") >= 1
	})
	// 唤醒证据:拆解请求发生且快照含 awaiting_round 声明(主宰看到了死等现场)。
	seen := false
	for _, rr := range model.decomposeRequests() {
		if strings.Contains(rr.LastUser, "tc-1 [awaiting_round]") {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("拆解快照应含 awaiting_round 声明")
	}
}
