package swarm

import (
	"fmt"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/vault"
)

// 虫群基线提示词(P2-06d 起按第四章 §11 三段式组装,正文拆入
// promptspec.go:workerEditableDefault 可编辑正文 + workerSafetyRules
// 执行铁律 + workerArtifactRules 写回规约 + ResidentStyleRules 常驻
// 文风;system 段组装入口 WorkerSystemSegments——RoE 约束块置顶、
// 意图块/锚定资产/启动指令收尾)。P3-11a 起执行会话带常驻文风条款
// (第四章 §10 作用域 v2:执行叙述/回执解释按人话写,工具参数与
// 结构化写回照契约)。

// Budget 运行预算(第四章 §6 v2,P4-01:单位-意图为雇佣关系,领了干完
// 为止)。max_turns / run_seconds **默认 0 = 不限**(与引擎 0=off 语义
// 统一);面板可配上限随时配回(键值表覆盖,见 Claim 的消费点),在飞
// 单位维持领取时预算——覆盖只对新领取的单位生效。配了上限时到线不是
// 终点:引擎 Settlement 收尾把发现写回后**原会话 Resume 续跑**、每段
// 预算逐段重算(与受阻恢复同语义,见 drive 的预算续跑);exhausted
// 落地仅剩任务总闸场景(R16 语义保留,由执行器按终因落地)。
type Budget struct {
	MaxTurns       int // 段内最大模型轮次(0 = 不限)
	RunSeconds     int // 段内墙钟上限(0 = 不限)
	WrapupMaxTurns int // 收尾环节轮数(0 → 默认 2;只读默认,不进全局配置)
}

const defaultWrapupMaxTurns = 2

func (b Budget) withDefaults() Budget {
	if b.WrapupMaxTurns <= 0 {
		b.WrapupMaxTurns = defaultWrapupMaxTurns
	}
	return b
}

// DefaultBudget 返回默认运行预算(0 = 不限;设置面板只读展示用)。
func DefaultBudget() Budget {
	return Budget{}.withDefaults()
}

// wrapupPrompt 预算耗尽时注入的收尾指令(Settlement):把已取得的发现
// 写回图再结束——预算耗尽是暂停接力,不是失败(R16)。
const wrapupPrompt = `运行预算即将耗尽。立即收尾:把本次已取得、尚未提交的可保留发现用 submit_candidate_fact / submit_hypothesis 写回,然后用一句话说明进度与剩余工作。不要发起新的探测。`

