package shepherd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/skill"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ActorShepherd 牧者的账本 actor(shepherd.record 事件归属)。
const ActorShepherd = "shepherd"

// 会话与消息标识。
//
// SessionID 每任务一个牧者会话(第四章 §1"每个任务一个总对话入口,
// 独立会话"):shepherd-<taskID>,transcript JSONL 落任务目录,跨消息
// 原会话续(每次应答经 Resume 恢复上下文,对话不因消息间隔重置)。
func SessionID(taskID string) string { return "shepherd-" + taskID }

// ClaimQASID 一轮成果追问会话的转录锚(P4-14):shepherdqa-<claimID>-r<N>。
// 每轮独立会话(续接不新建;"新开一轮"才换锚)——token 行按此与牧者
// 主对话分开对账(信息条独立计),原会话续经 Resume 跨消息保持上下文。
func ClaimQASID(claimID string, round int) string {
	return fmt.Sprintf("shepherdqa-%s-r%d", claimID, round)
}

// qaLinePrefix 成果追问工作线键前缀(同成果的追问串行应答;轮是会话
// 边界,不是工作线边界)。
const qaLinePrefix = "qa:"

func qaLine(claimID string) string { return qaLinePrefix + claimID }

// convKey 工作线在 convs 表的键(进程内)。
func convKey(taskID, line string) string { return taskID + "\x00" + line }

// Service 牧者会话服务(P2-07):每个任务一条串行会话工作线——
// 面板消息入账(user_message,已排队)即返回,后台工作器逐条应答
// (送达 → 工具调用留痕 → 回复入账);对话历史全走账本事件流,
// 重启后从事件流重建视图,未应答消息收养续答。零值不可用。
type Service struct {
	Store    *rings.Store
	Provider llm.Provider // nil = 未配模型(消息入账后落失败态,面板如实呈现)
	// RootDir 任务工作目录根(转录 JSONL 落盘);空 = 不落转录。
	RootDir string
	// Model 静态模型名兜底;ModelOf 非空时按请求解析(failover 链注入,
	// 主备切换后 token 按模型分账)。
	Model   string
	ModelOf func() string
	// WindowOf 当前上下文窗口解析(token;0 = 未配置——不启用自动压缩,
	// 信息条的消耗比例不呈现)。装配层从模型库解析注入。
	WindowOf func() int
	// Wake 任务唤醒(紧急意图/建议/目标/约束入账后通知主宰;装配层
	// 注入 Manager 的 Task.Wake 路径)。nil = 不唤醒(单测)。
	Wake func(taskID string)
	// Hiber 任务冬眠门(P2-08,第七章「任务冬眠」:牧者该任务会话
	// 冻结——入口拒绝新消息并提示"任务冬眠中",对话历史照常只读;
	// 已排队未答的消息挂起在门上,唤醒后续答)。nil = 无冬眠语义。
	Hiber *hiber.Gate
	// Skills 文风改写 skill 注册表(第四章 §10 按需层):非 nil 时会话
	// 挂 Norma 原生 Skill 工具(deferred——工具描述只带名字与触发
	// 条件,完整指令调用时才注入)。nil = 不挂(加载失败降级,对话
	// 照常)。
	Skills *skill.Registry

	// TurnLimit 单条消息的模型轮上限(工具循环轮数);零值 8。
	TurnLimit int
	// TurnSeconds 单条消息的墙钟上限;零值 240。
	TurnSeconds int
	// MaxTokens 单回复输出上限;零值 4096。
	MaxTokens int

	mu    sync.Mutex
	convs map[string]*conversation
}

// conversation 一个任务的会话工作线(busy 时不再起新工作器,消息在
// 账本里排队依次应答)。
type conversation struct {
	kick chan struct{}
	busy bool
}

// TurnLimitDefault / TurnSecondsDefault 单条消息预算默认值(牧者是
// 对话角色:轮数够查图作答即可,不给长执行)。
const (
	TurnLimitDefault   = 8
	TurnSecondsDefault = 240
)

func (s *Service) turnLimit() int {
	if s.TurnLimit > 0 {
		return s.TurnLimit
	}
	return TurnLimitDefault
}

