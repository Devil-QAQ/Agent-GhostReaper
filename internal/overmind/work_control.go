package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

type workControlPlan struct {
	StepID  string `json:"step_id"`
	UnitID  string `json:"unit_id"`
	Action  string `json:"action"` // continue|steer|stop
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// executionProgress uses the same ledger read as the planning snapshot. The
// returned owner bindings travel with that reply, never with another round.
func (l *Loop) executionProgress(ctx context.Context, steps, facts []rings.NodeFull) ([]string, map[string]rings.ExecutionOwner, error) {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, nil, err
	}
	latest := rings.LatestExecutionOwners(evs)
	owners := map[string]rings.ExecutionOwner{}
	byUnit := map[string]string{}
	for _, step := range steps {
		if owner, ok := latest[step.ID]; step.Status == "running" && ok {
			owners[step.ID] = owner
			byUnit[owner.UnitID] = step.ID
		}
	}
	if len(owners) == 0 {
		return []string{"(无当前执行)"}, owners, nil
	}
	type timedTool struct {
		rec rings.SwarmRecord
		at  time.Time
		rev int64
	}
	completed := map[string][]timedTool{}
	active := map[string]map[string]timedTool{}
	submitted := map[string]int{}
	controls := map[string]rings.RawEvent{}
	steers := map[string]rings.SteerRecord{}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventSwarmRecord:
			var rec rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &rec) != nil {
				continue
			}
			owner, ok := owners[rec.StepID]
			if !ok || owner.UnitID != rec.UnitID || ev.Revision < owner.StartedRev {
				continue
			}
			if active[rec.StepID] == nil {
				active[rec.StepID] = map[string]timedTool{}
			}
			if rec.Type == rings.SwarmRecToolStarted {
				active[rec.StepID][rec.ToolCallID] = timedTool{rec, ev.OccurredAt, ev.Revision}
			}
			if rec.Type == rings.SwarmRecToolCall {
				delete(active[rec.StepID], rec.ToolCallID)
				completed[rec.StepID] = append(completed[rec.StepID], timedTool{rec, ev.OccurredAt, ev.Revision})
			}
		case rings.EventNodeAdded:
			var node rings.NodeAdded
			if json.Unmarshal(ev.Payload, &node) == nil && node.NodeType == rings.NodeFact {
				unit, _ := node.Detail["unit_id"].(string)
				if step := byUnit[unit]; step != "" && ev.Revision >= owners[step].StartedRev {
					submitted[step]++
				}
			}
		case rings.EventBlackboxRecord:
			var rec rings.BlackboxRecord
			if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.BlackboxHbControl && owners[rec.StepID].UnitID == rec.UnitID {
				controls[rec.StepID] = ev
			}
		case rings.EventSteerRecord:
			var rec rings.SteerRecord
			if json.Unmarshal(ev.Payload, &rec) == nil {
				steers[rec.SteerID] = rec
			}
		}
	}
	var lines []string
	for _, step := range steps {
		owner, ok := owners[step.ID]
		if !ok {
			continue
		}
		wait := "无挂等"
		if l.Swarm != nil {
			if l.Swarm.UnitPaused(l.TaskID, step.ID) {
				wait = "人工暂停"
			} else {
				switch l.Swarm.ClockWait(l.TaskID, owner.TranscriptID) {
				case "approval":
					wait = "等待审批"
				case "model":
					wait = "等待模型"
				}
			}
		}
		var confirmed []string
		for _, f := range facts {
			if unit, _ := f.Detail["unit_id"].(string); unit == owner.UnitID && f.CreatedRev >= owner.StartedRev {
				confirmed = append(confirmed, f.ID)
			}
		}
		line := fmt.Sprintf("%s [running] unit_id=%s；开始版本=%d；%s；工具返回%d次(含拒绝／暂缓，须看实际结果)；提交观察%d条，已确认%d条 %v", step.ID, owner.UnitID, owner.StartedRev, wait, len(completed[step.ID]), submitted[step.ID], len(confirmed), confirmed)
		var running []string
		for _, tool := range active[step.ID] {
			running = append(running, fmt.Sprintf("%s %s(运行%d秒)", tool.rec.ToolName, clipArgs(string(tool.rec.ToolArgs), 160), max(0, int(time.Since(tool.at).Seconds()))))
		}
		sort.Strings(running)
		if len(running) > 0 {
			line += "；工具正在执行=" + strings.Join(running, " / ")
		}
		for _, tool := range completed[step.ID][max(0, len(completed[step.ID])-3):] {
			line += fmt.Sprintf("；最近结果@%s %s %s error=%t: %s", tool.at.Format(time.RFC3339), tool.rec.ToolName, clipArgs(string(tool.rec.ToolArgs), 120), tool.rec.IsError, clipArgs(tool.rec.ResultHead, 180))
		}
		if ev, ok := controls[step.ID]; ok {
			var rec rings.BlackboxRecord
			_ = json.Unmarshal(ev.Payload, &rec)
			line += fmt.Sprintf("；上次复查 action=%v reason=%v outcome=%v", rec.Detail["action"], rec.Detail["reason"], rec.Detail["outcome"])
			if id, ok := rec.Detail["steer_id"].(string); ok && id != "" {
				line += " 当前回执=" + steers[id].State
			}
			progress := 0
			for _, tool := range completed[step.ID] {
				if tool.rev > ev.Revision {
					progress++
				}
			}
			line += fmt.Sprintf("；复查后实际工具返回%d次", progress)
		}
		lines = append(lines, line)
	}
	return lines, owners, nil
}

