package rings

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// NodeType 六类节点(第三章 §2;Hypothesis 由第四章 §5 扩入)。
type NodeType string

const (
	NodeGoal       NodeType = "goal"       // 里程碑,带机器可判定的完成谓词(P1-06 起接仲裁者)
	NodeStep       NodeType = "step"       // 意图/候选行动,不绑定具体工具
	NodeFact       NodeType = "fact"       // 挂证据的已证命题
	NodeAsset      NodeType = "asset"      // 资产节点族,共 12 类
	NodeTestClaim  NodeType = "testclaim"  // 可证伪的漏洞声明,带状态机(P1-08)
	NodeHypothesis NodeType = "hypothesis" // 虫群直觉候选回流
)

// TestClaim 状态机词汇(P1-08 基础版 + P2-02 完整版,第三章 §5):
// 候选 → 反证中 → 轮末机器评估 → Confirmed / 不成立 / 待确认 / 待下轮反证;
// 待下轮反证由收割派发下一轮回反证中;Confirmed/不成立/待确认可被重开
// 回反证中(reopen 事件,历史不删,R1/§3.1)。归地基层与 NodeType 同惯例:
// swarm/overmind/arbiter/server 四方共用,避免各自拼字符串漂移。
// Confirmed/不成立/待确认/待下轮反证均由仲裁者机器判定迁移(actor=arbiter,
// R2/R3);待确认与待下轮反证是一等状态且必带搁置理由(R4,理由在
// testclaim.record verdict 负载里,账本入口强制非空)。
// 不成立(P4-55 ②,问题清单 #75):存在性被反证推翻的终态——反证独立
// 单位 + 结构化结论,与确认同一质量保障,消除"未推翻即信、推翻却要
// 复核"的双重标准;新证据仍可经 reopen 重开再证。
const (
	TestClaimCandidate     = "candidate"      // 虫群经受控入口提交,待主宰收割派反证
	TestClaimTesting       = "testing"        // 反证轮已派发,验证单位执行中
	TestClaimAwaitingRound = "awaiting_round" // P2-02:轮末评估=再反证(解释未全灭且轮次未尽),待收割派下一轮
	TestClaimConfirmed     = "confirmed"      // 仲裁者判定:反证未推翻且影响面证据充分
	TestClaimRefuted       = "refuted"        // P4-55 ②:仲裁者判定不成立(存在性被反证推翻,理由"被反证推翻")
	TestClaimPending       = "pending"        // 待确认:机器生成搁置理由(R4),诚实呈现
	TestClaimFixed         = "fixed"          // P3-06:复测结论"已修复"(锤炼态保持已确认,修复态由复测记录承载;未修复复测回滚到修复前状态)
	TestClaimDiscarded     = "discarded"      // P4-55 审核迭代:已废弃终态(摘除资产程序确定性批量废弃,理由"目标已摘除";勾回不自动复活,再验走操作者深入)
)

var nodeTypes = map[NodeType]bool{
	NodeGoal:       true,
	NodeStep:       true,
	NodeFact:       true,
	NodeAsset:      true,
	NodeTestClaim:  true,
	NodeHypothesis: true,
}

// Hypothesis 状态机词汇(P2-03,第四章 §5 虫群直觉回流)。候选 → 主宰
// 收割评估三去向:验证中(值得跟,转 Step 派发验证,R12 不得派回原
// 发现者)/ 挂账(存疑留在图上待唤醒,低排序 ≠ 拒绝)/ 已废弃(不成立,
// 标记废弃 + 理由,永不删除,R10)。验证单位写回结构化结论后:成立 /
// 已废弃(验证推翻,理由留痕)/ 未能定论(回候选,下次收割再评估)。
// 归 rings 与 TestClaim 状态词汇同惯例:swarm/overmind/server 三方共用,
// 避免各自拼字符串漂移。
const (
	HypothesisCandidate = "candidate" // 虫群经 submit_hypothesis 提交,待主宰收割评估
	HypothesisTesting   = "testing"   // 验证 Step 已派发,验证单位执行中
	HypothesisHeld      = "held"      // 主宰裁决存疑挂账,留在图上待唤醒(每次唤醒重估)
	HypothesisConfirmed = "confirmed" // 验证单位结构化结论:成立(R11:假设成立 ≠ 漏洞已验证)
	HypothesisDiscarded = "discarded" // 废弃:评估判不成立或验证推翻,理由留痕,永不删除(R10)
)

// AssetClass 资产 12 类(第三章 §2)。类型清单开放:未知资产一律落入
// 其他类兜底,绝不因装不下而丢弃;其他类中反复出现的模式可晋升为正式类型。
type AssetClass string

const (
	AssetDomain        AssetClass = "domain"
	AssetSubdomain     AssetClass = "subdomain"
	AssetHost          AssetClass = "host"
	AssetService       AssetClass = "service"
	AssetAPI           AssetClass = "api"
	AssetOrganization  AssetClass = "organization"
	AssetMiniProgram   AssetClass = "miniprogram"
	AssetApp           AssetClass = "app"
	AssetWechatAccount AssetClass = "wechat_account"
	AssetPerson        AssetClass = "person"
	AssetLeakInfo      AssetClass = "leak_info"
	AssetOther         AssetClass = "other"
)

var assetClasses = map[AssetClass]bool{
	AssetDomain:        true,
	AssetSubdomain:     true,
	AssetHost:          true,
	AssetService:       true,
	AssetAPI:           true,
	AssetOrganization:  true,
	AssetMiniProgram:   true,
	AssetApp:           true,
	AssetWechatAccount: true,
	AssetPerson:        true,
	AssetLeakInfo:      true,
	AssetOther:         true,
}

// IsValidAssetClass 资产类是否在 12 类词表内(与 HarmClass/IntentAction
// 等词表同惯例:入口校验方共用,不自拼清单)。
func IsValidAssetClass(c AssetClass) bool { return assetClasses[c] }

// AssetSource 资产来源三态(P4-05,问题清单 #10):种子(建任务表单)/
// 手动(面板新增资产,actor=造物主)/ 发现(虫群回流收割入图,随回流
// 通道点亮)。落在 node.added 的 Detail["source"];写入方各自声明,
// 读侧 AssetSourceOf 归一。归 rings 与 12 类同惯例:三方共用不漂移。
const (
	AssetSourceSeed       = "seed"       // 建任务种子表单入账
	AssetSourceManual     = "manual"     // 面板"新增资产"手动入账(默认已入授权范围)
	AssetSourceDiscovered = "discovered" // 虫群发现,主宰收割入图(回流通道)
)

// AssetSourceOf 资产节点 detail 的来源归一:未标记的历史/P1 资产按种子
// 兜底(种子表单曾是唯一资产入口)。
func AssetSourceOf(detail map[string]any) string {
	if s, _ := detail["source"].(string); s != "" {
		return s
	}
	return AssetSourceSeed
}

// EdgeType 类型化边。第三章列明 about/covers/derived_from;
// 其余依第四章定稿:资产层级关系为类型化边(sub_of,R5)、待办必须绑定在
// 图上需 Step 依赖边(depends_on)、Hypothesis 须指向具体资产(复用 about);
// produces 为进度图"意图→事实→漏洞"血缘链补边(Step 产出 Fact;P2-03 起
// 虫群直觉候选同为 Step 产出物,Step → Hypothesis;P4-12 起收割入图的
// 发现型资产同为 Step 产出物,Step → Asset——对象出生的血缘边);
// verifies 为假设回流轨迹边(P2-03,第四章 §5):主宰裁决"值得跟"时
// 派发的验证 Step 指回它验证的假设——回流轨迹全程类型化可查。
type EdgeType string

const (
	EdgeSubOf       EdgeType = "sub_of"       // Asset → Asset(层级)
	EdgeAbout       EdgeType = "about"        // Fact|Hypothesis|TestClaim → Asset
	EdgeCovers      EdgeType = "covers"       // Step → Asset(覆盖)
	EdgeProduces    EdgeType = "produces"     // Step → Fact|Hypothesis|Asset(血缘)
	EdgeDerivedFrom EdgeType = "derived_from" // TestClaim → Step|Fact(血缘)
	EdgeDependsOn   EdgeType = "depends_on"   // Step → Step(依赖)
	EdgeVerifies    EdgeType = "verifies"     // Step → Hypothesis(验证步骤 → 被验证假设,P2-03)
)

type nodeTypeSet map[NodeType]bool

type edgeRule struct {
	from nodeTypeSet
	to   nodeTypeSet
}

var edgeRules = map[EdgeType]edgeRule{
	EdgeSubOf: {nodeTypeSet{NodeAsset: true}, nodeTypeSet{NodeAsset: true}},
	// P1-08 起 about 允许 TestClaim → Asset:漏洞声明的资产锚点必须挂到
	// 具体资产(第五章 §1"资产锚点"),与 Fact/Hypothesis 同一边类型。
	EdgeAbout:       {nodeTypeSet{NodeFact: true, NodeHypothesis: true, NodeTestClaim: true}, nodeTypeSet{NodeAsset: true}},
	EdgeCovers:      {nodeTypeSet{NodeStep: true}, nodeTypeSet{NodeAsset: true}},
	EdgeProduces:    {nodeTypeSet{NodeStep: true}, nodeTypeSet{NodeFact: true, NodeHypothesis: true, NodeAsset: true}},
	EdgeDerivedFrom: {nodeTypeSet{NodeTestClaim: true}, nodeTypeSet{NodeStep: true, NodeFact: true}},
	EdgeDependsOn:   {nodeTypeSet{NodeStep: true}, nodeTypeSet{NodeStep: true}},
	EdgeVerifies:    {nodeTypeSet{NodeStep: true}, nodeTypeSet{NodeHypothesis: true}},
}

// ActionLevel 意图四级分类(第四章 §3,Q8 于 P1-07 落地):风险分档框架
// 而非动作清单——任何新动作、任何攻击链组合均可映射到四档定级,不限制
// 模型可发起的动作类型。词汇表归 rings(与 NodeType/AssetClass 同惯例:
// 事件负载里的受限词汇由地基层统一校验)。
// P2-06b 三级处置 v2:四级保留用于展示/报告/留痕与审批门槛配置,不再
// 逐级决定放行——处置次序(拒绝线→门槛弹门→其余放行)在 swarm 审批门。
type ActionLevel string

const (
	ActionReadOnly      ActionLevel = "只读探测"   // L1
	ActionActiveTest    ActionLevel = "主动测试"   // L2
	ActionExploit       ActionLevel = "利用深入"   // L3
	ActionModifyPersist ActionLevel = "修改与持久化" // L4:增删改数据(弹门线的默认门槛)
)

// HarmClass 拒绝线的危害声明(第四章 §3 v2,P2-06b):意图分级之外的
// 结构化严重性标记——数据销毁与拒绝服务类无论门槛多宽都直接拒绝
// (非常严重项),造物主可在面板"研判后放行"。词汇表归 rings。
// 审批对象仍是 Step 意图(v1 不做参数兜底规则引擎,人即兜底)。
type HarmClass string

const (
	HarmNone            HarmClass = ""                  // 普通(缺省)
	HarmDataDestruction HarmClass = "data_destruction"  // 数据销毁
	HarmDenialOfService HarmClass = "denial_of_service" // 拒绝服务类
)

var harmClasses = map[HarmClass]bool{
	HarmNone:            true,
	HarmDataDestruction: true,
	HarmDenialOfService: true,
}

// IsValidHarmClass 危害声明是否在词表内。
func IsValidHarmClass(h HarmClass) bool { return harmClasses[h] }

var actionLevels = map[ActionLevel]bool{
	ActionReadOnly:      true,
	ActionActiveTest:    true,
	ActionExploit:       true,
	ActionModifyPersist: true,
}

// IntentAction 任务级拦截的动作类型(第四章 §4 v2.1,P2-08e):操作者语言
// 的动作分档——与意图四级分类(风险分档)正交,供造物主按"对什么资产
// 做什么动作"画任务级边界。词汇表归 rings;swarm 侧按工具与参数做
// 确定性归类(不依赖模型声明)。
type IntentAction string

const (
	IntentDataRead   IntentAction = "数据读取" // 拉取目标内容(GET/HEAD 等)
	IntentInjectTest IntentAction = "注入测试" // 发送测试载荷(sqlmap、POST 数据载荷等)
	IntentUpload     IntentAction = "上传"   // 向目标上传文件或内容
	IntentBruteForce IntentAction = "爆破"   // 口令/凭据爆破(hydra 等)
	IntentCrawl      IntentAction = "爬取"   // 目录枚举与镜像爬取(gobuster、wget -r 等)
	IntentPortScan   IntentAction = "端口扫描" // 端口与服务扫描(nmap、port_probe)
)

var intentActions = map[IntentAction]bool{
	IntentDataRead:   true,
	IntentInjectTest: true,
	IntentUpload:     true,
	IntentBruteForce: true,
	IntentCrawl:      true,
	IntentPortScan:   true,
}

// IsValidIntentAction 动作类型是否在词表内。
func IsValidIntentAction(a IntentAction) bool { return intentActions[a] }

