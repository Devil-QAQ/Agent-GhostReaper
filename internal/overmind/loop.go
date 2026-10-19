package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
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
// 跑的虫群单位至多 3 只。
const DefaultMaxConcurrentUnits = 3

// DefaultSettleGraceSeconds 收尾宽限默认值(第四章 §6:在跑单位优雅
// 收尾 90 秒,超时硬停)。
const DefaultSettleGraceSeconds = 90

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

	// Ckpt 断点冻结信号(P3-07,第七章「故障与恢复」断点续接):武装后
	// 循环在轮次边界退出(不再拆解派发,在飞单位由虫群侧同一信号收场
	// 落地),任务让位为已存档断点、进程可关。装配层与 Manager/虫群
	// 共持同一实例;nil = 无断点语义(单测)。
	Ckpt *ckpt.Gate

	mu       sync.Mutex
	lastErr  error
	settling bool // 已进入收尾(停止派发;l.mu 保护——定时器/循环两路写)
	// pauseCh 主宰循环的暂停闩(P2-08 单位控制的主宰循环粒度):nil/已
	// 关闭 = 运行中,开放 = 暂停中(轮次边界挂起)。l.mu 保护。
	pauseCh chan struct{}
	// guardRejects 入口守卫拒收简报(P2-06c 拒因回流):主宰拆解产物被
	// 受控入口拒收(组合声明链环节未确认 R13、意图派发 covers 幽灵资产
	// 等)时的拒因摘要,注入下一轮态势快照——主宰可见上次拒因,对症
	// 纠偏,不再原样重试。拒收只进内存简报不进事件流(拆解产物
	// 本就未入账,账本无此事实可记);进程内限注:重启即清空,语义
	// 与 LastErr 一致。限量防噪:环形保留最近 maxGuardRejects 条,
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
	seq          int       // 主宰会话序号(转录/用量记账锚点)

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
	// flight 在飞单位数、launching 已起飞未落地的步骤集、finished 循环
	// 已判完成(l.mu 保护):并发上限由 flight 计数控制(跨轮次持续,
	// 单位生命周期独立于循环迭代);launching 关闭"挑选与领取之间的
	// 异步间隙"里的双重起飞——循环迭代与落地续派两路并发挑选时,同
	// 一 dispatched 步骤不会被两只单位先后领取(Claim 不校验状态,后
	// 到者会把先到者的在途会话误判为崩溃残留而"重启收养");finished
	// 关闭"静默判定后尾部续派再起新单位"的竞态——完成收口与 schedule
	// 起单位在同一临界区互斥。
	flight    int
	launching map[string]struct{}
	finished  bool
}

// isSettling 是否已进入收尾(l.mu 保护)。
func (l *Loop) isSettling() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.settling
}

// LastErr 最近一次拆解/派发/执行/收割错误(诊断用)。循环不因单点
// 错误退出:如实记录后回到静默,等下一次唤醒重试。
func (l *Loop) LastErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
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

// noteLLMFailure 记一次任务级 LLM 失败(P2-06e):达阈值且未记过时落
// 黑匣子 task_blocked(带原因);幂等(受阻期间不重复记)。留痕走独立
// ctx(记账不随任务熔断断链)。
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
				"note": "模型调用恢复,任务受阻解除(图上 blocked 步骤经配置切换或恢复全部重开)",
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
// 执行→收割,反复至无新意图);ctx 结束(熔断/暂停)时退出,在飞
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
	l.runDone = make(chan struct{})
	defer close(l.runDone)
	l.settleKick = make(chan struct{}, 1)
	l.hbKick = make(chan struct{}, 1)
	l.landKick = make(chan struct{}, 1)
	// P4-04:在飞单位收拢后再让位(终态记账先落,与旧批内 wg.Wait 同序)。
	defer l.unitsWG.Wait()
	go l.heartbeatLoop(ctx)
	l.loadDeadline(ctx)
	if !l.currentDeadline().IsZero() {
		go l.settleWatch(ctx)
		l.armHardStop(ctx)
	}
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
			if l.cycle(ctx) {
				return
			}
		case <-l.landKick: // P4-04:单位落地 → 增量研判轮
			if l.cycle(ctx) {
				return
			}
		case <-wake:
			if l.cycle(ctx) {
				return // 任务完成:退出循环,完成终态由 Manager 记账
			}
		}
	}
}

// ckptArmed 断点冻结信号当前是否武装(nil 门 = 否)。
func (l *Loop) ckptArmed() bool {
	return l.Ckpt != nil && l.Ckpt.Armed(l.TaskID)
}

// ---- P2-08 主宰循环的挂起与暂停 ----

