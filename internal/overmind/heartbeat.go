package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// P2-05 心跳巡检(第八章第 8 节防线④):补事件驱动的盲区——纯事件驱动下
// "没有事件"不唤醒任何人(静默跑飞、卡死、死等无感知)。心跳即看门狗:
// 默认 600 秒一拍,每拍到点必醒(P3-14a B:距上轮研判结束满间隔即无条件
// 唤醒主宰——目标未完成不算任务结束,唤醒不依赖预筛发现;空闲心跳成本
// 造物主已确认接受)。预筛保留但职责收窄:对图上在跑意图做确定性检查
// (进度停滞/重复动作/预算消耗异常),发现只作黑匣子标记供主宰复查,
// P4-29：心跳照常唤醒机器处理，仅有新结果/新指令/图变化才规划。
// 巡检本身与每条发现都记入黑匣子(机器可扫的确定性
// 通道)。

const (
	// DefaultHeartbeatInterval 心跳巡检间隔(设计定稿 600 秒)。
	DefaultHeartbeatInterval = 600 * time.Second
	// DefaultStallSeconds 进度停滞判定窗口:在跑步骤超过此时长无任何
	// 工具调用事件即视为停滞。窗口远大于单工具硬超时(bash 60s / http
	// 15s / port 5s),长工具执行不会被误判。
	DefaultStallSeconds = 300
	// DefaultBurnAnomalyTokens 预算消耗异常阈值:单个虫群单位一个心跳
	// 窗口内 token 消耗(input+output)超过此值即视为异常。
	DefaultBurnAnomalyTokens = 400000
	// repeatWindow 重复动作判定:末尾连续相同调用(工具名+参数)达到
	// 此数即视为重复动作。
	repeatWindow = 3
)

// hbInterval 心跳间隔:装配显式值优先,零值时全局键覆盖(P2-08d
// Agent 栏可写),再退默认。每拍活读——改值下一拍生效。
func (l *Loop) hbInterval() time.Duration {
	if l.HeartbeatInterval > 0 {
		return l.HeartbeatInterval
	}
	if n, ok := settingPositiveInt(context.Background(), l.Store, SettingKeyHeartbeatSeconds); ok {
		return time.Duration(n) * time.Second
	}
	return DefaultHeartbeatInterval
}

// hbStallSeconds 停滞窗口秒数(零值取默认)。
func (l *Loop) hbStallSeconds() int {
	if l.StallSeconds > 0 {
		return l.StallSeconds
	}
	return DefaultStallSeconds
}

// hbBurnTokens 消耗异常阈值(零值取默认)。
func (l *Loop) hbBurnTokens() int {
	if l.BurnAnomalyTokens > 0 {
		return l.BurnAnomalyTokens
	}
	return DefaultBurnAnomalyTokens
}

