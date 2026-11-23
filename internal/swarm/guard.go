package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/rings"
)

// DefaultAnchorEverySteps 重锚定代码默认间隔(第八章第 8 节防线③:每
// N 步自动重注入任务包核心)。任务显式值 > Agent 页全局默认(P2-08d,
// Claim 应用)> 本默认(10);面板建任务表单可调。N 按"工具调用
// 边界"计——一次工具调用(含被舵杆暂缓的调用)是一个步。
const DefaultAnchorEverySteps = 10

// toolGuard 工具门卫(P2-05 纠偏纵深在执行层的挂接点):包装虫群单位的
// 全部工具,在每次工具调用边界做两件事——
//
//  1. 舵杆生效点(steer_work / kill_work 的 steer 侧):调用执行前先取
//     舵杆排队指令,有则暂缓本次调用、把纠偏指令注入上下文(Extra 用户
//     消息,引擎落 transcript),单位读到后自行重新规划——不打断、不丢
//     已有进展;"下一次工具调用之前注入"由此字面成立:被暂缓的调用
//     没有执行,指令先于其后的任何工具调用进入上下文。
//  2. 循环内重锚定(防线③):每 N 个调用边界把任务包核心(目标+意图+
//     预期观察+剩余预算)重注入一次(Extra 用户消息,transcript 可查),
//     治渐进式漂移(目标被长历史稀释)。
//
// Extra 消息由引擎随工具结果之后记入会话历史与 transcript(query.go
// extraMessages 路径),下一轮模型调用即见——注入内容对单位是 standing
// guidance,对审计是 transcript 里可查证的用户消息。
type toolGuard struct {
	env    *unitEnv
	pack   *TaskPack
	helm   *Helm
	everyN int

	start    time.Time
	deadline time.Time // 单位墙钟上限(含任务收尾线取小后的值);零值=不限
	maxTurns int

	mu    sync.Mutex // 并行工具调用下边界计数串行
	calls int        // 已经历的调用边界数(含暂缓)

	turns atomic.Int64 // 已用模型轮次(drive 侧 KindUsage 喂入,近似值)
}

// newToolGuard 构建门卫。everyN <= 0 时取默认(DefaultAnchorEverySteps)。
func newToolGuard(env *unitEnv, pack *TaskPack, helm *Helm, everyN int, deadline time.Time, maxTurns int) *toolGuard {
	if everyN <= 0 {
		everyN = DefaultAnchorEverySteps
	}
	return &toolGuard{
		env: env, pack: pack, helm: helm, everyN: everyN,
		start: time.Now(), deadline: deadline, maxTurns: maxTurns,
	}
}

// refreshBudget 刷新锚的剩余预算读数(P2-06b 段化执行:弹门挂起等待期
// 计时暂停,批准续跑后按剩余量重算;deadline 零值=不限)。maxTurns 是
// 当前预算窗的轮次顶(P4-01 ②:续跑重算后窗顶随基线前移,drive 传入
// 窗基线+段上限,与累计轮次读数同口径)。
func (g *toolGuard) refreshBudget(deadline time.Time, maxTurns int) {
	g.deadline = deadline
	g.maxTurns = maxTurns
}

// wrappedTool 包装后的工具:元数据(名称/Schema/权限)全透传内层,
// 只在 Call 上挂边界逻辑。
type wrappedTool struct {
	inner tool.CoreTool
	guard *toolGuard
}

func (w *wrappedTool) Name() string                { return w.inner.Name() }
func (w *wrappedTool) Description() string         { return w.inner.Description() }
func (w *wrappedTool) Prompt() string              { return w.inner.Prompt() }
func (w *wrappedTool) InputSchema() map[string]any { return w.inner.InputSchema() }
func (w *wrappedTool) IsReadOnly(in json.RawMessage) bool {
	return w.inner.IsReadOnly(in)
}

func (w *wrappedTool) IsConcurrencySafe(in json.RawMessage) bool {
	return w.inner.IsConcurrencySafe(in)
}

func (w *wrappedTool) CheckPermissions(ctx context.Context, in json.RawMessage, pc permission.Context) permission.Decision {
	return w.inner.CheckPermissions(ctx, in, pc)
}

