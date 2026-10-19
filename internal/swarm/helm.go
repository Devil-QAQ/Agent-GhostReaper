package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"ghostreaper/internal/rings"
)

// Helm 舵杆(P2-05,第八章第 8 节纠偏纵深):steer_work / kill_work 的
// 权威登记表。面板与主宰都经它下达指令(持有人:造物主和主宰都可持),
// 指令先入账(queued)再生效——生效点在虫群单位下一次工具调用边界
// (工具门卫 guard.go 消费),全程账本留痕。与审批门(LedgerGate)同一
// 装配形态:server 与执行器共持同一实例,不另立旁路。
//
// steer_work:消息排队,单位下一次工具调用之前注入其上下文——不打断、
// 不丢已有进展,单位收到后自行重新规划。
// kill_work:止损终止,走独立停止机制(cancel 单位 ctx,引擎真中断,
// 不进收尾);步骤落 aborted(中性可重派),审计闭环。止损钩子按任务
// 注册:执行器起飞该任务的首个单位时自登记(kill 路由到持单位的
// 执行器;一任务一执行器),未注册即无在跑单位可停。
type Helm struct {
	Store *rings.Store

	mu      sync.Mutex
	pending map[string][]rings.SteerRecord // key: taskID+"\x00"+stepID,已排队未送达
	kills   map[string]killHook            // taskID → 止损钩子(执行器自登记)
	// verdicts 已落回执的指令(adopted/rejected 去重,P2-08):同一指令
	// 至多迁移一次,重复标记静默忽略。进程内状态,重启后凭账本最新态
	// 重建视图(重复回执在重启后至多多一条,读面取最新态不受影响)。
	verdicts map[string]bool
}

// killHook 按任务的止损钩子:cancel 该任务在跑单位(一 Step 一单位),
// 返回被停单位 ID 与是否命中。
type killHook func(taskID, stepID string) (unitID string, ok bool)

// NewHelm 构建舵杆。
func NewHelm(store *rings.Store) *Helm {
	return &Helm{Store: store, pending: map[string][]rings.SteerRecord{}, kills: map[string]killHook{}}
}

func steerKey(taskID, stepID string) string { return taskID + "\x00" + stepID }

// steerID 生成指令稳定标识(st-*)。
func steerID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("st-%d", time.Now().UnixNano())
	}
	return "st-" + hex.EncodeToString(b[:])
}

// steerCtx 舵杆记账专用 ctx:指令留痕不随任务熔断断链(同 ledgerCtx 约定)。
var steerCtx = context.Background()

// attachTask 执行器自登记:该任务的单位起飞后,kill_work 路由到它。
// 已登记同一执行器则幂等;换执行器(重启/演示重建)覆盖登记。
func (h *Helm) attachTask(taskID string, hook killHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kills[taskID] = hook
}

// SteerWork 下达 steer_work:校验步骤在跑 → 排队 + 账本(queued)。
// 生效点在单位下一次工具调用边界(guard 消费并落 delivered 记录);
// 单位在生效前自然落地时指令停在已排队(过时语义属 P2-08 回执机制)。
func (h *Helm) SteerWork(ctx context.Context, taskID, stepID, message, by string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("swarm: steer 指令必须携带纠偏消息")
	}
	step, err := h.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("swarm: 舵杆指向步骤 %s: %w", stepID, err)
	}
	if step.Type != rings.NodeStep {
		return "", fmt.Errorf("swarm: 节点 %s 是 %s 不是 step", stepID, step.Type)
	}
	if step.Status != "running" {
		return "", fmt.Errorf("swarm: 步骤 %s 当前状态 %s,不在执行中——舵杆只指向在跑的虫群单位", stepID, step.Status)
	}
	rec := rings.SteerRecord{
		SteerID: steerID(), StepID: stepID, Command: rings.SteerCommandSteer,
		State: rings.SteerStateQueued, Message: strings.TrimSpace(message), By: by,
	}
	if _, err := h.Store.Append(steerCtx, taskID, rings.Event{
		Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec,
	}); err != nil {
		return "", fmt.Errorf("swarm: 舵杆排队记账: %w", err)
	}
	h.mu.Lock()
	h.pending[steerKey(taskID, stepID)] = append(h.pending[steerKey(taskID, stepID)], rec)
	h.mu.Unlock()
	return rec.SteerID, nil
}

