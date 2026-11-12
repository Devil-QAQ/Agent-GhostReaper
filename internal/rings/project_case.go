package rings

// P4-36 判断卷宗投影(claim_cases):发现列表摘要 + 发现详情/报告装配的
// 按条数据面。语义与既有事件流读侧逐一对齐——
//
//	列表摘要(latest-wins 小字段)← handleClaimsList 的事件扫描语义:
//	  falsify/verdict 最新覆盖、repro 定稿、复测/重放最新、复核标记;
//	timeline(时间线索引) ← report.claimCases 的全量扫描语义:
//	  created/state 自节点事件;falsify/verdict/reopen/retest/replay 带
//	  原始负载(repro 只存级别摘要——巨记录负载不进投影,过程明细由
//	  证据包导出按 rev 定向读事件);
//	alternatives/alt_killed ← arbiter 冻结清单与战役内排除(reopen 清战役);
//	evidence  ← ClaimEvidence 首绑次序 + EvidenceVersions 计数;
//	origin    ← claimOriginOf 的发现单位锚(含会话锚回退查 unit_sessions);
//	timing    ← claimTimingOf 的战役步骤状态段游标(reopen 清战役)。
//
// 写入按"读-改-写 + 小语句按列更新"(单写者事务内无竞态;每类事件只
// 触碰涉及的列)。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CaseTimelineEntry 时间线索引行(timeline 数组元素)。K 为条目种类
// (created|state|falsify|verdict|reopen|repro|retest|replay|review|suspend);
// P 为负载——记录类为事件原始 JSON,repro 为 {"severity":…} 摘要,
// created/state 为 {"status":…} 小摘要。
type CaseTimelineEntry struct {
	Rev int64           `json:"rev"`
	At  string          `json:"at"` // RFC3339Nano
	K   string          `json:"k"`
	P   json.RawMessage `json:"p,omitempty"`
}

// ReproDigest 最新定稿的投影形态:小字段 + 步骤(不含 Op——面板/报告
// 正文不显,完整参数在事件流与证据包;不含 Variants/Process 大数组,
// 计数在案,过程明细由证据包导出按 rev 定向读事件)。Rounds 为定稿
// 冻结的反证时间线人话三段(P4-56 ⑤;旧定稿为空,读侧退机器层)。
type ReproDigest struct {
	Rev                 int64               `json:"rev"`
	Severity            string              `json:"severity,omitempty"`
	Narrative           string              `json:"narrative,omitempty"`
	NarrativeUnpolished bool                `json:"narrative_unpolished,omitempty"`
	Rounds              []FalsifyRoundHuman `json:"rounds,omitempty"`
	Steps               []ReproStep         `json:"steps,omitempty"`
	NoReproReason       string              `json:"no_repro_reason,omitempty"`
	ProcessCount        int                 `json:"process_count,omitempty"`
	VariantsCount       int                 `json:"variants_count,omitempty"`
}

// SummaryDigest 定稿校对后的声明摘要(P4-59 ①,问题清单 #80①③):
// statement 存在性描述 / impact 影响面 / facts 链上事实描述,报告级
// 文风锚生成,随 repro 事件同批冻结;空字段 = 未生成,读侧退节点原文。
type SummaryDigest struct {
	Rev       int64           `json:"rev,omitempty"`
	Statement string          `json:"statement,omitempty"`
	Impact    string          `json:"impact,omitempty"`
	Facts     []FactStatement `json:"facts,omitempty"`
}

// CaseEvidenceBind 绑定流量证据条目(首绑次序)。
type CaseEvidenceBind struct {
	ExchangeID string `json:"id"`
	Rev        int64  `json:"rev"` // 首绑版本号
}

// TimingStepDigest 战役内单步骤的状态游标。
type TimingStepDigest struct {
	State string `json:"st"`
	At    string `json:"at"`
}

// TimingDigest 耗时拆解的增量态(读侧换算为呈现视图)。
type TimingDigest struct {
	LandedAt   string                      `json:"landed_at,omitempty"`
	TerminalAt string                      `json:"terminal_at,omitempty"`
	QueueSec   float64                     `json:"q,omitempty"`
	ExecSec    float64                     `json:"e,omitempty"`
	Steps      map[string]TimingStepDigest `json:"s,omitempty"`
}

