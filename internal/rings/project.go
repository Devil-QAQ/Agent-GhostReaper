package rings

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// applyEvent 将单个事件应用到投影(nodes/edges 与 v10 起的读侧汇总
// 投影)。事件插入与投影更新同处一个事务:任一步失败整体回滚,账本与
// 投影不会脱节。追加路径与重放路径共用本函数,保证投影确定性。
// actor 与 occurredAt 为事件信封字段(副作用投影按事件序语义消费)。
func applyEvent(tx *sql.Tx, taskID string, rev int64, kind EventKind, actor string, payload []byte, occurredAt time.Time) error {
	switch kind {
	case EventNodeAdded:
		var p NodeAdded
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 node.added 负载: %w", err)
		}
		if err := applyNodeAdded(tx, taskID, rev, p, occurredAt); err != nil {
			return err
		}
		return applyNodeAddedSide(tx, taskID, rev, actor, p, occurredAt)
	case EventNodeStateChanged:
		var p NodeStateChanged
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 node.state_changed 负载: %w", err)
		}
		if err := applyNodeStateChanged(tx, taskID, rev, p, occurredAt); err != nil {
			return err
		}
		return applyCaseStateChanged(tx, taskID, rev, p, occurredAt)
	case EventNodeAmended:
		var p NodeAmended
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 node.amended 负载: %w", err)
		}
		return applyNodeAmended(tx, taskID, rev, p, occurredAt)
	case EventEdgeAdded:
		var p EdgeAdded
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 edge.added 负载: %w", err)
		}
		return applyEdgeAdded(tx, taskID, rev, p)
	case EventTaskLifecycle:
		var p TaskLifecycle
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 task.lifecycle 负载: %w", err)
		}
		if err := applyTaskLifecycle(tx, taskID, rev, p); err != nil {
			return err
		}
		if err := applyTaskExtrasCreated(tx, taskID, p); err != nil {
			return err
		}
		return applyTaskLifecycleSide(tx, taskID, p, occurredAt)
	case EventTaskMeta:
		var p TaskMeta
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 task.meta 负载: %w", err)
		}
		return applyTaskMeta(tx, taskID, rev, p)
	case EventRoeUpdated:
		var p RoeUpdated
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 roe.updated 负载: %w", err)
		}
		return applyRoeUpdated(tx, taskID, rev, p)
	case EventSwarmRecord:
		var p SwarmRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 swarm.record 负载: %w", err)
		}
		if err := validateSwarmRecord(p); err != nil {
			return err
		}
		return applySwarmSession(tx, taskID, rev, p, occurredAt)
	case EventArbiterVerdict:
		var p ArbiterVerdict
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 arbiter.verdict 负载: %w", err)
		}
		return validateArbiterVerdict(p)
	case EventApprovalRecord:
		var p ApprovalRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 approval.record 负载: %w", err)
		}
		if err := validateApprovalRecord(p); err != nil {
			return err
		}
		return applyApprovalLog(tx, taskID, rev, p, occurredAt)
	case EventTestClaimRecord:
		var p TestClaimRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 testclaim.record 负载: %w", err)
		}
		if err := validateTestClaimRecord(tx, taskID, p); err != nil {
			return err
		}
		return applyClaimRecord(tx, taskID, rev, p, json.RawMessage(payload), occurredAt)
	case EventMirrorRecord:
		var p MirrorRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 mirror.record 负载: %w", err)
		}
		if err := validateMirrorRecord(p); err != nil {
			return err
		}
		return applyMirrorBrief(tx, taskID, p)
	case EventHypothesisRecord:
		var p HypothesisRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 hypothesis.record 负载: %w", err)
		}
		if err := validateHypothesisRecord(p); err != nil {
			return err
		}
		return applyHypState(tx, taskID, p)
	case EventBlackboxRecord:
		var p BlackboxRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 blackbox.record 负载: %w", err)
		}
		return validateBlackboxRecord(p)
	case EventSteerRecord:
		var p SteerRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 steer.record 负载: %w", err)
		}
		return validateSteerRecord(p)
	case EventOvermindRecord:
		var p OvermindRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 overmind.record 负载: %w", err)
		}
		if err := validateOvermindRecord(p); err != nil {
			return err
		}
		return applyOvermindRoundSide(tx, taskID, p)
	case EventShepherdRecord:
		var p ShepherdRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 shepherd.record 负载: %w", err)
		}
		if err := validateShepherdRecord(p); err != nil {
			return err
		}
		return applyClaimQA(tx, taskID, rev, p, occurredAt)
	case EventUnitControl:
		var p UnitControlRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 unit.control 负载: %w", err)
		}
		return validateUnitControl(p)
	case EventInterjectRecord:
		var p InterjectRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 interject.record 负载: %w", err)
		}
		return validateInterjectRecord(p)
	case EventGuardRecord:
		var p GuardRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 guard.record 负载: %w", err)
		}
		return validateGuardRecord(p)
	case EventReportRecord:
		var p ReportRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 report.record 负载: %w", err)
		}
		return validateReportRecord(p)
	case EventSessionMeta:
		var p SessionMeta
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 session.meta 负载: %w", err)
		}
		if err := validateSessionMeta(p); err != nil {
			return err
		}
		return applySessionLabel(tx, taskID, rev, p)
	case EventClosureRecord:
		var p ClosureRecord
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 closure.record 负载: %w", err)
		}
		if err := validateClosureRecord(p); err != nil {
			return err
		}
		return applyClosureRecordSide(tx, taskID, p, occurredAt)
	case EventOperatorDive:
		var p OperatorDive
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 operator.dive 负载: %w", err)
		}
		return validateOperatorDive(p)
	case EventClaimRoundHuman:
		var p ClaimRoundHuman
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 claim.round_human 负载: %w", err)
		}
		return applyClaimRoundHuman(tx, taskID, rev, p)
	case EventClaimSummaryPol:
		var p ClaimSummaryPolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 claim.summary 负载: %w", err)
		}
		return applyClaimSummaryPolish(tx, taskID, rev, p)
	case EventFactText:
		var p FactTextPolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 fact.text 负载: %w", err)
		}
		return applyNodePolish(tx, taskID, rev, p.FactID, p.Title, p.Statement, "", "", p.Unpolished)
	case EventHypoText:
		var p HypoTextPolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 hypo.text 负载: %w", err)
		}
		return applyNodePolish(tx, taskID, rev, p.HypothesisID, p.Title, "", p.Basis, p.Reason, p.Unpolished)
	case EventClaimRetextNote:
		var p RetestNotePolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 claim.retest_note 负载: %w", err)
		}
		return applyRetestNotePolish(tx, taskID, rev, p)
	case EventOvermindPolish:
		var p OvermindPolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 overmind.record_polish 负载: %w", err)
		}
		return validateOvermindPolish(p)
	case EventStepTitle:
		var p StepTitlePolish
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("rings: 解码 step.title 负载: %w", err)
		}
		if err := validateStepTitle(p); err != nil {
			return err
		}
		return applyNodePolish(tx, taskID, rev, p.StepID, p.Title, "", "", "", false)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownKind, string(kind))
	}
}

// applyNodeAddedSide node.added 的副作用投影(v10):判断卷宗初始化、
// 步骤战役登记、任务目标域(creator 种子资产)、收口新发现锚点
// (事实/判断/非种子资产)。
func applyNodeAddedSide(tx *sql.Tx, taskID string, rev int64, actor string, p NodeAdded, occurredAt time.Time) error {
	switch NodeType(p.NodeType) {
	case NodeTestClaim:
		return applyClaimNodeAdded(tx, taskID, rev, p, occurredAt)
	case NodeStep:
		return applyStepNodeAdded(tx, taskID, p, occurredAt)
	case NodeFact:
		return closureAnchorFinding(tx, taskID, occurredAt)
	case NodeAsset:
		if actor == "creator" && AssetSourceOf(p.Detail) != AssetSourceManual {
			if err := appendTaskTarget(tx, taskID, p.Title); err != nil {
				return err
			}
		}
		if AssetSourceOf(p.Detail) == AssetSourceSeed {
			return nil // 种子资产随建任务表单入账,不算执行中的新发现
		}
		return closureAnchorFinding(tx, taskID, occurredAt)
	}
	return nil
}

