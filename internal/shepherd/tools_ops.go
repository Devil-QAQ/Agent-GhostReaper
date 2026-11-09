package shepherd

// 牧者操作面工具(P4-54,问题清单 #78＋#73⑧牧者部分;造物主定宗旨:
// 牧者是最高权限的,可以改变整个任务的任何动作、资产、目标等)。
// 造物主在面板能做的操作,经牧者对话皆可做——同一账本动作、与面板
// 同源同步、R7 无旁路:目标修改/删除、注意事项追加/修改/删除、方向
// 优先级调整、停方向聚焦/解除、任务级冬眠/唤醒/终止、单位级终止/纠偏。
// 指令语义:造物主话语＝指令直接执行(非纳谏);回执如实(数字精确),
// 账本留痕可审计。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// updateGoalTool 修改目标(P4-54 ①):标题/完成条件——完成条件重新冻结
// (仲裁契约②判定标准变更走新事件,仲裁者读最后显式冻结值);修改通知
// 主宰并可复活任务(唤醒由本工具执行)。与面板目标管理修改同源。
func (s *Service) updateGoalTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "update_goal",
		Description: "修改既有目标:改标题和/或完成条件。完成条件必须是受限结构 JSON({\"op\":\"all_of|any_of\",\"conditions\":[{\"kind\":\"fact_confirmed|tool_observed\",\"asset_id\":\"a-…\",\"min\":1}]},tool_observed 另需 tool 与 result_contains 字段),改后立即重新锁定,达成与否由仲裁者按新条件判定。目标 ID 从 read_progress 查(g-* 标识)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id":   map[string]any{"type": "string", "description": "要修改的目标 ID"},
				"title":     map[string]any{"type": "string", "description": "新标题(不改标题则省略)"},
				"predicate": map[string]any{"type": "object", "description": "新完成条件(不改则省略;受限结构)"},
			},
			"required": []string{"goal_id"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				GoalID    string         `json:"goal_id"`
				Title     *string        `json:"title"`
				Predicate map[string]any `json:"predicate"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.GoalID) == "" {
				return tool.Errorf("goal_id 不能为空:先用 read_progress 查目标 ID"), nil
			}
			var title *string
			if args.Title != nil {
				t := strings.TrimSpace(*args.Title)
				if t == "" {
					return tool.Errorf("目标标题不能为空"), nil
				}
				title = &t
			}
			note := "造物主经牧者修改目标"
			if err := overmind.AmendGoal(ctx, s.Store, taskID, strings.TrimSpace(args.GoalID), title, args.Predicate, nil, note, ActorShepherd); err != nil {
				return tool.Errorf("修改目标被拒: " + err.Error()), nil
			}
			s.wake(taskID)
			parts := []string{}
			if title != nil {
				parts = append(parts, "标题已改为「"+*title+"」")
			}
			if args.Predicate != nil {
				parts = append(parts, "完成条件已重新锁定(达成判定按新条件)")
			}
			return tool.Text(fmt.Sprintf("目标 %s 已修改:%s。主宰已被通知。", args.GoalID, strings.Join(parts, ";"))), nil
		},
	})
}

// deleteGoalTool 删除目标(P4-54 ①;P4-55 审核迭代修订):该方向不要了
// ——同停聚焦力度:该目标名下未领取排队步骤取消留痕、在跑单位立即
// 终止(回执如实报数);目标转已删除,后续零新派。受影响的反证判断
// 程序不动状态(禁止连坐):交主宰下轮裁决——资产仍被其他目标需要
// 则继续锤炼,无人需要则挂起(理由"目标已删除")。
func (s *Service) deleteGoalTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "delete_goal",
		Description: "删除目标(该方向不要了):该方向的排队步骤取消、在跑单位立即终止、后续不再派发该方向;受影响的反证判断交主宰判定(继续锤炼或挂起),程序不改其状态。回执会如实报告终止与取消笔数。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id": map[string]any{"type": "string", "description": "要删除的目标 ID(read_progress 可查)"},
				"reason":  map[string]any{"type": "string", "description": "删除理由(留痕;可省略)"},
			},
			"required": []string{"goal_id"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				GoalID string `json:"goal_id"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.GoalID) == "" {
				return tool.Errorf("goal_id 不能为空:先用 read_progress 查目标 ID"), nil
			}
			res, err := overmind.DeleteGoal(ctx, s.Store, s.helm(), taskID, strings.TrimSpace(args.GoalID), args.Reason, ActorShepherd)
			if err != nil {
				return tool.Errorf("删除目标被拒: " + err.Error()), nil
			}
			s.wake(taskID)
			var b strings.Builder
			fmt.Fprintf(&b, "目标 %s 已删除:终止在途单位 %d 个,取消排队步骤 %d 个", args.GoalID, len(res.KilledUnits), len(res.AbortedSteps))
			b.WriteString("。该方向后续零新派;受影响的判断交主宰下轮判定(资产仍被其他目标需要则继续锤炼,无人需要则挂起)。")
			return tool.Text(b.String()), nil
		},
	})
}

