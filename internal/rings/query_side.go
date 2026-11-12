package rings

// P4-36 读侧投影查询:列表类接口与详情按条取数的直读入口(与写侧
// project_side/project_case 同表成对)。全部单表/索引小查询,零事件流
// 扫描;时刻列按 FmtLocal 呈现、锚点类时刻按 parseTimeCol 还原。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ---- 任务摘要(task_extras) ----

// TaskExtrasRow 任务摘要行(描述 + 种子目标域 + 任务类型,P4-44)。
type TaskExtrasRow struct {
	Description string
	Targets     []string
	TaskType    string // pentest|redteam|""(存量未标)
}

// TaskExtras 读单任务摘要(无行返回 ok=false,消费侧退空呈现)。
func (s *Store) TaskExtras(ctx context.Context, taskID string) (TaskExtrasRow, bool, error) {
	var row TaskExtrasRow
	var targets string
	err := s.db.QueryRowContext(ctx,
		`SELECT description, targets, task_type FROM task_extras WHERE task_id = ?`, taskID,
	).Scan(&row.Description, &targets, &row.TaskType)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("rings: 读任务摘要: %w", err)
	}
	_ = json.Unmarshal([]byte(targets), &row.Targets)
	if row.Targets == nil {
		row.Targets = []string{}
	}
	return row, true, nil
}

// ---- 收口状态(closure_states) ----

// ClosureStateRow 收口状态机的投影读形态(overmind.ClosureOf 的数据面)。
type ClosureStateRow struct {
	AnchorAt   time.Time
	AnchorKind string
	Rounds     int
	Pending    *ClosureRecord // 待决提醒(解析后的记录)
	PromptedAt time.Time
	Parked     bool
	TaskConfig ClosureTaskConfigJSON
	HasState   bool // 库内已有状态行(created 落过即有)
}

// ClosureState 读单任务收口状态(无行返回零值 + HasState=false)。
func (s *Store) ClosureState(ctx context.Context, taskID string) (ClosureStateRow, error) {
	var row ClosureStateRow
	var anchorAt, pending, pendingAt, taskConfig string
	var parked int
	err := s.db.QueryRowContext(ctx,
		`SELECT anchor_at, anchor_kind, rounds, pending, pending_at, pending_parked, task_config
		 FROM closure_states WHERE task_id = ?`, taskID,
	).Scan(&anchorAt, &row.AnchorKind, &row.Rounds, &pending, &pendingAt, &parked, &taskConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return row, fmt.Errorf("rings: 读收口状态: %w", err)
	}
	row.HasState = true
	row.AnchorAt, row.PromptedAt = parseTimeCol(anchorAt), parseTimeCol(pendingAt)
	row.Parked = parked != 0
	if pending != "" {
		rec := &ClosureRecord{}
		if json.Unmarshal([]byte(pending), rec) == nil {
			row.Pending = rec
		}
	}
	if taskConfig != "" {
		_ = json.Unmarshal([]byte(taskConfig), &row.TaskConfig)
	}
	return row, nil
}

// ---- 单位会话汇总(unit_sessions) ----

// UnitSessionRow 虫群会话列表行(started 升序 = 插入序;end_status 空 =
// 未收场,呈现层按步骤当前状态取态)。
type UnitSessionRow struct {
	StepID      string
	Transcript  string
	UnitID      string
	StartedAt   string // 呈现格式(FmtLocal 已换算)
	StartRev    int64
	EndStatus   string
	FinishedAt  string
	Turns       int
	Reason      string
	BillableSec float64
}

