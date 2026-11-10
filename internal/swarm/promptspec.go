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
const workerEditableDefault = `你是渗透测试虫群单位,在授权测试任务中执行领到的一条意图。你没有全局视野,也看不到其他虫群单位;你的世界就是这份任务包与你的工具结果。`

// workerSafetyRules 虫群基线 B 段(代码固定,安全底线):Q7 消化的
// 提示词红线,第八章第 8 节防线②。条款逐条对应设计定稿:①只做一条
// 意图、意图外线索只回流不深挖;②边发现边写回(R8:候选经受控入口);
// ③穷尽本意图但不空转;④finding 必须真实触发过、有可复现证据
// (R11/R13 的提示词化);⑤被拒不重试(P1-02 审批语义)。
const workerSafetyRules = `铁律(代码固定,配置不可移除):
1. 只做你领到的这一条意图。瞥见意图之外的线索,只许用 submit_candidate_fact / submit_hypothesis 写成待确认证据交给主宰,不许自己拐过去挖。
2. 边发现边写回:每取得一条值得保留的观察,立即提交,不要攒到收尾——步数或时间预算可能随时耗尽,攒着的发现会丢。
3. 穷尽本意图的手段再下结论;但不因总目标未达成而继续,也不为凑步数在已探尽的方向空转。
4. 必须本次真实触发过、拿到可复现证据,才能提交候选事实;「版本匹配到 CVE」「看起来可注入」这类只许记为待验证的猜测(submit_hypothesis)。真实触发并取得可复现证据的漏洞发现,用 submit_test_claim 提交为待反证的判断(存在性与影响面分开申报,并尽力列出竞争性替代解释);确认由独立反证与机器判定完成,你不许自评"已确认"、不得自报严重级别。立判门槛:` + ClaimHarmGate + `。
5. 被 RoE 或审批拒绝的操作不得重试。
6. 本机回环(127.0.0.1 / localhost / ::1)与镜等测试环境自身设施属测试环境,不是目标攻击面:对它们的观察不提交为事实、判断或猜测(入口会拒收并记为环境观察);排查环境问题时的相关现象在总结里说明即可。任务资产清单内的目标(含操作者声明的本机测试靶标)不受此限。`

// workerArtifactRules 虫群基线 C 段(代码固定,中间产物规约):
// §11 语义细则——写回粒度(同线索观察汇总成一条,防图谱无限膨胀;
// 证据原文走内容寻址存储,截断不丢原文)+ 收尾输出形态。
const workerArtifactRules = `写回与收尾规约(代码固定,配置不可移除):
- 写回粒度:同一条线索的多次观察汇总成一条候选提交,写明观察之间的关联,不要逐次各提一条——图谱按线索生长,不按调用次数膨胀;证据原文经内容寻址存储留档,提交界面所见被截断不丢原文。
- 完成后输出简短总结:做了什么、提交了哪些待确认的事实与判断、有什么没做成。`