func (s *Service) turnSeconds() int {
	if s.TurnSeconds > 0 {
		return s.TurnSeconds
	}
	return TurnSecondsDefault
}

func (s *Service) maxTokens() int {
	if s.MaxTokens > 0 {
		return s.MaxTokens
	}
	return 4096
}

func (s *Service) wake(taskID string) {
	if s.Wake != nil {
		s.Wake(taskID)
	}
}

func (s *Service) window() int {
	if s.WindowOf != nil {
		return s.WindowOf()
	}
	return 0
}

func (s *Service) modelOfCall() string {
	if s.ModelOf != nil {
		if m := s.ModelOf(); m != "" {
			return m
		}
	}
	return s.Model
}

// ErrNoProvider 未配模型时入队的应答失败原因。
var ErrNoProvider = errors.New("shepherd: 模型未配置(设置页激活模型后重发)")

// ErrHibernated 任务冬眠中的统一拒绝文案(对话入口提示"任务冬眠中")。
var ErrHibernated = errors.New("shepherd: 任务冬眠中,对话已冻结(唤醒后继续;历史仍可查看)")

// hibernating 任务当前是否冬眠中。
func (s *Service) hibernating(taskID string) bool {
	return s.Hiber != nil && s.Hiber.Active(taskID)
}

// ---- 入队与工作器 ----

// Enqueue 造物主发言入账(已排队):user_message 事件落账即返回,
// 应答由后台工作器异步完成(送达状态经 GET 视图轮询可见)。
// P2-08:任务冬眠中入口冻结——新消息拒绝(历史只读)。
func (s *Service) Enqueue(ctx context.Context, taskID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("shepherd: 消息不能为空")
	}
	if taskID == "" {
		return "", errors.New("shepherd: 任务 ID 不能为空")
	}
	if s.hibernating(taskID) {
		return "", ErrHibernated
	}
	id := shortID("sm")
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: "creator",
		Payload: rings.ShepherdRecord{MessageID: id, Type: rings.ShepherdRecUserMessage, Text: text},
	}); err != nil {
		return "", fmt.Errorf("shepherd: 消息入账: %w", err)
	}
	s.kickLine(taskID, "")
	return id, nil
}

// EnqueueClaim 成果追问发言入账(P4-14,问题清单 #24):一成果一线、
// 每轮一会话——newRound=false 续接当前轮(无轮则开第 1 轮),newRound=
// true 开新轮(旧轮留档,问答历史零删)。prelude 仅在该轮首条发言时由
// 调用方装配传入(服务端确定性成果链路上下文包),随 user_message 落账
// ——应答时注入模型会话、不上面板消息流;重启收养凭事件流照常携带。
// 返回(消息 ID, 轮次)。
func (s *Service) EnqueueClaim(ctx context.Context, taskID, claimID, text string, newRound bool, prelude string) (string, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, errors.New("shepherd: 追问不能为空")
	}
	if taskID == "" || claimID == "" {
		return "", 0, errors.New("shepherd: 任务或成果 ID 不能为空")
	}
	if s.hibernating(taskID) {
		return "", 0, ErrHibernated
	}
	round := 1
	if cur := s.CurrentRound(ctx, taskID, claimID); cur > 0 {
		if newRound {
			round = cur + 1
		} else {
			round = cur
		}
	}
	// 预载只随该轮首问携带:续问轮上误传的 prelude 不注入(防重复上下文)。
	if s.roundHasMessage(ctx, taskID, claimID, round) {
		prelude = ""
	}
	id := shortID("sm")
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: "creator",
		Payload: rings.ShepherdRecord{
			MessageID: id, Type: rings.ShepherdRecUserMessage, Text: text,
			ClaimID: claimID, Round: round, Prelude: prelude,
		},
	}); err != nil {
		return "", 0, fmt.Errorf("shepherd: 追问入账: %w", err)
	}
	s.kickLine(taskID, qaLine(claimID))
	return id, round, nil
}

// CurrentRound 成果当前追问轮次(0 = 尚未追问;多轮取最大)。
func (s *Service) CurrentRound(ctx context.Context, taskID, claimID string) int {
	sessions, err := s.claimQASessions(ctx, taskID)
	if err != nil {
		return 0
	}
	max := 0
	for _, x := range sessions {
		if x.ClaimID == claimID && x.Round > max {
			max = x.Round
		}
	}
	return max
}