// UnitSessions 列任务的全部单位会话(start_rev 升序)。
func (s *Store) UnitSessions(ctx context.Context, taskID string) ([]UnitSessionRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT step_id, transcript_id, unit_id, started_at, start_rev,
		   end_status, finished_at, turns, reason, billable
		 FROM unit_sessions WHERE task_id = ? ORDER BY start_rev`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询单位会话: %w", err)
	}
	defer rows.Close()
	var out []UnitSessionRow
	for rows.Next() {
		var r UnitSessionRow
		var startedAt, finishedAt string
		if err := rows.Scan(&r.StepID, &r.Transcript, &r.UnitID, &startedAt, &r.StartRev,
			&r.EndStatus, &finishedAt, &r.Turns, &r.Reason, &r.BillableSec); err != nil {
			return nil, fmt.Errorf("rings: 扫描单位会话: %w", err)
		}
		r.StartedAt, r.FinishedAt = FmtLocal(startedAt), FmtLocal(finishedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionAnchorOf 单位×步骤最近一次 session_started 的转录锚(溯源视图
// 的会话跳转锚;rev 上界取 0 = 不限)。
func (s *Store) SessionAnchorOf(ctx context.Context, taskID, unitID, stepID string, beforeRev int64) (string, error) {
	q := `SELECT transcript_id FROM unit_sessions
	      WHERE task_id = ? AND unit_id = ? AND step_id = ?`
	args := []any{taskID, unitID, stepID}
	if beforeRev > 0 {
		q += ` AND start_rev < ?`
		args = append(args, beforeRev)
	}
	q += ` ORDER BY start_rev DESC LIMIT 1`
	var sid string
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("rings: 查会话锚: %w", err)
	}
	return sid, nil
}

// StepLatestSession 步骤最近一次单位会话(P4-64 ⑤ 节点抽屉"单位/会话
// 记录"字段的数据面;一 Step 一单位,重派轮次各有会话时取最近)。
func (s *Store) StepLatestSession(ctx context.Context, taskID, stepID string) (unitID, transcriptID string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT unit_id, transcript_id FROM unit_sessions
		 WHERE task_id = ? AND step_id = ? ORDER BY start_rev DESC LIMIT 1`,
		taskID, stepID,
	).Scan(&unitID, &transcriptID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("rings: 查步骤会话: %w", err)
	}
	return unitID, transcriptID, true, nil
}

// ---- 会话组织信息(session_labels) ----

// SessionLabels 会话组织信息投影(替代事件流扫描;无行返回空表)。
func (s *Store) sessionLabelsOf(ctx context.Context, taskID string) (map[string]SessionLabel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id, title, note, pinned, pinned_rev
		 FROM session_labels WHERE task_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询会话标记: %w", err)
	}
	defer rows.Close()
	out := map[string]SessionLabel{}
	for rows.Next() {
		var sid, title, note string
		var pinned, pinnedRev int
		if err := rows.Scan(&sid, &title, &note, &pinned, &pinnedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描会话标记: %w", err)
		}
		out[sid] = SessionLabel{Title: title, Note: note, Pinned: pinned != 0, PinnedRev: int64(pinnedRev)}
	}
	return out, rows.Err()
}

// ---- 审批记录(approval_records) ----

// ApprovalRow 审批记录投影行(字段与 ApprovalRecord 一一对应;occurred_at
// 已换算呈现格式)。
type ApprovalRow struct {
	Rev         int64
	RequestID   string
	StepID      string
	UnitID      string
	Type        string
	ActionLevel string
	HarmClass   string
	Intent      string
	ToolName    string
	ToolArgs    string
	Note        string
	DenyLine    string
	DecidedBy   string
	Reason      string
	WaitMillis  int64
	OccurredAt  string
}

