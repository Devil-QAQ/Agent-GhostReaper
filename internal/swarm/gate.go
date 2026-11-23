package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// ApprovalRequest 一次弹门请求:面板据此展示"意图声明 + 实际工具调用
// 参数"供造物主核对(v1 人即兜底,第四章 §3)。Note 携带弹门上下文
// (P4-03 ②:拦截规则命中转人审时说明原因,命令原文在 ToolArgs)。
type ApprovalRequest struct {
	owner          *rings.ExecutionOwner
	ID             string
	TaskID         string
	StepID         string
	UnitID         string
	Intent         string // 意图声明(Step 标题)
	ActionLevel    rings.ActionLevel
	HarmClass      rings.HarmClass
	ToolName       string
	ToolArgs       string // 原始 JSON 参数
	Note           string // 弹门上下文(空 = 常规门槛弹门,无附加说明)
	PolicyRevision int64  // operator policy used for this exact action; never a host permission
	CreatedAt      time.Time
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
// P5-05 挂等宽限:票上等待带 5 分钟宽限(WaitGrace),超时让位收场
// (步骤落 gate_wait 停等态,票留待决表);让位与裁决在票内同锁互斥
// (beginYield),先到先处理。
type GateTicket struct {
	Req         ApprovalRequest
	ch          chan GateDecision
	requestedAt time.Time
	// mu 票内互斥锁(P5-05 让位/裁决串行化——单写者同锁):让位判定
	//(beginYield 排空裁决通道并置让位标记)与 Decide 的送达+唤醒在
	// 此串行,第 5 分钟边界的并发到达确定性收敛。
	mu         sync.Mutex
	closed     bool
	registered atomic.Bool
	// yielded 让位标记:等待单位已收场(步骤转停等态),Decide 送达后
	// 须唤醒步骤回队首档(已批准待上位),裁决通道里的决定无人消费。
	yielded bool
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

// WaitGrace 带宽限的等待裁决(P5-05):宽限内裁决到达=原位继续(零开销);
// 宽限耗尽先排空裁决通道(裁决与让位恰在边界并发到达的竞态在此收敛)
// ——有裁决则消费(让位取消),无裁决返回 yielded=true 由调用方冻结
// 收场让位。released/裁决的后续处置与 Wait 一致,由调用方处理。
func (t *GateTicket) WaitGrace(ctx context.Context, grace time.Duration) (d GateDecision, err error, yielded bool) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case d := <-t.ch:
		return d, nil, false
	case <-ctx.Done():
		return GateDecision{}, ctx.Err(), false
	case <-timer.C:
		// 边界互斥:同锁下排空裁决通道——Decide 的送达(ch 缓冲)先于
		// 本锁到达则裁决在手让位取消;让位先置标记则 Decide 唤醒步骤
		// 走停等态解除(已批准待上位)。两条路径都确定性收敛。
		t.mu.Lock()
		select {
		case d := <-t.ch:
			t.mu.Unlock()
			return d, nil, false
		default:
			t.yielded = true
			t.mu.Unlock()
			return GateDecision{}, nil, true
		}
	}
}

// Yielded 让位标记的读侧(Decide 唤醒判定用)。
func (t *GateTicket) Yielded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.yielded
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
	t.mu.Lock()
	defer t.mu.Unlock()
	g.mu.Lock()
	g.pending[req.ID] = t
	g.mu.Unlock()
	typ := rings.ApprovalRecRequested

	if err := g.recordFor(req, false, "swarm:"+req.UnitID, rings.ApprovalRecord{
		RequestID: req.ID, StepID: req.StepID, UnitID: req.UnitID,
		Type: typ, ActionLevel: req.ActionLevel,
		HarmClass: req.HarmClass, Intent: req.Intent,
		ToolName: req.ToolName, ToolArgs: []byte(req.ToolArgs), Note: req.Note,
	}); err != nil {
		t.closed = true
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		return nil, err
	}
	t.registered.Store(true)
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
	t := g.ticket(taskID, requestID)
	if t == nil {
		return ErrGateRequestNotFound
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrGateRequestNotFound
	}
	// Commit subscribers read the durable outcome during a decision write;
	// closeTicket may not yet have removed the in-memory request.
	t.registered.Store(false)
	defer t.registered.Store(true)
	if g.Hiber != nil && g.Hiber.Active(taskID) {
		return ErrGateFrozen
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
	if err := g.recordFor(t.Req, true, by, rings.ApprovalRecord{
		RequestID: requestID, StepID: t.Req.StepID, UnitID: t.Req.UnitID,
		Type: typ, ActionLevel: t.Req.ActionLevel, HarmClass: t.Req.HarmClass,
		Intent: t.Req.Intent, ToolName: t.Req.ToolName, ToolArgs: []byte(t.Req.ToolArgs),
		Note: t.Req.Note, DenyLine: denyLine, DecidedBy: by, Reason: d.Reason,
		WaitMillis: time.Since(t.requestedAt).Milliseconds(),
	}); err != nil {
		if errors.Is(err, rings.ErrExecutionEnded) {
			if closeErr := g.expireLocked(t, "所属执行已结束，批准未生效"); closeErr != nil {
				return closeErr
			}
			return ErrGateRequestNotFound
		}
		return err
	}
	g.closeTicket(t)
	select {
	case t.ch <- d:
	default:
	}

	// P5-05 让位后的裁决:等待单位已冻结收场(步骤停等态),送达无人
	// 消费——裁决落账后唤醒步骤回可领取+队首档(已批准待上位,批准与
	// 动作类拒绝同走队首:批准续跑指令带放行配额,拒绝带换路反馈)。
	// 唤醒前核对步骤现值:仅在停等态/收场过渡(running)上唤——终局
	// 清点等他方迁移不复活。
	if t.yielded && !d.Released {
		g.wakeYieldedStep(t.Req)
	}
	return nil
}