// heartbeatLoop 心跳 goroutine 主体:到点即预筛(发现入黑匣子,供主宰
// 复查),并到点必醒踢醒主宰(hbKick 容量 1 合并,P3-14a B);收尾中/
// ctx 结束即停(任务在依序收场,巡检让位)。记账用独立 ctx:巡检留痕
// 不随任务熔断断链。
// P2-08:冬眠中跳过本拍(任务全停,冻结的单位不是停滞——不打标记、
// 不写黑匣子,唤醒后下一拍恢复巡检;心跳自身就是计时器,门上冻结)。
// P2-08d:间隔每拍重读全局键(Agent 栏改值下一拍生效)——定时器逐拍
// 重布,不吃冻结期的旧值。
func (l *Loop) heartbeatLoop(ctx context.Context) {
	for {
		interval := l.hbInterval()
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-l.runDone:
			t.Stop()
			return
		case <-t.C:
		}
		if l.isSettling() {
			return
		}
		if l.Hiber != nil && l.Hiber.Active(l.TaskID) {
			if !l.Hiber.Wait(ctx, l.TaskID) {
				return // 冬眠挂起至唤醒;ctx 结束即停
			}
			continue // 唤醒后从下一拍起恢复巡检
		}
		findings, err := l.prescreen(context.Background())
		if err != nil {
			l.setErr(err)
			continue // 本拍失败如实留 LastErr,下拍再查
		}
		runlog.Debugf("心跳巡检: 任务 %s 本拍发现 %d 项", l.TaskID, len(findings))
		if berr := blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind,
			rings.BlackboxRecord{
				Type: rings.BlackboxHeartbeat,
				Detail: map[string]any{
					"interval_seconds": int(interval.Seconds()),
					"findings":         len(findings),
					"note":             "心跳巡检拍(到点必醒,看门狗活性)",
				},
			}); berr != nil {
			l.setErr(berr)
		}
		for _, f := range findings {
			// 巡检标记落运行日志 WARN(P3-13b ③:心跳巡检标记)。
			runlog.Warnf("心跳巡检标记: 任务 %s %s %s %s", l.TaskID, f.Type, f.StepID, f.UnitID)
			if berr := blackbox.Record(context.Background(), l.Store, l.TaskID, ActorOvermind, f); berr != nil {
				l.setErr(berr)
			}
		}
		// 到点必醒机器循环；P4-29 由规划触发筛选避免无新事件的模型调用。
		// 零在跑步骤+
		// 声明待派的死等形态不再有(旧语义"有发现才唤醒"对它永不触发);
		// 拆解提示词硬底线保证被唤醒的主宰必须产出新方向。
		select {
		case l.hbKick <- struct{}{}:
		default:
		}
	}
}

// hbFinding 预筛发现(落黑匣子前的中间态)。
type hbFinding struct {
	rec rings.BlackboxRecord
}

// prescreen 一拍机器预筛(确定性,零 LLM):对图上每个在跑步骤查三类
// 问题——进度停滞(超时无工具调用事件)、重复动作(末尾连续相同调用)、
// 预算消耗异常(单位一拍窗口 token 超阈)。只标记,不处置:处置是主宰
// 被唤醒后的研判事(steer/kill 由主宰或操作者下达)。
func (l *Loop) prescreen(ctx context.Context) ([]rings.BlackboxRecord, error) {
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "running")
	if err != nil {
		return nil, fmt.Errorf("overmind: 心跳预筛查步骤: %w", err)
	}
	if len(steps) == 0 {
		return nil, nil
	}
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 心跳预筛查事件: %w", err)
	}
	tokens, err := l.Store.TokenUsageRows(ctx, l.TaskID)
	if err != nil {
		return nil, fmt.Errorf("overmind: 心跳预筛查用量: %w", err)
	}

	now := time.Now()
	stallAfter := time.Duration(l.hbStallSeconds()) * time.Second
	windowStart := now.Add(-l.hbInterval())

	// 按步骤归拢执行事件(工具调用/会话起止),按单位归拢窗口内 token。
	type stepTrace struct {
		startedAt     time.Time
		unitID        string
		toolCalls     []rings.SwarmRecord
		lastUpdatedAt time.Time
	}
	traces := map[string]*stepTrace{}
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		tr := traces[rec.StepID]
		if tr == nil {
			tr = &stepTrace{}
			traces[rec.StepID] = tr
		}
		switch rec.Type {
		case rings.SwarmRecSessionStarted:
			tr.startedAt = ev.OccurredAt
			tr.unitID = rec.UnitID
			if tr.lastUpdatedAt.IsZero() {
				tr.lastUpdatedAt = ev.OccurredAt
			}
		case rings.SwarmRecToolCall:
			tr.toolCalls = append(tr.toolCalls, rec)
			tr.lastUpdatedAt = ev.OccurredAt
		}
	}

	burnByUnit := map[string]int{}
	for _, t := range tokens {
		if !t.OccurredAt.After(windowStart) {
			continue
		}
		if strings.HasPrefix(t.Agent, "swarm:") {
			burnByUnit[strings.TrimPrefix(t.Agent, "swarm:")] += t.InputTokens + t.OutputTokens
		}
	}

	var findings []rings.BlackboxRecord
	for _, s := range steps {
		tr := traces[s.ID]
		if tr == nil || tr.unitID == "" {
			continue // 投影在跑但无执行记录:领取竞态,留待下一拍
		}
		// ① 进度停滞:上次账本活动(会话起或末次工具调用)距今超窗。
		if !tr.lastUpdatedAt.IsZero() && now.Sub(tr.lastUpdatedAt) > stallAfter {
			findings = append(findings, rings.BlackboxRecord{
				Type: rings.BlackboxHbStall, StepID: s.ID, UnitID: tr.unitID,
				Detail: map[string]any{
					"silent_seconds": int(now.Sub(tr.lastUpdatedAt).Seconds()),
					"tool_calls":     len(tr.toolCalls),
					"stall_window":   l.hbStallSeconds(),
					"note":           "进度停滞:超过判定窗口无任何工具调用记录",
				},
			})
		}
		// ② 重复动作:末尾连续相同调用(工具名+参数)达阈值。
		if n := trailingRepeat(tr.toolCalls); n >= repeatWindow {
			last := tr.toolCalls[len(tr.toolCalls)-1]
			findings = append(findings, rings.BlackboxRecord{
				Type: rings.BlackboxHbRepeat, StepID: s.ID, UnitID: tr.unitID,
				Detail: map[string]any{
					"repeat": n,
					"tool":   last.ToolName,
					"args":   clipArgs(string(last.ToolArgs), 120),
					"note":   "重复动作:末尾连续相同工具调用",
				},
			})
		}
		// ③ 预算消耗异常:该单位一拍窗口 token 消耗超阈。
		if burn := burnByUnit[tr.unitID]; burn > l.hbBurnTokens() {
			findings = append(findings, rings.BlackboxRecord{
				Type: rings.BlackboxHbBurnAnomaly, StepID: s.ID, UnitID: tr.unitID,
				Detail: map[string]any{
					"burned_tokens": burn,
					"threshold":     l.hbBurnTokens(),
					"note":          "预算消耗异常:单拍窗口 token 消耗超阈",
				},
			})
		}
	}
	return findings, nil
}