func (l *Loop) applyWorkControl(ctx context.Context, plan workControlPlan, owners map[string]rings.ExecutionOwner, evaluatedRev int64) error {
	owner, ok := owners[plan.StepID]
	if !ok || owner.UnitID != plan.UnitID {
		return fmt.Errorf("控制对象不属于本轮快照的实际执行")
	}
	if strings.TrimSpace(plan.Reason) == "" {
		return fmt.Errorf("执行复查必须说明真实进展或前提依据")
	}
	detail := map[string]any{"action": plan.Action, "reason": plan.Reason, "evaluated_rev": evaluatedRev, "started_rev": owner.StartedRev}
	var err error
	var id string
	switch plan.Action {
	case "continue":
		detail["outcome"] = "continued"
		_, err = l.Store.AppendForExecution(ctx, l.TaskID, owner, rings.Event{Kind: rings.EventBlackboxRecord, Actor: ActorOvermind, Payload: rings.BlackboxRecord{Type: rings.BlackboxHbControl, StepID: plan.StepID, UnitID: plan.UnitID, Detail: detail}})
		return err
	case "steer", "stop":
		if l.Swarm == nil || l.Swarm.Helm == nil {
			return fmt.Errorf("执行控制未接入，本次未执行")
		}
		if plan.Action == "steer" {
			id, err = l.Swarm.Helm.SteerWorkForExecution(ctx, l.TaskID, owner, plan.Message, swarm.ActorOvermindHelm)
			detail["outcome"] = "queued"
		} else {
			id, err = l.Swarm.Helm.KillWorkForExecution(ctx, l.TaskID, owner, plan.Reason, swarm.ActorOvermindHelm)
			detail["outcome"] = "delivered"
		}
	default:
		return fmt.Errorf("复查动作只允许continue、steer或stop")
	}
	detail["steer_id"] = id
	if err != nil {
		detail["outcome"] = "unconfirmed"
		detail["error"] = err.Error()
	}
	_, recordErr := l.Store.Append(ctx, l.TaskID, rings.Event{Kind: rings.EventBlackboxRecord, Actor: ActorOvermind, Payload: rings.BlackboxRecord{Type: rings.BlackboxHbControl, StepID: plan.StepID, UnitID: plan.UnitID, Detail: detail}})
	if recordErr != nil {
		return recordErr
	}
	return err
}

// Only alerts actually present in this frozen snapshot require a decision.
// A missing/invalid decision leaves the triggering revisions unconsumed.
func (l *Loop) workReviewsDue(ctx context.Context, owners map[string]rings.ExecutionOwner, rev int64) (map[string]bool, error) {
	events, err := l.Store.Events(ctx, l.TaskID, l.lastPlanRev, 0)
	if err != nil {
		return nil, err
	}
	due := map[string]bool{}
	since := time.Now().Add(-2 * l.hbInterval())
	for _, ev := range events {
		if ev.Revision > rev || ev.Kind != rings.EventBlackboxRecord || !ev.OccurredAt.After(since) {
			continue
		}
		var rec rings.BlackboxRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && heartbeatAlert(rec.Type) {
			if owner, ok := owners[rec.StepID]; ok && owner.UnitID == rec.UnitID {
				due[rec.StepID] = true
			}
		}
	}
	return due, nil
}

func validateWorkReviews(plans []workControlPlan, owners map[string]rings.ExecutionOwner, due map[string]bool) error {
	reviewed := map[string]bool{}
	for _, plan := range plans {
		owner, ok := owners[plan.StepID]
		valid := ok && plan.UnitID == owner.UnitID && strings.TrimSpace(plan.Reason) != "" && (plan.Action == "continue" || plan.Action == "steer" && strings.TrimSpace(plan.Message) != "" || plan.Action == "stop")
		if valid {
			if reviewed[plan.StepID] {
				return fmt.Errorf("同一执行在一轮中只能决定一次: %s", plan.StepID)
			}
			reviewed[plan.StepID] = true
		}
	}
	for step := range due {
		if !reviewed[step] {
			return fmt.Errorf("本轮告警尚缺有效复查决定: %s；需按快照单位编号给continue、steer或stop及具体依据", step)
		}
	}
	return nil
}