func (w *wrappedTool) Call(ctx context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
	if !w.guard.env.beginCallback(ctx) {
		return tool.Errorf("所属执行已结束，本次调用未执行"), nil
	}
	defer w.guard.env.endCallback()
	res, err := w.guard.call(w.inner, ctx, in, tc)
	text := res.Flatten()
	if err != nil {
		text += "\n" + err.Error()
	}
	id := ""
	if tc != nil {
		id = tc.ToolUseID
	}
	if auditErr := w.guard.env.recordActualToolResult(id, w.inner.Name(), in, text, res.IsError || err != nil); auditErr != nil {
		return res, auditErr
	}
	return res, err
}

// wrapAll 包装工具全集。
func (g *toolGuard) wrapAll(tools []tool.CoreTool) []tool.CoreTool {
	out := make([]tool.CoreTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, &wrappedTool{inner: t, guard: g})
	}
	return out
}

// anchorMarker 重锚定消息的稳定标识(transcript 断言与面板对账用)。
const anchorMarker = "【任务锚 · 重锚定】"

// steerMarker 舵杆注入消息的稳定标识。
const steerMarker = "【纠偏指令 · steer】"

// pauseDeferNote 单位暂停/任务冬眠暂缓调用的结果文本(账本 tool_call
// 记录 is_error 的 ResultHead 可辨认——被暂缓的调用没有执行内层工具)。
const pauseDeferNote = "本次调用已暂缓(未执行):单位挂起(暂停/任务冬眠),恢复后继续"

// deferNote 舵杆暂缓调用的结果文本(账本 tool_call 记录 is_error 的
// ResultHead 可辨认——被暂缓的调用没有执行内层工具)。
const deferNote = "本次调用已暂缓(未执行):有纠偏指令待你消化,见随后的指令消息"

// ActorOvermindHelm 主宰经舵杆下达时的 By 标识(与操作者 creator 区分)。
const ActorOvermindHelm = "overmind"

// call 边界逻辑:挂起检查 → 舵杆生效点 → 拦截判定补口(只读工具)→
// 内层执行 → 重锚定节拍。同一助手轮的并行工具调用并发进入时,舵杆
// 指令由先到的边界取走并全量注入(一次性送达);重锚定计数在锁内推进,
// 节拍不变式恒成立:注入次数 == floor(调用边界数 / N)。
//
// P2-08 挂起检查在最前:单位暂停闩/任务冬眠门任一在即,本次调用暂缓
// (不执行内层工具)并收拢当前执行段——"暂停在当前工具调用完成后挂起"
// 的字面落地:正在执行的工具调用照常完成,边界处不再发起下一个。已
// 排队的舵杆指令不取走(留在排队表,恢复后的第一个边界照常送达——
// 回执不伪造)。
//
// P2-08e 拦截判定补口:只读工具(port_probe/http_probe)在 Norma 权限
// 管线第 3 阶段自动放行、不进 CanUseTool——硬规则与任务级拦截在调用
// 边界兜住,触达目标的工具全部逐次判定;非只读工具(bash)已在审批门
// 判过,此处不重复(防配额双重计数)。回流入口(submit_*)只写自家
// 账本不碰目标,豁免(防证据原文里的破坏性字样误伤)。P4-63:硬规则
// 确定性拒绝只拒本次动作(单位存活换路;P5-03③ 起无连拒计数升级杀),
// 模糊形态弹门人审。
func (g *toolGuard) call(inner tool.CoreTool, ctx context.Context, in json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
	if g.env.Executor.unitShouldPark(g.env) {
		g.env.reviewMu.Lock()
		g.env.parkArmed = true
		g.env.fuseSegment()
		g.env.reviewMu.Unlock()
		return tool.Errorf(pauseDeferNote), nil
	}
	g.mu.Lock()
	g.calls++
	n := g.calls
	fireAnchor := n%g.everyN == 0
	var dirs []rings.SteerRecord
	if g.helm != nil {
		var err error
		dirs, err = g.helm.takeForUnit(g.env.TaskID, g.env.StepID, g.env.UnitID)
		if err != nil {
			g.mu.Unlock()
			return tool.Result{}, fmt.Errorf("纠偏指令核对失败: %w", err)
		}
	}
	g.mu.Unlock()

	// ---- 舵杆生效点:下一次工具调用之前注入 ----
	if len(dirs) > 0 {
		if err := g.recordDeliveredBatch(dirs, n); err != nil {
			g.helm.returnPending(g.env.TaskID, g.env.StepID, dirs)
			return tool.Result{}, fmt.Errorf("纠偏送达记录失败: %w", err)
		}
		res := tool.Errorf(deferNote)
		for _, d := range dirs {
			res.Extra = append(res.Extra, llm.UserText(g.steerMessage(d)))
		}
		if fireAnchor {
			res.Extra = append(res.Extra, llm.UserText(g.anchorMessage(n)))
		}
		return res, nil
	}

	// ---- 拦截判定补口(只读工具)----
	if inner.IsReadOnly(in) {
		if reason := g.guardReadOnlyCall(ctx, inner.Name(), in); reason != "" {
			return tool.Errorf(reason), nil
		}
	}
	if tc != nil && tc.ToolUseID != "" {
		if _, err := g.env.appendBusiness(ctx, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + g.env.UnitID, Payload: rings.SwarmRecord{
			Type: rings.SwarmRecToolStarted, StepID: g.env.StepID, UnitID: g.env.UnitID, TranscriptID: g.env.TranscriptID,
			ToolCallID: tc.ToolUseID, ToolName: inner.Name(), ToolArgs: in,
		}}); err != nil {
			return tool.Result{}, fmt.Errorf("工具开始记录失败: %w", err)
		}
	}

	res, err := inner.Call(ctx, in, tc)
	// 后台命令完成提醒(P3-05):转后台的 bash 命令一结束,事实随本
	// 边界注入(Extra 用户消息,transcript 可查),模型免盲轮询。
	for _, note := range g.env.Executor.bgNotes(g.env.UnitID) {
		res.Extra = append(res.Extra, llm.UserText(note))
	}
	if fireAnchor {
		res.Extra = append(res.Extra, llm.UserText(g.anchorMessage(n)))
	}
	return res, err
}

