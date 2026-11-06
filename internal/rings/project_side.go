package rings

// P4-36 面板按需取数的读侧汇总投影(schema v10):applyEvent 在 nodes/
// edges 等既有投影之外,同事务维护下列小字段投影——列表类接口与详情
// 按条取数直读投影,不再触碰事件流全量扫描。增量维护与全量重放共用
// applyEvent 写入路径(rebuildProjections 同一份代码,确定性同源)。
//
//	unit_sessions    swarm.record session_started/finished → 单位会话汇总
//	session_labels   session.meta 末态 → 会话组织信息
//	approval_records approval.record → 审批记录追加日志
//	claim_cases      testclaim.record / 节点事件 → 判断卷宗摘要与时间线索引
//	hyp_states       hypothesis.record → 假设最新评估/验证结论
//	task_extras      created 描述 + creator 种子资产 → 任务摘要
//	closure_states   收口信号状态机末态
//	claim_qa         shepherd.record user_message → 成果追问计数
//	mirror_briefs    mirror.record → 镜留痕摘要(证据装配用)
//
// 写入一律"读-改-写"(SQL 只做裸列赋值,表达式与算术在 Go 侧完成),
// 单写者事务内无并发竞态。时刻列存 RFC3339Nano(UTC),读取侧解析呈现;
// JSON 列用结构体序列化(map 键排序,重放字节一致)。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// timeRFC 时刻列的存储格式。
const timeRFC = time.RFC3339Nano

// fmtTimeUTC 时刻 → 存储格式。
func fmtTimeUTC(t time.Time) string { return t.UTC().Format(timeRFC) }

// parseTimeCol 存储格式 → 时刻(坏值退零值,呈现侧如实)。
func parseTimeCol(s string) time.Time {
	if t, err := time.Parse(timeRFC, s); err == nil {
		return t
	}
	return time.Time{}
}

// FmtLocal 时刻列 → 面板呈现格式(本地时区;与账本事件侧读侧同一口径,
// server 层消费)。
func FmtLocal(s string) string {
	if t, err := time.Parse(timeRFC, s); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return s
}

// ---- 单位会话汇总(unit_sessions) ----

// noteFieldStr session_finished note 的字符串字段(按下一字段名截断)。
// note 形如 "reason=… turns=N status=… billable=Xs gate_wait=…",
// reason 是自然语句含空格,按 " turns=" 截断;与 swarm 执行器写入侧成对。
func noteFieldStr(note, field string) (string, bool) {
	i := strings.Index(note, field+"=")
	if i < 0 {
		return "", false
	}
	v := note[i+len(field)+1:]
	if j := strings.IndexAny(v, " s"); j >= 0 && field != "reason" {
		v = v[:j]
	}
	return v, true
}