// parkGate 轮次边界的挂起检查:任务冬眠门与循环暂停闩任一在即挂起,
// 全开放行(false = ctx 结束,循环退出)。唤醒后顺延收尾线(冬眠期间
// 任务总闸墙钟停走);两源都复查,先唤醒又立刻暂停的次序不漏。
func (l *Loop) parkGate(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
			if !l.Hiber.Wait(ctx, l.TaskID) {
				return false
			}
			l.extendDeadline()
			continue
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

// cycle 一次唤醒的处理:反复"TestClaim 确认判定 → 收割(候选事实转正
// + 候选声明派反证 + 候选假设收割评估/验证结论迁移)→ Goal 完成判定
// → LLM 拆解 → 派发 Step → 调度虫群",直到一轮下来全无活动(零裁决、
// 零派发、零执行、零收割产出)。全部目标经机器判定达成即停派(P1-06
// 语义),但已派出的在飞工作(含反证/验证 Step)照常收尾——P1-08
// 时序不变式:反证/验证 Step 靠 ran/judged/收割计数把循环撑到终态,
// 不得因目标达成而遗弃。真正静默且目标全部达成时返回 true(P1-10:
// 循环退出,任务进完成终态,completed 由 Manager 记账),拆解失败
// 不派发不崩溃(留 LastErr),等下一次唤醒重试。ctx 结束时尽快收尾:
// 不再派发,在飞单位随 ctx 中止。
// P4-04 增量粒度:收割前置到拆解之前(落地即转正,本轮研判的态势
// 快照就含最新增量);调度不等批(schedule 起单位即返);全无活动但
// 仍有在飞单位时等下一只落地(waitLanding)——任一单位结果落地,
// 本轮即基于增量规划下一步,在途步骤不阻塞对新结果的响应。
func (l *Loop) cycle(ctx context.Context) bool {
	// 直驱 cycle 的路径(单测)无 Run 装配:落地通知通道按需建(Run
	// 正常装配时已建,此处幂等;建先于任何单位起飞,无竞态)。
	if l.landKick == nil {
		l.landKick = make(chan struct{}, 1)
	}
	for ctx.Err() == nil {
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
		// ① TestClaim 确认判定:反证轮已到终态的声明交仲裁者机器裁决
		// (R2/R3:执行成功 ≠ 结论成立,确认只认机器求值)。先于收割与
		// Goal 判定——目标达成停派时不得把反证中的声明永久搁置。
		judged, err := l.judgeClaims(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		// ② 收割前置(P4-04):虫群刚写回的候选即刻转正/派反证/评估
		// 假设——本轮拆解的态势快照即含最新增量(落地与研判紧邻的前提)。
		// 候选声明派反证 Step(第三章 §5:反证测试 = 普通意图,由不同
		// 虫群单位领取,R12);候选假设收割评估(P2-03,第四章 §5:三
		// 去向裁决 + 验证结论迁移)。派出的反证/验证由本轮 ④ 调度执行。
		if _, err := Harvest(ctx, l.Store, l.TaskID); err != nil {
			l.setErr(err)
			return false
		}
		claims, err := HarvestClaims(ctx, l.Store, l.TaskID)
		if err != nil {
			l.setErr(err)
			return false
		}
		hypos, err := l.harvestHypotheses(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		// ③ Goal 完成判定:机器终审先于 LLM 拆解(R2——连主宰的
		// "看起来完成了"都不算数,只有仲裁者对冻结谓词的求值算数)。
		// 达成即停派,但在飞工作继续收尾(waitLanding 等齐)。
		done, err := l.judgeGoals(ctx)
		if err != nil {
			l.setErr(err)
			return false
		}
		dispatched := 0
		var combinedIDs []string
		if !done {
			reply, evalRev, err := l.decompose(ctx)
			if err != nil {
				l.setErr(err)
				return false
			}
			// 组合发现(五问第③问的产物):多条已确认 Fact 合并为攻击链级
			// TestClaim,血缘(derived_from × N + about)随创建一批落账;
			// 之后走 HarvestClaims 标准收割派反证(R12:发现者=主宰,
			// 反证必为虫群独立单位)。单条失败不拖垮其余,如实留 LastErr;
			// 拒因回流(P2-06c):守卫拒收(链环节未确认 R13 等)进简报,
			// 注入下一轮快照——主宰对症纠偏,不盲目原样重试。
			for _, cp := range reply.CombinedClaims {
				cid, err := SubmitCombinedClaim(ctx, l.Store, l.TaskID,
					cp.Statement, cp.Impact, cp.Rationale,
					chainLinksOf(cp.Chain), cp.AssetID, cp.Vulnclass, cp.Alternatives)
				if err != nil {
					if ctx.Err() == nil {
						l.setErr(fmt.Errorf("overmind: 组合声明: %w", err))
						l.noteGuardReject(fmt.Sprintf("组合声明「%s」被拒: %v", cp.Statement, err))
					}
					continue
				}
				combinedIDs = append(combinedIDs, cid)
			}
			for _, p := range reply.NewIntents {
				if err := l.dispatch(ctx, p); err != nil {
					// 单条派发失败(如 covers 指向不存在的资产被账本整批拒)
					// 不拖垮其余意图;失败如实留在 LastErr,拒因回流进简报
					//(P2-06c,同上)。
					if ctx.Err() == nil {
						l.setErr(err)
						l.noteGuardReject(fmt.Sprintf("意图「%s」被拒: %v", p.Intent, err))
					}
					continue
				}
				dispatched++
			}
			// 纳谏裁决(P2-07,第四章 §7:对主宰 = 建议进正式规划,裁决后
			// 改):本轮研判对全部待裁决建议落裁决事件,建议状态随之改变;
			// 采纳的体现由拆解规则 14 约束(本轮意图或理由中说明)。
			l.writeAdviceVerdicts(ctx, reply.Advices)
			// 研判记录(P2-06 五问驱动研判从账本可验证):研判结论有内容时
			// 落 overmind.record(assessment)——五问逐问结论 + 派发摘要 +
			// 组合声明 ID。纯派发/组合而无研判文本不单独落账(派发与组合
			// 在账本本就全量可查,空壳研判记录无信息量,账本侧亦拒收)。
			if reply.Assessment.hasContent() {
				a := reply.Assessment
				rec := rings.OvermindRecord{
					Type: rings.OvermindRecAssessment, SessionID: reply.sessionID,
					EvaluatedRev: evalRev, Dispatched: dispatched, Combined: combinedIDs,
					QAccess:      strings.TrimSpace(a.QAccess),
					QPrivilege:   strings.TrimSpace(a.QPrivilege),
					QCombination: strings.TrimSpace(a.QCombination),
					QUnlock:      strings.TrimSpace(a.QUnlock),
					QOverturn:    strings.TrimSpace(a.QOverturn),
					Note:         strings.TrimSpace(a.Note),
				}
				if _, err := l.Store.Append(ctx, l.TaskID, rings.Event{
					Kind: rings.EventOvermindRecord, Actor: ActorOvermind, Payload: rec,
				}); err != nil {
					l.setErr(fmt.Errorf("overmind: 研判记录入账: %w", err))
				}
			}
			// 组合声明的反证即时派发(P4-04 收割前置的补位):前置收割发生
			// 在本轮拆解之前,刚提交的组合声明要等下一轮才会有反证 Step——
			// 本轮立即补收割,反证照常进本轮调度,不得遗弃到下一次外部
			// 唤醒(与虫群候选声明的派发节奏对齐)。
			if len(combinedIDs) > 0 {
				if c2, err := HarvestClaims(ctx, l.Store, l.TaskID); err != nil {
					l.setErr(err)
				} else {
					claims = c2
				}
			}
		}
		// ④ 调度(P4-04 不等批):本轮派发的意图与图上遗留的 dispatched
		// 步骤(如收割派出的反证 Step,目标达成后亦照常执行)一并启动
		// 单位,启动即返;单位生命周期独立于本轮迭代,落地通知见 ⑤。
		ran := l.schedule(ctx)
		if ctx.Err() != nil {
			return false
		}
		// ⑤ 静默判定:本轮零裁决、零派发、零执行、零收割产出 → 仍有
		// 在飞单位时等下一只落地(增量响应点,不空转烧拆解);真正无
		// 在飞才回静默。拆解产物全灭时立即重拆解只会拿到同样的坏产物,
		// 形成零进展热循环(每圈烧一次 LLM 调用)——留 LastErr 等下一次
		// 唤醒。
		if judged == 0 && dispatched == 0 && ran == 0 && len(claims.Dispatched) == 0 && hypos == 0 {
			if l.waitLanding(ctx) {
				continue // 有新落地(或收尾/心跳信号):回顶收割→拆解增量轮
			}
			// 静默点复判目标:收割可能在本次迭代内补齐了最后的达成证据
			// (如候选事实转正),完成不必等下一次唤醒迟到。此刻无任何
			// 在飞工作,全部达成即任务完成(P1-10)。
			settled, err := l.judgeGoals(ctx)
			if err != nil {
				l.setErr(err)
				return false
			}
			// 完成收口(P4-04):与调度起单位同临界区互斥——目标全达成
			// 且在飞为零时锁定完成(finished 后调度不再起新单位,关
			// "判定后尾部续派"竞态);竞态下又有单位起飞则回迭代顶。
			l.mu.Lock()
			quiet := l.flight == 0 && !l.finished
			if quiet && settled {
				l.finished = true
			}
			l.mu.Unlock()
			if !quiet {
				continue
			}
			return settled
		}
	}
	return false
}

// waitLanding 等待下一只单位落地(P4-04 增量响应):true = 有新落地
// (或收尾线/心跳信号,回迭代顶处理);false = 无在飞单位且无未消费
// 的落地通知——静默,回 Run 等外部唤醒。落地通知容量 1 合并:同窗
// 多只落地并成一轮,轮内收割全可见。等待期间任务总闸到点
// (settleKick)与心跳预筛发现(hbKick)同样打断——迭代顶的检查自会
// 路由进收尾/复查。
func (l *Loop) waitLanding(ctx context.Context) bool {
	if l.inFlight() == 0 {
		// 计数已清零但通知未消费(落地写账晚于本轮收割的竞态):补一轮,
		// 下一迭代的前置收割会处理其账本写入。
		select {
		case <-l.landKick:
			return true
		default:
		}
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-l.settleKick:
		return true // 顶部收尾检查接管
	case <-l.hbKick:
		return true // 心跳发现:回顶复查
	case <-l.landKick:
		return true
	}
}

// inFlight 在飞单位数快照(l.mu 保护)。
func (l *Loop) inFlight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flight
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
			return false, fmt.Errorf("overmind: 目标 %s 裁决落账: %w", g.ID, err)
		}
	}
	return !pending, nil
}