// TaskPack 最小任务包七要素(第四章 §2):虫群单位换实例、换上下文
// 仍能接着同一条工作线继续。Claim 时从图上构建并绑定图版本,
// 执行前复检(R14)以 GraphVersion 为准。
type TaskPack struct {
	TaskID string
	StepID string

	// ① 当前目标
	GoalID    string
	GoalTitle string
	// ② 本步骤意图与预期观察(预期观察是验收参考而非产出上限)
	StepIntent          string
	ExpectedObservation string
	// ActionLevel 意图四级分类(第四章 §3;P2-06b 三级处置 v2:四级用于
	// 展示/留痕与审批门槛比照):派发时由主宰标注,Claim 时从
	// step.detail.action_level 读入任务包。
	ActionLevel rings.ActionLevel
	// HarmClass 拒绝线的危害声明(P2-06b,第四章 §3 v2):数据销毁/
	// 拒绝服务类——无论门槛多宽都直接拒绝(非常严重项),面板可研判后
	// 放行。从 step.detail.harm_class 读入,缺省空=普通。
	HarmClass rings.HarmClass
	// ExternalServices 外部服务声明(P4-67 ⑤ 规划前置):意图拆解契约的
	// 可选字段——语义化描述本步骤将用的公开信息服务类型(不枚举站名),
	// 如"代码托管平台的公开仓库"。执行中命中声明的服务形态 → 免分诊
	// 放行(声明与 host 的匹配由分诊层一次性判定后入缓存);清单外新
	// 地址照走分诊。从 step.detail.external_services 读入,空=无声明。
	ExternalServices string
	// ③ 相关可信事实与证据(仅 confirmed 事实;证据引用随 detail)
	Facts []FactBrief
	// ④ 必要资产与权限(本步骤 covers 的资产 + RoE 边界)
	Assets        []AssetBrief
	Authorization RoE
	// ⑤ 前序尝试及失败原因(同目标下的其他 Step)
	PriorAttempts []AttemptBrief
	// ⑥ 授权与预算
	Budget Budget
	// AnchorEverySteps 循环内重锚定间隔(P2-05,第八章第 8 节防线③):
	// 每 N 个工具调用边界重注入一次任务包核心。任务未显式设定(0/缺省)
	// 时 Claim 取 Agent 页全局默认(P2-08d 键值覆盖),再退代码默认
	// DefaultAnchorEverySteps(10,guard 归一);显式值随任务 created
	// 事件 detail 透出(anchor_every_steps),面板建任务表单可调。
	AnchorEverySteps int
	// TaskDeadline 任务收尾线(P2-04,第四章 §6 任务级总闸):创建时刻+
	// 整体超时。零值=不限。单位墙钟预算取 min(运行预算, 距此线剩余)——
	// 到点在跑单位就地进收尾(Settlement 写回发现),不硬杀不打断审计。
	TaskDeadline time.Time
	// ⑦ 当前图版本(R14 执行前复检锚点)
	GraphVersion int64

	// Credentials 可用目标凭据清单(P3-09 目标凭据管理:凭据是配置而非
	// 问答)。条目只携带名称/用户名/环境变量名——秘密在 bash 子进程的
	// GR_CRED_* 环境变量里,不进任务包与提示词正文。
	Credentials []vault.Brief

	// Falsify 反证任务上下文(P1-08 + P2-02,第三章 §5):本 Step 为主宰
	// 派发的反证意图(detail.kind=falsify)时非 nil。R12:反证单位与发现
	// 单位必不相同——发现者不得自证,由领任务包时的绑定与入口校验双重
	// 保障。P2-02:携带轮次与本轮待检的存活竞争解释清单(收割派发时
	// 从账本算出并冻结进 step.detail,前几轮已排除的不重复派)。
	Falsify *FalsifyBrief

	// Verify 假设验证任务上下文(P2-03,第四章 §5):本 Step 为主宰收割
	// 评估"值得跟"派发的验证意图(detail.kind=verify)时非 nil。R12:验证
	// 单位与提出单位必不相同——由领任务包时的绑定与写回入口校验双重保障。
	Verify *HypothesisBrief

	// TaskType 任务类型(P4-44 ②:created 事件 detail 定格,建后不可改):
	// 渗透/红队;空 = 存量未标(行为等同红队)。
	TaskType string

	// ScopeClosure 种子闭包(P4-44 ①③):种子/手动/已放行资产的主机锚点
	// 集(给域名=域名级授权,给 IP=服务器级授权)。仅渗透任务在领取时
	// 构建并冻结进任务包——权限管线据此对命令目标做范围外硬拦;红队/
	// 未标任务为 nil(现状行为,不拦)。冻结语义与授权边界同款:面板
	// 放行/新增资产对下一批领取的单位生效。
	ScopeClosure *rings.ScopeClosure

	// PendingScope 待批资产清单(P4-44 ③:渗透任务的范围外发现资产,
	// 锁定不测)。领取时冻结,RoE 约束块据此渲染"待批资产"行;空 = 无。
	PendingScope []rings.ExcludedAssetView
}

// HypothesisBrief 假设验证任务上下文:被验证假设的冻结要素。
type HypothesisBrief struct {
	HypothesisID string
	Statement    string // 假设陈述
	Basis        string // 提出者依据(允许弱依据)
	ProposerUnit string // 提出单位(R12 独立性锚点,仅作背景告知)
}

// FalsifyBrief 反证任务上下文:被反证声明的冻结要素 + 本轮目标。
type FalsifyBrief struct {
	ClaimID        string
	Round          int    // 反证轮次(P2-02;旧步骤缺省 0 显示为 1)
	Statement      string // 存在性声明
	Impact         string // 影响面声明(与存在性分开反证)
	Vulnclass      string
	Evidence       string // 发现者证据截头或引用
	DiscovererUnit string // 发现单位(R12 独立性锚点,仅作背景告知)
	// Alternatives 本轮须逐条检验的竞争解释(存活清单;全量冻结清单
	// 在声明 detail,重开后回到全量)。空 = 该声明无竞争解释语义
	//(P1-08 旧声明)。
	Alternatives []AltBrief
	// Chain 攻击链环节(P2-06 组合发现:链式 TestClaim 的血缘链条,
	// 反证单位须看到链条才能独立重放组合效果)。空 = 单线索声明。
	Chain []ChainBrief
}

// ChainBrief 攻击链环节条目(P2-06,第四章 §1 五问之"两条线索组合成
// 新的边界问题"):组合声明的每条 Fact 环节(血缘经 derived_from 边
// 挂在声明名下,此处为任务包呈现形态)。
type ChainBrief struct {
	FactID string
	Role   string // 环节角色(入口/凭据/横移/目标等,发现者语言)
	Title  string // 环节事实陈述
}

// AltBrief 任务包里的单条竞争解释。
type AltBrief struct {
	ID    string
	Label string
}

