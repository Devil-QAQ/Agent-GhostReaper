package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
}

// killHook 按任务的止损钩子:cancel 该任务在跑单位(一 Step 一单位),
// 返回被停单位 ID 与是否命中。
type killHook func(taskID, stepID, expectedUnit string) (unitID string, ok bool)

// NewHelm 构建舵杆。
func NewHelm(store *rings.Store) *Helm {
	return &Helm{Store: store, pending: map[string][]rings.SteerRecord{}, kills: map[string]killHook{}}
}

func steerKey(taskID, stepID string) string { return taskID + "\x00" + stepID }

// splitSteerKey 拆出 (taskID, stepID);格式不符如实报 false。
func splitSteerKey(k string) (taskID, stepID string, ok bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == '\x00' {
			return k[:i], k[i+1:], true
		}
	}
	return "", "", false
}

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
// 单位在生效前自然落地时，未送达指令按所属单位落过时回执。
func (h *Helm) SteerWork(ctx context.Context, taskID, stepID, message, by string) (string, error) {
	owner, err := h.Store.CurrentExecutionOwner(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("步骤%s不在执行中或执行已更换: %w", stepID, err)
	}
	return h.SteerWorkForExecution(ctx, taskID, owner, message, by)
}

func (h *Helm) SteerWorkForExecution(ctx context.Context, taskID string, owner rings.ExecutionOwner, message, by string) (string, error) {
	stepID := owner.StepID
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("swarm: steer 指令必须携带纠偏消息")
	}
	step, err := h.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("swarm: 纠偏指向步骤 %s: %w", stepID, err)
	}
	if step.Type != rings.NodeStep {
		return "", fmt.Errorf("swarm: 节点 %s 是 %s 不是 step", stepID, step.Type)
	}
	if step.Status != "running" {
		return "", fmt.Errorf("swarm: 步骤 %s 当前状态 %s,不在执行中——纠偏只指向在跑的虫群单位", stepID, step.Status)
	}
	rec := rings.SteerRecord{
		SteerID: steerID(), StepID: stepID, Command: rings.SteerCommandSteer,
		State: rings.SteerStateQueued, Message: strings.TrimSpace(message), By: by,
	}
	rec.UnitID = owner.UnitID
	event := rings.Event{
		Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.Store.AppendForExecution(ctx, taskID, owner, event)
	if err != nil {
		return "", fmt.Errorf("swarm: 纠偏排队记账: %w", err)
	}
	h.pending[steerKey(taskID, stepID)] = append(h.pending[steerKey(taskID, stepID)], rec)
	return rec.SteerID, nil
}

// KillWork 将面板请求绑定到当前实际单位：排队入账后按该单位取消，
// 再记录送达或过时。未送达的纠偏只清理同一单位，不影响后继执行。
func (h *Helm) KillWork(ctx context.Context, taskID, stepID, reason, by string) (string, error) {
	owner, err := h.Store.CurrentExecutionOwner(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("步骤%s不在执行中或执行已更换: %w", stepID, err)
	}
	return h.KillWorkForExecution(ctx, taskID, owner, reason, by)
}

func (h *Helm) KillWorkForExecution(ctx context.Context, taskID string, owner rings.ExecutionOwner, reason, by string) (string, error) {
	stepID := owner.StepID
	step, err := h.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return "", fmt.Errorf("swarm: 纠偏指向步骤 %s: %w", stepID, err)
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
	rec := rings.SteerRecord{SteerID: steerID(), StepID: stepID, Command: rings.SteerCommandKill,
		State: rings.SteerStateQueued, Reason: strings.TrimSpace(reason), By: by}
	rec.UnitID = owner.UnitID
	if _, err := h.Store.AppendForExecution(ctx, taskID, owner, rings.Event{Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec}); err != nil {
		return "", err
	}
	unitID, ok := hook(taskID, stepID, owner.UnitID)
	if !ok {
		rec.State, rec.Note = rings.SteerStateStale, "原执行已结束或更换，本次止损未执行"
		if _, err := h.Store.Append(steerCtx, taskID, rings.Event{Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec}); err != nil {
			return rec.SteerID, err
		}
		return rec.SteerID, fmt.Errorf("swarm: 步骤 %s 无在跑单位(领取竞态:单位刚干完)", stepID)
	}
	rec.UnitID, rec.State, rec.Note = unitID, rings.SteerStateDelivered, "止损终止:单位 ctx 已 cancel,引擎真中断(不进收尾),步骤落 aborted"
	if _, err := h.Store.Append(steerCtx, taskID, rings.Event{
		Kind: rings.EventSteerRecord, Actor: actorOf(by), Payload: rec,
	}); err != nil {
		return rec.SteerID, fmt.Errorf("swarm: 单位ctx已取消，但止损送达记账失败: %w", err)
	}
	h.expirePending(taskID, stepID, owner.UnitID, "止损终止:单位已停,指令失去生效点(已过时)")
	return rec.SteerID, nil
}