// noteSeconds 数值字段("billable=3.5s" → 3.5;缺失/解析失败返回 0)。
func noteSeconds(note, field string) float64 {
	v, ok := noteFieldStr(note, field)
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// NoteTurns turns 整数字段(缺失/解析失败返回 0)。
func NoteTurns(note string) int { return int(noteSeconds(note, "turns")) }

// NoteReason 终因字段(reason= 到 turns= 之间——拒绝/受阻终因是自然语句)。
func NoteReason(note string) string {
	i := strings.Index(note, "reason=")
	if i < 0 {
		return ""
	}
	v := note[i+len("reason="):]
	if j := strings.Index(v, " turns="); j >= 0 {
		v = v[:j]
	}
	return v
}

// NoteStatus 终态字段(status=;缺失退 failed,与会话页读侧同口径)。
func NoteStatus(note string) string {
	i := strings.Index(note, "status=")
	if i >= 0 {
		st := note[i+len("status="):]
		if j := strings.IndexByte(st, ' '); j >= 0 {
			st = st[:j]
		}
		if st != "" {
			return st
		}
	}
	return "failed"
}

// applySwarmSession swarm.record → unit_sessions:session_started(transcript
// 非空)建行,键 = step+transcript(与会话列表去重键同源,幂等);
// session_finished 更新终态并累加 billable(原会话续跑链每段一记)。
func applySwarmSession(tx *sql.Tx, taskID string, rev int64, p SwarmRecord, occurredAt time.Time) error {
	if p.TranscriptID == "" {
		return nil
	}
	switch p.Type {
	case SwarmRecSessionStarted:
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO unit_sessions
			 (task_id, step_id, transcript_id, unit_id, started_at, start_rev)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			taskID, p.StepID, p.TranscriptID, p.UnitID,
			fmtTimeUTC(occurredAt), rev,
		); err != nil {
			return fmt.Errorf("rings: 投影写入单位会话: %w", err)
		}
	case SwarmRecSessionEnd:
		var billable float64
		if err := tx.QueryRow(
			`SELECT billable FROM unit_sessions
			 WHERE task_id = ? AND step_id = ? AND transcript_id = ?`,
			taskID, p.StepID, p.TranscriptID,
		).Scan(&billable); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // 无 started 前导(不可能形态),与事件读侧同口径跳过
			}
			return fmt.Errorf("rings: 读单位会话计费: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE unit_sessions SET
			   end_status = ?, finished_at = ?, turns = ?, reason = ?, billable = ?
			 WHERE task_id = ? AND step_id = ? AND transcript_id = ?`,
			NoteStatus(p.Note), fmtTimeUTC(occurredAt), NoteTurns(p.Note),
			NoteReason(p.Note), billable+noteSeconds(p.Note, "billable"),
			taskID, p.StepID, p.TranscriptID,
		); err != nil {
			return fmt.Errorf("rings: 投影更新单位会话收场: %w", err)
		}
	}
	return nil
}

// ---- 会话组织信息(session_labels) ----

// applySessionLabel session.meta → session_labels(指针字段语义:携带即改,
// 空串清除;置顶动作的版本号作置顶会话间的次序锚,同任务列表惯例)。
func applySessionLabel(tx *sql.Tx, taskID string, rev int64, p SessionMeta) error {
	var title, note string
	var pinned int
	var pinnedRev int64
	err := tx.QueryRow(
		`SELECT title, note, pinned, pinned_rev FROM session_labels
		 WHERE task_id = ? AND session_id = ?`, taskID, p.SessionID,
	).Scan(&title, &note, &pinned, &pinnedRev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rings: 读会话标记: %w", err)
	}
	if p.Title != nil {
		title = *p.Title
	}
	if p.Note != nil {
		note = *p.Note
	}
	if p.Pinned != nil {
		if *p.Pinned {
			pinned = 1
		} else {
			pinned = 0
		}
		pinnedRev = rev
	}
	if _, err := tx.Exec(
		`INSERT INTO session_labels (task_id, session_id, title, note, pinned, pinned_rev)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id, session_id) DO UPDATE SET
		   title = excluded.title, note = excluded.note,
		   pinned = excluded.pinned, pinned_rev = excluded.pinned_rev`,
		taskID, p.SessionID, title, note, pinned, pinnedRev,
	); err != nil {
		return fmt.Errorf("rings: 投影写入会话标记: %w", err)
	}
	return nil
}

// boolNum 布尔 → 0/1(投影整数列)。
func boolNum(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- 审批记录(approval_records) ----

// applyApprovalLog approval.record → approval_records(追加日志,事件序
// 即版本序;放行集合(forced)按 request_id 存在性判定,读取侧同口径)。
func applyApprovalLog(tx *sql.Tx, taskID string, rev int64, p ApprovalRecord, occurredAt time.Time) error {
	if _, err := tx.Exec(
		`INSERT INTO approval_records
		 (task_id, rev, request_id, step_id, unit_id, type, action_level, harm_class,
		  intent, tool_name, tool_args, note, deny_line, decided_by, reason, wait_millis, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, rev, p.RequestID, p.StepID, p.UnitID, p.Type, string(p.ActionLevel),
		string(p.HarmClass), p.Intent, p.ToolName, string(p.ToolArgs), p.Note,
		p.DenyLine, p.DecidedBy, p.Reason, p.WaitMillis, fmtTimeUTC(occurredAt),
	); err != nil {
		return fmt.Errorf("rings: 投影写入审批记录: %w", err)
	}
	return nil
}

// ---- 镜留痕摘要(mirror_briefs) ----