// claimCaseRow claim_cases 行的可变视图(读-改-写)。
type claimCaseRow struct {
	claimID              string
	falsifyStep          string
	verifier             string
	pendingReason        string
	verdictRev           int64
	severity             string
	reproSteps           int
	retestVerdict        string
	retestRound          int
	retestAt             string
	replayVerdict        string
	replayRound          int
	replayAt             string
	hasEvidence          bool
	reportExcluded       bool
	reportExcludedReason string
	evidenceBinds        int
	reproEvents          int
	timeline             []CaseTimelineEntry
	alternatives         []Alternative
	altKilled            map[string]bool
	repro                *ReproDigest
	evidence             []CaseEvidenceBind
	discoverer           string
	discoverStep         string
	discoverSess         string
	timing               TimingDigest
	summary              *SummaryDigest
	// roundHumans 逐轮人话(P4-64 ①)/retestNotes 复测备注校对稿(P4-64 ⑦):
	// 显示层包装列,按轮 upsert(轮号全局续号),存储 JSON 键为轮号字符串。
	roundHumans map[int]ClaimRoundHuman
	retestNotes map[int]string
	// changed* 脏列标记(save 只写触碰过的列组)。
	changedDigest   bool
	changedTimeline bool
	changedAlts     bool
	changedRepro    bool
	changedEvidence bool
	changedOrigin   bool
	changedTiming   bool
	changedSummary  bool
	changedRounds   bool
	changedNotes    bool
}

