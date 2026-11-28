package swarm

import (
	"context"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

// 提示词组装与注入规范(第四章 §11,P2-06d)。位置即优先级:
//
//   - RoE 约束块:system 最顶部,声明"凌驾于下方一切探索启发式"——
//     授权硬边界先于一切方法论被模型读到;
//   - worker 意图块:system 尾部——system 段由引擎每次请求原样重发
//     (agentcore.Session.system),上下文压缩(compaction)只重写消息
//     历史、压不到 system,长会话漂移后意图核心仍在;
//   - planner 易变态势:user turn(既有 snapshot 路径)——system 保持
//     稳定利于前缀缓存,易变内容不进 system。
//
// 三段式组装:段 A 可编辑正文(存通用配置键值表,后台可改)+ 段 B/C
// 代码固定安全段(执行铁律、写回规约等)。结构保证"编辑 DB 正文永远
// 不可能删除代码拥有的段":组装函数无条件把固定段追加在可编辑正文
// 之后,DB 只能覆盖 A 段内容,防配置误操作拆掉安全底线。

// SettingKeyPromptSwarm 虫群基线可编辑正文(A 段)的配置键。
const SettingKeyPromptSwarm = "prompt.swarm.editable"

// workerEditableDefault 虫群基线 A 段默认正文(角色与视野;代码常量,
// 键未设置/空值/读失败时使用)。可编辑的是"你是谁、怎么干活"的正文;
// 铁律与写回规约是代码固定段,不在此处。
const workerEditableDefault = `你是渗透测试虫群单位，在授权测试任务中执行领到的一条意图。任务简报及只读工具提供本任务已有的资产、事实、漏洞和其他意图；用这些信息理解归属与复用已有成果，执行仍围绕领到的意图。`

// workerSafetyRules 虫群基线 B 段(代码固定,安全底线):Q7 消化的
// 提示词红线,第八章第 8 节防线②。条款逐条对应设计定稿:①只做一条
// 意图、意图外线索只回流不深挖;②边发现边写回(R8:候选经受控入口);
// ③职责=把意图干到底(探索→发现→证明→反证自查→交卷,做完即停,
// P5-01①单会话连打)、穷尽本意图但不空转;④交卷门槛(必须真实触发
// 过、拿到可复现证据)+ 反证自查自觉句(无结构化强制字段)+ 定级锚
// 自报(R11/R13 的提示词化);⑤被拒不重试(P1-02 审批语义)。
const workerSafetyRules = `铁律(代码固定,配置不可移除):
1. 只做你领到的这一条意图。瞥见意图之外的线索,只许用 submit_candidate_fact 写回交给主宰,不许自己拐过去挖。
2. 边发现边写回:每取得一条值得保留的观察,立即提交,不要攒到收尾——步数或时间预算可能随时耗尽,攒着的发现会丢。
3. 你的职责是把领到的意图干到底:探索 → 发现 → 证明 → 反证自查 → 交卷,做完即停。意图写“探索”“发现”也包含本意图内的证明、自查与结果写回；写过事实不等于完成，不把已经坐实的问题留给下一只单位专门复核交卷。证据与自查足够时及时交卷或补充原漏洞，不等枚举完所有同类对象；其余工作仍围绕本意图，完成后停止，不因总目标未达成而继续，也不在已探尽方向空转。
4. 普通观察、无漏洞结果和核对通过用 submit_candidate_fact 写回后正常完成意图，不交漏洞单；意图里写“交卷”也不代表普通结果可以作为漏洞。必须本次真实触发过、拿到可复现证据,才许交卷(submit_claim);「版本匹配到 CVE」「看起来可注入」这类疑似只许记为实际观察及证据(submit_candidate_fact)，未验证的猜测不得作为事实提交,不许交卷。交漏洞前自己再想想:这个结果会不会是缓存假象、权限本来就有、环境因素导致的?换个角度验证一下再交,交卷单 countercheck_note 如实写你做了什么检查、为什么没翻车。severity 按定级锚自报,机器只核验证据不校验级别。立判门槛:` + ClaimHarmGate + `。
5. 被 RoE 或审批拒绝的操作不得重试。
6. 本机回环(127.0.0.1 / localhost / ::1)与镜等测试环境自身设施属测试环境,不是目标攻击面:对它们的观察不提交为事实、判断或猜测(入口会拒收并记为环境观察);排查环境问题时的相关现象在总结里说明即可。任务资产清单内的目标(含操作者声明的本机测试靶标)不受此限。
7. 查阅公开资料与其他工具动作走同一判断入口，不声明信息源身份或额外站点许可。资料中提到的组织、人员、文件或地址不自动成为任务资产；实际观察和来源证据如实写回，未验证内容不得当作事实或交卷。`

// workerArtifactRules 虫群基线 C 段(代码固定,中间产物规约):
// §11 语义细则——写回粒度(同线索观察汇总成一条,防图谱无限膨胀;
// 证据原文走内容寻址存储,截断不丢原文)+ 收尾输出形态。
const workerArtifactRules = `写回与收尾规约(代码固定,配置不可移除):
- 事实产生时结合任务简报及只读工具提供的全图信息确定归属：同一漏洞的新证据或影响补充，用submit_candidate_fact的claim_id归到原漏洞；已坐实的独立漏洞正常submit_claim，不能只记事实就遗漏申报。普通观察仍是事实。同一根因不因新证据、入口或标题变化另报；已有信息足够时直接写回，不增加专门判断或探测。
- 写回粒度:同一条线索的多次观察汇总成一条候选提交,写明观察之间的关联,不要逐次各提一条——图谱按线索生长,不按调用次数膨胀;证据原文经内容寻址存储留档,提交界面所见被截断不丢原文。
- 已返回且未变化的材料先复用会话记录，不为重述已知信息重复调用；需要核对当前状态、条件变化或补缺证据时照常读取。旧工具输出有摘录和存档引用时，细节按需用 read 读取原文，不凭摘录猜测，也不重新执行原操作代替回读。
- 完成后输出简短总结:做了什么、提交了哪些事实与交卷、有什么没做成。`

// ResidentStyleRules 全部角色共用的自然语言生成要求。按提问组织结果，
// 概览与证据分工；在生成时写清，不在生成后裁句。工具参数、JSON 和
// 账本引用保持原契约。humanizer skill 另管操作者明确要求的文本改写。
const ResidentStyleRules = `输出文风(代码固定,配置不可移除):
写给正在操作系统的人。回复服务于这一次提问，不把读到的全部材料转述一遍。进度问题直接说当前结果与剩余问题；问漏洞就说问题和实际影响；问一条事实就解释该条观察和它能支持的结论。默认用一个短段落，确有不同要点再分段；只有明确索取证据、操作过程或完整报告时才逐项展开。问“具体是什么”需要把事情说明确，不等于要求抄录请求与报文。证据和执行过程已有独立入口，答复只保留理解结论必需的细节与可点击引用。回答完所问即结束，不自行附加测试计划、扩展方向或征求继续的套话。
日常进度、单条结果解释和插话答复，以两三句、约60至120个中文字为写作目标：直接说结果，再说理解它所必需的条件和范围。这个长度是写作参考，不是截断规则；复杂问题、明确索取细节或完整报告时按材料充分展开，不为凑字数丢掉关键事实。
用平实中文，把名称、说明、结果分清。不写开场白、套话或重复总结；不把长材料压成一大段，也不把正常句子压成自造名词。
下面只示意表达方式与信息密度，不是当前任务的事实，不能照搬结论：
问“现在有什么结果”：已确认1条漏洞：公开备份含有可用的后台凭据。尚未验证登录后的功能和数据。
问“这条观察说明什么”：未登录时，后台拒绝了访问。目前只检查了首页，不能据此判断其他入口也受保护。
问“这个漏洞影响在哪里”：无需登录即可下载备份，并用其中的凭据进入后台。已验证后台访问，尚未验证数据读写。
概览让人看懂做了什么、看到了什么、还有什么未确定；命令、逐项请求、报文与冗长参数放在对应证据或执行内容里。地址与对象首次出现时说清，之后在同一上下文内可简写，不在每句话重复完整地址。行业概念在影响理解时用一句普通话解释，不写成教材。数字与时间用可核对的实际值；不知道就说不知道，不把未验证说成没有问题，也不把观察说成已确认漏洞。
只根据账本和工具返回的真实数据回答，不编造发现或执行结果；引用节点仍用系统支持的可点击引用格式。系统状态与操作使用操作者语言，牧者/主宰/虫群/RoE 等名称保留。不贴机器字段、内部报错或编号解释，不用 emoji、服务腔、营销词和空泛评价。
作用域:以上只约束给人读的叙述、总结、理由与回答；结构化输出照契约——工具参数与 JSON 的字段名、结构、取值、标识和必要定位信息保持准确，不因文风删改业务信息。`

// HumanReplyPrompt 将本轮阅读任务放在提问之后。账本仍保存操作者原话，
// 这里只装配模型输入；不改工具参数、权限或任务执行要求。
func HumanReplyPrompt(text string) string {
	return text + "\n\n【本次答复要求】\n本次只用两三句完整的话直接回答，写作目标60至120字，省去验证报文与过程清单，保留理解结论必需的条件、来源引用与已验证范围。普通观察、正常认证与漏洞利用分别如实描述：使用泄露的有效凭据登录，不等于绕过了认证逻辑；没有看到功能入口，不等于系统没有功能。若操作者明确要求详细过程或完整报告，则按其要求充分展开。仅约束给人看的说明，不改变工具调用、参数或应执行的任务。"
}

// StructuredFieldStyle 拆解意图与交卷摘要共用的字段要求。执行信息保留
// 必要定位与边界；独立显示名称负责列表阅读，不用字数限制裁掉业务信息。
const StructuredFieldStyle = "用平实中文讲清本条要做什么或实际观察到什么；保留执行所需的目标定位、数字和必要条件，不在一句话里复述全部参数或枚举清单；名称由独立显示字段承担，不为凑字数删掉关键业务信息。状态、操作使用操作者语言"

// ClaimHarmGate 立判断危害门槛(P4-30 ①,问题清单 #51):收割纪律条款。
// 虫群 submit_test_claim、主宰组合立判(combined_claims)与存量复核共用
// 同一份——仅当声明成立会带来实际危害或边界突破才立判断;纯前置条件、
// 环境特性、无害观察只记事实(作为组合判断环节引用时自然呈现)。工具
// 说明、虫群铁律、主宰纪律三处引用此常量,防漂移(同 StructuredFieldStyle
// 的共用句纪律)。
const ClaimHarmGate = "只有判断成立会给目标带来实际危害或边界突破——未授权访问数据/功能、可执行代码、越过鉴权、拒绝服务、泄露敏感信息——才立判断;纯前置条件(如验证码可解)、环境特性(如端口持续响应)、无害观察一律不立判断,只提交事实(它们作为组合判断环节引用时自然呈现)"

// SeverityAnchor 定级校准锚(P4-30 ②,问题清单 #51;P5-01① 起随提示词
// 给虫群自报级别):交卷自报与收割定稿共用——影响面定级只跟直接影响
// 走,信息性(无直接危害)不定级不立。交卷工具说明与虫群铁律引用此
// 常量,防漂移。
const SeverityAnchor = "定级只跟直接影响走:critical=系统级权限或大量敏感数据;high=管理员或敏感数据访问;medium=普通业务越权或有限敏感信息;low=需苛刻前提才有影响;无直接危害的信息性观察不定级也不立判断"

// WorkerPromptSpec 虫群基线三段式规约(A 可编辑 + B/C/D 代码固定;
// D 段常驻文风:虫群执行叙述与舵杆回执解释按人话写,工具参数照契约)。
var WorkerPromptSpec = PromptSpec{
	SettingKey:      SettingKeyPromptSwarm,
	DefaultEditable: workerEditableDefault,
	Fixed:           []string{workerSafetyRules, workerArtifactRules, ResourcePrinciples, ResidentStyleRules},
}

// PromptSpec 三段式组装规约:SettingKey 指向 A 段在通用配置键值表的
// 键(空 = 无可编辑段);DefaultEditable 为 A 段默认正文;Fixed 为
// 代码固定安全段(B/C…,顺序即追加顺序)。
type PromptSpec struct {
	SettingKey      string
	DefaultEditable string
	Fixed           []string
}

// Assemble 三段式组装:返回 [A(键值覆盖或默认), B, C, …]。键空值、
// 读失败一律退代码默认(fail-safe:配置故障不挡执行,更不可能拆掉
// 安全底线)。store 为 nil(纯展示/测试)同样退默认。
func (p PromptSpec) Assemble(ctx context.Context, store *rings.Store) []string {
	body := p.DefaultEditable
	if p.SettingKey != "" && store != nil {
		if e, ok, err := store.Setting(ctx, p.SettingKey); err == nil && ok {
			if v := strings.TrimSpace(e.Value); v != "" {
				body = v
			}
		}
	}
	return append([]string{body}, p.Fixed...)
}

// roeConstraintHeader RoE 约束块的固定抬头与凌驾声明(§11:system
// 最顶部并声明"凌驾于下方一切探索启发式")。
const roeConstraintHeader = "【授权约束 · RoE】本块为授权硬边界,凌驾于下方一切探索启发式;越出本块的规划与动作一律不做。"

// RenderRoEConstraint is shared by worker and planner prompts. Inventory
// membership is separate from actual action permission; notes retain operator
// requirements without a second classifier or per-host permission mechanism.
func RenderRoEConstraint(lead string, allowed, denied []string, excluded []rings.ExcludedAssetView, notes string) string {
	var b strings.Builder
	b.WriteString(roeConstraintHeader)
	if lead != "" {
		b.WriteString(lead)
	}
	b.WriteString("\n- 工具边界: ")
	switch {
	case len(denied) > 0 && len(allowed) > 0:
		fmt.Fprintf(&b, "禁止 %v;仅允许 %v", denied, allowed)
	case len(denied) > 0:
		fmt.Fprintf(&b, "禁止 %v;其余工具可用", denied)
	case len(allowed) > 0:
		fmt.Fprintf(&b, "仅允许 %v", allowed)
	default:
		b.WriteString("未设白名单与黑名单(工具全集)")
	}
	b.WriteString("\n- 动作判定:当前启用的硬规则命中直接拒绝，其余按实际动作与任务边界判断；需要操作者决定时才审批。批准仅适用于该次调用。\n- 资产加入:仅决定资产列表与后续规划，不加入不等于禁止访问，观察与证据仍须写回。\n")
	b.WriteString("- 授权范围: 仅限本任务授权目标;本系统仅供授权测试。\n")
	if notes != "" {
		b.WriteString("- 造物主注意事项(对做法的要求,须遵守;未提及的做法你自主选择):\n")
		for _, line := range strings.Split(notes, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintf(&b, "  · %s\n", line)
			}
		}
	}
	if len(excluded) > 0 {
		parts := make([]string, 0, len(excluded))
		for _, a := range excluded {
			parts = append(parts, fmt.Sprintf("%s(%s)", a.ID, a.Title))
		}
		fmt.Fprintf(&b, "- 范围外资产(已从授权范围摘除,不测不派): %s\n", strings.Join(parts, "、"))
	}

	return b.String()
}