// validateClosureRecord 校验收口信号记录(P4-18;审核迭代 1 扩 config)。
// 不进 nodes/edges 投影(审计流,同 overmind.record)。结构性强制:类型
// 词汇受限;prompt 是机器判定快照,轮数与双阈值必为正、锚点时刻必为可
// 解析时刻——快照字段缺失的提醒无从对账;config 是任务级配置快照,开关
// 必带、单项值非负(0 = 该项用全局默认,范围上界在 server 写入口)。
func validateClosureRecord(p ClosureRecord) error {
	if !closureRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownClosureRecType, p.Type)
	}
	if p.Type == ClosureRecConfig {
		if p.Enabled == nil {
			return fmt.Errorf("%w: config 缺开关", ErrInvalidClosureRecord)
		}
		if p.Rounds < 0 || p.Minutes < 0 {
			return fmt.Errorf("%w: config 轮数/间隔不能为负", ErrInvalidClosureRecord)
		}
		return nil
	}
	if p.Type != ClosureRecPrompt {
		return nil // continue/end/dismiss:备注可选,无快照字段
	}
	if p.Rounds < 1 || p.RoundsThreshold < 1 || p.MinutesThreshold < 1 {
		return fmt.Errorf("%w: prompt 快照缺轮数或阈值", ErrInvalidClosureRecord)
	}
	if _, err := time.Parse(time.RFC3339, p.AnchorAt); err != nil {
		return fmt.Errorf("%w: prompt 锚点时刻不可解析: %w", ErrInvalidClosureRecord, err)
	}
	if !closureAnchorKinds[p.AnchorKind] {
		return fmt.Errorf("%w: prompt 锚点种类 %q 不在词表内", ErrInvalidClosureRecord, p.AnchorKind)
	}
	return nil
}

// sessionIDRe 会话锚白名单(P3-13b ②):字母数字 / 下划线 / 连字符 /
// 冒号(插话子会话键 u:swarm-* 形态)——从根上排除路径穿越与分隔符。
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)

// 会话名称与备注的长度上限(受控校验)。
const (
	SessionTitleLimit = 80
	SessionNoteLimit  = 500
)

// validateSessionMeta 校验会话元数据(P3-13b ②)。不进 nodes/edges
// 投影(组织信息流,读取侧扫事件投影)。结构性强制:会话锚在白名单
// 内;至少携带一项修改(nil 事件无意义,拒收防手滑空写);名称/备注
// 长度受限。
func validateSessionMeta(p SessionMeta) error {
	if !sessionIDRe.MatchString(p.SessionID) {
		return fmt.Errorf("%w: 会话锚 %q 非法", ErrInvalidSessionMeta, p.SessionID)
	}
	if p.Title == nil && p.Note == nil && p.Pinned == nil {
		return fmt.Errorf("%w: 至少携带名称 / 备注 / 置顶一项", ErrInvalidSessionMeta)
	}
	if p.Title != nil && len([]rune(*p.Title)) > SessionTitleLimit {
		return fmt.Errorf("%w: 名称超长(现 %d 字,上限 %d 字)", ErrInvalidSessionMeta, len([]rune(*p.Title)), SessionTitleLimit)
	}
	if p.Note != nil && len([]rune(*p.Note)) > SessionNoteLimit {
		return fmt.Errorf("%w: 备注超长(现 %d 字,上限 %d 字)", ErrInvalidSessionMeta, len([]rune(*p.Note)), SessionNoteLimit)
	}
	return nil
}

// validateShepherdRecord 校验牧者会话记录。不进 nodes/edges 投影(审计
// 流,同 swarm.record)。结构性强制:类型词汇受限;user_message/reply/
// advice 携带正文(空回复无从呈现);message_state 状态词汇受限且锚定
// MessageID;tool_call 须有工具名;advice/advice_verdict 须有 AdviceID,
// 裁决须有理由(纳谏的可审计面:为何采纳/为何不采纳)。
func validateShepherdRecord(p ShepherdRecord) error {
	if !shepherdRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownShepherdRecType, p.Type)
	}
	switch p.Type {
	case ShepherdRecUserMessage:
		if strings.TrimSpace(p.Text) == "" || p.MessageID == "" {
			return fmt.Errorf("%w: user_message 缺正文或 message_id", ErrShepherdRecMissingField)
		}
	case ShepherdRecReply:
		if strings.TrimSpace(p.Text) == "" || p.MessageID == "" {
			return fmt.Errorf("%w: reply 缺正文或 message_id", ErrShepherdRecMissingField)
		}
	case ShepherdRecAdvice:
		// 建议锚 AdviceID(跨问答存续,不锚单条消息)。
		if strings.TrimSpace(p.Text) == "" || p.AdviceID == "" {
			return fmt.Errorf("%w: advice 缺正文或 advice_id", ErrShepherdRecMissingField)
		}
	case ShepherdRecMessageState:
		if p.State != ShepherdMsgDelivered && p.State != ShepherdMsgFailed {
			return fmt.Errorf("%w: %s", ErrUnknownShepherdMsgState, p.State)
		}
		if p.MessageID == "" {
			return fmt.Errorf("%w: message_state 缺 message_id", ErrShepherdRecMissingField)
		}
		if p.State == ShepherdMsgFailed && strings.TrimSpace(p.Note) == "" {
			return fmt.Errorf("%w: 失败须携带原因", ErrShepherdRecMissingField)
		}
	case ShepherdRecThinking, ShepherdRecToolCall:
		if p.MessageID == "" {
			return fmt.Errorf("%w: %s 缺 message_id", ErrShepherdRecMissingField, p.Type)
		}
		if p.Type == ShepherdRecToolCall && p.ToolName == "" {
			return fmt.Errorf("%w: tool_call 缺工具名", ErrShepherdRecMissingField)
		}
	case ShepherdRecAdviceVerdict:
		if p.AdviceID == "" {
			return fmt.Errorf("%w: advice_verdict 缺 advice_id", ErrShepherdRecMissingField)
		}
		if p.State != ShepherdAdviceAdopted && p.State != ShepherdAdviceRejected {
			return fmt.Errorf("%w: %s", ErrUnknownShepherdAdviceState, p.State)
		}
		if strings.TrimSpace(p.Reason) == "" {
			return fmt.Errorf("%w: 判定须携带理由", ErrShepherdRecMissingField)
		}
	case ShepherdRecSystemNote:
		if strings.TrimSpace(p.Text) == "" || p.MessageID == "" {
			return fmt.Errorf("%w: system_note 缺正文或 message_id", ErrShepherdRecMissingField)
		}
	}
	return nil
}

// validateSteerRecord 校验舵杆记录。不进 nodes/edges 投影(审计流,同
// swarm.record)。结构性强制:指令与状态词汇受限;指令须锚定 Step;
// steer 必须携带纠偏消息(空消息的纠偏无从生效);状态机
// queued→delivered→adopted/rejected(或生效前落地→stale)的次序自洽
// 由入口保证——账本侧只锁词汇与必填,同一 SteerID 的最新事件即当前态。
func validateSteerRecord(p SteerRecord) error {
	if !steerCommands[p.Command] {
		return fmt.Errorf("%w: %s", ErrUnknownSteerCommand, p.Command)
	}
	if !steerStates[p.State] {
		return fmt.Errorf("%w: %s", ErrUnknownSteerState, p.State)
	}
	if p.SteerID == "" || p.StepID == "" {
		return fmt.Errorf("%w: 纠偏记录缺 steer_id/step_id", ErrInvalidSteerRecord)
	}
	// 纠偏消息必填限于携带完整指令的记录(queued/delivered/stale 随原
	// 记录回写);adopted/rejected 是回执状态迁移,消息在排队/送达记录
	// 里,单条查询按 SteerID 汇合(P2-08 回执状态机)。
	if p.Command == SteerCommandSteer && strings.TrimSpace(p.Message) == "" &&
		(p.State == SteerStateQueued || p.State == SteerStateDelivered || p.State == SteerStateStale) {
		return fmt.Errorf("%w: steer 指令必须携带纠偏消息", ErrInvalidSteerRecord)
	}
	return nil
}

