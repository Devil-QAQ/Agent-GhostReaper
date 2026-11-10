package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

func timeDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// ErrStaleGraph R14 复检失败:领取后图版本已前进(前置事实/RoE/资产
// 已变),拒绝按过期计划执行。
var ErrStaleGraph = errors.New("swarm: 图版本已变化,拒绝按过期计划执行(R14)")

// Executor 虫群执行器:一只单位领一条意图执行,完成即销毁
// (一 Step 一单位,第四章 §2)。零值不可用,NewExecutor 构建。
type Executor struct {
	Store    *rings.Store
	Provider llm.Provider
	Blobs    *BlobStore
	Budget   Budget
	// Model 模型名(token 独立表按模型记账,第四章 §6)。装配层从环境
	// 变量注入;缺省空串如实记空(用量仍按任务/agent 可查)。
	Model string
	// RootDir 任务工作目录根(第七章 §5):每步子目录 <RootDir>/<taskID>/i<stepID>/,
	// 转录与 blob 亦按任务切分。
	RootDir string
	// Gate 审批弹门(P1-07,第四章 §3):中高危意图分级下的工具调用
	// 逐次过门。nil = 无门可用——此时 L2+ 的非只读工具调用直接拒绝
	// (fail-safe:没有人在环,就不放行危险动作),L1 照常放行留痕。
	Gate ApprovalGate
	// Mirror 镜(P2-01,第一章"全程流量经镜"):非 nil 时每只单位的
	// HTTP 通道强制经镜——http_probe 工具拿镜接线客户端、bash 子进程
	// 注入代理环境变量,请求/响应留痕入账本(R18 引用 blob)。nil = 无镜
	// (P1 行为,http_probe 直连);会话起不来即执行失败——镜是强制
	// 基础设施,起不来就不放行任何流量。
	Mirror *mirror.Mirror
	// ModelRetryLadder / BlockedDeepAfter 意图级模型错误长退避参数
	// (P4-27 ①):退避梯度(1m/5m/15m)与深阈值(连续失败跨线才落
	// blocked);零值取 Default*(深阈值全局键 swarm.modelretry.deep_minutes
	// 可配,Agent 栏虫群参数)。退避等待与零进展失败段不计预算
	// (block_wait)。测试缩小时长只影响时序不影响语义。
	ModelRetryLadder []time.Duration
	BlockedDeepAfter time.Duration
	// Helm 舵杆(P2-05,第八章第 8 节):steer_work / kill_work 的权威
	// 登记表,面板与主宰经它下达指令;生效点在单位工具调用边界
	//(见 guard.go)。NewExecutor 自建默认实例;装配层把同一实例交给
	// server(与审批门同一装配形态)。nil 不会被替换——零值 Helm 也会
	// 自建,舵杆永远可用。
	Helm *Helm
	// Hiber 任务冬眠门(P2-08;P4-33 起为冻结收场):门关(任务冬眠)
	// 期间面板只读、审批冻结、牧者入口冻结;单位侧门等待仅剩收场过渡
	// (P4-33:收场令与门同发,等待者带节拍复查见令即落地,不再驻留
	// 门上过冬眠)。装配层与 Manager/主宰循环/牧者/审批门共持同一
	// 实例。nil = 无门(单测:无冬眠语义)。
	Hiber *hiber.Gate
	// Ckpt 断点冻结信号(P3-07,第七章「故障与恢复」断点续接):武装后
	// 单位在工具调用边界收场——当前调用完成后落地,session_finished 带
	// checkpointed 标记(会话保留可续),步骤回可领取。P4-33 冬眠收场
	// 复用同一约定(武装即收场)。装配层与 Manager/主宰循环共持同一
	// 实例;nil = 无断点语义(单测)。
	Ckpt *ckpt.Gate

	// RoundHumanGen 反证轮人话生成钩子(P4-64 ①,问题清单 #82):每轮
	// falsify 结构化结论入账时同链生成该轮"疑点→怎么验的→结论"三段
	// 人话,与机器记录同批落账(生成失败由钩子侧回退拼句,不阻塞轮次
	// 入账)。装配层按任务路由到在跑主宰循环;nil 或返回 nil = 本轮
	// 不落人话(无在跑循环等,读侧退机器层)。
	RoundHumanGen func(ctx context.Context, taskID, claimID string, rec *rings.TestClaimRecord) *rings.ClaimRoundHuman
	// RetestNotePolisher 复测结论备注校对钩子(P4-64 ⑦):复测结论入账
	// 时过既有校对通道,校对稿与 retest 记录同批落账(机器 Note 原文
	// 一字不动)。nil 或返回 nil = 不落校对稿,读侧退机器记录原文。
	RetestNotePolisher func(ctx context.Context, taskID, claimID string, round int, verdict, note string) *rings.RetestNotePolish

	mu      sync.Mutex
	unitSeq int

	// unitCancels 在飞单位的取消句柄登记表(P2-04 收尾宽限硬停):
	// 任务级收尾的宽限超时后,主宰对仍挂着的单位逐个硬停(cancel 单位
	// ctx → 引擎真中断,不进收尾)。单位落地即注销。
	unitCancels map[string]context.CancelFunc
	// stepUnits 在飞单位的步骤索引(task+"\x00"+step → unitID,P2-05
	// kill_work 止损定位用;一 Step 一单位)。单位落地即注销。
	stepUnits map[string]string
	// unitLatches 单位暂停闩(P2-08 三级控制):task+"\x00"+step → 开放
	// 通道(暂停中),关闭即继续。单位落地即清理(pause_lapse 留痕)。
	unitLatches map[string]chan struct{}

	// claimMu 起飞临界区(P1-05 并发落地):Claim → R14 复检 → 首笔入账
	// 三步串行;单位的终态入账(finishUnit)亦进本临界区。没有它,并发
	// 单位的首笔记账或友邻单位的终态记账会推进 revision,把其他单位刚
	// 绑定的图版本误判成过期——R14 只应捕捉真实前提变化(RoE/前置事实/
	// 资产),不捕捉友邻单位的记账噪音。
	claimMu sync.Mutex

	// quotaMu 量化额度临界区(P2-08e):任务级规则"读计数 → 判 → 入账"
	// 三步串行——并发单位同时过门时账面序号不重不断。
	quotaMu sync.Mutex

	// bgMu 后台 bash 进程登记表(P3-05 限时放开):unitID → 在册后台
	// 命令。转后台的进程脱离调用 ctx 继续运行,唯一回收点是单位落地
	// (finishUnit defer)——kill_work/硬停/熔断都经单位 ctx 让单位落地,
	// 落地路径统一整组回收,不另设分叉。输出文件保留(审计与
	// 后续读取),登记项注销。
	bgMu   sync.Mutex
	bgRuns map[string][]*bgRun

	// rtMu 复测会话并发互斥(P3-06):同声明同时至多一轮复测——账本
	// 悬空扫描与活动登记同临界区;悬空会话(进程重启残留)在下次发起
	// 时收口为中断留痕(见 BeginRetest)。liveRetests 是"当前真在跑"的
	// 唯一权威(进程重启即清零,账本悬空不误报)。
	rtMu        sync.Mutex
	liveRetests map[string]bool // taskID+"\x00"+claimID

	// rpMu 重放校验并发互斥(P3-07):同声明同时至多一轮重放(确定性
	// 执行,无会话无悬空——活动登记只防并发重复发起)。
	rpMu        sync.Mutex
	liveReplays map[string]bool // taskID+"\x00"+claimID
	clockMu     sync.Mutex
	clocks      map[string]*runClock // taskID+"\x00"+transcript → 活动时钟(P4-02 ④,见 clock.go)
}

// NewExecutor 构建执行器。blobs 为 nil 时在 RootDir/blobs 自建。
func NewExecutor(store *rings.Store, provider llm.Provider, rootDir string, budget Budget) *Executor {
	e := &Executor{Store: store, Provider: provider, Budget: budget, RootDir: rootDir,
		unitCancels: map[string]context.CancelFunc{}, stepUnits: map[string]string{},
		unitLatches: map[string]chan struct{}{}, bgRuns: map[string][]*bgRun{},
		clocks: map[string]*runClock{}}
	if e.Blobs == nil {
		e.Blobs = NewBlobStore(filepath.Join(rootDir, "blobs"))
	}
	e.Helm = NewHelm(store)
	// 单位号从账本最大值之后继续(重启收养纪律):进程内计数器归零会与
	// 历史单位撞号——单位号同时是会话文件名(swarm-uN),撞号即两个
	// 单位的会话写进同一 transcript。查询失败按 0 起(与历史行为一致,
	// 仅新鲜库无碰撞风险)。
	if n, err := store.MaxUnitSeq(context.Background()); err == nil {
		e.unitSeq = n
	}
	return e
}

// KillUnitByStep kill_work 的止损钩子(P2-05):按 task+step 找在跑
// 单位并 cancel 其单位 ctx——引擎真中断(不进收尾),步骤落 aborted。
// 命中返回单位 ID;未命中(单位已落地/未起飞)返回 ok=false。
func (e *Executor) KillUnitByStep(taskID, stepID string) (string, bool) {
	e.mu.Lock()
	unitID, ok := e.stepUnits[steerKey(taskID, stepID)]
	var cancel context.CancelFunc
	if ok {
		cancel = e.unitCancels[unitID]
	}
	e.mu.Unlock()
	if !ok || cancel == nil {
		return "", false
	}
	cancel()
	return unitID, true
}

// RunningStep 任务级规则定向告知(P2-08e 审核迭代)用的在飞单位条目。
type RunningStep struct {
	StepID string
	UnitID string
}

// RunningUnitSteps 任务当前在飞单位清单:server 侧规则变更时据此算
// 受影响单位(范围 ∩ 步骤 covers 资产),只对相交单位定向投递舵杆
// 消息——不相关单位零打扰。按步骤 ID 稳定排序。
func (e *Executor) RunningUnitSteps(taskID string) []RunningStep {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []RunningStep
	for k, unitID := range e.stepUnits {
		t, s, found := strings.Cut(k, "\x00")
		if !found || t != taskID {
			continue
		}
		out = append(out, RunningStep{StepID: s, UnitID: unitID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepID < out[j].StepID })
	return out
}

// CancelRunningUnits 硬停在飞单位(收尾宽限超时的兜底,第四章 §6):
// 逐个 cancel 单位专属 ctx——引擎按真中断收场(aborted_*,不进收尾),
// 账本终态入账照常(审计闭环)。返回被硬停的单位清单(留痕用)。
func (e *Executor) CancelRunningUnits() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for id, cancel := range e.unitCancels {
		ids = append(ids, id)
		cancel()
	}
	return ids
}

// CancelUnitsOf 硬停单个任务的在飞单位(P4-18 收口结束的宽限硬停):
// 与 CancelRunningUnits 同一收场语义,范围限定在单任务——多任务并发
// 时收口结束只硬停本任务单位,不误伤其他任务。返回被硬停的单位清单。
func (e *Executor) CancelUnitsOf(taskID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for k, unitID := range e.stepUnits {
		t, _, found := strings.Cut(k, "\x00")
		if !found || t != taskID {
			continue
		}
		if cancel, ok := e.unitCancels[unitID]; ok {
			ids = append(ids, unitID)
			cancel()
		}
	}
	return ids
}

// RunResult 一次虫群单位执行的结果摘要。
type RunResult struct {
	UnitID       string
	TranscriptID string
	Reason       string // 引擎终因: completed|max_turns|timeout|aborted_*|审批拒绝终止: …|checkpointed|...
	Turns        int
	StepStatus   string // done|exhausted|failed|aborted|approval_denied|blocked|dispatched(断点/R14 回可领取)
	FinalText    string
	Stale        bool // R14 复检失败,未执行
	// BlockedClass blocked 归因(P4-27 ⑥⑦):model_fault=模型故障(跨
	// 深阈值,计任务受阻);determinant=确定性拒绝(程序缺陷,不计
	// 故障计数)。仅 StepStatus==blocked 时有值;随步骤 detail 与黑匣子
	// 落账,任务受阻计数(overmind)据此排除确定性归因。
	BlockedClass string
	// Checkpointed 断点存档收场(P3-07):当前工具调用完成后单位落地,
	// 步骤回可领取、会话保留可续(session_finished 带 Checkpointed 标记)。
	Checkpointed bool
	// ScopeWait 范围等待批准收场(P4-51 ①):命令命中任务范围外,拦下
	// 出待决卡后单位冻结收场让位名额——步骤落 scope_wait 态(不可领
	// 取,等"加入资产/禁止测试"裁决)、会话冻结保留(session_finished
	// 带 ScopeWait 标记)、并发名额释放、计时停走;批准后步骤回可领取
	//(插队队首),重领的单位原会话 Resume 续跑。
	ScopeWait bool

	// 时间账(P2-06b 审批等待 / P2-06e 模型故障 / P2-08 挂起等待,计时
	// 暂停对账):billable 只含各有效执行段时长,gateWait 为弹门等待合计,
	// blockWait 为模型故障重试等待合计(退避等待与零进展失败段),
	// pauseWait 为单位暂停/任务冬眠的挂起等待合计——四者随
	// session_finished note 落账可对账(等待都不吃预算)。
	billable  time.Duration
	gateWait  time.Duration
	blockWait time.Duration
	pauseWait time.Duration
}

