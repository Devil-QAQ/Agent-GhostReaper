package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ghostreaper/internal/rings"
)

// TestClaim 确认裁决(P1-08 基础 + P2-02 完整版,第三章 §5/§6):Goal
// 完成判定与 TestClaim 确认是同一机制的两次应用——声明 + 证据 + 冻结
// 判定标准 → 裁决。本文件落地声明侧;四点最小契约逐点对应:
//
//	① 判定标准受限结构:ClaimStandard 只有 kind/require_impact/max_rounds
//	   三个字段,ParseClaimStandard 对结构外的一切拒收。P2-02 判定标准
//	   为 "完整反证"(full_falsify):竞争性替代解释逐条排除(跨轮单调
//	   累积)+ 存在性立住 + 末轮影响面证据充分 + 各轮验证者独立,全部
//	   满足才判 Confirmed;解释未全灭且轮次未尽 → 待下轮反证
//	   (awaiting_round);P1-08 的 single_round_falsify 保留兼容旧账本。
//	② 创建时冻结:标准与竞争解释清单均存于 TestClaim 创建事件负载
//	   (detail.standard / detail.alternatives),账本无"改 detail"的
//	   事件种类 ⇒ 物理冻结(与 Goal 谓词同一论证)。
//	③ 只读账本结构化证据:求值只读创建事件、节点投影、testclaim.record
//	   事件流,不读任何外部输入、零写账本;重开后旧战役证据整体失效
//	   (campaign 边界 = 最近一次 reopen 事件)。
//	④ LLM 陈述不构成证据:求值只吃 falsify_result 的布尔、逐条解释
//	   排除结论(AltResult.Refuted)与影响面定级词汇(经受控入口落账),
//	   Note/EvidenceHead 等自然语言文本不进求值——发现者自己的任何
//	   "已确认"文本更无通道。

var (
	// ErrClaimNotFound 声明节点在事件流中无创建事件。
	ErrClaimNotFound = errors.New("arbiter: testclaim not found")
	// ErrClaimNotTesting 声明不在 testing 状态(未派反证、已终态或待下轮),
	// 当前轮不可(重复)求值。
	ErrClaimNotTesting = errors.New("arbiter: testclaim not in testing state")
	// ErrNoClaimStandard 声明创建时未冻结判定标准——无标准的声明不由
	// 机器终审(收割侧入口本应拒收其进入反证)。
	ErrNoClaimStandard = errors.New("arbiter: testclaim has no frozen standard")
	// ErrClaimNotReady 当前反证轮尚未结束(反证 Step 未到终态),还不可裁决。
	ErrClaimNotReady = errors.New("arbiter: falsify round not finished")
)

// 判定标准 kind 值域(single_round_falsify 为 P1-08 兼容保留)。
const (
	ClaimStandardSingleRoundFalsify = "single_round_falsify"
	ClaimStandardFullFalsify        = "full_falsify" // P2-02 完整反证(多轮 + 竞争解释全灭)
)

// DefaultMaxFalsifyRounds 完整反证的每战役轮数上限缺省值。
const DefaultMaxFalsifyRounds = 3

var claimStandardKinds = map[string]bool{
	ClaimStandardSingleRoundFalsify: true,
	ClaimStandardFullFalsify:        true,
}

// ClaimStandard TestClaim 判定标准的受限结构。
type ClaimStandard struct {
	Kind string `json:"kind"`
	// RequireImpact 影响面证据是否为确认必需(第三章 §5:报告级别跟
	// 影响面证据走——缺省 true,声明的影响面无证据支撑就不得确认)。
	RequireImpact bool `json:"require_impact"`
	// MaxRounds full_falsify 专属:单一战役(两次重开之间)的反证轮数
	// 上限;到限仍未全灭即诚实记待确认(不得无限反证刷轮)。0 → 默认 3。
	MaxRounds int `json:"max_rounds,omitempty"`
}

