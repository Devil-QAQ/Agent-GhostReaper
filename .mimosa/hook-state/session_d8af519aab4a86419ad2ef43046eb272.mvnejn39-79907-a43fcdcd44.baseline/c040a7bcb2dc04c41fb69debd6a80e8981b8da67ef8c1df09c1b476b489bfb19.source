package swarm

import (
	"fmt"

	"ghostreaper/internal/rings"
)

// 意图四级分类细则(Q8 消化,第四章 §3 定稿的落地文本)。
//
// 四级是风险分档框架而非动作清单:任何新动作、任何攻击链组合都必须
// 映射到四档之一,不限制模型可发起的动作类型。分档的判定标准是动作
// 对目标环境的因果方向,逐级单调递进——高级别蕴含低级别的一切风险:
//
//	L1 只读探测:只获取信息,不发送攻击载荷、不改变目标任何数据与
//	   状态,重复执行结果一致(端口开放性判定、banner 抓取、GET 拉取
//	   页面、DNS/公开信息收集)。判定标准:动作对目标无可观测副作用。
//	L2 主动测试:向目标发送测试载荷以检验响应(注入探测、目录枚举、
//	   弱口令尝试、畸形/边界请求)。判定标准:发出了非正常业务流量,
//	   可能触发目标告警或日志,但设计上不利用成功结果、不落地任何
//	   持久存在物。
//	L3 利用深入:建立在"利用已确认弱点"之上,实际取得未授权的访问
//	   或数据(读取敏感文件、无副作用命令回显、凭据试用、横向探测)。
//	   判定标准:动作以利用成功为前提,接触了授权范围外的数据或能力,
//	   但仍不修改目标状态。
//	L4 修改与持久化:改变目标状态或留下长期存在物(写文件、上传
//	   webshell、建账号、改配置、删数据)。判定标准:目标状态发生改变
//	   或新增长期存在物。
//
// 三级处置(第四章 §3 v2,P2-06b 重构):四级保留用于展示/报告/留痕
// 与审批门槛配置,不再逐级决定放行。审批对象仍是 Step 意图而非工具
// (同一工具不同用途危险度不同;v1 不做参数兜底规则引擎,审批界面同屏
// 展示意图声明与实际调用参数,人即兜底)。处置次序见 executor.go
// approvalGate:
//
//	拒绝线——非常严重:数据销毁/拒绝服务类(harm_class 声明)与超
//	   RoE 授权范围直接拒绝且留痕;面板提供"研判后放行"逐次放行;
//	弹门线——增删改数据:意图分级达到审批门槛(默认 L4)时阻塞
//	   等待面板批准/拒绝,弹门不可被对话指令跳过(R9);
//	其余一律放行留痕:不列举、不预判;tool_call 全量记录兜底审计。
//
// 审批强度的执行层挂接点是 Norma CanUseTool 回调(第二章 §3③):
// 只读工具(引擎侧 ReadOnly)在权限管线自动放行不进回调,非只读工具
// 逐次过 gate(见 executor.go approvalGate)。

// DefaultApprovalThreshold RoE 审批门槛缺省值:修改与持久化(L4)——
// 默认宽,仅增删改弹门(第四章 §3 v2);敏感目标可在 RoE 收紧到更低
// 级别(该级及以上弹门)。
const DefaultApprovalThreshold = rings.ActionModifyPersist

// ActionLevelRank 意图分级的风险序(L1=1 … L4=4);未知识别为 0,
// 调用方按 fail-safe 处理(审批门对 0 级直接拒绝)。
func ActionLevelRank(l rings.ActionLevel) int {
	switch l {
	case rings.ActionReadOnly:
		return 1
	case rings.ActionActiveTest:
		return 2
	case rings.ActionExploit:
		return 3
	case rings.ActionModifyPersist:
		return 4
	default:
		return 0
	}
}

// NormalizeApprovalThreshold 归一 RoE 审批门槛:空值取默认宽(仅增删改
// 弹门);非法值 fail-safe 收紧为只读探测(全部弹门)——门槛只可能来自
// 账本校验后的合法值,非法意味着绕过了受控入口,按最保守档处理。
func NormalizeApprovalThreshold(l rings.ActionLevel) rings.ActionLevel {
	if ActionLevelRank(l) == 0 {
		if l == "" {
			return DefaultApprovalThreshold
		}
		return rings.ActionReadOnly
	}
	return l
}

// ActionLevels 四级分类的有序清单(面板下拉与文档用)。
func ActionLevels() []rings.ActionLevel {
	return []rings.ActionLevel{
		rings.ActionReadOnly, rings.ActionActiveTest,
		rings.ActionExploit, rings.ActionModifyPersist,
	}
}

// errActionLevelMissing 分级缺失/非法时审批门的 fail-safe 拒绝理由。
func errActionLevelMissing(l rings.ActionLevel) error {
	return fmt.Errorf("意图分级缺失或非法(%q),安全闸门拒绝执行", string(l))
}