// validateUnitControl 校验单位控制记录(P2-08)。不进 nodes/edges 投影
// (审计流,同 steer.record)。结构性强制:动作与目标词汇受限;unit 目标
// 须锚定步骤与单位,overmind 目标不带单位字段。
func validateUnitControl(p UnitControlRecord) error {
	if !unitControlActions[p.Action] {
		return fmt.Errorf("%w: %s", ErrUnknownUnitCtlAction, p.Action)
	}
	switch p.Target {
	case "unit":
		if p.StepID == "" || p.UnitID == "" {
			return fmt.Errorf("%w: unit 目标缺 step_id/unit_id", ErrInvalidUnitControl)
		}
	case "overmind":
		if p.StepID != "" || p.UnitID != "" {
			return fmt.Errorf("%w: overmind 目标不带单位字段", ErrInvalidUnitControl)
		}
	default:
		return fmt.Errorf("%w: %s", ErrUnknownUnitCtlTarget, p.Target)
	}
	return nil
}

// validateInterjectRecord 校验插话子会话记录(P2-08)。不进 nodes/edges
// 投影(审计流,同 shepherd.record)。结构性强制:类型/状态/模式词汇
// 受限;user_message/reply 携带正文并锚 MessageID;steer 锚 SteerID;
// advice/advice_verdict 锚 AdviceID 且裁决须有理由;unit 目标须带
// SessionID(跨进程稳定的会话锚)。
func validateInterjectRecord(p InterjectRecord) error {
	if !interjectRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownInterjectRecType, p.Type)
	}
	if p.Target != "overmind" && p.Target != "unit" {
		return fmt.Errorf("%w: %s", ErrUnknownInterjectTarget, p.Target)
	}
	if p.Target == "unit" && p.SessionID == "" {
		return fmt.Errorf("%w: unit 目标缺 session_id", ErrInterjectRecMissingField)
	}
	switch p.Type {
	case InterjectRecUserMessage:
		if strings.TrimSpace(p.Text) == "" || p.MessageID == "" {
			return fmt.Errorf("%w: user_message 缺正文或 message_id", ErrInterjectRecMissingField)
		}
		if p.Mode != "ask" && p.Mode != "steer" {
			return fmt.Errorf("%w: %s", ErrUnknownInterjectMode, p.Mode)
		}
	case InterjectRecReply:
		if strings.TrimSpace(p.Text) == "" || p.MessageID == "" {
			return fmt.Errorf("%w: reply 缺正文或 message_id", ErrInterjectRecMissingField)
		}
	case InterjectRecMessageState:
		if p.State != InterjectMsgDelivered && p.State != InterjectMsgFailed {
			return fmt.Errorf("%w: %s", ErrUnknownInterjectMsgState, p.State)
		}
		if p.MessageID == "" {
			return fmt.Errorf("%w: message_state 缺 message_id", ErrInterjectRecMissingField)
		}
		if p.State == InterjectMsgFailed && strings.TrimSpace(p.Note) == "" {
			return fmt.Errorf("%w: 失败须携带原因", ErrInterjectRecMissingField)
		}
	case InterjectRecSteer:
		if p.SteerID == "" {
			return fmt.Errorf("%w: steer 缺 steer_id", ErrInterjectRecMissingField)
		}
	case InterjectRecAdvice:
		if strings.TrimSpace(p.Text) == "" || p.AdviceID == "" {
			return fmt.Errorf("%w: advice 缺正文或 advice_id", ErrInterjectRecMissingField)
		}
	case InterjectRecAdviceVerdict:
		if p.AdviceID == "" {
			return fmt.Errorf("%w: advice_verdict 缺 advice_id", ErrInterjectRecMissingField)
		}
		if p.State != ShepherdAdviceAdopted && p.State != ShepherdAdviceRejected {
			return fmt.Errorf("%w: %s", ErrUnknownShepherdAdviceState, p.State)
		}
		if strings.TrimSpace(p.Reason) == "" {
			return fmt.Errorf("%w: 判定须携带理由", ErrInterjectRecMissingField)
		}
	}
	return nil
}

// validateGuardRecord 校验拦截判定记录(P2-08e)。不进 nodes/edges 投影
// (审计流,同 approval.record)。结构性强制:类型词汇受限;计数记录
// 必须锚定规则与序号(Nth ≥ 1)——账面可对账"第几次"。
func validateGuardRecord(p GuardRecord) error {
	if !guardRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownGuardRecType, p.Type)
	}
	if p.StepID == "" || p.UnitID == "" {
		return ErrGuardRecMissingField
	}
	if p.Type == GuardRecQuotaCount {
		if p.RuleID == "" || p.Nth < 1 {
			return fmt.Errorf("%w: 计数记录缺 rule_id 或序号非法", ErrGuardRecMissingField)
		}
	}
	return nil
}

// validateReportRecord 校验报告存档记录(P3-02)。不进 nodes/edges 投影
// (审计流,同 swarm.record)。结构性强制:类型/格式词汇受限;原文引用与
// 尺寸必填(R18:大对象走内容寻址,登记里必有引用可回放);证据版本与
// 账本版本非负(版本锚点);写入侧的乐观锁比对在 server 入口(需读当前
// 证据版本,账本侧只锁结构)。
func validateReportRecord(p ReportRecord) error {
	if !reportRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownReportRecType, p.Type)
	}
	if p.Format == "" || p.Ref == "" || p.Size <= 0 {
		return ErrReportRecMissingField
	}
	if p.EvidenceVersion < 0 || p.LedgerRev < 0 {
		return ErrReportRecMissingField
	}
	return nil
}

// validateOvermindRecord 校验主宰研判记录。不进 nodes/edges 投影(审计流,
// 同 hypothesis.record)。结构性强制:类型词汇受限;assessment 五问与
// 五问外研判(Note)至少一项非空——全空的研判记录没有信息量,纯噪音,
// 账本侧直接拒收(纯静默轮不落研判记录,由写入侧保证)。
func validateOvermindRecord(p OvermindRecord) error {
	if !overmindRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownOvermindRecType, p.Type)
	}
	if p.Type == OvermindRecAssessment {
		any := strings.TrimSpace(p.QAccess) != "" ||
			strings.TrimSpace(p.QPrivilege) != "" ||
			strings.TrimSpace(p.QCombination) != "" ||
			strings.TrimSpace(p.QUnlock) != "" ||
			strings.TrimSpace(p.QOverturn) != "" ||
			strings.TrimSpace(p.Note) != ""
		if !any {
			return ErrOvermindRecEmptyAssessment
		}
	}
	return nil
}

// validateBlackboxRecord 校验黑匣子记录。不进 nodes/edges 投影(系统级
// 审计流,同 swarm.record)。结构性强制:类型词汇受限——黑匣子是机器
// 可扫的确定性通道,自由文本类型会让"预算状态变化必留痕"失去可查询性。
func validateBlackboxRecord(p BlackboxRecord) error {
	if !blackboxRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownBlackboxRecType, p.Type)
	}
	return nil
}

// validateOvermindPolish 校验研判记录校对稿(P4-65 ⑨)。不进投影(审计流
// 侧表,同 overmind.record——读侧从事件流按 rec_rev 合并)。物理校验
// 拒空事件(P4-64 先例):锚定版本必填且为正,六字段全空不落(生成侧
// 控制零内容不落账,空事件无信息量)。
func validateOvermindPolish(p OvermindPolish) error {
	if p.RecRev <= 0 {
		return fmt.Errorf("rings: overmind.record_polish 缺锚定规划记录版本")
	}
	if strings.TrimSpace(p.QAccess) == "" && strings.TrimSpace(p.QPrivilege) == "" &&
		strings.TrimSpace(p.QCombination) == "" && strings.TrimSpace(p.QUnlock) == "" &&
		strings.TrimSpace(p.QOverturn) == "" && strings.TrimSpace(p.Note) == "" {
		return fmt.Errorf("rings: overmind.record_polish 字段全空(不落空事件)")
	}
	return nil
}

// validateStepTitle 校验步骤意图标题校对稿(P4-65 ⑩)。物理校验拒空
// 事件(P4-64 先例):步骤与标题必填。
func validateStepTitle(p StepTitlePolish) error {
	if p.StepID == "" || strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("rings: step.title 缺步骤或标题(不落空事件)")
	}
	return nil
}

