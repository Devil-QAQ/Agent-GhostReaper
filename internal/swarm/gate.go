package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ApprovalRequest 一次弹门请求:面板据此展示"意图声明 + 实际工具调用
// 参数"供造物主核对(v1 人即兜底,第四章 §3)。Note 携带弹门上下文
// (P4-03 ②:拦截规则命中转人审时说明原因,命令原文在 ToolArgs)。
type ApprovalRequest struct {
	ID          string
	TaskID      string
	StepID      string
	UnitID      string
	Intent      string // 意图声明(Step 标题)
	ActionLevel rings.ActionLevel
	HarmClass   rings.HarmClass
	ToolName    string
	ToolArgs    string // 原始 JSON 参数
	Note        string // 弹门上下文(空 = 常规门槛弹门,无附加说明)
	// GateGroup 弹门来源的拦截规则组(硬规则弹门处置时携带;空 = 审批
	// 门槛弹门)。P4-17:代理旁路组的批准据此武装该单位的组级放行。
	GateGroup string
	// AttackTarget 攻击行为卡的目标(P4-67,非空 = 攻击行为卡):分诊判定
	// "信息源上的攻击行为"——host 是信息源(查资料用的公开服务)但这次
	// 动作是主动攻击。批准 = 放行该次动作(host 仍非目标,不武装步骤级
	// 放行);拒绝 = 只杀该动作不杀单位(drive 侧注入换路反馈)。值为主
	// 机显示形态 host[:port],记账 Type=attack_wait。
	AttackTarget string
	CreatedAt    time.Time
}

// GateDecision 面板(或收尾路径)对弹门请求的裁决。
type GateDecision struct {
	Approved bool
	By       string // 裁决者标识(面板=creator)
	Reason   string
	// Released 冬眠收场放行(P4-33,非裁决):任务冬眠冻结收场时,票上
	// 等待的单位经此令收场落地——票面保持待裁决冻结(不产生任何裁决
	// 记录),唤醒后重领的单位重新过门时重新立票。Decide/Expire 永不
	// 置位;单位侧见令先于 Approved 处理。
	Released bool
}

// GateTicket 一笔在途弹门请求与其裁决送达通道(P2-06b 挂起/续跑形态)。
// Enter 只登记不阻塞——单位把当前执行段收拢后停在票上等裁决(等待期
// 无活动引擎时钟,run_seconds 不消耗,第四章 §3 v2"审批等待期单位
// 计时暂停");Decide 先记账后送达。ch 容量 1:裁决可在单位收段期间
// 先行抵达,不丢失。
type GateTicket struct {
	Req         ApprovalRequest
	ch          chan GateDecision
	requestedAt time.Time
}

// Wait 等待裁决(单位收段后调用;阻塞点即计时暂停点)。
func (t *GateTicket) Wait(ctx context.Context) (GateDecision, error) {
	select {
	case d := <-t.ch:
		return d, nil
	case <-ctx.Done():
		return GateDecision{}, ctx.Err()
	}
}

// ApprovalGate 审批弹门(P1-07 建立,P2-06b 三级处置 v2 重构):达到
// 审批门槛的意图分级下的工具调用在此挂起,等待面板批准/拒绝。R9 的
// 结构保障:裁决的唯一来源是 Decide(面板通道,先记账后送达)与 ctx
// 结束(熔断/收尾,Expire 结案);会话内容(模型或用户文本)不构成
// 任何一条决策通路。
type ApprovalGate interface {
	// Enter 登记审批请求(账本 approval.record requested)并返回等待票,
	// 不阻塞——调用方(审批回调)收拢当前执行段后停在票上等裁决。
	Enter(req ApprovalRequest) (*GateTicket, error)
	// Decide 面板裁决:先记账(approved/denied,含等待时长)再送达等待方。
	// 请求不存在或已裁决时返回错误(不重复记账)。
	Decide(ctx context.Context, taskID, requestID string, d GateDecision) error
	// Expire 等待期间任务收尾/熔断的结案:补记 denied(closeout)并注销
	// 待决请求(裁决若已先行送达则如实报告不存在)。
	Expire(taskID, requestID, reason string) error
	// Pending 当前待裁决请求快照(面板轮询)。
	Pending(taskID string) []ApprovalRequest
}