// trailingRepeat 末尾连续相同调用(工具名+参数原文)的长度。
func trailingRepeat(calls []rings.SwarmRecord) int {
	if len(calls) == 0 {
		return 0
	}
	last := calls[len(calls)-1]
	sig := last.ToolName + "\x00" + string(last.ToolArgs)
	n := 1
	for i := len(calls) - 2; i >= 0; i-- {
		if calls[i].ToolName+"\x00"+string(calls[i].ToolArgs) != sig {
			break
		}
		n++
	}
	return n
}

// heartbeatMarks 快照的"心跳标记"节素材:近两个心跳窗口内的预筛发现
// (确定性读黑匣子,主宰被唤醒后据此复查在跑意图;过旧标记自然滚出)。
func (l *Loop) heartbeatMarks(ctx context.Context) []string {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil
	}
	since := time.Now().Add(-2 * l.hbInterval())
	var out []string
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord || !ev.OccurredAt.After(since) {
			continue
		}
		var rec rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		var line string
		switch rec.Type {
		case rings.BlackboxHbStall:
			line = fmt.Sprintf("%s [%s] 进度停滞: %v 秒无工具调用记录",
				rec.StepID, markRunning, rec.Detail["silent_seconds"])
		case rings.BlackboxHbRepeat:
			line = fmt.Sprintf("%s [%s] 重复动作: 连续 %v 次相同调用 %v%v",
				rec.StepID, markRunning, rec.Detail["repeat"], rec.Detail["tool"], rec.Detail["args"])
		case rings.BlackboxHbBurnAnomaly:
			line = fmt.Sprintf("%s [%s] 预算消耗异常: 一拍消耗 %v tokens(阈 %v)",
				rec.StepID, markRunning, rec.Detail["burned_tokens"], rec.Detail["threshold"])
		default:
			continue
		}
		out = append(out, line)
	}
	return out
}

// markRunning 快照里标记节的固定状态词(在跑意图)。
const markRunning = "running"