// 任务级拦截处置词(第四章 §4:处置 = 禁止 / 允许 + 次数上限)。
const (
	IntentForbid = "forbid" // 禁止:命中直接拒绝留痕
	IntentAllow  = "allow"  // 允许:Quota > 0 时限次,超额自动转禁止
)

var intentDispositions = map[string]bool{
	IntentForbid: true,
	IntentAllow:  true,
}

// IsValidIntentDisposition 处置词是否在词表内。
func IsValidIntentDisposition(s string) bool { return intentDispositions[s] }

// IntentRule 一条任务级意图拦截规则(第四章 §4 v2.1,P2-08e):
// 资产范围 × 动作类型 × 处置。随 RoE 快照提交(R7 无旁路:增删改走
// roe.updated 记账,下一轮领取读到新值);AssetIDs 空 = 全部资产;
// Quota 仅处置=允许时生效(0 = 不限次)。
type IntentRule struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`              // 动作描述(自由文本或内置动作词)
	AssetIDs    []string `json:"asset_ids,omitempty"` // 资产范围(空=全部资产)
	Disposition string   `json:"disposition"`         // forbid|allow
	Quota       int      `json:"quota,omitempty"`     // 允许时的次数上限(0=不限)
	Patterns    []string `json:"patterns,omitempty"`  // 判定正则(空=按内置动作词分类判)
	Note        string   `json:"note,omitempty"`
}

// validateIntentRules RoE 快照内的任务级规则校验:ID 唯一非空、动作描述
// 非空(审核迭代三轮起为自由文本,不限词表;正则编译校验在 server
// 保存入口)、处置在词表内、次数上限非负、禁止不得携带次数上限。
func validateIntentRules(rules []IntentRule) error {
	seen := map[string]bool{}
	for i := range rules {
		r := &rules[i]
		if strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("%w: 第 %d 条规则缺 ID", ErrInvalidIntentRule, i+1)
		}
		if seen[r.ID] {
			return fmt.Errorf("%w: 规则 ID %q 重复", ErrInvalidIntentRule, r.ID)
		}
		seen[r.ID] = true
		if strings.TrimSpace(r.Action) == "" {
			return fmt.Errorf("%w: 第 %d 条规则缺动作描述", ErrInvalidIntentRule, i+1)
		}
		if !IsValidIntentDisposition(r.Disposition) {
			return fmt.Errorf("%w: 处置 %q 不在词表内(forbid|allow)", ErrInvalidIntentRule, r.Disposition)
		}
		if r.Quota < 0 {
			return fmt.Errorf("%w: 规则 %s 次数上限不能为负", ErrInvalidIntentRule, r.ID)
		}
		if r.Disposition == IntentForbid && r.Quota > 0 {
			return fmt.Errorf("%w: 规则 %s 是禁止,不得携带次数上限", ErrInvalidIntentRule, r.ID)
		}
	}
	return nil
}

// EventKind 事件类型。事件流信封(task_id+revision+kind+payload)保持稳定,
// 新 kind 只在 applyEvent 的 switch 中扩展受控入口,不改表结构(第三章 §7)。
// P1-04 扩入三类:任务生命周期(第七章)、RoE 变更(R7 一切授权变更皆账本
// 事件)、虫群原始执行记录(第四章 §2 受控入口;不进双图投影,走事件流审计)。
// P1-06 扩入仲裁裁决(第三章 §6:Goal 完成判定的机器终审结果,审计流)。
// P1-07 扩入审批记录(第四章 §3 审批门的弹门与裁决,审计流)。
// P1-08 扩入 TestClaim 状态机记录(第三章 §5:反证写回与确认裁决,审计流)。
// P2-01 扩入镜留痕(第一章"全程流量经镜":镜对工具流量每次交换记账)。
// P2-02 扩入重开与收割定稿记录(testclaim.record 新类型 reopen/repro)。
// P2-03 扩入假设回流记录(hypothesis.record:主宰收割评估裁决 + 验证
// 单位结构化结论,第四章 §5 虫群直觉回流)。
// P2-04 扩入黑匣子记录(第四章 §6 账本分工:预算状态变化与聚合里程碑等
// 系统级事件入年轮黑匣子通道;逐次 token 用量走独立记账表,不进事件流)。
// P2-05 扩入舵杆记录(第八章第 8 节纠偏纵深:steer_work / kill_work 指令
// 走账本,排队与送达两级留痕;采纳/被拒/过时的回执状态机属 P2-08)。
// P2-06 扩入主宰研判记录(第四章 §1 态势研判五问:每轮拆解会话的研判
// 轨迹入账,五问驱动研判从账本可验证)。
// P2-06h 扩入任务元数据(任务列表完整版:分类指派 / 置顶 / 墓碑删除,
// 均为面板操作的任务级事实,走账本留痕)。
// P2-07 扩入牧者会话记录(第四章 §1 牧者:总对话入口的完整对话流与
// 纳谏轨迹,消息送达状态与建议裁决全程留痕)。
// P2-08e 扩入拦截判定记录(guard.record:全局硬规则与任务级拦截的
// 判定留痕与量化额度计数,审计流)。
// P3-13b 扩入会话元数据(session.meta:会话重命名/备注/置顶的面板
// 操作记账,列表组织信息流)。
// P4-18 扩入收口信号记录(closure.record:连续零新发现的收口提醒与
// 造物主的两键裁决,问题清单 #30)。
type EventKind string

const (
	EventNodeAdded        EventKind = "node.added"
	EventNodeStateChanged EventKind = "node.state_changed"
	EventNodeAmended      EventKind = "node.amended"
	EventEdgeAdded        EventKind = "edge.added"
	EventTaskLifecycle    EventKind = "task.lifecycle"
	EventTaskMeta         EventKind = "task.meta"
	EventRoeUpdated       EventKind = "roe.updated"
	EventSwarmRecord      EventKind = "swarm.record"
	EventArbiterVerdict   EventKind = "arbiter.verdict"
	EventApprovalRecord   EventKind = "approval.record"
	EventTestClaimRecord  EventKind = "testclaim.record"
	EventMirrorRecord     EventKind = "mirror.record"
	EventHypothesisRecord EventKind = "hypothesis.record"
	EventBlackboxRecord   EventKind = "blackbox.record"
	EventSteerRecord      EventKind = "steer.record"
	EventOvermindRecord   EventKind = "overmind.record"
	EventShepherdRecord   EventKind = "shepherd.record"
	EventUnitControl      EventKind = "unit.control"
	EventInterjectRecord  EventKind = "interject.record"
	EventGuardRecord      EventKind = "guard.record"
	EventReportRecord     EventKind = "report.record"
	EventSessionMeta      EventKind = "session.meta"
	EventClosureRecord    EventKind = "closure.record"
	EventOperatorDive     EventKind = "operator.dive"

	// P4-64(问题清单 #82)显示层包装族:模型文本的冻结时点＝校对时点,
	// 包装稿独立事件冻结(R1:节点与机器记录原文一字不动,机器判定/
	// 任务包/仲裁全走原文;投影 latest-wins,读侧优先链退原文)。
	EventClaimRoundHuman EventKind = "claim.round_human" // 反证轮人话三段(逐轮,随 falsify 记录同批)
	EventClaimSummaryPol EventKind = "claim.summary"     // 声明摘要(立判断批次生成;定稿随新定稿 repro 覆盖)
	EventFactText        EventKind = "fact.text"         // 事实标题短句与描述(收割确认时)
	EventHypoText        EventKind = "hypo.text"         // 假设标题/依据/评估理由(收割评估批次)
	EventClaimRetextNote EventKind = "claim.retest_note" // 复测结论备注校对稿(随复测记录同批)

	// P4-65(问题清单 #82 A⑨⑩)界面层包装族:研判记录与拆解派发落账后
	// 异步补写校对稿(派发与研判轮不等校对;同 P4-64 规矩——原文一字
	// 不动,校对稿仅显示层,读侧优先链退原文,存量不回填)。
	EventOvermindPolish EventKind = "overmind.record_polish" // 研判记录五问+叙述校对稿(研判记录落账后异步)
	EventStepTitle      EventKind = "step.title"             // 步骤意图标题校对稿(拆解派发落账后异步)
)

// Event 账本事件信封。Payload 按 Kind 对应 NodeAdded/NodeStateChanged/EdgeAdded。
type Event struct {
	// Revision 为 0 时由单写者自动分配(任务内取当前最大值 +1,单调递增);
	// 非 0 为显式指定(导入/恢复场景),必须严格大于任务当前最大 revision。
	Revision   int64
	Kind       EventKind
	Actor      string    // 事件归属者:creator / overmind / arbiter / swarm:<unitID> / system 等
	OccurredAt time.Time // 业务发生时刻,零值 = 记账时刻
	Payload    any
}

// NodeAdded 是 EventNodeAdded 的负载。
type NodeAdded struct {
	NodeID     string         `json:"node_id"`
	NodeType   NodeType       `json:"node_type"`
	Title      string         `json:"title,omitempty"`
	Status     string         `json:"status,omitempty"`
	AssetClass AssetClass     `json:"asset_class,omitempty"` // 仅 asset 类节点;空值兜底为 other
	Detail     map[string]any `json:"detail,omitempty"`      // 节点自由细节(证据引用、意图分级等)
}

// NodeStateChanged 是 EventNodeStateChanged 的负载。
// Status 在地基层为自由文本,具体状态机(Step/TestClaim)由 P1-04+/P1-08 定义。
type NodeStateChanged struct {
	Evidence []string `json:"evidence,omitempty"`
	NodeID   string   `json:"node_id"`
	Status   string   `json:"status"`
	Note     string   `json:"note,omitempty"` // 迁移依据(人工标记达成的理由等;机器迁移不带,P3-14b ④)
}

// NodeAmended 是 EventNodeAmended 的负载(P4-54,问题清单 #78):既有
// 节点的受限字段变更——账本只追加,变更走新事件(与 TaskMeta/SessionMeta
// 的指针语义同族)。Title 指针 nil 不动、非 nil 整体覆盖;Detail 按 key
// 合并进节点 detail(键白名单按节点类型见 amendableKeys:goal 的
// predicate=完成条件重新冻结〔仲裁契约②判定标准变更走新事件,读侧取
// 最新〕与 focus=方向聚焦标记;step 的 priority=优先级调整,队列比较
// 器读投影立即生效)。变更依据随 Note 留痕;写入口(server 面板/牧者)
// 负责业务级校验(谓词受限结构、优先级词汇、目标状态前置)。
type NodeAmended struct {
	NodeID string         `json:"node_id"`
	Title  *string        `json:"title,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
	Note   string         `json:"note,omitempty"`
}