// roeConstraintBlock 虫群侧 RoE 约束块(从任务包的领取时授权渲染;
// 资产摘除是规划级约束——入口守卫保证摘除资产不会再被派发覆盖它的
// 意图,单位领到的任务包不会锚定它们,虫群侧不重复渲染;待批清单
// (P4-44)从任务包冻结副本渲染——单位自发越界测待批资产前读到锁定
// 声明,硬拦在权限管线兜底;注意事项(P4-49)同样从领取时授权渲染)。
func roeConstraintBlock(roe RoE) string {
	return RenderRoEConstraint(
		"你的探索手段受此框定。", roe.AllowedTools, roe.DeniedTools, nil, roe.CreatorNotes)
}

// 意图块/锚定资产/启动指令(§11 不可变块,追加顺序固定:意图块 →
// 意图锚定资产 → 启动指令,启动指令永远是 system 最后一句)。

// intentBlock 意图块:system 尾部的意图核心(本步骤意图 + 预期观察)。
// compaction 压不到 system,长会话压缩后意图核心仍在。
func intentBlock(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【本步骤意图 · 只做这一条】\n")
	fmt.Fprintf(&b, "意图: %s\n", strings.TrimSpace(pack.StepIntent))
	if pack.ExpectedObservation != "" {
		fmt.Fprintf(&b, "预期观察: %s(验收参考,非产出上限;预期外发现按新进事实通道回传)\n",
			strings.TrimSpace(pack.ExpectedObservation))
	}
	if pack.ActionLevel != "" {
		fmt.Fprintf(&b, "意图分级: %s(背景信息，实际调用独立判断)\n", string(pack.ActionLevel))
	}
	return b.String()
}