// setFocusTool 停方向聚焦(P4-54 ①):"其他都停掉只做 X"——非 X 方向
// 排队步骤取消留痕、在跑单位立即终止、派发守卫不再派其他方向(回执
// 如实报数);被取消反证的声明转挂起,不自动重派。goal_id 省略 = 解除
// 聚焦(恢复各方向,挂起声明唤醒)。
func (s *Service) setFocusTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "set_focus",
		Description: "停方向聚焦(「其他都停掉只做 X」)或解除聚焦。聚焦后:只派发该方向的意图,其他方向的排队步骤取消、在跑单位立即终止,反证中的判断挂起;造物主后插的急活不受限。重新聚焦或解除聚焦时,挂起的判断恢复反证。goal_id 省略 = 解除聚焦恢复各方向。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id": map[string]any{"type": "string", "description": "聚焦的目标 ID(省略 = 解除聚焦)"},
			},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				GoalID string `json:"goal_id"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			gid := strings.TrimSpace(args.GoalID)
			if gid == "" {
				n, err := overmind.UnfocusGoal(ctx, s.Store, taskID, ActorShepherd)
				if err != nil {
					return tool.Errorf("解除聚焦被拒: " + err.Error()), nil
				}
				s.wake(taskID)
				return tool.Text(fmt.Sprintf("方向聚焦已解除,恢复各方向派发;唤醒挂起判断 %d 个。", n)), nil
			}
			res, err := overmind.FocusGoal(ctx, s.Store, s.helm(), taskID, gid, ActorShepherd)
			if err != nil {
				return tool.Errorf("停聚焦被拒: " + err.Error()), nil
			}
			s.wake(taskID)
			g, _ := s.Store.Node(ctx, taskID, gid)
			title := gid
			if g != nil {
				title = g.Title
			}
			var b strings.Builder
			fmt.Fprintf(&b, "已聚焦「%s」:终止在途单位 %d 个,取消排队步骤 %d 个,挂起判断 %d 个,唤醒属该方向的挂起判断 %d 个", title, len(res.KilledUnits), len(res.AbortedSteps), len(res.SuspendedClaims), res.ResumedClaims)
			b.WriteString("。此后只派发该方向,其他方向零新派。")
			return tool.Text(b.String()), nil
		},
	})
}

// setPriorityTool 方向优先级调整(P4-54 ①):直接落账,队列领取次序立即
// 按新优先级(对照可在虫群排队列表与领取次序核验)。goal_id = 调整该
// 方向的全部排队步骤;step_ids = 调整指定步骤;priority 省略 = 回默认。
func (s *Service) setPriorityTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "set_priority",
		Description: "调整方向/步骤的派发优先级(如「X 最优先」「把 Y 降级」)。落账后队列领取次序立即按新优先级。给 goal_id 调整该方向全部排队步骤,给 step_ids 调整指定步骤;priority: high|normal|low,省略 = 回默认。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id":  map[string]any{"type": "string", "description": "方向(目标)ID——调整该方向的全部排队步骤"},
				"step_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "指定步骤 ID 清单(与 goal_id 二选一)"},
				"priority": map[string]any{"type": "string", "enum": []string{"high", "normal", "low"}, "description": "目标优先级;省略 = 回默认"},
			},
			"required": []string{"priority"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				GoalID   string   `json:"goal_id"`
				StepIDs  []string `json:"step_ids"`
				Priority string   `json:"priority"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			ids := trimAll(args.StepIDs)
			if gid := strings.TrimSpace(args.GoalID); gid != "" {
				steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
				if err != nil {
					return tool.Errorf("查步骤失败: " + err.Error()), nil
				}
				ids = nil
				for _, st := range steps {
					if g, _ := st.Detail["goal_id"].(string); g == gid && st.Status == "dispatched" {
						ids = append(ids, st.ID)
					}
				}
				if len(ids) == 0 {
					return tool.Text("该方向当前没有排队中的步骤,优先级无需调整。"), nil
				}
			}
			if len(ids) == 0 {
				return tool.Errorf("给出 goal_id 或 step_ids 之一(当前都没有)"), nil
			}
			changed, err := overmind.SetStepsPriority(ctx, s.Store, taskID, ids, args.Priority, ActorShepherd)
			if err != nil {
				return tool.Errorf("优先级调整被拒: " + err.Error()), nil
			}
			word := "默认"
			if p := strings.TrimSpace(args.Priority); p != "" {
				word = p
			}
			if len(changed) == 0 {
				return tool.Text("步骤都已领取或已结束,优先级未变(在跑单位已占名额,调了不生效)。"), nil
			}
			return tool.Text(fmt.Sprintf("优先级已调为 %s,生效步骤 %d 个(%s)。队列领取次序立即按新优先级。", word, len(changed), strings.Join(changed, "、"))), nil
		},
	})
}

