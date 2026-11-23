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

const wrapupPrompt = `运行预算即将耗尽。立即收尾:把本次实际观察到、尚未提交的事实及证据用 submit_candidate_fact 写回；未验证的想法不要作为事实提交。然后用一句话说明进度与剩余工作。不要发起新的探测。`

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

	// Retake 补做证据/重验拉起情报(P5-02):本 Step 为交卷三查不过派的
	// 补做步骤或 reopen 派的重验步骤(detail.kind=retake)时非 nil——单位
	// 起飞接管原交卷会话续跑(转录取原步骤最近会话),失败原因/重开理由
	// 作为续跑首条指令注入;补做后经 submit_claim 带 claim_id 重交。
	Retake *RetakeBrief

	// TaskType 任务类型(P4-44 ②:created 事件 detail 定格,建后不可改):
	// 渗透/红队;空 = 存量未标(行为等同红队)。
	TaskType string

	// Briefing 任务简报(P5-04 虫群知情化):开工第一条消息开头的轻量
	// 概况(锚例句 + 概况 JSON——目标/已确认事实/已确认漏洞/在跑排队
	// 意图,无 coverage 信号)。领取时冻结;装配失败为空串(静默降级,
	// 虫群照常开工)。见 briefing.go。
	Briefing string
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
		fmt.Fprintf(&b, "  意图分级: %s(描述用途；实际动作按当前硬规则与边界判断，需要操作者决定时才审批)\n", string(p.ActionLevel))
	}
	if p.ExpectedObservation != "" {
		fmt.Fprintf(&b, "  预期观察: %s(验收参考,非产出上限;预期外发现按新进事实通道回传)\n", p.ExpectedObservation)
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
	b.WriteString("; 实际调用按当前边界判断，批准只适用于该次调用\n")
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

	return b.String()
}

func clipLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