// EdgeAdded 是 EventEdgeAdded 的负载。
type EdgeAdded struct {
	EdgeType EdgeType       `json:"edge_type"`
	FromNode string         `json:"from_node"`
	ToNode   string         `json:"to_node"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// TaskLifecycle 是 EventTaskLifecycle 的负载(P1-04,第七章):
// Manager 对任务生命周期的一切操控记账(创建/排队/拉起/暂停/恢复/停止),
// 熔断"先追加控制事件、再 cancel 任务 ctx"的次序以此为基础。
// completed(P1-10)是任务完成终态:全部目标经仲裁者机器判定达成、主宰
// 循环自然退出时由 Manager 记账——与 stopped 同为终态,审计上与
// arbiter.verdict 裁决链同账本可互证。
// settling(P2-04,第四章 §6 任务级总闸)是运行中的收尾子态:任务整体
// 超时到点,主宰停止派发、在跑单位优雅收尾、宽限硬停后经仲裁者终局
// 判定定终态(completed|stopped)——超时不是失败,收尾全程可审计。
// checkpointed(P3-07,第七章「故障与恢复」断点续接)是手动断点存档态:
// 操作者主动冻结(在跑单位到工具调用边界收拢、会话保留可续)后进程
// 可关;重启收养把 checkpointed 与运行中同列自动重拉,原会话原地续跑。
// 与 hibernated 的分工(P4-33 起):冬眠=冻结收场让位名额、唤醒拉起
// 续跑;断点=手动存档、重启自动续跑——两者收场同约定(边界落地、
// 会话保留可续)。
type TaskLifecycle struct {
	Action string         `json:"action"` // created|queued|started|paused|resumed|stopped|completed|settling
	Title  string         `json:"title,omitempty"`
	Detail map[string]any `json:"detail,omitempty"` // 自由细节(如 reason、操作者)
}

// 生命周期动作白名单(受控入口:未知动作一律拒)。hibernated(P2-08,
// 第七章「任务冬眠」)是运行中的冻结子态:主宰循环挂起、虫群单位挂起、
// 牧者会话冻结、计时器停走;唤醒记账为 started|settling(恢复冬眠前
// 状态,detail 带 woken 标记)——配对扫描(hibernated → 下一条生命周期
// 事件)即冬眠墙钟时长,任务收尾线据此顺延。
var taskLifecycleActions = map[string]bool{
	"created": true, "queued": true, "started": true,
	"paused": true, "resumed": true, "stopped": true,
	"completed": true, "settling": true, "hibernated": true,
	"checkpointed": true,
}

// TaskMeta 是 EventTaskMeta 的负载(P2-06h,任务列表完整版):任务级
// 面板操作的元数据记账——分类指派、置顶、墓碑删除。与 lifecycle 分工:
// 生命周期事件管运行状态,本事件管列表组织信息;投影各管各列,互不
// 覆盖(updated_rev 仍锚最后一条生命周期事件,FIFO 重准入次序不受
// 分类/置顶操作扰动)。指针字段 = 本次要改的项(nil 不动),空字符串
// 分类 = 移出分类。删除是墓碑标记(第七章 §3:与 R1 历史不删一致,
// 真正的数据销毁是造物主主动执行的特权清除,不属业务状态变更)——
// 删除前任务须先落终态(运行中的先停再删,由写入口保证;账本侧只锁
// 结构)。写入口(面板侧)拒绝对已墓碑任务再改元数据;重放侧不校验
// 跨表事实(分类存在性),保证历史事件重放确定性。
type TaskMeta struct {
	Category *string `json:"category,omitempty"` // 分类 ID(空串 = 移出分类)
	Pinned   *bool   `json:"pinned,omitempty"`   // 置顶(true 置顶 / false 取消)
	Deleted  bool    `json:"deleted,omitempty"`  // 墓碑标记(不可逆)
	Reason   string  `json:"reason,omitempty"`   // 操作理由(删除必填,随账本留痕)
}

// RoeUpdated 是 EventRoeUpdated 的负载(P1-04,第四章 §4):
// RoE(授权边界)变更走账本(R7 无旁路),改动下一轮生效——每次变更推进
// 图版本,虫群执行前复检(R14)据此识别"凭据/边界已变"。
// AllowedTools/DeniedTools 界定虫群可用工具全集的过滤边界(Q7)。
// ApprovalThreshold 界定审批门槛(P2-06b,第四章 §3 v2):该级及以上的
// 意图弹门等待面板批准,其余放行留痕。空值 = 默认宽(仅增删改弹门,
// 即"修改与持久化"级);可收紧到更低级别(敏感目标逐级加严)。
// 旧字段 MaxActionLevel(P1-07 分级上限)随三级处置 v2 废止——四级不再
// 逐级决定放行,上限语义由门槛+拒绝线取代;字段保留仅为旧账本事件
// 的读兼容,消费侧不再使用。
// IntentRules 是任务级意图拦截规则(P2-08e,第四章 §4 v2.1):资产范围 ×
// 动作类型 × 处置,随快照整体提交(增删 = 提交新快照);量化额度由
// 处置"允许 + 次数上限"承载(第四章 §4 的正式落地形态)。
// ExcludedAssets 是授权范围摘除清单(P4-05,问题清单 #10):资产默认
// 全部纳入授权范围,摘除 = 提交含该资产 ID 的新快照;摘除的资产主宰
// 不再派发覆盖它的意图(派发入口守卫拒绝,covers 判定),改动下一轮
// 生效(不做硬中断,在飞单位跑完)。
// ScopeApproved 是范围外资产放行清单(P4-44,问题清单 #61):渗透任务
// 的待批资产经面板/牧者放行后转范围内可测;随快照整体提交,语义与
// ExcludedAssets 同款(增删 = 提交新快照,条目须图上存在)。
// CreatorNotes 是造物主注意事项(P4-49,问题清单 #70):对做法的软要求
// (如"用小字典,不要用大字典""不要修改任何数据")——与拦截规则分工:
// 方法偏好写这里,绝对红线配拦截规则。**指针语义**:nil = 本次变更不
// 动它(其余字段是整体覆盖快照,透传当前值的各写入口极易漏带新字段
// 造成误清空;本字段只有建任务与 RoE 面板两处显式写,nil 即保持)。
// 非空指针整体覆盖(空串 = 清空注意事项)。投影按逐笔叠加(COALESCE),
// 事件流重放仍确定。
type RoeUpdated struct {
	AllowedTools      []string     `json:"allowed_tools,omitempty"`      // 白名单(空=不设白名单)
	DeniedTools       []string     `json:"denied_tools,omitempty"`       // 黑名单(优先于白名单)
	ApprovalThreshold ActionLevel  `json:"approval_threshold,omitempty"` // 审批门槛(空=默认宽:仅增删改弹门)
	IntentRules       []IntentRule `json:"intent_rules,omitempty"`       // 任务级拦截规则(P2-08e)
	ExcludedAssets    []string     `json:"excluded_assets,omitempty"`    // 授权范围摘除清单(P4-05;空=全部纳入)
	ScopeApproved     []string     `json:"scope_approved,omitempty"`     // 范围外资产放行清单(P4-44;随快照整体提交)
	CreatorNotes      *string      `json:"creator_notes,omitempty"`      // 造物主注意事项(P4-49;nil=本次不动)
	MaxActionLevel    ActionLevel  `json:"max_action_level,omitempty"`   // 已废止(P1-07 分级上限,读兼容)
	Note              string       `json:"note,omitempty"`               // 变更说明(谁、为何)
}

// 虫群原始执行记录类型白名单(受控入口)。
const (
	SwarmRecSessionStarted = "session_started"  // 虫群会话开始(绑定 transcript_id 与图版本)
	SwarmRecToolCall       = "tool_call"        // 一次工具调用及其结果(R18:大输出只存 blob 引用)
	SwarmRecSessionEnd     = "session_finished" // 会话结束(终因与去向)
	SwarmRecStaleRefused   = "stale_refused"    // R14 复检失败,拒绝按过期计划执行
)

var swarmRecordTypes = map[string]bool{
	SwarmRecSessionStarted: true,
	SwarmRecToolCall:       true,
	SwarmRecSessionEnd:     true,
	SwarmRecStaleRefused:   true,
}

// SwarmRecord 是 EventSwarmRecord 的负载(P1-04,第四章 §2 受控入口):
// 虫群单位的原始执行记录实时入账——防"结束后一次性提交"丢进度;不进
// nodes/edges 投影(双图只收业务节点),审计走事件流查询。
// 大段工具输出入内容寻址文件,账本只存引用(R18:ResultRef 形如 "sha256:...")。
type SwarmRecord struct {
	StepID       string          `json:"step_id"`
	UnitID       string          `json:"unit_id"`
	TranscriptID string          `json:"transcript_id,omitempty"` // Norma 转录 ID(第二章四类记录分工)
	Type         string          `json:"type"`
	GraphVersion int64           `json:"graph_version,omitempty"` // 领取时绑定的图版本
	ToolName     string          `json:"tool_name,omitempty"`
	ToolArgs     json.RawMessage `json:"tool_args,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	ResultRef    string          `json:"result_ref,omitempty"`  // 内容寻址 blob 引用(R18)
	ResultHead   string          `json:"result_head,omitempty"` // 结果截头(引用存在时)
	Note         string          `json:"note,omitempty"`
	// Resumable session_finished 的可续标记(P3-05c ① 恢复闭环):模型
	// 故障受阻(blocked)收场时会话保留——恢复重开该步骤时优先原会话
	// Resume 续跑(上下文零重建,与审批续跑/冬眠/重启收养同语义)。
	// 仅 session_finished 携带;其余收场(exhausted 接力等)不设。
	Resumable bool `json:"resumable,omitempty"`
	// Checkpointed session_finished 的断点存档标记(P3-07 断点续接):
	// 手动断点冻结收场——当前工具调用完成后单位落地,步骤回可领取,
	// 会话保留可续;恢复领取时优先原会话 Resume 续跑(与 Resumable 同
	// 语义、不同来源:Resumable=模型故障受阻,Checkpointed=操作者主动
	// 存档断点)。仅 session_finished 携带。
	Checkpointed bool `json:"checkpointed,omitempty"`
	// ScopeWait session_finished 的范围等待标记(P4-51 ①):命令命中
	// 任务范围外,单位冻结收场让位名额——步骤落 scope_wait 态等"加入
	// 资产/禁止测试"裁决,会话冻结保留;批准后步骤回可领取(插队队首),
	// 重领的单位经转录 Resume 原会话续跑。仅 session_finished 携带。
	ScopeWait bool `json:"scope_wait,omitempty"`
}

// ArbiterConditionResult 仲裁裁决中单个条件的求值结果:要求的结构化
// 证据与实际计数一一对应(R2:机器终审必须可审计到"依据什么判的")。
type ArbiterConditionResult struct {
	Kind           string `json:"kind"`                      // 条件种类(值域见 arbiter 包)
	AssetID        string `json:"asset_id"`                  // 条件锚定的资产
	Tool           string `json:"tool,omitempty"`            // tool_observed:工具名
	ResultContains string `json:"result_contains,omitempty"` // tool_observed:结果匹配串
	Required       int    `json:"required"`                  // 达标阈值(条件里的 min)
	Actual         int    `json:"actual"`                    // 账本内实计数
	Satisfied      bool   `json:"satisfied"`
}

// 审批记录类型白名单(受控入口)。
const (
	ApprovalRecRequested = "requested"  // 弹门:审批请求已登记,等待面板裁决
	ApprovalRecApproved  = "approved"   // 面板批准
	ApprovalRecDenied    = "denied"     // 拒绝(面板拒绝/拒绝线自动拒绝/等待期间熔断,原因见 Reason 与 DenyLine)
	ApprovalRecForced    = "forced"     // P2-06b:研判后放行(P4-51 起写路径废除,历史记录留档;批准/拒绝同 RequestID 收口新卡)
	ApprovalRecGroupPass = "group_pass" // P4-17:同组放行(代理旁路组批准一次后,该单位同组命令直接执行的逐次留痕)
	ApprovalRecStepPass  = "step_pass"  // P4-27 ⑧:同步骤放行(审批门槛批准一次后,该单位本步骤后续命令直接执行的逐次留痕)
	ApprovalRecScopeWait = "scope_wait" // P4-51:范围待决卡(渗透拦截/红队可否决;同一目标一张卡,RequestID=sc-<目标>)
)

var approvalRecordTypes = map[string]bool{
	ApprovalRecRequested: true,
	ApprovalRecApproved:  true,
	ApprovalRecDenied:    true,
	ApprovalRecForced:    true,
	ApprovalRecGroupPass: true,
	ApprovalRecStepPass:  true,
	ApprovalRecScopeWait: true,
}

// 拒绝线类别(DenyLine 词表,P2-06b):拒绝的来源分类,面板据此呈现
// "研判后放行"入口(仅拒绝线的自动拒绝可放行;面板拒绝是造物主已
// 决策过的,不再提供放行)。
const (
	DenyLineHarm         = "harm"          // 数据销毁/拒绝服务类(含危害声明非法的 fail-safe)
	DenyLineRoeScope     = "roe_scope"     // 超 RoE 授权范围(工具边界)
	DenyLineLevelInvalid = "level_invalid" // 意图分级缺失/非法(fail-safe)
	DenyLineNoGate       = "no_gate"       // 需弹门但无门可用(fail-safe)
	DenyLinePanel        = "panel"         // 面板对弹门请求的拒绝
	DenyLineCloseout     = "closeout"      // 等待审批期间任务收尾/熔断,按拒绝结案
	DenyLineHardRule     = "hard_rule"     // 全局内置硬规则命中(P2-08e,参数级确定性正则)
	DenyLineIntentRule   = "intent_rule"   // 任务级拦截命中/超额(P2-08e)
)

var denyLines = map[string]bool{
	DenyLineHarm:         true,
	DenyLineRoeScope:     true,
	DenyLineLevelInvalid: true,
	DenyLineNoGate:       true,
	DenyLinePanel:        true,
	DenyLineCloseout:     true,
	DenyLineHardRule:     true,
	DenyLineIntentRule:   true,
}

// IsValidDenyLine 拒绝线类别是否在词表内。
func IsValidDenyLine(s string) bool { return denyLines[s] }

// ApprovalRecord 是 EventApprovalRecord 的负载(P1-07,第四章 §3;
// P2-06b 三级处置 v2):审批门的完整审计流——弹门请求(requested)、
// 面板裁决(approved/denied)、拒绝线自动拒绝与 fail-safe 拒绝(均记
// denied 并附 DenyLine 与原因)、研判后放行(forced)。不进 nodes/edges
// 投影(审计流,同 swarm.record 模式),查询走事件流。Intent 与 ToolArgs
// 同时落账:审批界面展示"意图声明+实际调用参数"供人核对(人即兜底),
// 账本保留同一份依据——forced 放行的执行正是重放这份参数。
// WaitMillis 是该请求从登记到裁决的等待时长(弹门等待期单位计时暂停
// 的时间账,approved/denied 时携带)。
type ApprovalRecord struct {
	RequestID   string          `json:"request_id"`
	StepID      string          `json:"step_id"`
	UnitID      string          `json:"unit_id"`
	Type        string          `json:"type"`
	ActionLevel ActionLevel     `json:"action_level"`         // 触发本次审批判定的意图分级
	HarmClass   HarmClass       `json:"harm_class,omitempty"` // 拒绝线的危害声明(数据销毁/拒绝服务类)
	Intent      string          `json:"intent,omitempty"`     // 意图声明(Step 标题)
	ToolName    string          `json:"tool_name"`            // 实际调用的工具
	ToolArgs    json.RawMessage `json:"tool_args,omitempty"`  // 实际调用参数(人即兜底的核对物)
	Note        string          `json:"note,omitempty"`       // 弹门上下文(如拦截规则命中转人审的原因)
	DenyLine    string          `json:"deny_line,omitempty"`  // denied 时的拒绝线类别
	DecidedBy   string          `json:"decided_by,omitempty"` // 裁决者(approved/denied/forced 时)
	Reason      string          `json:"reason,omitempty"`     // 拒绝原因/批准备注
	WaitMillis  int64           `json:"wait_millis,omitempty"`
}