// ledgerCtx 账本入账专用 ctx:虫群记录与状态迁移不可因任务熔断而消失
// (Store 排队语义:入队后必然执行到提交)。
var ledgerCtx = context.Background()

// guardLedgerCtx 拦截判定专用 ctx(P2-08e):硬规则读与额度计数入账
// 不随任务熔断而失守——收尾/熔断路径上的判定照常执行(fail-safe
// 方向:宁可多拦,账面不断)。
var guardLedgerCtx = context.Background()

// Run 执行一条已领取的意图。流程(R14 在最前):
// 复检图版本 → step→running → session_started 入账(绑 transcript_id)
// → Norma Session 循环(工具结果实时入账,R18 大输出只存引用)
// → 按引擎终因迁移 step 状态(completed→done;预算→exhausted,R16;
// 熔断中断→aborted;其余→failed)→ session_finished 入账。
//
// 返回 ErrStaleGraph 时未发生任何模型调用与工具执行。
// 注意:pack 的版本绑定发生在本方法之外(调用方先 Claim)——并发
// 单位调度请用 RunStep(领取与复检同临界区,互不误判过期)。
func (e *Executor) Run(ctx context.Context, pack *TaskPack) (*RunResult, error) {
	e.claimMu.Lock()
	env, budget, stale, err := e.beginUnit(ctx, pack)
	e.claimMu.Unlock()
	if err != nil {
		return stale, err
	}
	return e.finishUnit(ctx, env, pack, budget)
}

// RunStep 领取并执行一条意图(P1-05 主宰调度入口):Claim(七要素,
// 绑定当前图版本)→ R14 复检 → 首笔入账三步在同一临界区,并发单位
// 互不把对方的绑定版本推过期;此后长执行在临界区外。返回语义与
// Run 一致(ErrStaleGraph 时未发生任何模型调用与工具执行)。
func (e *Executor) RunStep(ctx context.Context, taskID, stepID string) (*RunResult, error) {
	e.claimMu.Lock()
	pack, err := e.Claim(ctx, taskID, stepID)
	if err != nil {
		e.claimMu.Unlock()
		return nil, err
	}
	env, budget, stale, err := e.beginUnit(ctx, pack)
	e.claimMu.Unlock()
	if err != nil {
		return stale, err
	}
	// 长执行已在临界区外:并发单位只串行"领取+起飞",不串行执行。
	return e.finishUnit(ctx, env, pack, budget)
}

// beginUnit 单位起飞临界区体(须持 claimMu):R14 执行前复检 → 分配
// 单位身份 → step→running → session_started 首笔入账。res 非 nil 时为
// R14 拒绝结果(未执行,step 回 dispatched 待重新领取)。
func (e *Executor) beginUnit(ctx context.Context, pack *TaskPack) (*unitEnv, Budget, *RunResult, error) {
	// ---- R14 执行前复检 ----
	cur, err := e.Store.CurrentRevision(ctx, pack.TaskID)
	if err != nil {
		return nil, Budget{}, nil, fmt.Errorf("swarm: 复检图版本: %w", err)
	}
	if cur != pack.GraphVersion {
		e.mu.Lock()
		e.unitSeq++
		n := e.unitSeq
		e.mu.Unlock()
		unitID := fmt.Sprintf("u%d", n)
		// 拒绝也要留痕(审计可见"谁在何时因版本变化放弃执行")。
		_, _ = e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
			Kind: rings.EventSwarmRecord, Actor: "swarm:" + unitID,
			Payload: rings.SwarmRecord{
				StepID: pack.StepID, UnitID: unitID, Type: rings.SwarmRecStaleRefused,
				GraphVersion: pack.GraphVersion,
				Note:         fmt.Sprintf("领取时版本 %d,执行前已到 %d", pack.GraphVersion, cur),
			},
		})
		return nil, Budget{}, &RunResult{UnitID: unitID, StepStatus: "dispatched", Stale: true}, ErrStaleGraph
	}

	// ---- 领取只领 dispatched(P4-45 防复活) ----
	// 调度选中与领取之间存在他方收场窗口(范围摘除止损取消排队步骤、
	// 模型取消同类):此刻复查步骤现状态,非 dispatched 拒领——领到
	// 已收场的步骤会把终态覆写回 running(复活),步骤状态机物理保证
	// 终态不再领取。R14 挡不住"选中后落账、领取前读到新版"的时序,
	// 状态复查补上这一面。
	if step, nerr := e.Store.Node(ctx, pack.TaskID, pack.StepID); nerr == nil && step.Status != "dispatched" {
		e.mu.Lock()
		e.unitSeq++
		n := e.unitSeq
		e.mu.Unlock()
		unitID := fmt.Sprintf("u%d", n)
		_, _ = e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
			Kind: rings.EventSwarmRecord, Actor: "swarm:" + unitID,
			Payload: rings.SwarmRecord{
				StepID: pack.StepID, UnitID: unitID, Type: rings.SwarmRecStaleRefused,
				GraphVersion: pack.GraphVersion,
				Note:         fmt.Sprintf("领取时步骤已为 %s,不再领取", step.Status),
			},
		})
		return nil, Budget{}, &RunResult{UnitID: unitID, StepStatus: step.Status, Stale: true}, ErrStaleGraph
	}

	env := e.newUnit(pack)
	budget := pack.Budget.withDefaults()

	// 原会话续(P2-06f 重启收养 + P3-05c ① 受阻重开 + P3-07 断点续接):
	// 该步骤存在可续会话(上一进程崩溃残留 / 受阻保留 / 断点存档收场)
	// ——本单位接过原会话原地续跑,账本归因钉在真实会话文件上
	//(TranscriptID 沿用原值,UnitID 是新单位;与冬眠唤醒同为"原地续",
	// 省上下文重建成本)。
	resumeNote := ""
	if tid, kind := e.pendingSession(ctx, pack.TaskID, pack.StepID); tid != "" {
		env.TranscriptID = tid
		env.resumed = true
		env.resumeKind = kind
		switch kind {
		case resumeBlocked:
			resumeNote = "受阻重开:原会话续(" + tid + ")"
		case resumeCheckpoint:
			resumeNote = "断点续接:原会话续(" + tid + ")"
		case resumeScope:
			resumeNote = "范围批准续跑:原会话续(" + tid + ")"
		default:
			resumeNote = "重启接管:原会话续(" + tid + ")"
		}
	}

	// step → running(虫群领取即动工)
	if _, err := e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "swarm:" + env.UnitID,
		Payload: rings.NodeStateChanged{NodeID: pack.StepID, Status: "running"},
	}); err != nil {
		runlog.Errorf("账本写入失败: 步骤 running %s/%s: %v", pack.TaskID, pack.StepID, err)
		return nil, Budget{}, nil, fmt.Errorf("swarm: 标记步骤 running: %w", err)
	}
	if _, err := e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID,
		Payload: rings.SwarmRecord{
			StepID: pack.StepID, UnitID: env.UnitID, TranscriptID: env.TranscriptID,
			Type: rings.SwarmRecSessionStarted, GraphVersion: pack.GraphVersion,
			Note: resumeNote,
		},
	}); err != nil {
		runlog.Errorf("账本写入失败: 单位起飞 %s/%s: %v", pack.TaskID, env.UnitID, err)
		return nil, Budget{}, nil, fmt.Errorf("swarm: session_started 写入: %w", err)
	}
	if resumeNote != "" {
		runlog.Infof("虫群单位起飞: %s 任务 %s 步骤 %s(%s)", env.UnitID, pack.TaskID, pack.StepID, resumeNote)
	} else {
		runlog.Infof("虫群单位起飞: %s 任务 %s 步骤 %s", env.UnitID, pack.TaskID, pack.StepID)
	}
	return env, budget, nil, nil
}

// 可续会话的来源类别(pendingSession 判定,续跑指令措辞随类别不同)。
const (
	resumeNone       = ""           // 无可续会话,按全新会话起飞
	resumeAdoption   = "adoption"   // 崩溃残留:session_started 后无 session_finished
	resumeBlocked    = "blocked"    // 模型故障受阻保留(session_finished 带 Resumable)
	resumeCheckpoint = "checkpoint" // 断点存档收场(session_finished 带 Checkpointed)
	resumeScope      = "scope"      // 范围等待批准收场(P4-51:session_finished 带 ScopeWait;批准后重领续跑)
)

// pendingSession 查该步骤的可续会话转录 ID 与来源(P2-06f 重启收养判定
// + P3-05c ① 受阻重开判定 + P3-07 断点续接判定):扫事件流,步骤最近
// 一笔 session_started 之后没有 session_finished 即未收场——进程崩溃的
// 精确签名(正常落地、R14 拒绝、审批拒绝、硬停的收场路径都会落
// session_finished);最近一笔 session_finished 带 Resumable 标记则是
// 模型故障受阻的保留会话(恢复重开优先原会话续),带 Checkpointed 标记
// 则是断点存档收场的保留会话(重启/恢复续跑优先原会话续)。返回空串 =
// 无可续会话,按全新会话起飞。
func (e *Executor) pendingSession(ctx context.Context, taskID, stepID string) (tid string, kind string) {
	evs, err := e.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return "", resumeNone // 读失败退全新会话(fail-safe:恢复不了就接力,不挡领取)
	}
	pending := ""
	kind = resumeNone
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil || rec.StepID != stepID {
			continue
		}
		switch rec.Type {
		case rings.SwarmRecSessionStarted:
			pending, kind = rec.TranscriptID, resumeAdoption
		case rings.SwarmRecSessionEnd:
			switch {
			case rec.Checkpointed:
				// 断点存档收场:续跑资格保留,恢复领取时原会话 Resume。
				kind = resumeCheckpoint
			case rec.ScopeWait:
				// 范围等待批准收场(P4-51):步骤被"加入资产"裁决唤醒回
				// 可领取时,重领的单位原会话 Resume 续跑(重发被拦命令)。
				kind = resumeScope
			case rec.Resumable:
				// 受阻保留会话:续跑资格保留,恢复重开时原会话 Resume。
				kind = resumeBlocked
			default:
				pending, kind = "", resumeNone
			}
		}
	}
	return pending, kind
}

// ckptArmed 断点冻结信号当前是否武装(nil 门 = 否)。
func (e *Executor) ckptArmed(taskID string) bool {
	return e.Ckpt != nil && e.Ckpt.Armed(taskID)
}

