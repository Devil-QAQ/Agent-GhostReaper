package overmind

// P4-18 收口信号(问题清单 #30):任务连续 N 轮研判零新发现(新发现 =
// 事实/资产/声明节点新增,机器可查;任一新增即清零重计)且距上次新发现
// ≥ 间隔(默认 N=5 轮 + 30 分钟,双条件同时满足,面板可配)→ 落一条
// closure.record(prompt)——面板据此弹窗提醒,任务不打断(照常跑,不做
// 任何暂停/冻结);造物主两键裁决:结束 = 优雅收尾(收尾时序 + 终版
// 报告),继续 = 不停、同规则重新计数(防反复打断)。P4-23:弹窗被
// 直接关闭(未点继续/结束)落 dismiss——待决保留并标搁置,任务列表
// 红点是它的持久落点,处理(继续/结束)才消。
//
// 计数与待决态全部从账本确定性推导(零 LLM,重启不丢):锚点 = 上次
// 新发现或上次「继续」(无则建任务时刻),锚点后的 assessment 记录数即
// 连续零新发现轮数;最后一条 prompt 之后出现新发现或裁决即视为已解决。
// 评估挂在每轮研判记录落账之后(轮数只在研判轮推进时增长;心跳巡检
// 保证空闲任务持续产生研判轮,时间条件不会因静默而漏判)。

import (
	"context"
	"encoding/json"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 收口信号默认阈值(审核迭代 1 造物主拍板:全局默认 20 轮 + 50 分钟;
// 生效值三层取法——任务级配置 > Agent 栏全局键 > 此代码默认)。
const (
	DefaultClosureRounds  = 20
	DefaultClosureMinutes = 50
)

// SettingKeyClosureRounds / SettingKeyClosureMinutes 收口信号全局默认键
// (Agent 栏可写;定位 = 全局默认,任务未单独配置时用此值)。生效节奏 =
// 评估点活读:下一次研判轮评估即用新值,已触发的提醒不回收。
const (
	SettingKeyClosureRounds  = "overmind.closure.rounds"
	SettingKeyClosureMinutes = "overmind.closure.minutes"
)

// ClosurePending 一条待决的收口提醒:提醒记录与触发时刻(面板弹窗的
// 数据面)。P4-23:弹窗被直接关闭(未点继续/结束)落 dismiss 后待决
// 保留、Parked=true——弹窗不再自动弹,任务列表标红点,处理才消
// (继续/结束或新出现新发现即消)。
type ClosurePending struct {
	Record     rings.ClosureRecord
	PromptedAt time.Time
	Parked     bool // 弹窗被直接关闭后的搁置态(红点落点;新 prompt 重置)
}

// ClosureTaskConfig 收口信号的任务级配置(审核迭代 1):建任务表单的
// 初始配置随 created detail.closure 落库,运行中变更落 closure.record
// (config)——最新一条即当前快照(整体覆盖)。Rounds/Minutes 为 0 =
// 该项未单独设定(用全局默认);无任何任务级配置时 HasConfig=false
// (开关默认开)。
type ClosureTaskConfig struct {
	HasConfig bool
	Enabled   bool
	Rounds    int
	Minutes   int
}

// ClosureState 收口信号的账本推导态(overmind 评估与面板投影共一读法,
// 展示与操作同源)。
type ClosureState struct {
	// Rounds 锚点后的研判轮数(连续零新发现轮数)。
	Rounds int
	// AnchorAt 计数锚点时刻(上次新发现 / 上次「继续」/ 建任务)。
	AnchorAt time.Time
	// AnchorKind 锚点种类(rings.ClosureAnchor*;面板措辞依据)。
	AnchorKind string
	// Pending 待决提醒(最后一条 prompt 未被裁决或新发现解决;nil = 无)。
	Pending *ClosurePending
	// TaskConfig 任务级配置快照(最新 config 事件或 created 初始)。
	TaskConfig ClosureTaskConfig
}

// ClosureEffective 生效配置(三层:任务级 > Agent 栏全局键 > 代码默认,
// 审核迭代 1 造物主拍板)。开关只看任务级(无任务级配置 = 开)。
func ClosureEffective(ctx context.Context, store *rings.Store, tc ClosureTaskConfig) (enabled bool, rounds, minutes int) {
	enabled = true
	if tc.HasConfig {
		enabled = tc.Enabled
	}
	rounds, minutes = DefaultClosureRounds, DefaultClosureMinutes
	if n, ok := settingPositiveInt(ctx, store, SettingKeyClosureRounds); ok {
		rounds = n
	}
	if n, ok := settingPositiveInt(ctx, store, SettingKeyClosureMinutes); ok {
		minutes = n
	}
	if tc.Rounds > 0 {
		rounds = tc.Rounds
	}
	if tc.Minutes > 0 {
		minutes = tc.Minutes
	}
	return enabled, rounds, minutes
}

// ClosureOf 从账本确定性推导任务的收口信号态(单遍扫描,零 LLM)。
// 锚点取"上次新发现或上次「继续」"中较后者,从未有过则取建任务时刻;
// 锚点后的 overmind.record(assessment) 计为研判轮;新发现 = 事实 /
// 声明的 node.added 与资产新增中的手动/发现来源(种子资产随建任务
// 表单入账在首轮研判之前,不算执行中的新发现)。任一新增即清零重计。
func ClosureOf(ctx context.Context, store *rings.Store, taskID string) (ClosureState, error) {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return ClosureState{}, err
	}
	st := ClosureState{}
	var prompt *ClosurePending
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventTaskLifecycle:
			var p rings.TaskLifecycle
			if json.Unmarshal(ev.Payload, &p) != nil || p.Action != "created" {
				continue
			}
			if st.AnchorAt.IsZero() {
				st.AnchorAt, st.AnchorKind = at(ev), rings.ClosureAnchorCreated
			}
			// 建任务表单的初始配置(审核迭代 1):只存显式项,有键即视为
			// 已配置。
			if raw, ok := p.Detail["closure"].(map[string]any); ok {
				st.TaskConfig = parseClosureDetail(raw)
			}
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.NodeType {
			case rings.NodeFact, rings.NodeTestClaim:
				st.AnchorAt, st.Rounds, st.AnchorKind = at(ev), 0, rings.ClosureAnchorFinding
				prompt = nil // 新发现即清零重计,待决提醒随之解决
			case rings.NodeAsset:
				// 手动补入(P4-05)与虫群发现照算;种子来源跳过(建任务
				// 表单随单入账,锚点保持建任务时刻)。
				if rings.AssetSourceOf(p.Detail) == rings.AssetSourceSeed {
					continue
				}
				st.AnchorAt, st.Rounds, st.AnchorKind = at(ev), 0, rings.ClosureAnchorFinding
				prompt = nil
			}
		case rings.EventOvermindRecord:
			var p rings.OvermindRecord
			if json.Unmarshal(ev.Payload, &p) != nil || p.Type != rings.OvermindRecAssessment {
				continue
			}
			st.Rounds++
		case rings.EventClosureRecord:
			var p rings.ClosureRecord
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Type {
			case rings.ClosureRecPrompt:
				prompt = &ClosurePending{Record: p, PromptedAt: at(ev)}
			case rings.ClosureRecDismiss:
				// 弹窗被直接关闭(P4-23):待决保留、标搁置;无待决时
				// 忽略(历史 dismiss 不造出幻影提醒)。
				if prompt != nil {
					prompt.Parked = true
				}
			case rings.ClosureRecContinue:
				st.AnchorAt, st.Rounds, st.AnchorKind = at(ev), 0, rings.ClosureAnchorContinue
				prompt = nil
			case rings.ClosureRecEnd:
				prompt = nil // 结束走收尾时序,任务终态由收尾流程落
			case rings.ClosureRecConfig:
				// 任务级配置变更(审核迭代 1):快照式整体覆盖,最新即当前。
				st.TaskConfig = ClosureTaskConfig{
					HasConfig: true, Enabled: p.Enabled != nil && *p.Enabled,
					Rounds: p.Rounds, Minutes: p.Minutes,
				}
			}
		}
	}
	st.Pending = prompt
	return st, nil
}