// DefaultClaimStandard 默认(也是 P2-02 起唯一新发)判定标准:完整反证 +
// 影响面证据必需 + 每战役至多 3 轮。由虫群受控入口在创建声明时写入
// detail.standard(冻结)。
func DefaultClaimStandard() map[string]any {
	return map[string]any{
		"kind": ClaimStandardFullFalsify, "require_impact": true,
		"max_rounds": DefaultMaxFalsifyRounds,
	}
}

// ParseClaimStandard 解析并校验受限结构判定标准(契约①的声明侧入口)。
// require_impact 缺省规范化为 true(从严:影响面声明必须有证据);
// full_falsify 的 max_rounds 缺省规范化为 3、负数拒收。
func ParseClaimStandard(raw map[string]any) (*ClaimStandard, error) {
	if raw == nil {
		return nil, fmt.Errorf("arbiter: 判定标准不能为空")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 判定标准序列化: %w", err)
	}
	var st ClaimStandard
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("arbiter: 判定标准不是合法的受限结构: %w", err)
	}
	if !claimStandardKinds[st.Kind] {
		return nil, fmt.Errorf("arbiter: 未知判定标准种类 %q(允许: %s/%s)",
			st.Kind, ClaimStandardFullFalsify, ClaimStandardSingleRoundFalsify)
	}
	if _, ok := raw["require_impact"]; !ok {
		st.RequireImpact = true
	}
	if st.Kind == ClaimStandardFullFalsify {
		if _, ok := raw["max_rounds"]; !ok || st.MaxRounds == 0 {
			st.MaxRounds = DefaultMaxFalsifyRounds
		}
	}
	if st.MaxRounds < 0 {
		return nil, fmt.Errorf("arbiter: max_rounds 不得为负: %d", st.MaxRounds)
	}
	return &st, nil
}

// maxRoundsNorm 归一后的轮数上限(single_round 恒 1)。
func (st *ClaimStandard) maxRoundsNorm() int {
	if st.Kind == ClaimStandardSingleRoundFalsify || st.MaxRounds == 0 {
		return 1
	}
	return st.MaxRounds
}

// TestClaim 裁决的叶子条件种类(只进 testclaim.record verdict 负载,
// 与 Goal 谓词的 fact_confirmed/tool_observed 同格式不同值域)。
const (
	// CondFalsifyCompleted 当前反证轮已写回结构化结论(falsify_result 存在)。
	CondFalsifyCompleted = "falsify_completed"
	// CondExistenceNotRefuted 战役内反证未推翻存在性声明。
	CondExistenceNotRefuted = "existence_not_refuted"
	// CondAlternativesKilled 竞争性替代解释全部被排除(P2-02:Required=
	// 冻结清单条数,Actual=战役内经结构化写回排除的条数,跨轮单调累积)。
	CondAlternativesKilled = "alternatives_killed"
	// CondImpactSupported 末轮影响面证据足以支撑所声明的影响。
	CondImpactSupported = "impact_supported"
	// CondVerifierIndependent 各轮反证单位 ≠ 发现单位(R12 发现者不得
	// 自证,机器终审 + 独立派发双重保障的裁决侧核验)。
	CondVerifierIndependent = "verifier_independent"
)

// Step 终态清单(与 swarm 执行器终因迁移一致:done|exhausted|failed|
// aborted|approval_denied)。反证 Step 到终态后当前轮才可裁决;预算耗尽/
// 失败/中止/审批拒绝终止同样诚实进入裁决(结论必然是待确认,理由如实
// 记录)。approval_denied(P2-06b):反证被审批拒绝熔断——被拒单位不得
// 换法重试,该轮无结构化结论。
var stepTerminal = map[string]bool{"done": true, "exhausted": true, "failed": true, "aborted": true, "approval_denied": true}