// 拦截判定记录类型白名单(P2-08e,受控入口)。
const (
	GuardRecQuotaCount = "quota_count" // 量化额度计数:任务级规则允许层放行一次,计第 Nth 次
)

var guardRecordTypes = map[string]bool{
	GuardRecQuotaCount: true,
}

// GuardRecord 是 EventGuardRecord 的负载(P2-08e,第四章 §3 v2.1 判定链
// ②③):拦截判定的审计与计数流。拒绝判定走 approval.record(denied +
// DenyLine=hard_rule/intent_rule),本事件承载放行侧的量化额度计数——
// "次数计数走账本、超额自动转禁止"的账面事实。不进 nodes/edges 投影
// (审计流,同 approval.record),查询走事件流。
type GuardRecord struct {
	StepID   string `json:"step_id"`
	UnitID   string `json:"unit_id"`
	Type     string `json:"type"`
	RuleID   string `json:"rule_id"`             // 命中的任务级规则 ID
	Action   string `json:"action,omitempty"`    // 归类出的动作类型
	Nth      int    `json:"nth"`                 // 第几次(1 起;配额判定的账面序号)
	ToolName string `json:"tool_name,omitempty"` // 实际放行的工具
	Note     string `json:"note,omitempty"`
}

// 报告存档记录类型白名单(受控入口,P3-02)。
const ReportRecArchived = "archived" // 报告存档写入(渲染原文入 blob,登记入账本)

var reportRecordTypes = map[string]bool{ReportRecArchived: true}

// ReportRecord 是 EventReportRecord 的负载(P3-02,第五章报告 × 流量证据
// 绑定三机制):报告存档的登记流——报告 md 是确定性渲染(R17),存档 =
// 某一账本状态的报告快照,原文入内容寻址存储(R18),本事件承载登记与
// 版本锚点。不进 nodes/edges 投影(审计流,同 swarm.record),查询走
// 事件流。EvidenceVersion 是写入时的任务证据版本(乐观锁锚点:evidence
// 绑定/收割定稿会推进它,旧版本的存档写入被写入口以 409 拒绝;存档后
// 证据再更新即推导为"待更新")。LedgerRev 是渲染所读的任务账本版本号。
type ReportRecord struct {
	Type            string   `json:"type"`                   // archived
	Format          string   `json:"format"`                 // 存档格式(md)
	Ref             string   `json:"ref"`                    // 报告原文的内容寻址引用(R18)
	EvidenceVersion int64    `json:"evidence_version"`       // 写入时的任务证据版本
	LedgerRev       int64    `json:"ledger_rev"`             // 渲染所读的任务账本版本号
	ClaimsScope     []string `json:"claims_scope,omitempty"` // 存档范围(空=全部发现)
	Size            int64    `json:"size"`                   // 原文字节数
	By              string   `json:"by,omitempty"`           // 存档者(面板 = creator)
}

// SessionMeta 是 EventSessionMeta 的负载(P3-13b ② 会话管理):面板对
// 会话的组织信息记账——重命名(自定义名称,默认名自动)、备注、置顶。
// 与 task.meta 同分工思想:生命周期/业务事件不触碰会话呈现名,本事件
// 只管列表组织信息;不进 nodes/edges 投影,读取侧扫事件流投影。指针
// 字段 = 本次要改的项(nil 不动),空字符串名称/备注 = 清除(回默认)。
// SessionID 是会话锚:虫群单位与牧者为转录 ID(swarm-* / shepherd-*),
// 主宰面板会话用稳定键 "overmind"(插话目标键同源)——跨进程不变。
type SessionMeta struct {
	SessionID string  `json:"session_id"`
	Title     *string `json:"title,omitempty"` // 重命名(nil 不动;空串 = 回默认名)
	Note      *string `json:"note,omitempty"`  // 备注(nil 不动;空串 = 清除)
	Pinned    *bool   `json:"pinned,omitempty"`
}

// ArbiterVerdict 是 EventArbiterVerdict 的负载(P1-06,第三章 §6):
// 仲裁者对 Goal 完成判定的裁决——机器纯函数对冻结谓词求值的结果,
// 含逐条件证据计数。不进 nodes/edges 投影(审计流,同 swarm.record);
// Goal 的状态迁移走 node.state_changed(由裁决触发),本事件承载判定依据。
// 谓词本体存于 Goal 创建事件负载(detail.predicate),创建时冻结。
type ArbiterVerdict struct {
	GoalID       string                   `json:"goal_id"`
	Op           string                   `json:"op"` // all_of|any_of(冻结谓词的组合子)
	Satisfied    bool                     `json:"satisfied"`
	EvaluatedRev int64                    `json:"evaluated_rev"` // 求值时的账本版本(审计锚点)
	Conditions   []ArbiterConditionResult `json:"conditions"`
}

// TestClaim 记录类型白名单(受控入口,P1-08 + P2-02 + P3-02 + P3-06)。
const (
	TestClaimRecFalsify      = "falsify_result" // 反证单位经 submit_falsify_result 写回的结构化反证结论
	TestClaimRecVerdict      = "verdict"        // 仲裁者对声明的轮末评估/终审裁决(机器纯函数求值)
	TestClaimRecReopen       = "reopen"         // P2-02:重开(结论可反驳/过期;追加事件,历史不删)
	TestClaimRecRepro        = "repro"          // P2-02:收割定稿(复现步骤 + 严重级别,确认裁决同批落)
	TestClaimRecEvidence     = "evidence"       // P3-02:流量证据绑定/补绑(镜交换 ↔ 声明,面板操作入账)
	TestClaimRecRetest       = "retest"         // P3-06:复测结论(已修复/未修复,复测会话受限工具写回)
	TestClaimRecReplay       = "replay"         // P3-07:重放校验结论(绑定证据确定性重放,零 LLM)
	TestClaimRecReportReview = "report_review"  // P4-30:存量复核标记(信息性判断不纳入报告;追加事件,账本不涂改,判断保留可追溯)
	TestClaimRecSuspend      = "suspend"        // P4-54:操作者取消挂起(停方向聚焦取消的反证:声明回挂起、理由随行;重新聚焦经 reopen 唤醒。P4-55 审核迭代起目标删除不再程序挂起,改交主宰裁决)
	TestClaimRecDiscard      = "discard"        // P4-55 审核迭代:程序确定性废弃(摘除资产连坐:理由"目标已摘除";主宰裁决放弃同用本记录)。终态只追加,历史不涂改
)

var testClaimRecordTypes = map[string]bool{
	TestClaimRecFalsify:      true,
	TestClaimRecVerdict:      true,
	TestClaimRecReopen:       true,
	TestClaimRecRepro:        true,
	TestClaimRecEvidence:     true,
	TestClaimRecRetest:       true,
	TestClaimRecReplay:       true,
	TestClaimRecReportReview: true,
	TestClaimRecSuspend:      true,
	TestClaimRecDiscard:      true,
}

// 复测结论词表(P3-06,第三章 §5 复测语义):fixed=已修复(本轮真实重放
// 显示漏洞不再可触发);unfixed=未修复(漏洞仍可触发或未取得已修复
// 证据);inconclusive=未能定论(只入记录,不迁移状态——诚实呈现,
// 与 R4 待确认同精神)。词汇表归 rings,写回入口与读侧共用。
const (
	RetestVerdictFixed        = "fixed"
	RetestVerdictUnfixed      = "unfixed"
	RetestVerdictInconclusive = "inconclusive"
)

var retestVerdicts = map[string]bool{
	RetestVerdictFixed:        true,
	RetestVerdictUnfixed:      true,
	RetestVerdictInconclusive: true,
}

// IsValidRetestVerdict 复测结论是否在词表内。
func IsValidRetestVerdict(v string) bool { return retestVerdicts[v] }

// 重放校验结论词表(P3-07,第三章 §5 证据复验):reproduced=重放一致
// (全部绑定交换的原响应原样再现——确定性逐字节比对,发现按原样仍可
// 复现);diverged=重放有变化(至少一条交换的响应已变化——发现可能
// 已不在,交操作者研判,是否已修复的判定归复测);unreachable=重放
// 不可达(全部交换的请求失败——目标不可达,如实呈现)。重放校验零
// LLM、不迁移声明状态(确定性比对不冒充判定;修复状态轴归复测会话)。
const (
	ReplayVerdictReproduced  = "reproduced"
	ReplayVerdictDiverged    = "diverged"
	ReplayVerdictUnreachable = "unreachable"
)

var replayVerdicts = map[string]bool{
	ReplayVerdictReproduced:  true,
	ReplayVerdictDiverged:    true,
	ReplayVerdictUnreachable: true,
}

// IsValidReplayVerdict 重放校验结论是否在词表内。
func IsValidReplayVerdict(v string) bool { return replayVerdicts[v] }

// ReplayResult 重放校验的单条交换结果(P3-07):被重放的原始绑定交换、
// 本次重放产生的新交换(镜留痕,归因重放锚,流量页可回放)、双侧响应
// 事实(状态/字节数/响应体是否逐字节一致)与失败原因。事实齐备,结论
// 由读侧按词表呈现——确定性比对不隐藏任何一侧的原样。
type ReplayResult struct {
	ExchangeID    string `json:"exchange_id"`               // 被重放的原始绑定交换 ID
	NewExchangeID string `json:"new_exchange_id,omitempty"` // 本次重放的新交换(失败时缺省)
	Method        string `json:"method,omitempty"`
	URL           string `json:"url,omitempty"`
	OldStatus     int    `json:"old_status,omitempty"`   // 原交换响应状态(无响应=0)
	NewStatus     int    `json:"new_status,omitempty"`   // 重放响应状态(请求失败=0)
	BodySame      bool   `json:"body_same,omitempty"`    // 双侧响应体逐字节一致(双侧均有响应时才有意义)
	HeadersSame   bool   `json:"headers_same,omitempty"` // 双侧响应头一致(剔 Date 类必变头后逐条比对,P3-14b ②)
	OldBytes      int64  `json:"old_bytes,omitempty"`
	NewBytes      int64  `json:"new_bytes,omitempty"`
	Error         string `json:"error,omitempty"` // 重放请求失败原因(网络/超时)
}

// verdict 裁决 Decision 值域(P2-02;P1-08 旧数据无 Decision,按
// Satisfied 推导:satisfied→confirm、否则→pending)。P4-55 ② 增
// refuted:终审不成立(存在性被反证推翻;Satisfied 必为 false,理由
// "被反证推翻"必填——非 confirm 裁决搁置理由必填的同一物理强制)。
const (
	VerdictConfirm      = "confirm"       // 终审确认(Satisfied 必为 true)
	VerdictRefuted      = "refuted"       // 终审不成立(P4-55 ②:存在性被反证推翻,理由"被反证推翻")
	VerdictPending      = "pending"       // 终审待确认(R4 搁置理由必填)
	VerdictAdvanceRound = "advance_round" // 轮末评估:解释未全灭且轮次未尽,待派下一轮反证
)

// reopen 触发词表(P2-02,重开机制三语义:结论可反驳/重开/过期)。
const (
	ReopenNewEvidence = "new_evidence" // 新反证/新证据推翻旧结论(结论可反驳)
	ReopenManual      = "manual"       // 造物主/主宰显式重开(面板反驳)
	ReopenExpired     = "expired"      // 结论过期(环境/时效已变,须复核)
)

var reopenTriggers = map[string]bool{
	ReopenNewEvidence: true, ReopenManual: true, ReopenExpired: true,
}

// 影响面定级词表(P2-02,第五章 §1:级别 critical/high/medium/low,
// 跟影响面证据走——只由反证轮的结构化影响面证据携带,发现者无级别通道)。
var impactLevels = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}

// Alternative 竞争性替代解释(第三章 §5:逐条列出,作为待检验反面假设
// 挂在声明下)。P2-02 在声明创建时冻结进 detail.alternatives;反证轮经
// AltResult 逐条写回排除结论,仲裁者只认结构化 Refuted(契约④)。
type Alternative struct {
	ID    string `json:"id"`    // 稳定标识(alt1、alt2…),反证写回按 ID 引用
	Label string `json:"label"` // 一句话反面假设(如"缓存污染:内容可能来自中间缓存")
}

// AltResult 反证轮对单条竞争解释的结构化结论(Refuted=true 即本轮排除)。
type AltResult struct {
	ID      string `json:"id"`
	Refuted bool   `json:"refuted"`
	// Label 展示侧回填的疑点内容:账本写回只带 ID,报告/面板装配时
	// 按冻结清单补(界面显示内容本身,不显示 alt 编号),不落账。
	Label string `json:"label,omitempty"`
}