// FactBrief 任务包里的可信事实条目。
type FactBrief struct {
	ID       string
	Title    string
	Evidence string // 证据截头或引用
}

// AssetBrief 任务包里的资产条目。
type AssetBrief struct {
	ID    string
	Title string
	Class string
}

// AttemptBrief 前序尝试条目。
type AttemptBrief struct {
	StepID string
	Intent string
	Status string // done|exhausted|failed|...
	Reason string // 失败/耗尽原因(detail.fail_reason,可为空)
}

// Render 把七要素任务包渲染为首条用户提示。各节固定标头,便于
// 测试断言与模型解析。
func (p *TaskPack) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "【任务包 · 七要素】\n")
	fmt.Fprintf(&b, "任务 %s / 步骤 %s\n\n", p.TaskID, p.StepID)

	b.WriteString("一、当前目标\n")
	if p.GoalID != "" {
		fmt.Fprintf(&b, "  %s (%s)\n", p.GoalTitle, p.GoalID)
	} else {
		b.WriteString("  (图中无目标节点,以本步骤意图为准)\n")
	}

	b.WriteString("\n二、本步骤意图(只做这一条)\n")
	fmt.Fprintf(&b, "  %s\n", p.StepIntent)
	if p.ActionLevel != "" {
		fmt.Fprintf(&b, "  意图分级: %s(中高级别动作将逐次经审批门,被拒不重试)\n", string(p.ActionLevel))
	}
	if p.ExpectedObservation != "" {
		fmt.Fprintf(&b, "  预期观察: %s(验收参考,非产出上限;预期外发现按待确认通道回传)\n", p.ExpectedObservation)
	}

	b.WriteString("\n三、相关可信事实(已确认)\n")
	if len(p.Facts) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, f := range p.Facts {
		fmt.Fprintf(&b, "  - %s: %s", f.ID, f.Title)
		if f.Evidence != "" {
			fmt.Fprintf(&b, " | 证据: %s", clipLine(f.Evidence, 160))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n四、必要资产与权限\n")
	if len(p.Assets) == 0 {
		b.WriteString("  (无绑定资产)\n")
	}
	for _, a := range p.Assets {
		fmt.Fprintf(&b, "  - %s [%s] %s\n", a.ID, a.Class, a.Title)
	}
	b.WriteString("  RoE 工具边界: ")
	if len(p.Authorization.DeniedTools) > 0 {
		fmt.Fprintf(&b, "禁止 %v; ", p.Authorization.DeniedTools)
	}
	if len(p.Authorization.AllowedTools) > 0 {
		fmt.Fprintf(&b, "仅允许 %v", p.Authorization.AllowedTools)
	} else {
		b.WriteString("未设白名单")
	}
	fmt.Fprintf(&b, "; 审批门槛: %s(该级及以上需批准,等待批准)\n",
		string(NormalizeApprovalThreshold(p.Authorization.ApprovalThreshold)))
	b.WriteString("\n")

	b.WriteString("\n五、前序尝试(同目标)\n")
	if len(p.PriorAttempts) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, a := range p.PriorAttempts {
		fmt.Fprintf(&b, "  - %s [%s] %s", a.StepID, a.Status, a.Intent)
		if a.Reason != "" {
			fmt.Fprintf(&b, " | 原因: %s", clipLine(a.Reason, 120))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\n六、授权与预算\n")
	fmt.Fprintf(&b, "  授权: 仅限 RoE 边界内操作;本系统仅供授权测试。\n")
	// 造物主注意事项(P4-49):任务包携带注入之一——新单位出生即见;
	// 与 RoE 约束块、重锚定重申同源于领取时冻结的授权副本。管边界
	// 不管打法:注意事项之外的做法自由。
	if p.Authorization.CreatorNotes != "" {
		b.WriteString("  造物主注意事项(对做法的要求,须遵守;未提及的做法你自主选择):\n")
		for _, line := range strings.Split(p.Authorization.CreatorNotes, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintf(&b, "    - %s\n", line)
			}
		}
	}
	if len(p.Credentials) > 0 {
		b.WriteString("  可用凭据(秘密经环境变量注入,命令里引用变量名,勿回显其值):\n")
		for _, c := range p.Credentials {
			line := "    - " + c.Name
			if c.Username != "" {
				line += " | 用户名 " + c.Username
			}
			line += " | $" + c.EnvVar + "\n"
			b.WriteString(line)
		}
	}
	bud := p.Budget.withDefaults()
	switch {
	case bud.MaxTurns > 0 && bud.RunSeconds > 0:
		fmt.Fprintf(&b, "  预算: 每段最多 %d 轮 / %d 秒;段末收尾把发现写回,预算重算后原会话续跑。\n", bud.MaxTurns, bud.RunSeconds)
	case bud.MaxTurns > 0:
		fmt.Fprintf(&b, "  预算: 每段最多 %d 轮(时长不限);段末收尾把发现写回,预算重算后原会话续跑。\n", bud.MaxTurns)
	case bud.RunSeconds > 0:
		fmt.Fprintf(&b, "  预算: 每段时长上限 %d 秒(轮数不限);段末收尾把发现写回,预算重算后原会话续跑。\n", bud.RunSeconds)
	default:
		b.WriteString("  预算: 轮数与时长不限(领了干完为止);边发现边写回。\n")
	}
	if !p.TaskDeadline.IsZero() {
		fmt.Fprintf(&b, "  任务收尾线: %s(到点就地收尾,把发现写回图)\n",
			p.TaskDeadline.Format("15:04:05"))
	}

	fmt.Fprintf(&b, "\n七、当前图版本\n  %d\n", p.GraphVersion)

	// 反证任务专用节(仅验证类意图携带;固定标头便于测试断言与模型解析)。
	if p.Falsify != nil {
		f := p.Falsify
		if f.Round > 0 {
			fmt.Fprintf(&b, "\n八、反证任务(验证类意图 · 第 %d 轮;你不是发现者,须独立复核,R12)\n", f.Round)
		} else {
			b.WriteString("\n八、反证任务(验证类意图;你不是发现者,须独立复核,R12)\n")
		}
		fmt.Fprintf(&b, "  判断 %s\n", f.ClaimID)
		fmt.Fprintf(&b, "  存在性判断: %s\n", f.Statement)
		fmt.Fprintf(&b, "  影响面判断: %s(与存在性分开反证)\n", f.Impact)
		if f.Vulnclass != "" {
			fmt.Fprintf(&b, "  漏洞类型: %s\n", f.Vulnclass)
		}
		fmt.Fprintf(&b, "  发现者证据: %s\n", clipLine(f.Evidence, 200))
		if len(f.Chain) > 0 {
			b.WriteString("  攻击链环节(组合判断:整条链的组合效果才是存在性,须逐环节独立重放):\n")
			for _, c := range f.Chain {
				line := "    - " + c.FactID
				if c.Role != "" {
					line += " [" + c.Role + "]"
				}
				if c.Title != "" {
					line += " " + clipLine(c.Title, 120)
				}
				b.WriteString(line + "\n")
			}
		}
		if len(f.Alternatives) > 0 {
			b.WriteString("  本轮待检的其他可能原因(逐条给出 refuted 结论;真实排除才可标 true):\n")
			for _, a := range f.Alternatives {
				fmt.Fprintf(&b, "    - %s: %s\n", a.ID, clipLine(a.Label, 160))
			}
		}
		b.WriteString("  你的职责: 独立尝试推翻上述判断——重放观察、核查证据、逐条排除其他可能原因。\n")
		b.WriteString("  完成后必须经 submit_falsify_result 写回结构化结论(existence_refuted / impact_supported")
		if len(f.Alternatives) > 0 {
			b.WriteString(" + 逐条 alternatives 按 ID 给 refuted")
		}
		b.WriteString(" + 本轮证据);\n")
		fmt.Fprintf(&b, "  影响面成立时须给 impact_level(critical|high|medium|low,报告级别只跟影响面证据走;%s);\n", SeverityAnchor)
		b.WriteString("  只输出文本结论不算完成,确认判定由仲裁者机器完成(你的 note 陈述不构成证据)。\n")
	}

	// 假设验证任务专用节(仅验证类意图携带;固定标头便于测试断言与模型解析)。
	if p.Verify != nil {
		h := p.Verify
		fmt.Fprintf(&b, "\n九、假设验证任务(验证类意图;你不是提出者,须独立复核,R12)\n")
		fmt.Fprintf(&b, "  假设 %s\n", h.HypothesisID)
		fmt.Fprintf(&b, "  假设陈述: %s\n", h.Statement)
		fmt.Fprintf(&b, "  提出者依据: %s\n", clipLine(h.Basis, 200))
		b.WriteString("  你的职责: 独立验证这条假设——设计验证手段、真实执行、经 submit_hypothesis_result\n")
		b.WriteString("  写回结构化结论(verdict: supported=成立|refuted=不成立|inconclusive=未能定论 + 本轮证据);\n")
		b.WriteString("  验证中取得的可保留观察同时走 submit_candidate_fact / submit_test_claim 正常回传;\n")
		b.WriteString("  假设成立不等于漏洞已验证(R11),只代表这条直觉值得跟。\n")
	}
	return b.String()
}

func clipLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