// wakeYieldedStep 让位步骤的裁决唤醒(P5-05):gate_wait 停等步骤回
// dispatched+队首档(Note "scope_head" 机器标记,投影合并 scope_head
// 与 head_seq;调度器/面板排序同源 StepLaunchLess)。收场过渡中
// (running,单位的终态入账未落)同样唤——单位侧终态写入对停等态有
// 跳过保护,不会覆盖唤醒。裁决者(actor=creator)即唤醒者,主宰事件
// 触发通道同批看见。
func (g *LedgerGate) wakeYieldedStep(req ApprovalRequest) {
	for {
		rev, err := g.store.CurrentRevision(gateLedgerCtx, req.TaskID)
		if err != nil {
			return
		}
		step, err := g.store.Node(gateLedgerCtx, req.TaskID, req.StepID)
		if err != nil || (step.Status != rings.StepGateWait && step.Status != "running") {
			return
		}
		ev := rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: req.StepID, Status: "dispatched", Note: "scope_head"}}
		if req.owner != nil {
			owner := *req.owner
			owner.ApprovalRequest = req.ID
			_, err = g.store.AppendForExecution(gateLedgerCtx, req.TaskID, owner, ev)
		} else {
			_, err = g.store.AppendAtRevision(gateLedgerCtx, req.TaskID, rev, ev)
		}
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		if err != nil && !errors.Is(err, rings.ErrExecutionEnded) {
			runlog.Errorf("让位唤醒落账失败: %s %s: %v", req.TaskID, req.StepID, err)
		}
		return
	}
}

// Expire 结案:等待裁决期间任务被熔断/收尾,按拒绝结案(留痕不因 ctx
// 取消而断)。裁决已先行送达时请求不在待决表,如实返回不存在。
func (g *LedgerGate) Expire(taskID, requestID, reason string) error {
	t := g.ticket(taskID, requestID)
	if t == nil {
		return ErrGateRequestNotFound
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrGateRequestNotFound
	}
	return g.expireLocked(t, reason)
}

func (g *LedgerGate) expireLocked(t *GateTicket, reason string) error {
	t.registered.Store(false)
	defer t.registered.Store(true)
	err := g.record(t.Req.TaskID, "swarm:"+t.Req.UnitID, rings.ApprovalRecord{
		RequestID: t.Req.ID, StepID: t.Req.StepID, UnitID: t.Req.UnitID,
		Type: rings.ApprovalRecDenied, ActionLevel: t.Req.ActionLevel,
		HarmClass: t.Req.HarmClass, Intent: t.Req.Intent,
		ToolName: t.Req.ToolName, ToolArgs: []byte(t.Req.ToolArgs),
		Note: t.Req.Note, DenyLine: rings.DenyLineCloseout, Reason: reason,
		WaitMillis: time.Since(t.requestedAt).Milliseconds(),
	})
	if err != nil {
		return err
	}
	g.closeTicket(t)
	select {
	case t.ch <- GateDecision{Released: true}:
	default:
	}
	return nil
}

func (g *LedgerGate) ticket(task, id string) *GateTicket {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.pending[id]
	if t == nil || t.Req.TaskID != task {
		return nil
	}
	return t
}
func (g *LedgerGate) closeTicket(t *GateTicket) {
	t.closed = true
	g.mu.Lock()
	delete(g.pending, t.Req.ID)
	g.mu.Unlock()
}
func (g *LedgerGate) recordFor(req ApprovalRequest, allowYield bool, actor string, rec rings.ApprovalRecord) error {
	if req.owner == nil {
		return g.record(req.TaskID, actor, rec)
	}
	owner := *req.owner
	if allowYield {
		owner.ApprovalRequest = req.ID
	}
	_, err := g.store.AppendForExecution(gateLedgerCtx, req.TaskID, owner, rings.Event{Kind: rings.EventApprovalRecord, Actor: actor, Payload: rec})
	return err
}

// Pending 待裁决请求快照(按登记时间升序)。
func (g *LedgerGate) Pending(taskID string) []ApprovalRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []ApprovalRequest
	for _, t := range g.pending {
		if t.Req.TaskID != taskID {
			continue
		}
		ready := t.registered.Load()
		if !ready {
			// The commit notification may run before Enter's caller resumes.
			// Publish only a request already present in the durable ledger.
			rows, err := g.store.ApprovalByRequest(gateLedgerCtx, taskID, t.Req.ID)
			ready = err == nil && len(rows) > 0 && rows[len(rows)-1].Type == rings.ApprovalRecRequested
		}
		if ready {
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
		t.mu.Lock()
		if !t.closed {
			select {
			case t.ch <- GateDecision{Released: true}:
			default:
			}
		}
		t.mu.Unlock()
	}
	return len(tickets)
}

// DropTickets 唤醒清出:冬眠收场让位后遗留的待裁决票不可再裁(持有
// 单位已退出)——清出待决表,零裁决记录,等价重启后的内存票据清空;
// 账本 requested 原样留痕(审计),唤醒后重领的单位重新过门时重新
// 立票。返回清出票数。
func (g *LedgerGate) DropTickets(taskID string) int {
	g.mu.Lock()
	var tickets []*GateTicket
	for _, t := range g.pending {
		if t.Req.TaskID == taskID {
			tickets = append(tickets, t)
		}
	}
	g.mu.Unlock()
	n := 0
	for _, t := range tickets {
		t.mu.Lock()
		if !t.closed {
			g.closeTicket(t)
			n++
		}
		t.mu.Unlock()
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