// guardExemptTool 拦截判定豁免的工具:受控回流入口(submit_*)只写自家
// 账本、不触达目标环境——拦它没有安全收益,只会让证据原文里的破坏性
// 字样(DROP TABLE 等)误伤正常写回。
func guardExemptTool(toolName string) bool {
	if strings.HasPrefix(toolName, "submit_") {
		return true
	}
	// These registered tools only read this task's ledger. They cannot execute
	// a command or contact a target; another model decision would review the
	// same policy information while accomplishing no environmental action.
	switch toolName {
	case "search_facts", "list_assets", "search_trails", "list_claims", "traffic_search", "traffic_get", "traffic_blob":
		return true
	}
	return false
}

// guardReadOnlyCall uses the same decision chain as non-read-only tools.
// The harness skips CanUseTool for read-only tools, so this is its one entry.
func (g *toolGuard) guardReadOnlyCall(ctx context.Context, toolName string, in json.RawMessage) string {
	d := g.env.approvalGate(g.pack)(ctx, toolName, in, permission.Context{})
	if d.Behavior == permission.Deny {
		return d.Message
	}
	return ""
}

// steerMessage 渲染纠偏指令(注入上下文的用户消息)。回执要求(P2-08
// 回执状态机):单位须在下一条回复开头单独一行输出回执标记,drive 扫
// 描标记落账(已采纳/被拒)。
func (g *toolGuard) steerMessage(d rings.SteerRecord) string {
	by := "操作者"
	if d.By == ActorOvermindHelm {
		by = "主宰(心跳巡检)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n来自 %s,注入于你的下一次工具调用前(本次调用已暂缓,未执行):\n%s\n",
		steerMarker, by, d.Message)
	b.WriteString("请消化指令后自行重新规划:与指令冲突的方向不要继续;原有动作仍有必要时可重新发起(暂缓不是拒绝);已取得的进展不受影响。\n")
	fmt.Fprintf(&b, "消化后在你的下一条回复开头单独一行输出回执(三选一,不要输出其他格式):【STEER:%s:采纳】 或 【STEER:%s:拒绝:一句理由】;暂不表态就继续干活,不要停在等确认上。\n", d.SteerID, d.SteerID)
	return b.String()
}