// LedgerGate ApprovalGate 的账本实现:每任务一套挂进 Executor,审批
// 流全程留痕(请求、裁决、等待时长、拒绝原因都是 approval.record 事件)。
type LedgerGate struct {
	store *rings.Store
	// Hiber 任务冬眠门(P2-08,第七章「任务冬眠」:待裁决审批随冬眠
	// 冻结——审批单只读展示、不可裁决;唤醒后恢复可裁决,批准的单位
	// 继续执行(继续前按 R14 复检,由单位侧挂起路径处理))。装配层
	// 注入;nil = 无冬眠语义(单测)。
	Hiber *hiber.Gate

	mu      sync.Mutex
	pending map[string]*GateTicket // requestID -> 等待中的请求
}

// NewLedgerGate 构建账本审批门。
func NewLedgerGate(store *rings.Store) *LedgerGate {
	return &LedgerGate{store: store, pending: map[string]*GateTicket{}}
}

// ErrGateRequestNotFound 裁决目标不存在(未知 ID 或已裁决)。
var ErrGateRequestNotFound = errors.New("swarm: 审批请求不存在或已处理")

// ErrGateFrozen 任务冬眠中审批单冻结(P2-08:只读展示,唤醒恢复可裁决)。
var ErrGateFrozen = errors.New("swarm: 任务冬眠中,审批单已冻结为只读(唤醒后恢复可审批)")

// ledgerCtx 账本入账专用 ctx(与 executor 同款):收尾/熔断路径的审批
// 记录不可因任务 ctx 取消而断账。
var gateLedgerCtx = context.Background()

// gateRequestID 生成请求短 ID(ap-<rand>)。
func gateRequestID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ap-%d", time.Now().UnixNano())
	}
	return "ap-" + hex.EncodeToString(b[:])
}

func (g *LedgerGate) record(taskID string, actor string, rec rings.ApprovalRecord) error {
	_, err := g.store.Append(gateLedgerCtx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: actor, Payload: rec,
	})
	if err != nil {
		return fmt.Errorf("swarm: 审批记录写入: %w", err)
	}
	return nil
}

// Enter 弹门登记:挂进待决表 → 记账 requested → 返回等待票。
// 挂表先于记账:P3-08 实时推送在记账提交回调里读待决表组装载荷,
// 先挂后记使载荷天然含本笔(记账失败回滚摘除,不留未记账的幻影待决)。
// P4-67:攻击行为卡(AttackTarget 非空)记账 Type=attack_wait(卡面与
// 统计按类型区分),票 ID 仍随机(每动作一票,批准只放行该次)。
func (g *LedgerGate) Enter(req ApprovalRequest) (*GateTicket, error) {
	req.ID = gateRequestID()
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	t := &GateTicket{Req: req, ch: make(chan GateDecision, 1), requestedAt: req.CreatedAt}
	g.mu.Lock()
	g.pending[req.ID] = t
	g.mu.Unlock()
	typ := rings.ApprovalRecRequested
	if req.AttackTarget != "" {
		typ = rings.ApprovalRecAttackWait
	}
	if err := g.record(req.TaskID, "swarm:"+req.UnitID, rings.ApprovalRecord{
		RequestID: req.ID, StepID: req.StepID, UnitID: req.UnitID,
		Type: typ, ActionLevel: req.ActionLevel,
		HarmClass: req.HarmClass, Intent: req.Intent,
		ToolName: req.ToolName, ToolArgs: []byte(req.ToolArgs), Note: req.Note,
	}); err != nil {
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		return nil, err
	}
	return t, nil
}

// Decide 面板裁决:记账在先、送达在后(账本先于一切副作用,R7 惯例)。
// 等待时长随裁决落账——弹门等待期的时间账可对账(第四章 §3 v2)。
// P2-08:任务冬眠中审批单冻结为只读——裁决被拒(唤醒后恢复可裁决)。
// P4-33:冬眠收场时票上等待的单位经 RetireTickets 放行落地,票面保持
// 待裁决冻结留待唤醒清出(无主票不可再裁);唤醒后重领的单位重新
// 过门时重新立票。
func (g *LedgerGate) Decide(_ context.Context, taskID, requestID string, d GateDecision) error {
	if g.Hiber != nil && g.Hiber.Active(taskID) {
		return ErrGateFrozen
	}
	g.mu.Lock()
	t, ok := g.pending[requestID]
	if ok && (t.Req.TaskID != taskID) {
		ok = false // task_id 隔离:别任务的裁决不得触碰本任务请求
	}
	if ok {
		delete(g.pending, requestID)
	}
	g.mu.Unlock()
	if !ok {
		return ErrGateRequestNotFound
	}

	typ := rings.ApprovalRecDenied
	denyLine := rings.DenyLinePanel
	if d.Approved {
		typ = rings.ApprovalRecApproved
		denyLine = ""
	}
	by := d.By
	if by == "" {
		by = "creator"
	}
	if err := g.record(taskID, by, rings.ApprovalRecord{
		RequestID: requestID, StepID: t.Req.StepID, UnitID: t.Req.UnitID,
		Type: typ, ActionLevel: t.Req.ActionLevel, HarmClass: t.Req.HarmClass,
		Intent: t.Req.Intent, ToolName: t.Req.ToolName, ToolArgs: []byte(t.Req.ToolArgs),
		Note: t.Req.Note, DenyLine: denyLine, DecidedBy: by, Reason: d.Reason,
		WaitMillis: time.Since(t.requestedAt).Milliseconds(),
	}); err != nil {
		return err
	}
	t.ch <- d
	return nil
}