// roundHasMessage 该轮是否已有发言(预载只随首问)。
func (s *Service) roundHasMessage(ctx context.Context, taskID, claimID string, round int) bool {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return false
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.ClaimID == claimID && rec.Round == round &&
			rec.Type == rings.ShepherdRecUserMessage {
			return true
		}
	}
	return false
}

// OnTerminal 任务终态收尾提示(P2-08f,Manager 终态记账点经装配层注入):
// 落一笔 system_note(任务已到终态)并唤起工作线——牧者据此输出一句
// 进展总结(先查图,只许用真实数据;§10 牧者常驻条款)。任务已终态,
// 该提示不受冬眠门冻结(终态任务不会再唤醒,门上挂着就永远答不上)。
// status 为 Manager 终态词(completed|stopped)。
func (s *Service) OnTerminal(taskID, status string) {
	if taskID == "" {
		return
	}
	word := map[string]string{"completed": "已完成", "stopped": "已停止"}[status]
	if word == "" {
		word = status
	}
	id := shortID("sn")
	if _, err := s.Store.Append(context.Background(), taskID, rings.Event{
		Kind: rings.EventShepherdRecord, Actor: "system",
		Payload: rings.ShepherdRecord{
			MessageID: id, Type: rings.ShepherdRecSystemNote,
			Text: fmt.Sprintf("任务已到终态:%s。请核对账本后输出一句任务进展总结(目标达成情况、确认发现数、关键结论)。", word),
		},
	}); err != nil {
		return
	}
	s.kickLine(taskID, "")
}

// Start 服务启动的重启收养(P2-06f 同纪律):账本里已排队未送达的
// 消息继续应答;已送达未回复的(进程中断残留)如实落失败态——原会话
// 转录里已有该消息与部分应答,重发会重复注入,按中断结案,操作者可
// 追问。牧者主对话与各成果追问线(P4-14)同纪律逐线收养。每条有待办
// 的线踢一脚工作器(无待办即空转退出)。
func (s *Service) Start(ctx context.Context) error {
	rows, err := s.Store.Tasks(ctx)
	if err != nil {
		return fmt.Errorf("shepherd: 收养扫描: %w", err)
	}
	for _, r := range rows {
		lines, aerr := s.adoptionScan(ctx, r.ID)
		if aerr != nil {
			return aerr
		}
		for line, broken := range lines {
			for _, msg := range broken {
				if _, err := s.Store.Append(ctx, r.ID, rings.Event{
					Kind: rings.EventShepherdRecord, Actor: ActorShepherd,
					Payload: rings.ShepherdRecord{
						MessageID: msg.id, Type: rings.ShepherdRecMessageState,
						ClaimID: msg.claimID, Round: msg.round,
						State: rings.ShepherdMsgFailed, Note: "进程重启,应答中断(原会话已保留该消息,可追问继续)",
					},
				}); err != nil {
					return fmt.Errorf("shepherd: 中断消息结案: %w", err)
				}
			}
			s.kickLine(r.ID, line)
		}
	}
	return nil
}