// validateTestClaimRecord 校验 TestClaim 状态机记录。不进 nodes/edges
// 投影(审计流,同 swarm.record/arbiter.verdict)。结构性强制:
//   - falsify_result 须绑反证 Step 与单位(写回无主即不可审计);逐条
//     竞争解释结论必须有 ID;影响面定级只允许四级词汇——impact_supported
//     =true 时必填(级别跟影响面证据走,发现者侧无级别通道);
//   - verdict 无论结论必须逐条列出依据条件(R2);非 confirm 裁决必须
//     携带理由(R4:待确认/待下轮反证都是一等状态,理由缺失的"待确认"
//     就是"偷懒的待确认",账本入口直接拒收);decision 与 satisfied
//     自洽(确认裁决不得 satisfied=false,反之亦然);
//   - reopen 须带触发词与理由(重开必有可审计的"为何重开");
//   - repro 的严重级别只允许四级词汇(空=未定级,如实呈现不造数据);
//   - evidence(P3-02)须锚定本任务图上的 TestClaim,且每个交换必须是
//     本任务账本内已有的 http/https 镜留痕(blob 引用非空——隧道留痕
//     无原文可回放,绑定了也不构成可验证证据,入口即拒);
//   - report_review(P4-30)须锚定本任务图上的 TestClaim 且复核理由
//     必填(标记必有可审计的"为何不纳入报告");账本只追加标记记录,
//     判断节点与历史记录不动(不涂改)。
func validateTestClaimRecord(tx *sql.Tx, taskID string, p TestClaimRecord) error {
	if !testClaimRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownTestClaimRecType, p.Type)
	}
	if p.ClaimID == "" {
		return ErrTestClaimRecMissingField
	}
	switch p.Type {
	case TestClaimRecFalsify:
		if p.StepID == "" || p.UnitID == "" {
			return ErrTestClaimRecMissingField
		}
		for _, a := range p.Alternatives {
			if strings.TrimSpace(a.ID) == "" {
				return ErrAltResultMissingID
			}
		}
		if p.ImpactLevel != "" && !impactLevels[p.ImpactLevel] {
			return fmt.Errorf("%w: %s", ErrUnknownImpactLevel, p.ImpactLevel)
		}
		if p.ImpactSupported && p.ImpactLevel == "" {
			return ErrTestClaimRecMissingField // 影响面成立必带定级(收割定稿的唯一级别来源)
		}
		return nil
	case TestClaimRecVerdict:
		if len(p.Conditions) == 0 {
			return ErrTestClaimRecMissingField
		}
		if p.Decision != "" && p.Decision != VerdictConfirm && p.Decision != VerdictRefuted && p.Decision != VerdictPending && p.Decision != VerdictAdvanceRound {
			return fmt.Errorf("%w: %s", ErrUnknownClaimDecision, p.Decision)
		}
		// 有效 decision:显式值优先;P1-08 旧数据无 Decision,按 Satisfied 推导。
		decision := p.Decision
		if decision == "" {
			if p.Satisfied {
				decision = VerdictConfirm
			} else {
				decision = VerdictPending
			}
		}
		if (decision == VerdictConfirm) != p.Satisfied {
			return fmt.Errorf("%w: decision/satisfied 不自洽(%s/%v)", ErrUnknownClaimDecision, decision, p.Satisfied)
		}
		if decision != VerdictConfirm && strings.TrimSpace(p.PendingReason) == "" {
			return ErrTestClaimPendingNoReason
		}
		return nil
	case TestClaimRecReopen:
		if !reopenTriggers[p.Trigger] {
			return fmt.Errorf("%w: %s", ErrUnknownReopenTrigger, p.Trigger)
		}
		if strings.TrimSpace(p.Reason) == "" {
			return ErrReopenNoReason
		}
		return nil
	case TestClaimRecEvidence:
		return validateEvidenceBinding(tx, taskID, p)
	case TestClaimRecRetest:
		// 复测结论(P3-06):会话锚(伪步骤 ID)必带、结论词受限、
		// 证据必带(经内容寻址 blob 留档,只写结论不算复测)。
		if p.StepID == "" || p.UnitID == "" {
			return ErrTestClaimRecMissingField
		}
		if !IsValidRetestVerdict(p.RetestVerdict) {
			return fmt.Errorf("%w: %s", ErrUnknownRetestVerdict, p.RetestVerdict)
		}
		if p.EvidenceRef == "" && strings.TrimSpace(p.EvidenceHead) == "" {
			return ErrTestClaimRecMissingField
		}
		return nil
	case TestClaimRecReplay:
		return validateReplayRecord(tx, taskID, p)
	case TestClaimRecReportReview:
		// 存量复核标记(P4-30 ③):复核理由必填;锚点必须是本任务图上
		// TestClaim(标记的是判断,锚错即拒)。只追加标记记录,判断
		// 节点与历史不动——账本不涂改,读取侧按最新标记过滤报告。
		if strings.TrimSpace(p.Reason) == "" {
			return fmt.Errorf("%w: 复核理由为空", ErrReportReviewInvalid)
		}
		nt, err := nodeTypeOf(tx, taskID, p.ClaimID)
		if err != nil {
			return fmt.Errorf("%w: 判断 %s: %w", ErrReportReviewInvalid, p.ClaimID, err)
		}
		if nt != NodeTestClaim {
			return fmt.Errorf("%w: 节点 %s 是 %s 不是 testclaim", ErrReportReviewInvalid, p.ClaimID, nt)
		}
		return nil
	case TestClaimRecSuspend:
		// 操作者取消挂起(P4-54):挂起理由必填;锚点必须是本任务图上的
		// TestClaim。状态迁移(testing→pending)由写入口另行落
		// node.state_changed,本记录承载理由(投影 pending_reason)。
		if strings.TrimSpace(p.Reason) == "" {
			return fmt.Errorf("%w: 挂起理由为空", ErrTestClaimPendingNoReason)
		}
		nt, err := nodeTypeOf(tx, taskID, p.ClaimID)
		if err != nil {
			return fmt.Errorf("%w: 判断 %s: %w", ErrReopenNoReason, p.ClaimID, err)
		}
		if nt != NodeTestClaim {
			return fmt.Errorf("%w: 节点 %s 是 %s 不是 testclaim", ErrReopenNoReason, p.ClaimID, nt)
		}
		return nil
	case TestClaimRecDiscard:
		// 程序确定性废弃(P4-55 审核迭代,造物主 2026-11-08 定案:摘除
		// 资产→锚定声明/假设全废弃,理由"目标已摘除";主宰裁决放弃同用):
		// 废弃理由必填;锚点必须是本任务图上的 TestClaim。状态迁移
		// (锤炼中→discarded)由写入口同批落 node.state_changed,终态只
		// 追加、历史不涂改;勾回资产不自动复活(再验走操作者深入)。
		if strings.TrimSpace(p.Reason) == "" {
			return fmt.Errorf("%w: 废弃理由为空", ErrTestClaimPendingNoReason)
		}
		nt, err := nodeTypeOf(tx, taskID, p.ClaimID)
		if err != nil {
			return fmt.Errorf("%w: 判断 %s: %w", ErrReopenNoReason, p.ClaimID, err)
		}
		if nt != NodeTestClaim {
			return fmt.Errorf("%w: 节点 %s 是 %s 不是 testclaim", ErrReopenNoReason, p.ClaimID, nt)
		}
		return nil
	default: // repro
		if p.Severity != "" && !impactLevels[p.Severity] {
			return fmt.Errorf("%w: %s", ErrUnknownImpactLevel, p.Severity)
		}
		// P4-62 A:无复现包原因与复现步骤互斥——原因非空即本次定稿
		// 无步骤(兜底直出已废,倒池形态物理进不了账本)。
		if p.NoReproReason != "" && len(p.Steps) > 0 {
			return fmt.Errorf("%w: repro 记录不得同时携带步骤与无复现包原因", ErrTestClaimRecMissingField)
		}
		// P4-73 ④:流程步骤未生成标注只伴随存在的包(证明交换种子在内、
		// 挑选失败如实标注);无步骤的定稿走 NoReproReason,不带本标注。
		if p.StepsIncomplete != "" && len(p.Steps) == 0 {
			return fmt.Errorf("%w: 流程步骤未生成标注不得脱离复现步骤单独携带", ErrTestClaimRecMissingField)
		}
		return nil
	}
}