// Expire 结案:等待裁决期间任务被熔断/收尾,按拒绝结案(留痕不因 ctx
// 取消而断)。裁决已先行送达时请求不在待决表,如实返回不存在。
func (g *LedgerGate) Expire(taskID, requestID, reason string) error {
	g.mu.Lock()
	t, ok := g.pending[requestID]
	if ok && (t.Req.TaskID != taskID) {
		ok = false
	}
	if ok {
		delete(g.pending, requestID)
	}
	g.mu.Unlock()
	if !ok {
		return ErrGateRequestNotFound
	}
	return g.record(taskID, "swarm:"+t.Req.UnitID, rings.ApprovalRecord{
		RequestID: requestID, StepID: t.Req.StepID, UnitID: t.Req.UnitID,
		Type: rings.ApprovalRecDenied, ActionLevel: t.Req.ActionLevel,
		HarmClass: t.Req.HarmClass, Intent: t.Req.Intent,
		ToolName: t.Req.ToolName, ToolArgs: []byte(t.Req.ToolArgs),
		Note: t.Req.Note, DenyLine: rings.DenyLineCloseout, Reason: reason,
		WaitMillis: time.Since(t.requestedAt).Milliseconds(),
	})
}

// Pending 待裁决请求快照(按登记时间升序)。
func (g *LedgerGate) Pending(taskID string) []ApprovalRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []ApprovalRequest
	for _, t := range g.pending {
		if t.Req.TaskID == taskID {
			out = append(out, t.Req)
		}
	}
	// 请求数极少,插入排序足够。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- P4-33 冬眠让位的票据配套 ----

// RetireTickets 冬眠收场放行:任务下全部待裁决票向等待单位送达 Released
// 令(单位收场落地,票面保持待裁决冻结——不产生任何裁决记录,面板
// 冬眠中只读可见)。返回放行票数。
func (g *LedgerGate) RetireTickets(taskID string) int {
	g.mu.Lock()
	var tickets []*GateTicket
	for _, t := range g.pending {
		if t.Req.TaskID == taskID {
			tickets = append(tickets, t)
		}
	}
	g.mu.Unlock()
	for _, t := range tickets {
		select {
		case t.ch <- GateDecision{Released: true}:
		default: // 票已先行送达(竞态):等待方自行处理
		}
	}
	return len(tickets)
}

// DropTickets 唤醒清出:冬眠收场让位后遗留的待裁决票不可再裁(持有
// 单位已退出)——清出待决表,零裁决记录,等价重启后的内存票据清空;
// 账本 requested 原样留痕(审计),唤醒后重领的单位重新过门时重新
// 立票。返回清出票数。
func (g *LedgerGate) DropTickets(taskID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for id, t := range g.pending {
		if t.Req.TaskID == taskID {
			delete(g.pending, id)
			n++
		}
	}
	return n
}

// ExpireTickets 熔断结案:冬眠任务熔断时,遗留待裁决票逐笔按拒绝结案
// 留痕(与挂起单位被任务收尾 cancel 的既有口径一致)。返回结案票数。
func (g *LedgerGate) ExpireTickets(taskID, reason string) int {
	g.mu.Lock()
	var ids []string
	for id, t := range g.pending {
		if t.Req.TaskID == taskID {
			ids = append(ids, id)
		}
	}
	g.mu.Unlock()
	for _, id := range ids {
		_ = g.Expire(taskID, id, reason)
	}
	return len(ids)
}