// anchorMessage 渲染任务锚(任务包核心:目标+意图+预期观察+剩余预算)。
func (g *toolGuard) anchorMessage(nthBoundary int) string {
	p := g.pack
	var b strings.Builder
	fmt.Fprintf(&b, "%s(第 %d 个调用边界注入;每 %d 步重锚一次)\n", anchorMarker, nthBoundary, g.everyN)
	if p.GoalID != "" {
		fmt.Fprintf(&b, "目标: %s (%s)\n", clipLine(p.GoalTitle, 120), p.GoalID)
	} else {
		b.WriteString("目标: (图中无目标节点,以本步骤意图为准)\n")
	}
	fmt.Fprintf(&b, "本步骤意图(只做这一条): %s\n", clipLine(p.StepIntent, 200))
	if p.ExpectedObservation != "" {
		fmt.Fprintf(&b, "预期观察: %s\n", clipLine(p.ExpectedObservation, 160))
	}
	// 造物主注意事项重申(P4-49 三处注入之三):长会话漂移防线——
	// 与任务包同源的领取时冻结副本,每 N 步随锚重申一次。
	if p.Authorization.CreatorNotes != "" {
		b.WriteString("造物主注意事项(重申,须遵守):\n")
		for _, line := range strings.Split(p.Authorization.CreatorNotes, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Fprintf(&b, "  · %s\n", line)
			}
		}
	}
	// 剩余预算读数(P4-01:0=不限的维度不显示"约剩 0/0"式误导读数,
	// 如实呈现已用轮次/时长;两侧都设了才并排显示余量)。
	turnsUsed := int(g.turns.Load())
	runSecs := time.Since(g.start).Seconds()
	remainSecs := -1 // -1 = 墙钟不限
	if !g.deadline.IsZero() {
		if s := int(time.Until(g.deadline).Seconds()); s > 0 {
			remainSecs = s
		} else {
			remainSecs = 0
		}
	}
	switch {
	case g.maxTurns > 0 && remainSecs >= 0:
		remainTurns := g.maxTurns - turnsUsed
		if remainTurns < 0 {
			remainTurns = 0
		}
		fmt.Fprintf(&b, "剩余预算: 轮次约剩 %d/%d,墙钟约剩 %d 秒(已运行 %.0f 秒)\n",
			remainTurns, g.maxTurns, remainSecs, runSecs)
	case g.maxTurns > 0:
		remainTurns := g.maxTurns - turnsUsed
		if remainTurns < 0 {
			remainTurns = 0
		}
		fmt.Fprintf(&b, "剩余预算: 轮次约剩 %d/%d(已运行 %.0f 秒)\n", remainTurns, g.maxTurns, runSecs)
	case remainSecs >= 0:
		fmt.Fprintf(&b, "剩余预算: 墙钟约剩 %d 秒,轮数不限(已运行 %.0f 秒)\n", remainSecs, runSecs)
	default:
		fmt.Fprintf(&b, "剩余预算: 不限(已用 %d 轮,已运行 %.0f 秒)\n", turnsUsed, runSecs)
	}
	if !p.TaskDeadline.IsZero() && p.TaskDeadline.After(time.Now()) {
		fmt.Fprintf(&b, "任务收尾线: %s(到点就地收尾,把发现写回图)\n", p.TaskDeadline.Format("15:04:05"))
	}
	b.WriteString("纪律提醒: 意图外线索只回传不深挖;边发现边写回;被拒不重试。\n")
	return b.String()
}

// recordDelivered 送达记账:每条指令独立落 delivered(回指排队记录的
// SteerID,消息原文随行——单条查询自足)。
func (g *toolGuard) recordDeliveredBatch(dirs []rings.SteerRecord, atBoundary int) error {
	events := make([]rings.Event, 0, len(dirs))
	for _, d := range dirs {
		d.State, d.UnitID = rings.SteerStateDelivered, g.env.UnitID
		d.Note = fmt.Sprintf("已交该单位第 %d 个工具调用边界返回，等待单位处理；该次调用未执行", atBoundary)
		events = append(events, rings.Event{Kind: rings.EventSteerRecord, Actor: "swarm:" + g.env.UnitID, Payload: d})
	}
	_, err := g.env.appendBusiness(steerCtx, events...)
	return err
}

// This capability reads the platform's immutable output store, not a remote
// target or an arbitrary local path. Its input cannot turn it into execution.
func (env *unitEnv) internalArtifactRead(name string, input json.RawMessage) bool {
	if name != "read" || env.Executor.Blobs == nil {
		return false
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &args) != nil || !strings.HasPrefix(args.Path, BlobRefPrefix) {
		return false
	}
	_, err := env.Executor.Blobs.Path(args.Path)
	return err == nil
}