// applyMirrorBrief mirror.record → mirror_briefs(同交换重放留痕时后者
// 覆盖,与事件流"最后一条即最新"的读取语义一致)。
func applyMirrorBrief(tx *sql.Tx, taskID string, p MirrorRecord) error {
	if _, err := tx.Exec(
		`INSERT INTO mirror_briefs (task_id, exchange_id, type, method, url, status, tls, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id, exchange_id) DO UPDATE SET
		   type = excluded.type, method = excluded.method, url = excluded.url,
		   status = excluded.status, tls = excluded.tls, error = excluded.error`,
		taskID, p.ExchangeID, p.Type, p.Method, p.URL, p.Status, p.TLS, p.Error,
	); err != nil {
		return fmt.Errorf("rings: 投影写入镜留痕摘要: %w", err)
	}
	return nil
}

// ---- 假设结论(hyp_states) ----

// applyHypState hypothesis.record → hyp_states(latest-wins:evaluation 更新
// 评估裁决与验证步骤,result 更新验证步骤/单位/结论)。
func applyHypState(tx *sql.Tx, taskID string, p HypothesisRecord) error {
	var verifyStep, verifier, decision, reason, verdict string
	err := tx.QueryRow(
		`SELECT verify_step, verifier, decision, reason, verdict FROM hyp_states
		 WHERE task_id = ? AND hypothesis_id = ?`, taskID, p.HypothesisID,
	).Scan(&verifyStep, &verifier, &decision, &reason, &verdict)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rings: 读假设状态: %w", err)
	}
	switch p.Type {
	case HypothesisRecEvaluation:
		decision, reason = p.Decision, p.Reason
		if p.StepID != "" {
			verifyStep = p.StepID
		}
	case HypothesisRecResult:
		verifyStep, verifier, verdict = p.StepID, p.UnitID, p.Verdict
	}
	if _, err := tx.Exec(
		`INSERT INTO hyp_states (task_id, hypothesis_id, verify_step, verifier, decision, reason, verdict)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id, hypothesis_id) DO UPDATE SET
		   verify_step = excluded.verify_step, verifier = excluded.verifier,
		   decision = excluded.decision, reason = excluded.reason, verdict = excluded.verdict`,
		taskID, p.HypothesisID, verifyStep, verifier, decision, reason, verdict,
	); err != nil {
		return fmt.Errorf("rings: 投影写入假设状态: %w", err)
	}
	return nil
}

// ---- 成果追问计数(claim_qa) ----

