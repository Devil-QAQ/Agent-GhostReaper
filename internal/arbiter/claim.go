package arbiter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

// TestClaim 确认裁决(P5-02 v1.3.0 机器三查,第三章 §5 修订版):确认
// 判定收口为机器三查——纯代码、毫秒级、交卷即查,不经收割轮。本文件
// 落地三查的纯函数求值;四点最小契约逐点对应:
//
//	① 判定标准受限结构:ClaimStandard 只有 kind/require_impact/
//	   max_rounds 三个字段,ParseClaimStandard 对结构外的一切拒收。
//	   三查口径下标准只承担"创建时冻结"一件事(查三),完整反证的
//	   kind 语义(竞争解释/影响面/验证者独立)已随 v1.3.0 砍除清单
//	   退役,不再进求值。
//	② 创建时冻结:标准存于 TestClaim 创建事件负载(detail.standard),
//	   账本无"改 detail"的事件种类 ⇒ 物理冻结(与 Goal 谓词同一论证)
//	   ——查三的机器落点。
//	③ 只读账本结构化证据:求值输入是账本解析出的事实(引用命中/
//	   证明物匹配/自查记录/推翻记录),不读任何外部输入、零写账本;
//	   账本解析(引用→交换→响应体)由调用方(overmind 交卷即查入口)
//	   完成,与复现包种子解析同源,防门槛与装配漂移。
//	④ LLM 陈述不构成证据:虫群自评(countercheck_note 之外的任何
//	   "已确认"文本)无进求值的通道——查二只认自查记录非空这一
//	   结构性事实,不读内容;查一只认引用命中与证明物子串匹配。
//
// 三查判定次序固定(查一→查二→查三),逐条给出条件行(R2 可审计)。

var (
	// ErrClaimNotFound 声明节点在事件流中无创建事件。
	ErrClaimNotFound = errors.New("arbiter: testclaim not found")
	// ErrNoClaimStandard 声明创建时未冻结判定标准——无标准的声明不由
	// 机器终审(交卷受控入口在创建时冻结标准,缺失即账本异常)。
	ErrNoClaimStandard = errors.New("arbiter: testclaim has no frozen standard")
)

// 判定标准 kind 值域(single_round_falsify 为 P1-08 兼容保留)。
const (
	ClaimStandardSingleRoundFalsify = "single_round_falsify"
	ClaimStandardFullFalsify        = "full_falsify" // P2-02 完整反证(v1.3.0 起仅作冻结词面,求值只验"创建时冻结"一件事)
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
	// RequireImpact 影响面证据是否为确认必需(P5-02 起不再进求值:
	// 结构化影响申报已随砍除清单退役,字段为受限结构的既有形状保留)。
	RequireImpact bool `json:"require_impact"`
	// MaxRounds full_falsify 专属(P5-02 起不再进求值,同上)。
	MaxRounds int `json:"max_rounds,omitempty"`
}

// DefaultClaimStandard 默认判定标准:由交卷受控入口在创建声明时写入
// detail.standard(冻结)。v1.3.0 起求值只验"创建时冻结且可解析",
// kind 的反证语义不再展开。
func DefaultClaimStandard() map[string]any {
	return map[string]any{
		"kind": ClaimStandardFullFalsify, "require_impact": true,
		"max_rounds": DefaultMaxFalsifyRounds,
	}
}

// ParseClaimStandard 解析并校验受限结构判定标准(契约①的声明侧入口;
// 查三的"标准可解析"机器落点)。
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
	return &st, nil
}

// 三查条件种类(只进 testclaim.record verdict 负载,判定次序固定:
// 查一→查二→查三)。P5-02⑥:旧判定叶子条件(反证轮完成/存在性未推翻/
// 竞争解释全排除/影响面有证据/验证者独立)已随砍除清单物理删除。
const (
	// CondEvidenceTraceable 查一·证据是真的:v1.3.0 语义保留进化——
	// 交卷单 evidence_refs 逐条在账本命中(镜交换或可组包直连工具
	// 记录,P4-62 C(b) 通道回查),且 proving_marker 在命中引用的
	// 响应体中出现(子串匹配)。引用不命中或证明物不匹配即不过。
	CondEvidenceTraceable = "evidence_traceable"
	// CondCountercheckDone 查二·反证做了且没翻车:countercheck_note
	// 非空(有自查记录),且当前战役(reopen 清边界)内无未消解的
	// 推翻记录。
	CondCountercheckDone = "countercheck_done"
	// CondStandardFrozen 查三·标准没偷改、不认自吹:判定标准创建时
	// 冻结(存在且可解析,R2 契约②),虫群自评陈述不构成证据(R2
	// 契约④——求值只吃结构化事实,自评文本无通道)。
	CondStandardFrozen = "standard_frozen"
)