// RecordVerdict 落一条回执(P2-08 回执状态机:已采纳/被拒):单位消化
// 纠偏指令后经助手文本中的回执标记触发。同一条指令至多一条回执
// (按账本最新态去重，重建与并发提交不重复迁移)。
func (h *Helm) RecordVerdict(taskID, stepID, unitID string, r steerReceipt) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := rings.SteerStateRejected
	note := "单位明示不采纳"
	if r.Adopted {
		state = rings.SteerStateAdopted
		note = "单位明示采纳,已按指令调整方向"
	}
	if r.Reason != "" {
		note += ":" + r.Reason
	}
	for attempt := 0; attempt < 8; attempt++ {
		rev, err := h.Store.CurrentRevision(steerCtx, taskID)
		if err != nil {
			return err
		}
		previous, err := h.Store.LatestSteerRecord(steerCtx, taskID, r.SteerID)
		if err != nil {
			return fmt.Errorf("纠偏回执无对应指令: %w", err)
		}
		if previous.StepID != stepID || previous.UnitID != unitID || previous.Command != rings.SteerCommandSteer {
			return fmt.Errorf("纠偏回执不属于该执行收到的指令")
		}
		if previous.State == rings.SteerStateAdopted || previous.State == rings.SteerStateRejected {
			return nil
		}
		if previous.State != rings.SteerStateDelivered {
			return fmt.Errorf("纠偏指令未送达或已过时，不接受采纳回执")
		}
		_, err = h.Store.AppendAtRevision(steerCtx, taskID, rev, rings.Event{Kind: rings.EventSteerRecord, Actor: "swarm:" + unitID, Payload: rings.SteerRecord{
			SteerID: r.SteerID, StepID: stepID, UnitID: unitID, Command: rings.SteerCommandSteer, State: state, Note: note,
		}})
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		return err
	}
	return fmt.Errorf("纠偏回执落账时状态持续变化，本次未确认")
}

// Bound instructions never move to a replacement execution of the same step.
func (h *Helm) takeForUnit(taskID, stepID, unitID string) ([]rings.SteerRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := steerKey(taskID, stepID)
	if len(h.pending[key]) == 0 {
		return nil, nil
	}
	owner, err := h.Store.CurrentExecutionOwner(steerCtx, taskID, stepID)
	if err != nil {
		return nil, err
	}
	if owner.UnitID != unitID {
		return nil, rings.ErrExecutionEnded
	}
	var active []rings.SteerRecord
	for _, d := range h.pending[key] {
		if d.UnitID == unitID {
			active = append(active, d)
			continue
		}
		d.State, d.Note = rings.SteerStateStale, "原执行已结束或更换，纠偏不转交后继单位"
		if _, err := h.Store.Append(steerCtx, taskID, rings.Event{Kind: rings.EventSteerRecord, Actor: "swarm", Payload: d}); err != nil {
			return nil, err
		}
	}
	delete(h.pending, key)
	return active, nil
}

func (h *Helm) returnPending(taskID, stepID string, dirs []rings.SteerRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := steerKey(taskID, stepID)
	h.pending[key] = append(dirs, h.pending[key]...)
}

// actorOf 下达者 → 账本 actor(creator 面板 / overmind 心跳纠偏)。
func actorOf(by string) string {
	if by == "" {
		return "creator"
	}
	return by
}