// adoptionScan 收养扫描:返回全部有待办的会话线(牧者主对话 = "" 线;
// 成果追问线 = "qa:<claimID>")及各自"已送达未回复"的消息(进程中断的
// 精确签名——送达事件在、回复事件不在)。
func (s *Service) adoptionScan(ctx context.Context, taskID string) (map[string][]*convMessage, error) {
	st, err := s.linesState(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := map[string][]*convMessage{}
	for line, cs := range st {
		pending, broken := false, []*convMessage{}
		for _, m := range cs.messages {
			switch {
			case m.state == "queued" && !m.replied:
				pending = true
			case m.state == rings.ShepherdMsgDelivered && !m.replied:
				// 已送达未回复且进程已重启 = 应答中断的精确签名;已回复的
				// 消息送达态就此终态,不再被收养误标。
				broken = append(broken, m)
			}
		}
		if pending || len(broken) > 0 {
			out[line] = broken
		}
	}
	return out, nil
}

// kickLine 唤起会话工作线(串行:busy 时只补 kick,消息排队依次答)。
// line 空 = 牧者主对话;"qa:<claimID>" = 成果追问线。
func (s *Service) kickLine(taskID, line string) {
	s.mu.Lock()
	if s.convs == nil {
		s.convs = map[string]*conversation{}
	}
	key := convKey(taskID, line)
	c, ok := s.convs[key]
	if !ok {
		c = &conversation{kick: make(chan struct{}, 1)}
		s.convs[key] = c
	}
	if !c.busy {
		c.busy = true
		go s.worker(context.Background(), taskID, line, c)
	}
	s.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// worker 逐条应答队列中的消息(队列即账本:每轮从事件流取最旧的
// 未应答消息),空则退出;退出前复查防"入账与退出"缝隙丢消息。
// P2-08:任务冬眠中挂起在门上(会话冻结,已排队消息唤醒后续答)。
func (s *Service) worker(ctx context.Context, taskID, line string, c *conversation) {
	defer func() {
		s.mu.Lock()
		c.busy = false
		s.mu.Unlock()
		// 复查:消息恰落在"取不到 → 退出"与"入账完成"之间时,由这里
		// 兜住(等价于对自己补一脚;无待办则空转即退,不空转循环)。
		if st, err := s.lineState(ctx, taskID, line); err == nil && st.queuedCount() > 0 {
			s.kickLine(taskID, line)
		}
	}()
	for {
		st, err := s.lineState(ctx, taskID, line)
		if err != nil {
			return
		}
		msg := st.oldestQueued()
		if msg == nil {
			return
		}
		// P2-08f:任务冬眠中会话冻结,但系统收尾提示(任务终态)例外——
		// 终态任务不会再唤醒,门上挂着就永远答不上;总结照常出。
		if s.hibernating(taskID) && !msg.system {
			if s.Hiber == nil || !s.Hiber.Wait(ctx, taskID) {
				return
			}
			continue
		}
		text := msg.text
		if msg.system {
			// 系统提示原样注入(正文已含核对账本与一句总结的指令)。
			text = "(系统提示)" + msg.text
		}
		s.answer(ctx, taskID, msg, text)
	}
}

// answer 应答一条消息:原会话续(Resume)→ 注入消息 → 流式消费
// (首个事件即送达)→ 工具调用/思考/回复逐笔入账。失败如实落
// message_state failed(原因可见,可重发)。msg.claimID 非空时为成果
// 追问会话(P4-14):转录锚与 token 记账换到该轮的独立会话,首问的
// 预载成果上下文包一并在注入文本前置(面板消息流不呈现预载)。
func (s *Service) answer(ctx context.Context, taskID string, msg *convMessage, text string) {
	fail := func(note string) {
		_, _ = s.Store.Append(context.Background(), taskID, rings.Event{
			Kind: rings.EventShepherdRecord, Actor: ActorShepherd,
			Payload: rings.ShepherdRecord{
				MessageID: msg.id, Type: rings.ShepherdRecMessageState,
				ClaimID: msg.claimID, Round: msg.round,
				State: rings.ShepherdMsgFailed, Note: note,
			},
		})
	}
	if s.Provider == nil {
		fail(ErrNoProvider.Error())
		return
	}

	sid := SessionID(taskID)
	if msg.claimID != "" {
		sid = ClaimQASID(msg.claimID, msg.round)
		if msg.prelude != "" {
			text = "(成果追问预载)\n" + msg.prelude + "\n\n【造物主的追问】" + text
		}
	}
	opts := agentcore.Options{
		Provider:     s.Provider,
		SystemPrompt: shepherdSystemSegments(ctx, s.Store),
		Tools:        s.toolset(taskID, msg.id),
		// 文风 skill 按需层(P3-11):非 nil 时 SDK 自动追加 Skill 工具,
		// 与看/说/答工具并列;nil 安全(不挂)。
		Skills: s.Skills,
		// 牧者工具全部为图上只读与账本受控写入,不触达目标侧——无门
		// 可弹(紧急意图的执行侧审批由虫群单位照常过门,R9 不破)。
		CanUseTool: func(context.Context, string, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		MaxTurns:               s.turnLimit(),
		MaxDuration:            time.Duration(s.turnSeconds()) * time.Second,
		MaxTokens:              s.maxTokens(),
		SessionID:              sid,
		DisableBackgroundTasks: true,
	}
	var tstore *transcript.Store
	if s.RootDir != "" {
		tstore = transcript.NewStore(filepath.Join(s.RootDir, taskID, "transcripts"))
		opts.Transcript = tstore
	}
	if w := s.window(); w > 0 {
		// MaxOutputTokens 同步会话输出上限:压缩预留 min(输出上限,20000),
		// 不设则默认预留 20000——小窗口配置会被预留吃穿(实测发现)。
		opts.Compaction = &compaction.Config{ContextWindow: w, MaxOutputTokens: s.maxTokens()}
	}
	sess := agentcore.NewSession(opts)
	defer sess.Close()
	// 原会话续:转录存在则恢复上下文(消息间隔不重置对话);缺失即
	// 全新会话(首条消息)。会话续不重复计历史 token(UsageTracker 段
	// 内增量,KindUsage 每次运行从零起算)。
	if tstore != nil {
		if _, err := os.Stat(tstore.MainPath(sid)); err == nil {
			if err := sess.Resume(sid); err != nil {
				fail("恢复会话失败: " + err.Error())
				return
			}
		}
	}

	usage := &swarm.UsageTracker{
		Store: s.Store, TaskID: taskID, Agent: ActorShepherd,
		Model: s.Model, ModelOf: s.ModelOf, Transcript: sid,
	}
	appendRec := func(p rings.ShepherdRecord) {
		p.ClaimID, p.Round = msg.claimID, msg.round
		_, _ = s.Store.Append(context.Background(), taskID, rings.Event{
			Kind: rings.EventShepherdRecord, Actor: ActorShepherd, Payload: p,
		})
	}

	toolNames := map[string]string{}
	toolArgs := map[string]json.RawMessage{}
	delivered := false
	var think strings.Builder
	replyText := ""
	for ev, err := range sess.Prompt(ctx, text) {
		if err != nil {
			fail("应答中断: " + clipHead(err.Error(), 300))
			return
		}
		if !delivered {
			delivered = true
			appendRec(rings.ShepherdRecord{
				MessageID: msg.id, Type: rings.ShepherdRecMessageState,
				State: rings.ShepherdMsgDelivered,
			})
		}
		switch ev.Kind {
		case harness.KindUsage:
			if ev.Usage != nil {
				_ = usage.OnUsage(*ev.Usage)
			}
		case harness.KindToolUse:
			if ev.ToolUse != nil {
				toolNames[ev.ToolUse.ID] = ev.ToolUse.Name
				if len(ev.ToolUse.Input) > 0 {
					toolArgs[ev.ToolUse.ID] = ev.ToolUse.Input
				}
			}
		case harness.KindToolResult:
			if ev.ToolResult == nil {
				continue
			}
			head := flattenBlocks(ev.ToolResult.Content)
			appendRec(rings.ShepherdRecord{
				MessageID: msg.id, Type: rings.ShepherdRecToolCall,
				ToolName: toolNames[ev.ToolResult.ToolUseID], ToolArgs: toolArgs[ev.ToolResult.ToolUseID],
				IsError: ev.ToolResult.IsError, ResultHead: clipHead(head, 1024),
			})
		case harness.KindThinking:
			if ev.Text != "" {
				think.WriteString(ev.Text)
			}
		case harness.KindResult:
			if ev.Terminal != nil {
				if ev.Terminal.Err != nil {
					fail("模型终止错误: " + clipHead(ev.Terminal.Err.Error(), 300))
					return
				}
				replyText = strings.TrimSpace(ev.Terminal.Text)
			}
		}
	}
	if t := strings.TrimSpace(think.String()); t != "" {
		appendRec(rings.ShepherdRecord{
			MessageID: msg.id, Type: rings.ShepherdRecThinking, Text: clipHead(t, 1200),
		})
	}
	if replyText == "" {
		fail("回复为空(模型未产出正文;可重发或换模型)")
		return
	}
	appendRec(rings.ShepherdRecord{
		MessageID: msg.id, Type: rings.ShepherdRecReply, Text: clipTail(replyText, 16000),
	})
}

// ---- 读视图 ----

// ToolCallView 一次牧者工具调用(折叠卡的面板数据)。
type ToolCallView struct {
	Name       string `json:"name"`
	Args       string `json:"args,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	ResultHead string `json:"result_head,omitempty"`
	At         string `json:"at"`
}

// MessageView 一条问答(user_message 及其锚定的状态/工具/思考/回复/建议;
// System=true 为系统收尾提示,正文非造物主发言)。
type MessageView struct {
	ID        string         `json:"id"`
	Text      string         `json:"text"`
	System    bool           `json:"system,omitempty"`
	State     string         `json:"state"` // queued|delivered|failed
	Note      string         `json:"note,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	ToolCalls []ToolCallView `json:"tool_calls,omitempty"`
	Advices   []AdviceView   `json:"advices,omitempty"` // 本条消息内提交的建议(内联呈现)
	Reply     string         `json:"reply,omitempty"`
	At        string         `json:"at"`
	ReplyAt   string         `json:"reply_at,omitempty"`
}

// AdviceView 一条对主宰的建议(纳谏轨迹)。
type AdviceView struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	State  string `json:"state"` // pending|adopted|rejected
	Reason string `json:"reason,omitempty"`
	At     string `json:"at"`
}

// View 会话读视图(面板对话页的数据面;信息条的 token/模型数据由
// server 层从 token 独立表与 failover 链补齐)。Round/Rounds/Questions
// 为成果追问会话的轮次信息(P4-14;主对话为零值)。
type View struct {
	Messages   []MessageView `json:"messages"`
	Advices    []AdviceView  `json:"advices"`
	Busy       bool          `json:"busy"`
	StartedAt  string        `json:"started_at,omitempty"` // 会话首事件时刻(信息条时长锚点)
	ProviderOn bool          `json:"provider_on"`          // 模型是否已装配
	Round      int           `json:"round,omitempty"`      // 追问:本视图轮次
	Rounds     int           `json:"rounds,omitempty"`     // 追问:该成果全部轮数
	Questions  int           `json:"questions,omitempty"`  // 追问:本视图轮内问数
}

type convMessage struct {
	id, text, state, note string
	replied               bool   // reply 事件已落(送达态的终态判定)
	system                bool   // system_note(任务终态收尾提示,P2-08f):不受冬眠冻结
	claimID               string // 成果追问锚(空 = 牧者主对话)
	round                 int    // 追问轮次(claimID 非空时有效)
	prelude               string // 该轮首问的预载上下文包(注入模型会话,不上面板)
}

type convState struct {
	messages []*convMessage
	byID     map[string]*convMessage
}

func (st *convState) queuedCount() int {
	n := 0
	for _, m := range st.messages {
		if m.state == "queued" && !m.replied {
			n++
		}
	}
	return n
}

func (st *convState) oldestQueued() *convMessage {
	for _, m := range st.messages {
		if m.state == "queued" && !m.replied {
			return m
		}
	}
	return nil
}

// lineOf 记录归属的会话线(主对话 "" / 成果追问 "qa:<claimID>")。
func lineOf(rec rings.ShepherdRecord) string {
	if rec.ClaimID == "" {
		return ""
	}
	return qaLine(rec.ClaimID)
}

// lineKeyOf 视图归属的会话线键(主对话 claimID 为空)。
func lineKeyOf(claimID string) string {
	if claimID == "" {
		return ""
	}
	return qaLine(claimID)
}

// linesState 从事件流重建全部会话线的状态(P4-14 起主对话与各成果追问
// 线同表分键;消息送达态与建议裁决态都是"最新事件覆盖"读法,重启后
// 零内存依赖)。追问线的消息跨轮聚合(轮是会话边界,应答时按消息自身
// 的 round 换会话)。
func (s *Service) linesState(ctx context.Context, taskID string) (map[string]*convState, error) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]*convState{}
	stateOf := func(line string) *convState {
		st, ok := out[line]
		if !ok {
			st = &convState{byID: map[string]*convMessage{}}
			out[line] = st
		}
		return st
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) != nil {
			continue
		}
		st := stateOf(lineOf(rec))
		switch rec.Type {
		case rings.ShepherdRecUserMessage, rings.ShepherdRecSystemNote:
			m := &convMessage{id: rec.MessageID, text: rec.Text, state: "queued",
				system:  rec.Type == rings.ShepherdRecSystemNote,
				claimID: rec.ClaimID, round: rec.Round, prelude: rec.Prelude}
			st.messages = append(st.messages, m)
			st.byID[m.id] = m
		case rings.ShepherdRecMessageState:
			if m, ok := st.byID[rec.MessageID]; ok {
				m.state, m.note = rec.State, rec.Note
			}
		case rings.ShepherdRecReply:
			if m, ok := st.byID[rec.MessageID]; ok {
				m.replied = true
			}
		}
	}
	return out, nil
}

