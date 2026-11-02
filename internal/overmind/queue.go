package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

const SettingKeyQueueLimit = "overmind.queue.limit"

// DefaultQueueLimit 派发准入上限默认值(P4-29):在跑+排队+等前置 ≥ 上限
// 时拒收新增普通探索。原 6(=执行 3+排队 3,起始验证值);2026-10-26 随
// 并发默认 3→5 同步上调为 8(=执行 5+排队 3),维持"排队缓冲 3"配比,
// 实测见 Process/P4进度.md P4-29 调优简记。
const DefaultQueueLimit = 8

// QueueWatermark 的排队数只计前置已完成的可领取步骤。
type QueueWatermark = rings.QueueWatermark

func QueueLimit(ctx context.Context, store *rings.Store) int {
	if n, ok := settingPositiveInt(ctx, store, SettingKeyQueueLimit); ok {
		return n
	}
	return DefaultQueueLimit
}

// RunnableSteps 与水位、调度共用前置闸；排序由领取比较器处理。
func RunnableSteps(dispatched, all []rings.NodeFull, edges []rings.EdgeView) []rings.NodeFull {
	status := map[string]string{}
	for _, s := range all {
		status[s.ID] = s.Status
	}
	blocked := map[string]bool{}
	for _, e := range edges {
		if e.Type == rings.EdgeDependsOn && status[e.To] != "done" {
			blocked[e.From] = true
		}
	}
	var out []rings.NodeFull
	for _, s := range dispatched {
		if !blocked[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

func QueueWatermarkOf(steps []rings.NodeFull, edges []rings.EdgeView, limit int) QueueWatermark {
	w := QueueWatermark{Limit: limit}
	var pending []rings.NodeFull
	for _, s := range steps {
		switch s.Status {
		case "running":
			w.Running++
		case "dispatched":
			pending = append(pending, s)
		}
	}
	w.Queued = len(RunnableSteps(pending, steps, edges))
	w.Waiting = len(pending) - w.Queued
	// 等前置的普通探索也预留容量，避免前置完成时集中转排队突破上限。
	// 等待数仍单列，不伪装成已可领取。
	w.Available = max(0, limit-w.Running-w.Queued-w.Waiting)
	return w
}

// queueView 重读到同一账本版本；写入仍须 AppendAtRevision 复检。
func queueView(ctx context.Context, store *rings.Store, task string) (int64, []rings.NodeFull, []rings.EdgeView, error) {
	for ctx.Err() == nil {
		rev, err := store.CurrentRevision(ctx, task)
		if err != nil {
			return 0, nil, nil, err
		}
		steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
		if err != nil {
			return 0, nil, nil, err
		}
		edges, err := store.Edges(ctx, task, rings.EdgeDependsOn)
		if err != nil {
			return 0, nil, nil, err
		}
		now, err := store.CurrentRevision(ctx, task)
		if err != nil {
			return 0, nil, nil, err
		}
		if rev == now {
			return rev, steps, edges, nil
		}
	}
	return 0, nil, nil, ctx.Err()
}

type QueueFullError struct{ Watermark QueueWatermark }

func (e *QueueFullError) Error() string {
	return "派发已满，等待单位结果后再规划：" + queueLine(e.Watermark)
}

// 来源由受控入口决定；高优先级、意图措辞不构成豁免。
type dispatchSource uint8

const (
	sourceExplore dispatchSource = iota
	sourceFalsify
	sourceVerify
	sourceUrgent
)

type cancelPlan struct {
	StepID   string   `json:"step_id"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
}

func protectedStep(s rings.NodeFull) bool {
	kind, _ := s.Detail["kind"].(string)
	urgent, _ := s.Detail["urgent"].(bool)
	return urgent || kind == "falsify" || kind == StepDetailKindVerify
}

// cancelQueued 只取消仍未领取的普通探索；整棵依赖后继同批留痕。
// 反证、验证、急活及它们的关键前置受保护。模型给取消理由与图上证据，
// 程序核对引用、状态、领取竞态和依赖闭包，不代替模型判断语义重复。
func (l *Loop) cancelQueued(ctx context.Context, p cancelPlan, evaluatedRev int64) error {
	if strings.TrimSpace(p.Reason) == "" || len(p.Evidence) == 0 {
		return fmt.Errorf("取消须注明理由与图上依据")
	}
	for _, id := range p.Evidence {
		n, err := l.Store.Node(ctx, l.TaskID, id)
		if err != nil || n.CreatedRev > evaluatedRev {
			return fmt.Errorf("取消依据 %s 不在本轮快照中", id)
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for ctx.Err() == nil {
		rev, steps, edges, err := queueView(ctx, l.Store, l.TaskID)
		if err != nil {
			return err
		}
		byID := map[string]rings.NodeFull{}
		for _, s := range steps {
			byID[s.ID] = s
		}
		root, ok := byID[p.StepID]
		if !ok || root.CreatedRev > evaluatedRev {
			return fmt.Errorf("取消步骤不在本轮快照中")
		}
		ids := map[string]bool{p.StepID: true}
		for changed := true; changed; {
			changed = false
			for _, e := range edges {
				if ids[e.To] && !ids[e.From] && byID[e.From].Status == "dispatched" {
					ids[e.From] = true
					changed = true
				}
			}
		}
		var evs []rings.Event
		for _, s := range steps {
			if !ids[s.ID] {
				continue
			}
			if s.Status != "dispatched" || protectedStep(s) {
				return fmt.Errorf("步骤 %s 已领取或属反证、验证、急活及其关键前置，保留", s.ID)
			}
			if _, flying := l.launching[s.ID]; flying {
				return fmt.Errorf("步骤 %s 已领取，保留", s.ID)
			}
			note := strings.TrimSpace(p.Reason)
			if s.ID != p.StepID {
				note = "前置「" + root.Title + "」已取消；" + note
			}
			evs = append(evs, rings.Event{Kind: rings.EventNodeStateChanged, Actor: ActorOvermind, Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "cancelled", Note: note, Evidence: p.Evidence}})
		}
		_, err = l.Store.AppendAtRevision(ctx, l.TaskID, rev, evs...)
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		return err
	}
	return ctx.Err()
}

func queueLine(w QueueWatermark) string {
	return fmt.Sprintf("执行中 %d / 排队 %d / 等前置 %d / 本轮还可新增 %d（派发上限 %d）", w.Running, w.Queued, w.Waiting, w.Available, w.Limit)
}

// planningTriggers 只消费真实增量。主宰自己派发/取消、领取、记研判和
// 心跳到点都不是新规划依据；心跳仍会唤醒循环做机器巡检与收割。
func (l *Loop) planningTriggers(ctx context.Context) ([]int64, error) {
	evs, err := l.Store.Events(ctx, l.TaskID, l.lastPlanRev, 0)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, ev := range evs {
		trigger := false
		switch ev.Kind {
		case rings.EventSwarmRecord:
			var p rings.SwarmRecord
			trigger = json.Unmarshal(ev.Payload, &p) == nil && p.Type == rings.SwarmRecSessionEnd
		case rings.EventRoeUpdated:
			trigger = true
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) == nil {
				urgent, _ := p.Detail["urgent"].(bool)
				trigger = p.NodeType == rings.NodeAsset || p.NodeType == rings.NodeGoal || (p.NodeType == rings.NodeStep && urgent) || (p.NodeType == rings.NodeHypothesis && ev.Actor == "creator")
			}
		case rings.EventTestClaimRecord:
			var p rings.TestClaimRecord
			trigger = json.Unmarshal(ev.Payload, &p) == nil && p.Type == rings.TestClaimRecReopen
		case rings.EventHypothesisRecord:
			// 验证结果由单位落地处理；外部升级/管理才单独触发。
			trigger = ev.Actor == "creator"
		case rings.EventShepherdRecord:
			var p rings.ShepherdRecord
			trigger = json.Unmarshal(ev.Payload, &p) == nil && (p.Type == rings.ShepherdRecUserMessage || p.Type == rings.ShepherdRecAdvice)
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			trigger = ev.Actor == "creator" && json.Unmarshal(ev.Payload, &p) == nil
		}
		if trigger {
			out = append(out, ev.Revision)
		}
	}
	return out, nil
}