// ---- TestClaim 确认判定(P1-08 + P2-02,仲裁者接管) ----

// judgeClaims 逐个裁决 testing 状态的声明:当前反证轮已到终态者交
// 仲裁者机器求值,裁决审计流(testclaim.record verdict)+ 状态迁移
// (confirmed|pending|awaiting_round)一批事务落账(actor=arbiter);
// Confirmed 的同批再落收割定稿(testclaim.record repro:复现步骤 +
// 严重级别,P2-02)。反证未完(ErrClaimNotReady)留待下轮;单声明
// 完整性错误如实暴露到 LastErr 但不拖垮其余声明。待确认/待下轮从此
// 带机器生成的搁置/推进理由(R4)。返回本轮落账的裁决数(静默判定依据)。
func (l *Loop) judgeClaims(ctx context.Context) (int, error) {
	claims, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeTestClaim, rings.TestClaimTesting)
	if err != nil {
		return 0, fmt.Errorf("overmind: 判定: 查声明: %w", err)
	}
	judged := 0
	for _, c := range claims {
		j, err := arbiter.JudgeClaim(ctx, l.Store, l.TaskID, c.ID)
		if errors.Is(err, arbiter.ErrClaimNotReady) {
			continue // 当前反证轮未结束,留待下轮
		}
		if err != nil {
			l.setErr(fmt.Errorf("overmind: 判定声明 %s: %w", c.ID, err))
			continue
		}
		// 落账次序:裁决 → (确认时)收割定稿 → 状态迁移,同一批事务。
		events := []rings.Event{
			{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: j.Verdict},
		}
		if j.Status == rings.TestClaimConfirmed {
			// 收割定稿(P2-02):复现步骤(镜留痕交换+操作方法)与
			// 严重级别(末轮影响面定级)随确认同批落库,报告与面板
			// 只读这份定稿(R17:智能发生在写回时)。
			repro, rerr := AssembleRepro(ctx, l.Store, l.TaskID, c.ID, j.Severity)
			if rerr != nil {
				l.setErr(fmt.Errorf("overmind: 定稿声明 %s: %w", c.ID, rerr))
			} else {
				events = append(events, rings.Event{
					Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: *repro})
			}
		}
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: j.Status}})
		if _, err := l.Store.Append(ctx, l.TaskID, events...); err != nil {
			return judged, fmt.Errorf("overmind: 声明 %s 裁决落账: %w", c.ID, err)
		}
		judged++
	}
	return judged, nil
}