// ReproStep 收割定稿的单条复现步骤(P2-02,第五章 §1+P2-02 任务书):
// 操作方法(工具与参数)+ 一句话描述 + HTTP 请求/响应原文的镜留痕引用
// (Burp 可验证数据包,完整原文经 blob 回放,R18)。确定性组装自反证
// 战役的镜留痕;P3-05b ① 起只收目标流量——请求 host 在任务授权范围
// (种子资产)内的交换才入步骤,外网查证流量不进(流量页照常全量可查)。
// P4-08 ③ 起组装侧去同型:同方法+同路径+同状态的重复交换只保留构成
// 复现流程的一条(该型首条成功交换,无成功则首条),其余进 Variants
// (变体流量不进复现步骤,证据包 zip 全量收,审计不丢)。P4-08 ④ 起
// Desc 为定稿时模型生成的指令式人话(冻结入账本;生成失败回退确定性
// 描述),报告导出与面板渲染零 LLM(R17 不破)。
// P4-35 起最小复现集口径(问题清单 #57 ①):去同型结果只是候选池,
// 定稿时模型从中挑出构成复现流程的必要步骤(对比交换/反证变体/枚举
// 试错一条不进面板),未选中的交换只留在 Process 引用清单(全量在
// 仓库与证据包)。
type ReproStep struct {
	Seq     int    `json:"seq,omitempty"`
	Round   int    `json:"round,omitempty"` // 所属反证轮次
	StepID  string `json:"step_id"`         // 反证 Step(归因)
	UnitID  string `json:"unit_id"`
	Op      string `json:"op,omitempty"`     // 操作方法:发起本步的工具与参数(证据包收,面板/报告正文不显;P4-35 起总量封顶,完整参数在 swarm.record 审计流)
	Desc    string `json:"desc,omitempty"`   // 做什么(指令式人话,P4-08 ④;旧记录为确定性描述或缺省)
	Method  string `json:"method,omitempty"` // HTTP 方法
	URL     string `json:"url,omitempty"`
	Status  int    `json:"status,omitempty"`
	BlobRef string `json:"blob_ref,omitempty"` // 交换原文(请求/响应)的内容寻址引用
	// ExchangeID 镜交换 ID(P3-02 审核迭代:复现数据包按请求包/返回包分
	// 侧回放与复制,面板经它走流量页同一分段端点;旧记录缺省,面板退回
	// 预渲染原文)。
	ExchangeID string `json:"exchange_id,omitempty"`
	Note       string `json:"note,omitempty"`
	// Short 步骤短名(P4-35 ②,"① 拿会话凭据"式的标题段,≤8 字):定稿
	// 时模型生成,与 Desc 同批同规矩(失败回退确定性短名);旧记录无本
	// 字段,读侧退"步骤 N"。
	Short string `json:"short,omitempty"`
	// Purpose 该步在复现里的作用(P4-35 ②,"作用:为什么"行):定稿时
	// 模型生成;旧记录无本字段,读侧省略该行。
	Purpose string `json:"purpose,omitempty"`
	// Unpolished 该步描述未过文风校验(P4-24 ②,问题清单 #39):模式
	// 闸门拦截后退回重写一次仍含机器语特征,如实标注原文保留(读侧
	// 渲染一行说明,不阻塞定稿)。旧记录无本字段。
	Unpolished bool `json:"unpolished,omitempty"`
	// Source 步骤原料来源(P4-62 C(b),问题清单 #83):空 = 镜留痕交换
	// (既有口径);"tool" = 由工具调用记录确定性组包(直连旁路流量,镜
	// 交换缺失时按工具参数/命令重构请求包,响应侧按工具结果还原或如实
	// 缺失)。读侧渲染同源(blob 文档同构)。
	Source string `json:"source,omitempty"`
}

// ReproProcessRef 过程流量清单条目(P4-35 ④,问题清单 #57):引用形态,
// 不内嵌原文——按编号从内容寻址仓库取回,字节一致(R18)。
type ReproProcessRef struct {
	ExchangeID string `json:"exchange_id"`
	BlobRef    string `json:"blob_ref"`
}

// TestClaimRecord 是 EventTestClaimRecord 的负载(P1-08 + P2-02,第三章
// §5/§6):TestClaim 状态机的审计流,不进 nodes/edges 投影(状态迁移本身
// 走 node.state_changed;本事件承载状态机依据,同 arbiter.verdict 分工)。
// 四类记录:
//   - falsify_result(actor=swarm:<unit>):反证单位写回的单轮结构化结论。
//     存在性与影响面分开反证(第三章 §5 定稿):两个布尔各自独立;
//     P2-02 起每轮携带逐条竞争解释的排除结论(Alternatives)与影响面
//     定级(ImpactLevel,impact_supported=true 时必填——级别跟影响面
//     证据走)。证据经内容寻址落 blob(R18),账本只存引用与截头。
//   - verdict(actor=arbiter):仲裁者按声明创建时冻结的判定标准求值的
//     裁决。Decision=confirm 即 Confirmed(Satisfied 必真);pending 即
//     待确认;advance_round 即待下轮反证(解释未全灭且轮次未尽)。
//     非 confirm 的裁决 PendingReason 必填(R4:账本受控入口物理强制)。
//   - reopen(P2-02):重开记录——Confirmed/待确认被新反证/人工/过期
//     重开回反证中;Trigger 与 Reason 必填,历史永不删除(R1)。
//   - repro(P2-02,actor=overmind):收割定稿——确认裁决同批落库的
//     复现步骤与严重级别(Severity=末轮反证影响面定级,发现者无级别
//     通道);重开后再确认会落新 repro,旧记录自然过期(取代语义,
//     读取侧取最新)。
//   - evidence(P3-02):流量证据绑定/补绑(见 evidence 侧字段注释)。
//   - retest(P3-06,actor=swarm:rt-*):复测会话经受限工具写回的结论
//     (fixed|unfixed|inconclusive)+ 本轮证据;fixed/unfixed 同批迁移
//     声明状态(锤炼态与修复态正交:fixed 是已确认发现之上的修复标记,
//     未修复复测回滚到修复前状态,历史不删,R1)。
//   - replay(P3-07,actor=swarm:rp-*):重放校验结论——对绑定流量证据
//     做确定性原样重放(经镜新留痕、零 LLM),逐条交换的新旧对照事实
//     与结论词(reproduced|diverged|unreachable)入账;不迁移状态。
//   - report_review(P4-30,actor=overmind):存量复核标记——主宰按立判
//     危害门槛复核图上既有判断,纯信息性的标记"不纳入报告"。只追加
//     标记记录:账本不涂改、判断保留可追溯(Reason 必填);报告只收
//     已确认且非信息性,读取侧按最新标记取舍(取代语义)。
type TestClaimRecord struct {
	ClaimID string `json:"claim_id"`
	StepID  string `json:"step_id,omitempty"` // 所属/依据的反证 Step;retest = 本轮复测会话锚(伪步骤 ID,兼转录名)
	UnitID  string `json:"unit_id,omitempty"` // 反证单位(falsify_result 必填);retest = 复测单位(rt-*)
	Type    string `json:"type"`

	// falsify_result 侧:结构化反证结论(契约④:仲裁者只读布尔、解释
	// 排除结论与定级,Note/EvidenceHead 文本不进求值)。
	Round            int         `json:"round,omitempty"` // 反证轮次(受控入口从 step.detail.round 机器填写)
	ExistenceRefuted bool        `json:"existence_refuted,omitempty"`
	ImpactSupported  bool        `json:"impact_supported,omitempty"`
	ImpactLevel      string      `json:"impact_level,omitempty"` // critical|high|medium|low(impact_supported=true 时必填)
	Alternatives     []AltResult `json:"alternatives,omitempty"` // 本轮逐条竞争解释排除结论(ID 引用冻结清单)
	EvidenceRef      string      `json:"evidence_ref,omitempty"`
	EvidenceHead     string      `json:"evidence_head,omitempty"`
	Note             string      `json:"note,omitempty"`

	// verdict 侧。
	Decision      string                   `json:"decision,omitempty"` // confirm|refuted|pending|advance_round(P1-08 旧数据缺省,按 Satisfied 推导;refuted=P4-55 ② 不成立)
	Satisfied     bool                     `json:"satisfied,omitempty"`
	PendingReason string                   `json:"pending_reason,omitempty"` // 非 confirm 裁决的搁置/推翻/推进理由(R4,必填;refuted 为"被反证推翻")
	Conditions    []ArbiterConditionResult `json:"conditions,omitempty"`     // 逐条件依据(R2 可审计)
	EvaluatedRev  int64                    `json:"evaluated_rev,omitempty"`

	// reopen 侧(P2-02);report_review 侧(P4-30:复核理由,必填)。
	Trigger string `json:"trigger,omitempty"` // new_evidence|manual|expired
	Reason  string `json:"reason,omitempty"`

	// repro 侧(P2-02,收割定稿)。
	Severity string `json:"severity,omitempty"` // critical|high|medium|low(=末轮影响面定级)
	// Narrative 来龙去脉叙述(P4-13,问题清单 #23):确认定稿时主宰模型
	// 基于链上节点与证据原文写的一段自然叙述(在哪发现的来源载体/谁发现
	// 的/怎么验证/怎么确认),随确认裁决同批冻结入账;报告导出与面板渲染
	// 零 LLM(R17);生成失败回退确定性拼句(不阻塞机器裁决);重开再确认
	// 随新定稿重新生成(取代语义同 Steps)。叙述是包装不是证据,与机器
	// 字段矛盾时以原文为准。旧定稿无本字段,读侧如实标注不编造。
	Narrative string `json:"narrative,omitempty"`
	// Statement / Impact / Facts 定稿校对后的声明摘要(P4-59 ①,问题清单
	// #80①③):确认定稿时主宰模型按报告级文风锚改写——Statement 存在性
	// 描述(一句话本质+影响对象,复杂类加"就是…从而…"通俗原理句)、
	// Impact 影响面(直接说能拿到什么/能干什么+数字精确实际值)、Facts
	// 链上事实描述(derived_from 事实与发现步骤产出的事实)。只改文风,
	// 节点原文一字不动(机器判定与审计仍读节点;包装不是证据,与机器
	// 字段矛盾以原文为准)。空 = 未生成或未过闸(读侧退节点原文,即生成
	// 失败的回退不阻塞);重开再确认随新定稿重新生成(取代语义同上)。
	Statement string          `json:"statement,omitempty"`
	Impact    string          `json:"impact,omitempty"`
	Facts     []FactStatement `json:"facts,omitempty"`
	// Rounds 反证时间线人话(P4-56 ⑤,问题清单 #80②):确认定稿时主宰
	// 模型对当前战役每轮反证写的"疑点→怎么验的→结论"三段人话,随 repro
	// 同批冻结;机器原文(替代解释标签/证据截头/结构化结论)由读侧折叠
	// 呈现,默认只展示人话层。失败回退确定性拼句;重开再确认随新定稿
	// 重新生成。旧定稿无本字段,读侧只呈现机器层(如实,不编造)。
	Rounds []FalsifyRoundHuman `json:"rounds,omitempty"`
	// NarrativeUnpolished 叙述未过文风校验(P4-24 ②,问题清单 #39):
	// 定稿校对+模式闸门重写一次后仍含机器语特征,如实标注原文保留
	// (读侧渲染一行说明,不阻塞定稿)。旧记录无本字段。
	NarrativeUnpolished bool        `json:"narrative_unpolished,omitempty"`
	Steps               []ReproStep `json:"steps,omitempty"`
	// NoReproReason 无复现包的原因(P4-62 A,问题清单 #83:兜底直出废除,
	// 复现区只呈现模型挑选过的最小集):非空 = 本次定稿未生成复现步骤,
	// 原因句冻结入账,面板与报告如实呈现(读侧零 LLM,R17)。两种来源:
	// 候选池无可用包(含池空与模型空选,确定性原因句或模型理由)与挑选
	// 失败(重试一次后仍失败)。旧记录无本字段(旧口径候选池直出)。空 =
	// 正常定稿(Steps 为模型挑选结果)。
	NoReproReason string `json:"no_repro_reason,omitempty"`
	// Variants 去同型时被收起的变体交换(P4-08 ③):同方法+同路径+同
	// 状态的重复交换不构成复现流程,不进 Steps;账本记录保留(证据包
	// zip 全量收,审计不丢)。账次序与原交换次序一致,不编 Seq。
	// P4-35 ④ 起新定稿不再填充(变体 Op 随步骤全量内嵌曾把单条记录
	// 撑到百 MB 级,问题清单 #57 ②):过程流量改记 Process 引用清单;
	// 字段保留以解码旧记录,读侧(证据包导出)新旧同收。
	Variants []ReproStep `json:"variants,omitempty"`
	// Process 过程流量清单(P4-35 ①④,问题清单 #57):定稿战役范围内
	// 全部交换的引用(账次序,仅交换 ID 与内容寻址引用——请求/响应
	// 原文大块头一律在仓库,数据库不内嵌,第六章 R18 精神)。对比交换、
	// 反证变体、枚举试错不进面板复现步骤,但全量在此留档,证据包导出
	// 照旧全量(审计不丢)。旧定稿记录无本字段(过程流量在 Variants)。
	Process []ReproProcessRef `json:"process,omitempty"`

	// evidence 侧(P3-02,绑定/补绑):本次追加绑定的镜交换 ID。绑定与
	// 补绑是同一操作类型——声明已有证据再绑即补绑,读取侧按事件累计。
	// 每个交换必须是本任务账本内已有的 http/https 留痕(有 blob 原文可
	// 回放),账本入口物理校验(见 validateTestClaimRecord)。
	Exchanges []string `json:"exchanges,omitempty"`
	By        string   `json:"by,omitempty"` // 绑定者(面板 = creator)

	// retest 侧(P3-06,复测结论):RetestVerdict 受限词汇
	// fixed|unfixed|inconclusive——结论只写回会话绑定的声明(工具参数
	// 无声明 ID,服务端定死防串号);fixed/unfixed 随记录同批迁移声明
	// 状态(fixed / 回滚到修复前状态),inconclusive 只入记录不动状态。
	RetestVerdict string `json:"retest_verdict,omitempty"`

	// replay 侧(P3-07,重放校验):ReplayVerdict 受限词汇
	// reproduced|diverged|unreachable——对声明的绑定流量证据做确定性
	// 原样重放(经镜新留痕),结论与逐条交换事实(ReplayResults)入账;
	// 不迁移声明状态(修复状态轴归复测,重放只陈述"原样重放后响应是否
	// 一致")。StepID = 本轮重放锚(伪步骤 ID,兼镜归因键)。
	ReplayVerdict string         `json:"replay_verdict,omitempty"`
	ReplayResults []ReplayResult `json:"replay_results,omitempty"`
}