// validateReplayRecord 重放校验结论(P3-07)的物理校验:结论词受限;
// 逐条交换事实必带(只写结论不算校验);锚(伪步骤 ID)必带;被重放的
// 交换必须在该声明的绑定证据集内(重放对象是操作者绑定的证据,重放
// 别的声明/未绑定的流量即串号,入口拒)。同事务查 events 表,绑定先于
// 重放落账(重放读取的绑定必然已入账)。
func validateReplayRecord(tx *sql.Tx, taskID string, p TestClaimRecord) error {
	if p.StepID == "" {
		return ErrTestClaimRecMissingField
	}
	if !IsValidReplayVerdict(p.ReplayVerdict) {
		return fmt.Errorf("%w: %s", ErrUnknownReplayVerdict, p.ReplayVerdict)
	}
	if len(p.ReplayResults) == 0 {
		return ErrTestClaimRecMissingField
	}
	nt, err := nodeTypeOf(tx, taskID, p.ClaimID)
	if err != nil {
		return fmt.Errorf("%w: 判断 %s: %w", ErrReplayRecordInvalid, p.ClaimID, err)
	}
	if nt != NodeTestClaim {
		return fmt.Errorf("%w: 节点 %s 是 %s 不是 testclaim", ErrReplayRecordInvalid, p.ClaimID, nt)
	}
	bound := map[string]bool{}
	rows, err := tx.Query(
		`SELECT payload FROM events
		 WHERE task_id = ? AND kind = ?
		   AND json_extract(payload, '$.type') = ?
		   AND json_extract(payload, '$.claim_id') = ?`,
		taskID, EventTestClaimRecord, TestClaimRecEvidence, p.ClaimID,
	)
	if err != nil {
		return fmt.Errorf("rings: 校验重放绑定集 %s: %w", p.ClaimID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return fmt.Errorf("rings: 扫描绑定集 %s: %w", p.ClaimID, err)
		}
		var rec TestClaimRecord
		if json.Unmarshal([]byte(payload), &rec) != nil {
			continue
		}
		for _, ex := range rec.Exchanges {
			bound[ex] = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rings: 遍历绑定集 %s: %w", p.ClaimID, err)
	}
	seen := make(map[string]bool, len(p.ReplayResults))
	for _, r := range p.ReplayResults {
		if strings.TrimSpace(r.ExchangeID) == "" {
			return fmt.Errorf("%w: 重放交换 ID 为空", ErrReplayRecordInvalid)
		}
		if seen[r.ExchangeID] {
			return fmt.Errorf("%w: 交换 %s 在本批内重复", ErrReplayRecordInvalid, r.ExchangeID)
		}
		seen[r.ExchangeID] = true
		if !bound[r.ExchangeID] {
			return fmt.Errorf("%w: 交换 %s 不在判断 %s 的绑定证据内(重放只许绑定证据)", ErrReplayRecordInvalid, r.ExchangeID, p.ClaimID)
		}
	}
	return nil
}

// validateEvidenceBinding 流量证据绑定(P3-02)的物理校验:声明必须是
// 图上既有 TestClaim(绑错对象即拒);每个交换必须在本任务事件流内有
// 对应的 http/https 镜留痕(blob 引用非空)。同事务内查 events 表——
// 追加时只看得见更早的事件,绑定的交换必然先于绑定落账;全量重放时
// 事件表全量在场,合法账本重放结果一致(确定性不受影响)。
func validateEvidenceBinding(tx *sql.Tx, taskID string, p TestClaimRecord) error {
	nt, err := nodeTypeOf(tx, taskID, p.ClaimID)
	if err != nil {
		return fmt.Errorf("%w: 判断 %s: %w", ErrEvidenceBindingInvalid, p.ClaimID, err)
	}
	if nt != NodeTestClaim {
		return fmt.Errorf("%w: 节点 %s 是 %s 不是 testclaim", ErrEvidenceBindingInvalid, p.ClaimID, nt)
	}
	if len(p.Exchanges) == 0 {
		return fmt.Errorf("%w: 绑定的流量记录清单为空", ErrEvidenceBindingInvalid)
	}
	seen := make(map[string]bool, len(p.Exchanges))
	for _, ex := range p.Exchanges {
		if strings.TrimSpace(ex) == "" {
			return fmt.Errorf("%w: 绑定的流量记录 ID 为空", ErrEvidenceBindingInvalid)
		}
		if seen[ex] {
			return fmt.Errorf("%w: 流量记录 %s 在本批内重复", ErrEvidenceBindingInvalid, ex)
		}
		seen[ex] = true
		var hit int
		err := tx.QueryRow(
			`SELECT 1 FROM events
			 WHERE task_id = ? AND kind = ?
			   AND json_extract(payload, '$.exchange_id') = ?
			   AND json_extract(payload, '$.blob_ref') != ''`,
			taskID, EventMirrorRecord, ex,
		).Scan(&hit)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: 流量记录 %s 不在本任务留痕内,或为无原文的隧道记录", ErrEvidenceBindingInvalid, ex)
		}
		if err != nil {
			return fmt.Errorf("rings: 校验绑定交换 %s: %w", ex, err)
		}
	}
	return nil
}

// validateApprovalRecord 校验审批记录。不进 nodes/edges 投影(审计流,
// 同 swarm.record)——弹门与裁决本身可从事件流完整复盘。
// ActionLevel 只对 requested/approved/forced 强制合法:denied 是 fail-safe 的
// 拒绝留痕,被拒对象可能正是"分级缺失/非法"的调用,记录必须能如实
// 落账,不受词汇表约束。DenyLine/HarmClass 非空时必须在词表内(旧事件
// 无这些字段,重放兼容:缺省不校验)。
func validateApprovalRecord(p ApprovalRecord) error {
	if !approvalRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownApprovalRecType, p.Type)
	}
	// P4-51:范围待决卡锚定的是目标(资产),不是一次命令——红队发现卡
	// 无步骤/单位/工具上下文,渗透拦截卡有但分级不适用,四字段与分级
	// 均豁免(仅要求 RequestID;同号裁决记录一并豁免)。denied/forced
	// 沿既有豁免分级校验:拒绝留痕,被拒对象可能正是"分级缺失/非法"
	// 的调用,记录必须能如实落账,不受词汇表约束。
	// P4-71:反向裁决(reversed)锚定的是一条历史记录,不是一次命令——
	// 不执行命令、无步骤/单位/工具上下文,与范围卡同族豁免(仅要求
	// RequestID;原记录引用在 Note)。
	if IsScopeCardRecord(p) || p.Type == ApprovalRecReversed {
		if p.RequestID == "" {
			return ErrApprovalRecMissingField
		}
	} else {
		if p.RequestID == "" || p.StepID == "" || p.UnitID == "" || p.ToolName == "" {
			return ErrApprovalRecMissingField
		}
		if p.Type != ApprovalRecDenied && p.Type != ApprovalRecForced && !actionLevels[p.ActionLevel] {
			return fmt.Errorf("%w: %s", ErrInvalidActionLevel, string(p.ActionLevel))
		}
	}
	if p.DenyLine != "" && !IsValidDenyLine(p.DenyLine) {
		return fmt.Errorf("%w: %s", ErrUnknownDenyLine, p.DenyLine)
	}
	if p.HarmClass != "" && !IsValidHarmClass(p.HarmClass) {
		return fmt.Errorf("%w: %s", ErrUnknownHarmClass, p.HarmClass)
	}
	return nil
}

// validateArbiterVerdict 校验仲裁裁决。它不进 nodes/edges 投影(审计流,
// 同 swarm.record);"判达成"必须逐条列出依据条件,否则裁决不可审计
// (R2:终审裁决必须是机器纯函数求值,求值依据须随裁决落账)。
func validateArbiterVerdict(p ArbiterVerdict) error {
	if p.GoalID == "" {
		return ErrArbiterVerdictMissingField
	}
	if p.Satisfied && len(p.Conditions) == 0 {
		return ErrArbiterVerdictMissingField
	}
	return nil
}

// validateMirrorRecord 校验镜留痕。不进 nodes/edges 投影(审计流,同
// swarm.record)。结构性强制三点:
//   - 归因必须齐:无主流量不可审计(ExchangeID/StepID/UnitID 缺一即拒);
//   - http/https 类型必须携带 blob 引用——"全量留痕"意味着完整交换原文
//     可回放,有引用才算留痕(R18 的账本侧物理强制;https 为 P3-01
//     CA MITM 解密留痕,与 http 同粒度);
//   - tunnel 类型如实区分(非 TLS 协议兜底/解密接管失败),只记元数据。
func validateMirrorRecord(p MirrorRecord) error {
	if !mirrorRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownMirrorRecType, p.Type)
	}
	if p.ExchangeID == "" || p.StepID == "" || p.UnitID == "" {
		return ErrMirrorRecMissingField
	}
	if (p.Type == MirrorRecHTTP || p.Type == MirrorRecHTTPS) && p.BlobRef == "" {
		return ErrMirrorRecMissingField
	}
	return nil
}