// lineState 单条会话线的状态(空 = 主对话)。
func (s *Service) lineState(ctx context.Context, taskID, line string) (*convState, error) {
	lines, err := s.linesState(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return lines[line], nil
}

// View 牧者主对话视图(消息聚合 + 建议状态 + 运行态;成果追问记录
// 不混入——独立留存,互不污染)。
func (s *Service) View(ctx context.Context, taskID string) (*View, error) {
	return s.viewOf(ctx, taskID, func(rec rings.ShepherdRecord) bool {
		return rec.ClaimID == ""
	}, "")
}

// ViewClaim 成果追问会话视图(P4-14):claimID 的 round 轮会话
// (round=0 取最新轮)。轮次与问数随视图携带(入口按钮与会话页的
// 数据面)。
func (s *Service) ViewClaim(ctx context.Context, taskID, claimID string, round int) (*View, error) {
	sessions, err := s.claimQASessions(ctx, taskID)
	if err != nil {
		return nil, err
	}
	rounds, latest := 0, 0
	for _, x := range sessions {
		if x.ClaimID != claimID {
			continue
		}
		rounds++
		if x.Round > latest {
			latest = x.Round
		}
	}
	if round <= 0 {
		round = latest
	}
	finalRound := round
	v, err := s.viewOf(ctx, taskID, func(rec rings.ShepherdRecord) bool {
		return rec.ClaimID == claimID && rec.Round == finalRound
	}, claimID)
	if err != nil {
		return nil, err
	}
	v.Round, v.Rounds = round, rounds
	for _, m := range v.Messages {
		if !m.System {
			v.Questions++
		}
	}
	return v, nil
}

// viewOf 事件流一遍扫描的视图装配(match 命中的记录进本视图;claimID
// 空 = 主对话——运行态 busy 按对应工作线读)。
func (s *Service) viewOf(ctx context.Context, taskID string, match func(rings.ShepherdRecord) bool, claimID string) (*View, error) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, err
	}
	v := &View{Messages: []MessageView{}, Advices: []AdviceView{}, ProviderOn: s.Provider != nil}
	advIdx := map[string]int{}
	msgIdx := map[string]int{}
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || !match(rec) {
			continue
		}
		at := ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
		if v.StartedAt == "" {
			v.StartedAt = at
		}
		switch rec.Type {
		case rings.ShepherdRecUserMessage, rings.ShepherdRecSystemNote:
			v.Messages = append(v.Messages, MessageView{
				ID: rec.MessageID, Text: rec.Text, State: "queued", At: at,
				System: rec.Type == rings.ShepherdRecSystemNote,
			})
			msgIdx[rec.MessageID] = len(v.Messages) - 1
		case rings.ShepherdRecMessageState:
			if i, ok := msgIdx[rec.MessageID]; ok {
				// 回复是送达态的终态:已回复的消息不再被后续状态事件
				// 覆盖(历史误标事件在读面自愈,不再需要数据修补)。
				if v.Messages[i].State != "replied" {
					v.Messages[i].State, v.Messages[i].Note = rec.State, rec.Note
				}
			}
		case rings.ShepherdRecReply:
			if i, ok := msgIdx[rec.MessageID]; ok {
				v.Messages[i].Reply, v.Messages[i].ReplyAt = rec.Text, at
				v.Messages[i].State = "replied" // 回复即送达态终态
			}
		case rings.ShepherdRecThinking:
			if i, ok := msgIdx[rec.MessageID]; ok {
				v.Messages[i].Thinking = rec.Text
			}
		case rings.ShepherdRecToolCall:
			if i, ok := msgIdx[rec.MessageID]; ok {
				v.Messages[i].ToolCalls = append(v.Messages[i].ToolCalls, ToolCallView{
					Name: rec.ToolName, Args: string(rec.ToolArgs), IsError: rec.IsError,
					ResultHead: rec.ResultHead, At: at,
				})
			}
		case rings.ShepherdRecAdvice:
			av := AdviceView{ID: rec.AdviceID, Text: rec.Text, State: rings.ShepherdAdvicePending, At: at}
			if i, ok := msgIdx[rec.MessageID]; ok {
				// 锚定消息:建议回到对话流原位(牧者在哪条消息里提的,卡片就在哪)。
				v.Messages[i].Advices = append(v.Messages[i].Advices, av)
				advIdx[rec.AdviceID] = -(i + 2) // 负值编码 = 消息内联位
			} else {
				v.Advices = append(v.Advices, av) // 无锚(旧事件):顶层列表,流程尾部呈现
				advIdx[rec.AdviceID] = len(v.Advices) - 1
			}
		case rings.ShepherdRecAdviceVerdict:
			if i, ok := advIdx[rec.AdviceID]; ok {
				if i < 0 {
					mi := -i - 2
					for j := range v.Messages[mi].Advices {
						if v.Messages[mi].Advices[j].ID == rec.AdviceID {
							v.Messages[mi].Advices[j].State, v.Messages[mi].Advices[j].Reason = rec.State, rec.Reason
						}
					}
				} else {
					v.Advices[i].State, v.Advices[i].Reason = rec.State, rec.Reason
				}
			}
		}
	}
	s.mu.Lock()
	if c, ok := s.convs[convKey(taskID, lineKeyOf(claimID))]; ok && c.busy {
		v.Busy = true
	}
	s.mu.Unlock()
	return v, nil
}