// loadClaimCase 读行;不存在时建默认行(幂等)。
func loadClaimCase(tx *sql.Tx, taskID, claimID string) (*claimCaseRow, error) {
	row := &claimCaseRow{claimID: claimID, altKilled: map[string]bool{}}
	var hasEvidence, reportExcluded int
	var rawTimeline, rawAlts, rawKilled, rawRepro, rawEvidence, rawTiming, rawSummary string
	var rawRounds, rawNotes string
	err := tx.QueryRow(
		`SELECT falsify_step, verifier, pending_reason, verdict_rev, severity, repro_steps,
		   retest_verdict, retest_round, retest_at, replay_verdict, replay_round, replay_at,
		   has_evidence, report_excluded, report_excluded_reason, evidence_binds, repro_events,
		   timeline, alternatives, alt_killed, repro, evidence, discoverer, discover_step,
		   discover_session, timing, summary, round_humans, retest_notes
		 FROM claim_cases WHERE task_id = ? AND claim_id = ?`, taskID, claimID,
	).Scan(&row.falsifyStep, &row.verifier, &row.pendingReason, &row.verdictRev, &row.severity,
		&row.reproSteps, &row.retestVerdict, &row.retestRound, &row.retestAt,
		&row.replayVerdict, &row.replayRound, &row.replayAt,
		&hasEvidence, &reportExcluded, &row.reportExcludedReason,
		&row.evidenceBinds, &row.reproEvents,
		&rawTimeline, &rawAlts, &rawKilled, &rawRepro, &rawEvidence,
		&row.discoverer, &row.discoverStep, &row.discoverSess, &rawTiming, &rawSummary,
		&rawRounds, &rawNotes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO claim_cases (task_id, claim_id) VALUES (?, ?)`,
			taskID, claimID,
		); err != nil {
			return nil, fmt.Errorf("rings: 建判断卷宗行: %w", err)
		}
		return row, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rings: 读判断卷宗: %w", err)
	}
	row.hasEvidence, row.reportExcluded = hasEvidence != 0, reportExcluded != 0
	if rawTimeline != "" {
		_ = json.Unmarshal([]byte(rawTimeline), &row.timeline)
	}
	if rawAlts != "" {
		_ = json.Unmarshal([]byte(rawAlts), &row.alternatives)
	}
	if rawKilled != "" && rawKilled != "[]" {
		var ids []string
		if json.Unmarshal([]byte(rawKilled), &ids) == nil {
			for _, id := range ids {
				row.altKilled[id] = true
			}
		}
	}
	if rawRepro != "" {
		row.repro = &ReproDigest{}
		if json.Unmarshal([]byte(rawRepro), row.repro) != nil {
			row.repro = nil
		}
	}
	if rawEvidence != "" {
		_ = json.Unmarshal([]byte(rawEvidence), &row.evidence)
	}
	if rawTiming != "" {
		_ = json.Unmarshal([]byte(rawTiming), &row.timing)
	}
	if row.timing.Steps == nil {
		row.timing.Steps = map[string]TimingStepDigest{}
	}
	if rawSummary != "" {
		row.summary = &SummaryDigest{}
		if json.Unmarshal([]byte(rawSummary), row.summary) != nil {
			row.summary = nil
		}
	}
	if rawRounds != "" {
		var m map[string]ClaimRoundHuman
		if json.Unmarshal([]byte(rawRounds), &m) == nil {
			row.roundHumans = map[int]ClaimRoundHuman{}
			for k, v := range m {
				if n, err := strconv.Atoi(k); err == nil {
					row.roundHumans[n] = v
				}
			}
		}
	}
	if rawNotes != "" {
		var m map[string]string
		if json.Unmarshal([]byte(rawNotes), &m) == nil {
			row.retestNotes = map[int]string{}
			for k, v := range m {
				if n, err := strconv.Atoi(k); err == nil {
					row.retestNotes[n] = v
				}
			}
		}
	}
	return row, nil
}

// save 脏列组写回(每条小语句只触碰本次事件涉及的列)。
func (r *claimCaseRow) save(tx *sql.Tx, taskID string) error {
	if r.changedDigest {
		hasEvidence, excluded := 0, 0
		if r.hasEvidence {
			hasEvidence = 1
		}
		if r.reportExcluded {
			excluded = 1
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET falsify_step = ?, verifier = ?, pending_reason = ?,
			   verdict_rev = ?, severity = ?, repro_steps = ?, retest_verdict = ?,
			   retest_round = ?, retest_at = ?, replay_verdict = ?, replay_round = ?,
			   replay_at = ?, has_evidence = ?, report_excluded = ?,
			   report_excluded_reason = ?, evidence_binds = ?, repro_events = ?
			 WHERE task_id = ? AND claim_id = ?`,
			r.falsifyStep, r.verifier, r.pendingReason, r.verdictRev, r.severity,
			r.reproSteps, r.retestVerdict, r.retestRound, r.retestAt,
			r.replayVerdict, r.replayRound, r.replayAt, hasEvidence, excluded,
			r.reportExcludedReason, r.evidenceBinds, r.reproEvents, taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入判断摘要: %w", err)
		}
	}
	if r.changedTimeline {
		b, err := json.Marshal(r.timeline)
		if err != nil {
			return fmt.Errorf("rings: 序列化时间线: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET timeline = ? WHERE task_id = ? AND claim_id = ?`,
			string(b), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入时间线: %w", err)
		}
	}
	if r.changedAlts {
		alts, err := json.Marshal(r.alternatives)
		if err != nil {
			return fmt.Errorf("rings: 序列化冻结解释: %w", err)
		}
		killed := make([]string, 0, len(r.altKilled))
		for id := range r.altKilled {
			killed = append(killed, id)
		}
		killedJSON, err := json.Marshal(killed)
		if err != nil {
			return fmt.Errorf("rings: 序列化解释排除: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET alternatives = ?, alt_killed = ? WHERE task_id = ? AND claim_id = ?`,
			string(alts), string(killedJSON), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入冻结解释: %w", err)
		}
	}
	if r.changedRepro {
		repro := []byte("")
		if r.repro != nil {
			var err error
			if repro, err = json.Marshal(r.repro); err != nil {
				return fmt.Errorf("rings: 序列化定稿: %w", err)
			}
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET repro = ? WHERE task_id = ? AND claim_id = ?`,
			string(repro), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入定稿: %w", err)
		}
	}
	if r.changedEvidence {
		b, err := json.Marshal(r.evidence)
		if err != nil {
			return fmt.Errorf("rings: 序列化证据绑定: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET evidence = ?, evidence_binds = ?, has_evidence = ? WHERE task_id = ? AND claim_id = ?`,
			string(b), r.evidenceBinds, boolNum(r.hasEvidence), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入证据绑定: %w", err)
		}
	}
	if r.changedOrigin {
		if _, err := tx.Exec(
			`UPDATE claim_cases SET discoverer = ?, discover_step = ?, discover_session = ? WHERE task_id = ? AND claim_id = ?`,
			r.discoverer, r.discoverStep, r.discoverSess, taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入溯源: %w", err)
		}
	}
	if r.changedTiming {
		b, err := json.Marshal(r.timing)
		if err != nil {
			return fmt.Errorf("rings: 序列化耗时态: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET timing = ? WHERE task_id = ? AND claim_id = ?`,
			string(b), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入耗时态: %w", err)
		}
	}
	if r.changedSummary {
		summary := []byte("")
		if r.summary != nil {
			var err error
			if summary, err = json.Marshal(r.summary); err != nil {
				return fmt.Errorf("rings: 序列化判断摘要: %w", err)
			}
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET summary = ? WHERE task_id = ? AND claim_id = ?`,
			string(summary), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入判断摘要: %w", err)
		}
	}
	if r.changedRounds {
		b, err := marshalRoundKeyed(r.roundHumans)
		if err != nil {
			return fmt.Errorf("rings: 序列化逐轮人话: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET round_humans = ? WHERE task_id = ? AND claim_id = ?`,
			string(b), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入逐轮人话: %w", err)
		}
	}
	if r.changedNotes {
		b, err := marshalRoundKeyed(r.retestNotes)
		if err != nil {
			return fmt.Errorf("rings: 序列化复测备注校对稿: %w", err)
		}
		if _, err := tx.Exec(
			`UPDATE claim_cases SET retest_notes = ? WHERE task_id = ? AND claim_id = ?`,
			string(b), taskID, r.claimID,
		); err != nil {
			return fmt.Errorf("rings: 投影写入复测备注校对稿: %w", err)
		}
	}
	return nil
}

// marshalRoundKeyed 轮号键 JSON(map 键升序,重放确定性:同输入同输出)。
func marshalRoundKeyed[V any](m map[int]V) ([]byte, error) {
	if len(m) == 0 {
		return []byte(""), nil
	}
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		v, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		kb, _ := json.Marshal(strconv.Itoa(k))
		b.Write(kb)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// appendTimeline 时间线追一条。
func (r *claimCaseRow) appendTimeline(rev int64, at time.Time, k string, p json.RawMessage) {
	r.timeline = append(r.timeline, CaseTimelineEntry{Rev: rev, At: fmtTimeUTC(at), K: k, P: p})
	r.changedTimeline = true
}

// applyClaimNodeAdded node.added(testclaim)→ 卷宗初始化:溯源锚(detail 的
// 转录锚优先,缺省回退查该单位×步骤最近的 session_started——与
// claimOriginOf 的事件序语义一致)、冻结解释清单、时间线 created、
// 耗时落地时刻。
func applyClaimNodeAdded(tx *sql.Tx, taskID string, rev int64, p NodeAdded, occurredAt time.Time) error {
	row, err := loadClaimCase(tx, taskID, p.NodeID)
	if err != nil {
		return err
	}
	row.discoverer, _ = p.Detail["unit_id"].(string)
	row.discoverStep, _ = p.Detail["step_id"].(string)
	row.discoverSess, _ = p.Detail["transcript_id"].(string)
	if row.discoverSess == "" && row.discoverer != "" && row.discoverStep != "" {
		sid, serr := lookupSessionAnchor(tx, taskID, row.discoverer, row.discoverStep, rev)
		if serr != nil {
			return serr
		}
		row.discoverSess = sid
	}
	row.alternatives = ParseAlternatives(p.Detail["alternatives"])
	row.appendTimeline(rev, occurredAt, "created", nil)
	row.timing.LandedAt = fmtTimeUTC(occurredAt)
	row.changedAlts, row.changedOrigin, row.changedTiming = true, true, true
	return row.save(tx, taskID)
}

// lookupSessionAnchor 该单位×步骤在 rev 之前最近一次 session_started 的
// 转录锚(事件序语义:发现/反证记录总在其单位的会话开始之后)。
func lookupSessionAnchor(tx *sql.Tx, taskID, unitID, stepID string, rev int64) (string, error) {
	var sid string
	err := tx.QueryRow(
		`SELECT transcript_id FROM unit_sessions
		 WHERE task_id = ? AND unit_id = ? AND step_id = ? AND start_rev < ?
		 ORDER BY start_rev DESC LIMIT 1`,
		taskID, unitID, stepID, rev,
	).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("rings: 查会话锚: %w", err)
	}
	return sid, nil
}

// ParseAlternatives 创建事件 detail 解冻结解释清单(ID 缺失的条目跳过;
// arbiter 与投影共用本实现)。
func ParseAlternatives(raw any) []Alternative {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []Alternative
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		label, _ := m["label"].(string)
		if id == "" {
			continue
		}
		out = append(out, Alternative{ID: id, Label: label})
	}
	return out
}

// applyStepNodeAdded node.added(step 带归属判断)→ 耗时态战役登记
// (初始 dispatched,与 claimTimingOf 的事件回放同口径)。
func applyStepNodeAdded(tx *sql.Tx, taskID string, p NodeAdded, occurredAt time.Time) error {
	claimID, _ := p.Detail["testclaim_id"].(string)
	if claimID == "" {
		return nil
	}
	row, err := loadClaimCase(tx, taskID, claimID)
	if err != nil {
		return err
	}
	if row.timing.Steps == nil {
		row.timing.Steps = map[string]TimingStepDigest{}
	}
	row.timing.Steps[p.NodeID] = TimingStepDigest{State: "dispatched", At: fmtTimeUTC(occurredAt)}
	row.changedTiming = true
	return row.save(tx, taskID)
}

// applyCaseStateChanged node.state_changed → 判断侧时间线 state 与耗时
// 终态;步骤侧(归属判断)按状态段累加排队/执行(重开清战役,旧战役
// 步骤不在游标表内自然忽略——与 claimTimingOf 的 inCampaign 口径一致)。
func applyCaseStateChanged(tx *sql.Tx, taskID string, rev int64, p NodeStateChanged, occurredAt time.Time) error {
	nt, err := nodeTypeOf(tx, taskID, p.NodeID)
	if err != nil {
		return nil // 节点不在投影(异常形态),与读侧容错同口径跳过
	}
	switch nt {
	case NodeTestClaim:
		row, err := loadClaimCase(tx, taskID, p.NodeID)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"status": p.Status})
		row.appendTimeline(rev, occurredAt, "state", payload)
		if p.Status == TestClaimConfirmed || p.Status == TestClaimPending {
			row.timing.TerminalAt = fmtTimeUTC(occurredAt)
			row.changedTiming = true
		}
		return row.save(tx, taskID)
	case NodeStep:
		var detail string
		if err := tx.QueryRow(
			`SELECT detail FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, p.NodeID,
		).Scan(&detail); err != nil {
			return nil
		}
		var d struct {
			TestClaimID string `json:"testclaim_id"`
		}
		if json.Unmarshal([]byte(detail), &d) != nil || d.TestClaimID == "" {
			return nil
		}
		row, err := loadClaimCase(tx, taskID, d.TestClaimID)
		if err != nil {
			return err
		}
		cur, ok := row.timing.Steps[p.NodeID]
		if !ok {
			return nil // 非当前战役步骤(重开前旧战役),与读侧同口径忽略
		}
		from, fromAt := cur.State, parseTimeCol(cur.At)
		seg := occurredAt.Sub(fromAt).Seconds()
		switch {
		case from == "dispatched" && p.Status == "running":
			row.timing.QueueSec += seg
		case from == "running" && p.Status != "running":
			row.timing.ExecSec += seg
		}
		row.timing.Steps[p.NodeID] = TimingStepDigest{State: p.Status, At: fmtTimeUTC(occurredAt)}
		row.changedTiming = true
		return row.save(tx, taskID)
	}
	return nil
}

// applyClaimRecord testclaim.record → 卷宗(摘要 latest-wins + 时间线 +
// 战役状态;payload 为事件原始负载,时间线索引直取原文)。
func applyClaimRecord(tx *sql.Tx, taskID string, rev int64, p TestClaimRecord, payload json.RawMessage, occurredAt time.Time) error {
	row, err := loadClaimCase(tx, taskID, p.ClaimID)
	if err != nil {
		return err
	}
	switch p.Type {
	case TestClaimRecFalsify:
		row.falsifyStep, row.verifier = p.StepID, p.UnitID
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "falsify", payload)
		for _, a := range p.Alternatives {
			if a.Refuted {
				row.altKilled[a.ID] = true
				row.changedAlts = true
			}
		}
	case TestClaimRecVerdict:
		row.pendingReason, row.verdictRev = p.PendingReason, rev
		if p.StepID != "" {
			row.falsifyStep = p.StepID
		}
		if p.UnitID != "" {
			row.verifier = p.UnitID
		}
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "verdict", payload)
	case TestClaimRecReopen:
		row.appendTimeline(rev, occurredAt, "reopen", payload)
		row.altKilled = map[string]bool{}
		row.timing.QueueSec, row.timing.ExecSec = 0, 0
		row.timing.Steps = map[string]TimingStepDigest{}
		row.changedAlts, row.changedTiming = true, true
	case TestClaimRecRepro:
		row.severity, row.reproSteps = p.Severity, len(p.Steps)
		row.reproEvents++
		row.changedDigest = true
		steps := make([]ReproStep, len(p.Steps))
		for i, st := range p.Steps {
			st.Op = "" // 面板/报告正文不显,完整参数在事件流与证据包
			steps[i] = st
		}
		row.repro = &ReproDigest{
			Rev: rev, Severity: p.Severity, Narrative: p.Narrative,
			NarrativeUnpolished: p.NarrativeUnpolished, Rounds: p.Rounds, Steps: steps,
			NoReproReason: p.NoReproReason,
			ProcessCount:  len(p.Process), VariantsCount: len(p.Variants),
		}
		row.changedRepro = true
		// 声明摘要(P4-59 ①):字段全空 = 本次定稿未生成校对摘要,摘要列
		// 一并清空(取代语义——重开后再确认的新定稿为准,不残留旧摘要)。
		if p.Statement != "" || p.Impact != "" || len(p.Facts) > 0 {
			row.summary = &SummaryDigest{Rev: rev, Statement: p.Statement, Impact: p.Impact, Facts: p.Facts}
		} else {
			row.summary = nil
		}
		row.changedSummary = true
		summary, _ := json.Marshal(map[string]string{"severity": p.Severity})
		row.appendTimeline(rev, occurredAt, "repro", summary)
	case TestClaimRecEvidence:
		row.evidenceBinds++
		if len(p.Exchanges) > 0 {
			row.hasEvidence = true
		}
		bound := map[string]bool{}
		for _, e := range row.evidence {
			bound[e.ExchangeID] = true
		}
		for _, ex := range p.Exchanges {
			if !bound[ex] {
				bound[ex] = true
				row.evidence = append(row.evidence, CaseEvidenceBind{ExchangeID: ex, Rev: rev})
			}
		}
		row.changedEvidence = true
	case TestClaimRecRetest:
		row.retestVerdict, row.retestRound = p.RetestVerdict, p.Round
		row.retestAt = fmtTimeUTC(occurredAt)
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "retest", payload)
	case TestClaimRecReplay:
		row.replayVerdict, row.replayRound = p.ReplayVerdict, p.Round
		row.replayAt = fmtTimeUTC(occurredAt)
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "replay", payload)
	case TestClaimRecReportReview:
		row.reportExcluded, row.reportExcludedReason = true, p.Reason
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "review", payload)
	case TestClaimRecSuspend:
		// 操作者取消挂起(P4-54):挂起理由 latest-wins 进 pending_reason
		// (面板/报告读同一口径),时间线留 suspend 条目;状态迁移由
		// node.state_changed(testing→pending)另行投影。
		row.pendingReason = p.Reason
		row.verdictRev = rev
		row.changedDigest = true
		row.appendTimeline(rev, occurredAt, "suspend", payload)
	case TestClaimRecDiscard:
		// 程序确定性废弃(P4-55 审核迭代):时间线留 discard 条目(理由
		// 随行可溯);pending_reason 不动——废弃是终态不是搁置,理由在
		// 本条目与 node.state_changed Note。
		row.appendTimeline(rev, occurredAt, "discard", payload)
	}
	return row.save(tx, taskID)
}

// applyClaimRoundHuman claim.round_human → 卷宗逐轮人话(P4-64 ①):
// 按轮 upsert(轮号全局续号,重开后新轮次增量补);机器 falsify 记录
// 原文一字不动。
func applyClaimRoundHuman(tx *sql.Tx, taskID string, rev int64, p ClaimRoundHuman) error {
	if p.ClaimID == "" || p.Round <= 0 ||
		(p.Doubt == "" && p.How == "" && p.Conclusion == "") {
		return fmt.Errorf("rings: claim.round_human 缺判断、轮次不合法或三段全空(不落空事件)")
	}
	row, err := loadClaimCase(tx, taskID, p.ClaimID)
	if err != nil {
		return err
	}
	if row.roundHumans == nil {
		row.roundHumans = map[int]ClaimRoundHuman{}
	}
	row.roundHumans[p.Round] = ClaimRoundHuman{
		ClaimID: p.ClaimID, Round: p.Round,
		Doubt: p.Doubt, How: p.How, Conclusion: p.Conclusion, Unpolished: p.Unpolished,
	}
	row.changedRounds = true
	return row.save(tx, taskID)
}

// applyClaimSummaryPolish claim.summary → 摘要列(P4-64 ②:立账即生成
// 的声明摘要)。与 repro 事件的 summary 写入同列 latest-wins——立账在
// 先、定稿在后(账本次序),重定稿覆盖立账稿(取代语义既有);全空
// 字段不入账(生成侧控制,不落空事件)。
func applyClaimSummaryPolish(tx *sql.Tx, taskID string, rev int64, p ClaimSummaryPolish) error {
	if p.ClaimID == "" {
		return fmt.Errorf("rings: claim.summary 缺判断")
	}
	if p.Statement == "" && p.Impact == "" && len(p.Facts) == 0 {
		return fmt.Errorf("rings: claim.summary 字段全空(不落空事件)")
	}
	row, err := loadClaimCase(tx, taskID, p.ClaimID)
	if err != nil {
		return err
	}
	row.summary = &SummaryDigest{Rev: rev, Statement: p.Statement, Impact: p.Impact, Facts: p.Facts}
	row.changedSummary = true
	return row.save(tx, taskID)
}

// applyNodePolish fact.text / hypo.text → node_polish(P4-64 ③④⑥⑧):
// 节点显示层包装 latest-wins(每类节点只来自自己的生成批次,整行覆盖
// 无半覆盖);节点原文一字不动。unpolished 为标题未过闸的如实标注。
func applyNodePolish(tx *sql.Tx, taskID string, rev int64, nodeID, title, statement, basis, reason string, unpolished bool) error {
	if nodeID == "" {
		return fmt.Errorf("rings: 节点文本包装缺节点")
	}
	if title == "" && statement == "" && basis == "" && reason == "" {
		return fmt.Errorf("rings: 节点文本包装字段全空(不落空事件)")
	}
	if _, err := tx.Exec(
		`INSERT INTO node_polish (task_id, node_id, title, statement, basis, reason, unpolished, rev)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id, node_id) DO UPDATE SET
		   title = excluded.title, statement = excluded.statement,
		   basis = excluded.basis, reason = excluded.reason,
		   unpolished = excluded.unpolished, rev = excluded.rev`,
		taskID, nodeID, title, statement, basis, reason, boolNum(unpolished), rev,
	); err != nil {
		return fmt.Errorf("rings: 投影写入节点文本包装: %w", err)
	}
	return nil
}

// applyRetestNotePolish claim.retest_note → 复测备注校对稿(P4-64 ⑦):
// 按轮 upsert;机器 retest 记录 Note 原文一字不动。
func applyRetestNotePolish(tx *sql.Tx, taskID string, rev int64, p RetestNotePolish) error {
	if p.ClaimID == "" || p.Round <= 0 || p.Note == "" {
		return fmt.Errorf("rings: claim.retest_note 缺判断/轮次/备注")
	}
	row, err := loadClaimCase(tx, taskID, p.ClaimID)
	if err != nil {
		return err
	}
	if row.retestNotes == nil {
		row.retestNotes = map[int]string{}
	}
	row.retestNotes[p.Round] = p.Note
	row.changedNotes = true
	return row.save(tx, taskID)
}