// validateHypothesisRecord 校验假设回流记录。不进 nodes/edges 投影
// (审计流,同 testclaim.record)。结构性强制:
//   - evaluation 三去向词汇受限,理由必填——挂账与废弃都是一等去向,
//     "为何跟/为何不跟"缺失即不可审计(R10:直觉有账、废案有理由);
//   - result 结论词汇受限(supported|refuted|inconclusive),须绑验证
//     Step 与单位(写回无主即不可审计)。
func validateHypothesisRecord(p HypothesisRecord) error {
	if !hypothesisRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownHypothesisRecType, p.Type)
	}
	if p.HypothesisID == "" {
		return ErrHypothesisRecMissingField
	}
	switch p.Type {
	case HypothesisRecEvaluation:
		if !hypothesisDecisions[p.Decision] {
			return fmt.Errorf("%w: %s", ErrUnknownHypothesisDecision, p.Decision)
		}
		if strings.TrimSpace(p.Reason) == "" {
			return ErrHypothesisEvalNoReason
		}
		return nil
	default: // result
		if !hypothesisVerdicts[p.Verdict] {
			return fmt.Errorf("%w: %s", ErrUnknownHypothesisVerdict, p.Verdict)
		}
		if p.StepID == "" || p.UnitID == "" {
			return ErrHypothesisRecMissingField
		}
		return nil
	}
}

