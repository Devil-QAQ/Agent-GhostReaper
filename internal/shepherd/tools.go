package shepherd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 牧者工具集(第四章 §1:不干活,只装三类工具——看/说/答;答即回复
// 本身,无工具)。全部工具不触达目标侧:看类只读图上投影,说类只写
// 账本(建议/紧急意图/目标/约束),越界由账本受控入口与审批门兜住
// (R9:紧急意图照走派发校验与执行审批,对话不构成旁路)。

// shortID 面板侧惯例的短随机 ID。
func shortID(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func allowAll(context.Context, json.RawMessage, permission.Context) permission.Decision {
	return permission.Allowed()
} // toolset 组装一个任务的牧者工具。messageID 为当前应答的消息锚点
// (建议等跨消息对象借此回到对话流的原位呈现)。P4-54 起操作面全量
// 对齐面板(最高权限:任务/目标/注意事项/优先级/聚焦/范围/单位/约束
// 皆可经对话操作,同一账本动作)。
func (s *Service) toolset(taskID, messageID string) []tool.CoreTool {
	return []tool.CoreTool{
		s.readProgressTool(taskID),
		s.readFindingsTool(taskID),
		s.readUnitsTool(taskID),
		s.adviseTool(taskID, messageID),
		s.urgentIntentTool(taskID),
		s.addGoalTool(taskID),
		s.updateGoalTool(taskID),
		s.deleteGoalTool(taskID),
		s.setFocusTool(taskID),
		s.setPriorityTool(taskID),
		s.setNotesTool(taskID),
		s.addConstraintTool(taskID),
		s.setAssetScopeTool(taskID),
		s.setInfoSourceTool(taskID),
		s.taskControlTool(taskID),
		s.unitControlTool(taskID),
	}
}

// ---- 看:查图 / 查漏洞 / 看虫群在干嘛 ----

// readProgressTool 目标/步骤/资产覆盖/RoE 的当前态势(问进度的数据面)。
func (s *Service) readProgressTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_progress",
		Description: "读任务当前态势:目标与达成状态、步骤及优先级、资产覆盖进度、授权约束(RoE)。回答进度类问题前先调用本工具。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    alwaysTrue,
		Concurrent:  alwaysTrue,
		Permissions: allowAll,
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var b strings.Builder
			task, err := s.Store.Task(ctx, taskID)
			if err == nil && task != nil {
				fmt.Fprintf(&b, "任务状态: %s\n", task.Status)
			}
			goals, _ := s.Store.NodesByType(ctx, taskID, rings.NodeGoal, "")
			b.WriteString("目标:\n")
			if len(goals) == 0 {
				b.WriteString("  (无)\n")
			}
			for _, g := range goals {
				mark := ""
				if f, _ := g.Detail["focus"].(bool); f {
					mark = " ★聚焦中(只做该方向)"
				}
				fmt.Fprintf(&b, "  - %s [%s]%s %s\n", g.ID, g.Status, mark, g.Title)
			}
			steps, _ := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
			b.WriteString("步骤:\n")
			if len(steps) == 0 {
				b.WriteString("  (无)\n")
			}
			for _, st := range steps {
				line := fmt.Sprintf("  - %s [%s] %s", st.ID, st.Status, st.Title)
				if p, _ := st.Detail["priority"].(string); p != "" && p != "normal" {
					line += fmt.Sprintf(" (优先级 %s)", p)
				}
				b.WriteString(line + "\n")
			}
			if ag, err := s.Store.AssetGraph(ctx, taskID); err == nil {
				total, covered := len(ag.Assets), 0
				for _, a := range ag.Assets {
					if a.Covered {
						covered++
					}
				}
				fmt.Fprintf(&b, "资产覆盖: %d/%d\n", covered, total)
				for _, a := range ag.Assets {
					mark := "未覆盖"
					if a.Covered {
						mark = "已覆盖"
					}
					fmt.Fprintf(&b, "  - %s [%s] %s (%s)\n", a.ID, a.AssetClass, a.Title, mark)
				}
			}
			if roe, err := s.Store.CurrentRoE(ctx, taskID); err == nil {
				b.WriteString("授权约束(RoE):\n")
				if len(roe.DeniedTools) > 0 {
					fmt.Fprintf(&b, "  禁止工具: %v\n", roe.DeniedTools)
				}
				if len(roe.AllowedTools) > 0 {
					fmt.Fprintf(&b, "  仅允许工具: %v\n", roe.AllowedTools)
				}
				th := roe.ApprovalThreshold
				if th == "" {
					th = "修改与持久化(默认:仅增删改需批准)"
				}
				fmt.Fprintf(&b, "  审批门槛: %s\n", string(th))
				if roe.Note != "" {
					fmt.Fprintf(&b, "  说明: %s\n", roe.Note)
				}
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// readFindingsTool 发现(漏洞声明)、事实与假设的当前清单(查图/查漏洞
// 的数据面)。已确认事实一并列出——它是 fact# 引用与研判输入的共同
// 数据面(第四章 §1"看:查图、查漏洞")。
func (s *Service) readFindingsTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_findings",
		Description: "读当前发现、事实与假设:漏洞判断(状态/存在性判断/严重级别)、已确认事实、虫群直觉回传(假设状态)。回答发现/事实类问题前先调用本工具;引用事实给编号(如 fact:f-xxxx)。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    alwaysTrue,
		Concurrent:  alwaysTrue,
		Permissions: allowAll,
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var b strings.Builder
			claims, _ := s.Store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
			b.WriteString("发现(漏洞判断):\n")
			if len(claims) == 0 {
				b.WriteString("  (无)\n")
			}
			for _, c := range claims {
				fmt.Fprintf(&b, "  - %s [%s] %s\n", c.ID, rings.ClaimStatusWord(c.Status), c.Title)
				if sev, _ := c.Detail["severity"].(string); sev != "" {
					fmt.Fprintf(&b, "    严重级别: %s\n", sev)
				}
			}
			facts, _ := s.Store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
			b.WriteString("已确认事实:\n")
			if len(facts) == 0 {
				b.WriteString("  (无)\n")
			}
			for _, f := range facts {
				fmt.Fprintf(&b, "  - %s: %s\n", f.ID, f.Title)
			}
			hyps, _ := s.Store.NodesByType(ctx, taskID, rings.NodeHypothesis, "")
			b.WriteString("假设(虫群直觉回传):\n")
			if len(hyps) == 0 {
				b.WriteString("  (无)\n")
			}
			for _, h := range hyps {
				fmt.Fprintf(&b, "  - %s [%s] %s\n", h.ID, rings.HypStatusWord(h.Status), h.Title)
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// readUnitsTool 虫群单位在干嘛(在跑步骤 + 最近工具调用)。
func (s *Service) readUnitsTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "read_units",
		Description: "读虫群执行近况:在跑步骤的领取与会话信息、最近的工具调用记录。回答「虫群在干嘛/卡没卡」前先调用本工具。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    alwaysTrue,
		Concurrent:  alwaysTrue,
		Permissions: allowAll,
		Run: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var b strings.Builder
			evs, err := s.Store.Events(ctx, taskID, 0, 0)
			if err != nil {
				return tool.Errorf("读事件流失败: " + err.Error()), nil
			}
			type unitLive struct {
				UnitID, Transcript, StartedNote string
			}
			running := map[string]*unitLive{} // stepID → 在跑单位
			var recent []string
			for _, ev := range evs {
				if ev.Kind != rings.EventSwarmRecord {
					continue
				}
				var rec rings.SwarmRecord
				if json.Unmarshal(ev.Payload, &rec) != nil {
					continue
				}
				switch rec.Type {
				case rings.SwarmRecSessionStarted:
					running[rec.StepID] = &unitLive{rec.UnitID, rec.TranscriptID, rec.Note}
				case rings.SwarmRecSessionEnd:
					delete(running, rec.StepID)
				case rings.SwarmRecToolCall:
					line := fmt.Sprintf("%s %s · %s %s", ev.OccurredAt.Local().Format("15:04:05"),
						rec.StepID, rec.ToolName, toolCallBrief(rec.ToolArgs))
					if rec.IsError {
						line += " (出错)"
					}
					recent = append(recent, line)
				}
			}
			b.WriteString("在跑步骤:\n")
			if len(running) == 0 {
				b.WriteString("  (无——当前没有虫群单位在执行)\n")
			}
			for stepID, u := range running {
				fmt.Fprintf(&b, "  - %s 单位 %s(会话 %s)\n", stepID, u.UnitID, u.Transcript)
				if u.StartedNote != "" {
					fmt.Fprintf(&b, "    %s\n", u.StartedNote)
				}
			}
			b.WriteString("最近工具调用(最新 12 条):\n")
			if len(recent) == 0 {
				b.WriteString("  (无)\n")
			}
			tail := recent
			if len(tail) > 12 {
				tail = tail[len(tail)-12:]
			}
			for _, line := range tail {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
			return tool.Text(strings.TrimRight(b.String(), "\n")), nil
		},
	})
}

// toolCallBrief 工具参数的一行摘要(截断,审计原文在账本)。
func toolCallBrief(args json.RawMessage) string {
	s := strings.TrimSpace(string(args))
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// ---- 说:建议(纳谏)/ 紧急意图 / 新目标 / 新约束 ----

// adviseTool 对主宰的建议(纳谏语义,第四章 §7:建议进正式规划,裁决
// 后改——入账 pending,主宰下一轮研判裁决,理由回写)。P4-54 指令
// 语义定稿:纳谏只适用于牧者自发的建议(造物主没要求的想法);造物
// 主的话语是指令,直接用操作工具执行,不走本工具。
func (s *Service) adviseTool(taskID, messageID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "advise_overmind",
		Description: "向主宰提交你自发提出的规划建议(纳谏:记录后由主宰在下一轮规划判定,采纳与否都带理由回写,不由牧者代决)。只用于造物主没要求、你自己认为值得做的改进想法;造物主的指令不走本工具——他说改什么就直接用对应操作工具执行。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"advice": map[string]any{"type": "string", "description": "建议正文:改什么方向、为什么。一句到三句,给主宰的规划输入,不是命令"},
			},
			"required": []string{"advice"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Advice string `json:"advice"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.Advice) == "" {
				return tool.Errorf("advice 不能为空:写明建议的方向与理由后重发"), nil
			}
			id := shortID("av")
			if _, err := s.Store.Append(ctx, taskID, rings.Event{
				Kind: rings.EventShepherdRecord, Actor: ActorShepherd,
				Payload: rings.ShepherdRecord{
					MessageID: messageID, Type: rings.ShepherdRecAdvice,
					AdviceID: id, Text: strings.TrimSpace(args.Advice),
				},
			}); err != nil {
				return tool.Errorf("建议记录失败: " + err.Error()), nil
			}
			s.wake(taskID)
			return tool.Text(fmt.Sprintf("建议 %s 已记录(待判定)。主宰在下一轮规划时判定:采纳则规划随之改变,不采纳会带回理由。", id)), nil
		},
	})
}

// urgentIntentTool 紧急意图(插急活,第四章 §2:高优先级标记入账,
// 虫群单位照常从图上领取,纪律不破,主宰事后可见)。
func (s *Service) urgentIntentTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "urgent_intent",
		Description: "登记高优先级意图(插急活):立即以 high 优先级派发为 Step,虫群单位照常从图上领取执行,主宰下一轮可见。造物主要求「先去做 X」时用本工具。动作分级如实申报——分级决定审批强度,不许压低。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"intent":               map[string]any{"type": "string", "description": "一句话意图:做什么、对哪个资产"},
				"expected_observation": map[string]any{"type": "string", "description": "预期观察:做完后应看到什么(验收参考)"},
				"action_level":         map[string]any{"type": "string", "description": "动作分级:只读探测|主动测试|利用深入|修改与持久化"},
				"covers": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "本意图覆盖的资产 ID 清单(图上存在的资产,可从 read_progress 结果取)"},
			},
			"required": []string{"intent", "action_level"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Intent              string   `json:"intent"`
				ExpectedObservation string   `json:"expected_observation"`
				ActionLevel         string   `json:"action_level"`
				Covers              []string `json:"covers"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.Intent) == "" {
				return tool.Errorf("intent 不能为空:写明要做什么后重发"), nil
			}
			stepID, err := overmind.UrgentDispatch(ctx, s.Store, taskID,
				strings.TrimSpace(args.Intent), strings.TrimSpace(args.ExpectedObservation),
				args.ActionLevel, args.Covers)
			if err != nil {
				return tool.Errorf("紧急意图派发被拒: " + err.Error()), nil
			}
			s.wake(taskID)
			return tool.Text(fmt.Sprintf("紧急意图已派发为步骤 %s(高优先级)。虫群单位领取后执行;任务未运行时排在图上,唤醒后执行。", stepID)), nil
		},
	})
}