// ---- 拆解(态势研判五问驱动,P2-06 完整版) ----

// IntentPlan 拆解产物:一条待派发意图(Step 的原料)。
type IntentPlan struct {
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
	DependsOn []string `json:"depends_on"`
}

// ChainLinkPlan 拆解回复里的组合声明链环节(引用态势快照中的已确认 Fact)。
type ChainLinkPlan struct {
	FactID string `json:"fact_id"`
	Role   string `json:"role"`
}

// CombinedClaimPlan 拆解回复里的组合声明草稿(五问第③问"两条线索组合成
// 新的边界问题"的直接产物):多条已确认 Fact 合并为攻击链级发现。
type CombinedClaimPlan struct {
	Statement    string          `json:"statement"`
	Impact       string          `json:"impact"`
	Rationale    string          `json:"rationale"`
	Chain        []ChainLinkPlan `json:"chain"`
	AssetID      string          `json:"asset_id"`
	Vulnclass    string          `json:"vulnclass"`
	Alternatives []string        `json:"alternatives"`
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
	Assessment     *AssessmentPlan     `json:"assessment"`
	NewIntents     []IntentPlan        `json:"new_intents"`
	CombinedClaims []CombinedClaimPlan `json:"combined_claims"`
	Advices        []adviceVerdictPlan `json:"advices"` // 待裁决建议的裁决(P2-07 纳谏)

	sessionID string // 研判会话锚点(研判记录下钻转录用;不参与 JSON)
}

// SettingKeyPromptOvermind 主宰拆解基线可编辑正文(A 段)的配置键
// (P2-06d,通用配置键值表)。
const SettingKeyPromptOvermind = "prompt.overmind.editable"

// overmindEditableDefault 主宰拆解基线 A 段默认正文(角色与研判总则;
// 可编辑)。五问与拆解纪律、输出契约是代码固定段(见下),编辑正文
// 删不掉。
const overmindEditableDefault = `你是渗透测试任务的主宰(Overmind),agents 内部唯一计划权威,负责态势研判与意图拆解:先按标准五问研判态势,再把当前目标拆解为下一步交给虫群执行的意图(Step)。你的判断只来自态势快照中的图上事实,不持有私有记忆;拆解产物会被入口校验,引用不存在的资产或链上未确认的环节会被整批拒收。`