// applyTaskLifecycle 维护 tasks 投影:created 插入行,其余动作更新 status。
// title 仅在事件携带非空值时覆盖(创建时定题)。
func applyTaskLifecycle(tx *sql.Tx, taskID string, rev int64, p TaskLifecycle) error {
	if !taskLifecycleActions[p.Action] {
		return fmt.Errorf("%w: %s", ErrUnknownTaskAction, p.Action)
	}
	detail, err := marshalDetail(p.Detail)
	if err != nil {
		return err
	}
	// 以 TEXT 绑定(P2-04 修复既有缺陷:[]byte 按 BLOB 绑定时,下方
	// CASE WHEN ? != '{}' 的 BLOB/TEXT 比较永不相等,任何后续生命周期
	// 事件都会把已存的 detail 清空——created 的 detail 从此不可依赖)。
	detailStr := string(detail)
	if p.Action == "created" {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM tasks WHERE task_id = ?`, taskID).Scan(&exists); err == nil {
			return fmt.Errorf("%w: task %s", ErrTaskExists, taskID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("rings: 查重任务 %s: %w", taskID, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO tasks (task_id, title, status, detail, created_rev, updated_rev)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			taskID, p.Title, p.Action, detailStr, rev, rev,
		); err != nil {
			return fmt.Errorf("rings: 投影写入任务 %s: %w", taskID, err)
		}
		return nil
	}
	res, err := tx.Exec(
		`UPDATE tasks SET status = ?, updated_rev = ?,
		 title = CASE WHEN ? != '' THEN ? ELSE title END,
		 detail = CASE WHEN ? != '{}' THEN ? ELSE detail END
		 WHERE task_id = ?`,
		p.Action, rev, p.Title, p.Title, detailStr, detailStr, taskID,
	)
	if err != nil {
		return fmt.Errorf("rings: 投影更新任务 %s 生命周期: %w", taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: task %s", ErrTaskNotFound, taskID)
	}
	return nil
}

// applyTaskMeta 维护 tasks 投影的元数据列(P2-06h):分类 / 置顶 /
// 墓碑。指针为 nil 的项不动;置顶写入 pinned=1 并记 pinned_rev(置顶
// 次序锚)。不触碰 status/title/detail/updated_rev——生命周期列仍由
// lifecycle 事件独管,分类/置顶操作不扰动 FIFO 重准入的 UpdatedRev 锚。
// 结构性强制:三字段全空(无一项要改)即拒——无信息量的元数据事件
// 只会推高版本号。
func applyTaskMeta(tx *sql.Tx, taskID string, rev int64, p TaskMeta) error {
	if p.Category == nil && p.Pinned == nil && !p.Deleted {
		return fmt.Errorf("%w: task %s", ErrTaskMetaNoField, taskID)
	}
	if p.Deleted && strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("%w: task %s", ErrTaskMetaDeleteNoReason, taskID)
	}
	res, err := tx.Exec(
		`UPDATE tasks SET
		   category   = COALESCE(?, category),
		   pinned     = COALESCE(?, pinned),
		   pinned_rev = CASE WHEN ? = 1 THEN ? ELSE pinned_rev END,
		   deleted    = CASE WHEN ? = 1 THEN 1 ELSE deleted END
		 WHERE task_id = ?`,
		p.Category, boolToInt(p.Pinned), boolToInt(p.Pinned), rev, boolToInt(&p.Deleted), taskID,
	)
	if err != nil {
		return fmt.Errorf("rings: 投影更新任务 %s 元数据: %w", taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: task %s", ErrTaskNotFound, taskID)
	}
	return nil
}

// boolToInt 指针布尔 → SQL 参数(nil = NULL,由 COALESCE/CASE 语义消费)。
func boolToInt(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

// applyRoeUpdated 维护 roe_state 投影:RoE 是"当前值"语义,每次变更整体覆盖。
// ApprovalThreshold 非空时必须是合法意图分级(四级词汇表,受控入口校验);
// IntentRules(P2-08e)整体覆盖并做词表校验;ExcludedAssets(P4-05)整体
// 覆盖,条目须非空且不重复(资产存在性在 server 写入口查图);ScopeApproved
// (P4-44 放行清单)整体覆盖,校验同款;MaxActionLevel
// (P1-07 分级上限)已废止,仅读兼容——不再校验不再消费。
func applyRoeUpdated(tx *sql.Tx, taskID string, rev int64, p RoeUpdated) error {
	if p.ApprovalThreshold != "" && !actionLevels[p.ApprovalThreshold] {
		return fmt.Errorf("%w: %s", ErrInvalidActionLevel, string(p.ApprovalThreshold))
	}
	if err := validateIntentRules(p.IntentRules); err != nil {
		return err
	}
	seenAsset := map[string]bool{}
	for _, id := range p.ExcludedAssets {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("rings: 授权范围摘除清单含空资产 ID")
		}
		if seenAsset[id] {
			return fmt.Errorf("rings: 授权范围摘除清单重复资产 %s", id)
		}
		seenAsset[id] = true
	}
	seenApproved := map[string]bool{}
	for _, id := range p.ScopeApproved {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("rings: 范围外资产放行清单含空资产 ID")
		}
		if seenApproved[id] {
			return fmt.Errorf("rings: 范围外资产放行清单重复资产 %s", id)
		}
		seenApproved[id] = true
	}
	allowed, err := json.Marshal(p.AllowedTools)
	if err != nil {
		return fmt.Errorf("rings: 序列化 allowed_tools: %w", err)
	}
	if p.AllowedTools == nil {
		allowed = []byte("[]")
	}
	denied, err := json.Marshal(p.DeniedTools)
	if err != nil {
		return fmt.Errorf("rings: 序列化 denied_tools: %w", err)
	}
	rules, err := json.Marshal(p.IntentRules)
	if err != nil {
		return fmt.Errorf("rings: 序列化 intent_rules: %w", err)
	}
	if p.IntentRules == nil {
		rules = []byte("[]")
	}
	exAssets, err := json.Marshal(p.ExcludedAssets)
	if err != nil {
		return fmt.Errorf("rings: 序列化 excluded_assets: %w", err)
	}
	if p.ExcludedAssets == nil {
		exAssets = []byte("[]")
	}
	scApproved, err := json.Marshal(p.ScopeApproved)
	if err != nil {
		return fmt.Errorf("rings: 序列化 scope_approved: %w", err)
	}
	if p.ScopeApproved == nil {
		scApproved = []byte("[]")
	}
	if _, err := tx.Exec(
		`INSERT INTO roe_state (task_id, allowed_tools, denied_tools, approval_threshold, intent_rules, excluded_assets, note, updated_rev)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id) DO UPDATE SET
		   allowed_tools      = excluded.allowed_tools,
		   denied_tools       = excluded.denied_tools,
		   approval_threshold = excluded.approval_threshold,
		   intent_rules       = excluded.intent_rules,
		   excluded_assets    = excluded.excluded_assets,
		   note               = excluded.note,
		   updated_rev        = excluded.updated_rev`,
		taskID, string(allowed), string(denied), string(p.ApprovalThreshold), string(rules), string(exAssets), p.Note, rev,
	); err != nil {
		return fmt.Errorf("rings: 投影写入任务 %s RoE: %w", taskID, err)
	}
	// P4-44 放行清单列(v11)单独补写(同事务,行已由上面的 UPSERT 就位):
	// 与 ExcludedAssets 同为"当前值"投影的组成部分,整体覆盖。
	if _, err := tx.Exec(
		`UPDATE roe_state SET scope_approved = ? WHERE task_id = ?`,
		string(scApproved), taskID,
	); err != nil {
		return fmt.Errorf("rings: 投影写入任务 %s 放行清单: %w", taskID, err)
	}
	// P4-49 注意事项列(v12)逐笔叠加:指针 nil = 本次不动(COALESCE 保旧
	// 值),非空指针整体覆盖(归一换行与首尾空白)。透传当前值的各
	// roe.updated 写入口不带本字段即自然保持,建任务/RoE 面板显式写。
	var creatorNotes any
	if p.CreatorNotes != nil {
		creatorNotes = NormalizeCreatorNotes(*p.CreatorNotes)
	}
	if _, err := tx.Exec(
		`UPDATE roe_state SET creator_notes = COALESCE(?, creator_notes) WHERE task_id = ?`,
		creatorNotes, taskID,
	); err != nil {
		return fmt.Errorf("rings: 投影写入任务 %s 注意事项: %w", taskID, err)
	}
	// P4-67 信息源清单列(v16)同款指针语义:nil = 本次不动;非空指针
	// 整体覆盖(空数组 = 清空)。条目校验:host 非空不重复(分诊自动授/
	// 牧者标记/面板撤销三写入口都提交完整新快照)。
	var infoSrcs any
	if p.InfoSources != nil {
		seenSrc := map[string]bool{}
		for _, s := range *p.InfoSources {
			if strings.TrimSpace(s.Host) == "" {
				return fmt.Errorf("rings: 信息源清单含空地址")
			}
			if seenSrc[s.Host] {
				return fmt.Errorf("rings: 信息源清单重复地址 %s", s.Host)
			}
			seenSrc[s.Host] = true
		}
		b, merr := json.Marshal(*p.InfoSources)
		if merr != nil {
			return fmt.Errorf("rings: 序列化 info_sources: %w", merr)
		}
		infoSrcs = string(b)
	}
	if _, err := tx.Exec(
		`UPDATE roe_state SET info_sources = COALESCE(?, info_sources) WHERE task_id = ?`,
		infoSrcs, taskID,
	); err != nil {
		return fmt.Errorf("rings: 投影写入任务 %s 信息源清单: %w", taskID, err)
	}
	return nil
}

// CreatorNotesMaxRunes 注意事项长度上限(多行合计;写入口共用——面板
// 表单与牧者 set_notes 同一上限)。
const CreatorNotesMaxRunes = 2000

// NormalizeCreatorNotes 注意事项的归一形态:首尾空白去除、CRLF 统一
// 为 LF、行内尾空白去除、空行折叠(多行要求的呈现与注入共用一份,
// 防服务端写入口与投影各写一套漂移)。
func NormalizeCreatorNotes(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// validateSwarmRecord 校验虫群原始执行记录。它不进 nodes/edges 投影——
// 双图只收业务节点,原始执行记录是审计流,查询走事件流(Events)。
func validateSwarmRecord(p SwarmRecord) error {
	if !swarmRecordTypes[p.Type] {
		return fmt.Errorf("%w: %s", ErrUnknownSwarmRecType, p.Type)
	}
	if p.StepID == "" || p.UnitID == "" {
		return ErrSwarmRecMissingField
	}
	if p.Type == SwarmRecToolCall && p.ToolName == "" {
		return ErrSwarmRecMissingField
	}
	return nil
}

func applyNodeAdded(tx *sql.Tx, taskID string, rev int64, p NodeAdded, occurredAt time.Time) error {
	nt := NodeType(p.NodeType)
	if !nodeTypes[nt] {
		return fmt.Errorf("%w: %s", ErrUnknownNodeType, p.NodeType)
	}
	if p.NodeID == "" {
		return ErrNodeIDEmpty
	}
	class := AssetClass(p.AssetClass)
	if nt == NodeAsset {
		if class == "" {
			class = AssetOther // 未知资产一律落入其他类兜底,不丢弃
		}
		if !assetClasses[class] {
			return fmt.Errorf("%w: %s", ErrUnknownAssetClass, p.AssetClass)
		}
	}
	if err := rejectExistingNode(tx, taskID, p.NodeID); err != nil {
		return err
	}
	detail, err := marshalDetail(p.Detail)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO nodes (task_id, node_id, node_type, status, title, asset_class, detail, created_rev, updated_rev, added_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, p.NodeID, string(nt), p.Status, p.Title, string(class), detail, rev, rev,
		fmtTimeUTC(occurredAt),
	); err != nil {
		return fmt.Errorf("rings: 投影写入节点 %s: %w", p.NodeID, err)
	}
	return nil
}

// amendableKeys node.amended 按节点类型的 detail 键白名单(P4-54 受控
// 入口:变更只开放操作面声明的字段,结构外一律拒——goal 的 predicate
// 由写入口按仲裁受限结构校验后入账,focus 为布尔;step 的 priority 限
// high|normal|low)。
var amendableKeys = map[NodeType]map[string]bool{
	NodeGoal: {"predicate": true, "focus": true},
	NodeStep: {"priority": true},
}

// applyNodeAmended node.amended 投影(P4-54):节点标题(指针覆盖)与
// detail(键白名单内合并)就地更新,updated_rev 推进;历史事件不动,
// 重放确定性(逐事件按同序合并)。
func applyNodeAmended(tx *sql.Tx, taskID string, rev int64, p NodeAmended, occurredAt time.Time) error {
	if p.NodeID == "" {
		return ErrNodeIDEmpty
	}
	var nt, title, detail string
	if err := tx.QueryRow(
		`SELECT node_type, title, detail FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, p.NodeID,
	).Scan(&nt, &title, &detail); err != nil {
		return fmt.Errorf("%w: task %s node %s", ErrNodeNotFound, taskID, p.NodeID)
	}
	allowed := amendableKeys[NodeType(nt)]
	if allowed == nil {
		return fmt.Errorf("%w: %s 节点不支持变更", ErrAmendNotAllowed, nt)
	}
	for k, v := range p.Detail {
		if !allowed[k] {
			return fmt.Errorf("%w: %s 节点的 %q 键不可变更", ErrAmendNotAllowed, nt, k)
		}
		if k == "focus" {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%w: focus 必须是布尔值", ErrAmendNotAllowed)
			}
		}
	}
	m := map[string]any{}
	if json.Unmarshal([]byte(detail), &m) != nil {
		m = map[string]any{}
	}
	for k, v := range p.Detail {
		if v == nil {
			delete(m, k) // 显式 null = 移除该键(如清除优先级回缺省)
			continue
		}
		m[k] = v
	}
	nextTitle := title
	if p.Title != nil {
		nextTitle = *p.Title
	}
	merged, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("rings: 变更序列化 detail: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE nodes SET title = ?, detail = ?, updated_rev = ? WHERE task_id = ? AND node_id = ?`,
		nextTitle, string(merged), rev, taskID, p.NodeID,
	); err != nil {
		return fmt.Errorf("rings: 投影变更节点 %s: %w", p.NodeID, err)
	}
	return nil
}