// FactStatement 定稿校对后的事实描述(P4-59 ①):FactID 指向链上事实
// 节点,Statement 为报告级文风改写(只改文风,节点原文不动;空 = 该条
// 未生成或未过闸,读侧退节点标题)。
type FactStatement struct {
	FactID    string `json:"fact_id"`
	Statement string `json:"statement,omitempty"`
}

// FalsifyRoundHuman 一轮反证的人话三段(P4-56 ⑤):疑点 = 该轮待排除
// 的其他可能原因(疑问句形态)、怎么验的 = 验证过程人话、结论 = 该轮
// 结论词(已排除/未排除/推翻)。定稿时模型生成冻结;Doubt/How/Conclusion
// 为空表示该轮无人话(读侧退机器层如实呈现)。
type FalsifyRoundHuman struct {
	Round      int    `json:"round"`
	Doubt      string `json:"doubt,omitempty"`
	How        string `json:"how,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	// Unpolished 该轮人话未过文风校验(P4-24 ② 同款如实标注,原文保留)。
	Unpolished bool `json:"unpolished,omitempty"`
}

// ClaimRoundHuman 是 EventClaimRoundHuman 的负载(P4-64 ①,问题清单
// #82):反证轮人话三段的逐轮冻结——每轮反证结构化结论(testclaim.record
// falsify_result)入账时同链生成,与机器记录同批落账(生成失败回退
// 确定性拼句,不阻塞轮次入账);重开后新轮次增量补(轮号全局续号,
// 投影按轮 upsert)。机器记录原文一字不动;三段空 = 未生成(读侧退
// 机器层,存量旧账本无本事件同退)。
type ClaimRoundHuman struct {
	ClaimID    string `json:"claim_id"`
	Round      int    `json:"round"`
	Doubt      string `json:"doubt,omitempty"`
	How        string `json:"how,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	Unpolished bool   `json:"unpolished,omitempty"`
}

// ClaimSummaryPolish 是 EventClaimSummaryPol 的负载(P4-64 ②):声明
// 摘要的立账即生成——收割立判断批次(候选声明派反证时)按报告级文风
// 锚生成(同 genClaimSummary 管线:存在性描述/影响面/链上事实描述),
// 锤炼中(未定稿)声明读侧即有摘要;确认定稿随新定稿重新生成(repro
// 记录内嵌字段,投影同列覆盖,取代语义既有)。空字段 = 未生成,读侧
// 退节点原文。
type ClaimSummaryPolish struct {
	ClaimID   string          `json:"claim_id"`
	Statement string          `json:"statement,omitempty"`
	Impact    string          `json:"impact,omitempty"`
	Facts     []FactStatement `json:"facts,omitempty"`
}

// FactTextPolish 是 EventFactText 的负载(P4-64 ③⑥,问题清单 #82):
// 事实的显示层包装——收割确认批次生成(不限于已定稿判断链上的事实),
// Title 为短句标准校对的标题(术语门槛+完整可读资产标识+机器语闸门,
// 不套报告级四段锚),Statement 为报告级描述口径的一句话描述。节点
// 原文一字不动;空字段 = 未生成(读侧退节点标题)。
type FactTextPolish struct {
	FactID    string `json:"fact_id"`
	Title     string `json:"title,omitempty"`
	Statement string `json:"statement,omitempty"`
	// Unpolished 标题未过机器语闸门(P4-24 ② 同款如实标注,原文保留)。
	Unpolished bool `json:"unpolished,omitempty"`
}

// HypoTextPolish 是 EventHypoText 的负载(P4-64 ④⑥⑧,问题清单 #82):
// 假设的显示层包装——收割评估批次(evaluateHypotheses)对每条被评估
// 假设同批改写冻结:Title 短句标准校对标题、Basis 依据改写、Reason
// 本次评估理由校对。未评估(等待验证瞬时态)不生成,读侧保持节点与
// 评估记录原文。Latest-wins:每次评估的新事件覆盖旧包装(随评估记录
// 同批落账,不重复记账)。
type HypoTextPolish struct {
	HypothesisID string `json:"hypothesis_id"`
	Title        string `json:"title,omitempty"`
	Basis        string `json:"basis,omitempty"`
	Reason       string `json:"reason,omitempty"`
	// Unpolished 标题未过机器语闸门(P4-24 ② 同款如实标注,原文保留)。
	Unpolished bool `json:"unpolished,omitempty"`
}

// RetestNotePolish 是 EventClaimRetextNote 的负载(P4-64 ⑦,问题清单
// #82):复测结论备注的校对稿——复测结论入账时过既有校对通道(随
// retest 记录同批落账)。机器记录 Note 原文一字不动;按轮匹配
// (ClaimID+Round),空 = 未生成(读侧退机器记录原文)。
type RetestNotePolish struct {
	ClaimID string `json:"claim_id"`
	Round   int    `json:"round"`
	Note    string `json:"note,omitempty"`
}

// OvermindPolish 是 EventOvermindPolish 的负载(P4-65 ⑨,问题清单 #82):
// 研判记录(overmind.record assessment)五问与叙述的显示层校对稿——研判
// 记录落账后异步补写(研判轮与派发不等校对;校对稿就绪前读侧显示原文,
// 读侧优先链既有语义)。RecRev 锚定研判记录的事件版本(逐轮匹配,研判
// 记录不可变故无取代语义);机器记录原文一字不动(会话原文下钻走原文
// 域)。字段空 = 该问未生成校对稿(读侧退研判记录原文)。
type OvermindPolish struct {
	RecRev       int64  `json:"rec_rev"` // 锚定的研判记录事件版本
	QAccess      string `json:"q_access,omitempty"`
	QPrivilege   string `json:"q_privilege,omitempty"`
	QCombination string `json:"q_combination,omitempty"`
	QUnlock      string `json:"q_unlock,omitempty"`
	QOverturn    string `json:"q_overturn,omitempty"`
	Note         string `json:"note,omitempty"`
}

// StepTitlePolish 是 EventStepTitle 的负载(P4-65 ⑩,问题清单 #82):步骤
// 意图标题的显示层校对稿——拆解派发落账后异步补写(派发不等校对;任务
// 包与机器消费用节点原文,校对稿仅显示层,与声明摘要同款包装分离)。步
// 骤节点 Title 一字不动;空 = 未生成(读侧退节点标题)。
type StepTitlePolish struct {
	StepID string `json:"step_id"`
	Title  string `json:"title"`
}

// 镜留痕记录类型白名单(受控入口,P2-01;P3-01 增 https)。
const (
	MirrorRecHTTP   = "http"   // HTTP 交换全量留痕:完整请求/响应原文在 blob(R18)
	MirrorRecHTTPS  = "https"  // HTTPS 交换解密留痕(P3-01 CA MITM):与 http 同粒度,原文在 blob,目标证书校验结论如实标注
	MirrorRecTunnel = "tunnel" // CONNECT 隧道透传:仅元数据留痕(非 TLS 协议兜底 / 解密接管失败,如实区分)
)

var mirrorRecordTypes = map[string]bool{
	MirrorRecHTTP:   true,
	MirrorRecHTTPS:  true,
	MirrorRecTunnel: true,
}

// MirrorRecord 是 EventMirrorRecord 的负载(P2-01,第一章"全程流量经镜"):
// 镜对一次工具流量的留痕。不进 nodes/edges 投影(审计流,同 swarm.record;
// 流量页 UI 属 [P3])。归因字段(Step/Unit/Transcript)把流量钉回图上的
// 执行记录——审计时可从 Step 一路下钻到 HTTP 原文。完整交换文档
// (请求行/头/体 + 响应行/头/体,Burp 可核对级保真)经内容寻址入 blob
// (R18:流量体一律走文件存储,账本只存引用与摘要字段)。
type MirrorRecord struct {
	ExchangeID   string `json:"exchange_id"`
	Type         string `json:"type"` // http | tunnel(白名单见上)
	StepID       string `json:"step_id"`
	UnitID       string `json:"unit_id"`
	TranscriptID string `json:"transcript_id,omitempty"`

	// http 侧摘要(原文在 blob);tunnel 侧 URL=CONNECT 目标、字节为双向计数。
	Method    string `json:"method,omitempty"`
	URL       string `json:"url,omitempty"`
	Status    int    `json:"status,omitempty"`
	ReqBytes  int64  `json:"req_bytes,omitempty"`
	RespBytes int64  `json:"resp_bytes,omitempty"`
	BlobRef   string `json:"blob_ref,omitempty"` // http/https 全量留痕的交换文档引用(R18,必填)
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	Error     string `json:"error,omitempty"` // 转发失败等异常如实留痕(有失败记录才算全量可审计)
	TLS       string `json:"tls,omitempty"`   // https 侧目标证书校验结论 token(trusted/self_signed/expired/hostname_mismatch/untrusted/invalid,标签见 mirror.TLSLabel)
}

// Hypothesis 记录类型白名单(受控入口,P2-03)。
const (
	HypothesisRecEvaluation = "evaluation" // 主宰收割评估的裁决(转验证/挂账/废弃 + 理由)
	HypothesisRecResult     = "result"     // 验证单位经 submit_hypothesis_result 写回的结构化结论
)

var hypothesisRecordTypes = map[string]bool{
	HypothesisRecEvaluation: true,
	HypothesisRecResult:     true,
}

// 评估三去向(第四章 §5 定稿:值得跟 / 存疑 / 不成立)。
const (
	HypothesisVerify  = "verify"  // 值得跟:转 Step 派发验证(R12 不得派回原发现者)
	HypothesisHold    = "hold"    // 存疑:留在图上待唤醒(低排序 ≠ 拒绝)
	HypothesisDiscard = "discard" // 不成立:标记废弃 + 记录理由(不删除,R10)
)

var hypothesisDecisions = map[string]bool{
	HypothesisVerify: true, HypothesisHold: true, HypothesisDiscard: true,
}

// 验证结论词汇(结构化写回;Note 文本不构成结论)。
const (
	HypothesisSupported    = "supported"    // 成立
	HypothesisRefuted      = "refuted"      // 不成立(推翻)
	HypothesisInconclusive = "inconclusive" // 未能定论(回候选再评估)
)

var hypothesisVerdicts = map[string]bool{
	HypothesisSupported: true, HypothesisRefuted: true, HypothesisInconclusive: true,
}

// IsValidHypothesisVerdict 验证结论词汇判定(swarm/overmind/server 共用;
// 与状态词汇同惯例:词汇表归 rings,消费方不得自拼字符串)。
func IsValidHypothesisVerdict(v string) bool { return hypothesisVerdicts[v] }