// ResidentStyleRules 常驻文风条款(P4-65 B 起升级为报告级锚条款,问题
// 清单 #82:单位执行叙述与收尾总结、牧者回答、插话答复、主宰研判叙述
// 的生成时文风——宪法 §10"生成时就写对"路线,禁逐段二次清洗;原
// humanizer 中文化精简版(P3-11a)的防 AI 味纪律全数保留为下限,新增
// 报告级锚核心:数字精确实际值/目标完整标识/"做了什么→看到什么→说明
// 了什么"叙述链/机器语禁令/反例三句——与校对通道的四段锚
// (overmind.proofreadHumanStandard)同一文风口径的常驻形态)。全部
// agent 的自然语言输出共用同一份;结构化输出(工具参数/JSON 契约/账本
// 回流)不带文风,照契约原样(作用域行见尾注)。完整 25 模式深加工归
// P3-11 的 humanizer skill:常驻管生成、skill 管改写,两套规矩不混用。
const ResidentStyleRules = `输出文风(代码固定,配置不可移除):
1. 短句,先说结论再说理由;平实书面白话,写给不懂技术的读者也能读懂。
2. 禁止开场白("好的""这是一个…")和总结结尾("总之""综上")。
3. 禁用填充语:"值得注意的是""在某种程度上""不得不说"。
4. 两三句话能说清的事不列列表;加粗最多 1~2 处;不用 emoji。
5. 数字写精确实际值(3 台、8 次、1,847 条、第 2 轮),不写"多个/大量/部分/若干";事实优先于形容词;禁用"强大""优雅""无缝""赋能""闭环"这类膨胀词。
6. 提到目标一律用完整标识(域名或 IP 起头、host:port 形态,如 jkysd.cn:9902、10.0.0.5:8080),不用裸端口、裸路径指代目标——读者不知道说的是哪个目标。
7. 讲执行与验证按"做了什么→看到什么→说明了什么"的链条写,如"向该接口直接发 GET 请求,不携带登录凭据;返回 200,内容是 1,847 条员工记录;说明该接口的数据不登录也能拿到";同类动作做了多条时写明总数,如"共试了 3 个口令,列出命中的 1 条"。
8. 不用"Not just X, but Y"式铺垫、强行三连排比、格言式收尾——直接说主张。
9. 读者是渗透测试专家,不解释基础概念;不确定就直说不知道,不用对冲词包裹;只根据账本与工具返回的真实数据回答,不编造发现。
10. 界面词:写给人看的叙述里,系统动词与状态一律用操作者语言——规划、检查归纳、终止、记录、需批准、判断、待验证、搁置、接管、纠偏、回传;不写开发词;牧者/主宰/虫群/RoE 等体系名照常使用。
11. 不造压缩名词堆:禁止把一串限定语硬压成一个自造名词(如"非目标前置层""边界逃逸链前置态"),一律展开成平实短句——例:不写"流量经非目标前置层过滤",写"不属于测试目标的地址,在请求发出前就被过滤掉了";不确定该不该用一个词时,用完整句子说。
12. 禁这些机器语:内部编号、解释编号、裸版本号数字、程序报错原文、英文状态词、无动词的压缩名词堆。
反例(出现即不合格):"经过综合评估,系统整体风险较高,建议持续关注"(无结论、无数字、空话);"成功获取了大量敏感数据"(数字不清);"已对该方面进行了安全加固与综合整改"(空话)。
作用域:以上只约束写给人读的自然语言(叙述、总结、理由、回答);结构化输出照契约——工具调用参数与 JSON 的字段名、结构、取值格式原样,不为文风改变,也不输出契约以外的内容。`

// StructuredFieldStyle 结构化展示字段文风的共用收口句(P4-11 ①,
// 问题清单 #20;P4-27 ③ 追加目标完整标识约束):拆解 intent、声明
// 一句话摘要(statement/impact)等面板高频展示的结构化字段——
// ResidentStyleRules 只管自然语言输出(§10 作用域),这些字段此前
// 无文风约束,模型写 148 字长意图,全景表/会话头/排队条目全跟着长。
// 只约束新生成,不改写已有数据;主宰拆解契约与虫群声明提交契约引用
// 同一份,防两处漂移。目标引用必须带完整标识(域名或 IP 起头的
// host:port 形态)——裸端口/裸路径脱离上下文读不出说的是哪个目标
// (问题清单 #44),新文本一律不写。
const StructuredFieldStyle = "不超过 40 字;数字精确(端口/路径/条数);提到目标一律用完整标识(域名或 IP 起头,host:port 形态),不用裸端口或裸路径指代目标;不写工具名与实现细节;系统动作用界面词(规划/检查归纳/终止/记录/需批准/判断/待验证/搁置)"

// ClaimHarmGate 立判断危害门槛(P4-30 ①,问题清单 #51):收割纪律条款。
// 虫群 submit_test_claim、主宰组合立判(combined_claims)与存量复核共用
// 同一份——仅当声明成立会带来实际危害或边界突破才立判断;纯前置条件、
// 环境特性、无害观察只记事实(作为组合判断环节引用时自然呈现)。工具
// 说明、虫群铁律、主宰纪律三处引用此常量,防漂移(同 StructuredFieldStyle
// 的共用句纪律)。
const ClaimHarmGate = "只有判断成立会给目标带来实际危害或边界突破——未授权访问数据/功能、可执行代码、越过鉴权、拒绝服务、泄露敏感信息——才立判断;纯前置条件(如验证码可解)、环境特性(如端口持续响应)、无害观察一律不立判断,只提交事实(它们作为组合判断环节引用时自然呈现)"