func applyNodeStateChanged(tx *sql.Tx, taskID string, rev int64, p NodeStateChanged, occurredAt time.Time) error {
	// P4-27 ⑥⑦:blocked 归因的机器标记(Note 恰为 model_fault/
	// determinant)随状态迁移合并进节点 detail(blocked_class 键),
	// 供受阻派生按归因过滤(确定性拒绝不计故障)。其余 Note(人工
	// 标记的迁移依据)照旧只留事件流,不进投影(P3-14b ④ 惯例)。
	// P4-51:scope_wait 目标(Note "scope_wait <host> <port>",对账唤醒
	// 的判定面)与范围批准回队标记(Note "scope_head",领取排序的队首
	// 权)同为机器标记,随迁移合并进 detail。
	if p.Note == "model_fault" || p.Note == "determinant" || p.Status == "cancelled" ||
		(p.Status == StepScopeWait && strings.HasPrefix(p.Note, "scope_wait ")) || p.Note == "scope_head" {
		var detail string
		if err := tx.QueryRow(
			`SELECT detail FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, p.NodeID,
		).Scan(&detail); err != nil {
			return fmt.Errorf("%w: task %s node %s", ErrNodeNotFound, taskID, p.NodeID)
		}
		m := map[string]any{}
		if json.Unmarshal([]byte(detail), &m) != nil {
			m = map[string]any{} // detail 损坏时重建(状态迁移不因旧值失败)
		}
		switch {
		case p.Status == "cancelled":
			m["cancellation_reason"] = p.Note
		case p.Status == StepScopeWait:
			// "scope_wait <host> <port>":冻结时携带的越界目标(对账唤醒
			// 按 host:port 重判范围;port 可缺省=全端口意图)。
			fields := strings.Fields(p.Note)
			m["scope_wait_host"] = fields[1]
			if len(fields) > 2 {
				m["scope_wait_port"] = fields[2]
			}
		case p.Note == "scope_head":
			m["scope_head"] = true
		default:
			m["blocked_class"] = p.Note
		}
		raw, err := marshalDetail(m)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE nodes SET status = ?, detail = ?, updated_rev = ? WHERE task_id = ? AND node_id = ?`,
			p.Status, raw, rev, taskID, p.NodeID,
		); err != nil {
			return fmt.Errorf("rings: 投影更新节点 %s 状态: %w", p.NodeID, err)
		}
		return markNodeConfirmed(tx, taskID, rev, p, occurredAt)
	}
	res, err := tx.Exec(
		`UPDATE nodes SET status = ?, updated_rev = ? WHERE task_id = ? AND node_id = ?`,
		p.Status, rev, taskID, p.NodeID,
	)
	if err != nil {
		return fmt.Errorf("rings: 投影更新节点 %s 状态: %w", p.NodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: task %s node %s", ErrNodeNotFound, taskID, p.NodeID)
	}
	return markNodeConfirmed(tx, taskID, rev, p, occurredAt)
}

// markNodeConfirmed 确认时刻列(v10):转 confirmed 的迁移记 confirmed_at/
// confirmed_rev(事实列表的时刻小字段);其余状态不动。
func markNodeConfirmed(tx *sql.Tx, taskID string, rev int64, p NodeStateChanged, occurredAt time.Time) error {
	if p.Status != "confirmed" {
		return nil
	}
	if _, err := tx.Exec(
		`UPDATE nodes SET confirmed_at = ?, confirmed_rev = ? WHERE task_id = ? AND node_id = ?`,
		fmtTimeUTC(occurredAt), rev, taskID, p.NodeID,
	); err != nil {
		return fmt.Errorf("rings: 投影更新节点 %s 确认时刻: %w", p.NodeID, err)
	}
	return nil
}

func applyEdgeAdded(tx *sql.Tx, taskID string, rev int64, p EdgeAdded) error {
	et := EdgeType(p.EdgeType)
	rule, ok := edgeRules[et]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownEdgeType, p.EdgeType)
	}
	if p.FromNode == "" || p.ToNode == "" {
		return ErrEdgeEndpointsEmpty
	}
	if p.FromNode == p.ToNode {
		return fmt.Errorf("%w: %s", ErrEdgeSelfLoop, p.FromNode)
	}
	fromType, err := nodeTypeOf(tx, taskID, p.FromNode)
	if err != nil {
		return err
	}
	toType, err := nodeTypeOf(tx, taskID, p.ToNode)
	if err != nil {
		return err
	}
	if !rule.from[fromType] || !rule.to[toType] {
		return fmt.Errorf("%w: %s(%s) ─%s→ %s(%s)", ErrEdgeRuleViolation, p.FromNode, fromType, et, p.ToNode, toType)
	}
	var exists int
	err = tx.QueryRow(
		`SELECT 1 FROM edges WHERE task_id = ? AND edge_type = ? AND from_node = ? AND to_node = ?`,
		taskID, string(et), p.FromNode, p.ToNode,
	).Scan(&exists)
	if err == nil {
		return fmt.Errorf("%w: task %s %s(%s→%s)", ErrEdgeExists, taskID, et, p.FromNode, p.ToNode)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rings: 查重边 %s(%s→%s): %w", et, p.FromNode, p.ToNode, err)
	}
	detail, err := marshalDetail(p.Detail)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO edges (task_id, edge_type, from_node, to_node, detail, created_rev)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, string(et), p.FromNode, p.ToNode, detail, rev,
	); err != nil {
		return fmt.Errorf("rings: 投影写入边 %s(%s→%s): %w", et, p.FromNode, p.ToNode, err)
	}
	return nil
}

func rejectExistingNode(tx *sql.Tx, taskID, nodeID string) error {
	var exists int
	err := tx.QueryRow(
		`SELECT 1 FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, nodeID,
	).Scan(&exists)
	if err == nil {
		return fmt.Errorf("%w: task %s node %s", ErrNodeExists, taskID, nodeID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rings: 查重节点 %s: %w", nodeID, err)
	}
	return nil
}

func nodeTypeOf(tx *sql.Tx, taskID, nodeID string) (NodeType, error) {
	var t string
	err := tx.QueryRow(
		`SELECT node_type FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, nodeID,
	).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: task %s node %s", ErrNodeNotFound, taskID, nodeID)
	}
	if err != nil {
		return "", fmt.Errorf("rings: 查询节点 %s 类型: %w", nodeID, err)
	}
	return NodeType(t), nil
}

// marshalDetail 序列化节点/边自由细节。encoding/json 对 map 键排序,
// 相同数据字节级一致,重放重建不产生漂移。
func marshalDetail(d map[string]any) ([]byte, error) {
	if d == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("rings: 序列化 detail: %w", err)
	}
	return b, nil
}

// rebuildProjections 清空投影并按 (task_id, revision) 全量重放事件流,
// 复用 applyEvent 的写入路径——增量维护与全量重放同一份代码,确定性有保证。
func rebuildProjections(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM edges`); err != nil {
		return fmt.Errorf("rings: 清空 edges 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM nodes`); err != nil {
		return fmt.Errorf("rings: 清空 nodes 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM roe_state`); err != nil {
		return fmt.Errorf("rings: 清空 roe_state 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM tasks`); err != nil {
		return fmt.Errorf("rings: 清空 tasks 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM unit_sessions`); err != nil {
		return fmt.Errorf("rings: 清空 unit_sessions 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_labels`); err != nil {
		return fmt.Errorf("rings: 清空 session_labels 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM approval_records`); err != nil {
		return fmt.Errorf("rings: 清空 approval_records 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM claim_cases`); err != nil {
		return fmt.Errorf("rings: 清空 claim_cases 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM hyp_states`); err != nil {
		return fmt.Errorf("rings: 清空 hyp_states 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM task_extras`); err != nil {
		return fmt.Errorf("rings: 清空 task_extras 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM closure_states`); err != nil {
		return fmt.Errorf("rings: 清空 closure_states 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM claim_qa`); err != nil {
		return fmt.Errorf("rings: 清空 claim_qa 投影: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM mirror_briefs`); err != nil {
		return fmt.Errorf("rings: 清空 mirror_briefs 投影: %w", err)
	}
	rows, err := tx.Query(
		`SELECT task_id, revision, kind, actor, payload, occurred_at FROM events ORDER BY task_id, revision`)
	if err != nil {
		return fmt.Errorf("rings: 读取事件流: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, kind, actor, payload, occurred string
		var rev int64
		if err := rows.Scan(&taskID, &rev, &kind, &actor, &payload, &occurred); err != nil {
			return fmt.Errorf("rings: 扫描事件: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return fmt.Errorf("rings: 解析事件 occurred_at: %w", err)
		}
		if err := applyEvent(tx, taskID, rev, EventKind(kind), actor, []byte(payload), at); err != nil {
			return fmt.Errorf("rings: 重放事件 task %s rev %d: %w", taskID, rev, err)
		}
	}
	return rows.Err()
}