// setNotesTool 注意事项管理(P4-54 ①):追加/修改/删除造物主注意事项
// (对做法的软要求),与 RoE 面板编辑同源——同一 roe.updated 记账、下一
// 轮生效、面板实时可见。replace = 整体覆盖,clear = 清空。
func (s *Service) setNotesTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "set_notes",
		Description: "管理造物主注意事项(对做法的软要求,如「用小字典」「不要修改任何数据」):append=追加一行,replace=整体覆盖,clear=清空。与 RoE 面板注意事项编辑同源,下一轮生效。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"append", "replace", "clear"},
					"description": "append=追加一行 / replace=整体覆盖 / clear=清空"},
				"text": map[string]any{"type": "string", "description": "注意事项文本(append/replace 必填)"},
			},
			"required": []string{"action"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Action string `json:"action"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			text := rings.NormalizeCreatorNotes(args.Text)
			switch args.Action {
			case "append", "replace":
				if text == "" {
					return tool.Errorf("text 不能为空:写明注意事项内容"), nil
				}
			case "clear":
			default:
				return tool.Errorf("action 须为 append|replace|clear"), nil
			}
			st, err := s.Store.CurrentRoE(ctx, taskID)
			if err != nil {
				return tool.Errorf("读当前约束失败: " + err.Error()), nil
			}
			next := text
			verb := "已写入"
			switch args.Action {
			case "append":
				if st.CreatorNotes == "" {
					next = text
				} else {
					next = st.CreatorNotes + "\n" + text
				}
				verb = "已追加一行"
			case "replace":
				verb = "已更新"
			case "clear":
				next = ""
				verb = "已清空"
			}
			next = rings.NormalizeCreatorNotes(next)
			if len([]rune(next)) > rings.CreatorNotesMaxRunes {
				return tool.Errorf(fmt.Sprintf("注意事项过长(上限 %d 字),未写入", rings.CreatorNotesMaxRunes)), nil
			}
			if _, err := s.Store.Append(ctx, taskID, rings.Event{
				Kind: rings.EventRoeUpdated, Actor: ActorShepherd,
				Payload: rings.RoeUpdated{
					AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
					ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
					ExcludedAssets: st.ExcludedAssets, ScopeApproved: st.ScopeApproved,
					CreatorNotes: &next, Note: "造物主经牧者" + map[string]string{"append": "追加", "replace": "修改", "clear": "清空"}[args.Action] + "注意事项",
				},
			}); err != nil {
				return tool.Errorf("注意事项写入失败: " + err.Error()), nil
			}
			s.wake(taskID)
			return tool.Text(fmt.Sprintf("注意事项%s(下一轮生效)。当前注意事项:\n%s", verb, notesPreview(next))), nil
		},
	})
}

// notesPreview 注意事项回显(空态人话)。
func notesPreview(s string) string {
	if s == "" {
		return "(无)"
	}
	return s
}

// taskControlTool 任务级控制(P4-54 ①):冬眠/唤醒/终止——与面板按钮
// 同源同账本(Manager 生命周期记账)。
func (s *Service) taskControlTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "task_control",
		Description: "任务级控制:hibernate=冬眠(冻结收场,面板只读,唤醒后继续)/ wake=唤醒(冬眠、暂停、断点存档都可恢复)/ stop=终止(任务到此收场,不可恢复)。与面板任务控制按钮同源同账本。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"hibernate", "wake", "stop"},
					"description": "hibernate=冬眠 / wake=唤醒 / stop=终止"},
				"reason": map[string]any{"type": "string", "description": "理由(留痕;终止建议写明)"},
			},
			"required": []string{"action"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Action string `json:"action"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			detail := map[string]any{"by": ActorShepherd}
			if r := strings.TrimSpace(args.Reason); r != "" {
				detail["reason"] = r
			}
			switch args.Action {
			case "hibernate":
				if s.TaskHibernate == nil {
					return tool.Errorf("任务运行控制未装配,冬眠指令不可用"), nil
				}
				if err := s.TaskHibernate(taskID, detail); err != nil {
					return tool.Errorf("冬眠被拒: " + err.Error()), nil
				}
				return tool.Text("任务已冬眠:面板转为只读,对话冻结;说「唤醒任务」恢复。"), nil
			case "wake":
				if s.TaskWake == nil {
					return tool.Errorf("任务运行控制未装配,唤醒指令不可用"), nil
				}
				if err := s.TaskWake(taskID); err != nil {
					return tool.Errorf("唤醒被拒: " + err.Error()), nil
				}
				return tool.Text("任务已唤醒,继续运行。"), nil
			case "stop":
				if s.TaskStop == nil {
					return tool.Errorf("任务运行控制未装配,终止指令不可用"), nil
				}
				if err := s.TaskStop(taskID); err != nil {
					return tool.Errorf("终止被拒: " + err.Error()), nil
				}
				return tool.Text("任务已终止(任务到此收场,不可恢复)。"), nil
			default:
				return tool.Errorf("action 须为 hibernate|wake|stop"), nil
			}
		},
	})
}

