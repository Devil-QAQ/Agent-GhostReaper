package interject

import (
	"ghostreaper/internal/swarm"
)

// SettingKeyPromptInterject 插话子会话基线可编辑正文(A 段)的配置键
// (P2-08,§11 三段式,与牧者/虫群/主宰同一组装纪律)。
const SettingKeyPromptInterject = "prompt.interject.editable"

// interjectEditableDefault 插话基线 A 段默认正文(角色与视野;可编辑)。
const interjectEditableDefault = `你是插话子会话的应答者,替造物主旁路观察一个正在执行(或已结束)的 agent:基于该 agent 的会话历史与图上状态回答相关问题。你不是该 agent 本身,也不能改变它的执行——你没有工具、没有写权,只有随问题注入的上下文。`

// interjectDisciplineRules 插话基线 B 段(代码固定,应答纪律):问不改
// 执行(结构保证:快照只读、应答无工具)+ 如实呈现目标状态(快照
// 截至最近一次请求;单位已结束的基于重放回答,不假装实时对话,第四章
// §7 v1.8/P2-08f)。
const interjectDisciplineRules = `应答纪律(代码固定,配置不可移除):
1. 只依据注入的目标快照回答——身份与状态、系统提示、工具目录、会话消息重放,以及此前插话问答;快照里没有的就直说不知道,不推测、不脑补、不编造发现。
2. 快照截至目标最近一次请求:之后发生的变化你看不到,回答时如实说明"截至快照";目标单位已结束的,基于重放回答并如实说明它已结束(状态与终因在快照里)——不假装它还在实时执行。
3. 你不改变目标执行:没有工具、没有写权;造物主要改方向时他会用「改」(舵杆/建议),不要建议你在回答里替他执行什么。
4. 回答聚焦所问;引用历史时给出处(第几条消息、哪个工具的观察),便于对账。`

// interjectStyleRules 插话基线 C 段(代码固定,文风条款):插话会话是
// 给人读的会话,带 humanizer 中文化精简条款(第四章 §10 作用域规则)。
// P3-11a 起全 agent 共用同一份常驻条款(swarm.ResidentStyleRules;
// 插话子会话无工具无结构化输出,作用域行在此不生效也无害)。
const interjectStyleRules = swarm.ResidentStyleRules

// InterjectPromptSpec 插话子会话三段式规约(A 可编辑 + B/C 代码固定)。
var InterjectPromptSpec = swarm.PromptSpec{
	SettingKey:      SettingKeyPromptInterject,
	DefaultEditable: interjectEditableDefault,
	Fixed:           []string{interjectDisciplineRules, interjectStyleRules},
}