// HypothesisRecord 是 EventHypothesisRecord 的负载(P2-03,第四章 §5
// 虫群直觉回流):假设回流轨迹的审计流,不进 nodes/edges 投影(状态迁移
// 本身走 node.state_changed;本事件承载裁决与结论依据,同 testclaim.record
// 分工)。两类记录:
//   - evaluation(actor=overmind):主宰收割评估——三去向裁决与理由
//     (挂账与废弃都是一等去向,理由必填:直觉有账、废案有理由,R10);
//     verify 携带派发的验证意图,StepID 指向同批落账的验证 Step。
//   - result(actor=swarm:<unit>):验证单位写回的单轮结构化结论
//     (supported|refuted|inconclusive);R12 验证者 ≠ 发现者由写回入口
//     校验;证据经内容寻址落 blob(R18),账本只存引用与截头;
//     Note 文本不参与结论。
type HypothesisRecord struct {
	HypothesisID string `json:"hypothesis_id"`
	StepID       string `json:"step_id,omitempty"` // evaluation=验证 Step;result=写回单位执行的验证 Step(必填)
	UnitID       string `json:"unit_id,omitempty"` // result 必填:写回的验证单位
	Type         string `json:"type"`

	// evaluation 侧。
	Decision     string `json:"decision,omitempty"`      // verify|hold|discard
	Reason       string `json:"reason,omitempty"`        // 三去向均必填(可审计的"为何跟/为何不跟")
	VerifyIntent string `json:"verify_intent,omitempty"` // verify:派发的验证意图(Step 标题)

	// result 侧。
	Verdict      string `json:"verdict,omitempty"` // supported|refuted|inconclusive
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	EvidenceHead string `json:"evidence_head,omitempty"`
	Note         string `json:"note,omitempty"`
}

// 黑匣子记录类型白名单(受控入口,P2-04,第四章 §6 账本分工:预算状态
// 变化与聚合里程碑入年轮黑匣子通道)。系统崩了它还在——写入用独立 ctx,
// 不随任务熔断断链(记录侧约定)。P2-05 扩入心跳巡检四类:巡检拍本身
// (看门狗活着)与三类机器预筛发现(第八章第 8 节防线④)。
const (
	BlackboxBudgetHalf       = "budget_half"        // 聚合里程碑:单位轮次预算过半
	BlackboxUnitExhausted    = "unit_exhausted"     // 单位预算耗尽落地(P4-01 ② 后仅剩任务总闸场景;R16 暂停接力,非失败)
	BlackboxBudgetRenewal    = "unit_budget_renew"  // P4-01 ②:预算到线,收尾写回,原会话续跑(预算重算)
	BlackboxTaskSettling     = "task_settling"      // 任务进入收尾(整体超时到点,停止派发)
	BlackboxGraceHardStop    = "grace_hard_stop"    // 收尾宽限超时,对在跑单位硬停
	BlackboxTaskSettled      = "task_settled"       // 收尾定终态(终局判定结果落定)
	BlackboxHeartbeat        = "heartbeat"          // P2-05:心跳巡检拍(无事也留痕,看门狗活性)
	BlackboxHbStall          = "hb_stall"           // P2-05:预筛发现——进度停滞(超时无事件)
	BlackboxHbRepeat         = "hb_repeat"          // P2-05:预筛发现——重复动作(连续相同调用)
	BlackboxHbBurnAnomaly    = "hb_burn_anomaly"    // P2-05:预筛发现——预算消耗异常(单拍 token 超阈)
	BlackboxUnitApprovalDeny = "unit_approval_deny" // P2-06b:审批拒绝熔断单位(拒绝线自动拒绝/面板拒绝)
	BlackboxUnitBlocked      = "unit_blocked"       // P2-06e:单位模型错误退避重跑耗尽,步骤受阻
	BlackboxTaskBlocked      = "task_blocked"       // P2-06e:任务级 LLM 连续失败超阈值,任务受阻
	BlackboxTaskResumed      = "task_resumed"       // P2-06e:任务受阻后模型调用恢复
	BlackboxRestartAdoption  = "restart_adoption"   // P2-06f:进程重启收养(幽灵清理/重拉/重准入的系统级审计锚点)
	BlackboxEnvObservation   = "env_observation"    // P4-25 ①:本机环回/测试环境自身设施的观察(镜自指排除),拒收原文留痕供排查
	BlackboxBGOutputTrunc    = "bg_output_trunc"    // P4-37 ①:后台命令输出超限截断(100MB 闸,文件尾省略标记)
	BlackboxBGLifespanKill   = "bg_lifespan_kill"   // P4-37 ②:后台命令寿命到点强杀(30 分钟闸,kill 留痕)
	BlackboxToolSpill        = "tool_spill"         // P4-39 ①:超长工具输出落盘占位(30k 阈值,对话内只留截头+引用)
	BlackboxMicroCompact     = "micro_compact"      // P4-39 ②:旧轮工具结果体清理(微压缩,清出条数与字节数)
	BlackboxAssessTimeout    = "assess_timeout"     // P4-42 ②:主宰单轮研判墙钟超时中断(弃本轮,心跳/事件可再触发)
	BlackboxLLMChainStreak   = "llm_chain_streak"   // P4-57 ④:同端点同错误连续命中(≥2 次,同类连环——不等肉眼发现)
)

var blackboxRecordTypes = map[string]bool{
	BlackboxBudgetHalf: true, BlackboxUnitExhausted: true, BlackboxBudgetRenewal: true,
	BlackboxTaskSettling: true, BlackboxGraceHardStop: true, BlackboxTaskSettled: true,
	BlackboxHeartbeat: true, BlackboxHbStall: true, BlackboxHbRepeat: true, BlackboxHbBurnAnomaly: true,
	BlackboxUnitApprovalDeny: true,
	BlackboxUnitBlocked:      true, BlackboxTaskBlocked: true, BlackboxTaskResumed: true,
	BlackboxRestartAdoption: true, BlackboxEnvObservation: true,
	BlackboxBGOutputTrunc: true, BlackboxBGLifespanKill: true,
	BlackboxToolSpill: true, BlackboxMicroCompact: true,
	BlackboxAssessTimeout:  true,
	BlackboxLLMChainStreak: true,
}

// BlackboxRecord 是 EventBlackboxRecord 的负载(P2-04,第四章 §6):系统级
// 事件的横切记账——预算状态变化(exhausted/settling/硬停/终态)与聚合
// 里程碑(预算过半)。不进 nodes/edges 投影(审计流,同 swarm.record)。
// Scope 把系统事件钉回图上对象(Step/Unit),审计时可下钻。
type BlackboxRecord struct {
	Type   string         `json:"type"`
	StepID string         `json:"step_id,omitempty"`
	UnitID string         `json:"unit_id,omitempty"`
	Detail map[string]any `json:"detail,omitempty"` // reason/deadline/achieved 等自由细节
}

// TokenUsage 是 token 独立记账表的一行(P2-04,第四章 §6:逐次 LLM 调用
// 的 input/output/cache 用量按任务/agent/模型全量记账,防账本膨胀——独立
// 表不进事件流,仪表盘直接读)。一行 = 一次模型调用(一个模型轮)。
type TokenUsage struct {
	TaskID           string
	Agent            string // 记账归属:swarm:uN / overmind / …
	Model            string
	TranscriptID     string // 会话原文 JSONL 的文件名锚点
	Turn             int    // 运行内模型轮次(自 1 起)
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	OccurredAt       time.Time
}

// 舵杆指令与回执状态词汇(P2-05 建立,P2-08 补全回执状态机,第八章
// 第 8 节:已排队 → 已送达 → 已采纳/被拒;生效前单位落地 → 已过时)。
const (
	SteerCommandSteer = "steer" // steer_work:纠偏消息排队,单位下一次工具调用前注入
	SteerCommandKill  = "kill"  // kill_work:止损终止,走独立停止机制(单位真中断)

	SteerStateQueued    = "queued"    // 已排队(指令入账,等待生效点)
	SteerStateDelivered = "delivered" // 已送达(在生效点注入单位上下文)
	SteerStateAdopted   = "adopted"   // 已采纳(单位消化后按指令调整了方向,回执标记随行)
	SteerStateRejected  = "rejected"  // 被拒(单位明示不采纳,理由随行)
	SteerStateStale     = "stale"     // 已过时(生效前单位落地/被止损,指令失去生效点)
)

var steerCommands = map[string]bool{SteerCommandSteer: true, SteerCommandKill: true}
var steerStates = map[string]bool{
	SteerStateQueued: true, SteerStateDelivered: true,
	SteerStateAdopted: true, SteerStateRejected: true, SteerStateStale: true,
}

// SteerRecord 是 EventSteerRecord 的负载(P2-05,第八章第 8 节纠偏纵深):
// 舵杆指令的审计流,不进 nodes/edges 投影(同 swarm.record 模式,查询走
// 事件流)。持有人:造物主(面板)与主宰(心跳巡检后纠偏)皆可下达
// (By 区分);steer 在单位下一次工具调用边界注入其上下文(不打断、
// 不丢已有进展),kill 走独立停止机制(cancel 单位 ctx,引擎真中断,
// 不进收尾)。R9 纪律同理适用:舵杆不可跳过审批门。
type SteerRecord struct {
	SteerID string `json:"steer_id"` // 稳定标识(st-*,送达记录回指排队记录)
	StepID  string `json:"step_id"`
	UnitID  string `json:"unit_id,omitempty"` // delivered 时填(送达/被执行的单位)
	Command string `json:"command"`           // steer|kill
	State   string `json:"state"`             // queued|delivered
	Message string `json:"message,omitempty"` // steer:纠偏消息(delivered 侧回写原文,单条查询自足)
	By      string `json:"by,omitempty"`      // 下达者:creator(面板)/ overmind(心跳纠偏)
	Reason  string `json:"reason,omitempty"`  // kill:止损理由
	Note    string `json:"note,omitempty"`    // 送达侧补充(如注入时机)
}

// OperatorDive 是 EventOperatorDive 的负载(P4-48,问题清单 #67):操作者
// 深入指令——从成果(TestClaim)或事实(Fact)节点出发"推一把"的节点
// 锚定指令。落账即记账(R7 无旁路:任务/锚定节点/方向/造物主/时间在
// 账本可审计),写入方负责经既有唤醒通道立即唤醒主宰;纳谏语义——
// 立即规划是硬承诺,方向文本是建议,采纳由主宰裁决(研判记录说明
// 理由)。不进 nodes/edges 投影(指令是审计流,锚定节点本就在图上)。
// 同一节点可多次深入,每次一条事件、各自可溯。
type OperatorDive struct {
	NodeID    string `json:"node_id"`             // 锚定节点 ID(成果或事实)
	Direction string `json:"direction,omitempty"` // 可选方向文本(留空 = 主宰自行判断)
}

// validateOperatorDive 负载结构校验:锚定节点必填。
func validateOperatorDive(p OperatorDive) error {
	if strings.TrimSpace(p.NodeID) == "" {
		return fmt.Errorf("rings: 深入指令缺锚定节点")
	}
	return nil
}

// 主宰研判记录类型白名单(受控入口,P2-06)。
const OvermindRecAssessment = "assessment" // 态势研判:五问结论 + 派发摘要(每轮拆解会话的研判轨迹)
const OvermindRecPlanning = "planning"     // 真实规划调用前的触发依据与水位

var overmindRecordTypes = map[string]bool{OvermindRecAssessment: true, OvermindRecPlanning: true}

// OvermindRecord 是 EventOvermindRecord 的负载(P2-06,第四章 §1 主宰
// 职责"态势研判"的标准五问):每轮拆解会话的研判轨迹审计流,不进
// nodes/edges 投影(同 hypothesis.record 分工——研判本身不是图上业务
// 节点,但"研判发生过、研判了什么"必须可审计)。五问是启发式起点
// (保证下限),不是封闭问题清单(不设上限)——五问之外的研判入 Note。
// EvaluatedRev 是本轮研判所读的账本版本,兼作下一轮"自上轮研判以来
// 的变化"计算锚点(态势快照据此 diff——五问全是"新 X 是否 Y"形式,
// 没有变化集就没有研判输入)。
type OvermindRecord struct {
	TriggerRevisions []int64         `json:"trigger_revisions,omitempty"`
	Bootstrap        bool            `json:"bootstrap,omitempty"`
	Queue            *QueueWatermark `json:"queue,omitempty"`
	Type             string          `json:"type"`
	SessionID        string          `json:"session_id,omitempty"`    // 研判会话锚点(转录 JSONL 下钻)
	EvaluatedRev     int64           `json:"evaluated_rev,omitempty"` // 研判所读账本版本

	// assessment 侧:五问结论(空 = 本轮该问无可研判项,如实呈现)。
	QAccess      string `json:"q_access,omitempty"`      // 新身份能访问哪些已有资产
	QPrivilege   string `json:"q_privilege,omitempty"`   // 新权限是否改变其他漏洞的影响
	QCombination string `json:"q_combination,omitempty"` // 两条单独影响有限的线索是否组合成新的边界问题
	QUnlock      string `json:"q_unlock,omitempty"`      // 新的可达关系是否解锁之前受阻的步骤
	QOverturn    string `json:"q_overturn,omitempty"`    // 新证据是否推翻旧结论
	Note         string `json:"note,omitempty"`          // 五问之外的研判(启发式起点,不设上限)

	Dispatched int      `json:"dispatched,omitempty"` // 本轮派发意图数
	Combined   []string `json:"combined,omitempty"`   // 本轮提交的组合声明 ID(攻击链级发现)
}