// KillWork 下达 kill_work:校验钩子与在跑单位 → cancel 单位 ctx(引擎
// 真中断)→ 送达记账。同步骤未送达的 steer 指令一并落 stale 回执
// (工作已止损,后续无生效点;P2-08 回执状态机闭环)。找不到在跑单位
// 时如实报错,账本零残留。
func (h *Helm) KillWork(ctx context.Context, taskID, stepID, reason, by string) (string, error) {
	step, err := h.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("swarm: 舵杆指向步骤 %s: %w", stepID, err)
	}
	if step.Type != rings.NodeStep {
		return "", fmt.Errorf("swarm: 节点 %s 是 %s 不是 step", stepID, step.Type)
	}
	if step.Status != "running" {
		return "", fmt.Errorf("swarm: 步骤 %s 当前状态 %s,不在执行中——无在跑单位可止损", stepID, step.Status)
	}
	h.mu.Lock()
	hook := h.kills[taskID]
	h.mu.Unlock()
	if hook == nil {
		return "", fmt.Errorf("swarm: 任务 %s 未接执行器,无在跑单位可停", taskID)
	}
	unitID, ok := hook(taskID, stepID)
	if !ok {
		return "", fmt.Errorf("swarm: 步骤 %s 无在跑单位(领取竞态:单位刚落地)", stepID)
	}
	rec := rings.SteerRecord{
		SteerID: steerID(), StepID: stepID, UnitID: unitID, Command: rings.SteerCommandKill,
		State: rings.SteerStateDelivered, Reason: strings.TrimSpace(reason), By: by,
		Note: "止损终止:单位 ctx 已 cancel,引擎真中断(不进收尾),步骤落 aborted",
	}
	if _, err := h.Store.Append(steerCtx, taskID, rings.Event{
		Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec,
	}); err != nil {
		return "", fmt.Errorf("swarm: 舵杆止损记账: %w", err)
	}
	h.expirePending(taskID, stepID, "止损终止:单位已停,指令失去生效点(已过时)")
	return rec.SteerID, nil
}

// RecordVerdict 落一条回执(P2-08 回执状态机:已采纳/被拒):单位消化
// 纠偏指令后经助手文本中的回执标记触发。同一条指令至多一条回执
// (进程内去重;重复标记静默忽略——回执是状态迁移不是流水)。
func (h *Helm) RecordVerdict(taskID, stepID, unitID string, r steerReceipt) {
	h.mu.Lock()
	if h.verdicts == nil {
		h.verdicts = map[string]bool{}
	}
	if h.verdicts[r.SteerID] {
		h.mu.Unlock()
		return
	}
	h.verdicts[r.SteerID] = true
	h.mu.Unlock()
	state := rings.SteerStateRejected
	note := "单位明示不采纳"
	if r.Adopted {
		state = rings.SteerStateAdopted
		note = "单位明示采纳,已按指令调整方向"
	}
	if r.Reason != "" {
		note += ":" + r.Reason
	}
	_, _ = h.Store.Append(steerCtx, taskID, rings.Event{
		Kind: rings.EventSteerRecord, Actor: "swarm:" + unitID,
		Payload: rings.SteerRecord{
			SteerID: r.SteerID, StepID: stepID, UnitID: unitID,
			Command: rings.SteerCommandSteer, State: state, Note: note,
		},
	})
}

// take 取走并清空该步骤已排队的 steer 指令(工具门卫在调用边界消费;
// 同一时刻只有一个边界能取到——指令一次性送达,不重复注入)。
func (h *Helm) take(taskID, stepID string) []rings.SteerRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	dirs := h.pending[steerKey(taskID, stepID)]
	delete(h.pending, steerKey(taskID, stepID))
	return dirs
}

// actorOf 下达者 → 账本 actor(creator 面板 / overmind 心跳纠偏)。
func actorOf(by string) string {
	if by == "" {
		return "creator"
	}
	return by
}