// finishUnit 单位落地:驱动执行循环并按引擎终因入账终态(无论成败,
// 审计闭环)。单位长执行挂在单位专属 ctx 上(P2-04:收尾宽限硬停的
// 取消锚点,与任务 ctx 派生、不放大熔断语义);预算耗尽另落黑匣子
// 留痕(unit_exhausted,第四章 §6 预算状态变化入黑匣子通道)。
// 终态入账与领取同临界区(claimMu):友邻单位的终态两笔
// (state_changed + session_finished)若插进另一单位的"领取绑版本 →
// 复检"窗口,会把记账噪音误判成 R14 过期(与 claimMu 宣示的不变式
// 相悖——R14 只应捕捉真实前提变化,不捕捉友邻单位的生命周期记账)。
func (e *Executor) finishUnit(ctx context.Context, env *unitEnv, pack *TaskPack, budget Budget) (*RunResult, error) {
	// 任务标记进请求 ctx(P2-06e):failover 链据此应用任务级主备覆盖。
	unitCtx, cancel := context.WithCancel(failover.WithTask(ctx, pack.TaskID))
	e.mu.Lock()
	e.unitCancels[env.UnitID] = cancel
	e.stepUnits[steerKey(env.TaskID, env.StepID)] = env.UnitID
	e.mu.Unlock()
	// 舵杆止损路由自登记(P2-05):该任务的单位起飞即把 kill_work
	// 路由到本执行器(幂等;演示/重启换执行器时覆盖)。
	if e.Helm != nil {
		e.Helm.attachTask(env.TaskID, e.KillUnitByStep)
	}
	defer func() {
		// 后台 bash 进程整组回收(P3-05):单位落地即杀残余后台命令
		// (kill_work/硬停/熔断都经单位 ctx 令单位落地,统一收口于此),
		// 输出文件保留供审计与后续读取。
		e.reapUnitBG(env.UnitID)
		e.mu.Lock()
		delete(e.unitCancels, env.UnitID)
		// 步骤索引只在本单位是在飞者时清除(kill 止损竞态下后继单位
		// 可能已重新登记,不得误删)。
		if e.stepUnits[steerKey(env.TaskID, env.StepID)] == env.UnitID {
			delete(e.stepUnits, steerKey(env.TaskID, env.StepID))
		}
		e.mu.Unlock()
		// 单位落地即清暂停闩(在暂停中落地 = 暂停失效,pause_lapse 留痕)。
		e.dropUnitLatch(env.TaskID, env.StepID)
		cancel()
	}()

	res, err := e.drive(unitCtx, env, pack, budget)
	// 落地收尾:同步骤未送达的舵杆指令失去生效点,落 stale 回执
	//(P2-08 回执状态机闭环;正常路径无未送达指令,零留痕)。
	if e.Helm != nil {
		e.Helm.expirePending(pack.TaskID, pack.StepID, "单位已干完,指令失去生效点(已过时)")
	}

	// 终态入账(无论成败,审计闭环)。时间账随 session_finished note 落账:
	// billable(有效执行段合计)、gate_wait(弹门等待合计)、block_wait
	//(模型故障等待合计)与 pause_wait(单位暂停/任务冬眠挂起合计)分开
	// 呈现,审批等待、故障等待与挂起期间计时暂停可对账(第四章 §3 v2 /
	// P2-06e"故障不吃预算" / P2-08"挂起不吃预算")。
	// P3-05c ①:受阻收场时会话保留(Resumable 标记)——恢复重开该步骤
	// 时优先原会话 Resume 续跑,上下文零重建。
	// P3-07:断点存档收场时步骤回可领取、会话保留(Checkpointed 标记)
	// ——重启/恢复领取时优先原会话 Resume 续跑。
	status := res.StepStatus
	// blocked 归因随状态迁移落步骤 detail(P4-27 ⑥⑦:确定性拒绝与
	// 模型故障都落 blocked,但归因分开——报告侧受阻派生与任务受阻
	// 计数只认模型故障;投影层把这两个机器标记合并进 detail,其他
	// Note 不落投影)。
	stateNote := ""
	if status == StepBlocked {
		stateNote = res.BlockedClass
	}
	// P4-51 ①:scope_wait 态携带越界目标(机器标记"scope_wait <host>
	// <port>",投影层合并进 detail——对账唤醒按 host:port 重判范围的
	// 判定面)。
	if status == rings.StepScopeWait && env.scopeHold != nil {
		stateNote = "scope_wait " + env.scopeHold.host + " " + env.scopeHold.port
	}
	e.claimMu.Lock()
	_, serr := e.Store.Append(ledgerCtx, pack.TaskID,
		rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "swarm:" + env.UnitID,
			Payload: rings.NodeStateChanged{NodeID: pack.StepID, Status: status, Note: stateNote},
		},
		rings.Event{
			Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID,
			Payload: rings.SwarmRecord{
				StepID: pack.StepID, UnitID: env.UnitID, TranscriptID: env.TranscriptID,
				Type: rings.SwarmRecSessionEnd, GraphVersion: pack.GraphVersion,
				Resumable:    status == StepBlocked,
				Checkpointed: res.Checkpointed,
				ScopeWait:    res.ScopeWait,
				Note: fmt.Sprintf("reason=%s turns=%d status=%s billable=%.1fs gate_wait=%.1fs block_wait=%.1fs pause_wait=%.1fs",
					res.Reason, res.Turns, status, res.billable.Seconds(), res.gateWait.Seconds(), res.blockWait.Seconds(), res.pauseWait.Seconds()),
			},
		},
	)
	e.claimMu.Unlock()
	if serr != nil && err == nil {
		runlog.Errorf("账本写入失败: 单位结束 %s/%s: %v", pack.TaskID, env.UnitID, serr)
		err = fmt.Errorf("swarm: 结束状态写入: %w", serr)
	}
	runlog.Infof("虫群单位干完: %s 任务 %s 步骤 %s 结束 %s(原因 %s,轮次 %d)", env.UnitID, pack.TaskID, pack.StepID, status, res.Reason, res.Turns)

	// 预算耗尽 ≠ 失败(R16):终态落地后黑匣子留痕(暂停接力,非失败)。
	// P4-01 ② 后 exhausted 落地仅剩任务总闸场景(配了上限的单位到线
	// 在 drive 内原会话续跑,不落到这里)。
	if res.StepStatus == "exhausted" {
		if berr := blackbox.Record(ledgerCtx, e.Store, pack.TaskID, "swarm:"+env.UnitID,
			rings.BlackboxRecord{
				Type: rings.BlackboxUnitExhausted, StepID: pack.StepID, UnitID: env.UnitID,
				Detail: map[string]any{
					"reason": res.Reason, "turns": res.Turns,
					"note": "预算耗尽,收尾已把发现写回(暂停接力,非失败)",
				},
			}); berr != nil && err == nil {
			err = berr
		}
	}
	// 模型故障/确定性拒绝受阻(P2-06e,P4-27 ①⑥):原因与等待时长入
	// 黑匣子;归因分开(确定性拒绝是程序缺陷,不进任务受阻判定)。
	if res.StepStatus == StepBlocked {
		note := "模型错误长退避跨深阈值,步骤受阻(等待配置恢复自动重开)"
		if res.BlockedClass == BlockedClassDeterminant {
			note = "模型确定性拒绝,立即受阻不重试(程序缺陷/配置错误,不计故障)"
		}
		if berr := blackbox.Record(ledgerCtx, e.Store, pack.TaskID, "swarm:"+env.UnitID,
			rings.BlackboxRecord{
				Type: rings.BlackboxUnitBlocked, StepID: pack.StepID, UnitID: env.UnitID,
				Detail: map[string]any{
					"reason": res.Reason, "class": res.BlockedClass, "note": note,
				},
			}); berr != nil && err == nil {
			err = berr
		}
	}
	// 审批拒绝熔断单位(P2-06b,第四章 §3 v2):拒绝线自动拒绝与面板拒绝
	// 都以单位级终止收场——黑匣子留系统事件,拒绝原因在 approval.record。
	if res.StepStatus == StepApprovalDenied {
		if berr := blackbox.Record(ledgerCtx, e.Store, pack.TaskID, "swarm:"+env.UnitID,
			rings.BlackboxRecord{
				Type: rings.BlackboxUnitApprovalDeny, StepID: pack.StepID, UnitID: env.UnitID,
				Detail: map[string]any{
					"reason": res.Reason,
					"note":   "审批拒绝终止单位会话(单位级终止,被拒单位不得换法重试)",
				},
			}); berr != nil && err == nil {
			err = berr
		}
	}
	return res, err
}

// newUnit 为本次执行分配单位身份与工作目录。
func (e *Executor) newUnit(pack *TaskPack) *unitEnv {
	e.mu.Lock()
	e.unitSeq++
	n := e.unitSeq
	e.mu.Unlock()
	unitID := fmt.Sprintf("u%d", n)
	return &unitEnv{
		Executor:     e,
		TaskID:       pack.TaskID,
		StepID:       pack.StepID,
		UnitID:       unitID,
		TranscriptID: "swarm-" + unitID,
	}
}

// stepDir 每条意图独立子目录(第七章 §5),中间产物互不踩脚。
func (e *Executor) stepDir(taskID, stepID string) string {
	return filepath.Join(e.RootDir, taskID, "i"+stepID)
}

// drive 驱动 Norma Session 完成执行循环,消费事件流并实时入账。
// 镜已装配时,先起本单位的镜会话(归因=Step/Unit/Transcript),会话
// 存活期即执行期(含弹门挂起期),工具的 HTTP 流量全部经镜留痕。
//
// P4-01 ② 预算续跑(第四章 §6 v2):配了上限的单位到线不落地——引擎
// Settlement 收尾把发现写回后,原会话 Resume 续跑、每段预算逐段重算
// (预算窗基线见 budgetWindow);任务总闸到点不续,硬收落 exhausted。
// 默认 0=不限时无到线路径,单位领了干完为止(失控防护与总闸照旧)。
//
// P2-06b 段化执行(第四章 §3 v2"审批等待期单位计时暂停"):弹门挂起时
// 当前执行段收拢(审批回调 cancel 段 ctx,引擎以 aborted 收段、被挂起
// 调用未执行),单位停在审批票上等裁决——等待期无活动引擎时钟,不消耗
// run_seconds;批准后经转录 Resume 原会话续跑,新段预算 = 剩余预算
// (轮次与墙钟都按已耗扣减)。billable 只累计各执行段时长,弹门等待
// 计入 gateWait——时间账随 session_finished 留痕可对账。
// unitModelName 单位会话当前服务模型名(压缩窗口按服务腿取):链支持
// 逐请求解析时取实时值(任务绑定+会话语境——绑定任务按绑定腿的模型,
// 会话按实际服务腿,P4-41 ③),否则退装配层注入的静态名。
func unitModelName(e *Executor, taskID, transcriptID string) string {
	if f := modelNowFunc(e.Provider, taskID, transcriptID); f != nil {
		if m := f(); m != "" {
			return m
		}
	}
	return e.Model
}