// applyClaimQA shepherd.record user_message(带成果锚)→ claim_qa 计数
// (每 (成果 × 轮) 一行;累计问数、起止时刻,first_rev = 首问事件序,
// 读取侧按它保持首问出现次序)。
func applyClaimQA(tx *sql.Tx, taskID string, rev int64, p ShepherdRecord, occurredAt time.Time) error {
	if p.Type != ShepherdRecUserMessage || p.ClaimID == "" || p.Round <= 0 {
		return nil
	}
	at := fmtTimeUTC(occurredAt)
	var questions int
	var startedAt, lastAt string
	err := tx.QueryRow(
		`SELECT questions, started_at, last_at FROM claim_qa
		 WHERE task_id = ? AND claim_id = ? AND round = ?`,
		taskID, p.ClaimID, p.Round,
	).Scan(&questions, &startedAt, &lastAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rings: 读成果追问计数: %w", err)
	}
	isFirst := false
	if errors.Is(err, sql.ErrNoRows) {
		isFirst = true
	}
	questions++
	if startedAt == "" {
		startedAt = at
	}
	lastAt = at
	if isFirst {
		if _, err := tx.Exec(
			`INSERT INTO claim_qa (task_id, claim_id, round, questions, started_at, last_at, first_rev)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			taskID, p.ClaimID, p.Round, questions, startedAt, lastAt, rev,
		); err != nil {
			return fmt.Errorf("rings: 投影写入成果追问: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(
		`UPDATE claim_qa SET questions = ?, started_at = ?, last_at = ?
		 WHERE task_id = ? AND claim_id = ? AND round = ?`,
		questions, startedAt, lastAt, taskID, p.ClaimID, p.Round,
	); err != nil {
		return fmt.Errorf("rings: 投影更新成果追问: %w", err)
	}
	return nil
}

// ---- 任务摘要(task_extras)与收口状态机(closure_states) ----

// applyTaskExtrasCreated created 事件 → task_extras(描述与任务类型只在
// 建任务时定格——类型建后不可改,digestOf 的语义原样)。
func applyTaskExtrasCreated(tx *sql.Tx, taskID string, p TaskLifecycle) error {
	desc, _ := p.Detail["description"].(string)
	taskType, _ := p.Detail["task_type"].(string)
	if taskType != "" && !IsValidTaskType(taskType) {
		return fmt.Errorf("rings: 任务 %s 类型 %q 不在词表内(pentest|redteam)", taskID, taskType)
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO task_extras (task_id, description, task_type) VALUES (?, ?, ?)`,
		taskID, desc, taskType,
	); err != nil {
		return fmt.Errorf("rings: 投影写入任务摘要: %w", err)
	}
	return nil
}

// appendTaskTarget creator 落账的种子资产入任务目标域(手动补入不入,
// digestOf 语义原样)。
func appendTaskTarget(tx *sql.Tx, taskID, title string) error {
	if title == "" {
		return nil
	}
	var raw string
	err := tx.QueryRow(
		`SELECT targets FROM task_extras WHERE task_id = ?`, taskID,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // 无 created 前导(不可能形态),防御跳过
		}
		return fmt.Errorf("rings: 读任务目标域: %w", err)
	}
	var targets []string
	if json.Unmarshal([]byte(raw), &targets) != nil {
		targets = nil
	}
	targets = append(targets, title)
	b, err := json.Marshal(targets)
	if err != nil {
		return fmt.Errorf("rings: 序列化任务目标域: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE task_extras SET targets = ? WHERE task_id = ?`, string(b), taskID,
	); err != nil {
		return fmt.Errorf("rings: 投影更新任务目标域: %w", err)
	}
	return nil
}

// ClosureTaskConfigJSON 任务级收口配置的投影形态(与 overmind.
// ClosureTaskConfig 同字段;由 created detail.closure 或 config 记录
// 快照而来,读侧 json.Unmarshal 共用)。
type ClosureTaskConfigJSON struct {
	HasConfig bool `json:"has,omitempty"`
	Enabled   bool `json:"enabled,omitempty"`
	Rounds    int  `json:"rounds,omitempty"`
	Minutes   int  `json:"minutes,omitempty"`
}

// ParseClosureTaskConfig created detail.closure 的宽容解析(只认显式项;
// 与 overmind 侧同款语义的唯一实现,读侧共用)。
func ParseClosureTaskConfig(raw map[string]any) ClosureTaskConfigJSON {
	tc := ClosureTaskConfigJSON{HasConfig: true, Enabled: true}
	if v, ok := raw["enabled"].(bool); ok {
		tc.Enabled = v
	}
	if v, ok := raw["rounds"].(float64); ok && v > 0 {
		tc.Rounds = int(v)
	}
	if v, ok := raw["minutes"].(float64); ok && v > 0 {
		tc.Minutes = int(v)
	}
	return tc
}

// closureRow closure_states 行的可变视图(读-改-写)。
type closureRow struct {
	anchorAt   string
	anchorKind string
	rounds     int
	pending    string // 待决提醒的 ClosureRecord JSON(空 = 无)
	pendingAt  string
	parked     bool
	taskConfig string
}

// loadClosure 读行(无行 = 零值新任务)。
func loadClosure(tx *sql.Tx, taskID string) (*closureRow, error) {
	row := &closureRow{}
	var parked int
	err := tx.QueryRow(
		`SELECT anchor_at, anchor_kind, rounds, pending, pending_at, pending_parked, task_config
		 FROM closure_states WHERE task_id = ?`, taskID,
	).Scan(&row.anchorAt, &row.anchorKind, &row.rounds, &row.pending, &row.pendingAt, &parked, &row.taskConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rings: 读收口状态: %w", err)
	}
	row.parked = parked != 0
	return row, nil
}

// save 写行(upsert)。
func (r *closureRow) save(tx *sql.Tx, taskID string) error {
	parked := 0
	if r.parked {
		parked = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO closure_states
		 (task_id, anchor_at, anchor_kind, rounds, pending, pending_at, pending_parked, task_config)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id) DO UPDATE SET
		   anchor_at = excluded.anchor_at, anchor_kind = excluded.anchor_kind,
		   rounds = excluded.rounds, pending = excluded.pending,
		   pending_at = excluded.pending_at, pending_parked = excluded.pending_parked,
		   task_config = excluded.task_config`,
		taskID, r.anchorAt, r.anchorKind, r.rounds, r.pending, r.pendingAt, parked, r.taskConfig,
	); err != nil {
		return fmt.Errorf("rings: 投影写入收口状态: %w", err)
	}
	return nil
}

// closureAnchorFinding 新发现锚点(事实/判断入账,或手动/发现来源资产
// 入图;种子资产随建任务表单入账在首轮研判前,不算执行中的新发现)。
func closureAnchorFinding(tx *sql.Tx, taskID string, occurredAt time.Time) error {
	row, err := loadClosure(tx, taskID)
	if err != nil {
		return err
	}
	row.anchorAt, row.anchorKind, row.rounds = fmtTimeUTC(occurredAt), ClosureAnchorFinding, 0
	row.pending, row.pendingAt, row.parked = "", "", false
	return row.save(tx, taskID)
}

// applyTaskLifecycleSide created 事件 → 收口状态初始化(锚点 = 建任务
// 时刻;初始配置自 detail.closure)。
func applyTaskLifecycleSide(tx *sql.Tx, taskID string, p TaskLifecycle, occurredAt time.Time) error {
	if p.Action != "created" {
		return nil
	}
	row, err := loadClosure(tx, taskID)
	if err != nil {
		return err
	}
	if row.anchorAt == "" {
		row.anchorAt, row.anchorKind = fmtTimeUTC(occurredAt), ClosureAnchorCreated
	}
	if raw, ok := p.Detail["closure"].(map[string]any); ok {
		b, err := json.Marshal(ParseClosureTaskConfig(raw))
		if err != nil {
			return fmt.Errorf("rings: 序列化收口配置: %w", err)
		}
		row.taskConfig = string(b)
	}
	return row.save(tx, taskID)
}

// applyOvermindRoundSide 研判轮计数(assessment)。
func applyOvermindRoundSide(tx *sql.Tx, taskID string, p OvermindRecord) error {
	if p.Type != OvermindRecAssessment {
		return nil
	}
	row, err := loadClosure(tx, taskID)
	if err != nil {
		return err
	}
	row.rounds++
	return row.save(tx, taskID)
}

// applyClosureRecordSide closure.record → 收口状态机(prompt 置待决、
// dismiss 搁置、continue 重锚、end 清待决、config 覆盖配置;语义与
// overmind.ClosureOf 的事件回放一致——本投影即该回放的增量形态)。
func applyClosureRecordSide(tx *sql.Tx, taskID string, p ClosureRecord, occurredAt time.Time) error {
	row, err := loadClosure(tx, taskID)
	if err != nil {
		return err
	}
	switch p.Type {
	case ClosureRecPrompt:
		b, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("rings: 序列化收口提醒: %w", err)
		}
		row.pending, row.pendingAt, row.parked = string(b), fmtTimeUTC(occurredAt), false
	case ClosureRecDismiss:
		if row.pending != "" {
			row.parked = true
		}
	case ClosureRecContinue:
		row.anchorAt, row.anchorKind, row.rounds = fmtTimeUTC(occurredAt), ClosureAnchorContinue, 0
		row.pending, row.pendingAt, row.parked = "", "", false
	case ClosureRecEnd:
		row.pending, row.pendingAt, row.parked = "", "", false
	case ClosureRecConfig:
		b, err := json.Marshal(ClosureTaskConfigJSON{
			HasConfig: true, Enabled: p.Enabled != nil && *p.Enabled,
			Rounds: p.Rounds, Minutes: p.Minutes,
		})
		if err != nil {
			return fmt.Errorf("rings: 序列化收口配置: %w", err)
		}
		row.taskConfig = string(b)
	}
	return row.save(tx, taskID)
}