// ClaimJudgement 一次声明裁决的产物:Verdict 是待落账的裁决记录
// (type=verdict),Status 是声明应迁入的状态(confirmed|pending|
// awaiting_round)。记账与状态迁移由调用方(主宰循环)以 actor=arbiter
// 落账,本函数零写。确认时附带收割定稿素材:Severity(末轮影响面定级,
// 级别跟影响面证据走)与 AliveAlt(advance 时的存活解释清单,供收割
// 派发下一轮)。
type ClaimJudgement struct {
	Verdict  rings.TestClaimRecord
	Status   string              // rings.TestClaimConfirmed | TestClaimPending | TestClaimAwaitingRound
	Severity string              // confirm 时非空可能:末轮 impact_level(发现者无级别通道)
	Round    int                 // 本轮轮次(verdict.Round)
	AliveAlt []rings.Alternative // advance 时:待下一轮反证的存活解释
}

// claimOrigin 声明事件流里冻结/累积的判定锚点。Campaign(战役)= 最近
// 一次 reopen 事件之后的证据跨度:重开即旧证据整体失效,一切从新一轮
// 反证重新累积(结论可反驳的落地形态;历史事件永不删除,R1)。
type claimOrigin struct {
	standard     *ClaimStandard
	discoverer   string              // 发现单位(detail.unit_id)
	alternatives []rings.Alternative // 创建时冻结的竞争解释清单
	campaignRev  int64               // 当前战役起点(最近 reopen 的 revision;0=从未重开)
	rounds       []falsifyRoundInfo  // 战役内反证轮(事件顺序)
}

// falsifyRoundInfo 战役内一轮反证:绑定的反证 Step 与其结构化结论。
type falsifyRoundInfo struct {
	stepID string
	round  int    // step.detail.round(P1-08 旧步骤缺省 0)
	status string // 反证 Step 当前状态
	result *rings.TestClaimRecord
}

