package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// DefaultMaxConcurrentUnits 虫群并发上限默认值(P1-05):单任务同时在
// 跑的虫群单位至多 5 只。原 3;2026-10-26 造物主拍板上调——5 并发实测
// (GLM glm-5.3,持续约 8 分钟满并发)零限流零退避零切备,详见
// Process/P4进度.md P4-29 调优简记。
const DefaultMaxConcurrentUnits = 5

// DefaultSettleGraceSeconds 收尾宽限默认值(第四章 §6:在跑单位优雅
// 收尾 90 秒,超时硬停)。
const DefaultSettleGraceSeconds = 90

// DefaultAssessWall 单轮研判墙钟默认值(P4-42 ②):15 分钟。
const DefaultAssessWall = 15 * time.Minute

// SettleController 任务级总闸的收尾操控面(P2-04,第四章 §6):主宰循环
// 经它驱动 Manager 的任务生命周期——进入收尾(状态可见)与收尾定终态。
// 装配层注入 Manager 实现;nil = 无操控面(单测用:黑匣子与账本事件照记,
// 内存状态无人翻转)。接口归 overmind 而非引 manager 包,保持主宰不依赖
// 管理器(装配在 cmd)。
type SettleController interface {
	// MarkSettling 进入收尾(started → settling 记账 + 状态翻转)。
	MarkSettling(taskID string, detail map[string]any) error
	// FinishSettling 收尾定终态(achieved → completed,否则 stopped)。
	FinishSettling(taskID string, achieved bool, detail map[string]any) error
}

// Loop 主宰循环(P1-05 最简,第四章):事件唤醒 → LLM 拆解派发 →
// 虫群并发执行 → 收割 → 循环;一轮唤醒内反复迭代,拆解无新意图即回
// 静默等待下一次唤醒(事件驱动,不轮询)。零值不可用:TaskID/Store/
// Swarm/Provider 须齐备,通常经 Manager 的 OnTaskRun 挂入任务 goroutine
// (每任务一个 Loop 实例;唤醒由事件方调用 Task.Wake() 触发)。
// P2-04 起承载任务级总闸:任务整体超时(created 事件锚点)到点依序
// 收场——停止派发 → 在跑单位优雅收尾(宽限硬停兜底)→ 终局判定 →
// 定终态(见 settle.go)。
type Loop struct {
	TaskID   string
	Store    *rings.Store
	Swarm    *swarm.Executor // 虫群执行器:一 Step 一单位,RunStep 领取并执行
	Provider llm.Provider    // 拆解 LLM(与虫群执行可同端点,会话各自独立)

	// MaxConcurrentUnits 单任务虫群单位并发上限;零值 DefaultMaxConcurrentUnits(3)。
	MaxConcurrentUnits int

	// RootDir 任务工作目录根(主宰会话转录 JSONL 落盘根,P2-04 四类
	// 记录分工:会话原文 JSONL)。空 = 主宰会话不落转录(引擎链路照常)。
	RootDir string
	// Model 模型名(token 独立表按模型记账);空串如实记空。
	Model string
	// SettleGraceSeconds 收尾宽限(在跑单位优雅收尾的硬停上限,秒);
	// 零值 DefaultSettleGraceSeconds(90)。
	SettleGraceSeconds int
	// SettleCtl 收尾操控面(通常为 Manager);nil = 无操控面(见接口注释)。
	SettleCtl SettleController

	// ---- P2-05 心跳巡检(第八章第 8 节防线④;详见 heartbeat.go)----
	// HeartbeatInterval 巡检间隔;零值 DefaultHeartbeatInterval(600s)。
	HeartbeatInterval time.Duration
	// StallSeconds 进度停滞判定窗口(秒);零值 DefaultStallSeconds(300)。
	StallSeconds int
	// BurnAnomalyTokens 预算消耗异常阈值(单单位一拍窗口 token);
	// 零值 DefaultBurnAnomalyTokens(400k)。
	BurnAnomalyTokens int

	// TaskBlockedThreshold 任务级 LLM 连续失败受阻阈值(P2-06e,第七章
	// 「故障与恢复语义」);零值 DefaultTaskBlockedThreshold(2)。
	TaskBlockedThreshold int

	// Hiber 任务冬眠门(P2-08,第七章「任务冬眠」):门关(任务冬眠)时
	// 循环在轮次边界挂起(无状态轮次,天然可重入),任务总闸墙钟与
	// 收尾宽限随冬眠时长顺延;门开原地继续研判。装配层与 Manager/虫群/
	// 牧者/审批门共持同一实例。nil = 无冬眠语义(单测)。
	Hiber *hiber.Gate

	// AssessWall 单轮研判的墙钟上限(P4-42 ②,问题清单 #69:模型流式
	// 调用挂死的循环活性兜底):从发起到回包超时即弃本轮,黑匣子记
	// "研判超时中断",由下拍心跳或事件重触发——超时不是"无活可派",
	// 0 意图硬底线语义不破,循环必须能继续。链上单请求已有读超时
	// (failover.ReadFirst 三限),此墙钟兜的是整轮(链内多腿重放/
	// 退避叠加可能远超单请求上限)的形态。零值 DefaultAssessWall。
	AssessWall time.Duration

	// Ckpt 断点冻结信号(P3-07,第七章「故障与恢复」断点续接):武装后
	// 循环在轮次边界退出(不再拆解派发,在飞单位由虫群侧同一信号收场
	// 落地),任务让位为已存档断点、进程可关。装配层与 Manager/虫群
	// 共持同一实例;nil = 无断点语义(单测)。
	Ckpt *ckpt.Gate

	mu       sync.Mutex
	lastErr  error
	settling bool // 已进入收尾(停止派发;l.mu 保护——定时器/循环两路写)
	// settleBy 收尾发起方(l.mu 保护,进入收尾时定格):task_gate(整体
	// 超时,缺省)/ closure_prompt(收口提醒「结束」键,P4-18)——定终态
	// 的 detail.settled_by 由此透出,面板区分「已收尾」的两种来路。
	settleBy string
	// pauseCh 主宰循环的暂停闩(P2-08 单位控制的主宰循环粒度):nil/已
	// 关闭 = 运行中,开放 = 暂停中(轮次边界挂起)。l.mu 保护。
	pauseCh chan struct{}
	// guardRejects 入口守卫拒收简报(P2-06c 拒因回流):主宰拆解产物被
	// 受控入口拒收(组合声明链环节未确认 R13、意图派发 covers 幽灵资产
	// 等)时的拒因摘要,注入下一轮态势快照——主宰可见上次拒因,对症
	// 纠偏,不再原样重试。拒收只进内存简报不进事件流(拆解产物
	// 本就未入账,账本无此事实可记);进程内限注:重启即清空,语义
	// 与 lastErr 一致。限量防噪:环形保留最近 maxGuardRejects 条,
	// 超出挤掉最旧。l.mu 保护。
	guardRejects []string

	// 任务级受阻计数(P2-06e,l.mu 保护):主宰拆解模型错误与虫群单位
	// blocked 落地各计一次,任何一次成功(拆解成功/单位完成)清零;达
	// 阈值记一次黑匣子 task_blocked(面板受阻徽标的原因锚点),其后
	// 恢复记 task_resumed。面板徽标本身由"图上是否存在 blocked 步骤"
	// 派生(持久真值),此处黑匣子只承载阈值触发与恢复的审计轨迹。
	llmFails     int
	blockedNoted bool

	// 任务级总闸的运行件(deadline 仅循环 goroutine 与布防 goroutine 读,
	// l.mu 保护——P2-08 起唤醒路径会顺延它):
	deadline     time.Time // 任务收尾线(基础线 + 冬眠抵扣;零值=不限)
	deadlineBase time.Time // 收尾线基础值(created 锚点 + 整体超时;顺延的加数底)
	// seq 主宰会话序号(转录/用量记账锚点);P4-64 起反证轮人话/复测
	// 备注钩子从虫群单位线程并发触发生成会话,seqMu 保护自增。
	seq   int
	seqMu sync.Mutex

	settleKick chan struct{} // enterSettling → Run 的收尾信号(自唤醒;容量 1 合并)
	hbKick     chan struct{} // 心跳预筛有发现 → Run 的复查信号(容量 1 合并)
	runDone    chan struct{} // Run 生命周期锚点(宽限硬停 goroutine 的退出信号)

	// ---- P4-04 增量唤醒与在飞调度(问题清单 #9:单位落地应立刻研判,
	// 不等批内其他单位)----
	// landKick 单位落地通知(容量 1 合并):任一单位结果落地即踢醒等待
	// 中的循环——研判轮与落地紧邻。旧实现单位 goroutine 跑在 schedule
	// 的 wg.Wait 里,批不清空循环拿不到轮次(存量任务 12 步骤滚完 3
	// 名额中间零研判轮的根源)。Run 启动时创建。
	landKick chan struct{}
	// unitsWG 全部单位 goroutine 的收拢锚点:Run 退出前等齐(单位终态
	// 记账先于任务 goroutine 让位,保持旧批内 wg.Wait 的收序保证)。
	unitsWG sync.WaitGroup
	// polishWG 异步补写 goroutine 的收拢锚点(P4-65 ⑨⑩):研判记录与
	// 意图标题的校对稿在落账后异步生成——Run 退出前等齐(终态后读侧
	// 仍是完整校对稿;单测直接调 cycle 时等待 polishWG 显式收口)。
	polishWG        sync.WaitGroup
	resultsMu       sync.Mutex
	polishMu        sync.Mutex
	polishCtx       context.Context
	polishQueue     []displayJob
	polishKnown     map[string]bool
	polishRunning   map[string]int64
	polishActive    int
	polishDeferred  bool
	polishRecoverMu sync.Mutex
	// flight 在飞单位数、launching 已起飞未落地的步骤集、finished 循环
	// 已判完成(l.mu 保护):并发上限由 flight 计数控制(跨轮次持续,
	// 单位生命周期独立于循环迭代);launching 关闭"挑选与领取之间的
	// 异步间隙"里的双重起飞——循环迭代与落地续派两路并发挑选时,同
	// 一 dispatched 步骤不会被两只单位先后领取(Claim 不校验状态,后
	// 到者会把先到者的在途会话误判为崩溃残留而"重启收养");finished
	// 关闭"静默判定后尾部续派再起新单位"的竞态——完成收口与 schedule
	// 起单位在同一临界区互斥。
	flight       int
	launching    map[string]struct{}
	finished     bool
	planned      bool
	lastPlanRev  int64
	planTriggers []int64
}

// isSettling 是否已进入收尾(l.mu 保护)。
func (l *Loop) isSettling() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.settling
}

func (l *Loop) setErr(err error) {
	l.mu.Lock()
	l.lastErr = err
	l.mu.Unlock()
}

// maxGuardRejects 拒因回流注入的简报上限(限量防噪:快照只带最近几条,
// 更旧的被更新拒因挤掉——持续被拒的产物自然会顶上来)。
const maxGuardRejects = 3

// noteGuardReject 记一条入口守卫拒收简报(拒因回流,P2-06c):单行
// "提交「…」被拒: 原因"形态,注入下一轮快照供主宰纠偏。
func (l *Loop) noteGuardReject(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.guardRejects = append(l.guardRejects, clipEval(line))
	if len(l.guardRejects) > maxGuardRejects {
		l.guardRejects = l.guardRejects[len(l.guardRejects)-maxGuardRejects:]
	}
}

// guardRejectBriefs 快照用的拒收简报快照(拷贝,调用方免锁读)。
func (l *Loop) guardRejectBriefs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.guardRejects))
	copy(out, l.guardRejects)
	return out
}

// DefaultTaskBlockedThreshold 任务级 LLM 连续失败受阻阈值(P2-06e):
// 拆解模型错误与单位 blocked 落地合计连续 2 次即记受阻(单位侧重试已
// 消化过多次模型失败,任务级阈值只做"模型确已不可用"的确认)。
const DefaultTaskBlockedThreshold = 2

// taskBlockedThreshold 受阻阈值(零值取默认)。
func (l *Loop) taskBlockedThreshold() int {
	if l.TaskBlockedThreshold > 0 {
		return l.TaskBlockedThreshold
	}
	return DefaultTaskBlockedThreshold
}

// assessWall 单轮研判墙钟(零值取默认)。
func (l *Loop) assessWall() time.Duration {
	if l.AssessWall > 0 {
		return l.AssessWall
	}
	return DefaultAssessWall
}

// deterministicErr 是否确定性拒绝(4xx/超长,P4-27 ⑦)——重试与换腿
// 都无意义,不计任务级故障计数(程序缺陷非模型故障)。
func deterministicErr(err error) bool {
	switch failover.Classify(context.Background(), err) {
	case failover.ClassDeterminant, failover.ClassTooLong:
		return true
	}
	return false
}

// noteLLMFailure 记一次任务级 LLM 失败(P2-06e):达阈值且未记过时落
// 黑匣子 task_blocked(带原因);幂等(受阻期间不重复记)。留痕走独立
// ctx(记账不随任务熔断断链)。P4-27 ⑦:确定性拒绝由调用侧判定排除
// (deterministicErr / RunResult.BlockedClass),本计数只认模型故障。
func (l *Loop) noteLLMFailure(_ context.Context, reason string) {
	l.mu.Lock()
	l.llmFails++
	cross := l.llmFails >= l.taskBlockedThreshold() && !l.blockedNoted
	if cross {
		l.blockedNoted = true
	}
	l.mu.Unlock()
	if !cross {
		return
	}
	runlog.Infof("任务受阻: %s(%s)", l.TaskID, clipEval(reason))
	_ = blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
		rings.BlackboxRecord{
			Type: rings.BlackboxTaskBlocked,
			Detail: map[string]any{
				"reason": clipEval(reason),
				"note":   "任务级 LLM 连续失败达阈值,任务受阻(等待配置恢复或恢复全部)",
			},
		})
}