// addGoalTool 登记新目标(第四章 §4:目标 = 最终可核验目标,新增通知
// 主宰并可复活任务)。
func (s *Service) addGoalTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "add_goal",
		Description: "登记新目标(任务的最终可核验目标)。目标记录并通知主宰;需要机器判定达成时,由主宰后续拆解补完成谓词。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "目标标题(可核验的最终目标)"},
			},
			"required": []string{"title"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			if strings.TrimSpace(args.Title) == "" {
				return tool.Errorf("title 不能为空:一句话写明可核验的目标"), nil
			}
			id := shortID("g")
			if err := overmind.SeedGoal(ctx, s.Store, taskID, id, strings.TrimSpace(args.Title), map[string]any{}); err != nil {
				return tool.Errorf("目标记录失败: " + err.Error()), nil
			}
			s.wake(taskID)
			return tool.Text(fmt.Sprintf("目标 %s 已登记(进行中),主宰已被通知。", id)), nil
		},
	})
}

// addConstraintTool 登记新约束(RoE 边界收紧:追加禁用工具 + 说明;
// 第四章 §4 约束框定探索边界,改动下一轮生效,不做硬中断)。
func (s *Service) addConstraintTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "add_constraint",
		Description: "追加授权约束:把工具加入禁用清单(RoE 收紧,下一轮生效,不硬中断在跑单位)。白名单与审批门槛的调整在 RoE 面板操作。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deny_tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "要禁用的工具名清单(追加,不覆盖既有禁用)"},
				"note": map[string]any{"type": "string", "description": "约束说明:谁、为何收紧(记录留痕)"},
			},
			"required": []string{"deny_tools"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				DenyTools []string `json:"deny_tools"`
				Note      string   `json:"note"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			args.DenyTools = trimAll(args.DenyTools)
			if len(args.DenyTools) == 0 {
				return tool.Errorf("deny_tools 不能为空:写明要禁用的工具名"), nil
			}
			roe, err := s.Store.CurrentRoE(ctx, taskID)
			if err != nil {
				return tool.Errorf("读当前约束失败: " + err.Error()), nil
			}
			merged := append([]string(nil), roe.DeniedTools...)
			for _, t := range args.DenyTools {
				if !containsStr(merged, t) {
					merged = append(merged, t)
				}
			}
			note := strings.TrimSpace(args.Note)
			if note == "" {
				note = "造物主经牧者追加约束"
			} else {
				note = "造物主经牧者追加约束: " + note
			}
			if _, err := s.Store.Append(ctx, taskID, rings.Event{
				Kind: rings.EventRoeUpdated, Actor: ActorShepherd,
				Payload: rings.RoeUpdated{
					AllowedTools: roe.AllowedTools, DeniedTools: merged,
					ApprovalThreshold: roe.ApprovalThreshold, Note: note,
				},
			}); err != nil {
				return tool.Errorf("约束记录失败: " + err.Error()), nil
			}
			s.wake(taskID)
			return tool.Text(fmt.Sprintf("约束已记录:禁用 %v(下一轮生效)。", args.DenyTools)), nil
		},
	})
}

// setInfoSourceTool 信息源标记/撤销(P4-67 ⑥ 牧者通道;P4-68 ① 起与
// RoE 面板标记同源):"xxx 是查资料的不是目标"→标记信息源
// (swarm.AllowScopeAccess 统一入口——入清单＋同主机资产退役,与分诊
// 自动授、面板标记行同一账本动作,RoE 面板同步可见);"xxx 别当信息
// 源了"→撤销(恢复拦截,后续访问重新走分诊)。与 set_asset_scope 的
// 分工:那个管"测不测"(纳入/摘除),这个管"用不用"(访问权)——
// 信息源不入资产图、不算覆盖。
func (s *Service) setInfoSourceTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "set_info_source",
		Description: "标记或撤销信息源(与 RoE 面板允许访问同源,面板实时可见)。标记=该地址是查资料用的公开信息服务(代码托管/搜索引擎/公共文档等),虫群访问它直接放行、不当作测试目标(不入资产图、不算覆盖);撤销=恢复拦截,再次访问会拦下问一次。造物主说「xxx 是查资料的不是目标」用 mark,「xxx 别当信息源了」用 revoke。地址给域名或 IP(可带端口)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"mark", "revoke"},
					"description": "mark=标记为信息源(访问放行) / revoke=撤销(恢复拦截)"},
				"hosts": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "地址清单:域名或 IP(host[:port] 写法)"},
				"basis": map[string]any{"type": "string",
					"description": "标记依据一句话(mark 时给;撤销可省)"},
			},
			"required": []string{"action", "hosts"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Action string   `json:"action"`
				Hosts  []string `json:"hosts"`
				Basis  string   `json:"basis"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			args.Hosts = trimAll(args.Hosts)
			if len(args.Hosts) == 0 {
				return tool.Errorf("hosts 不能为空:给出域名或 IP(host[:port] 写法)"), nil
			}
			var b strings.Builder
			switch args.Action {
			case "mark":
				for _, ident := range args.Hosts {
					a, ok := rings.ScopeAnchorOf(ident)
					if !ok {
						fmt.Fprintf(&b, "%s 不是可识别的主机地址,跳过;\n", ident)
						continue
					}
					basis := strings.TrimSpace(args.Basis)
					if basis == "" {
						basis = "操作者标记:查资料用的公开信息服务"
					}
					if err := swarm.AllowScopeAccess(ctx, s.Store, taskID, a.Host, basis, ActorShepherd); err != nil {
						return tool.Errorf("标记信息源失败: " + err.Error()), nil
					}
					fmt.Fprintf(&b, "已标记 %s 为信息源(访问放行,不当作测试目标);\n", ident)
				}
				return tool.Text(strings.TrimRight(b.String(), ";\n") + "。RoE 面板同步可见,可随时撤销。"), nil
			case "revoke":
				for _, ident := range args.Hosts {
					if err := swarm.RevokeInfoSource(ctx, s.Store, taskID, ident, ActorShepherd); err != nil {
						fmt.Fprintf(&b, "%s: %v;\n", ident, err)
						continue
					}
					fmt.Fprintf(&b, "已撤销 %s 的信息源标记(恢复拦截,再次访问会拦下问一次);\n", ident)
				}
				return tool.Text(strings.TrimRight(b.String(), ";\n") + "。RoE 面板同步可见。"), nil
			default:
				return tool.Errorf("action 须为 mark 或 revoke"), nil
			}
		},
	})
}