func (e *Executor) drive(ctx context.Context, env *unitEnv, pack *TaskPack, budget Budget) (*RunResult, error) {
	res := &RunResult{UnitID: env.UnitID, TranscriptID: env.TranscriptID}

	// 活动时钟(P4-02 ④):登记即活动(起飞即计时),落地注销——
	// 等待点(弹门/退避/挂起)在段循环内逐点冻结,读侧见 clock.go。
	e.clockSet(pack.TaskID, env.TranscriptID, 0, true)
	defer e.clockDrop(pack.TaskID, env.TranscriptID)

	if e.Mirror != nil {
		sess, err := e.Mirror.Session(mirror.Meta{
			TaskID: pack.TaskID, StepID: pack.StepID,
			UnitID: env.UnitID, TranscriptID: env.TranscriptID,
		})
		if err != nil {
			res.Reason, res.StepStatus = "mirror_error", "failed"
			return res, fmt.Errorf("swarm: 起镜会话: %w", err)
		}
		env.mirror = sess
		defer sess.Close()
	}

	stepDir := e.stepDir(pack.TaskID, pack.StepID)

	// 工具门卫(P2-05 纠偏纵深):舵杆生效点 + 循环内重锚定,包装全部
	// 工具后在调用边界生效(锚间隔随任务包,面板建任务可调)。段化执行
	// 下锚的剩余预算读数逐段刷新(挂起等待不计入)。
	guard := newToolGuard(env, pack, e.Helm, pack.AnchorEverySteps, time.Time{}, budget.MaxTurns)
	// 落盘守卫(P4-39 ①)在门卫之外再包一层:单次工具输出超 30,000 字符
	// 即整段落 blob,对话内只留截头+存档引用(read 按引用回读)。
	tools := wrapSpillGuard(env, guard.wrapAll(filterTools(env.toolset(), pack.Authorization)))

	// 工具调用实时记账:KindToolUse 记名与参数,KindToolResult 落账
	//(大结果整段入 blob,账本只存引用与截头,R18);KindUsage 逐轮切
	// 增量入 token 独立表(P2-04 四类记录分工,一行=一次模型调用)。
	// 记账器跨段累计(单位粒度的账,不是段粒度)。
	var mu sync.Mutex
	toolNames := map[string]string{}         // toolUseID -> name
	toolArgs := map[string]json.RawMessage{} // toolUseID -> args
	usage := &UsageTracker{
		Store: e.Store, TaskID: pack.TaskID, Agent: "swarm:" + env.UnitID,
		Model: e.Model, ModelOf: modelNowFunc(e.Provider, pack.TaskID, env.TranscriptID), Transcript: env.TranscriptID,
		MaxTurns: budget.MaxTurns, StepID: pack.StepID, UnitID: env.UnitID,
	}
	var usageErr error // 用量/里程碑记账失败:不中断执行,收尾时如实带回

	transcriptStore := transcript.NewStore(filepath.Join(e.RootDir, pack.TaskID, "transcripts"))
	// 复测单位(P3-06)的首条消息是发起时确定性组装的复测上下文
	// (自动首条消息机制),不是七要素任务包。
	input := pack.Render()
	if env.retest != nil {
		input = env.retest.firstMessage
	}
	// 原会话续(P2-06f 重启收养 + P3-05c ① 受阻重开 + P3-07 断点续接):
	// 首段不重注入任务包——七要素原文已在恢复出的会话历史里,只追发短
	// 续跑指令(与 P2-06e 模型重试的短指令同一纪律,防续跑消息重复注入);
	// 转录缺失/为空时在首段续跑处降级回全新会话(exhausted 同款接力)。
	if env.resumed {
		switch env.resumeKind {
		case resumeBlocked:
			input = modelRecoveredResumePrompt(pack)
		case resumeCheckpoint:
			input = checkpointResumePrompt(pack)
		case resumeScope:
			input = scopeApprovedResumePrompt(pack)
		default:
			input = adoptionResumePrompt(pack)
		}
	}
	// system 段(§11 组装规范,见 promptspec.go):RoE 约束块置顶(凌驾
	// 声明)+ 三段式基线(A 可编辑/B 铁律/C 写回规约)+ 意图块/锚定
	// 资产/启动指令收尾(不可变块顺序固定)。按领取时授权与任务包冻结,
	// 审批挂起段化续跑重用同一份(system 段逐段稳定)。
	// 复测单位(P3-06)走复测基线(受限工具集对应的规约,标准写回
	// 规约对复测无意义且会误导)。
	var workerSegments []string
	if env.retest != nil {
		workerSegments = RetestSystemSegments(ctx, e.Store, pack.Authorization, env.retest)
	} else {
		workerSegments = WorkerSystemSegments(ctx, e.Store, pack.Authorization, pack)
	}
	billable, gateWait := time.Duration(0), time.Duration(0)
	blockWait := time.Duration(0)
	pauseWait := time.Duration(0)
	// win 当前预算窗基线(P4-01 ②:配了上限时每段预算逐段重算——到线
	// 续跑重置基线;窗内消耗 = 累计值 − 基线。审批/故障/挂起等待不吃
	// 窗内预算,与既有时间账口径一致)。
	win := budgetWindow{}

	// 任务收尾线的冬眠抵扣(P2-08:冬眠期间任务总闸墙钟停走):唤醒续跑
	// 段按累计冬眠时长顺延;未冬眠过 credit=0 原样。单位暂停不抵扣
	//(细粒度暂停只停本单位的 run_seconds,任务墙钟照走)。
	taskDeadline := pack.TaskDeadline
	if !taskDeadline.IsZero() && env.deadlineCredit > 0 {
		taskDeadline = taskDeadline.Add(env.deadlineCredit)
	}

	// R14 拒绝旧计划收场(P2-08 挂起续跑的复检出口):留痕 + 清暂停闩
	//(暂停已履行职责,不是失效)+ 步骤回可领取交主宰重判。
	staleExit := func(why string) {
		_, _ = e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
			Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID,
			Payload: rings.SwarmRecord{
				StepID: pack.StepID, UnitID: env.UnitID, TranscriptID: env.TranscriptID,
				Type: rings.SwarmRecStaleRefused, GraphVersion: pack.GraphVersion,
				Note: "挂起期间前提变化,R14 拒绝旧计划(步骤回可领取,交主宰重判): " + why,
			},
		})
		e.clearUnitLatch(pack.TaskID, pack.StepID)
		res.Reason, res.StepStatus, res.Stale = "pause_stale(R14)", "dispatched", true
	}

	// 舵杆回执去重(adopted/rejected 至多一条;跨段累计)。
	receiptSeen := map[string]bool{}
	// asst 本段助手正文累计(舵杆回执标记扫描源)。
	var asst strings.Builder

	// P2-06e 意图级模型错误退避重跑:requestID 标记每次逻辑注入,重试
	// 只追发短续跑指令、不重注入任务包原文(防续跑消息重复注入);
	// 注入记录守卫保证同一 (requestID, 次数) 的续跑指令至多入会话一次。
	reqSeq := 0
	reqID := nextRequestID(env.UnitID, &reqSeq)
	modelFails := 0
	var failStreak time.Time // 连续失败起算时刻(成功即清零;深阈值只量全不通连续段)
	injectedNotes := map[string]bool{}

	for seg := 0; ; seg++ {
		// ---- P3-07 断点冻结:存档断点武装后,单位在段循环顶收场 ----
		// 步骤回可领取、会话保留可续(Checkpointed 标记);复测单位豁免
		//(侧会话不是任务图上工作,跑完自然落地,进程被杀由悬空收口)。
		if env.retest == nil && e.ckptArmed(pack.TaskID) {
			res.Reason, res.StepStatus, res.Checkpointed = "checkpointed", "dispatched", true
			break
		}
		// ---- P2-08 段间挂起:单位暂停闩在即原地冻结;冬眠门在即收场 ----
		// 工具边界的挂起由 guard 收段后经 continue 回到这里统一处理;
		// 审批续跑/模型重试回到段首的路径同样在此处理(无死角)。
		// P4-33:复测单位豁免冬眠冻结(与断点豁免同款——侧会话跑完自然
		// 落地,不给冬眠收场添等待者)。
		if env.parkArmed || (env.retest == nil && e.hibernating(pack.TaskID)) {
			env.parkArmed = false
			hibernatingNow := e.hibernating(pack.TaskID)
			anchor := e.parkAnchorRev(ledgerCtx, pack.TaskID)
			unitPaused := e.unitLatch(pack.TaskID, pack.StepID) != nil
			hiberGen := uint64(0)
			if e.Hiber != nil {
				hiberGen = e.Hiber.Generation(pack.TaskID)
			}
			e.clockSet(pack.TaskID, env.TranscriptID, billable, false)
			waitStart := time.Now()
			resumed := e.waitUnpark(ctx, env)
			pauseWait += time.Since(waitStart)
			if !resumed {
				res.Reason, res.StepStatus = "aborted", "aborted"
				return res, fmt.Errorf("swarm: 挂起等待被中断: %w", ctx.Err())
			}
			// P3-07:挂起等待期间断点武装 → 落地收场(与段循环顶同款;
			// 挂起中的单位由 waitUnpark 的断点检查唤醒到此处)。
			if env.retest == nil && e.ckptArmed(pack.TaskID) {
				res.Reason, res.StepStatus, res.Checkpointed = "checkpointed", "dispatched", true
				break
			}
			// R14 复检:挂起期间前提变过 → 拒绝旧计划,交主宰重判。
			if why := e.premiseStale(ledgerCtx, pack.TaskID, pack.StepID, anchor); why != "" {
				staleExit(why)
				break
			}
			// 冬眠抵扣收尾线(本次挂起涉及冬眠才重算;总额口径幂等):
			// 入睡时已在冬眠,或等待期间又睡过一觉(代数前进)。无总闸的
			// 任务零值收尾线不加抵扣——零值 + credit 会变成已过期的
			// 非零线,唤醒即被误判总闸到点(P4-02 活体发现)。
			if e.Hiber != nil && (hibernatingNow || e.Hiber.Generation(pack.TaskID) != hiberGen) {
				if credit, cerr := e.Store.HibernateCredit(ledgerCtx, pack.TaskID); cerr == nil {
					env.deadlineCredit = credit
					if !pack.TaskDeadline.IsZero() {
						taskDeadline = pack.TaskDeadline.Add(credit)
					}
				}
			}
			// 段 0 的挂起发生在会话开始前:首发注入(任务包/收养续跑指令)
			// 保持不变,续跑指令只在已有会话的段间替换。
			if seg > 0 {
				input = unitResumePrompt(unitPaused)
			}
		}

		// 活动时钟:段起点标记活动(等待解除回到这里的路径全覆盖)。
		e.clockSet(pack.TaskID, env.TranscriptID, billable, true)

		// ---- 段预算:本预算窗剩余(扣窗内已耗)与任务收尾线取小 ----
		// P4-01 ②:预算窗逐段重算——配了上限时到线不是终点,先收住
		//(引擎 Settlement 已写回)再原会话续跑;窗耗尽的判定在窗基线
		// 上算(续跑重置基线,审批等待/挂起等待不吃窗内预算照旧)。
		// 任务总闸到点不续(硬收,见下方终因处置)。
		if !taskGateReached(taskDeadline) {
			if budget.RunSeconds > 0 && timeDuration(budget.RunSeconds)-(billable-win.billable) <= 0 {
				e.renewBudget(ledgerCtx, pack, env, "timeout", &win, billable, usage.Turns())
			}
			if budget.MaxTurns > 0 && budget.MaxTurns-(usage.Turns()-win.turns) <= 0 {
				e.renewBudget(ledgerCtx, pack, env, "max_turns", &win, billable, usage.Turns())
			}
		}
		remain := time.Duration(0) // 0 = 不限
		if budget.RunSeconds > 0 {
			remain = timeDuration(budget.RunSeconds) - (billable - win.billable)
		}
		maxDur := unitMaxDuration(remain, taskDeadline)
		turnsLeft := 0
		if budget.MaxTurns > 0 {
			turnsLeft = budget.MaxTurns - (usage.Turns() - win.turns)
		}

		segCtx, cancelSeg := context.WithCancel(ctx)
		env.segCancel = cancelSeg
		env.park = nil

		// 单位会话压缩器(P4-39 ②):旧轮工具结果体微压缩(宿主实现,
		// 覆盖本项目工具名)+ 摘要压缩(引擎默认时机与参数,摘要器
		// 从同一 Provider 构造)。窗口未配置(0)不挂——与牧者同款
		// 装配纪律;模型名按当前服务腿解析,主备切换后逐段跟随。
		unitOpts := agentcore.Options{
			Provider:       e.Provider,
			SystemPrompt:   workerSegments,
			Tools:          tools,
			PermissionMode: permission.ModeDefault,
			CanUseTool:     env.approvalGate(pack),
			MaxTurns:       turnsLeft,
			MaxDuration:    maxDur,
			Settlement: &harness.Settlement{
				Prompt:   retestWrapupOf(env),
				MaxTurns: budget.WrapupMaxTurns,
			},
			MaxTokens:              4096,
			WorkingDir:             stepDir,
			Transcript:             transcriptStore,
			SessionID:              env.TranscriptID,
			DisableBackgroundTasks: true,
		}
		if uc := newUnitCompactor(e.Store, pack.TaskID, pack.StepID, env.UnitID,
			failover.ContextWindowFor(ctx, e.Store, unitModelName(e, pack.TaskID, env.TranscriptID)), 4096, e.Provider); uc != nil {
			// 判空再挂:nil *unitCompactor 塞进接口是非 nil 值,引擎会
			// 照调方法(空指针崩溃),必须只在真挂时赋值。
			unitOpts.Compactor = uc
		}
		sess := agentcore.NewSession(unitOpts)
		if seg > 0 {
			// 批准续跑:从转录恢复原会话(消息与累计用量),同一份会话
			// 原地继续——被挂起的调用由模型重新发起,审批门的单次放行
			// 配额匹配后放行。
			if err := sess.Resume(env.TranscriptID); err != nil {
				cancelSeg()
				res.Reason, res.StepStatus = "resume_error", "failed"
				return res, fmt.Errorf("swarm: 恢复会话: %w", err)
			}
		} else if env.resumed {
			// 原会话续(P2-06f 重启收养 + P3-05c ① 受阻重开):恢复崩溃
			// 残留或受阻保留的会话(引擎 cleanResumed 丢弃未收尾的尾部
			// tool_use,模型重新发起)。转录缺失/为空降级回全新会话,重注入
			// 完整任务包。用量不重复计:历史行属上一进程/上一单位,本段
			// 增量自段首基线清零起算(见段首 beginSegment)。
			if err := sess.Resume(env.TranscriptID); err != nil {
				cancelSeg()
				res.Reason, res.StepStatus = "resume_error", "failed"
				return res, fmt.Errorf("swarm: 恢复会话: %w", err)
			}
			if len(sess.Messages()) == 0 {
				input = pack.Render()
			}
		}
		// 段首用量基线清零:KindUsage 是单次 Prompt 运行内的累计(每段
		// 从零起算),跨段不清零会把上一段的累计误当本段基线,产生负
		// 增量行(P2-06f 修订,审批续跑/模型重试/重启收养段同享)。
		usage.beginSegment()
		guard.refreshBudget(deadlineOf(maxDur), win.turns+budget.MaxTurns)

		segStart := time.Now()
		turnsAtSeg := usage.Turns()
		var streamErr, termErr error
		for ev, err := range sess.Prompt(segCtx, input) {
			if err != nil {
				streamErr = err
				break
			}
			switch ev.Kind {
			case harness.KindUsage:
				if ev.Usage != nil {
					if uerr := usage.OnUsage(*ev.Usage); uerr != nil {
						usageErr = uerr
					}
					// 重锚定的剩余预算读数(P2-05):逐轮喂入门卫(近似值,
					// 工具并发期间可能滞后一轮)。
					guard.turns.Store(int64(usage.Turns()))
				}
			case harness.KindToolUse:
				if ev.ToolUse != nil {
					mu.Lock()
					toolNames[ev.ToolUse.ID] = ev.ToolUse.Name
					if len(ev.ToolUse.Input) > 0 {
						toolArgs[ev.ToolUse.ID] = ev.ToolUse.Input
					}
					mu.Unlock()
				}
			case harness.KindToolResult:
				if ev.ToolResult == nil {
					continue
				}
				mu.Lock()
				name := toolNames[ev.ToolResult.ToolUseID]
				args := toolArgs[ev.ToolResult.ToolUseID]
				mu.Unlock()
				text := flattenResult(ev.ToolResult.Content)
				rec := rings.SwarmRecord{
					StepID: pack.StepID, UnitID: env.UnitID, TranscriptID: env.TranscriptID,
					Type: rings.SwarmRecToolCall, GraphVersion: pack.GraphVersion,
					ToolName: name, ToolArgs: args, IsError: ev.ToolResult.IsError,
				}
				// R18 分层:工具已把原始大输出入 blob 并在返回文本中携带引用的,
				// 账本直接记该引用(原文一次落盘);否则此处兜底 spill。
				if ref := extractBlobRef(text); ref != "" && e.Blobs.Exists(ref) {
					rec.ResultRef, rec.ResultHead = ref, clipLine(text, 512)
				} else if ref, head := e.Blobs.spill(text); ref != "" {
					rec.ResultRef, rec.ResultHead = ref, clipLine(head, 512)
				} else {
					rec.ResultHead = clipLine(text, 2048)
				}
				// 工具执行异常落运行日志 ERROR(P3-13b ③;参数与结果原文
				// 不进日志——账本已有,红线:流量正文零记录)。设计内暂缓
				//(审批挂起/舵杆注入/单位挂起)与段收拢打断的在飞调用不落
				// ERROR(P3-14b ①:账本各有完整审计,日志只留轨迹),单位
				// 预算到点打断是机制触发落 WARN,真异常照旧 ERROR。
				if ev.ToolResult.IsError {
					kind := designDeferral(text)
					switch {
					case kind != "":
						runlog.Infof("%s: 任务 %s 单位 %s 工具 %s",
							kind, pack.TaskID, env.UnitID, name)
					case isEngineInterrupt(text):
						// 引擎合成中断(norma drainSynthetic)。segCtx 已取消 =
						// 段收拢(审批挂起/舵杆注入/单位挂起/任务收场)打断,
						// 与设计内暂缓同族,INFO;segCtx 未取消 = 单位墙钟预算
						// 到点打断在飞调用,单位随后按预算语义收场,WARN。
						if segCtx.Err() != nil {
							runlog.Infof("工具调用中断(段收拢): 任务 %s 单位 %s 工具 %s",
								pack.TaskID, env.UnitID, name)
						} else {
							runlog.Warnf("工具调用中断(预算到点): 任务 %s 单位 %s 工具 %s",
								pack.TaskID, env.UnitID, name)
						}
					default:
						runlog.Errorf("工具执行异常: 任务 %s 单位 %s 工具 %s: %s",
							pack.TaskID, env.UnitID, name, clipLine(text, 200))
					}
				}
				if _, aerr := e.Store.Append(ledgerCtx, pack.TaskID, rings.Event{
					Kind: rings.EventSwarmRecord, Actor: "swarm:" + env.UnitID, Payload: rec,
				}); aerr != nil {
					// 记账失败不中断执行,但结果必须如实带回。
					runlog.Errorf("账本写入失败: 工具调用 %s/%s: %v", pack.TaskID, env.UnitID, aerr)
					return res, fmt.Errorf("swarm: 工具调用写入: %w", aerr)
				}
			case harness.KindText:
				// 助手正文累计(P2-08 舵杆回执标记的扫描源;段末统一扫)。
				if ev.Text != "" {
					asst.WriteString(ev.Text)
				}
			case harness.KindResult:
				if ev.Terminal != nil {
					res.Reason = string(ev.Terminal.Reason)
					res.Turns = ev.Terminal.Turns
					res.FinalText = ev.Terminal.Text
					res.StepStatus = stepStatusFor(ev.Terminal.Reason)
					termErr = ev.Terminal.Err
				}
			}
		}
		elapsed := time.Since(segStart)
		_ = sess.Close()
		cancelSeg() // 段 ctx 用毕即收(已结束的段,取消无副作用)

		// 舵杆回执扫描(P2-08):助手正文中的采纳/拒绝标记 → 回执落账
		//(已采纳/被拒;去重,未表态落地停在已送达,不伪造回执)。
		if e.Helm != nil {
			for _, r := range scanSteerReceipts(asst.String()+"\n"+res.FinalText, receiptSeen) {
				e.Helm.RecordVerdict(pack.TaskID, pack.StepID, env.UnitID, r)
			}
		}
		asst.Reset()

		// ---- P2-06e/P4-27 ①⑥ 模型故障:长退避续试,确定性立即受阻 ----
		// 非取消的流断与 model_error 终因都是模型故障:退避等待不计
		// 预算(blockWait),重试只追发短续跑指令(requestID 防续跑
		// 消息重复注入);瞬时失败长退避循环(1m/5m/15m,模型不通
		// 持续等),仅连续失败跨深阈值才落受阻;确定性拒绝(4xx/
		// 超长)重试无意义,立即受阻且不计故障(归因 determinant)。
		if failBrief, failErr, ok := modelFailure(segCtx, streamErr, termErr, res); ok {
			// 时间账:本段有成功轮次照计 billable,零进展段归 block_wait
			//(故障不吃预算)。
			if usage.Turns() > turnsAtSeg {
				billable += elapsed
			} else {
				blockWait += elapsed
			}
			// 活动时钟:退避等待前冻结(读数停在已收拢的 billable)。
			e.clockSet(pack.TaskID, env.TranscriptID, billable, false)
			modelFails++
			if failStreak.IsZero() {
				failStreak = time.Now()
			}
			// 确定性拒绝:立即受阻不重试(P4-27 ⑥)。
			if cls := failover.Classify(context.Background(), failErr); cls == failover.ClassDeterminant || cls == failover.ClassTooLong {
				runlog.Errorf("模型确定性拒绝(不重试): 任务 %s 单位 %s: %s", pack.TaskID, env.UnitID, failBrief)
				res.Reason, res.StepStatus = blockedReasonDeterminant(failBrief), StepBlocked
				res.BlockedClass = BlockedClassDeterminant
				break
			}
			deep := e.modelBlockedDeepAfter(ctx)
			streak := time.Since(failStreak)
			landBlocked := func() {
				res.Reason, res.StepStatus = blockedReason(modelFails, streak, failBrief), StepBlocked
				res.BlockedClass = BlockedClassModelFault
			}
			// 深阈值:连续失败跨线(全不通)才受阻;此前长退避续试。
			if streak >= deep {
				landBlocked()
				break
			}
			noteKey := reqID + "#" + strconv.Itoa(modelFails)
			if injectedNotes[noteKey] {
				// 守卫命中:同一次重试的续跑指令已在会话里,不再重复注入
				//(不变式被破坏的防御路径,按受阻收场)。
				landBlocked()
				break
			}
			wait := e.modelRetryBackoff(modelFails, streak, deep)
			runlog.Warnf("模型错误重跑: 任务 %s 单位 %s 第 %d 次(已连续失败 %s,再不通 %s 后受阻): %s",
				pack.TaskID, env.UnitID, modelFails, roundDur(streak), roundDur(deep-streak), failBrief)
			if !sleepInterruptible(ctx, wait) {
				res.Reason, res.StepStatus = "aborted", "aborted"
				return res, fmt.Errorf("swarm: 模型重试等待被中断: %w", ctx.Err())
			}
			blockWait += wait
			injectedNotes[noteKey] = true
			input = modelRetryNote(reqID, modelFails)
			continue
		}
		billable += elapsed
		// 模型恢复:连续失败计数与起算时刻清零(深阈值只量"全不通"
		// 的连续段;恢复过就重新起算)。
		modelFails, failStreak = 0, time.Time{}
		// ---- P4-51 ①:范围等待批准——单位冻结收场让位名额 ----
		// 命中范围外的命令拦下出待决卡后,单位收段冻结:goroutine 退出
		//(并发名额让位给其他方向)、会话冻结保留(scope_wait 标记,批
		// 准后重领原会话 Resume 续跑)、计时停走(等待不进任何时间账)。
		// 裁决与唤醒走面板两键/RoE 面板的对账通道,单位侧不驻留等待
		//(与弹门挂起占名额等待是两套纪律:弹门等待是既有纪律不动)。
		if env.scopeHold != nil {
			e.clockSet(pack.TaskID, env.TranscriptID, billable, false)
			res.Reason, res.StepStatus, res.ScopeWait = "scope_wait", rings.StepScopeWait, true
			break
		}
		// P4-51 ②:目标已获准但本单位闭包是领取时快照——步骤回可领取,
		// 重领的新任务包含该目标,原会话 Resume 续跑重发命令(与断点收
		// 场同款落地)。
		if env.scopeReclaim {
			res.Reason, res.StepStatus, res.Checkpointed = "scope_reclaim", "dispatched", true
			break
		}
		// ---- P2-08 工具边界/段间挂起:先于流错误结案 ----
		// guard 收段后引擎可能以 aborted 终态或段 ctx 流错误收段,两者都
		// 是挂起信号而非失败(段是被挂起收拢的);单位在被暂缓调用后自然
		// 完成的是落地(真实终态,暂停闩随落地清理留痕);ctx 已结束
		//(kill/熔断)不进挂起,维持 aborted 落地语义。
		if ctx.Err() == nil &&
			(env.parkArmed || (env.retest == nil && e.hibernating(pack.TaskID))) &&
			res.StepStatus != "done" && res.StepStatus != "exhausted" &&
			res.StepStatus != StepApprovalDenied {
			continue
		}
		if streamErr != nil {
			res.Reason, res.StepStatus = "stream_error", "failed"
			return res, fmt.Errorf("swarm: 执行流错误: %w", streamErr)
		}

		// ---- 弹门挂起:等裁决(计时暂停,等待期无活动引擎时钟) ----
		if env.park != nil {
			// P3-07 断点冻结:审批票过期留痕,恢复续跑后重新过门(挂起中
			// 的调用未执行,不构成已裁决;进程级收场不带票过夜)。
			if env.retest == nil && e.ckptArmed(pack.TaskID) {
				_ = e.Gate.Expire(pack.TaskID, env.park.Req.ID, "断点存档,审批票过期;恢复续跑后重新过门")
				env.park = nil
				res.Reason, res.StepStatus, res.Checkpointed = "checkpointed", "dispatched", true
				break
			}
			// 挂起锚点(P2-08):图版本与冬眠代数——等待期间任务冬眠过的,
			// 批准续跑前按 R14 复检、收尾线按冬眠时长顺延。
			env.ticketRev, _ = e.Store.CurrentRevision(ledgerCtx, pack.TaskID)
			if e.Hiber != nil {
				env.ticketGen = e.Hiber.Generation(pack.TaskID)
			}
			// 活动时钟:弹门等待前冻结(等待期计时暂停的可视面)。
			e.clockSet(pack.TaskID, env.TranscriptID, billable, false)
			waitStart := time.Now()
			d, werr := env.park.Wait(ctx)
			gateWait += time.Since(waitStart)
			if werr != nil {
				// 等待期间任务熔断/收尾:按拒绝结案留痕,单位随任务 ctx 中止。
				_ = e.Gate.Expire(pack.TaskID, env.park.Req.ID, "等待审批期间任务收尾/终止,按拒绝结案")
				env.park = nil
				res.Reason, res.StepStatus = "aborted", "aborted"
				return res, fmt.Errorf("swarm: 审批等待被中断: %w", ctx.Err())
			}
			if d.Released {
				// P4-33 冬眠收场放行:票面保持待裁决冻结(零裁决记录),
				// 单位收场落地——步骤回可领取、会话保留可续;唤醒后重领
				// 原会话续跑,重新过门(与断点收场同款落地)。
				env.park = nil
				res.Reason, res.StepStatus, res.Checkpointed = "checkpointed", "dispatched", true
				break
			}
			if d.Approved {
				// 冬眠跨弹门(P2-08:唤醒后批准的单位继续执行,继续前按 R14
				// 复检):等待期间任务冬眠过(代数前进)才复检,普通审批等待
				// 不引入额外复检(P2-06b 语义不变)。
				if e.Hiber != nil && e.Hiber.Generation(pack.TaskID) != env.ticketGen {
					if why := e.premiseStale(ledgerCtx, pack.TaskID, pack.StepID, env.ticketRev); why != "" {
						staleExit(why)
						break
					}
					if credit, cerr := e.Store.HibernateCredit(ledgerCtx, pack.TaskID); cerr == nil {
						env.deadlineCredit = credit
						// 零值收尾线(无总闸)不加抵扣,同段间挂起路径。
						if !pack.TaskDeadline.IsZero() {
							taskDeadline = pack.TaskDeadline.Add(credit)
						}
					}
				}
				// 批准续跑:执行侧授权(单次放行配额;代理旁路组同时
				// 武装单位级放行,P4-17)+ 续跑指令(裁决账已由 Decide 先记)。
				env.armGateApproval(env.park.Req)
				input = approvalResumePrompt(env.park.Req, d)
				reqID = nextRequestID(env.UnitID, &reqSeq) // 新逻辑注入,新 requestID
				env.park = nil
				continue
			}
			// 面板拒绝:拒绝即熔断该单位会话(单位级终止,第四章 §3 v2)。
			env.deniedReason = "审批拒绝终止: 面板拒绝" + reasonSuffix(d.Reason)
			env.park = nil
			break
		}
		// ---- P4-01 ②:预算到线 → 原会话续跑(每段预算逐段重算) ----
		// 引擎已跑 Settlement 把发现写回(transcript 可查);任务总闸到点
		// 不续——硬收落 exhausted(总闸永远在)。续跑不重注入任务包
		//(七要素已在会话历史),只追发短续跑指令(与其他续跑同纪律)。
		if res.StepStatus == "exhausted" && !taskGateReached(taskDeadline) {
			e.renewBudget(ledgerCtx, pack, env, res.Reason, &win, billable, usage.Turns())
			input = budgetResumePrompt(pack)
			continue
		}
		// 正常终态或拒绝线熔断:段循环结束。
		break
	}

	// 拒绝即熔断:Step 终态"审批拒绝终止"(主宰收割可见原因,被拒单位
	// 不得换法重试——终态步骤不再派发)。
	if env.deniedReason != "" {
		res.Reason, res.StepStatus = env.deniedReason, StepApprovalDenied
	}
	if res.StepStatus == "" {
		res.Reason, res.StepStatus = "no_terminal", "failed"
		return res, errors.New("swarm: 未收到终止事件")
	}
	// 时间账(第四章 §3 v2 审批等待 / P2-06e 模型故障 / P2-08 挂起等待,
	// 计时暂停对账):billable 只含有效执行段,gate_wait 为弹门等待合计,
	// block_wait 为模型故障等待合计(退避等待与零进展失败段),pause_wait
	// 为单位暂停/任务冬眠挂起合计——session_finished note 里可对账
	//(等待都不吃预算)。
	res.billable, res.gateWait, res.blockWait, res.pauseWait = billable, gateWait, blockWait, pauseWait
	if res.Stale {
		// 挂起期间前提变化(R14 复检拒绝):与领取时复检同款出口,
		// 步骤已回 dispatched 待重领(交主宰重判)。
		return res, ErrStaleGraph
	}
	if usageErr != nil {
		// 执行本体成功,横切记账有缺口——如实带回(不静默吞掉审计线索)。
		return res, usageErr
	}
	return res, nil
}