// unitControlTool 单位级控制(P4-54 ①):终止指定步骤的在跑单位
// (kill_work,与熔断同款真中断)/ 纠偏(steer_work,消息在下一工具
// 调用边界注入,不打断)——舵杆既有通道,面板同源。
func (s *Service) unitControlTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "unit_control",
		Description: "单位级控制:kill=终止指定步骤的在跑单位(方向错误时止损,真中断)/ steer=给在跑单位下达纠偏消息(不打断,下一工具调用前注入,带回执)。step_id 从 read_units 查。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"kill", "steer"}, "description": "kill=终止 / steer=纠偏"},
				"step_id": map[string]any{"type": "string", "description": "目标步骤 ID(read_units 可查在跑步骤)"},
				"text":    map[string]any{"type": "string", "description": "steer 的纠偏消息 / kill 的理由"},
			},
			"required": []string{"action", "step_id"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Action string `json:"action"`
				StepID string `json:"step_id"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			stepID := strings.TrimSpace(args.StepID)
			if stepID == "" {
				return tool.Errorf("step_id 不能为空:先用 read_units 查在跑步骤"), nil
			}
			helm := s.helm()
			if helm == nil {
				return tool.Errorf("执行器未装配,无在跑单位可控制"), nil
			}
			switch args.Action {
			case "kill":
				reason := strings.TrimSpace(args.Text)
				if reason == "" {
					reason = "造物主经牧者终止单位"
				}
				if _, err := helm.KillWork(ctx, taskID, stepID, reason, ActorShepherd); err != nil {
					return tool.Errorf("终止被拒: " + err.Error()), nil
				}
				return tool.Text(fmt.Sprintf("步骤 %s 的单位终止指令已下达(真中断,留痕可查)。", stepID)), nil
			case "steer":
				msg := strings.TrimSpace(args.Text)
				if msg == "" {
					return tool.Errorf("steer 的纠偏消息不能为空"), nil
				}
				if _, err := helm.SteerWork(ctx, taskID, stepID, msg, ActorShepherd); err != nil {
					return tool.Errorf("纠偏被拒: " + err.Error()), nil
				}
				return tool.Text(fmt.Sprintf("纠偏消息已排队,步骤 %s 的单位在下一工具调用前收到(回执状态可在面板查看)。", stepID)), nil
			default:
				return tool.Errorf("action 须为 kill|steer"), nil
			}
		},
	})
}