// setAssetScopeTool 资产范围操作(P4-45 ② + P4-54 通配与标识扩展):
// 纳入/摘除两向,与 RoE 面板同源——同一 roe.updated 账本状态、同一条
// 止损链路(swarm 资产范围操作),面板实时可见,无旁路(R7)。纳入=
// 渗透任务放行待批资产/移回已摘除(图上没有的标识按写法入图,来源
// 手动、默认范围内);红队任务本就全部可测,如实答复。摘除=资产不再
// 测试:覆盖它的在途单位立即终止、排队步骤取消,主宰下一轮不再派发
// 它;图上没有的标识(含 `*.域` 通配写法)按写法入图后摘除——通配
// 摘除对其后新发现的子域同样生效(派发守卫按通配排除模式判定)。
func (s *Service) setAssetScopeTool(taskID string) tool.CoreTool {
	return tool.Build(tool.Spec{
		Name:        "set_asset_scope",
		Description: "调整资产的测试范围(与 RoE 面板同源,面板实时可见)。纳入=让资产可测:渗透任务的待批资产放行、已摘除的移回范围;图上没有的标识按写法入图(来源手动,默认可测)。红队任务本就全部可测,如实答复。摘除=资产不再测试:覆盖它的在途单位立即终止、排队步骤取消,主宰不再派发它;支持 *.域 通配写法(摘除后该域与全部子域不再测,含其后新发现的)。造物主说「把 X 纳入范围/可以测了」用 include,「X 不能测了/摘除」用 exclude。资产给 ID(a-*)或标识(域名/IP/通配写法)皆可。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"include", "exclude"},
					"description": "include=纳入(可测) / exclude=摘除(不测,止损终止在途)"},
				"assets": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "资产清单:任务内资产 ID(a-*)或资产标识(域名/IP/host:端口/*.域 通配写法)"},
			},
			"required": []string{"action", "assets"},
		},
		Permissions: allowAll,
		Run: func(ctx context.Context, in json.RawMessage, _ *tool.ToolContext) (tool.Result, error) {
			var args struct {
				Action string   `json:"action"`
				Assets []string `json:"assets"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return tool.Errorf("bad input: " + err.Error()), nil
			}
			args.Assets = trimAll(args.Assets)
			if len(args.Assets) == 0 {
				return tool.Errorf("assets 不能为空:给出资产 ID(a-*)或标识(域名/IP/通配写法)"), nil
			}
			// 标识解析入图:图上已有(ID/标题/同锚点写法)直接复用;
			// 没有的——纳入按手动来源入图(默认范围内),摘除按发现
			// 形态入图再摘(与建任务初始摘除同形态,P4-53 注意事项 1)。
			source := rings.AssetSourceManual
			if args.Action == "exclude" {
				source = rings.AssetSourceDiscovered
			}
			var ids []string
			for _, ident := range args.Assets {
				// a- 开头且无点无通配的写法按资产 ID 引用对待:图上
				// 没有即陈旧引用,报错带可用资产供自纠(不把笔误 ID 当
				// 主机名入图);带点/通配的标识才是主机写法,按写法入图。
				if looksLikeAssetID(ident) {
					if n, err := s.Store.Node(ctx, taskID, ident); err != nil || n.Type != rings.NodeAsset {
						assets, _ := s.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
						return tool.Errorf(fmt.Sprintf("资产 %s 不在本任务图上(可用资产: %s)", ident, joinAssetIDs(assets))), nil
					}
				}
				id, _, err := swarm.EnsureScopeAsset(ctx, s.Store, taskID, ident, source, ActorShepherd)
				if err != nil {
					return tool.Errorf("资产解析失败: " + err.Error()), nil
				}
				ids = append(ids, id)
			}
			title := func(id string) string {
				if a, err := s.Store.Node(ctx, taskID, id); err == nil && a.Type == rings.NodeAsset {
					return fmt.Sprintf("%s(%s)", id, a.Title)
				}
				return id
			}
			switch args.Action {
			case "include":
				res, err := swarm.IncludeAssets(ctx, s.Store, taskID, ids, ActorShepherd)
				if err != nil {
					return tool.Errorf("纳入失败: " + err.Error()), nil
				}
				s.wake(taskID)
				var b strings.Builder
				for _, id := range res.Revived {
					fmt.Fprintf(&b, "已复活 %s(此前转信息源退役,现重新纳入测试范围);\n", title(id))
				}
				for _, id := range res.Approved {
					fmt.Fprintf(&b, "已放行待批资产 %s;\n", title(id))
				}
				for _, id := range res.Reincluded {
					fmt.Fprintf(&b, "已纳入 %s,移回测试范围;\n", title(id))
				}
				for _, id := range res.AlreadyIn {
					if s.taskTypeIs(ctx, taskID, rings.TaskTypePentest) {
						fmt.Fprintf(&b, "%s 本就在测试范围内,无需纳入;\n", title(id))
					} else {
						fmt.Fprintf(&b, "%s 本就全部可测(红队任务不设待批),无需纳入;\n", title(id))
					}
				}
				return tool.Text(strings.TrimRight(b.String(), ";\n") + "。RoE 面板同步可见,主宰已被通知。"), nil
			case "exclude":
				res, err := swarm.ExcludeAssets(ctx, s.Store, s.helm(), taskID, ids, ActorShepherd)
				if err != nil {
					return tool.Errorf("摘除失败: " + err.Error()), nil
				}
				// P4-69 ①(摘除联动收口,三通道一致):挂等这些目标的冻结
				// 单位与未决卡即时收口——意图覆盖者终止留痕、不覆盖者唤醒
				// 换路,卡按拒绝语义收口消红点(与 RoE 面板摘除/卡上禁止
				// 测试同一条对账链)。
				s.resolveScopeWakes(taskID)
				s.wake(taskID)
				var b strings.Builder
				for _, id := range res.AlreadyOut {
					fmt.Fprintf(&b, "%s 已在范围外,无需再摘除;\n", title(id))
				}
				if len(res.Excluded) > 0 {
					for _, id := range res.Excluded {
						fmt.Fprintf(&b, "已摘除 %s;\n", title(id))
					}
					fmt.Fprintf(&b, "终止在途单位 %d 个,取消排队步骤 %d 个,主宰不再派发覆盖它的意图。\n",
						len(res.KilledUnits), len(res.AbortedSteps))
				}
				return tool.Text(strings.TrimRight(b.String(), ";\n") + "。RoE 面板同步可见,主宰已被通知。"), nil
			default:
				return tool.Errorf("action 须为 include 或 exclude"), nil
			}
		},
	})
}

// taskTypeIs 任务类型判定(created 定格;读失败按非渗透,如实走保守分支)。
func (s *Service) taskTypeIs(ctx context.Context, taskID string, want string) bool {
	detail, err := s.Store.CreatedDetail(ctx, taskID)
	if err != nil {
		return false
	}
	return rings.TaskTypeOf(detail) == want
}

// looksLikeAssetID 是否按资产 ID 引用对待(a- 开头、无点、无通配、
// 无端口——与收割入图/范围卡入图的 a-<hex8> 同族前缀;带点或通配的是
// 主机写法,不在此列)。
func looksLikeAssetID(ident string) bool {
	return strings.HasPrefix(ident, "a-") &&
		!strings.ContainsAny(ident, ".*:") && len(ident) > 2
}

// joinAssetIDs 可用资产 ID 摘要(错误提示用,≤12 条防刷屏)。
func joinAssetIDs(assets []rings.NodeFull) string {
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ID)
	}
	if len(ids) > 12 {
		ids = append(ids[:12], fmt.Sprintf("…共 %d 个", len(ids)))
	}
	if len(ids) == 0 {
		return "(图上暂无资产)"
	}
	return strings.Join(ids, "、")
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func alwaysTrue(json.RawMessage) bool { return true }