// StepApprovalDenied Step 终态:审批拒绝终止(P2-06b,第四章 §3 v2
// "拒绝 = 熔断该虫群单位会话")。终态步骤不再派发——被拒单位不得换法
// 重试由状态机物理保证;主宰收割在快照「审批拒绝」节看到原因后重新研判。
const StepApprovalDenied = "approval_denied"

// StepBlocked Step 终态:模型故障受阻(P2-06e,第七章「故障与恢复
// 语义」)。意图级模型错误退避重跑耗尽后落下,原因随 session_finished
// note 与黑匣子 unit_blocked 可查;区别于 failed(不可归因模型故障)与
// exhausted(预算):blocked 等配置恢复/切换自动重开(终态任务除外)。
const StepBlocked = "blocked"

// deadlineOf 段墙钟 → 门卫读数用的绝对期限(0 = 不限)。
func deadlineOf(maxDur time.Duration) time.Time {
	if maxDur <= 0 {
		return time.Time{}
	}
	return time.Now().Add(maxDur)
}

// retestWrapupOf 收尾指令按单位类型取(复测单位的收尾是写回复测结论,
// 不是候选回流,P3-06)。
func retestWrapupOf(env *unitEnv) string {
	if env.retest != nil {
		return retestWrapupPrompt
	}
	return wrapupPrompt
}