// overmindDisciplineRules 主宰基线 B 段(代码固定,研判与拆解纪律):
// P1-05 最简起步,P1-07 补四级分类细则,P2-04 补预算接力规则,P2-06
// 五问驱动研判与派发策略,P2-06d 补 §11 语义细则(意图数量硬底线/
// 多样性条款/复核上限/重派判定树/收尾词——防停摆与防乱派双向钉住、
// 防同方向复读)。
const overmindDisciplineRules = `态势研判标准五问(逐问给一句结论,写入 assessment;该问本轮无可研判项就写"无",不要编造;五问是起点不是上限,五问之外的重要研判写入 assessment.note):
1. 新身份能访问哪些已有资产?(新凭据/新会话/新入口对已有资产的可达性)
2. 新权限是否改变其他漏洞的影响?(原有发现在新权限下的实际影响是否变化)
3. 两条单独影响有限的线索是否组合成新的边界问题?(组合成攻击链时用 combined_claims 提交组合声明)
4. 新的可达关系是否解锁之前受阻的步骤?(exhausted/failed 的步骤是否值得换路重派)
5. 新证据是否推翻旧判断?(已有事实/声明是否与新证据矛盾)
拆解规则:
1. 只依据态势快照中真实存在的资产与已确认事实派发意图;意图必须具体、可执行、可观察。
2. 不重复派发与现有步骤(任何状态)相同或等价的意图。
3. 无值得新派发的意图时输出空列表,不要为了输出而编造意图——研判后确无工作时 0 条意图是完全正常的结果(收尾词;但受第 10 条硬底线约束)。
4. 每条意图必须定动作分级,分档标准按动作对目标环境的因果方向从严选取:
   - 只读探测:只获取信息,不发送攻击载荷、不改变目标任何数据与状态,重复执行结果一致(端口判定、banner 抓取、GET 拉取页面、DNS/公开信息收集)。
   - 主动测试:向目标发送测试载荷检验响应(注入探测、目录枚举、弱口令尝试、畸形请求);发了非正常业务流量但不利用成功结果、不落地持久物。
   - 利用深入:以利用已确认弱点为前提,实际取得未授权访问或数据(读敏感文件、无副作用命令回显、凭据试用、横向探测);仍不修改目标状态。
   - 修改与持久化:改变目标状态或留下长期存在物(写文件、上传 webshell、建账号、改配置、删数据);执行时逐次弹门等待造物主批准。
5. 危害声明(harm_class):意图属数据销毁(data_destruction)或拒绝服务类(denial_of_service)时必须声明——此类动作无论审批门槛多宽都直接拒绝,仅当造物主在面板研判后放行才执行;普通意图留空。
6. 预算耗尽(exhausted)的步骤是暂停接力,不是失败:其单位撞预算前已把发现写回图(见"预算耗尽步骤"轨迹)。同一条工作线值得继续时,派"接着上次进度继续"的意图(接力单位在任务包里能看到前序尝试与已确认事实,从暂停点续跑);不值得继续或方向应换时,按态势另派。接续意图要写明从哪条轨迹续起。
7. 每条意图可带优先级(high|normal|low,缺省 normal)与前置步骤(depends_on,Step ID 清单;前置完成后才执行——只在后一步必须用到前一步产出时才声明,不设多余依赖)。
8. 被审批拒绝终止(approval_denied)的步骤:拒绝原因见"审批拒绝"节——被拒单位不得换法重试;研判后确有必要再走该方向时,须依据新的证据或方法另派意图,不得原样重派。
9. 组合声明(combined_claims):仅当两条及以上已确认事实组合出新的边界问题时提交——存在性与影响面分开声明,链条逐环节引用已确认事实 ID 并注明环节角色(如 入口/凭据/横移/目标),组合声明同样要经受独立反证,不得自评成立。
10. 意图数量硬底线:图上无未完成步骤(dispatched/running)且仍有未达成目标时,必须派发至少 1 条意图;输出 0 条意图仅在两种情形正当——①在等在跑单位产出(图上有未完成步骤)②全部目标已达成。其余情形的 0 意图是停摆,不许。目标未达成且图上无在跑/待执行工作时,本轮必须产出至少一个新方向(新模块/新角度/更深一层),禁止重复已确认事实与已完成步骤。
11. 路线多样性:探索期理想同时维持 2~3 条机理不同的路线(不同入口、不同技术路径)并存,防单点钻牛角尖;多样性永远服从 RoE 与优先级,不为多而多。
12. 复核上限:同一否定方向至多复核一次——复核后仍是否定,尊重结论,不再向该方向派发;后续新证据出现时走 TestClaim 重开与 Hypothesis 图上留档机制兜底,不靠重复派发。
13. 重派判定树:停下的步骤按停法分别处置——done:收割产出后按产出研判;exhausted:读"预算耗尽步骤"执行轨迹的真实进度(轮数、工具实调、产出物)决定接力或换向;blocked(受阻):按受阻原因换路绕行或换前提,仅当阻塞已解除才回原路。依据一律是轨迹真实进度,不是状态措辞。
14. 待裁决建议(纳谏):快照「待裁决建议」区每条都必须在 advices 里裁决——adopt=true 须在本轮意图中体现或在理由中写明何时体现;adopt=false 的理由必须基于图上事实(不空泛)。牧者的建议是研判输入,不是命令;裁决后规划随之改变,建议状态随之更新。`