// anchorAssetsBlock 意图锚定资产:本意图的工作对象(任务包必要资产节
// 的锚点形态;完整七要素仍在首条 user turn 的任务包全文里)。
func anchorAssetsBlock(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【意图锚定资产】\n")
	if len(pack.Assets) == 0 {
		b.WriteString("(无绑定资产)\n")
		return b.String()
	}
	for _, a := range pack.Assets {
		fmt.Fprintf(&b, "- %s [%s] %s\n", a.ID, a.Class, a.Title)
	}
	return b.String()
}

// startupInstruction 启动指令(§11:永远是 system 最后一句)。
const startupInstruction = "【启动指令】现在开始执行上述意图:先读任务包全文,再动手;全程遵守上方铁律与写回规约。"

// WorkerSystemSegments 组装虫群单位 system 段(§11 全规范):
//
//	[0] RoE 约束块(最顶,凌驾声明)
//	[1] A 段 可编辑正文(键值覆盖或代码默认)
//	[2] B 段 执行铁律(代码固定)
//	[3] C 段 写回规约(代码固定)
//	[4] D 段 常驻文风(代码固定;自然语言按人话写,工具参数照契约)
//	[5] 意图块(system 尾,compaction 压不掉)
//	[6] 意图锚定资产
//	[7] 启动指令(最后一句)
//
// 任务包七要素全文仍是首条 user turn;system 尾只锚定意图核心——被
// 压缩的是 user turn 的细节,压不掉的是"这条单位该干什么"。
func WorkerSystemSegments(ctx context.Context, store *rings.Store, roe RoE, pack *TaskPack) []string {
	segs := []string{roeConstraintBlock(roe)}
	segs = append(segs, WorkerPromptSpec.Assemble(ctx, store)...)
	segs = append(segs, intentBlock(pack), anchorAssetsBlock(pack), startupInstruction)
	return segs
}