// reasonSuffix 拒绝理由的后缀拼接(空理由如实省略)。
func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return ": " + reason
}

// approvalResumePrompt 批准后的续跑指令(进会话上下文,transcript 可查:
// 续跑单位看到的是明确的审批结果,不是凭空继续)。P4-52 ②:批准附意
// 见(面板意见框)非空时以造物主指令语义注入——意见全文随放行进入原
// 会话(舵杆注入通道同族:引擎注入 user 消息,transcript 与审批记录双
// 处可对账),单位须遵照执行。
func approvalResumePrompt(req ApprovalRequest, d GateDecision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【审批结果 · 续跑】面板已批准请求 %s(意图:%s;工具 %s;参数 %s)。",
		req.ID, req.Intent, req.ToolName, clipLine(req.ToolArgs, 400))
	if r := strings.TrimSpace(d.Reason); r != "" {
		fmt.Fprintf(&b, "造物主批准附言(请遵照执行):%s。", r)
	}
	b.WriteString("该次调用已被单次授权,可直接重新发起;等待期间该单位的运行预算未消耗,请继续完成本步骤意图。")
	if req.GateGroup == HardGroupProxyBypass {
		b.WriteString("本单位后续命中代理旁路组的命令已一并获准,可直接执行。")
	} else if req.GateGroup == "" {
		b.WriteString("本步骤的后续命令已一并获准,可直接执行。")
	}
	return b.String()
}

// adoptionResumePrompt 重启收养的续跑指令(P2-06f,进会话上下文,
// transcript 可查:续跑单位看到的是明确的收养声明,不是凭空继续)。
// 任务包七要素原文已在恢复出的会话历史里,不重注入——防续跑消息
// 重复注入(与 P2-06e 模型重试的短指令同一纪律)。
func adoptionResumePrompt(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【重启接管 · 续跑】执行进程重启,本会话已从转录完整恢复(历史消息与已耗用量俱在)。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("已执行过的动作与已写回的结论勿重复;若意图已近完成,直接收尾写回最终结论。本单位的运行预算已按新领取重置。")
	return b.String()
}

// modelRecoveredResumePrompt 受阻重开的续跑指令(P3-05c ①,进会话
// 上下文):模型配置恢复,本会话从转录完整恢复——上下文零重建,
// 从中断点继续,不是从头再来。
func modelRecoveredResumePrompt(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【恢复 · 续跑】模型故障已解除(配置恢复或切换),本会话已从转录完整恢复(历史消息与已耗用量俱在)。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("此前因模型错误未完成的动作可重新发起;已执行过的动作与已写回的结论勿重复。本单位的运行预算已按新领取重置。")
	return b.String()
}

// checkpointResumePrompt 断点续接的续跑指令(P3-07,进会话上下文,
// transcript 可查):进程重启后从账本续跑——本会话从转录完整恢复,
// 上下文零重建,从断点继续,不是从头再来。
func checkpointResumePrompt(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【断点 · 续跑】执行进程重启,任务从账本续跑,本会话已从转录完整恢复(历史消息与已耗用量俱在)。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("断点存档前被暂缓的调用可重新发起;已执行过的动作与已写回的结论勿重复;若意图已近完成,直接收尾写回最终结论。本单位的运行预算已按新领取重置。")
	return b.String()
}

// scopeApprovedResumePrompt 范围批准的续跑指令(P4-51 ②,进会话上下文,
// transcript 可查):此前等待批准的目标已加入任务范围,新任务包闭包已
// 含该目标——被拦下的命令可直接重新发起(会自然通过范围判定),从暂
// 停点继续,不是从头再来。
func scopeApprovedResumePrompt(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【范围批准 · 续跑】此前等待批准的范围外目标已加入任务范围,本会话已从转录完整恢复(历史消息与已耗用量俱在)。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("此前被拦下等待批准的命令现在可以重新发起;已执行过的动作与已写回的结论勿重复;若意图已近完成,直接收尾写回最终结论。本单位的运行预算已按新领取重置。")
	return b.String()
}