// noteLLMSuccess 记一次 LLM 成功(P2-06e):清计数;此前记过受阻则落
// task_resumed(审计闭环:受阻→恢复轨迹可查)。
func (l *Loop) noteLLMSuccess() {
	l.mu.Lock()
	had := l.blockedNoted
	l.llmFails, l.blockedNoted = 0, false
	l.mu.Unlock()
	if !had {
		return
	}
	runlog.Infof("任务恢复: %s(模型调用恢复,受阻解除)", l.TaskID)
	_ = blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
		rings.BlackboxRecord{
			Type: rings.BlackboxTaskResumed,
			Detail: map[string]any{
				"note": "模型调用恢复,任务受阻解除(图上受阻步骤经配置切换或恢复全部重开)",
			},
		})
}

// noteAssessTimeout 研判墙钟中断留痕(P4-42 ②):黑匣子记"规划超时
// 中断"(界面词,条目原文"研判超时中断")——超时不是"无活可派",
// 0 意图硬底线语义不破,循环由下拍心跳或事件重触发。wall=false 无
// 操作。留痕走独立 ctx(不随任务熔断断链)。
func (l *Loop) noteAssessTimeout(wall bool, sid string) {
	if !wall {
		return
	}
	runlog.Warnf("规划超时中断: 任务 %s 会话 %s(单轮墙钟 %.0f 秒,弃本轮待重触发)",
		l.TaskID, sid, l.assessWall().Seconds())
	_ = blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
		rings.BlackboxRecord{
			Type: rings.BlackboxAssessTimeout,
			Detail: map[string]any{
				"session_id":   sid,
				"wall_seconds": int(l.assessWall().Seconds()),
				"note":         "规划超时中断:弃本轮,心跳或新事件会再触发",
			},
		})
}

// unitLimit 虫群单位并发上限:装配显式值优先,零值时全局键覆盖
// (P2-08d Agent 栏可写),再退代码默认。每轮派发决策活读——改值对
// 下一轮派发生效,在飞单位不硬中断(与预算同纪律)。
func (l *Loop) unitLimit() int {
	if l.MaxConcurrentUnits > 0 {
		return l.MaxConcurrentUnits
	}
	if n, ok := settingPositiveInt(context.Background(), l.Store, SettingKeyUnitsLimit); ok {
		return n
	}
	return DefaultMaxConcurrentUnits
}

// Run 循环主体:等待事件唤醒,每轮唤醒执行一轮 cycle(拆解→派发→
// 执行→收割);ctx 结束(熔断/暂停)时退出,在飞
// 单位随 ctx 中止后由 unitsWG 收拢。cycle 报告任务完成(全部目标经
// 仲裁者机器判定达成)时循环退出——任务 goroutine 随之自然让位,
// Manager 据此落 completed 完成终态(P1-10)。唤醒通道容量 1,处理期间
// 到达的唤醒合并为下一轮 cycle。
// P4-04:单位落地是独立唤醒源(landKick)——在飞步骤不阻塞循环拿到
// 研判轮,拆解失败/返回后落地的结果同样有下一轮消费(零丢失)。
// P2-04:任务整体超时到点先于唤醒——收尾线定时器触发即依序收场
// (停止派发 → 在跑单位优雅收尾 → 宽限硬停 → 终局判定 → 定终态,
// 见 settle.go);恢复后已过线的任务立即收场(暂停时间照算绝对墙钟)。
// 收尾期间不再消费唤醒。
// P2-05:心跳巡检 goroutine 常驻(补事件驱动盲区)——每拍到点必醒
// (P3-14a B:距上轮研判结束满间隔即无条件唤醒,目标未完成不算任务
// 结束;预筛只做停滞/重复动作的黑匣子标记,不承担唤醒判定),发现
// 入黑匣子。
// P2-08:轮次边界查冬眠门与循环暂停闩——冬眠中循环挂起(无状态轮次,
// 天然可重入),唤醒后按冬眠时长顺延收尾线;定时器(收尾线/宽限硬停)
// 同为冬眠感知,冬眠期间墙钟冻结、到点不推进。
func (l *Loop) Run(ctx context.Context, wake <-chan struct{}) {
	// 任务标记进请求 ctx(P2-06e):拆解会话的模型请求经 failover 链时
	// 据此应用任务级主备覆盖(虫群侧在执行器 finishUnit 同样挂接)。
	ctx = failover.WithTask(ctx, l.TaskID)
	resultKick := make(chan struct{}, 1)
	// 交卷即查注册(P5-02②):交卷受控入口落账后同步调用本循环的
	// CheckClaimOnSubmit(机器三查,毫秒-秒级,不经收割轮);循环退出
	// 即注销(交卷停候选,由收割兜底轮或下次循环接管)。
	if l.Swarm != nil {
		l.Swarm.RegisterSubmissionHandlers(l.TaskID, swarm.SubmissionHandlers{ClaimCheck: l.CheckClaimOnSubmit, FactSubmitted: func() {
			select {
			case resultKick <- struct{}{}:
			default:
			}
		}})
		defer l.Swarm.UnregisterSubmissionHandlers(l.TaskID)
	}
	l.mu.Lock()
	runDone := make(chan struct{})
	l.runDone = runDone
	l.mu.Unlock()
	var background sync.WaitGroup
	var hardStopDone <-chan struct{}
	watchCtx, stopWatch := context.WithCancel(ctx)
	polishCtx, stopPolish := context.WithCancel(ctx)
	l.polishMu.Lock()
	l.polishCtx = polishCtx
	l.polishMu.Unlock()
	defer func() {
		// Checkpoint exit does not cancel the task context. A watcher may be
		// parked inside the hibernation gate, where runDone is not observed.
		stopWatch()
		if ctx.Err() != nil || l.ckptArmed() || (l.Hiber != nil && l.Hiber.Active(l.TaskID)) {
			stopPolish()
		}
		l.unitsWG.Wait()
		l.polishWG.Wait()
		stopPolish()
		close(runDone)
		background.Wait()
		if hardStopDone != nil {
			<-hardStopDone
		}
	}()
	l.settleKick = make(chan struct{}, 1)
	l.hbKick = make(chan struct{}, 1)
	l.landKick = make(chan struct{}, 1)
	// P4-04:在飞单位收拢后再让位(终态记账先落,与旧批内 wg.Wait 同序)。
	// P4-65 ⑨⑩:异步补写(研判记录/意图标题校对稿)收拢后再让位——
	// 循环退出时在途补写完成,终态任务的读侧仍是完整校对稿。
	background.Add(1)
	go func() { defer background.Done(); l.heartbeatLoop(watchCtx) }()
	l.loadDeadline(ctx)
	// P4-33 收尾续走:任务生命周期已是收尾中(冬眠唤醒拉起的 settling
	// 续收尾)——新循环直接进收尾流程,不回运行态重新拆解。
	if by := l.ledgerSettling(ctx); by != "" {
		l.mu.Lock()
		l.settling = true
		l.settleBy = by
		l.mu.Unlock()
	}
	if !l.currentDeadline().IsZero() {
		background.Add(1)
		go func() { defer background.Done(); l.settleWatch(watchCtx) }()
		hardStopDone = l.armHardStop(watchCtx)
	}
	// A slow assessment cannot own the wake channel. Each wake first offers
	// already-dispatched work to the existing scheduler, then coalesces one
	// planning notification. Reservations and execution guards stay in schedule.
	planWake := make(chan struct{}, 1)
	pumpCtx, stopPump := context.WithCancel(ctx)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			kick := planWake
			select {
			case <-pumpCtx.Done():
				return
			case _, open := <-wake:
				if !open || pumpCtx.Err() != nil {
					return
				}
			case <-resultKick:
				kick = l.landKick
			}
			if pumpCtx.Err() != nil {
				return
			}
			if err := l.processResults(ctx); err != nil {
				l.setErr(err)
			}
			l.schedule(ctx)
			select {
			case kick <- struct{}{}:
			default:
			}
		}
	}()
	// Stop the wake producer before waiting for unit goroutines: no Add may
	// race the final Wait when the loop completes or checkpoints.
	defer func() { stopPump(); <-pumpDone }()
	l.schedule(ctx)
	l.recoverDisplay(polishCtx)
	for {
		if !l.parkGate(ctx) {
			return
		}
		// P3-07 断点冻结:武装即退出(在飞单位由虫群侧同一信号收场落地;
		// 拉起时信号已被 Manager.launch 解除,出生即收场不会发生)。
		if l.ckptArmed() {
			return
		}
		if l.isSettling() {
			l.settle(ctx)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-l.settleKick: // 收尾线到点:回到循环顶进 settle 流程
		case <-l.hbKick: // 心跳预筛有发现:唤醒主宰复查在跑意图
			if l.cycle(ctx, true) {
				return
			}
		case <-l.landKick: // 结果通知只消费真实增量，不凭重复通知重试空闲拆解。
			if l.cycle(ctx, false) {
				return
			}
		case <-planWake:
			if l.cycle(ctx, true) {
				return // 任务完成:退出循环,完成终态由 Manager 记账
			}
		}
	}
}

// ckptArmed 断点冻结信号当前是否武装(nil 门 = 否)。
func (l *Loop) ckptArmed() bool {
	return l.Ckpt != nil && l.Ckpt.Armed(l.TaskID)
}

// ledgerSettling 任务生命周期当前是否收尾中(读账本任务投影,与重启
// 收养同源);返回收尾发起方 settled_by(空 = 非收尾中,取缺省
// task_gate)。P4-33:冬眠唤醒拉起 settling 任务时,新循环据此直接进
// 收尾流程(收尾续走),发起方如实保留。
func (l *Loop) ledgerSettling(ctx context.Context) string {
	rows, err := l.Store.Tasks(ctx)
	if err != nil {
		return ""
	}
	for _, r := range rows {
		if r.ID != l.TaskID {
			continue
		}
		if r.Status != "settling" {
			return ""
		}
		by := rings.SettleByTaskGate
		evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
		if err != nil {
			return by
		}
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Kind != rings.EventTaskLifecycle {
				continue
			}
			var p rings.TaskLifecycle
			if json.Unmarshal(evs[i].Payload, &p) == nil {
				if v, ok := p.Detail["settled_by"].(string); ok && v != "" {
					by = v
				}
			}
			break
		}
		return by
	}
	return ""
}

// ---- P2-08 主宰循环的挂起与暂停 ----

// parkGate 轮次边界的挂起检查:任务冬眠门与循环暂停闩任一在即挂起,
// 全开放行(false = ctx 结束,循环退出)。唤醒后顺延收尾线(冬眠期间
// 任务总闸墙钟停走);两源都复查,先唤醒又立刻暂停的次序不漏。
// P4-33:冬眠收场令(断点信号)优先——见令即放行,调用方随即收场
// 退出;门等待带节拍重查,防武装与关门的夹缝等待者错过收场令。
func (l *Loop) parkGate(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if l.ckptArmed() {
			return true
		}
		if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
			if !l.Hiber.WaitTick(ctx, l.TaskID, loopRecheckTick) {
				return false
			}
			l.extendDeadline()
			continue // 唤醒/节拍到:回顶复查(收场令已武装即放行)
		}
		l.mu.Lock()
		ch := l.pauseCh
		l.mu.Unlock()
		if ch == nil {
			return true
		}
		select {
		case <-ch:
			return true // 已关闭 = 运行中
		default:
		}
		select {
		case <-ctx.Done():
			return false
		case <-ch: // 继续:回顶复查冬眠(暂停期间任务可能已冬眠)
		}
	}
}

// loopRecheckTick 冬眠门等待的重查节拍(P4-33,与虫群侧 hiberRecheckTick
// 同义):夹缝等待者每秒回顶复查收场令。
const loopRecheckTick = time.Second

// PauseLoop 主宰循环暂停(P2-08 单位控制·主宰循环粒度):轮次边界挂起
// (无状态轮次,天然可重入)。幂等。
func (l *Loop) PauseLoop(by, reason string) error {
	l.mu.Lock()
	if l.pauseCh != nil {
		l.mu.Unlock()
		return nil
	}
	l.pauseCh = make(chan struct{})
	l.mu.Unlock()
	return l.recordLoopControl(rings.UnitCtlPause, by, reason)
}

// ResumeLoop 主宰循环继续(P2-08):开闩放行,原地继续研判。
func (l *Loop) ResumeLoop(by string) error {
	l.mu.Lock()
	ch := l.pauseCh
	l.pauseCh = nil
	l.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("overmind: 主宰循环不在暂停中")
	}
	close(ch)
	return l.recordLoopControl(rings.UnitCtlResume, by, "")
}

// LoopPaused 主宰循环当前是否暂停中(面板状态投影)。
func (l *Loop) LoopPaused() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pauseCh != nil
}

// recordLoopControl 循环控制记账(谁、何时、对主宰循环做了什么;R7)。
func (l *Loop) recordLoopControl(action, by, reason string) error {
	_, err := l.Store.Append(context.Background(), l.TaskID, rings.Event{
		Kind: rings.EventUnitControl, Actor: "creator",
		Payload: rings.UnitControlRecord{Target: "overmind", Action: action, By: by, Reason: reason},
	})
	if err != nil {
		return fmt.Errorf("overmind: 循环控制记账: %w", err)
	}
	return nil
}

// currentDeadline 收尾线快照(l.mu 保护;唤醒路径会顺延它)。
func (l *Loop) currentDeadline() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deadline
}

// extendDeadline 唤醒后顺延收尾线:基础线 + 累计冬眠时长(冬眠期间
// 任务总闸墙钟停走,第七章「任务冬眠」)。无冬眠门或未设超时不动作。
func (l *Loop) extendDeadline() {
	if l.Hiber == nil || l.deadlineBase.IsZero() {
		return
	}
	credit, err := l.Store.HibernateCredit(context.Background(), l.TaskID)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.deadline = l.deadlineBase.Add(credit)
	l.mu.Unlock()
}