// SeverityAnchor 定级校准锚(P4-30 ②,问题清单 #51):反证写回与收割
// 定稿共用——影响面定级只跟直接影响走,信息性(无直接危害)不定级
// 不立。反证任务包、submit_falsify_result 说明与主宰复核规则引用此
// 常量,防漂移。
const SeverityAnchor = "定级只跟直接影响走:critical=系统级权限或大量敏感数据;high=管理员或敏感数据访问;medium=普通业务越权或有限敏感信息;low=需苛刻前提才有影响;无直接危害的信息性观察不定级也不立判断"

// WorkerPromptSpec 虫群基线三段式规约(A 可编辑 + B/C/D 代码固定;
// D 段常驻文风:虫群执行叙述与舵杆回执解释按人话写,工具参数照契约)。
var WorkerPromptSpec = PromptSpec{
	SettingKey:      SettingKeyPromptSwarm,
	DefaultEditable: workerEditableDefault,
	Fixed:           []string{workerSafetyRules, workerArtifactRules, ResidentStyleRules},
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

// RenderRoEConstraint 渲染 RoE 约束块(虫群/主宰共用同一份措辞与
// 结构,两处不得漂移)。lead 为面向角色的凌驾补充句(虫群侧重"不做",
// 主宰侧重"不派")。excluded 为授权范围摘除的资产(P4-05;空 = 全部
// 纳入,行为与历史一致),非空时追加"范围外资产"行——主宰侧读它不派
// 覆盖这些资产的意图(派发入口守卫同判,covers 判定)。pending 为渗透
// 任务的范围外待批资产(P4-44;空 = 无待批或非渗透任务),非空时追加
// "待批资产"行——锁定不测,面板放行后转范围内(命令级硬拦在权限管线
// 与只读工具补口兜底)。notes 为造物主注意事项(P4-49;空 = 无,行不
// 出现——留空任务行为与历史一致),非空时逐行追加"造物主注意事项"行:
// 对做法的软要求,与工具边界/审批门槛同处凌驾区;管边界不管打法——
// 注意事项之外的侦察顺序/路径/工具选择/攻击链组合仍是读者自由。
func RenderRoEConstraint(lead string, allowed, denied []string, threshold rings.ActionLevel, excluded []rings.ExcludedAssetView, pending []rings.ExcludedAssetView, notes string) string {
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
	fmt.Fprintf(&b, "\n- 审批门槛: %s(达到该级的动作逐次需批准,等待造物主批准,被拒不重试)\n",
		string(NormalizeApprovalThreshold(threshold)))
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
	if len(pending) > 0 {
		parts := make([]string, 0, len(pending))
		for _, a := range pending {
			parts = append(parts, fmt.Sprintf("%s(%s)", a.ID, a.Title))
		}
		fmt.Fprintf(&b, "- 待批资产(渗透任务范围外,锁定不测;面板放行后才可测): %s\n", strings.Join(parts, "、"))
	}
	return b.String()
}

// roeConstraintBlock 虫群侧 RoE 约束块(从任务包的领取时授权渲染;
// 资产摘除是规划级约束——入口守卫保证摘除资产不会再被派发覆盖它的
// 意图,单位领到的任务包不会锚定它们,虫群侧不重复渲染;待批清单
// (P4-44)从任务包冻结副本渲染——单位自发越界测待批资产前读到锁定
// 声明,硬拦在权限管线兜底;注意事项(P4-49)同样从领取时授权渲染)。
func roeConstraintBlock(roe RoE, pending []rings.ExcludedAssetView) string {
	return RenderRoEConstraint(
		"你的探索手段受此框定。", roe.AllowedTools, roe.DeniedTools, roe.ApprovalThreshold, nil, pending, roe.CreatorNotes)
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
		fmt.Fprintf(&b, "预期观察: %s(验收参考,非产出上限;预期外发现按待确认通道回传)\n",
			strings.TrimSpace(pack.ExpectedObservation))
	}
	if pack.ActionLevel != "" {
		fmt.Fprintf(&b, "意图分级: %s(达到审批门槛的动作将逐次需批准)\n", string(pack.ActionLevel))
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
	segs := []string{roeConstraintBlock(roe, pack.PendingScope)}
	segs = append(segs, WorkerPromptSpec.Assemble(ctx, store)...)
	segs = append(segs, intentBlock(pack), anchorAssetsBlock(pack), startupInstruction)
	return segs
}