// JudgeClaim TestClaim 确认判定(纯函数,零 LLM、零外部输入、零写)。
// 只对 testing 状态的声明求值:按创建时冻结的判定标准与竞争解释清单,
// 读当前战役(最近重开之后)账本内结构化证据得出裁决——
//
//	full_falsify:当前轮已写回结论且存在性立住且解释全灭(跨轮累积)
//	  且(标准要求时)末轮影响面证据充分且各轮验证者独立 → Confirmed;
//	  解释未全灭且战役轮次未到上限 → 待下轮反证(awaiting_round);
//	  其余(含轮次耗尽、影响面不足、R12 违例、存在性被推翻、轮未写回)
//	  → 待确认(R4 机器搁置理由)。
//	single_round_falsify(P1-08 兼容):单轮四条件,语义不变。
//
// 当前轮反证 Step 未到终态时返回 ErrClaimNotReady(调用方留待下轮)。
func JudgeClaim(ctx context.Context, store *rings.Store, taskID, claimID string) (*ClaimJudgement, error) {
	origin, err := loadClaimOrigin(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	node, err := store.Node(ctx, taskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 查声明 %s: %w", claimID, err)
	}
	if node.Type != rings.NodeTestClaim {
		return nil, fmt.Errorf("%w: task %s node %s 是 %s", ErrClaimNotFound, taskID, claimID, node.Type)
	}
	if node.Status != rings.TestClaimTesting {
		return nil, fmt.Errorf("%w: %s 当前 %s", ErrClaimNotTesting, claimID, node.Status)
	}
	// 当前轮 = 战役内轮次号最大(并列取最晚创建)的反证 Step。
	if len(origin.rounds) == 0 {
		return nil, fmt.Errorf("%w: 声明 %s 战役内无反证步骤", ErrClaimNotReady, claimID)
	}
	cur := &origin.rounds[len(origin.rounds)-1]
	for i := range origin.rounds {
		if origin.rounds[i].round >= cur.round {
			cur = &origin.rounds[i]
		}
	}
	if !stepTerminal[cur.status] {
		return nil, fmt.Errorf("%w: 反证 Step %s 状态 %s", ErrClaimNotReady, cur.stepID, cur.status)
	}

	rev, err := store.CurrentRevision(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 查图版本: %w", err)
	}

	if origin.standard.Kind == ClaimStandardSingleRoundFalsify {
		return judgeSingleRound(origin, cur, claimID, rev)
	}
	return judgeFullFalsify(origin, cur, claimID, rev)
}

// judgeSingleRound P1-08 单轮标准(旧账本兼容):四条件,语义与 P1-08
// 完全一致——反证完成 + 存在性未推翻 + 影响面证据充分(标准要求时)+
// 验证者独立,全满足判 Confirmed,否则待确认。
func judgeSingleRound(o *claimOrigin, cur *falsifyRoundInfo, claimID string, rev int64) (*ClaimJudgement, error) {
	res := cur.result
	j := &ClaimJudgement{Round: cur.round}
	v := &j.Verdict
	v.ClaimID, v.Type, v.EvaluatedRev = claimID, rings.TestClaimRecVerdict, rev
	v.StepID = cur.stepID

	hasResult := res != nil
	sat := func(b bool) rings.ArbiterConditionResult {
		actual := 0
		if b {
			actual = 1
		}
		return rings.ArbiterConditionResult{Required: 1, Actual: actual, Satisfied: b}
	}
	completed := hasResult
	independent := hasResult && res.UnitID != "" && res.UnitID != o.discoverer
	notRefuted := hasResult && !res.ExistenceRefuted
	if hasResult {
		v.UnitID = res.UnitID
	}
	v.Conditions = append(v.Conditions,
		func() rings.ArbiterConditionResult { c := sat(completed); c.Kind = CondFalsifyCompleted; return c }(),
		func() rings.ArbiterConditionResult { c := sat(notRefuted); c.Kind = CondExistenceNotRefuted; return c }(),
	)
	if o.standard.RequireImpact {
		impactOK := hasResult && res.ImpactSupported
		v.Conditions = append(v.Conditions, func() rings.ArbiterConditionResult {
			c := sat(impactOK)
			c.Kind = CondImpactSupported
			return c
		}())
	}
	v.Conditions = append(v.Conditions, func() rings.ArbiterConditionResult {
		c := sat(independent)
		c.Kind = CondVerifierIndependent
		return c
	}())
	return finishJudgement(j, o)
}

// judgeFullFalsify P2-02 完整反证标准。证据聚合口径(全部限于当前战役):
//   - 存在性被推翻:任一轮写回 existence_refuted=true(推翻即战役终局);
//   - 解释排除:冻结清单内某 ID 在任一轮被结构化写回 refuted=true
//     (跨轮单调累积——排除了就是排除了,不必每轮重排);
//   - 影响面证据:末轮(current round)的 impact_supported(影响面须在
//     最终立住的状态下成立,早期轮的支撑不抵末轮的否定);
//   - 验证者独立:每一轮写回的单位都 ≠ 发现单位。
func judgeFullFalsify(o *claimOrigin, cur *falsifyRoundInfo, claimID string, rev int64) (*ClaimJudgement, error) {
	j := &ClaimJudgement{Round: cur.round}
	v := &j.Verdict
	v.ClaimID, v.Type, v.EvaluatedRev = claimID, rings.TestClaimRecVerdict, rev
	v.StepID = cur.stepID

	res := cur.result
	completed := res != nil
	existenceRefuted := false
	independent := completed
	killed := map[string]bool{}
	for _, r := range o.rounds {
		if r.result == nil {
			continue
		}
		if r.result.UnitID == "" || r.result.UnitID == o.discoverer {
			independent = false
		}
		if r.result.ExistenceRefuted {
			existenceRefuted = true
		}
		for _, a := range r.result.Alternatives {
			if a.Refuted {
				killed[a.ID] = true
			}
		}
	}
	frozen := map[string]rings.Alternative{}
	for _, alt := range o.alternatives {
		frozen[alt.ID] = alt
	}
	killedInFrozen := 0
	for id := range frozen {
		if killed[id] {
			killedInFrozen++
		}
	}
	var alive []rings.Alternative
	for _, alt := range o.alternatives {
		if !killed[alt.ID] {
			alive = append(alive, alt)
		}
	}
	impactOK := completed && res.ImpactSupported

	sat := func(req, act int) rings.ArbiterConditionResult {
		return rings.ArbiterConditionResult{Required: req, Actual: act, Satisfied: act >= req}
	}
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	v.Conditions = append(v.Conditions,
		rings.ArbiterConditionResult{Kind: CondFalsifyCompleted, Required: 1, Actual: b(completed), Satisfied: completed},
		rings.ArbiterConditionResult{Kind: CondExistenceNotRefuted, Required: 1, Actual: b(!existenceRefuted), Satisfied: !existenceRefuted},
		func() rings.ArbiterConditionResult {
			c := sat(len(o.alternatives), killedInFrozen)
			c.Kind = CondAlternativesKilled
			return c
		}(),
	)
	if o.standard.RequireImpact {
		v.Conditions = append(v.Conditions, rings.ArbiterConditionResult{
			Kind: CondImpactSupported, Required: 1, Actual: b(impactOK), Satisfied: impactOK})
	}
	v.Conditions = append(v.Conditions, rings.ArbiterConditionResult{
		Kind: CondVerifierIndependent, Required: 1, Actual: b(independent), Satisfied: independent})
	if completed {
		v.UnitID = res.UnitID
	}

	// 决策次序:无结论/被推翻/R12 违例 → 待确认;解释全灭且影响面立住
	// → 确认;解释未全灭且轮次未尽 → 待下轮;其余(轮次耗尽/影响面
	// 不足且解释已全灭)→ 待确认。
	roundsUsed := len(o.rounds)
	switch {
	case !completed:
		j.Status = rings.TestClaimPending
		v.Decision = rings.VerdictPending
		v.PendingReason = singleIncompleteReason(o, cur)
	case existenceRefuted:
		j.Status = rings.TestClaimPending
		v.Decision = rings.VerdictPending
		v.PendingReason = "存在性被反证推翻,声明不成立,待复核(重开可再证)"
	case !independent:
		j.Status = rings.TestClaimPending
		v.Decision = rings.VerdictPending
		v.PendingReason = "战役内存在反证单位与发现单位相同(R12 违例),反证结果不被采信"
	case len(alive) == 0 && (!o.standard.RequireImpact || impactOK):
		j.Status = rings.TestClaimConfirmed
		v.Decision = rings.VerdictConfirm
		v.Satisfied = true
		if completed {
			j.Severity = res.ImpactLevel
		}
	case len(alive) > 0 && roundsUsed < o.standard.maxRoundsNorm():
		j.Status = rings.TestClaimAwaitingRound
		v.Decision = rings.VerdictAdvanceRound
		j.AliveAlt = alive
		v.PendingReason = fmt.Sprintf("第 %d 轮反证完成,%d 条竞争性替代解释未排除(%s),待派下一轮反证",
			roundNumber(cur.round), len(alive), altIDs(alive))
	default:
		j.Status = rings.TestClaimPending
		v.Decision = rings.VerdictPending
		var parts []string
		if len(alive) > 0 {
			parts = append(parts, fmt.Sprintf("%d 条竞争性替代解释未能在 %d 轮反证内排除(%s)",
				len(alive), o.standard.maxRoundsNorm(), altIDs(alive)))
		}
		if o.standard.RequireImpact && !impactOK {
			parts = append(parts, "末轮影响面证据不足以支撑所声明的实际影响")
		}
		v.PendingReason = strings.Join(parts, ";")
	}
	return j, nil
}

// finishJudgement 单轮标准的收尾(P1-08 语义):全条件满足判确认,
// 否则待确认并机器拼装搁置理由。
func finishJudgement(j *ClaimJudgement, o *claimOrigin) (*ClaimJudgement, error) {
	v := &j.Verdict
	v.Satisfied = true
	for _, c := range v.Conditions {
		if !c.Satisfied {
			v.Satisfied = false
			break
		}
	}
	if v.Satisfied {
		j.Status = rings.TestClaimConfirmed
		v.Decision = rings.VerdictConfirm
		if res := o.latestResult(); res != nil {
			j.Severity = res.ImpactLevel
		}
		return j, nil
	}
	j.Status = rings.TestClaimPending
	v.Decision = rings.VerdictPending
	v.PendingReason = pendingReason(o, v.Conditions)
	return j, nil
}

// latestResult 战役内最末一条结构化结论(单轮标准用)。
func (o *claimOrigin) latestResult() *rings.TestClaimRecord {
	for i := len(o.rounds) - 1; i >= 0; i-- {
		if o.rounds[i].result != nil {
			return o.rounds[i].result
		}
	}
	return nil
}

// pendingReason 单轮标准的机器搁置理由(R4):从逐条件求值结果确定性
// 生成,非自然语言自由发挥;反证轮没写回结论时理由聚焦"为什么没有结论",
// 其余情况逐条列明未被满足的前提。
func pendingReason(o *claimOrigin, conds []rings.ArbiterConditionResult) string {
	byKind := map[string]bool{}
	for _, c := range conds {
		if !c.Satisfied {
			byKind[c.Kind] = true
		}
	}
	if byKind[CondFalsifyCompleted] {
		return "反证轮未写回结构化结论(" + falsifyIncompleteReason(o.currentStatus()) + "),待重派反证后复核"
	}
	var parts []string
	if byKind[CondVerifierIndependent] {
		parts = append(parts, "反证单位与发现单位相同(R12 违例),反证结果不被采信")
	}
	if byKind[CondExistenceNotRefuted] {
		parts = append(parts, "存在性被反证推翻,声明不成立,待复核")
	}
	if byKind[CondImpactSupported] {
		parts = append(parts, "反证未推翻存在性,但影响面证据不足以支撑所声明的实际影响")
	}
	if len(parts) == 0 {
		parts = append(parts, "判定标准条件未满足")
	}
	return strings.Join(parts, ";")
}

// singleIncompleteReason 完整反证下当前轮无结构化结论的搁置理由。
func singleIncompleteReason(o *claimOrigin, cur *falsifyRoundInfo) string {
	return fmt.Sprintf("第 %d 轮反证未写回结构化结论(%s),待重派反证后复核",
		roundNumber(cur.round), falsifyIncompleteReason(cur.status))
}

// currentStatus 战役内最末一轮反证 Step 的状态(单轮标准理由拼装用)。
func (o *claimOrigin) currentStatus() string {
	if len(o.rounds) == 0 {
		return ""
	}
	return o.rounds[len(o.rounds)-1].status
}

// roundNumber 轮次的展示口径(旧数据 0 显示为 1)。
func roundNumber(r int) int {
	if r <= 0 {
		return 1
	}
	return r
}

// falsifyIncompleteReason 反证轮无结构化结论时,按反证 Step 终态如实说明。
func falsifyIncompleteReason(stepStatus string) string {
	switch stepStatus {
	case "done":
		return "反证步骤完成但未提交结构化结论"
	case "exhausted":
		return "反证轮预算耗尽未完成"
	case "failed":
		return "反证轮执行失败"
	case "aborted":
		return "反证轮被中止"
	case "approval_denied":
		return "反证轮被审批拒绝终止(该单位已熔断)"
	default:
		return "反证步骤状态 " + stepStatus
	}
}

// altIDs 解释清单的 ID 串(定序,机器拼装)。
func altIDs(alts []rings.Alternative) string {
	ids := make([]string, 0, len(alts))
	for _, a := range alts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// AliveAlternatives 当前战役内仍存活的竞争解释(收割派发下一轮反证的
// 目标清单)。纯读账本:冻结清单减去战役内已结构化排除的 ID;重开后
// 战役清空,自然回到全量。单轮标准无解释语义,返回空。
func AliveAlternatives(ctx context.Context, store *rings.Store, taskID, claimID string) ([]rings.Alternative, error) {
	o, err := loadClaimOrigin(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	killed := map[string]bool{}
	for _, r := range o.rounds {
		if r.result == nil {
			continue
		}
		for _, a := range r.result.Alternatives {
			if a.Refuted {
				killed[a.ID] = true
			}
		}
	}
	var alive []rings.Alternative
	for _, alt := range o.alternatives {
		if !killed[alt.ID] {
			alive = append(alive, alt)
		}
	}
	return alive, nil
}

// FrozenAlternatives 声明创建时冻结的竞争解释清单(重开新一轮反证的
// 全量目标:重开即全部复活,重新累积排除)。
func FrozenAlternatives(ctx context.Context, store *rings.Store, taskID, claimID string) ([]rings.Alternative, error) {
	o, err := loadClaimOrigin(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	return o.alternatives, nil
}

// loadClaimOrigin 从事件流取判定的全部锚点:创建事件(判定标准 + 发现
// 单位 + 冻结解释清单)+ 战役(最近 reopen 之后)的反证轮(Step 绑定
// detail.testclaim_id + 逐轮 falsify_result)。Step 的轮次与状态取自
// 节点投影;结论按 StepID 归轮。
func loadClaimOrigin(ctx context.Context, store *rings.Store, taskID, claimID string) (*claimOrigin, error) {
	if claimID == "" {
		return nil, fmt.Errorf("arbiter: claim id 为空")
	}
	o := &claimOrigin{}
	var created bool
	results := map[string][]*rings.TestClaimRecord{} // stepID → 战役内结论(按事件序)
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 读事件流: %w", err)
	}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return nil, fmt.Errorf("arbiter: 解码 node.added: %w", err)
			}
			if p.NodeID != claimID || p.NodeType != rings.NodeTestClaim {
				continue
			}
			created = true
			o.discoverer, _ = p.Detail["unit_id"].(string)
			raw, _ := p.Detail["standard"].(map[string]any)
			if raw == nil {
				return nil, fmt.Errorf("%w: claim %s", ErrNoClaimStandard, claimID)
			}
			st, err := ParseClaimStandard(raw)
			if err != nil {
				return nil, fmt.Errorf("arbiter: 声明 %s 冻结标准非法: %w", claimID, err)
			}
			o.standard = st
			o.alternatives = parseAlternatives(p.Detail["alternatives"])
		case rings.EventTestClaimRecord:
			var r rings.TestClaimRecord
			if err := json.Unmarshal(ev.Payload, &r); err != nil {
				return nil, fmt.Errorf("arbiter: 解码 testclaim.record: %w", err)
			}
			if r.ClaimID != claimID {
				continue
			}
			switch r.Type {
			case rings.TestClaimRecFalsify:
				if ev.Revision > o.campaignRev {
					results[r.StepID] = append(results[r.StepID], &r)
				}
			case rings.TestClaimRecReopen:
				// 重开:旧战役证据整体失效,战役起点推进。
				o.campaignRev = ev.Revision
				results = map[string][]*rings.TestClaimRecord{}
				o.rounds = nil
			}
		}
	}
	if !created {
		return nil, fmt.Errorf("%w: task %s claim %s", ErrClaimNotFound, taskID, claimID)
	}

	// 反证轮:战役内(创建 revision > campaignRev)绑定本声明的 Step。
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("arbiter: 查反证步骤: %w", err)
	}
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id != claimID {
			continue
		}
		if o.campaignRev > 0 && s.CreatedRev <= o.campaignRev {
			continue // 前战役步骤:历史保留,不再计入判定
		}
		round, _ := s.Detail["round"].(float64)
		ri := falsifyRoundInfo{stepID: s.ID, round: int(round), status: s.Status}
		if rs := results[s.ID]; len(rs) > 0 {
			// 每轮一条受控写回;防御性取最末条。
			ri.result = rs[len(rs)-1]
		}
		o.rounds = append(o.rounds, ri)
	}
	return o, nil
}

// parseAlternatives 从创建事件 detail 解冻结解释清单(ID 缺失的条目
// 丢弃——受控入口保证写回时 ID 齐全,这里只做防御)。
func parseAlternatives(raw any) []rings.Alternative {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []rings.Alternative
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
		out = append(out, rings.Alternative{ID: id, Label: label})
	}
	return out
}