// ApprovalRows 列任务全部审批记录(rev 升序;倒序呈现由消费侧翻)。
func (s *Store) ApprovalRows(ctx context.Context, taskID string) ([]ApprovalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rev, request_id, step_id, unit_id, type, action_level, harm_class,
		   intent, tool_name, tool_args, note, deny_line, decided_by, reason,
		   wait_millis, occurred_at
		 FROM approval_records WHERE task_id = ? ORDER BY rev`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询审批记录: %w", err)
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var occurred string
		if err := rows.Scan(&r.Rev, &r.RequestID, &r.StepID, &r.UnitID, &r.Type,
			&r.ActionLevel, &r.HarmClass, &r.Intent, &r.ToolName, &r.ToolArgs,
			&r.Note, &r.DenyLine, &r.DecidedBy, &r.Reason, &r.WaitMillis, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描审批记录: %w", err)
		}
		r.OccurredAt = FmtLocal(occurred)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApprovalByRequest 某请求的全部记录(rev 升序;最后一条即最新——
// 研判后放行的目标定位)。
func (s *Store) ApprovalByRequest(ctx context.Context, taskID, requestID string) ([]ApprovalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rev, request_id, step_id, unit_id, type, action_level, harm_class,
		   intent, tool_name, tool_args, note, deny_line, decided_by, reason,
		   wait_millis, occurred_at
		 FROM approval_records WHERE task_id = ? AND request_id = ? ORDER BY rev`,
		taskID, requestID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询请求审批记录: %w", err)
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var occurred string
		if err := rows.Scan(&r.Rev, &r.RequestID, &r.StepID, &r.UnitID, &r.Type,
			&r.ActionLevel, &r.HarmClass, &r.Intent, &r.ToolName, &r.ToolArgs,
			&r.Note, &r.DenyLine, &r.DecidedBy, &r.Reason, &r.WaitMillis, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描请求审批记录: %w", err)
		}
		r.OccurredAt = FmtLocal(occurred)
		out = append(out, r)
	}
	return out, rows.Err()
}

// StepDenyLatest 每步骤最近一次 denied 记录(approval_denied 终态的拒绝
// 来源;事件升序扫描的 lastDeny 同口径)。
func (s *Store) StepDenyLatest(ctx context.Context, taskID string) (map[string]ApprovalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rev, request_id, step_id, unit_id, type, action_level, harm_class,
		   intent, tool_name, tool_args, note, deny_line, decided_by, reason,
		   wait_millis, occurred_at
		 FROM approval_records
		 WHERE task_id = ? AND type = ? AND step_id != '' ORDER BY rev`,
		taskID, ApprovalRecDenied,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询步骤拒绝记录: %w", err)
	}
	defer rows.Close()
	out := map[string]ApprovalRow{}
	for rows.Next() {
		var r ApprovalRow
		var occurred string
		if err := rows.Scan(&r.Rev, &r.RequestID, &r.StepID, &r.UnitID, &r.Type,
			&r.ActionLevel, &r.HarmClass, &r.Intent, &r.ToolName, &r.ToolArgs,
			&r.Note, &r.DenyLine, &r.DecidedBy, &r.Reason, &r.WaitMillis, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描步骤拒绝记录: %w", err)
		}
		r.OccurredAt = FmtLocal(occurred)
		out[r.StepID] = r // rev 升序,留在最后的是最新
	}
	return out, rows.Err()
}

// ScopePendingCount 范围待决卡数(P4-51:任务行/RoE 红点与 gate 待决
// 合并计数)——type=scope_wait 且同 request_id 无 approved/denied/access
// 收口记录的目标数(同一目标一张卡,去重即数卡)。P4-69:access(允许
// 访问)补入收口集——与待决区显示层(最新记录口径)一致,允许访问裁决
// 后红点即消(此前仅显示层消卡、计数层不减的口径分裂)。
func (s *Store) ScopePendingCount(ctx context.Context, taskID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (
		   SELECT request_id FROM approval_records
		    WHERE task_id = ? AND type = ?
		   EXCEPT
		   SELECT request_id FROM approval_records
		    WHERE task_id = ? AND type IN (?, ?, ?)
		 )`, taskID, ApprovalRecScopeWait, taskID, ApprovalRecApproved, ApprovalRecDenied, ApprovalRecAccess,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("rings: 统计范围待决卡: %w", err)
	}
	return n, nil
}