// overmindOutputContract 主宰基线 C 段(代码固定,输出规约):JSON
// 契约是机器解析接口,配置不可移除。
const overmindOutputContract = `只输出 JSON:{"assessment":{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"},"new_intents":[{"intent":"一句话意图","expected_observation":"预期观察","action_level":"只读探测|主动测试|利用深入|修改与持久化","harm_class":"data_destruction|denial_of_service(仅危害类意图,普通意图省略)","covers":["资产ID"],"priority":"high|normal|low","depends_on":["StepID"]}],"combined_claims":[{"statement":"存在性声明(攻击链整体效果)","impact":"影响面声明","rationale":"组合理由","chain":[{"fact_id":"f-…","role":"入口"}],"asset_id":"资产ID","vulnclass":"漏洞类型","alternatives":["竞争性替代解释"]}],"advices":[{"id":"av-…","adopt":true,"reason":"裁决理由(不采纳须基于图上事实)"}]}
(advices 仅在快照存在待裁决建议时输出,无则省略该字段。)
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
		roe.AllowedTools, roe.DeniedTools, roe.ApprovalThreshold, excluded)}
	return append(segs, OvermindPromptSpec.Assemble(ctx, l.Store)...)
}

// nextSessionID 主宰会话的转录/用量锚点(P2-04 四类记录分工:会话原文
// JSONL + token 独立表):每次拆解/评估会话独立编号,文件互不混写。
func (l *Loop) nextSessionID(prefix string) string {
	l.seq++
	return "overmind-" + l.TaskID + "-" + prefix + strconv.Itoa(l.seq)
}

// decompose 一次 LLM 拆解:态势快照 → 研判结论(五问)+ 新意图列表
// + 组合声明草稿。无工具单轮会话,引擎链路(适配器/流式/用量)全真;
// 转录 JSONL 落盘(RootDir 已设时)、逐次用量入 token 独立表(P2-04
// 四类记录分工)。返回研判所读的账本版本(evaluatedRev)——研判记录
// 的锚点兼下一轮变化集的计算起点。
func (l *Loop) decompose(ctx context.Context) (*decomposeReply, int64, error) {
	snapshot, rev, err := l.snapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	sid := l.nextSessionID("d")
	opts := agentcore.Options{
		// 输出上限跟模型库配置走(P3-14a a):剥掉本地 MaxTokens——
		// failover 链的 capTokensProvider 在库配置了上限时压入配置值,
		// 未配置(0)则字段不发送走服务端默认。硬编码 4096 曾在库配置
		// 更宽时仍把长拆解在系统侧掐断(d3/d4 截断形态的根源之一)。
		Provider:               stripTokensProvider{l.Provider},
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
	usage := &swarm.UsageTracker{
		Store: l.Store, TaskID: l.TaskID, Agent: ActorOvermind,
		Model: l.Model, ModelOf: l.modelNow(), Transcript: sid, // MaxTurns=0:规划会话不落里程碑
	}
	var term *harness.Terminal
	for ev, err := range sess.Prompt(ctx, snapshot) {
		if err != nil {
			l.noteLLMFailure(ctx, err.Error())
			return nil, rev, fmt.Errorf("overmind: 拆解会话: %w", err)
		}
		if ev.Kind == harness.KindUsage && ev.Usage != nil {
			if uerr := usage.OnUsage(*ev.Usage); uerr != nil {
				l.setErr(uerr)
			}
		}
		if ev.Kind == harness.KindResult && ev.Terminal != nil {
			if ev.Terminal.Err != nil {
				l.noteLLMFailure(ctx, ev.Terminal.Err.Error())
				return nil, rev, fmt.Errorf("overmind: 拆解终止: %w", ev.Terminal.Err)
			}
			term = ev.Terminal
		}
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
	return reply, rev, nil
}

// jsonFenceRe 剥掉模型回复常见的 ```json 围栏。
var jsonFenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\s*(.*?)```")

// parseIntents 解析拆解回复:容忍围栏与前后杂文,取首尾大括号间的
// JSON 主体;空意图条目过滤(无陈述的意图不可执行)。P2-06 起回复
// 含研判结论(assessment)与组合声明(combined_claims),缺省兼容
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
	out := &decomposeReply{Assessment: r.Assessment, CombinedClaims: r.CombinedClaims, Advices: r.Advices}
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
		return nil, fmt.Errorf("overmind: 拆解无终态")
	}
	return parseIntents(term.Text)
}

// stripTokensProvider 拆解会话的输出上限策略(P3-14a a):剥掉请求里的
// 本地 MaxTokens(含引擎缺省值),输出上限交模型库配置决定——capTokens
// Provider 在库配置了上限时压入配置值,未配置(0)则字段不发送走服务端
// 默认。见 decompose 内注释。
type stripTokensProvider struct{ inner llm.Provider }

func (p stripTokensProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	req.MaxTokens = 0
	return p.inner.Complete(ctx, req)
}

func (p stripTokensProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	req.MaxTokens = 0
	return p.inner.Stream(ctx, req)
}

// snapshot 态势快照(拆解会话的用户提示):图版本 + 自上轮研判以来的
// 变化(五问的输入——五问全是"新 X 是否 Y"形式,没有变化集就没有
// 研判起点)+ 目标 + 资产覆盖 + 步骤(优先级/依赖等待)+ 已确认事实
// (带资产锚点)+ 候选假设 + 发现。主宰的判断只来自图(第四章:图是
// 唯一真相源),不持有私有记忆。返回快照文本与所读账本版本(研判
// 记录的锚点)。
func (l *Loop) snapshot(ctx context.Context) (string, int64, error) {
	rev, err := l.Store.CurrentRevision(ctx, l.TaskID)
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查图版本: %w", err)
	}
	goals, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeGoal, "")
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查目标: %w", err)
	}
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "")
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查步骤: %w", err)
	}
	facts, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeFact, "confirmed")
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查事实: %w", err)
	}
	hyps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeHypothesis, "")
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查假设: %w", err)
	}
	claims, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeTestClaim, "")
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查漏洞声明: %w", err)
	}
	ag, err := l.Store.AssetGraph(ctx, l.TaskID)
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查资产图: %w", err)
	}
	abouts, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeAbout)
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查资产锚点: %w", err)
	}
	depEdges, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDependsOn)
	if err != nil {
		return "", 0, fmt.Errorf("overmind: 查依赖边: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "【态势快照】\n图版本: %d\n", rev)

	// 自上轮研判以来的变化(P2-06 五问的输入):从最近一次研判记录的
	// EvaluatedRev 之后扫事件——新增资产/新确认事实(候选转正)/新声明
	// 与新假设/步骤状态迁移。首轮(无研判记录)如实标注。
	b.WriteString("\n自上轮研判以来的变化(五问审视对象):\n")
	if lines := l.deltaSinceLastAssessment(ctx); len(lines) > 0 {
		for _, line := range lines {
			fmt.Fprintf(&b, "  - %s\n", line)
		}
	} else {
		b.WriteString("  (无;首轮或上轮研判后账本无新变化)\n")
	}

	b.WriteString("\n目标:\n")
	if len(goals) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, g := range goals {
		fmt.Fprintf(&b, "  - %s [%s] %s\n", g.ID, g.Status, g.Title)
	}

	b.WriteString("\n资产(已覆盖=已有步骤 covers):\n")
	if len(ag.Assets) == 0 {
		b.WriteString("  (无)\n")
	}
	// 授权范围摘除清单(P4-05):摘除的资产标"范围外"——主宰不派覆盖
	// 它们的意图(派发入口守卫同判,双保险);覆盖缺口只看在范围内的。
	excludedSet := map[string]bool{}
	if roe, err := l.Store.CurrentRoE(ctx, l.TaskID); err != nil {
		b.WriteString("  (授权范围读取失败,按全部纳入处理)\n")
	} else {
		excludedSet = roe.ExcludedAssetSet()
	}
	for _, a := range ag.Assets {
		if excludedSet[a.ID] {
			fmt.Fprintf(&b, "  - %s [%s] %s (范围外,不派发)\n", a.ID, a.AssetClass, a.Title)
			continue
		}
		mark := "未覆盖"
		if a.Covered {
			mark = "已覆盖"
		}
		if a.Source == rings.AssetSourceManual {
			mark += ",手动补入"
		}
		fmt.Fprintf(&b, "  - %s [%s] %s (%s)\n", a.ID, a.AssetClass, a.Title, mark)
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

	// 审批拒绝(P2-06b,第四章 §3 v2):被审批拒绝终止熔断的单位与拒绝
	// 原因——主宰收割可见原因并重新研判(被拒单位不得换法重试;确有
	// 必要再走该方向须凭新证据/新方法另派)。含造物主研判后放行的记录。
	denials := l.denialBriefs(ctx)
	b.WriteString("\n审批拒绝(拒绝线自动拒绝/面板拒绝;被拒单位已熔断,不得换法重试):\n")
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
	hbMarks := l.heartbeatMarks(ctx)
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
	b.WriteString("\n已确认事实:\n")
	if len(facts) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, f := range facts {
		fmt.Fprintf(&b, "  - %s: %s", f.ID, f.Title)
		if assets := factAssets[f.ID]; len(assets) > 0 {
			fmt.Fprintf(&b, " (资产 %v)", assets)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n假设(虫群直觉回流):\n")
	if len(hyps) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, h := range hyps {
		fmt.Fprintf(&b, "  - %s [%s] %s\n", h.ID, h.Status, h.Title)
	}

	b.WriteString("\n发现(漏洞声明,可证伪):\n")
	if len(claims) == 0 {
		b.WriteString("  (无)\n")
	}
	for _, c := range claims {
		fmt.Fprintf(&b, "  - %s [%s] %s\n", c.ID, c.Status, c.Title)
	}

	// 待裁决建议(P2-07 纳谏):牧者代造物主提交的方向性建议,逐条
	// 裁决后状态改变(adopted|rejected 带理由);裁决规则见拆解规则 14。
	b.WriteString("\n待裁决建议(牧者代造物主提交;逐条在 advices 裁决):\n")
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

	b.WriteString("\n【拆解指令】\n先按五问研判态势(assessment 逐问一句结论),再依据以上态势输出下一步要派发的意图列表与组合声明,只输出 JSON;" +
		"若无新意图输出空列表。\n")
	return b.String(), rev, nil
}

// deltaSinceLastAssessment 自上轮研判以来的变化集(P2-06,五问的输入):
// 最近一条 overmind.record(assessment)的 EvaluatedRev 之后的事件——
// 新增资产、新事实入账(候选转正)、新声明/新假设、步骤状态迁移。
// 无研判记录时从头扫(首轮研判需要全量态势输入)。研判记录自身不计入
// (避免自我循环)。
func (l *Loop) deltaSinceLastAssessment(ctx context.Context) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil
	}
	var since int64 = -1 // -1 = 无研判记录(首轮):全部历史作为变化集
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
					lines = append(lines, fmt.Sprintf("新增目标 %s [%s] %s → 纳入本轮研判", p.NodeID, p.Status, p.Title))
				}
			case rings.NodeAsset:
				// 来源三态(P4-05):手动补入是造物主的动作指令——已默认
				// 纳入授权范围,研判路由按"评估覆盖缺口,优先派探测"预写。
				if rings.AssetSourceOf(p.Detail) == rings.AssetSourceManual {
					lines = append(lines, fmt.Sprintf("人手动补入资产 %s [%s] %s → 已默认纳入授权范围,评估覆盖缺口,优先派探测", p.NodeID, p.AssetClass, p.Title))
				} else {
					lines = append(lines, fmt.Sprintf("新资产 %s [%s] %s → 评估覆盖缺口,优先派探测", p.NodeID, p.AssetClass, p.Title))
				}
			case rings.NodeFact:
				lines = append(lines, fmt.Sprintf("新事实入账 %s [%s] %s → 按五问①③审视(可达性/组合)", p.NodeID, p.Status, p.Title))
			case rings.NodeTestClaim:
				lines = append(lines, fmt.Sprintf("新声明 %s [%s] %s → 跟进反证进展;确认后按五问②重估影响", p.NodeID, p.Status, p.Title))
			case rings.NodeHypothesis:
				lines = append(lines, fmt.Sprintf("新假设 %s [%s] %s → 收割评估时裁决去留", p.NodeID, p.Status, p.Title))
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
		}
	}
	return lines
}

// stepStopDirective 步骤停法的行动指令(§11 重派判定树的研判路由面:
// done / exhausted / blocked / 拒绝各走各的对策;依据是轨迹不是措辞)。
func stepStopDirective(status string) string {
	switch status {
	case "done":
		return " → 收割产出后按产出研判"
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

// chainLinksOf 拆解回复的链环节草稿转提交入口条目。
func chainLinksOf(plan []ChainLinkPlan) []ChainLink {
	out := make([]ChainLink, 0, len(plan))
	for _, p := range plan {
		out = append(out, ChainLink{FactID: p.FactID, Role: p.Role})
	}
	return out
}

// dispatch 派发一条拆解产物:绑定图上最新 active 目标,stepID 在任务内
// 顺序续号。covers 端点由账本校验,幽灵资产整批拒绝(错误上抛)。
// P2-06:优先级落 step.detail.priority(词汇受限,非法拒派);前置
// 落 depends_on 边(端点必须是图上 Step,环状依赖拒绝)。
func (l *Loop) dispatch(ctx context.Context, p IntentPlan) error {
	stepID, err := nextStepID(ctx, l.Store, l.TaskID)
	if err != nil {
		return err
	}
	if err := DispatchStep(ctx, l.Store, l.TaskID, stepID, strings.TrimSpace(p.Intent), DispatchOptions{
		GoalID:              l.currentGoalID(ctx),
		ExpectedObservation: p.ExpectedObservation,
		ActionLevel:         p.ActionLevel,
		HarmClass:           p.HarmClass,
		CoversAssets:        p.Covers,
		Priority:            p.Priority,
		DependsOn:           p.DependsOn,
	}); err != nil {
		return fmt.Errorf("overmind: 派发 %s(%s): %w", stepID, p.Intent, err)
	}
	return nil
}

// currentGoalID 图上最新目标(优先 active):拆解产物归到它名下,
// 前序尝试与任务包据此按目标分组。
func (l *Loop) currentGoalID(ctx context.Context) string {
	return CurrentGoalID(ctx, l.Store, l.TaskID)
}

// CurrentGoalID 图上最新目标(优先 active;无目标返回空)。牧者紧急
// 意图派发(UrgentDispatch)与主宰拆解共用——归属目标同源。
func CurrentGoalID(ctx context.Context, store *rings.Store, taskID string) string {
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
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "dispatched")
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 查待执行步骤: %w", err))
		return 0
	}
	if len(steps) == 0 {
		return 0
	}
	depEdges, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDependsOn)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 查依赖边: %w", err))
		return 0
	}
	all, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "")
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 查全部步骤(依赖闸): %w", err))
		return 0
	}
	runnable := selectRunnable(steps, all, depEdges)
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
// high > normal > low,同级按创建版本先后(先派先执行)。前置未完成
// 的步骤不是错误,是图上可见的等待态(主宰快照可见"前置 [sN]",
// 研判可换路重派)。dispatched 为待执行步骤集,all 为全图步骤
// (依赖闸需查前置的真实状态)。
func selectRunnable(dispatched, all []rings.NodeFull, depEdges []rings.EdgeView) []rings.NodeFull {
	depsOf := map[string][]string{}
	for _, e := range depEdges {
		depsOf[e.From] = append(depsOf[e.From], e.To)
	}
	statusOf := map[string]string{}
	for _, s := range all {
		statusOf[s.ID] = s.Status
	}
	var out []rings.NodeFull
	for _, s := range dispatched {
		ready := true
		for _, dep := range depsOf[s.ID] {
			if statusOf[dep] != "done" { // 前置不存在/未完成/失败/耗尽:一律等待
				ready = false
				break
			}
		}
		if ready {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := stepPriorityRank(out[i]), stepPriorityRank(out[j])
		if pi != pj {
			return pi > pj
		}
		return out[i].CreatedRev < out[j].CreatedRev
	})
	return out
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
// 静默让位;其余错误执行器已记账,此处如实暴露到 LastErr。单位落
// blocked(P2-06e 模型故障受阻)计任务级 LLM 失败;完成即记成功
// (清计数)。P4-04 返回是否真实落地(有单位会话跑完并落终态账;
// R14/领取竞态/熔断让位不算)——落地通知的判定依据。
func (l *Loop) runUnit(ctx context.Context, stepID string) bool {
	res, err := l.Swarm.RunStep(ctx, l.TaskID, stepID)
	if res != nil {
		switch res.StepStatus {
		case swarm.StepBlocked:
			l.noteLLMFailure(ctx, res.Reason)
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

// modelNow 拆解会话的按请求模型解析(failover 链注入;普通 Provider
// 返回 nil 退静态 Model)。
func (l *Loop) modelNow() func() string {
	type activeModeler interface{ ActiveModel() string }
	if am, ok := l.Provider.(activeModeler); ok {
		return am.ActiveModel
	}
	return nil
}

// denialBriefs 审批拒绝简报(P2-06b,第四章 §3 v2:主宰收割可见拒绝
// 原因):扫事件流里 denial 类 approval.record(排除等待期间任务收尾的
// closeout 结案——那是任务级中断不是审批裁决),逐步骤取最新一笔拒绝
// 及其后的研判后放行(forced)。确定性拼装,零 LLM。
func (l *Loop) denialBriefs(ctx context.Context) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 审批拒绝查事件: %w", err))
		return nil
	}
	type denialInfo struct {
		rec    rings.ApprovalRecord
		forced *rings.ApprovalRecord
	}
	latest := map[string]*denialInfo{} // requestID -> 最新拒绝与放行
	order := []string{}
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var rec rings.ApprovalRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		switch rec.Type {
		case rings.ApprovalRecDenied:
			if rec.DenyLine == rings.DenyLineCloseout {
				continue
			}
			if _, ok := latest[rec.RequestID]; !ok {
				order = append(order, rec.RequestID)
			}
			latest[rec.RequestID] = &denialInfo{rec: rec}
		case rings.ApprovalRecForced:
			if d, ok := latest[rec.RequestID]; ok {
				d.forced = &rec
			}
		}
	}
	var out []string
	for _, id := range order {
		d := latest[id]
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s | 工具 %s | 原因: %s", d.rec.StepID, clipEval(d.rec.Intent),
			d.rec.ToolName, clipEval(d.rec.Reason))
		if d.forced != nil {
			fmt.Fprintf(&b, " | 造物主已研判后放行(逐次执行,全记账)")
		}
		out = append(out, b.String())
	}
	return out
}