// 牧者会话记录类型白名单(受控入口,P2-07,第四章 §1 牧者)。
const (
	ShepherdRecUserMessage   = "user_message"   // 造物主发言(带送达状态机的锚点事件)
	ShepherdRecMessageState  = "message_state"  // 送达状态迁移:delivered|failed
	ShepherdRecReply         = "reply"          // 牧者回复正文(人话,文风条款约束)
	ShepherdRecThinking      = "thinking"       // 牧者思考段截头(消息类型徽标的面板可见面)
	ShepherdRecToolCall      = "tool_call"      // 牧者工具调用与结果(折叠卡的审计面)
	ShepherdRecAdvice        = "advice"         // 对主宰的建议提交(纳谏语义:入账待裁决)
	ShepherdRecAdviceVerdict = "advice_verdict" // 主宰裁决:adopted|rejected(裁决后改变)
	ShepherdRecSystemNote    = "system_note"    // 系统注入的收尾提示(P2-08f:任务终态触发牧者出一句进展总结)
)

var shepherdRecordTypes = map[string]bool{
	ShepherdRecUserMessage: true, ShepherdRecMessageState: true,
	ShepherdRecReply: true, ShepherdRecThinking: true, ShepherdRecToolCall: true,
	ShepherdRecAdvice: true, ShepherdRecAdviceVerdict: true,
	ShepherdRecSystemNote: true,
}

// 送达状态词表(P2-07):已排队(入账待处理)→ 已送达(注入牧者会话的
// 模型请求)→ 已回复 / 失败。失败态如实携带原因,可重发。
const (
	ShepherdMsgDelivered = "delivered"
	ShepherdMsgFailed    = "failed"
)

// 建议状态词表(纳谏语义,第四章 §7:对主宰 = 建议进正式规划,裁决后改)。
const (
	ShepherdAdvicePending  = "pending"  // 待裁决(主宰下一轮研判处理)
	ShepherdAdviceAdopted  = "adopted"  // 已采纳(裁决理由留痕)
	ShepherdAdviceRejected = "rejected" // 未采纳(裁决理由留痕)
)

// ShepherdRecord 是 EventShepherdRecord 的负载(P2-07,第四章 §1 牧者:
// 每个任务一个总对话入口,看/说/答):牧者会话的完整对话流审计——
// 造物主发言与送达状态、牧者回复与工具调用(折叠卡)、对主宰的建议
// (纳谏:提交 → 主宰裁决后改变)。不进 nodes/edges 投影(审计流,同
// swarm.record 分工——对话不是图上业务节点,但"说了什么、答了什么、
// 建议裁决结果"必须可审计,重启后对话历史从事件流完整重建)。
// MessageID 把一次问答的 user_message / message_state / tool_call /
// thinking / reply 锚在一起;advice 独立编号(AdviceID),裁决事件回指。
//
// P4-14(问题清单 #24)扩三字段:ClaimID+Round 锚"成果追问"子会话
// (一成果一会话、每轮独立;"新开一轮"= Round+1 新会话),空 ClaimID
// = 牧者主对话(既有语义不变,互不混流);Prelude 只随每轮首条
// user_message 落账——服务端确定性装配的成果链路上下文包,应答时注入
// 模型会话(面板消息流只呈造物主的问话原文,预载卡由发现详情数据另呈)。
type ShepherdRecord struct {
	MessageID string `json:"message_id,omitempty"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"` // user_message/reply/thinking/advice 的正文

	// 成果追问会话锚(P4-14):ClaimID 空串 = 牧者主对话。
	ClaimID string `json:"claim_id,omitempty"`
	Round   int    `json:"round,omitempty"`   // 追问轮次(1 起;每轮独立会话)
	Prelude string `json:"prelude,omitempty"` // user_message 侧:该轮首问预载的成果链路上下文包

	// message_state 侧。
	State string `json:"state,omitempty"` // delivered|failed
	Note  string `json:"note,omitempty"`  // failed 原因等补充

	// tool_call 侧。
	ToolName   string          `json:"tool_name,omitempty"`
	ToolArgs   json.RawMessage `json:"tool_args,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	ResultHead string          `json:"result_head,omitempty"`

	// advice / advice_verdict 侧。
	AdviceID string `json:"advice_id,omitempty"` // av-*
	Reason   string `json:"reason,omitempty"`    // 裁决理由(verdict 必填)
}

// ---- P2-08 人机通道:单位控制与插话子会话 ----

// 单位控制记录类型白名单(受控入口,P2-08,第四章 §7 三级控制)。
const (
	UnitCtlPause      = "pause"       // 暂停(当前工具调用完成后挂起:不退出、会话冻结、计时停)
	UnitCtlResume     = "resume"      // 继续(原地接着干,继续前按 R14 复检图版本)
	UnitCtlPauseLapse = "pause_lapse" // 暂停在生效前单位自然落地(暂停闩随之失效,如实留痕)
)

var unitControlActions = map[string]bool{
	UnitCtlPause: true, UnitCtlResume: true, UnitCtlPauseLapse: true,
}

// UnitControlRecord 是 EventUnitControl 的负载(P2-08,第四章 §7 单位
// 控制):单位级(或主宰循环)暂停/继续的操作审计流——谁、何时、对谁、
// 做了什么控制。不进 nodes/edges 投影(同 steer.record 分工);单位停止
// 不用本事件(kill_work 走 steer.record 的 kill 线,复用 P2-05 机制)。
type UnitControlRecord struct {
	Target string `json:"target"`            // unit|overmind(控制对象:虫群单位或主宰循环)
	StepID string `json:"step_id,omitempty"` // unit 侧:单位执行的步骤
	UnitID string `json:"unit_id,omitempty"` // unit 侧:单位 ID
	Action string `json:"action"`            // pause|resume|pause_lapse
	By     string `json:"by,omitempty"`      // 操作者(creator)
	Reason string `json:"reason,omitempty"`  // 操作理由(可选)
	Note   string `json:"note,omitempty"`    // 补充说明(如暂停失效原因)
}

// 插话子会话记录类型白名单(受控入口,P2-08,第四章 §7 插话)。
const (
	InterjectRecUserMessage   = "user_message"   // 造物主发言(问/改,绑定目标与提问时图版本)
	InterjectRecMessageState  = "message_state"  // 送达状态迁移:delivered|failed
	InterjectRecReply         = "reply"          // "问"的回答(基于目标历史,文风条款约束)
	InterjectRecSteer         = "steer"          // "改"对虫群:关联舵杆指令(回执状态机在 steer.record)
	InterjectRecAdvice        = "advice"         // "改"对主宰:建议提交(纳谏语义,裁决后改)
	InterjectRecAdviceVerdict = "advice_verdict" // 主宰裁决:adopted|rejected
)

var interjectRecordTypes = map[string]bool{
	InterjectRecUserMessage: true, InterjectRecMessageState: true,
	InterjectRecReply: true, InterjectRecSteer: true,
	InterjectRecAdvice: true, InterjectRecAdviceVerdict: true,
}

// 插话送达状态词表(同 shepherd 口径)。
const (
	InterjectMsgDelivered = "delivered"
	InterjectMsgFailed    = "failed"
)

// InterjectRecord 是 EventInterjectRecord 的负载(P2-08,第四章 §7
// 插话:在任一 agent 会话旁新开 /btw 式子会话——问(不改执行,即时答)
// 与改(对虫群=舵杆注入;对主宰=建议进正式规划)):子会话独立留存,
// 不污染主轨迹;问答、改向、回执关联全部走账本(R7)。Target 绑定
// overmind 或具体虫群单位(单位以 transcript_id 为稳定锚,跨进程不变);
// GraphVersion 是提问时的图版本(子会话上下文锚点)。不进 nodes/edges
// 投影(审计流,同 shepherd.record 分工)。
type InterjectRecord struct {
	Target    string `json:"target"`               // overmind|unit
	UnitID    string `json:"unit_id,omitempty"`    // unit 侧:单位 ID(uN,进程内标识)
	StepID    string `json:"step_id,omitempty"`    // unit 侧:所负责步骤
	SessionID string `json:"session_id,omitempty"` // unit 侧:单位会话锚点(transcript_id,稳定)
	MessageID string `json:"message_id,omitempty"` // im-*(一次发言/问答的锚)
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"` // user_message/reply/advice 的正文

	// user_message 侧。
	Mode         string `json:"mode,omitempty"`          // ask(问)|steer(改)
	GraphVersion int64  `json:"graph_version,omitempty"` // 提问时图版本

	// message_state 侧。
	State string `json:"state,omitempty"` // delivered|failed
	Note  string `json:"note,omitempty"`  // failed 原因等补充

	// steer 侧:关联舵杆指令(回执状态机:已排队→已送达→已采纳/被拒/已过时,
	// 状态事件在 steer.record,视图按 SteerID 汇合)。
	SteerID string `json:"steer_id,omitempty"`

	// advice / advice_verdict 侧。
	AdviceID string `json:"advice_id,omitempty"` // av-*
	Reason   string `json:"reason,omitempty"`    // 裁决理由(verdict 必填)
}

// ---- P4-18 收口信号(问题清单 #30) ----

// 收口信号记录类型白名单(受控入口,P4-18;审核迭代 1 扩 config)。
const (
	ClosureRecPrompt   = "prompt"   // 收口提醒触发(机器判定:连续 N 轮研判零新发现 + 距上次新发现 ≥ 间隔,双条件同时满足)
	ClosureRecContinue = "continue" // 造物主选「继续」:任务不停,同规则重新计数
	ClosureRecEnd      = "end"      // 造物主选「结束」:走优雅收尾(收尾时序 + 终版报告)
	ClosureRecConfig   = "config"   // 任务级配置变更(审核迭代 1:开关 + 轮数 + 间隔,快照式整体覆盖,最新一条即当前)
	ClosureRecDismiss  = "dismiss"  // 弹窗被直接关闭、未裁决(P4-23:待决保留,任务列表标红点,处理才消)
)

var closureRecordTypes = map[string]bool{
	ClosureRecPrompt:   true,
	ClosureRecContinue: true,
	ClosureRecEnd:      true,
	ClosureRecConfig:   true,
	ClosureRecDismiss:  true,
}

// 收口计数锚点种类(prompt 快照词表,P4-18):面板据此措辞——finding=
// 距上次新发现 / continue=距上次选择继续 / created=开跑以来零新发现。
const (
	ClosureAnchorFinding  = "finding"
	ClosureAnchorContinue = "continue"
	ClosureAnchorCreated  = "created"
)

var closureAnchorKinds = map[string]bool{
	ClosureAnchorFinding:  true,
	ClosureAnchorContinue: true,
	ClosureAnchorCreated:  true,
}

// 收尾发起方(终态 detail.settled_by 的词表,P4-18):面板据此区分
// "整体超时收尾"与"收口提醒结束"——两者都是有序收尾(已收尾徽标),
// 与操作者熔断不同。
const (
	SettleByTaskGate      = "task_gate"      // 任务整体超时到点(P2-04 既有语义)
	SettleByClosurePrompt = "closure_prompt" // 收口提醒的「结束」键
)

// ClosureRecord 是 EventClosureRecord 的负载(P4-18,问题清单 #30):
// 收口信号的审计流——机器触发的提醒(prompt, actor=overmind)与造物主
// 的两键裁决(continue/end, actor=creator)。不进 nodes/edges 投影
// (同 overmind.record 分工);读取侧从事件流确定性推导计数与待决态
// (见 overmind.ClosureOf):锚点 = 上次新发现(事实/资产/声明节点新增)
// 或上次「继续」,任一新增即清零重计;prompt 待决到被裁决或新发现
// 出现为止。prompt 侧字段是触发时刻的机器快照(数字精确,面板直读)。
type ClosureRecord struct {
	Type string `json:"type"` // prompt|continue|end|config|dismiss

	// prompt 侧(触发时刻快照)。
	Rounds           int    `json:"rounds,omitempty"`            // 锚点后的研判轮数(overmind.record assessment 计数)
	Minutes          int    `json:"minutes,omitempty"`           // 触发时距锚点分钟数
	RoundsThreshold  int    `json:"rounds_threshold,omitempty"`  // 触发时生效的轮数阈值(面板可配)
	MinutesThreshold int    `json:"minutes_threshold,omitempty"` // 触发时生效的间隔阈值(分钟,面板可配)
	AnchorAt         string `json:"anchor_at,omitempty"`         // 锚点时刻(RFC3339:上次新发现 / 上次「继续」/ 建任务)
	AnchorKind       string `json:"anchor_kind,omitempty"`       // 锚点种类(finding|continue|created)

	// config 侧(任务级配置快照,审核迭代 1):Enabled 必带;Rounds/
	// Minutes 为 0 或缺省 = 该项不单独设定(用全局默认)。建任务表单的
	// 初始配置随 created detail.closure 落库,运行中变更落本事件,最新
	// 一条 config(或 created 初始)即当前任务级配置。
	Enabled *bool `json:"enabled,omitempty"`
	// Rounds/Minutes 复用 prompt 侧字段(config 型下语义为配置值)。

	// continue / end 侧。
	Reason string `json:"reason,omitempty"` // 备注(可选)
}

// QueueWatermark 同时用于规划审计与面板展示，排队只计可领取步骤。
type QueueWatermark struct {
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Waiting   int `json:"waiting"`
	Available int `json:"available"`
	Limit     int `json:"limit"`
}