// PendingScopeCards 当前待决的全部范围卡(P4-69 ①:摘除联动收口的卡面
// ——每个 request_id 的最新记录恰为 scope_wait 即待决,与面板待决区
// scopePendingViews 的"最新记录"口径同源,红队可否决卡/渗透拦截卡/
// 经其他通道裁决过的卡各归各)。投影表小查询,零事件流扫描。
func (s *Store) PendingScopeCards(ctx context.Context, taskID string) ([]ApprovalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.rev, a.request_id, a.step_id, a.unit_id, a.type, a.action_level, a.harm_class,
		   a.intent, a.tool_name, a.tool_args, a.note, a.deny_line, a.decided_by, a.reason,
		   a.wait_millis, a.occurred_at
		 FROM approval_records a
		 WHERE a.task_id = ? AND a.type = ?
		   AND NOT EXISTS (
		     SELECT 1 FROM approval_records b
		      WHERE b.task_id = a.task_id AND b.request_id = a.request_id AND b.rev > a.rev
		   )`,
		taskID, ApprovalRecScopeWait,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询待决范围卡: %w", err)
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var occurred string
		if err := rows.Scan(&r.Rev, &r.RequestID, &r.StepID, &r.UnitID, &r.Type,
			&r.ActionLevel, &r.HarmClass, &r.Intent, &r.ToolName, &r.ToolArgs,
			&r.Note, &r.DenyLine, &r.DecidedBy, &r.Reason, &r.WaitMillis, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描待决范围卡: %w", err)
		}
		r.OccurredAt = FmtLocal(occurred)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 假设结论(hyp_states) ----

// HypStateRow 假设最新评估/验证结论。
type HypStateRow struct {
	VerifyStep string
	Verifier   string
	Decision   string
	Reason     string
	Verdict    string
}

// HypStates 列任务全部假设结论(hypothesis_id → 行)。
func (s *Store) HypStates(ctx context.Context, taskID string) (map[string]HypStateRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hypothesis_id, verify_step, verifier, decision, reason, verdict
		 FROM hyp_states WHERE task_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询假设结论: %w", err)
	}
	defer rows.Close()
	out := map[string]HypStateRow{}
	for rows.Next() {
		var hypID string
		var r HypStateRow
		if err := rows.Scan(&hypID, &r.VerifyStep, &r.Verifier, &r.Decision, &r.Reason, &r.Verdict); err != nil {
			return nil, fmt.Errorf("rings: 扫描假设结论: %w", err)
		}
		out[hypID] = r
	}
	return out, rows.Err()
}

// ---- 成果追问计数(claim_qa) ----

// ClaimQARow 一轮成果追问会话的计数(升序)。
type ClaimQARow struct {
	ClaimID   string
	Round     int
	Questions int
	StartedAt string
	LastAt    string
}

// ClaimQAs 列任务全部成果追问会话(首问事件序——与事件流扫描时代的
// 出现次序一致)。
func (s *Store) ClaimQAs(ctx context.Context, taskID string) ([]ClaimQARow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT claim_id, round, questions, started_at, last_at
		 FROM claim_qa WHERE task_id = ? ORDER BY first_rev`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询成果追问: %w", err)
	}
	defer rows.Close()
	var out []ClaimQARow
	for rows.Next() {
		var r ClaimQARow
		var startedAt, lastAt string
		if err := rows.Scan(&r.ClaimID, &r.Round, &r.Questions, &startedAt, &lastAt); err != nil {
			return nil, fmt.Errorf("rings: 扫描成果追问: %w", err)
		}
		r.StartedAt, r.LastAt = FmtLocal(startedAt), FmtLocal(lastAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 镜留痕摘要(mirror_briefs) ----

// MirrorBriefRow 镜留痕摘要(证据装配用)。
type MirrorBriefRow struct {
	ExchangeID string
	Type       string
	Method     string
	URL        string
	Status     int
	TLS        string
	Error      string
}

// MirrorBriefs 按交换 ID 批量取摘要(不在库内的 ID 不出现在结果里——
// 悬空引用如实跳过,与事件读侧同口径)。
func (s *Store) MirrorBriefs(ctx context.Context, taskID string, ids []string) (map[string]MirrorBriefRow, error) {
	out := make(map[string]MirrorBriefRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// IN 清单按固定上限分批(占位符逐个绑定,非拼接)。
	for start := 0; start < len(ids); start += 64 {
		end := start + 64
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		q := `SELECT exchange_id, type, method, url, status, tls, error
		      FROM mirror_briefs WHERE task_id = ? AND exchange_id IN (`
		args := []any{taskID}
		for i, id := range batch {
			if i > 0 {
				q += `, `
			}
			q += `?`
			args = append(args, id)
		}
		q += `)`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("rings: 查询镜留痕摘要: %w", err)
		}
		for rows.Next() {
			var r MirrorBriefRow
			if err := rows.Scan(&r.ExchangeID, &r.Type, &r.Method, &r.URL, &r.Status, &r.TLS, &r.Error); err != nil {
				rows.Close()
				return nil, fmt.Errorf("rings: 扫描镜留痕摘要: %w", err)
			}
			out[r.ExchangeID] = r
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ---- 判断卷宗(claim_cases) ----

// ClaimDigest 判断的列表摘要(claim_cases 小字段)。Statement/Impact/
// Facts 为定稿校对后的声明摘要(P4-59 ①:空 = 未生成,读侧退节点原文)。
type ClaimDigest struct {
	ClaimID              string
	FalsifyStep          string
	Verifier             string
	PendingReason        string
	VerdictRev           int64
	Severity             string
	ReproSteps           int
	RetestVerdict        string
	RetestRound          int
	RetestAt             string
	ReplayVerdict        string
	ReplayRound          int
	ReplayAt             string
	HasEvidence          bool
	ReportExcluded       bool
	ReportExcludedReason string
	EvidenceBinds        int
	ReproEvents          int
	Statement            string          // 校对后存在性描述(空 = 未生成)
	Impact               string          // 校对后影响面(空 = 未生成)
	Facts                []FactStatement // 校对后事实描述(空 = 未生成)
}

// ClaimCaseProjection 判断卷宗投影(全列;详情与报告装配的数据面)。
// Timeline 的 At 为 RFC3339Nano 存储态(消费侧 FmtLocal 呈现);
// TimingView 由增量态换算(Rounds = 当前战役步骤数)。
type ClaimCaseProjection struct {
	ClaimDigest
	Timeline     []CaseTimelineEntry
	Alternatives []Alternative
	AltKilled    map[string]bool
	Repro        *ReproDigest
	Summary      *SummaryDigest
	Evidence     []CaseEvidenceBind
	Discoverer   string
	DiscoverStep string
	DiscoverSess string
	Timing       TimingView
	// RoundHumans 逐轮反证人话三段(P4-64 ①:每轮随 falsify 记录同批
	// 冻结,按轮 upsert;读侧与存量 repro.Rounds 合并,新事件优先)。
	RoundHumans map[int]FalsifyRoundHuman
	// RetestNotes 复测备注校对稿(P4-64 ⑦:按轮匹配,空 = 该轮无校对
	// 稿,读侧退机器记录 Note 原文)。
	RetestNotes map[int]string
}

// TimingView 耗时拆解呈现视图。
type TimingView struct {
	LandedAt   string
	TerminalAt string
	QueueSec   float64
	ExecSec    float64
	Rounds     int
}

// claimDigestCols 摘要列清单(列表读只取小字段,不触 JSON 大列)。
const claimDigestCols = `claim_id, falsify_step, verifier, pending_reason, verdict_rev,
  severity, repro_steps, retest_verdict, retest_round, retest_at, replay_verdict,
  replay_round, replay_at, has_evidence, report_excluded, report_excluded_reason,
  evidence_binds, repro_events`

func scanDigest(scan func(...any) error) (ClaimDigest, error) {
	var d ClaimDigest
	var hasEvidence, reportExcluded int
	var rawSummary string
	if err := scan(&d.ClaimID, &d.FalsifyStep, &d.Verifier, &d.PendingReason, &d.VerdictRev,
		&d.Severity, &d.ReproSteps, &d.RetestVerdict, &d.RetestRound, &d.RetestAt,
		&d.ReplayVerdict, &d.ReplayRound, &d.ReplayAt,
		&hasEvidence, &reportExcluded, &d.ReportExcludedReason,
		&d.EvidenceBinds, &d.ReproEvents, &rawSummary); err != nil {
		return d, err
	}
	d.HasEvidence, d.ReportExcluded = hasEvidence != 0, reportExcluded != 0
	if rawSummary != "" {
		var s SummaryDigest
		if json.Unmarshal([]byte(rawSummary), &s) == nil {
			d.Statement, d.Impact, d.Facts = s.Statement, s.Impact, s.Facts
		}
	}
	return d, nil
}

// ClaimDigests 列任务全部判断摘要(claim_id → 摘要)。附读定稿校对的
// 声明摘要小列(P4-59 ①:发现列表与事实页的文本优先来源,小 JSON 不触
// repro 大列)。
func (s *Store) ClaimDigests(ctx context.Context, taskID string) (map[string]ClaimDigest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+claimDigestCols+`, summary FROM claim_cases WHERE task_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询判断摘要: %w", err)
	}
	defer rows.Close()
	out := map[string]ClaimDigest{}
	for rows.Next() {
		d, err := scanDigest(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("rings: 扫描判断摘要: %w", err)
		}
		out[d.ClaimID] = d
	}
	return out, rows.Err()
}

// ClaimCaseOf 读单条判断卷宗(全列;不存在返回 ok=false)。
func (s *Store) ClaimCaseOf(ctx context.Context, taskID, claimID string) (*ClaimCaseProjection, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+claimDigestCols+`, timeline, alternatives, alt_killed, repro,
		   evidence, discoverer, discover_step, discover_session, timing, summary,
		   round_humans, retest_notes
		 FROM claim_cases WHERE task_id = ? AND claim_id = ?`, taskID, claimID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("rings: 查询判断卷宗: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	p, err := scanClaimCase(rows.Scan)
	if err != nil {
		return nil, false, fmt.Errorf("rings: 扫描判断卷宗: %w", err)
	}
	return p, true, rows.Err()
}

// ClaimCases 列任务全部判断卷宗(全列;报告装配用,claim_id 升序)。
func (s *Store) ClaimCases(ctx context.Context, taskID string) ([]*ClaimCaseProjection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+claimDigestCols+`, timeline, alternatives, alt_killed, repro,
		   evidence, discoverer, discover_step, discover_session, timing, summary,
		   round_humans, retest_notes
		 FROM claim_cases WHERE task_id = ? ORDER BY claim_id`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询判断卷宗: %w", err)
	}
	defer rows.Close()
	var out []*ClaimCaseProjection
	for rows.Next() {
		p, err := scanClaimCase(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("rings: 扫描判断卷宗: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// scanClaimCase 全列单行扫描:摘要小字段 + JSON 列解码 + 耗时态换算。
func scanClaimCase(scan func(...any) error) (*ClaimCaseProjection, error) {
	var p ClaimCaseProjection
	var rawTimeline, rawAlts, rawKilled, rawRepro, rawEvidence, rawTiming, rawSummary string
	var rawRounds, rawNotes string
	var hasEvidence, reportExcluded int
	err := scan(&p.ClaimID, &p.FalsifyStep, &p.Verifier, &p.PendingReason, &p.VerdictRev,
		&p.Severity, &p.ReproSteps, &p.RetestVerdict, &p.RetestRound, &p.RetestAt,
		&p.ReplayVerdict, &p.ReplayRound, &p.ReplayAt,
		&hasEvidence, &reportExcluded, &p.ReportExcludedReason,
		&p.EvidenceBinds, &p.ReproEvents,
		&rawTimeline, &rawAlts, &rawKilled, &rawRepro, &rawEvidence,
		&p.Discoverer, &p.DiscoverStep, &p.DiscoverSess, &rawTiming, &rawSummary,
		&rawRounds, &rawNotes)
	if err != nil {
		return nil, err
	}
	p.HasEvidence, p.ReportExcluded = hasEvidence != 0, reportExcluded != 0
	if rawTimeline != "" {
		_ = json.Unmarshal([]byte(rawTimeline), &p.Timeline)
	}
	if rawAlts != "" {
		_ = json.Unmarshal([]byte(rawAlts), &p.Alternatives)
	}
	p.AltKilled = map[string]bool{}
	if rawKilled != "" && rawKilled != "[]" {
		var ids []string
		if json.Unmarshal([]byte(rawKilled), &ids) == nil {
			for _, id := range ids {
				p.AltKilled[id] = true
			}
		}
	}
	if rawRepro != "" {
		p.Repro = &ReproDigest{}
		if json.Unmarshal([]byte(rawRepro), p.Repro) != nil {
			p.Repro = nil
		}
	}
	if rawSummary != "" {
		p.Summary = &SummaryDigest{}
		if json.Unmarshal([]byte(rawSummary), p.Summary) != nil {
			p.Summary = nil
		}
	}
	if rawEvidence != "" {
		_ = json.Unmarshal([]byte(rawEvidence), &p.Evidence)
	}
	if rawRounds != "" {
		var m map[string]FalsifyRoundHuman
		if json.Unmarshal([]byte(rawRounds), &m) == nil {
			p.RoundHumans = map[int]FalsifyRoundHuman{}
			for k, v := range m {
				if n, cerr := strconv.Atoi(k); cerr == nil {
					v.Round = n
					p.RoundHumans[n] = v
				}
			}
		}
	}
	if rawNotes != "" {
		var m map[string]string
		if json.Unmarshal([]byte(rawNotes), &m) == nil {
			p.RetestNotes = map[int]string{}
			for k, v := range m {
				if n, cerr := strconv.Atoi(k); cerr == nil && v != "" {
					p.RetestNotes[n] = v
				}
			}
		}
	}
	if rawTiming != "" {
		var t TimingDigest
		if json.Unmarshal([]byte(rawTiming), &t) == nil {
			p.Timing = TimingView{
				LandedAt: FmtLocal(t.LandedAt), TerminalAt: FmtLocal(t.TerminalAt),
				QueueSec: t.QueueSec, ExecSec: t.ExecSec, Rounds: len(t.Steps),
			}
		}
	}
	return &p, nil
}

// NodePolishRow 节点显示层包装(node_polish 投影行):事实取 Title/
// Statement,假设取 Title/Basis/Reason;空字段 = 未生成,读侧退原文。
// Unpolished 为标题未过机器语闸门的如实标注(P4-24 ② 同款,读侧渲染
// 一行说明)。
type NodePolishRow struct {
	NodeID     string
	Title      string
	Statement  string
	Basis      string
	Reason     string
	Unpolished bool
	Rev        int64
}

// NodePolish 列任务全部节点显示层包装(node_id → 行)。事实/假设标题
// 与文本的读侧优先表:一次小查询,列表与图装配共用(空表 = 全退原文)。
func (s *Store) NodePolish(ctx context.Context, taskID string) (map[string]NodePolishRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, title, statement, basis, reason, unpolished, rev
		 FROM node_polish WHERE task_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询节点文本包装: %w", err)
	}
	defer rows.Close()
	out := map[string]NodePolishRow{}
	for rows.Next() {
		var r NodePolishRow
		var unpolished int
		if err := rows.Scan(&r.NodeID, &r.Title, &r.Statement, &r.Basis, &r.Reason, &unpolished, &r.Rev); err != nil {
			return nil, fmt.Errorf("rings: 扫描节点文本包装: %w", err)
		}
		r.Unpolished = unpolished != 0
		out[r.NodeID] = r
	}
	return out, rows.Err()
}