// budgetResumePrompt 预算续跑指令(P4-01 ②,进会话上下文,transcript
// 可查):上一段预算到线,引擎收尾已把发现写回图——本会话从转录完整
// 恢复,新一段预算已重算,从暂停点继续。不重注入任务包(七要素已在
// 会话历史,防续跑消息重复注入,与其他续跑同纪律)。
func budgetResumePrompt(pack *TaskPack) string {
	var b strings.Builder
	b.WriteString("【预算 · 续跑】上一段运行预算到线,收尾已把发现写回图。本会话已从转录完整恢复,新一段运行预算已重算。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("收尾时没来得及写回的残留发现先写回,再继续未完成的动作;已写回的结论勿重复;若意图已近完成,直接收尾写回最终结论。")
	return b.String()
}

// budgetWindow 当前预算窗的消耗基线(P4-01 ②:配了上限时每段预算逐段
// 重算)。窗内消耗 = 累计值 − 基线;到线续跑重置基线(renewBudget)。
type budgetWindow struct {
	billable time.Duration // 窗起点前的累计有效执行时长
	turns    int           // 窗起点前的累计模型轮次
}

// taskGateReached 任务收尾线是否已到(P4-01 ②:总闸到点不续跑,硬收落
// exhausted——任务总闸永远在)。零值(未设总闸)恒 false。
func taskGateReached(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

// renewBudget 预算到线续跑(P4-01 ②):重算预算窗基线 + 黑匣子留痕
// (第四章 §6:预算状态变化入黑匣子通道)。billable/turns 为当前累计
// 有效时长与轮次(新窗从零起算)。
func (e *Executor) renewBudget(ctx context.Context, pack *TaskPack, env *unitEnv, reason string, win *budgetWindow, billable time.Duration, turns int) {
	win.billable, win.turns = billable, turns
	runlog.Infof("预算到线续跑: 任务 %s 单位 %s(原因 %s,收尾已写回,预算重算)",
		pack.TaskID, env.UnitID, reason)
	if err := blackbox.Record(ctx, e.Store, pack.TaskID, "swarm:"+env.UnitID,
		rings.BlackboxRecord{
			Type: rings.BlackboxBudgetRenewal, StepID: pack.StepID, UnitID: env.UnitID,
			Detail: map[string]any{
				"reason": reason, "turns": turns, "billable_seconds": billable.Seconds(),
				"note": "预算到线,收尾已写回,原会话续跑(预算重算)",
			},
		}); err != nil {
		runlog.Errorf("黑匣子写入失败: 预算续跑 %s/%s: %v", pack.TaskID, env.UnitID, err)
	}
}

// stepStatusFor 引擎终因 → step 终态(R16:预算耗尽是 exhausted 不是
// failed;熔断/取消是 aborted,中性可重派)。
func stepStatusFor(r harness.TerminalReason) string {
	switch r {
	case harness.ReasonCompleted:
		return "done"
	case harness.ReasonMaxTurns, harness.ReasonTimeout:
		return "exhausted"
	case harness.ReasonAbortedStreaming, harness.ReasonAbortedTools:
		return "aborted"
	default:
		return "failed"
	}
}

// approvalPendingNote 审批暂挂提示的固定前缀(给模型的暂挂消息含它;
// 日志分类据此识别设计内等待,P3-14b ①)。
const approvalPendingNote = "该调用已提交面板审批"

// designDeferral 工具结果是否为设计内暂缓,是则返回日志用类型词:
// 审批挂起(等面板裁决)/ 舵杆注入(操作者纠偏指令送达前暂缓本次调用)/
// 单位挂起(暂停/冬眠)。三者都是机制内等待,账本各有完整审计
// (approval.record / steer.record / unit.control),运行日志只留 INFO
// 轨迹,真异常才落 ERROR。引擎会把拒绝/暂缓理由包装进结果文本,按
// 特征串识别(而非前缀),包装形态变化不误分类。
func designDeferral(text string) string {
	switch {
	case strings.Contains(text, approvalPendingNote):
		return "审批暂挂"
	case strings.Contains(text, deferNote):
		return "纠偏注入暂缓"
	case strings.Contains(text, pauseDeferNote):
		return "单位挂起暂缓"
	}
	return ""
}

// engineInterruptNote norma v0.4.1 引擎合成中断的结果文本(streaming.go
// drainSynthetic:toolCtx 取消时对未落成结果的工具合成)。与拒绝文案的
// 落成存在竞态——审批挂起等段收拢场景下可能替代暂挂文案出现,故日志
// 分类须一并识别,否则偶发落 ERROR。
const engineInterruptNote = "tool execution interrupted"

// isEngineInterrupt 工具结果是否为引擎合成中断:合成结果只有这一段
// 文本,trim 后整串相等才认(包含匹配会误吞恰好含该短语的真实输出)。
func isEngineInterrupt(text string) bool {
	return strings.TrimSpace(text) == engineInterruptNote
}

// approvalGate 安全闸门的执行层挂接点(Norma CanUseTool 回调,第二章
// §3③):三级处置(第四章 §3 v2,P2-06b 重构;v2.1 判定链 P2-08e;P4-63
// 拒动作不杀单位)。审批对象是 Step 意图(分级 + 危害声明随任务包),
// 不是工具。判定次序:
//
//	① 拒绝线·超 RoE 授权范围:工具不在 RoE 边界内直接拒绝留痕
//	   (工具表已经过 RoE 过滤,此处二次校验防未来工具动态发现绕过
//	   静态过滤);
//	② 拒绝线·全局内置硬规则:参数级确定性正则 + 三档判定位(P4-63 A,
//	   动作位拒/非动作位放/模糊弹门)——不依赖模型声明(补 harm_class
//	   漏报的洞),插在意图分级之前,分级无论怎么声明都拦;确定性拒绝
//	   只拒本次动作不熔断单位,同规则累计 3 次升级杀(P4-63 B);
//	③ 拒绝线·任务级意图拦截:资产范围 × 动作类型 × 处置(P2-08e)
//	   ——禁止命中/超额只拒本次调用并留痕,同规则累计 3 次升级杀
//	   (P4-63 B);允许 + 次数上限计数入账,超额自动转禁止(量化额度);
//	④ 拒绝线·分级缺失/非法:fail-safe 拒绝(派发侧本应校验,此处兜住
//	   绕过 overmind.DispatchStep 的直接入账);危害声明非法同理;
//	⑤ 拒绝线·非常严重:数据销毁/拒绝服务类(harm_class)直接拒绝且
//	   留痕,面板提供"研判后放行"逐次放行——无论门槛多宽都不过弹门;
//	⑥ 弹门线的单次放行配额:批准续跑后模型重新发起同一调用(同工具
//	   同参数),消费配额放行(配额只来自面板 Decide,对话内容不构成
//	   决策通路——R9);
//	⑦ 弹门线·达到审批门槛:意图分级 ≥ RoE 审批门槛(默认宽:仅增删改
//	   弹门)→ 挂起等面板裁决:登记审批票 → 收拢当前执行段(等待期
//	   该单位计时暂停)→ 批准后 Resume 续跑/拒绝即熔断该单位会话;
//	   无门可用 fail-safe 拒绝(没有人在环,就不放行弹门级动作);
//	⑧ 其余一律放行留痕:不列举、不预判(留痕=tool_call 执行记录,
//	   全量记录兜底审计)。
//
// 熔断该单位会话的拒绝(①④⑤与升级保险触发):单位级终止,Step 终态
// "审批拒绝终止"(approval_denied),主宰收割可见原因。面板拒绝(弹门
// 拒)同样熔断——人否决的是这个单位正在做的事,语义不同(P4-63 B⑥)。
// 硬规则与意图拦截的确定性拒绝只拒那一条命令,单位存活读到反馈自行换
// 路。只读工具(引擎侧 ReadOnly,如 port_probe 与受控回流入口)在权限
// 管线第 3 阶段自动放行,不进本回调——回流入口只写自家账本、不碰目标,
// 不设门是防审批疲劳,RoE 工具过滤照常生效。
func (env *unitEnv) approvalGate(pack *TaskPack) permission.CanUseTool {
	return func(ctx context.Context, toolName string, input json.RawMessage, _ permission.Context) permission.Decision {
		// denyUnit 拒绝线处置:留痕 → 熔断单位 → 拒绝该次调用。
		denyUnit := func(denyLine, reason string) permission.Decision {
			env.denyGuard(pack, toolName, input, denyLine, reason)
			return permission.Denied(reason)
		}
		// ① 超 RoE 授权范围。
		if !pack.Authorization.Allows(toolName) {
			return denyUnit(rings.DenyLineRoeScope,
				"超出 RoE 授权范围:工具 "+toolName+" 不在授权边界内(拒绝线)")
		}
		// ② 全局内置硬规则(P2-08e):读规则用独立 ctx——收尾/熔断路径
		// 的判定不因任务 ctx 取消而失守(fail-safe 方向:宁可多拦)。
		// P4-03 ②:处置为弹门的规则(内置代理旁路组)命中改人审——
		// 挂起等面板裁决,审批卡带命令原文与命中上下文(如"怀疑观测
		// 被镜污染、直连对照复测"由人判);批准后模型重发同调用,先查
		// 放行配额(⑥ 同语义,查在拦前——否则批准的重发被再次拦住);
		// 拒绝照旧熔断。其余处置(拒绝)命中直接拒绝留痕。
		// P4-17:该单位代理旁路组已批准过一次的,同组任何命中(不同
		// 规则、不同参数)直接放行执行、逐次留痕,不再弹门。
		// P4-63 A:系统破坏/数据库破坏组按三档判定位收窄——动作位拒、
		// 非动作位放行不当动作、模糊形态弹门人审(人即兜底)。
		// P4-63 B:确定性拒绝只拒本次动作不熔断单位(denyHardAction),
		// 同规则累计 3 次升级杀;面板弹门拒绝照旧熔断(drive 侧)。
		if r, m, tier, hit := matchHardRule(guardLedgerCtx, env.Executor.Store, toolName, input); hit {
			if r.Action == HardActionGate || tier == hardTierAmbiguous {
				if a := env.allowance; a != nil && a.toolName == toolName && jsonEquiv(a.args, string(input)) {
					env.allowance = nil
					return permission.Allowed()
				}
				if env.bypassPass != nil && r.Group == HardGroupProxyBypass {
					env.recordGateGroupPass(pack, r, m, toolName, input)
					return permission.Allowed()
				}
				if env.Executor.Gate == nil {
					return permission.Denied(env.denyHardAction(pack, toolName, input, r, m, rings.DenyLineNoGate,
						"无审批门可用(fail-safe):命中拦截规则(需人审处置)的动作无人在环,本次调用拒绝"))
				}
				note := hardRuleGateNote(r, m)
				if tier == hardTierAmbiguous {
					note += " · 判位模糊,待批准或拒绝"
				}
				ticket, err := env.Executor.Gate.Enter(ApprovalRequest{
					TaskID: pack.TaskID, StepID: pack.StepID, UnitID: env.UnitID,
					Intent: pack.StepIntent, ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass,
					ToolName: toolName, ToolArgs: string(input), Note: note,
					GateGroup: r.Group,
				})
				if err != nil {
					return permission.Denied("审批请求登记失败(fail-safe): " + err.Error())
				}
				env.park = ticket
				env.fuseSegment()
				return permission.Denied(fmt.Sprintf(
					"%s(请求 %s):命令命中拦截规则(%s · %s),已提交面板审批,批准后继续执行",
					approvalPendingNote, ticket.Req.ID, r.Group, strings.TrimPrefix(r.Name, HardRuleBuiltinPrefix)))
			}
			return permission.Denied(env.denyHardAction(pack, toolName, input, r, m, rings.DenyLineHardRule, ""))
		}
		// ②b 范围外拦截(P4-51 ①,问题清单 #73②):渗透任务的命令目标
		// 不在种子闭包内——拦下出待决卡(同一目标一张卡)＋单位冻结收
		// 场让位名额(denyScope 内部完成出卡/分流/置位,drive 收段后落
		// scope_wait 态);已排除目标(禁止测试/摘除)直接拒留痕熔断。
		// 只读探测工具在门卫补口同判,bash 经由本回调。
		if reason := env.denyScope(pack, toolName, input); reason != "" {
			return permission.Denied(reason)
		}
		// ③ 任务级意图拦截(P2-08e,审核迭代:现读+范围过滤+不熔断):
		// 禁止命中 / 超额只拒本次调用并留痕,单位继续其余工作(告知走
		// 舵杆定向投递,见 server 侧规则变更端点);允许 + 次数上限在此
		// 计数入账。P4-63 B:同规则累计 3 次拒绝升级杀单位(防硬撞)。
		if reason, rid, deny := env.checkIntentRules(pack, toolName, input); deny {
			return permission.Denied(env.denyIntentAction(pack, toolName, input, reason, rid))
		}
		// ④ 分级缺失/非法、危害声明非法:fail-safe。
		if ActionLevelRank(pack.ActionLevel) == 0 {
			return denyUnit(rings.DenyLineLevelInvalid, errActionLevelMissing(pack.ActionLevel).Error())
		}
		if pack.HarmClass != "" && !rings.IsValidHarmClass(pack.HarmClass) {
			return denyUnit(rings.DenyLineHarm,
				fmt.Sprintf("危害声明非法(%q),拒绝线 fail-safe 拒绝", string(pack.HarmClass)))
		}
		// ⑤ 非常严重项:数据销毁/拒绝服务类直接拒绝(P4-51 ⑤:"规划后
		// 放行"整体废除,拒绝即终局)。
		if pack.HarmClass == rings.HarmDataDestruction || pack.HarmClass == rings.HarmDenialOfService {
			harmWord := "数据销毁"
			if pack.HarmClass == rings.HarmDenialOfService {
				harmWord = "拒绝服务类"
			}
			return denyUnit(rings.DenyLineHarm,
				"拒绝线(非常严重):"+harmWord+"动作直接拒绝,不提供放行通道")
		}
		// ⑥ 批准续跑的单次放行配额:同请求(同工具同参数)恰好一次。
		// 参数比对按 JSON 语义等价(键序与转义形态无关)——续跑重发的
		// 参数经会话历史往返可能改变转义形态,字节级比对会误判为不同
		// 调用。不同参数的调用不消费配额,重新过门。
		if a := env.allowance; a != nil && a.toolName == toolName && jsonEquiv(a.args, string(input)) {
			env.allowance = nil
			return permission.Allowed()
		}
		// ⑥b 同步骤放行(P4-27 ⑧):审批门槛的首条命令批准一次后,本
		// 单位本步骤的后续命令(不同工具、不同参数,含查看类)直接
		// 放行执行、逐次留痕——门第一次照弹、意图照审,同一意图的
		// 执行延续不再逐条弹门(P4-17 只解决了"同规则不同参数",这
		// 里补"同步骤不同命令")。
		if env.stepPass != nil {
			env.recordStepPass(pack, toolName, input)
			return permission.Allowed()
		}
		// ⑦ 弹门线:达到审批门槛的意图分级挂起等裁决。
		threshold := NormalizeApprovalThreshold(pack.Authorization.ApprovalThreshold)
		if ActionLevelRank(pack.ActionLevel) >= ActionLevelRank(threshold) {
			if env.Executor.Gate == nil {
				return denyUnit(rings.DenyLineNoGate,
					"无审批门可用(fail-safe):需批准级动作须有人在环批准")
			}
			ticket, err := env.Executor.Gate.Enter(ApprovalRequest{
				TaskID: pack.TaskID, StepID: pack.StepID, UnitID: env.UnitID,
				Intent: pack.StepIntent, ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass,
				ToolName: toolName, ToolArgs: string(input),
			})
			if err != nil {
				// 登记失败是账本基础设施故障:fail-safe 拒绝本次调用(不熔断
				// 单位——这不是审批裁决,错误如实透传由收尾带回)。
				return permission.Denied("审批请求登记失败(fail-safe): " + err.Error())
			}
			// 挂起:收拢当前执行段(被挂起调用未执行),单位停在票上等裁决。
			env.park = ticket
			env.fuseSegment()
			return permission.Denied(fmt.Sprintf(
				"%s(请求 %s),本回合暂挂;批准后将继续执行", approvalPendingNote, ticket.Req.ID))
		}
		// ⑧ 其余一律放行留痕(留痕=tool_call 执行记录)。
		return permission.Allowed()
	}
}

// gateAllowance 批准续跑的单次放行配额:面板批准后单位恢复,模型重新
// 发起同一调用时恰好放行一次(不同调用重新过门)。配额只在 drive 收到
// Decide 送达的批准时设置——对话内容不构成决策通路(R9)。
type gateAllowance struct {
	requestID string
	toolName  string
	args      string
}

// gateGroupPass P4-17 代理旁路组的单位级放行:面板对该组弹门批准一次后
// 武装,该单位后续命中该组任何弹门规则(组内不同规则、不同参数)直接
// 放行执行、逐次留痕。"永久"=单位生命周期——一 Step 一单位,销毁即
// 失效,任务内新单位重新弹门一次;拒绝照旧熔断。其他弹门规则不享受:
// 用户自建 gate 规则批准一次仅放行同参数那一次(现状不变)。放行只对
// 弹门处置生效——组内被改成拒绝处置的条目照旧拒绝,不越过用户意志。
type gateGroupPass struct {
	originRequestID string // 触发放行的批准请求(逐次留痕回指)
}

// gateStepPass P4-27 ⑧ 同步骤放行:审批门槛(意图分级)弹门批准一次后
// 武装,该单位本步骤后续命令不再逐条弹门(直接执行、逐次留痕)。
// 与 gateGroupPass 的分工:组级放行管硬规则弹门(代理旁路组),步骤级
// 放行管审批门槛(分级线);"同一单位同一步骤"=单位生命周期,一 Step
// 一单位,销毁即失效,任务内新单位重新弹门一次。
type gateStepPass struct {
	originRequestID string // 触发放行的批准请求(逐次留痕回指)
}

// armGateApproval 批准续跑的执行侧授权(P4-17;P4-27 ⑧):单次放行配额
// 恒置(同工具同参数恰好一次,重发同调用走配额不另留痕);来源是代理
// 旁路组弹门时同时武装单位级组放行(后续同组任何命中直接执行);来源
// 是审批门槛弹门(无 GateGroup)时同时武装同步骤放行(本步骤后续命令
// 不再逐条弹门)。用户自建 gate 规则不武装步骤级放行(P4-17 语义保持:
// 批准一次仅放行同参数那一次)。
func (env *unitEnv) armGateApproval(req ApprovalRequest) {
	env.allowance = &gateAllowance{requestID: req.ID, toolName: req.ToolName, args: req.ToolArgs}
	switch {
	case req.GateGroup == HardGroupProxyBypass:
		env.bypassPass = &gateGroupPass{originRequestID: req.ID}
	case req.GateGroup == "":
		env.stepPass = &gateStepPass{originRequestID: req.ID}
	}
}

// recordStepPass P4-27 ⑧ 同步骤放行的逐次留痕:每次经放行直接执行的
// 命令落一笔 step_pass 审批记录(带命令原文,与弹门流的审批卡同口径,
// 可对账)。留痕失败不挡执行——放行依据是先前的面板批准,账本基础
// 设施故障由 tool_call 侧的写入路径如实暴露。
func (env *unitEnv) recordStepPass(pack *TaskPack, toolName string, input json.RawMessage) {
	env.recordApproval(rings.ApprovalRecord{
		StepID: pack.StepID, UnitID: env.UnitID, Type: rings.ApprovalRecStepPass,
		ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass,
		Intent: pack.StepIntent, ToolName: toolName, ToolArgs: input,
		DecidedBy: "creator",
		Reason:    fmt.Sprintf("本步骤审批已批准一次(请求 %s),本命令直接放行", env.stepPass.originRequestID),
	})
}

// recordGateGroupPass P4-17 单位级放行的逐次留痕:每次经放行直接执行的
// 命令落一笔 group_pass 审批记录(带命令原文与命中规则,与弹门流的
// 审批卡同口径,可对账)。留痕失败不挡执行——放行依据是先前的面板
// 批准,账本基础设施故障由 tool_call 侧的写入路径如实暴露。
func (env *unitEnv) recordGateGroupPass(pack *TaskPack, r HardRule, matched, toolName string, input json.RawMessage) {
	env.recordApproval(rings.ApprovalRecord{
		StepID: pack.StepID, UnitID: env.UnitID, Type: rings.ApprovalRecGroupPass,
		ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass,
		Intent: pack.StepIntent, ToolName: toolName, ToolArgs: input,
		Note: hardRuleGateNote(r, matched), DecidedBy: "creator",
		Reason: fmt.Sprintf("代理旁路组已批准一次(请求 %s),本命令直接放行", env.bypassPass.originRequestID),
	})
}

// fuseSegment 收拢当前执行段(弹门挂起):取消段 ctx,引擎以 aborted
// 收段——被挂起的调用未执行,等待期无活动引擎时钟(计时暂停)。
func (env *unitEnv) fuseSegment() {
	if env.segCancel != nil {
		env.segCancel()
	}
}

// fuseUnit 拒绝即熔断该单位会话(单位级终止):与挂起共用段收拢机制,
// drive 检测 deniedReason 后以 approval_denied 终态收场。
func (env *unitEnv) fuseUnit() {
	env.fuseSegment()
}

// denyGuard 拒绝线统一处置(P2-08e 起审批门回调与门卫边界共用):
// 留痕(approval.record denied + DenyLine)→ 熔断单位。适用拒绝线:
// RoE 工具边界、分级缺失/危害声明非法 fail-safe、危害类拒绝。
// P4-63 B 起硬规则与意图拦截改走 denyHardAction/denyIntentAction——
// 拒动作不杀单位,同规则累计 3 次升级杀。
func (env *unitEnv) denyGuard(pack *TaskPack, toolName string, input json.RawMessage, denyLine, reason string) {
	env.deniedReason = "审批拒绝终止: " + reason
	env.recordApproval(rings.ApprovalRecord{
		StepID: pack.StepID, UnitID: env.UnitID, Type: rings.ApprovalRecDenied,
		ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass, DenyLine: denyLine,
		Intent: pack.StepIntent, ToolName: toolName, ToolArgs: input, Reason: reason,
	})
	env.fuseUnit()
}

// bumpDenyHits 同单位生命周期内按规则累计确定性拒绝次数(P4-63 B⑤,
// 键 hr:<硬规则ID> / ir:<意图规则ID>,累计不重置),返回累计值。
func (env *unitEnv) bumpDenyHits(key string) int {
	env.denyMu.Lock()
	defer env.denyMu.Unlock()
	if env.denyHits == nil {
		env.denyHits = map[string]int{}
	}
	env.denyHits[key]++
	return env.denyHits[key]
}

// denyHardAction P4-63 B:硬规则确定性拒绝——只拒本次动作(模型读到
// 换路指引,单位存活自行换路),不熔断单位;同规则累计
// HardDenyEscalateAfter 次升级杀单位(防硬撞/试探)。denyLine 取
// hard_rule(命中拒绝)或 no_gate(无门 fail-safe,reason 由调用方给)。
// 留痕走 approval.record denied(Reason 保持 hardRuleDenyReason 的可解析
// 身份格式,升级说明并入同一笔尾注)。返回给模型的拒绝文案。
func (env *unitEnv) denyHardAction(pack *TaskPack, toolName string, input json.RawMessage, r HardRule, matched string, denyLine, reason string) string {
	if reason == "" {
		reason = hardRuleDenyReason(r, matched)
	}
	escalated := false
	if n := env.bumpDenyHits("hr:" + r.ID); n >= HardDenyEscalateAfter {
		escalated = true
		reason += fmt.Sprintf(" · 同规则累计命中 %d 次,升级终止单位", n)
		env.deniedReason = "审批拒绝终止: " + reason
		env.fuseUnit()
	}
	env.recordApproval(rings.ApprovalRecord{
		StepID: pack.StepID, UnitID: env.UnitID, Type: rings.ApprovalRecDenied,
		ActionLevel: pack.ActionLevel, HarmClass: pack.HarmClass, DenyLine: denyLine,
		Intent: pack.StepIntent, ToolName: toolName, ToolArgs: input, Reason: reason,
	})
	text := hardRuleRejectText(r)
	if escalated {
		text += "(同规则累计命中已到上限,单位终止)"
	}
	return text
}

// denyIntentAction 任务级拦截拒绝(P2-08e 起只留痕不熔断;P4-63 B 加
// 升级保险):只拒绝本次调用,单位继续其余工作;同规则累计
// HardDenyEscalateAfter 次升级杀单位。规则变更的提前告知走舵杆定向
// 投递。返回给模型的拒绝文案。
func (env *unitEnv) denyIntentAction(pack *TaskPack, toolName string, input json.RawMessage, reason, ruleID string) string {
	escalated := false
	if n := env.bumpDenyHits("ir:" + ruleID); n >= HardDenyEscalateAfter {
		escalated = true
		reason += fmt.Sprintf(" · 同规则累计命中 %d 次,升级终止单位", n)
		env.deniedReason = "审批拒绝终止: " + reason
		env.fuseUnit()
	}
	env.recordApproval(rings.ApprovalRecord{
		StepID: pack.StepID, UnitID: env.UnitID, Type: rings.ApprovalRecDenied,
		ActionLevel: pack.ActionLevel, DenyLine: rings.DenyLineIntentRule,
		Intent: pack.StepIntent, ToolName: toolName, ToolArgs: input, Reason: reason,
	})
	if escalated {
		return reason + ";同规则累计命中已到上限,单位终止"
	}
	return reason
}

// recordApproval 审批审计流入口(拒绝等不进弹门的判定也要留痕)。
// 账本入账用独立 ctx:判定留痕不可因任务熔断而断账。
func (env *unitEnv) recordApproval(rec rings.ApprovalRecord) {
	// P4-51:范围待决卡自带稳定请求号(sc-<目标>,同一目标一张卡的
	// 去重键)——已设 RequestID 不覆盖;其余记录现生成 ap- 短号。
	if rec.RequestID == "" {
		rec.RequestID = gateRequestID()
	}
	_, _ = env.Executor.Store.Append(ledgerCtx, env.TaskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "swarm:" + env.UnitID, Payload: rec,
	})
}

// ---- 后台 bash 进程登记与回收(P3-05 限时放开) ----

// registerBG 登记一条转后台的 bash 命令(单位粒度)。
func (e *Executor) registerBG(unitID string, r *bgRun) {
	e.bgMu.Lock()
	e.bgRuns[unitID] = append(e.bgRuns[unitID], r)
	e.bgMu.Unlock()
}

// reapUnitBG 单位落地收口:杀残余后台进程(整组),注销登记;输出文件
// 保留(审计与后续读取)。已退出的自然跳过。
func (e *Executor) reapUnitBG(unitID string) {
	e.bgMu.Lock()
	runs := e.bgRuns[unitID]
	delete(e.bgRuns, unitID)
	e.bgMu.Unlock()
	for _, r := range runs {
		if !r.Finished() {
			killBashGroup(r.proc.cmd)
		}
	}
}

// BGNoteMarker 后台完成提醒的稳定标识(transcript 断言用)。
const BGNoteMarker = "【后台命令完成】"

// bgNotes 收集本单位已完成且尚未提醒过的后台命令的完成提醒(每个工具
// 调用边界调用;每条至多一次)。模型据此免盲轮询:转后台的命令一结束,
// 下一个工具调用边界就有事实送达。
func (e *Executor) bgNotes(unitID string) []string {
	e.bgMu.Lock()
	runs := e.bgRuns[unitID]
	e.bgMu.Unlock()
	var out []string
	for _, r := range runs {
		if r.notified || !r.Finished() {
			continue
		}
		r.notified = true
		trunc := ""
		if r.proc.out.Truncated() {
			trunc = "输出已超上限截断(文件尾有省略标记);"
		}
		out = append(out, fmt.Sprintf(
			"%s%s 已结束(%s);%s输出文件在本步骤工作目录,可用 read 或 bash(tail) 读取。",
			BGNoteMarker, r.outFile, exitLineOf(r.proc.waitErr), trunc))
	}
	return out
}

// flattenResult 工具结果块展平为纯文本。
func flattenResult(bs []llm.ContentBlock) string {
	var b strings.Builder
	for _, blk := range bs {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// blobRefRe 匹配工具返回文本中携带的内容寻址引用。
var blobRefRe = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// extractBlobRef 从工具结果文本中提取 blob 引用(工具层 spill 的回指)。
func extractBlobRef(s string) string {
	return blobRefRe.FindString(s)
}

// jsonEquiv 两段 JSON 文本的语义等价判定(解析后 DeepEqual;任一解析
// 失败退化为字节相等)。
func jsonEquiv(a, b string) bool {
	if a == b {
		return true
	}
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