// ---- 小件 ----

// ClaimQASession 一轮成果追问会话的摘要(P4-14;发现详情入口按钮与
// 会话页牧者组列表的数据面,标题由 server 层从声明节点补)。
type ClaimQASession struct {
	ClaimID   string `json:"claim_id"`
	Round     int    `json:"round"`
	Questions int    `json:"questions"`
	StartedAt string `json:"started_at,omitempty"`
	LastAt    string `json:"last_at,omitempty"`
}

// claimQASessions 全部成果追问会话(每 (成果 × 轮) 一条,按轮次升序;
// 事件流一遍扫描,重启后零内存依赖)。
func (s *Service) claimQASessions(ctx context.Context, taskID string) ([]ClaimQASession, error) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, err
	}
	type qaAgg struct {
		questions int
		startedAt string
		lastAt    string
	}
	idx := map[[2]string]*ClaimQASession{}
	agg := map[[2]string]*qaAgg{}
	order := [][2]string{}
	get := func(claimID string, round int) (*ClaimQASession, *qaAgg) {
		k := [2]string{claimID, fmt.Sprintf("%d", round)}
		if _, ok := idx[k]; !ok {
			idx[k] = &ClaimQASession{ClaimID: claimID, Round: round}
			agg[k] = &qaAgg{}
			order = append(order, k)
		}
		return idx[k], agg[k]
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventShepherdRecord {
			continue
		}
		var rec rings.ShepherdRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.ClaimID == "" || rec.Round <= 0 {
			continue
		}
		if rec.Type != rings.ShepherdRecUserMessage {
			continue
		}
		_, a := get(rec.ClaimID, rec.Round)
		at := ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
		a.questions++
		if a.startedAt == "" {
			a.startedAt = at
		}
		a.lastAt = at
	}
	out := make([]ClaimQASession, 0, len(order))
	for _, k := range order {
		x, a := idx[k], agg[k]
		x.Questions, x.StartedAt, x.LastAt = a.questions, a.startedAt, a.lastAt
		out = append(out, *x)
	}
	return out, nil
}

// ClaimQASessions 全部成果追问会话(server 层的列表数据面)。
func (s *Service) ClaimQASessions(ctx context.Context, taskID string) ([]ClaimQASession, error) {
	return s.claimQASessions(ctx, taskID)
}

func flattenBlocks(bs []llm.ContentBlock) string {
	var b strings.Builder
	for _, blk := range bs {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func clipHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