// at 事件业务时刻(账本读出的 OccurredAt 已解析归一)。
func at(ev rings.RawEvent) time.Time { return ev.OccurredAt }

// parseClosureDetail created detail.closure 的宽容解析(只认显式项)。
func parseClosureDetail(raw map[string]any) ClosureTaskConfig {
	tc := ClosureTaskConfig{HasConfig: true, Enabled: true}
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

// evalClosure 一轮研判记录落账后的收口评估:双条件同时满足且无待决
// 提醒时落一条 closure.record(prompt)。只写账不动任务——"弹窗提醒,
// 任务不打断"由本函数的实现保证(零控制面触点)。失败如实留 LastErr
// 不重试(下一轮评估重算,账本派生态不受影响)。
func (l *Loop) evalClosure(ctx context.Context) {
	l.mu.Lock()
	skip := l.settling || l.finished
	l.mu.Unlock()
	if skip {
		return
	}
	st, err := ClosureOf(ctx, l.Store, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	if st.Pending != nil || st.AnchorAt.IsZero() {
		return
	}
	// 三层生效配置(审核迭代 1):任务级 > Agent 栏全局键 > 代码默认;
	// 开关关闭即整任务不弹。
	enabled, roundsN, minutesN := ClosureEffective(ctx, l.Store, st.TaskConfig)
	if !enabled {
		return
	}
	if st.Rounds < roundsN || time.Since(st.AnchorAt) < time.Duration(minutesN)*time.Minute {
		return
	}
	rec := rings.ClosureRecord{
		Type:             rings.ClosureRecPrompt,
		Rounds:           st.Rounds,
		Minutes:          int(time.Since(st.AnchorAt).Minutes()),
		RoundsThreshold:  roundsN,
		MinutesThreshold: minutesN,
		AnchorAt:         st.AnchorAt.UTC().Format(time.RFC3339),
		AnchorKind:       st.AnchorKind,
	}
	if _, err := l.Store.Append(ctx, l.TaskID, rings.Event{
		Kind: rings.EventClosureRecord, Actor: ActorOvermind, Payload: rec,
	}); err != nil {
		l.setErr(err)
		return
	}
	runlog.Infof("收口提醒: %s(连续 %d 轮规划零新发现,距锚点 %d 分钟)", l.TaskID, rec.Rounds, rec.Minutes)
}
