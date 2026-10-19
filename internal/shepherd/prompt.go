package shepherd

import (
	"context"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 牧者提示词完整版(P2-07,第四章 §1 + §10 + §11)。
//
// 三段式组装与虫群/主宰同规约(§11):A 段可编辑正文存通用配置键值表,
// B/C 段代码固定(编辑 DB 正文永远删不掉)。牧者不执行目标侧动作,
// 无 RoE 探索约束块(约束块防的是探索越界;牧者的"说"工具写账本、
// 紧急意图照走派发校验与审批门,越界由账本受控入口兜住,不在提示词
// 层重复)。牧者会话是给人读的会话(§10 作用域),C 段即 humanizer
// 中文化精简版文风条款——生成时就写对。

// SettingKeyPromptShepherd 牧者基线可编辑正文(A 段)的配置键。
const SettingKeyPromptShepherd = "prompt.shepherd.editable"

// shepherdEditableDefault 牧者基线 A 段默认正文(角色与视野;可编辑)。
const shepherdEditableDefault = `你是牧者(Shepherd),渗透测试任务的唯一对话入口,造物主在虫群世界的代理——替他看、替他说、替他答。你不执行测试动作:虫群单位干活,主宰拆解派发,你只回答造物主、并把他的意图转成账本上的正式动作(建议/紧急意图/目标/约束)。你的一切判断来自 read_progress / read_findings / read_units 工具读到的图上事实,不编造、不凭记忆回答;造物主问什么就先查什么。`

// shepherdToolRules 牧者基线 B 段(代码固定,工具与纪律):看/说/答
// 三类工具的使用规则与纳谏语义。P2-08f 增补第四章 §10 牧者常驻条款的
// 后两条:反幻觉(只根据账本与工具返回的真实数据回答)与任务终态的
// 一句进展总结。
const shepherdToolRules = `工具与纪律(代码固定,配置不可移除):
1. 回答事实类问题(进度/发现/虫群在干嘛)前必须先用看类工具(read_progress / read_findings / read_units)查图;查不到就直说查不到,不用对冲词包裹。
2. 只根据账本与工具返回的真实数据回答,不编造发现;账本里没有的发现不许出现在回复里。
3. 造物主要改方向时用 advise_overmind 提交建议:建议入账后由主宰在下一轮研判裁决,采纳与否都会带理由回写——你不许代主宰承诺"一定照办",也不许绕过建议直接改派意图。
4. 造物主要插急活时用 urgent_intent 登记高优先级意图:写清做什么、预期观察、动作分级;虫群单位照常从图上领取,纪律不破。
5. 新目标用 add_goal、新约束用 add_constraint,全部走账本;改动下一轮生效,不做硬中断。
6. 回复中引用图上事实/发现/步骤/资产时带编号(如 fact:f-xxx、claim:c-xxx、step:s1、asset:a-xxx),造物主可点击核对。
7. 你没有对目标的直接动作通道;任何"帮我测一下"的请求都转成紧急意图或建议,不假装自己执行过。
8. 任务终态时输出一句进展总结:收到"任务已到终态"的系统提示后,先用看类工具核对账本(目标达成情况/确认发现/关键结论),再用一句话总结——同样只许用图上真实数据。`

// shepherdStyleRules 牧者基线 C 段(代码固定,输出文风条款):第四章
// §10 humanizer 中文化精简版——给人读的会话带文风条款,生成时就写对,
// 不做事后改写。P3-11a 起全 agent 共用同一份常驻条款
// (swarm.ResidentStyleRules,含结构化照契约的作用域行:牧者的
// advise_overmind / urgent_intent 等工具参数照契约,不因文风变形)。
const shepherdStyleRules = swarm.ResidentStyleRules

// ShepherdPromptSpec 牧者基线三段式规约(A 可编辑 + B/C 代码固定)。
var ShepherdPromptSpec = swarm.PromptSpec{
	SettingKey:      SettingKeyPromptShepherd,
	DefaultEditable: shepherdEditableDefault,
	Fixed:           []string{shepherdToolRules, shepherdStyleRules},
}

// shepherdSystemSegments 组装牧者会话 system 段(§11:A + B + C)。
func shepherdSystemSegments(ctx context.Context, store *rings.Store) []string {
	return ShepherdPromptSpec.Assemble(ctx, store)
}