// 查一/查二失败的机器原因句(P5-02 定稿文案,逐字入账;查三失败
// 原因由"判定标准未冻结"固定句承载)。
const (
	ReasonUntraceableEvidence = "证据不可追溯/证明物不匹配"
	ReasonCountercheckMissing = "反证自查无记录"
	ReasonRefutedOpen         = "存在未消解的推翻记录"
	ReasonStandardUnfrozen    = "判定标准未冻结"
)

// CheckFacts 三查的事实输入:账本解析(引用命中/响应体/推翻记录/冻结
// 标准)由调用方完成——arbiter 只做纯函数求值(毫秒级,零 IO 零写),
// 与复现包种子解析同源的解析在 overmind 侧单点收口。
type CheckFacts struct {
	// RefsTotal/RefsHit 交卷单 evidence_refs 总条数与在账本命中的条数
	// (镜交换按交换 ID 命中;直连形态按 URL 回查命中可组包工具记录,
	// P4-62 C(b) 通道)。
	RefsTotal, RefsHit int
	// MarkerInBody proving_marker 在任一命中引用的响应体原文中出现
	// (子串匹配——主证据承载证明物;引用全不命中时无体可验,恒假)。
	MarkerInBody bool
	// Countercheck countercheck_note 原文(查二只验非空,不读内容——
	// 自评陈述不构成证据)。
	Countercheck string
	// StandardFrozen 创建事件冻结的判定标准存在且可解析。
	StandardFrozen bool
	// RefutedOpen 当前战役内存在未消解的推翻记录(reopen 记录清
	// 战役边界=消解)。
	RefutedOpen bool
}

// CheckOutcome 三查判定结果:Verdict 是待落账的裁决记录(type=verdict),
// Status 是声明应迁入的状态(confirmed|insufficient|candidate——
// candidate 即三查不过待补考,声明留在候选态等重交)。记账与状态迁移
// 由调用方(交卷即查入口)以 actor=arbiter 落账,本求值零写。
type CheckOutcome struct {
	Verdict rings.TestClaimRecord
	Status  string
}

// ThreeChecks 机器三查(P5-02①:纯函数、判定次序固定、毫秒级)。
// 全过 → confirmed;任一不过 → insufficient(终态判定与补考次数控制
// 归调用方:首败回候选补考、补考再败落 insufficient)。
func ThreeChecks(f CheckFacts) *CheckOutcome {
	v := rings.TestClaimRecord{Type: rings.TestClaimRecVerdict, Decision: rings.VerdictConfirm, Satisfied: true}
	refsOK := f.RefsTotal > 0 && f.RefsHit == f.RefsTotal && f.MarkerInBody
	checkOK := strings.TrimSpace(f.Countercheck) != "" && !f.RefutedOpen
	cond := func(kind string, ok bool) rings.ArbiterConditionResult {
		actual := 0
		if ok {
			actual = 1
		}
		return rings.ArbiterConditionResult{Kind: kind, Required: 1, Actual: actual, Satisfied: ok}
	}
	v.Conditions = []rings.ArbiterConditionResult{
		cond(CondEvidenceTraceable, refsOK),
		cond(CondCountercheckDone, checkOK),
		cond(CondStandardFrozen, f.StandardFrozen),
	}
	out := &CheckOutcome{Verdict: v, Status: rings.TestClaimConfirmed}
	if refsOK && checkOK && f.StandardFrozen {
		return out
	}
	// 失败原因(缺哪样):按判定次序列第一个不过的查,机器拼装非
	// 自由发挥;多查同败逐条并列(补考反馈注入用)。
	var parts []string
	if !refsOK {
		parts = append(parts, ReasonUntraceableEvidence)
	}
	if !checkOK {
		if strings.TrimSpace(f.Countercheck) == "" {
			parts = append(parts, ReasonCountercheckMissing)
		} else {
			parts = append(parts, ReasonRefutedOpen)
		}
	}
	if !f.StandardFrozen {
		parts = append(parts, ReasonStandardUnfrozen)
	}
	out.Status = rings.TestClaimCandidate
	out.Verdict.Decision = rings.VerdictRetake
	out.Verdict.Satisfied = false
	out.Verdict.PendingReason = strings.Join(parts, ";")
	return out
}

// (存量旧账本的中断检测链——stepTerminal/claimOrigin/falsifyRoundInfo/
// InterruptedClaim/redispatchesUsed 等——已随立判断链与中断自动重派
// 机制退役整段删除,P5-03①③;新账本三查口径下零反证步骤零 testing
// 态,存量旧任务走升级删除路线。)