// settleWatch 收尾线看护(P2-04 定时器的冬眠感知版,P2-08):到点即
// 推进收尾;冬眠中到点不推进(墙钟冻结),唤醒后按顺延的收尾线重算,
// 仍过线才收场。循环已收场(runDone)即退出。
func (l *Loop) settleWatch(ctx context.Context) {
	for {
		d := time.Until(l.currentDeadline())
		if d <= 0 {
			l.enterSettling(ctx)
			return
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		case <-l.runDone:
			t.Stop()
			return
		}
		// 到点:冬眠中不推进,唤醒后重算顺延的收尾线。
		if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
			if !l.Hiber.Wait(ctx, l.TaskID) {
				return
			}
			l.extendDeadline()
			continue
		}
		l.enterSettling(ctx)
		return
	}
}

// cycle 一次唤醒只处理一轮。派发与领取不触发下一次规划；新结果、
// 操作者指令与外部图变化由 Run 的通知唤醒，单位落地无需等批。
func (l *Loop) cycle(ctx context.Context, retryIdle bool) bool {
	// 直驱 cycle 的路径(单测)无 Run 装配:落地通知通道按需建(Run
	// 正常装配时已建,此处幂等;建先于任何单位起飞,无竞态)。
	if l.landKick == nil {
		l.landKick = make(chan struct{}, 1)
	}
	if ctx.Err() == nil {
		// ---- P2-08 轮次边界挂起:冬眠门/循环暂停闩 ----
		if !l.parkGate(ctx) {
			return false
		}
		// ---- P3-07 断点冻结:轮次边界退出(本轮已落地的收割在账,在飞
		// 单位由虫群侧信号收场;不再拆解派发)----
		if l.ckptArmed() {
			return false
		}
		// ---- 任务级总闸(P2-04):收尾线到点即依序收场,不再拆解派发 ----
		if dl := l.currentDeadline(); !dl.IsZero() && !l.isSettling() && !time.Now().Before(dl) {
			l.enterSettling(ctx)
		}
		if l.isSettling() {
			return false // 在飞单位由 settle 流程等齐;终局判定由 settle 做
		}
		// P5-05 挂等让位:模型故障停等(model_wait)对账——链路不再
		// 全员冷却(有可路由腿)即唤醒回可领取+队首档;冷却自动恢复
		// 无配置事件可触发重开,循环轮即自动恢复的唤醒通道。
		l.resolveModelWaits(ctx)
		triggers, diveTriggered, err := l.planningTriggers(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		shouldPlan := !l.planned || len(triggers) > 0
		// 空轮求值(P4-42 ⑤,问题清单 #72):触发门无新事件时,0 意图
		// 硬底线的求值前提仍须检查——图上无待执行/在跑步骤且仍有未达成
		// 目标时,本轮必须给主宰一轮拆解(硬底线写在拆解提示词里,不被
		// 求值即永久停摆:补踢到达、循环活着、零研判零外联的实测形态)。
		// 拉起轮与心跳轮由此不再被"无新触发事件"短路;有在跑/排队工作
		// 时照旧等结果(P4-29 触发收紧不破)。
		if !shouldPlan && retryIdle && l.idleFloorDue(ctx) {
			shouldPlan = true
		}
		// ① 声明兜底收口(P5-02②:确认判定已收口交卷即查——交卷事件
		// 同步触发三查,不经收割轮;收割只做事后入账汇总:交卷即查
		// 失败窗口的重查 + 补考未重交/重验未再坐实的终态收口)。先于
		// 收割与 Goal 判定——目标达成停派时不得把候选声明永久搁置。
		if err := l.processResults(ctx); err != nil {
			l.setErr(err)
			return false
		}
		// ③ Goal 完成判定:机器终审先于 LLM 拆解(R2——连主宰的
		// "看起来完成了"都不算数,只有仲裁者对冻结谓词的求值算数)。
		// 达成即停派，但在飞工作继续收尾，落地通知后再判完成。
		done, err := l.judgeGoals(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		dispatched := 0
		if !done && shouldPlan {
			l.planTriggers = triggers
			prevPlanRev := l.lastPlanRev
			reply, evalRev, err := l.decompose(ctx)
			if err != nil {
				l.setErr(err)
				return false
			}
			l.planned, l.lastPlanRev = true, evalRev
			// 深入指令竞态补正(P4-48):指令在触发扫描后、快照读取前落账
			// 时,快照已读到并研判——触发登记(研判记录触发行)与派生
			// 步骤急活豁免同补;已在触发集内的不重复登记。
			if late := diveRevisionsBetween(ctx, l.Store, l.TaskID, prevPlanRev, evalRev); len(late) > 0 {
				seen := map[int64]bool{}
				for _, r := range l.planTriggers {
					seen[r] = true
				}
				for _, r := range late {
					if !seen[r] {
						l.planTriggers = append(l.planTriggers, r)
					}
				}
				diveTriggered = true
			}
			for _, observation := range reply.AssetObservations {
				fact, err := l.Store.Node(ctx, l.TaskID, observation.FactID)
				if err != nil || fact.Type != rings.NodeFact || fact.Status != "confirmed" {
					l.noteGuardReject("资产记录缺少已确认事实")
					continue
				}
				if err := l.recordFactAssets(ctx, *fact, observation.AssetObservations); err != nil {
					l.noteGuardReject("资产记录未保存：" + err.Error())
				}
			}
			for _, control := range reply.WorkControls {
				if err := l.applyWorkControl(ctx, control, reply.owners, evalRev); err != nil {
					l.noteGuardReject(fmt.Sprintf("复查执行 %s/%s 的%s未确认完成: %v", control.StepID, control.UnitID, control.Action, err))
				}
			}
			for _, cp := range reply.CancelSteps {
				if err := l.cancelQueued(ctx, cp, evalRev); err != nil {
					l.noteGuardReject(fmt.Sprintf("取消步骤 %s 被拒：%v", cp.StepID, err))
				}
			}
			// P4-30 ③ 存量复核:主宰按立判危害门槛复核图上既有判断,
			// 信息性的标记不纳入报告(程序校验形状并留痕;账本不涂改,
			// 报告过滤按标记走)。单条被拒不拖垮其余,拒因回流简报。
			for _, ep := range reply.ReportExclusions {
				if err := l.applyReportExclusion(ctx, ep, evalRev); err != nil {
					l.noteGuardReject(fmt.Sprintf("标记判断 %s 不纳入报告被拒：%v", ep.ClaimID, err))
				}
			}
			// 深入指令轮的派生意图按"操作者急活"来源落账(P4-48 ③):
			// 背压豁免与取消保护同急活(受控来源,模型自报无效);意图
			// 校验与审批门照走。
			intentSource := sourceExplore
			if diveTriggered {
				intentSource = sourceUrgent
			}
			// P4-65 ⑩:派发成功的意图记 (stepID, 原文) 对——研判记录落账后
			// 异步补写标题校对稿(派发不等校对,任务包与机器消费用节点原文)。
			var launchedTitles []stepTitleMaterial
			for _, p := range reply.NewIntents {
				stepID, err := l.dispatch(ctx, p, intentSource)
				if err != nil {
					// 单条派发失败(如 covers 指向不存在的资产被账本整批拒)
					// 不拖垮其余意图;失败如实留在 lastErr,拒因回流进简报
					//(P2-06c,同上)。
					if ctx.Err() == nil {
						var full *QueueFullError
						if !errors.As(err, &full) {
							l.setErr(err)
						}
						l.noteGuardReject(fmt.Sprintf("意图「%s」被拒: %v", p.Intent, err))
					}
					continue
				}
				launchedTitles = append(launchedTitles, stepTitleMaterial{StepID: stepID, Intent: strings.TrimSpace(p.Intent)})
				dispatched++
			}
			// 纳谏裁决(P2-07,第四章 §7:对主宰 = 建议进正式规划,裁决后
			// 改):本轮研判对全部待裁决建议落裁决事件,建议状态随之改变;
			// 采纳的体现由拆解规则 14 约束(本轮意图或理由中说明)。
			l.writeAdviceVerdicts(ctx, reply.Advices)
			// 研判记录(P2-06 五问驱动研判从账本可验证):研判结论有内容时
			// 落 overmind.record(assessment)——五问逐问结论 + 派发摘要 +
			// 原始来源。纯派发而无研判文本不单独落账(派发
			// 在账本本就全量可查,空壳研判记录无信息量,账本侧亦拒收)。
			if reply.Assessment.hasContent() {
				a := reply.Assessment
				rec := rings.OvermindRecord{
					Type: rings.OvermindRecAssessment, SessionID: reply.sessionID,
					EvaluatedRev: evalRev, Dispatched: dispatched, Queue: reply.queue, TriggerRevisions: triggers,
					QAccess:      strings.TrimSpace(a.QAccess),
					QPrivilege:   strings.TrimSpace(a.QPrivilege),
					QCombination: strings.TrimSpace(a.QCombination),
					QUnlock:      strings.TrimSpace(a.QUnlock),
					QOverturn:    strings.TrimSpace(a.QOverturn),
					Note:         strings.TrimSpace(a.Note),
				}
				if revs, err := l.Store.Append(ctx, l.TaskID, rings.Event{
					Kind: rings.EventOvermindRecord, Actor: ActorOvermind, Payload: rec,
				}); err != nil {
					l.setErr(fmt.Errorf("overmind: 规划记录写入: %w", err))
				} else {
					// P4-65 ⑨:研判记录落账后异步补写校对稿(研判轮与派发
					// 不等校对;读侧优先链退原文)。
					if len(revs) > 0 {
						l.polishAssessmentAsync(ctx, revs[0], rec)
					}
					// P4-18:一轮研判刚落账,评估收口信号(连续零新发现
					// 双条件;只写提醒不动任务,见 closure.go)。
					l.evalClosure(ctx)
				}
			}
			// P4-65 ⑩:本轮派发的意图标题异步补写校对稿(派发不等校对;
			// 不依赖研判记录是否落账——纯派发轮同样补写)。
			l.polishStepTitlesAsync(ctx, launchedTitles)
		}
		// ④ 调度(P4-04 不等批):本轮派发的意图与图上遗留的 dispatched
		// 步骤(如收割派出的反证 Step,目标达成后亦照常执行)一并启动
		// 单位,启动即返;单位生命周期独立于本轮迭代,落地通知见 ⑤。

		l.schedule(ctx)
		if ctx.Err() != nil {
			return false
		}
		settled, err := l.judgeGoals(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		l.mu.Lock()
		quiet := l.flight == 0 && !l.finished
		l.mu.Unlock()
		if quiet && settled {
			// 收口闸(P4-55 精神,P5-02② 口径):静默收口与声明兜底收口
			// 之间存在窗口——单位恰在本轮收口之后、flight 读数之前落地
			// 交卷,其声明本轮未收口即随循环退场被终局清点兜成"证据
			// 不足"。补一道终查:有新可收口声明(交卷即查失败窗口/补考
			// 未重交/重验未再坐实)即不收口——吃掉缓冲通知再走一轮;
			// 空才算真静默(收口前最后一刻的判定照常落账)。
			n, jerr := l.checkPendingClaims(ctx)
			if jerr != nil {
				l.setErr(jerr)
				return false
			}
			if n > 0 {
				return false // 新收口在手:不算静默,落地通知驱动再走一轮
			}
			l.mu.Lock()
			if l.flight == 0 && !l.finished {
				// A claim may have lost its graph-version race and returned to
				// dispatched just before this check. No in-flight goroutine is
				// not proof that executable work has finished.
				ready, readErr := l.readySteps(ctx)
				if readErr != nil {
					l.lastErr = readErr
					l.mu.Unlock()
					return false
				}
				if len(ready) > 0 {
					l.mu.Unlock()
					l.schedule(ctx)
					return false
				}
				l.finished = true
			} else {
				l.mu.Unlock()
				return false // 终判期间又有单位起飞:回到循环
			}
			l.mu.Unlock()
			return true
		}
		return false
	}
	return false
}

// ---- Goal 完成判定(P1-06,仲裁者接管) ----

// judgeGoals 逐个求值 active 目标的冻结谓词:达成即把裁决与状态迁移
// 落账(actor=arbiter,一批一事务)。返回 true 表示任务已无未达成的
// active 目标——主宰停派(机器硬终止,P1-05 遗留的"live 模型持续
// 返回等价意图仍持续派发"自此有界)。无谓词的目标不由机器判
// (ErrNoPredicate,"何时算完"交 LLM 拆解治理,维持 P1-05 行为);
// 图上无任何目标视为开放任务,同样交拆解治理。
func (l *Loop) judgeGoals(ctx context.Context) (bool, error) {
	goals, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeGoal, "")
	if err != nil {
		return false, fmt.Errorf("overmind: 判定: 查目标: %w", err)
	}
	if len(goals) == 0 {
		return false, nil
	}
	pending := false
	for _, g := range goals {
		if g.Status != GoalStatusActive {
			continue // achieved 等终态不再判
		}
		v, err := arbiter.Evaluate(ctx, l.Store, l.TaskID, g.ID)
		if errors.Is(err, arbiter.ErrNoPredicate) {
			pending = true
			continue
		}
		if err != nil {
			return false, fmt.Errorf("overmind: 判定目标 %s: %w", g.ID, err)
		}
		if !v.Satisfied {
			pending = true
			continue
		}
		// 达成:裁决审计流 + 状态迁移,同一批事务落账。
		if _, err := l.Store.Append(ctx, l.TaskID,
			rings.Event{Kind: rings.EventArbiterVerdict, Actor: arbiter.ActorArbiter, Payload: *v},
			rings.Event{Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
				Payload: rings.NodeStateChanged{NodeID: g.ID, Status: GoalStatusAchieved}},
		); err != nil {
			return false, fmt.Errorf("overmind: 目标 %s 判定落账: %w", g.ID, err)
		}
	}
	return !pending, nil
}

// ---- TestClaim 确认判定(P5-02② 已收口交卷即查,见 claimcheck.go) ----

// judgeClaims(收割侧反证轮判定)已随 P5-02 删除:确认判定收口为机器
// 三查,交卷事件同步触发(CheckClaimOnSubmit);收割侧只做事后兜底
// 收口(checkPendingClaims:交卷即查失败窗口重查 + 补考未重交/重验
// 未再坐实的终态)。

// ---- 拆解(态势研判五问驱动,P2-06 完整版) ----

// IntentPlan 拆解产物:一条待派发意图(Step 的原料)。
type IntentPlan struct {
	GoalID              string `json:"goal_id"`
	Intent              string `json:"intent"`
	ExpectedObservation string `json:"expected_observation"`
	ActionLevel         string `json:"action_level"`
	// HarmClass 拒绝线的危害声明(P2-06b,第四章 §3 v2):数据销毁/
	// 拒绝服务类。缺省空=普通。
	HarmClass string   `json:"harm_class"`
	Covers    []string `json:"covers"`
	// Priority 优先级(P2-06 派发策略):high|normal|low,缺省 normal;
	// 并发名额满员时空出名额按优先级领取。
	Priority string `json:"priority"`

	// DependsOn 前置步骤(P2-06 派发策略):本意图依赖的 Step ID 清单;
	// 前置全部完成后才可领取执行(第四章 §2"待办绑定在图上"+依赖边)。
	DependsOn  []string `json:"depends_on"`
	SourceRefs []string `json:"source_refs"`
}

// AssessmentPlan 拆解回复里的研判结论(五问逐问;五问之外入 note)。
type AssessmentPlan struct {
	QAccess      string `json:"q_access"`
	QPrivilege   string `json:"q_privilege"`
	QCombination string `json:"q_combination"`
	QUnlock      string `json:"q_unlock"`
	QOverturn    string `json:"q_overturn"`
	Note         string `json:"note"`
}

// hasContent 研判结论是否有信息量(五问与五问外全空 = 纯静默轮)。
func (a *AssessmentPlan) hasContent() bool {
	return a != nil && (strings.TrimSpace(a.QAccess) != "" ||
		strings.TrimSpace(a.QPrivilege) != "" ||
		strings.TrimSpace(a.QCombination) != "" ||
		strings.TrimSpace(a.QUnlock) != "" ||
		strings.TrimSpace(a.QOverturn) != "" ||
		strings.TrimSpace(a.Note) != "")
}

type decomposeReply struct {
	AssetObservations []factAssetObservation `json:"asset_observations,omitempty"`
	WorkControls      []workControlPlan      `json:"work_controls"`
	CancelSteps       []cancelPlan           `json:"cancel_steps"`
	queue             *QueueWatermark
	Assessment        *AssessmentPlan       `json:"assessment"`
	NewIntents        []IntentPlan          `json:"new_intents"`
	Advices           []adviceVerdictPlan   `json:"advices"`           // 待裁决建议的裁决(P2-07 纳谏)
	ReportExclusions  []reportExclusionPlan `json:"report_exclusions"` // 信息性判断复核标记(P4-30 ③)

	owners    map[string]rings.ExecutionOwner // 与实际快照相同的执行绑定，不由模型构造
	sessionID string                          // 研判会话锚点(研判记录下钻转录用;不参与 JSON)
}

// SettingKeyPromptOvermind 主宰拆解基线可编辑正文(A 段)的配置键
// (P2-06d,通用配置键值表)。
const SettingKeyPromptOvermind = "prompt.overmind.editable"

// overmindEditableDefault 主宰拆解基线 A 段默认正文(角色与研判总则;
// 可编辑)。五问与拆解纪律、输出契约是代码固定段(见下),编辑正文
// 删不掉。
const overmindEditableDefault = `你是渗透测试任务的主宰(Overmind),agents 内部唯一计划权威,负责态势规划与意图拆解:先按标准五问评估态势,再把当前目标拆解为下一步交给虫群执行的意图(Step)。你的结论只来自态势快照中的图上事实,不持有私有记忆;拆解产物会被入口校验,引用不存在的资产或链上未确认的环节会被整批拒收。`

// overmindDisciplineRules 主宰基线 B 段(代码固定,研判与拆解纪律):
// P1-05 最简起步,P1-07 补四级分类细则,P2-04 补预算接力规则,P2-06
// 五问驱动研判与派发策略,P2-06d 补 §11 语义细则(意图数量硬底线/
// 多样性条款/复核上限/重派判定树/收尾词——防停摆与防乱派双向钉住、
// 防同方向复读)。
const overmindDisciplineRules = `态势规划标准五问(逐问给一句结论,写入 assessment;该问本轮无可评估项就写"无",不要编造;五问是起点不是上限,五问之外的重要发现写入 assessment.note):
1. 新身份能访问哪些已有资产?(新凭据/新会话/新入口对已有资产的可达性)
2. 新权限是否改变其他漏洞的影响?(原有发现在新权限下的实际影响是否变化)
3. 两条单独影响有限的线索是否组合成新的边界问题?(只提出待验证方向，再派普通意图，不直接创建漏洞)
4. 新的可达关系是否解锁之前受阻的步骤?(原工作是否可继续，还是出现需要验证的新方向)
5. 新证据是否推翻旧结论?(已有事实/判断是否与新证据矛盾)
拆解规则:
1. 只依据态势快照中真实存在的资产与已确认事实派发意图;意图必须具体、可执行、可观察。普通核对、无漏洞结果和验证通过只要求写回实际事实并结束意图，不要求“交卷”；交卷专指已经坐实且有实际影响的漏洞，不用交漏洞单表示任务完成。
2. 不重复派发与现有步骤(任何状态)相同或等价的意图。一条意图由原会话完成探索、证明、自查和交卷或原漏洞事实补充，不拆成发现、复核、交卷接力。中途事实回流不代表该意图结束；原意图仍在执行或等待恢复时，不另派同方向的复核交卷工作。确有新的问题或缺失验证时，在意图中写清新增要确认什么，复用已有证据，不以换标题、入口或数据字段重复交卷。
3. 无值得新派发的意图时输出空列表,不要为了输出而编造意图——规划后确无工作时 0 条意图是完全正常的结果(收尾词;但受第 11 条硬底线约束)。
4. 每条意图必须定动作分级,分档标准按动作对目标环境的因果方向从严选取:
   - 只读探测:只获取信息,不发送攻击载荷、不改变目标任何数据与状态,重复执行结果一致(端口判定、banner 抓取、GET 拉取页面、DNS/公开信息收集)。
   - 主动测试:向目标发送测试载荷检验响应(注入探测、目录枚举、弱口令尝试、畸形请求);发了非正常业务流量但不利用成功结果、不落地持久物。
   - 利用深入:以利用已确认弱点为前提,实际取得未授权访问或数据(读敏感文件、无副作用命令回显、凭据试用、横向探测);仍不修改目标状态。
   - 修改与持久化:改变目标状态或留下长期存在物(写文件、建账号、改配置等)。动作分级仅描述预期工作，不自动授予或拒绝权限。
5. 危害申报(harm_class):意图属数据销毁(data_destruction)或拒绝服务类(denial_of_service)时必须申报——此字段描述风险，不作为另一套硬规则。只有当前启用的硬规则直接拒绝；其余实际动作按黑名单审批线判断：只对测试对象的实际增删改请求批准，同类目的沿用批准；明确禁止直接拒绝，不因本地产物或资产登记新增审批。普通意图留空。
6. 预算耗尽、暂停、模型等待、审批等待及重启恢复沿原意图的原会话继续，由既有恢复通道处理，不为恢复另派“接着做”的新意图。读取实际进度和等待原因，等待原工作恢复；新的运行单位编号不代表新的工作或新的会话。只有出现独立新方向才另派。
7. 每条意图可带优先级(high|normal|low,缺省 normal)与前置步骤(depends_on,Step ID 清单;前置完成后才执行——只在后一步必须用到前一步产出时才声明,不设多余依赖)。new_intents 可另带 source_refs，列出本次判断实际依据的资产、意图、已确认事实或已确认漏洞编号；无明确依据时省略，不编造。来源引用只记血缘，不增加执行前置条件。
8. 查阅公开资料与其他工具动作使用同一判断入口，不声明额外的信息源身份或站点许可；资料查阅不自动收编资产。
9. 审批拒绝针对被审查的具体动作，不自动终止整个意图或禁止访问该资产；不得重复提交相同被拒动作。历史 approval_denied 步骤按其实际终态处理，不凭拒绝自行扩大禁令。
10. 多条事实组合出的整体结论仍需验证：在 assessment 中写明方向，以普通 new_intents 指定缺少的预期观察。已确认且可追溯的事实和证据优先复用，只补缺失验证，不重做整条过程；实际执行后沿同一个 submit_claim 入口交卷，不自评成立，不将联想写进事实。
11. 意图数量硬底线:图上无未完成步骤且仍有未达成目标时,必须派发至少 1 条意图。未完成包括dispatched/running及scope_wait/gate_wait/pause_wait/model_wait，等待中的原会话仍持有工作，不是空闲。输出 0 条意图仅在两种情形正当——①在等在跑单位产出或原会话解除等待(图上有未完成步骤)②全部目标已达成。其余情形的 0 意图是停摆,不许。目标未达成且确实没有未完成工作时,本轮必须产出至少一个新方向(新模块/新角度/更深一层),禁止重复已确认事实与已完成步骤。
12. 路线多样性:探索期理想同时维持 2~3 条机理不同的路线(不同入口、不同技术路径)并存,防单点钻牛角尖;多样性永远服从 RoE 与优先级,不为多而多。
13. 规划只根据任务目标、实际观察与账本证据形成下一条意图；未验证的想法写在本轮研判及派发理由中，不能记成已确认事实。保留实际来源引用。复核上限:同一否定方向至多复核一次——复核后仍是否定,尊重结论,不再向该方向派发;后续新证据出现时走 TestClaim 重开和正常研判记录兜底,不靠重复派发。
14. 重派判定树:停下的步骤按停法分别处置——done:检查实际产出与归属后规划新工作，不仅为复述或重新申报已证明的结果另派;exhausted:读“预算耗尽步骤”轨迹，原意图沿原会话恢复，不复制工作;blocked(受阻):等待原通道恢复，确有不同前提或新方法才规划新的验证，不能把等待当失败。依据一律是轨迹真实进度,不是状态措辞。
15. 待判定建议(纳谏):快照「待判定建议」区每条都必须在 advices 里判定——adopt=true 须在本轮意图中体现或在理由中写明何时体现;adopt=false 的理由必须基于图上事实(不空泛)。牧者的建议是规划输入,不是命令;判定后规划随之改变,建议状态随之更新。
16. 本机回环(127.0.0.1 / localhost / ::1)与镜等测试环境自身设施不是目标攻击面:不对其派发探测或验证意图,也不把虫群对它们的观察当作发现规划后续;虫群的相关提交会被入口拒收并记为环境观察。查阅资料不自动形成测试目标或任务成果；背景资料仍按来源保留，不因被提到就派新意图。任务资产清单内的目标(含操作者声明的本机测试靶标)不受此限。
17. 队列治理:先读队列水位，等前置步骤也预留容量，避免前置完成时集中转排队突破上限。普通探索最多提交本轮还可新增数；容量满是正常等待，不是模型故障，不重复提交。操作者急活由各自入口豁免。每轮审视全部未领取步骤，结合最新证据取消重复、过时、前提失败的普通探索；用 cancel_steps 给步骤 ID、具体理由与图上依据 ID。急活及其关键前置保留，程序会复检并连带取消普通后继。cancel_steps只用于未领取步骤；在跑执行的复查使用work_controls，经现有舵杆纠偏或止损。刚派发新步骤不是继续规划的依据；一只单位结果落地就处理新结果，不等整批。
18. 立判危害门槛:` + swarm.ClaimHarmGate + `(用于研判哪些方向值得验证，实际漏洞只由虫群统一交卷)。对图上按旧口径已立的信息性判断(纯前置条件/环境特性/无害观察,含已确认的),用 report_exclusions 提交 {claim_id, reason} 标记"不纳入报告"——理由基于判断内容与影响面证据,写明为何无直接危害;已标记的不重复标记;无可标记时省略该字段。报告只收已确认且非信息性判断。
19. 操作者深入指令(节点锚定的推动):快照「操作者深入指令」区每条都是造物主从该节点出发的推动——本轮必须基于锚定节点立即研判并规划下一步(新方向/深挖/组合;硬承诺,不得空轮跳过)。方向文本是建议不是命令:采纳与否由你裁决,违反 RoE 或不可行时不执行并在 assessment 写明理由;方向留空时由你自行判断。已废弃/被推翻等终态节点同样可被锚定——作为线索重新探索,采纳方式由你裁决。由此派发的意图照常走校验与审批门。
20. 告警复查:读取告警发生时间、告警单位与当前实际执行／进展。对本轮要求复查的每个执行，在work_controls中选择continue、steer或stop，并写具体进展或前提依据；同一执行一轮只决定一次。高Token(输入含缓存)、长会话或工具尚未返回，不等于无进展；已有新事实、正常长工具、审批／模型等待应结合实际情况继续，不为告警本身终止。连续重复且无新进展时可steer，message写清如何结合已有材料修正当前工作；方向前提已失效时才stop。只使用快照列出的step_id和unit_id，不控制历史单位或编造单位。纠偏不改变RoE或审批权限，消息不得要求绕过它们；下轮检查真实工具结果、新事实和舵杆回执，不能把已排队当已送达、把采纳回执当已完成。本轮work_controls尚待程序提交，在assessment中只写决定或拟采取的动作，不提前声称“已纠偏／已停止”。`

// overmindOutputContract 主宰基线 C 段(代码固定,输出规约):JSON
// 契约是机器解析接口,配置不可移除。P4-11 ①:契约内带 intent 等结构化
// 展示字段的文风收口与正反样例(只管新生成,不改写图上已有步骤)。
const overmindOutputContract = `只输出 JSON:{"assessment":{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"},"new_intents":[{"goal_id":"本条意图所服务的目标编号","intent":"一句话意图","expected_observation":"预期观察","action_level":"只读探测|主动测试|利用深入|修改与持久化","harm_class":"data_destruction|denial_of_service(仅危害类意图,普通意图省略)","covers":["资产ID"],"priority":"high|normal|low","depends_on":["StepID"]}],"advices":[{"id":"av-…","adopt":true,"reason":"判定理由(不采纳须基于图上事实)"}]}
目标归属:new_intents每条明确填写快照中的goal_id，按本条实际服务的目标选择，不把全部工作挂到最新目标。聚焦方向仍按当前聚焦约束派发；跨目标组合可归到组合目标，前置资料按自身目标安排。goal_id是目标归属，不能放进source_refs；它不增加执行前置。
资产归档随正常研判顺带记录，可选输出asset_observations:[{"fact_id":"已有已确认事实编号","assets":[{"class":"domain|host|service|app|miniprogram","title":"实际地址或系统标识","evidence":"对应事实/证据的连续原文"}],"relations":[{"from_class":"service","from":"实际入口","to_class":"app","to":"已确认系统","kind":"hosted_on|same_system|redirects_to|resolves_to|related_to","evidence":"连续原文"}]}]。网站/系统是主体，域名、IP、端口按实际地址记录；新入口只有已确认same_system关系才归入已有系统，否则保留加入选择。共享IP、DNS、部署关系或组件指纹都不能证明同系统。app/miniprogram仅独立系统填写independent:true；认证模块、框架、组件识别留在原事实并锚定已有网站，不升为主体；接口/API/URL资源直接归其网站，不逐条建资产，不把HTTP方法放进资产名称。只用已经发现的事实判断归属和关系，不能确定归属就先独立，允许少记录；不得为资产登记、分类、补全关系或把图做完整而另派探测/判断/重复测试。没有新记录时省略此字段，不为归档单开一轮研判。
可选来源字段:new_intents 每条可带 "source_refs":["实际依据的节点ID"]，允许资产、意图、已确认事实及已确认漏洞。只引用真实依据；它不替代 covers 或 depends_on，不增加执行前置条件。没有明确引用时省略。首轮还没有事实或既有意图时，source_refs可省略，若填写只列实际作为来源的资产编号；不要把目标清单中的编号一起复制进来。任务目标说明要达成什么，不是来源节点。目标编号不能放入 source_refs；尚未创建的未来步骤也不能引用。depends_on 只引用图上实际已有步骤，不能猜测本批待派步骤将获得的编号。
可选取消字段:"cancel_steps":[{"step_id":"sN","reason":"最新证据表明重复、过时或前提已失败的具体理由","evidence":["图上依据节点ID"]}]。取消后仍保留节点与轨迹；没有应取消步骤时输出空列表。
执行复查字段:"work_controls":[{"step_id":"快照中的步骤ID","unit_id":"同一快照中的实际单位ID","action":"continue|steer|stop","reason":"具体进展或方向前提依据","message":"steer时必填，给当前单位的纠偏要求"}]。有本轮必须复查的告警时逐执行给决定；无须复查时省略或空列表。continue不发送新工作，steer与stop走已有舵杆；不替代cancel_steps，不更改授权。
可选复核字段:"report_exclusions":[{"claim_id":"tc-…","reason":"该判断为纯信息性观察(无直接危害)的具体理由"}]。标记该判断不纳入报告；已标记过的不重复标记，无可标记时省略该字段。
(advices 仅在快照存在待判定建议时输出,无则省略该字段。)
字段文风(intent 与判断是面板高频展示字段,生成时收口;只管新写,不改写图上已有步骤;P4-65 B 起样例对齐报告级锚口径——完整标识、数字精确、平实白话):
- intent:资产+手法+目标的点式短句,` + swarm.StructuredFieldStyle + `;写不下就收窄定位——点一个代表路径加"等 N 个",不铺枚举清单;命令与参数留给执行,不进意图。
  正例:对 10.0.0.5:8080 做目录枚举,找未授权入口
  正例(长定位收窄):对 10.0.0.5:4330 根目录直取 20 个常见文件名(README.md 等),确认根外文件是否暴露
  反例:针对目标 Web 应用开展全面深入的安全测试,利用多种手段验证潜在漏洞的存在性并评估影响范围(无定位、无数字、长而空)
  反例(铺清单):对 10.0.0.5:4330 根目录直取 20 个常见顶层文件名(如 README.md、LICENSE、CHANGELOG.md、site.log、key、.tok),确认根外敏感文件是否被静态暴露

不要输出 JSON 以外的任何内容。`

// OvermindPromptSpec 主宰拆解基线三段式规约(P2-06d,§11):A 可编辑
// 正文 + B 研判与拆解纪律 + C 输出规约 + D 常驻文风(代码固定;
// P3-11a:研判叙述按人话写,JSON 契约原样)。
var OvermindPromptSpec = swarm.PromptSpec{
	SettingKey:      SettingKeyPromptOvermind,
	DefaultEditable: overmindEditableDefault,
	Fixed:           []string{overmindDisciplineRules, overmindOutputContract, swarm.ResidentStyleRules},
}

// overmindSystemSegments 拆解会话 system 段(§11 位置规范):
//
//	[0] RoE 约束块(最顶,凌驾声明;从账本读当前值——约束改动下一轮
//	    规划生效,不做硬中断)
//	[1] A 段 可编辑正文(键值覆盖或代码默认)
//	[2] B 段 研判与拆解纪律(代码固定,含意图数量硬底线/多样性/
//	    复核上限/重派判定树/收尾词)
//	[3] C 段 输出规约 JSON 契约(代码固定)
//	[4] D 段 常驻文风(代码固定;assessment 叙述按人话写,JSON 照契约)
//
// 易变态势(快照)走 user turn,不进 system——system 保持稳定利于
// 前缀缓存(§11:worker 与 planner 的位置分叉是有意设计)。RoE 读
// 失败退零值如实展示(边界从宽起步语义,与 CurrentRoE 一致)。
func (l *Loop) overmindSystemSegments(ctx context.Context) []string {
	roe, err := l.Store.CurrentRoE(ctx, l.TaskID)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 读 RoE 组约束块: %w", err))
		roe = rings.RoeState{}
	}
	var excluded []rings.ExcludedAssetView
	if excluded, err = l.Store.ExcludedAssets(ctx, l.TaskID); err != nil {
		l.setErr(fmt.Errorf("overmind: 读授权范围摘除清单: %w", err))
		excluded = nil
	}

	segs := []string{swarm.RenderRoEConstraint(
		"派发的意图与采用的手段不得越出此边界。",
		roe.AllowedTools, roe.DeniedTools, excluded, rings.PolicyNotes(roe.CreatorNotes, roe.IntentRules))}
	return append(segs, OvermindPromptSpec.Assemble(ctx, l.Store)...)
}

// nextSessionID 主宰会话的转录/用量锚点(P2-04 四类记录分工:会话原文
// JSONL + token 独立表):每次拆解/评估会话独立编号,文件互不混写。
// P4-64 起虫线程钩子(反证轮人话/复测备注)并发调用,seqMu 保护。
func (l *Loop) nextSessionID(prefix string) string {
	l.seqMu.Lock()
	defer l.seqMu.Unlock()
	l.seq++
	return "overmind-" + l.TaskID + "-" + prefix + strconv.Itoa(l.seq)
}

// decompose 一次 LLM 拆解:态势快照 → 研判结论(五问)+ 新意图列表
// 。无工具单轮会话,引擎链路(适配器/流式/用量)全真;
// 转录 JSONL 落盘(RootDir 已设时)、逐次用量入 token 独立表(P2-04
// 四类记录分工)。返回研判所读的账本版本(evaluatedRev)——研判记录
// 的锚点兼下一轮变化集的计算起点。
func (l *Loop) decompose(ctx context.Context) (*decomposeReply, int64, error) {
	snapshot, rev, water, owners, err := l.snapshotDetails(ctx)
	if err != nil {
		return nil, 0, err
	}
	due, err := l.workReviewsDue(ctx, owners, rev)
	if err != nil {
		return nil, rev, err
	}
	if len(due) > 0 {
		var ids []string
		for step := range due {
			ids = append(ids, step+"/"+owners[step].UnitID)
		}
		sort.Strings(ids)
		snapshot += "\n本轮必须复查的告警执行: " + strings.Join(ids, ", ") + "。每个执行在work_controls中给一个continue、steer或stop决定及具体进展／前提依据；不能只在assessment口头提及。\n"
	}
	sid := l.nextSessionID("d")
	if _, err := l.Store.Append(ctx, l.TaskID, rings.Event{Kind: rings.EventOvermindRecord, Actor: ActorOvermind,
		Payload: rings.OvermindRecord{Type: rings.OvermindRecPlanning, SessionID: sid, EvaluatedRev: rev,
			TriggerRevisions: l.planTriggers, Bootstrap: !l.planned, Queue: &water}}); err != nil {
		return nil, rev, err
	}
	opts := agentcore.Options{
		// 输出上限跟模型库配置走(P3-14a a):剥掉本地 MaxTokens——
		// failover 链的 capTokensProvider 在库配置了上限时压入配置值,
		// 未配置(0)则字段不发送走服务端默认。硬编码 4096 曾在库配置
		// 更宽时仍把长拆解在系统侧掐断(d3/d4 截断形态的根源之一)。
		Provider:               failover.LibraryOutput(swarm.MeterProvider(l.Provider, l.Store, rings.TokenUsage{TaskID: l.TaskID, Agent: ActorOvermind, Model: l.Model, TranscriptID: sid}, l.setErr)),
		SystemPrompt:           l.overmindSystemSegments(ctx),
		Tools:                  []tool.CoreTool{}, // 显式空表:拆解会话无工具(nil 会挂引擎默认工具集)
		PermissionMode:         permission.ModeDefault,
		MaxTurns:               2,
		SessionID:              sid,
		DisableBackgroundTasks: true,
	}
	if l.RootDir != "" {
		opts.Transcript = transcript.NewStore(filepath.Join(l.RootDir, l.TaskID, "transcripts"))
	}
	sess := agentcore.NewSession(opts)
	defer sess.Close()
	var term *harness.Terminal
	// 单轮研判墙钟(P4-42 ②):发起到回包超时即弃本轮——链上单请求
	// 有读超时,但链内多腿重放/退避叠加可远超单请求上限,整轮兜底由此
	// 把关。超时后黑匣子记"研判超时中断"(独立 ctx,不随任务熔断断链),
	// 循环回静默等下拍心跳/事件重触发,不永久卡死。
	sctx, cancelWall := context.WithTimeout(ctx, l.assessWall())
	defer cancelWall()
	wallHit := false
	for ev, err := range sess.Prompt(sctx, snapshot) {
		if err != nil {
			if sctx.Err() != nil && ctx.Err() == nil {
				wallHit = true
			}
			if !deterministicErr(err) {
				l.noteLLMFailure(ctx, err.Error())
			}
			l.noteAssessTimeout(wallHit, sid)
			return nil, rev, fmt.Errorf("overmind: 拆解会话: %w", err)
		}
		if ev.Kind == harness.KindResult && ev.Terminal != nil {
			if ev.Terminal.Err != nil {
				if sctx.Err() != nil && ctx.Err() == nil {
					wallHit = true
				}
				if !deterministicErr(ev.Terminal.Err) {
					l.noteLLMFailure(ctx, ev.Terminal.Err.Error())
				}
				l.noteAssessTimeout(wallHit, sid)
				return nil, rev, fmt.Errorf("overmind: 拆解终止: %w", ev.Terminal.Err)
			}
			term = ev.Terminal
		}
	}
	if term == nil && sctx.Err() != nil && ctx.Err() == nil {
		// 引擎在墙钟截断下未送出错误事件(如已停在等待模型回包):墙钟
		// 到点即中断,与收到错误同口径留痕。终态已送达(term 在手)则
		// 结果完整,不判超时。
		l.noteAssessTimeout(true, sid)
		return nil, rev, fmt.Errorf("overmind: 拆解会话: 单轮墙钟 %.0f 秒到点,弃本轮", l.assessWall().Seconds())
	}
	l.noteLLMSuccess()
	reply, err := parseDecomposeTerminal(term)
	if err != nil {
		// 解析失败落 WARN(P3-14a d):此前 setErr 只进内存,零日志——
		// 截断报废形态在运行日志里无迹可查。
		runlog.Warnf("拆解解析失败: 任务 %s 会话 %s 原因 %s", l.TaskID, sid, clipEval(err.Error()))
		return nil, rev, err
	}
	reply.sessionID = sid
	reply.queue = &water
	reply.owners = owners
	if err := validateWorkReviews(reply.WorkControls, owners, due); err != nil {
		return nil, rev, err
	}
	return reply, rev, nil
}

// jsonFenceRe 剥掉模型回复常见的 ```json 围栏。
var jsonFenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\s*(.*?)```")

// parseIntents 解析拆解回复:容忍围栏与前后杂文,取首尾大括号间的
// JSON 主体;空意图条目过滤(无陈述的意图不可执行)。P2-06 起回复
// 含研判结论(assessment),缺省兼容
// 旧形态(只有 new_intents)。
func parseIntents(text string) (*decomposeReply, error) {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	if body == "" {
		return nil, fmt.Errorf("overmind: 拆解回复为空")
	}
	var r decomposeReply
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, fmt.Errorf("overmind: 拆解回复不是合法 JSON: %w", err)
	}
	out := &decomposeReply{AssetObservations: r.AssetObservations, Assessment: r.Assessment, Advices: r.Advices, WorkControls: r.WorkControls,
		CancelSteps: r.CancelSteps, ReportExclusions: r.ReportExclusions}
	for _, p := range r.NewIntents {
		if strings.TrimSpace(p.Intent) == "" {
			continue
		}
		out.NewIntents = append(out.NewIntents, p)
	}
	return out, nil
}

// parseDecomposeTerminal 拆解终态解析(P3-14a b):Terminal.Messages 是
// 引擎官方通道(v0.4.1)的全会话快照——先按序拼接 assistant 文本再解析,
// "续写接上"形态(max_tokens 截断段+引擎注入续写后的接续段)拼回完整
// JSON;拼接失败回退逐条解析(从最新往最旧,"重写再续"形态:模型续写时
// 整段重写,末条独立成文)。Terminal.Text 只带最后一轮文本、跨轮不拼接,
// 单独依赖它正是 d3/d4 截断报废的解析根源;非截断运行单条 assistant
// 文本,拼接即原文,行为不变。
func parseDecomposeTerminal(term *harness.Terminal) (*decomposeReply, error) {
	var texts []string
	if term != nil {
		for _, m := range term.Messages {
			if m.Role == llm.RoleAssistant {
				if t := strings.TrimSpace(m.Text()); t != "" {
					texts = append(texts, t)
				}
			}
		}
	}
	if len(texts) > 0 {
		if r, err := parseIntents(strings.Join(texts, "")); err == nil {
			return r, nil
		}
		for i := len(texts) - 1; i >= 0; i-- {
			if r, err := parseIntents(texts[i]); err == nil {
				return r, nil
			}
		}
	}
	if term == nil {
		return nil, fmt.Errorf("overmind: 拆解未结束")
	}
	return parseIntents(term.Text)
}

func (l *Loop) snapshot(ctx context.Context) (string, int64, error) {
	text, rev, _, _, err := l.snapshotDetails(ctx)
	return text, rev, err
}

func (l *Loop) snapshotDetails(ctx context.Context) (string, int64, QueueWatermark, map[string]rings.ExecutionOwner, error) {
	for ctx.Err() == nil {
		text, rev, water, owners, err := l.buildSnapshot(ctx)
		if err != nil {
			return "", 0, QueueWatermark{}, nil, err
		}
		now, err := l.Store.CurrentRevision(ctx, l.TaskID)
		if err != nil {
			return "", 0, QueueWatermark{}, nil, err
		}
		if now == rev {
			return text, rev, water, owners, nil
		}
	}
	return "", 0, QueueWatermark{}, nil, ctx.Err()
}

func (l *Loop) buildSnapshot(ctx context.Context) (string, int64, QueueWatermark, map[string]rings.ExecutionOwner, error) {
	rev, err := l.Store.CurrentRevision(ctx, l.TaskID)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查图版本: %w", err)
	}
	goals, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeGoal, "")
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查目标: %w", err)
	}
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "")
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查步骤: %w", err)
	}
	facts, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeFact, "confirmed")
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查事实: %w", err)
	}
	claims, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeTestClaim, "")
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查漏洞判断: %w", err)
	}
	ag, err := l.Store.AssetGraph(ctx, l.TaskID)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查资产图: %w", err)
	}
	abouts, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeAbout)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查资产锚点: %w", err)
	}
	depEdges, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDependsOn)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, fmt.Errorf("overmind: 查依赖边: %w", err)
	}

	memberIDs := map[string]bool{}
	memberKeys := map[string]bool{}
	for _, a := range ag.Assets {
		memberIDs[a.ID] = true
		_, _, key, e := rings.AssetIdentity(a.AssetClass, a.Title)
		if e == nil {
			memberKeys[key] = true
		}
	}
	water := QueueWatermarkOf(steps, depEdges, QueueLimit(ctx, l.Store))
	var b strings.Builder
	fmt.Fprintf(&b, "【态势快照】\n图版本: %d\n", rev)

	fmt.Fprintf(&b, "\n队列水位：%s\n", queueLine(water))
	b.WriteString("已批准待上位与补做证据优先领取下一个空位；等正在执行单位释放名额，不能保证立即起飞。\n")
	b.WriteString("\n自上轮规划以来的变化(五问审视对象):\n")
	if lines := l.deltaSinceLastAssessment(ctx, memberIDs); len(lines) > 0 {
		for _, line := range lines {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	} else {
		b.WriteString("  (无;首轮或上轮规划后账本无新变化)\n")
	}

	// 操作者深入指令(P4-48):每条指令注入锚定节点完整内容与血缘;
	// 纳谏语义见拆解规则 18——立即规划是硬承诺,方向是建议。
	if dives := l.diveBriefs(ctx); len(dives) > 0 {
		b.WriteString("\n操作者深入指令(造物主从该节点出发的推动;本轮必须逐条处理):\n")
		for _, line := range dives {
			fmt.Fprintf(&b, "%s\n", line)
		}
	}

	b.WriteString("\n目标:\n")
	if len(goals) == 0 {
		b.WriteString("  (无)\n")
	}
	// 方向聚焦(P4-54 停聚焦):造物主指令"其他都停掉只做 X"——聚焦行
	// 置顶声明,派发守卫同判(非聚焦方向的意图会被拒收,别浪费提交);
	// 聚焦目标行带[聚焦中]标注。
	focusGoalID := FocusGoalID(ctx, l.Store, l.TaskID)
	if focusGoalID != "" {
		if fg, gerr := l.Store.Node(ctx, l.TaskID, focusGoalID); gerr == nil {
			fmt.Fprintf(&b, "  ★ 方向聚焦中:只做「%s」(%s)——其他方向不再派发(派发入口会拒收)\n", fg.Title, fg.ID)
		}
	}
	for _, g := range goals {
		mark := ""
		if g.ID == focusGoalID {
			mark = " [聚焦中]"
		}
		fmt.Fprintf(&b, "  - %s [%s]%s %s\n", g.ID, g.Status, mark, g.Title)
	}

	b.WriteString("\n资产(已覆盖=已有步骤 covers):\n")
	if len(ag.Assets) == 0 {
		b.WriteString("  (无)\n")
	}
	// 授权范围摘除清单(P4-05):摘除的资产标"范围外"——主宰不派覆盖
	// 它们的意图(派发入口守卫同判,双保险);覆盖缺口只看在范围内的。
	// P4-44:范围外待批资产(渗透任务)标"待批"——锁定不测,面板放行
	// 后转范围内;红队/未标任务的范围外发现资产标"范围外(可测)"。
	for _, a := range ag.Assets {
		mark := "未覆盖"
		if a.Covered {
			mark = "已覆盖"
		}
		if a.Source == rings.AssetSourceManual {
			mark += ",手动补入"
		}
		fmt.Fprintf(&b, "  - %s [%s] %s (%s)\n", a.ID, a.AssetClass, a.Title, mark)
	}

	b.WriteString("\n资产层级(不代表产生来源):\n")
	for _, e := range ag.Hierarchy {
		fmt.Fprintf(&b, "  - %s 属于 %s\n", e.From, e.To)
	}
	b.WriteString("\n已确认的资产关系(观察事实为依据，不是执行前置或授权):\n")
	for _, r := range ag.Relations {
		b.WriteString("  - " + r.Summary() + "\n")
	}
	// 步骤行带优先级与依赖等待状态(P2-06 派发策略的研判可见面:
	// "受阻/等待中的步骤"是五问第④问的审视对象)。
	depDeps := map[string][]string{} // stepID → 前置清单(depends_on 边)
	for _, e := range depEdges {
		depDeps[e.From] = append(depDeps[e.From], e.To)
	}
	b.WriteString("\n步骤:\n")
	if len(steps) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, s := range steps {
		fmt.Fprintf(&b, "  - %s [%s] %s", s.ID, s.Status, s.Title)
		if p, _ := s.Detail["priority"].(string); p != "" && p != PriorityNormal {
			fmt.Fprintf(&b, " (优先级 %s)", p)
		}
		if deps := depDeps[s.ID]; len(deps) > 0 {
			fmt.Fprintf(&b, " (前置 %v)", deps)
		}
		b.WriteString("\n")
	}

	progress, owners, err := l.executionProgress(ctx, steps, facts)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, err
	}
	b.WriteString("\n当前执行与进展(单位编号与实际执行绑定；仅据真实进展复查，不按Token自动终止):\n")
	for _, line := range progress {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	// 审批拒绝(P2-06b 起;P4-68 ② 拒绝分族):拒绝记录与原因——面板
	// 拒绝/动作类拒绝只杀该动作,单位存活自行换路(连拒同类 3 次才升级
	// 终止单位,升级笔留痕可见);denyGuard 拒绝线(工具边界/危害声明/
	// 排除止损)与升级保险触发的才落单位终态,被终止单位不得换法重试,
	// 确有必要再走该方向须凭新证据/新方法另派。含造物主研判后放行的
	// 历史记录。
	denials := l.denialBriefs(ctx)
	b.WriteString("\n审批拒绝(只杀该动作,单位自行换路;标\"升级终止单位\"的例外,该单位已终止):\n")
	if len(denials) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, line := range denials {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	// 上轮提交被拒(P2-06c 拒因回流):主宰拆解产物被入口守卫拒收的拒因
	// 摘要(限量:最近几条)。对症纠偏——修正产物再提交,原样重试只会
	// 再被拒(如链环节换成已确认事实、covers 换成图上真实资产)。
	rejects := l.guardRejectBriefs()
	b.WriteString("\n上轮提交被拒(入口校验拒收;修正后再提交,原样重试只会再被拒):\n")
	if len(rejects) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, line := range rejects {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	// 心跳标记(P2-05 防线④:机器预筛发现,近两拍窗口):主宰被心跳
	// 唤醒后据此复查在跑意图(换方向派发/不再重复派发,P2-06 研判
	// 完整版的消费面);舵杆的直接纠偏通道操作者侧已可用,主宰侧
	// 研判驱动的 steer/kill 下达随 P2-08 回执状态机一并落(第八章
	// 第 8 节"持有人:造物主和主宰都可持")。
	hbMarks := l.heartbeatMarks(ctx, steps, owners)
	b.WriteString("\n心跳标记(机器预筛,确定性;有标记的意图值得复查):\n")
	if len(hbMarks) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, line := range hbMarks {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	// 预算耗尽步骤的执行轨迹(P2-04 接力依据,R16):主宰据此决定派
	//"接着上次进度继续"的接力意图还是换方向——轨迹含单位终因/轮数、
	// 工具实调与产出物(血缘可下钻),不是一句"失败"了事。
	exhausted := l.relayBriefs(ctx, steps)
	b.WriteString("\n预算耗尽步骤(暂停接力,非失败;发现已写回,可续可换):\n")
	if len(exhausted) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, line := range exhausted {
		fmt.Fprintf(&b, "  - %s\n", line)
	}

	// 已确认事实带资产锚点(P2-06 五问第①③问的研判输入:新身份对哪些
	// 资产可达、哪些线索可组合)。
	factAssets := map[string][]string{}
	for _, e := range abouts {
		factAssets[e.From] = append(factAssets[e.From], e.To)
	}
	// A fact returned by a running intent is progress, not a hand-off. Keep
	// its recorded producer and claim ownership visible to the planner.
	produces, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeProduces)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, err
	}
	factProducers := map[string][]string{}
	for _, e := range produces {
		factProducers[e.To] = append(factProducers[e.To], e.From)
	}
	sources, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeSourcedFrom)
	if err != nil {
		return "", 0, QueueWatermark{}, nil, err
	}
	claimIDs := map[string]bool{}
	for _, c := range claims {
		claimIDs[c.ID] = true
	}
	factClaims := map[string][]string{}
	for _, e := range sources {
		if claimIDs[e.From] {
			factClaims[e.To] = append(factClaims[e.To], e.From)
		}
	}
	b.WriteString("\n已确认事实:\n")
	if len(facts) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, f := range facts {
		fmt.Fprintf(&b, "  - %s: %s", f.ID, f.Title)
		if producers := factProducers[f.ID]; len(producers) > 0 {
			fmt.Fprintf(&b, " (产出意图 %v)", producers)
		}
		if claims := factClaims[f.ID]; len(claims) > 0 {
			fmt.Fprintf(&b, " (已归属漏洞 %v)", claims)
		}
		if decl := factNewAsset(f.Detail); decl != nil {
			_, _, key, e := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
			if e != nil {
				b.WriteString(" (资料观察，不作为资产规划)")
			} else if !memberKeys[key] {
				b.WriteString(" (观察对象未加入资产，不安排该对象的后续工作)")
			}
		}
		if assets := factAssets[f.ID]; len(assets) > 0 {
			fmt.Fprintf(&b, " (资产 %v)", assets)
		}
		b.WriteString("\n")
	}

	// P4-30 ③:判断行带影响面与定稿级别(按锚复核的输入)、已标记
	// 不纳入报告的如实标注(不重复标记)。
	sevOf, marked := l.claimReportState(ctx)
	b.WriteString("\n发现(漏洞判断):\n")
	if len(claims) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, c := range claims {
		fmt.Fprintf(&b, "  - %s [%s] %s", c.ID, c.Status, c.Title)
		if imp, _ := c.Detail["impact"].(string); imp != "" {
			fmt.Fprintf(&b, "(影响面: %s)", clipLine(imp, 60))
		}
		if sev := sevOf[c.ID]; sev != "" {
			fmt.Fprintf(&b, " [级别 %s]", sev)
		}
		if marked[c.ID] {
			b.WriteString(" [已标记不纳入报告]")
		}
		b.WriteString("\n")
	}

	// 待裁决建议(P2-07 纳谏):牧者代造物主提交的方向性建议,逐条
	// 裁决后状态改变(adopted|rejected 带理由);裁决规则见拆解规则 14。
	b.WriteString("\n待判定建议(牧者代造物主提交;逐条在 advices 判定):\n")
	if advices, aerr := l.pendingAdvices(ctx); aerr != nil || len(advices) == 0 {
		if aerr != nil {
			b.WriteString("  (读取失败)\n")
		} else {
			b.WriteString("  (无)\n")
		}
	} else {
		for _, a := range advices {
			fmt.Fprintf(&b, "  - %s: %s\n", a.ID, a.Text)
		}
	}

	b.WriteString("\n【拆解指令】\n先按五问评估态势(assessment 逐问一句结论),再依据以上态势输出普通意图列表；有告警时复查现有执行，给出继续、纠偏或停止的决定。只输出 JSON;" +
		"若无新意图输出空列表。\n")
	return b.String(), rev, water, owners, nil
}

// claimReportState 判断的报告侧状态(P4-30 ③):最新定稿级别与是否已
// 标记不纳入报告——事件流一次扫描,快照发现区与复核输入共用。
func (l *Loop) claimReportState(ctx context.Context) (sev map[string]string, marked map[string]bool) {
	sev, marked = map[string]string{}, map[string]bool{}
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil {
			continue
		}
		switch r.Type {
		case rings.TestClaimRecRepro:
			sev[r.ClaimID] = r.Severity // 最新定稿取代(重开再确认随新定稿走)
		case rings.TestClaimRecReportReview:
			marked[r.ClaimID] = true
		}
	}
	return
}

func (l *Loop) deltaSinceLastAssessment(ctx context.Context, memberIDs map[string]bool) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil
	}
	since := lastAssessmentRev(evs)
	nodeTitle := map[string]string{}
	var lines []string
	for _, ev := range evs {
		if since >= 0 && ev.Revision <= since {
			continue
		}
		// §11 触发事件带预写行动指令:事件行不只给事实,附"该做什么"
		//(如"人新增了目标:请据此补充探索方向")——主宰扫变化集的
		// 瞬间读到的就是研判路由,不是裸事实清单。
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			nodeTitle[p.NodeID] = p.Title
			switch p.NodeType {
			case rings.NodeGoal:
				if ev.Actor == "creator" {
					lines = append(lines, fmt.Sprintf("人新增了目标 %s [%s] %s → 据此补充探索方向", p.NodeID, p.Status, p.Title))
				} else {
					lines = append(lines, fmt.Sprintf("新增目标 %s [%s] %s → 纳入本轮规划", p.NodeID, p.Status, p.Title))
				}
			case rings.NodeAsset:
				if !memberIDs[p.NodeID] {
					continue
				}
				// 来源三态(P4-05):手动补入是造物主的动作指令——已默认
				// 纳入授权范围,研判路由按"评估覆盖缺口,优先派探测"预写。
				if rings.AssetSourceOf(p.Detail) == rings.AssetSourceManual {
					lines = append(lines, fmt.Sprintf("人手动补入资产 %s [%s] %s → 已默认纳入授权范围,评估覆盖缺口,优先派探测", p.NodeID, p.AssetClass, p.Title))
				} else {
					lines = append(lines, fmt.Sprintf("新资产 %s [%s] %s → 评估覆盖缺口,优先派探测", p.NodeID, p.AssetClass, p.Title))
				}
			case rings.NodeFact:
				lines = append(lines, fmt.Sprintf("新事实记录 %s [%s] %s → 按五问①③审视(可达性/组合)", p.NodeID, p.Status, p.Title))
			case rings.NodeTestClaim:
				lines = append(lines, fmt.Sprintf("新判断 %s [%s] %s → 跟进反证进展;确认后按五问②重估影响", p.NodeID, p.Status, p.Title))
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if stepIDRe.MatchString(p.NodeID) {
				lines = append(lines, fmt.Sprintf("步骤 %s → %s%s", p.NodeID, p.Status, stepStopDirective(p.Status)))
				continue
			}
			title := nodeTitle[p.NodeID]
			if title == "" {
				if n, err := l.Store.Node(ctx, l.TaskID, p.NodeID); err == nil {
					title = n.Title
				}
			}
			directive := ""
			switch p.Status {
			case "confirmed":
				directive = " → 新确认事实,按五问①③审视"
			case "achieved":
				directive = " → 目标已达成(机器判定)"
			}
			lines = append(lines, fmt.Sprintf("%s → %s(%s)%s", p.NodeID, p.Status, clipEval(title), directive))
		case rings.EventRoeUpdated:
			lines = append(lines, "RoE 授权边界已更新 → 复核后续派发是否越界")
			// 摘除清单变化(P4-05)单列一行:范围外资产即刻不派,正在
			// 覆盖它们的已派意图自然收尾(不做硬中断)。
			var rp rings.RoeUpdated
			if json.Unmarshal(ev.Payload, &rp) == nil && len(rp.ExcludedAssets) > 0 {
				lines = append(lines, fmt.Sprintf("授权范围摘除 %d 个资产 → 不再派发覆盖它们的意图", len(rp.ExcludedAssets)))
			}
		case rings.EventOperatorDive:
			// 触发行(P4-48):节点完整内容与血缘在「操作者深入指令」区。
			var p rings.OperatorDive
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			lines = append(lines, fmt.Sprintf("操作者深入指令:节点 %s(%s) → 本轮必须处理,见「操作者深入指令」区", p.NodeID, diveDirectionWord(p.Direction)))
		}
	}
	return lines
}

// lastAssessmentRev 事件流里最近一条研判记录(assessment)的
// EvaluatedRev;-1 = 无研判记录(首轮,全部历史作为变化集)。
func lastAssessmentRev(evs []rings.RawEvent) int64 {
	since := int64(-1)
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var r rings.OvermindRecord
		if json.Unmarshal(ev.Payload, &r) != nil {
			continue
		}
		if r.Type == rings.OvermindRecAssessment && r.EvaluatedRev > since {
			since = r.EvaluatedRev
		}
	}
	return since
}

// diveDirectionWord 深入指令方向的快照措辞(留空 = 主宰自行判断)。
func diveDirectionWord(direction string) string {
	if d := strings.TrimSpace(direction); d != "" {
		return "方向:" + clipEval(d)
	}
	return "方向留空,由你自行判断"
}

// diveBriefs 快照「操作者深入指令」区(P4-48,问题清单 #67):自上轮
// 研判以来的每条指令注入锚定节点完整内容与血缘——主宰读到的不是裸
// 指令,是该节点(成果/事实)的全貌:判断/影响面/证据、谁发现的
// (单位与步骤)、锚定哪些资产、(组合判断的)链环节。血缘走图上边
// (derived_from/produces/about),断链如实缺省不编造。
func (l *Loop) diveBriefs(ctx context.Context) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil
	}
	since := lastAssessmentRev(evs)
	if since < 0 {
		since = 0
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind != rings.EventOperatorDive || ev.Revision <= since {
			continue
		}
		var p rings.OperatorDive
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		out = append(out, l.diveNodeBrief(ctx, p)...)
	}
	return out
}

// diveNodeBrief 一条深入指令的锚定节点全貌(指令行 + 内容 + 血缘)。
func (l *Loop) diveNodeBrief(ctx context.Context, p rings.OperatorDive) []string {
	n, err := l.Store.Node(ctx, l.TaskID, p.NodeID)
	if err != nil {
		return []string{fmt.Sprintf("  - 指令:锚定节点 %s(读取失败:%v)· %s", p.NodeID, err, diveDirectionWord(p.Direction))}
	}
	var lines []string
	switch n.Type {
	case rings.NodeTestClaim:
		lines = append(lines, fmt.Sprintf("  - 指令:锚定发现 %s [%s] %s", n.ID, n.Status, diveDirectionWord(p.Direction)))
		lines = append(lines, fmt.Sprintf("    判断:%s", n.Title))
		if imp, _ := n.Detail["impact"].(string); imp != "" {
			lines = append(lines, fmt.Sprintf("    影响面:%s", clipEval(imp)))
		}
		if v, _ := n.Detail["vulnclass"].(string); v != "" {
			lines = append(lines, fmt.Sprintf("    类别:%s", v))
		}
		if u, _ := n.Detail["unit_id"].(string); u != "" {
			lines = append(lines, fmt.Sprintf("    发现单位:%s", u))
		}
		// 血缘:发现步骤(derived_from→Step)/组合链环节(detail.chain
		// 角色逐环节引用事实)/资产锚点(about→Asset)。
		if derived, derr := l.Store.Edges(ctx, l.TaskID, rings.EdgeDerivedFrom); derr == nil {
			for _, e := range derived {
				if e.From != n.ID {
					continue
				}
				if t, terr := l.Store.Node(ctx, l.TaskID, e.To); terr == nil && t.Type == rings.NodeStep {
					lines = append(lines, fmt.Sprintf("    发现步骤:%s %s", e.To, clipEval(t.Title)))
				}
			}
		}
		if raw, ok := n.Detail["chain"].([]any); ok {
			for _, item := range raw {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				fid, _ := m["fact_id"].(string)
				role, _ := m["role"].(string)
				if fid != "" {
					lines = append(lines, fmt.Sprintf("    链环节:%s(%s) %s", fid, role, clipEval(l.diveNodeTitle(ctx, fid))))
				}
			}
		}
		lines = append(lines, l.diveAssetAnchors(ctx, n.ID)...)
	case rings.NodeFact:
		lines = append(lines, fmt.Sprintf("  - 指令:锚定事实 %s [%s] %s", n.ID, n.Status, diveDirectionWord(p.Direction)))
		lines = append(lines, fmt.Sprintf("    内容:%s", n.Title))
		if eh, _ := n.Detail["evidence_head"].(string); eh != "" {
			lines = append(lines, fmt.Sprintf("    证据:%s", clipEval(eh)))
		} else if ev, _ := n.Detail["evidence"].(string); ev != "" {
			lines = append(lines, fmt.Sprintf("    证据:%s", clipEval(ev)))
		}
		if u, _ := n.Detail["unit_id"].(string); u != "" {
			lines = append(lines, fmt.Sprintf("    发现单位:%s", u))
		}
		// 血缘:产出步骤(produces←Step)。
		if produces, perr := l.Store.Edges(ctx, l.TaskID, rings.EdgeProduces); perr == nil {
			for _, e := range produces {
				if e.To != n.ID {
					continue
				}
				if t, terr := l.Store.Node(ctx, l.TaskID, e.From); terr == nil && t.Type == rings.NodeStep {
					lines = append(lines, fmt.Sprintf("    产出步骤:%s %s", e.From, clipEval(t.Title)))
				}
			}
		}
		lines = append(lines, l.diveAssetAnchors(ctx, n.ID)...)
	default:
		// 非成果/事实节点入口已拒;历史脏数据如实呈现不编造。
		lines = append(lines, fmt.Sprintf("  - 指令:锚定节点 %s(%s)[%s] %s", n.ID, n.Type, n.Status, diveDirectionWord(p.Direction)))
		lines = append(lines, fmt.Sprintf("    内容:%s", n.Title))
	}
	return lines
}

// diveAssetAnchors 锚定节点的资产锚点行(about→Asset)。
func (l *Loop) diveAssetAnchors(ctx context.Context, nodeID string) []string {
	abouts, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeAbout)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range abouts {
		if e.From != nodeID {
			continue
		}
		out = append(out, fmt.Sprintf("    资产锚点:%s %s", e.To, clipEval(l.diveNodeTitle(ctx, e.To))))
	}
	return out
}

// diveNodeTitle 节点标题回查(失败返空,如实缺省)。
func (l *Loop) diveNodeTitle(ctx context.Context, id string) string {
	if n, err := l.Store.Node(ctx, l.TaskID, id); err == nil {
		return n.Title
	}
	return ""
}

// stepStopDirective 步骤停法的行动指令(§11 重派判定树的研判路由面:
// done / exhausted / blocked / 拒绝各走各的对策;依据是轨迹不是措辞)。
func stepStopDirective(status string) string {
	switch status {
	case "done":
		return " → 检查归纳产出后按产出规划"
	case "exhausted":
		return " → 读轨迹定接力或换向(不凭状态措辞)"
	case "failed":
		return " → 读轨迹判因,凭新方法另派"
	case "blocked":
		return " → 按受阻原因换路,阻塞解除才回原路"
	case swarm.StepApprovalDenied:
		return " → 不换法重试;确有必要凭新证据另派"
	default:
		return ""
	}
}

// ---- 派发 ----

// stepIDRe 识别主宰派发的顺序步骤 ID(s1、s2…),用于续号。
var stepIDRe = regexp.MustCompile(`^s(\d+)$`)

// nextStepID 任务内顺序步骤 ID 续号(s1、s2…):意图派发与反证派发
// 共用同一序列(都是 Step,第四章"待办绑定在图上")。
func nextStepID(ctx context.Context, store *rings.Store, taskID string) (string, error) {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return "", fmt.Errorf("overmind: 查步骤续号: %w", err)
	}
	max := 0
	for _, s := range steps {
		if m := stepIDRe.FindStringSubmatch(s.ID); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > max {
				max = n
			}
		}
	}
	return fmt.Sprintf("s%d", max+1), nil
}

// dispatch 派发一条拆解产物:绑定模型明确选择的目标,stepID 在任务内
// 顺序续号。covers 端点由账本校验,幽灵资产整批拒绝(错误上抛)。
// P2-06:优先级落 step.detail.priority(词汇受限,非法拒派);前置
// 落 depends_on 边(端点必须是图上 Step,环状依赖拒绝)。
// P4-48:source=sourceUrgent(深入指令轮)时按"操作者急活"来源落账
// ——队列准入豁免、取消保护同急活;审批门照走。
func (l *Loop) dispatch(ctx context.Context, p IntentPlan, source dispatchSource) (string, error) {
	goalID := strings.TrimSpace(p.GoalID)
	if goalID == "" {
		goalID = FocusGoalID(ctx, l.Store, l.TaskID)
		if goalID == "" {
			goals, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeGoal, GoalStatusActive)
			if err != nil {
				return "", err
			}
			if len(goals) == 1 {
				goalID = goals[0].ID
			} else if len(goals) > 1 {
				return "", fmt.Errorf("任务有多个目标，意图须填写实际服务的目标编号goal_id")
			} else {
				all, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeGoal, "")
				if err != nil {
					return "", err
				}
				if len(all) > 0 {
					return "", fmt.Errorf("任务目标均已结束，本轮旧规划不再派发")
				}
			}
		}
	}
	stepID, err := nextStepID(ctx, l.Store, l.TaskID)
	if err != nil {
		return "", err
	}
	opt := DispatchOptions{
		goalMustBeActive:    true,
		GoalID:              goalID,
		ExpectedObservation: p.ExpectedObservation,
		ActionLevel:         p.ActionLevel,
		HarmClass:           p.HarmClass,
		CoversAssets:        p.Covers,
		Priority:            p.Priority,
		DependsOn:           p.DependsOn,
		SourceRefs:          p.SourceRefs,
	}
	if source == sourceUrgent {
		opt.source = sourceUrgent
		opt.Detail = map[string]any{"urgent": true, "by": "dive"}
	}
	if err := DispatchStep(ctx, l.Store, l.TaskID, stepID, strings.TrimSpace(p.Intent), opt); err != nil {
		return "", fmt.Errorf("overmind: 派发 %s(%s): %w", stepID, p.Intent, err)
	}
	return stepID, nil
}

// CurrentGoalID 图上最新目标(优先聚焦方向(P4-54 停聚焦:拆解产物与
// 急活默认服务聚焦方向)、再 active):牧者紧急意图派发(UrgentDispatch)
// 的缺省目标。普通拆解由模型明确选择实际服务的目标。
func CurrentGoalID(ctx context.Context, store *rings.Store, taskID string) string {
	if fg := FocusGoalID(ctx, store, taskID); fg != "" {
		return fg
	}
	pick := func(nodes []rings.NodeFull) string {
		if len(nodes) == 0 {
			return ""
		}
		return nodes[len(nodes)-1].ID
	}
	if active, err := store.NodesByType(ctx, taskID, rings.NodeGoal, "active"); err == nil {
		if id := pick(active); id != "" {
			return id
		}
	}
	all, err := store.NodesByType(ctx, taskID, rings.NodeGoal, "")
	if err != nil {
		return ""
	}
	return pick(all)
}

// UrgentDispatch 紧急意图派发(P2-07 牧者 urgent_intent 工具的落点,
// 第四章 §2"一切操控走账本"与 §1 牧者"说"职责):造物主的急活以
// high 优先级派发为 Step、归属当前目标,虫群单位照常从图上领取,
// 纪律不破(派发校验:分级必填合法、covers 须图上真实资产);主宰
// 下一轮研判可见(快照步骤区含此步骤),事后追认或另派。stepID 在
// 任务内顺序续号(与拆解派发同一序列)。
func UrgentDispatch(ctx context.Context, store *rings.Store, taskID, intent, expected, actionLevel string, covers []string) (string, error) {
	stepID, err := nextStepID(ctx, store, taskID)
	if err != nil {
		return "", err
	}
	opt := DispatchOptions{
		source:              sourceUrgent,
		GoalID:              CurrentGoalID(ctx, store, taskID),
		ExpectedObservation: expected,
		ActionLevel:         actionLevel,
		Priority:            PriorityHigh,
		CoversAssets:        covers,
		Detail:              map[string]any{"urgent": true, "by": "shepherd"},
	}
	if err := DispatchStep(ctx, store, taskID, stepID, intent, opt); err != nil {
		return "", err
	}
	return stepID, nil
}

// ---- 调度与收割 ----

// resolveModelWaits 模型故障停等对账(P5-05):model_wait 步骤在模型
// 链可用(不再全员冷却)时唤醒回 dispatched+队首档(已批准待上位语
// 义,重领单位原会话 Resume 续跑、R14 复检照走)。链就绪探测走
// Provider 的冷却面(AllCooling);Provider 未实现该面(非 failover 链
// 装配,单测直连)视为可用——退"领取即重试"的既有形态。有空位立即
// 拉起,无空位队首等位(StepLaunchLess 既有)。
func (l *Loop) resolveModelWaits(ctx context.Context) {
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, rings.StepModelWait)
	if err != nil || len(steps) == 0 {
		return
	}
	if l.modelChainCooling() {
		return
	}
	var evs []rings.Event
	for _, s := range steps {
		evs = append(evs, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
			Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "dispatched", Note: "scope_head"},
		})
	}
	if _, err := l.Store.Append(ctx, l.TaskID, evs...); err != nil {
		l.setErr(fmt.Errorf("overmind: 模型停等对账唤醒: %w", err))
		return
	}
	runlog.Infof("模型停等对账: %s 唤醒 %d 个让位单位(队首)", l.TaskID, len(steps))
	l.schedule(ctx)
}

// modelChainCooling 模型链是否全员冷却(无可路由腿)。Provider 未实现
// 冷却面视为可用(fail-safe 到重试)。
func (l *Loop) modelChainCooling() bool {
	if l.Swarm == nil || l.Swarm.Provider == nil {
		return false
	}
	c, ok := l.Swarm.Provider.(interface{ AllCooling() bool })
	return ok && c.AllCooling()
}

// schedule 调度虫群(P4-04 重构:启动即返,不等批):把全部 dispatched
// 步骤中可执行者各派一只单位(一 Step 一单位),并发受 unitLimit(默认
// 3)与在飞计数 flight 限制——单位生命周期独立于循环迭代,批内先落地
// 者不阻塞循环拿研判轮。依赖闸(P2-06,第四章 §2 依赖边):depends_on
// 前置未全部完成的步骤不派发,保持 dispatched 停在图上等待(前置完成
// 后下一轮迭代/落地续派再领取)。R14 拒绝的步骤保持 dispatched,留给
// 下一轮迭代/落地续派重领(R16 接力);收尾中/断点武装/已完成后不再
// 起新单位,任务总闸到点先推进收尾。返回本轮启动单位的步骤数(静默
// 判定依据)。
//
// 两路并发调用安全(循环迭代与单位落地的续派):挑选与计数在同一
// 临界区,同一步骤不会被重复起飞。循环迭代与落地续派都走这里——
// 名额空出的等待中步骤由落地路径零延迟交接,不等下一轮拆解。
func (l *Loop) schedule(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	if l.Swarm == nil {
		return 0 // 未装配执行器(测试/空转循环):无可派单位,不进起飞路径
	}
	if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
		return 0
	}
	if task, err := l.Store.Task(ctx, l.TaskID); err == nil {
		switch task.Status {
		case "hibernated", "checkpointed", "paused", "stopped", "completed", "settling":
			return 0
		}
	} else if !errors.Is(err, rings.ErrTaskNotFound) {
		l.setErr(err)
		return 0
	}
	// 任务总闸到点:不再起新单位,推进收尾(enterSettling 内部持 l.mu,
	// 须在临界区外调用)。
	if dl := l.currentDeadline(); !dl.IsZero() && !time.Now().Before(dl) {
		l.enterSettling(ctx)
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.settling || l.finished || l.ckptArmed() {
		return 0
	}
	runnable, err := l.readySteps(ctx)
	if err != nil {
		l.lastErr = err
		return 0
	}
	limit := l.unitLimit()
	started := 0
	for _, s := range runnable {
		if l.flight >= limit {
			break
		}
		if _, dup := l.launching[s.ID]; dup {
			continue // 已起飞未落地:两路并发挑选的去重(见字段注释)
		}
		if l.launching == nil {
			l.launching = make(map[string]struct{})
		}
		l.launching[s.ID] = struct{}{}
		l.flight++
		started++
		l.launchUnit(ctx, s.ID)
	}
	return started
}

// readySteps is shared by scheduling and final completion. It reads the step
// set once; queue membership and dependency truth come from that same set.
func (l *Loop) readySteps(ctx context.Context) ([]rings.NodeFull, error) {
	all, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 查调度步骤: %w", err)
	}
	var queued []rings.NodeFull
	for _, step := range all {
		if step.Status == "dispatched" {
			queued = append(queued, step)
		}
	}
	if len(queued) == 0 {
		return nil, nil
	}
	edges, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDependsOn)
	if err != nil {
		return nil, err
	}
	return selectRunnable(queued, all, edges), nil
}

// launchUnit 起飞一只单位并照料落地(P4-04):执行 → 在飞递减 → 落地
// 通知(landKick——循环增量研判的唤醒源,容量 1 合并)→ 名额空出即
// 时续派(等待中的可执行步骤零延迟交接,不等下一轮拆解)。调用方持
// l.mu 且已 flight++(名额已占);本函数不等单位结束。
func (l *Loop) launchUnit(ctx context.Context, stepID string) {
	l.unitsWG.Add(1)
	go func() {
		defer l.unitsWG.Done()
		landed := l.runUnit(ctx, stepID)
		l.mu.Lock()
		l.flight--
		delete(l.launching, stepID)
		l.mu.Unlock()
		if !landed {
			// R14 拒绝/领取失败:不是结果落地。仍发通知(计数变化需要
			// 循环重估等待),但续派交还循环迭代,避免对同一步骤的
			// 领取竞态就地复读。
			select {
			case l.landKick <- struct{}{}:
			default:
			}
			return
		}
		select {
		case l.landKick <- struct{}{}:
		default:
		}
		l.schedule(ctx)
	}()
}

// selectRunnable 派发策略的确定性选步(P2-06):依赖闸——depends_on
// 前置(全图任意状态)全部 done 才可执行,其余等待;优先级排序——
// high > normal > low,同级先急活(造物主插急活/深入指令派生,权威
// 最高)、其余按创建版本先后(先派先执行)。前置未完成的步骤不是
// 错误,是图上可见的等待态(主宰快照可见"前置 [sN]",研判可换路
// 重派)。dispatched 为待执行步骤集,all 为全图步骤(依赖闸需查前置
// 的真实状态)。
func selectRunnable(dispatched, all []rings.NodeFull, depEdges []rings.EdgeView) []rings.NodeFull {
	out := RunnableSteps(dispatched, all, depEdges)
	sort.Slice(out, func(i, j int) bool { return StepLaunchLess(out[i], out[j]) })
	return out
}

// StepLaunchLess 步骤领取次序的全序比较(调度器与面板排队列表同源,
// 展示与操作同源纪律):队首档——范围批准回队(P4-51 ②,scope_head)
// 与补做证据/重验拉起(P5-02④,retake)同档压队首、同档按进档序
// (head_seq=拉起/批准时图版本)先后不抢 → 优先级降序 → 同级急活 →
// 创建版本升序 → ID 兜底(全图无同 rev 同 ID,仅为全序确定性)。
func StepLaunchLess(a, b rings.NodeFull) bool {
	ha, hb := headOf(a), headOf(b)
	if ha != hb {
		return ha
	}
	if ha {
		if sa, sb := headSeqOf(a), headSeqOf(b); sa != sb {
			return sa < sb
		}
	}
	pa, pb := stepPriorityRank(a), stepPriorityRank(b)
	if pa != pb {
		return pa > pb
	}
	ca, cb := stepClassRank(a), stepClassRank(b)
	if ca != cb {
		return ca < cb
	}
	if a.CreatedRev != b.CreatedRev {
		return a.CreatedRev < b.CreatedRev
	}
	return a.ID < b.ID
}

// headOf 步骤是否进队首档(与"已批准待上位"同档):范围批准回队标记
// (P4-51 ②,detail.scope_head——被打断的老单位优先于一切新排队者)
// 或补做证据/重验拉起步骤(P5-02④,detail.kind=retake;三查不过的
// 补考与 reopen 重验按拉起序上位,显示统一"排队中")。
func headOf(s rings.NodeFull) bool {
	if h, _ := s.Detail["scope_head"].(bool); h {
		return true
	}
	k, _ := s.Detail["kind"].(string)
	return k == retakeStepKind
}

// headSeqOf 队首档内的进档序(detail.head_seq=拉起/批准时的图版本;
// 旧 scope_head 步骤无值取 0,先来先上位)。
func headSeqOf(s rings.NodeFull) int64 {
	v, _ := s.Detail["head_seq"].(float64)
	return int64(v)
}

// stepClassRank 同优先级内的类别权重:急活(造物主插急活/深入指令派生)
// 0 < 其余 1。P5-03②:反证档位已随三段流水线退役删除(豁免与提档
// 同批清理,新队首档只有"已批准待上位/补做证据"压队首,见 headOf)。
func stepClassRank(s rings.NodeFull) int {
	if urgent, _ := s.Detail["urgent"].(bool); urgent {
		return 0
	}
	return 1
}

// stepPriorityRank 步骤的优先级排序权重(detail.priority;缺省 normal)。
func stepPriorityRank(s rings.NodeFull) int {
	p, _ := s.Detail["priority"].(string)
	if r := PriorityRank(p); r > 0 {
		return r
	}
	return PriorityRank(PriorityNormal)
}

// runUnit 一只虫群单位:RunStep(领取+复检+起飞同临界区,并发安全)
// → 执行。R14 拒绝不改步骤状态(回 dispatched 待重领);熔断收尾
// 静默让位;其余错误执行器已记账,此处如实暴露到 lastErr。单位落
// blocked 计任务级 LLM 失败(P2-06e;P4-27 ⑦:确定性拒绝归因除外
// ——程序缺陷非模型故障,不污染健康判定),完成即记成功(清计数)。
// P4-04 返回是否真实落地(有单位会话跑完并落终态账;R14/领取竞态/
// 熔断让位不算)——落地通知的判定依据。
func (l *Loop) runUnit(ctx context.Context, stepID string) bool {
	res, err := l.Swarm.RunStep(ctx, l.TaskID, stepID)
	if res != nil {
		switch res.StepStatus {
		case swarm.StepBlocked:
			if res.BlockedClass != swarm.BlockedClassDeterminant {
				l.noteLLMFailure(ctx, res.Reason)
			}
		case "done":
			l.noteLLMSuccess()
		}
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, swarm.ErrStaleGraph) {
			l.setErr(fmt.Errorf("overmind: 执行 %s: %w", stepID, err))
		}
	}
	return res != nil && !res.Stale
}

// denialBriefs 审批拒绝简报(P2-06b,第四章 §3 v2:主宰收割可见拒绝
// 原因):扫事件流里 denial 类 approval.record(排除等待期间任务收尾的
// closeout 结案——那是任务级中断不是审批裁决),逐步骤取最新一笔拒绝。
// P4-51 ⑤:"规划后放行"写路径废除(历史 forced 记录留档,简报不再
// 消费)。确定性拼装,零 LLM。
func (l *Loop) denialBriefs(ctx context.Context) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 审批拒绝查事件: %w", err))
		return nil
	}
	latest := map[string]rings.ApprovalRecord{} // requestID -> 最新拒绝
	order := []string{}
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var rec rings.ApprovalRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		if rec.Type != rings.ApprovalRecDenied {
			continue
		}
		if rec.DenyLine == rings.DenyLineCloseout {
			continue
		}
		if _, ok := latest[rec.RequestID]; !ok {
			order = append(order, rec.RequestID)
		}
		latest[rec.RequestID] = rec
	}
	var out []string
	for _, id := range order {
		rec := latest[id]
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s | 工具 %s | 原因: %s", rec.StepID, clipEval(rec.Intent),
			rec.ToolName, clipEval(rec.Reason))
		out = append(out, b.String())
	}
	return out
}
