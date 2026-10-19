package interject

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ActorInterject 插话子会话的账本 actor 前缀(token 表/事件归属)。
const ActorInterject = "interject"

// TargetOvermind 插话目标:主宰循环。
const TargetOvermind = "overmind"

// Service 插话子会话服务(P2-08;P2-08f 问轻量化为快照直问):每
// (任务 × 目标)一条串行工作线——问(ask)入账即返,后台以目标最近
// 一次请求的上下文快照单次直问模型(不建会话、不打断主任务);改
// (steer)对虫群经舵杆、对主宰落建议,回执/裁决状态经读视图汇合。
// 零值不可用。
type Service struct {
	Store    *rings.Store
	Provider llm.Provider // nil = 未配模型(问会落失败态,改不受影响)
	RootDir  string       // 任务工作目录根(读目标转录构建快照)
	Model    string
	ModelOf  func() string
	// Helm 舵杆(改→对虫群注入;装配层与执行器共持同一实例)。
	Helm *swarm.Helm
	// Hiber 任务冬眠门:冬眠中插话入口冻结(面板纯只读;插话事件也
	// 会推进图版本,冻结保护唤醒后的 R14 复检锚点)。
	Hiber *hiber.Gate
	// Wake 任务唤醒(对主宰的建议入账后通知其裁决;装配层注入
	// Manager 的 Task.Wake 路径)。nil = 不唤醒(单测)。
	Wake func(taskID string)

	TurnSeconds int // 单次直问的墙钟上限;零值 AskSecondsDefault(120)
	MaxTokens   int // 单回复输出上限;零值 4096
	// MaxInFlight 进程内快照直问的并发上限;零值 2。每(任务×目标)
	// 工作线自身串行(每会话并发 1),本上限约束全部工作线合计。
	MaxInFlight int

	mu       sync.Mutex
	convs    map[string]*conversation
	askSlots chan struct{}
}

// conversation 一个(任务 × 目标)的会话工作线(busy 时排队应答)。
type conversation struct {
	kick chan struct{}
	busy bool
}

func (s *Service) turnSeconds() int {
	if s.TurnSeconds > 0 {
		return s.TurnSeconds
	}
	return AskSecondsDefault
}

func (s *Service) maxTokens() int {
	if s.MaxTokens > 0 {
		return s.MaxTokens
	}
	return 4096
}

// askSem 进程级并发信号量(懒建;容量 = MaxInFlight)。
func (s *Service) askSem() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.askSlots == nil {
		n := s.MaxInFlight
		if n <= 0 {
			n = 2
		}
		s.askSlots = make(chan struct{}, n)
	}
	return s.askSlots
}

// SetHelm 运行时接入舵杆(P3-05b ⑤ 模型激活免重启:装配层拉起执行器
// 后换入同一实例);helm 为持锁读侧(Enqueue 改向路径用)。
func (s *Service) SetHelm(h *swarm.Helm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Helm = h
}

func (s *Service) helm() *swarm.Helm {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Helm
}

func (s *Service) hibernating(taskID string) bool {
	return s.Hiber != nil && s.Hiber.Active(taskID)
}

// ErrHibernated 任务冬眠中的统一拒绝文案(入口提示"任务冬眠中")。
var ErrHibernated = errors.New("interject: 任务冬眠中,插话入口已冻结(唤醒后继续)")

// shortMsgID 生成插话消息标识(im-*)。
func shortMsgID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("im-%d", time.Now().UnixNano())
	}
	return "im-" + hex.EncodeToString(b[:])
}

// shortAdviceID 生成建议标识(av-*,与牧者建议同前缀同语义)。
func shortAdviceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("av-%d", time.Now().UnixNano())
	}
	return "av-" + hex.EncodeToString(b[:])
}

// sessionKey 子会话的稳定键:主宰 = "overmind";单位 = 其会话锚点
// (transcript_id,跨进程不变——单位序号 uN 每进程重排,转录 ID 才稳)。
func sessionKey(target, sessionID string) string {
	if target == TargetOvermind {
		return TargetOvermind
	}
	return "u:" + sessionID
}

// transcriptID 插话问话的用量记账锚(独立计量标签;P2-08f 起快照直问
// 不建会话、不落转录文件,token 行按此标签与目标用量分开对账)。
func transcriptID(key string) string {
	return "interject-" + strings.ReplaceAll(key, ":", "-")
}

// agentName token 记账归属(interject:<key>;面板按此前缀过滤求和)。
func agentName(key string) string { return ActorInterject + ":" + key }

// appendRec 受控入口记账(审计流,R7)。
func (s *Service) appendRec(ctx context.Context, taskID string, actor string, rec rings.InterjectRecord) error {
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventInterjectRecord, Actor: actor, Payload: rec,
	}); err != nil {
		return fmt.Errorf("interject: 记账: %w", err)
	}
	return nil
}

// ---- 入队 ----

// Enqueue 插话发言入队:mode=ask(问,不改执行即时答)或 mode=steer
// (改:对虫群=舵杆注入带回执;对主宰=建议进正式规划)。
// 问返回消息 ID(异步应答);改返回消息 ID 与关联的 steerID/adviceID。
func (s *Service) Enqueue(ctx context.Context, taskID, target, unitID, stepID, sessionID, mode, text string) (string, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", errors.New("interject: 发言不能为空")
	}
	if mode != "ask" && mode != "steer" {
		return "", "", fmt.Errorf("interject: 未知模式 %s(ask|steer)", mode)
	}
	if s.hibernating(taskID) {
		return "", "", ErrHibernated
	}
	rec := rings.InterjectRecord{Target: target, UnitID: unitID, StepID: stepID, SessionID: sessionID,
		MessageID: shortMsgID(), Type: rings.InterjectRecUserMessage, Text: text, Mode: mode}
	if mode == "ask" {
		rev, err := s.Store.CurrentRevision(ctx, taskID)
		if err == nil {
			rec.GraphVersion = rev // 子会话绑定提问时图版本
		}
		if err := s.appendRec(ctx, taskID, "creator", rec); err != nil {
			return "", "", err
		}
		s.kick(taskID, sessionKey(target, sessionID))
		return rec.MessageID, "", nil
	}
	// ---- 改向 ----
	refID := ""
	if target == TargetOvermind {
		refID = shortAdviceID()
		if err := s.appendRec(ctx, taskID, "creator", rec); err != nil {
			return "", "", err
		}
		if err := s.appendRec(ctx, taskID, "creator", rings.InterjectRecord{
			Target: target, MessageID: rec.MessageID, Type: rings.InterjectRecAdvice,
			AdviceID: refID, Text: text,
		}); err != nil {
			return "", "", err
		}
		if s.Wake != nil {
			s.wake(taskID) // 建议入账,唤醒主宰下轮裁决
		}
		return rec.MessageID, refID, nil
	}
	// 对虫群:舵杆注入(步骤须在跑;回执状态机 已排队→已送达→…)。
	helm := s.helm()
	if helm == nil {
		return "", "", errors.New("interject: 舵杆未装配(未配模型),改向不可用")
	}
	if stepID == "" {
		return "", "", errors.New("interject: 单位目标缺 step_id")
	}
	steerID, err := helm.SteerWork(ctx, taskID, stepID, text, "creator")
	if err != nil {
		return "", "", err
	}
	refID = steerID
	if err := s.appendRec(ctx, taskID, "creator", rec); err != nil {
		return "", "", err
	}
	if err := s.appendRec(ctx, taskID, "creator", rings.InterjectRecord{
		Target: target, UnitID: unitID, StepID: stepID, SessionID: sessionID,
		MessageID: rec.MessageID, Type: rings.InterjectRecSteer, SteerID: steerID, Text: text,
	}); err != nil {
		return "", "", err
	}
	return rec.MessageID, refID, nil
}

func (s *Service) wake(taskID string) {
	if s.Wake != nil {
		s.Wake(taskID)
	}
}

// kickTask 唤起(任务 × 目标)会话工作线(串行应答)。
func (s *Service) kick(taskID, key string) {
	s.mu.Lock()
	if s.convs == nil {
		s.convs = map[string]*conversation{}
	}
	ck := taskID + "|" + key
	c, ok := s.convs[ck]
	if !ok {
		c = &conversation{kick: make(chan struct{}, 1)}
		s.convs[ck] = c
	}
	if !c.busy {
		c.busy = true
		go s.worker(context.Background(), taskID, key, c)
	}
	s.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// worker 逐条应答队列中的问话(改向不进工作线——回执在舵杆/裁决线),
// 空则退出;退出前复查防缝隙。冬眠中挂起在门上(冻结),唤醒后续答。
func (s *Service) worker(ctx context.Context, taskID, key string, c *conversation) {
	defer func() {
		s.mu.Lock()
		c.busy = false
		s.mu.Unlock()
		if st, err := s.state(ctx, taskID, key); err == nil && st.queuedCount() > 0 {
			s.kick(taskID, key)
		}
	}()
	for {
		if s.hibernating(taskID) {
			if s.Hiber == nil || !s.Hiber.Wait(ctx, taskID) {
				return
			}
		}
		st, err := s.state(ctx, taskID, key)
		if err != nil {
			return
		}
		m := st.oldestQueued()
		if m == nil {
			return
		}
		s.answer(ctx, taskID, key, m.id, m.text)
	}
}

// answer 应答一条问话(快照直问,P2-08f):目标最近一次请求的不可变
// 上下文快照(身份/系统提示/工具目录/消息重放)+ 此前问答(追问链)
// + 问题,单次 Complete 直问模型——不建会话、不进目标 transcript、
// 单次限时;进程并发受 MaxInFlight 约束(每目标工作线自身串行)。
// 用量独立记账(agent=interject:<key>,信息条同源)。
func (s *Service) answer(ctx context.Context, taskID, key, messageID, question string) {
	target := TargetOvermind
	var sessionID string
	if strings.HasPrefix(key, "u:") {
		sessionID = strings.TrimPrefix(key, "u:")
		target = "unit"
	}
	fail := func(note string) {
		_ = s.appendRec(context.Background(), taskID, ActorInterject, rings.InterjectRecord{
			Target: target, SessionID: sessionID, MessageID: messageID,
			Type: rings.InterjectRecMessageState, State: rings.InterjectMsgFailed, Note: note,
		})
	}
	if s.Provider == nil {
		fail("模型未配置(LLM配置页激活模型后重发)")
		return
	}
	snapshot, serr := s.buildSnapshot(ctx, taskID, target, sessionID)
	if serr != nil {
		fail("目标快照装载失败: " + serr.Error())
		return
	}

	var packet strings.Builder
	packet.WriteString(snapshot)
	if prior := s.priorQA(ctx, taskID, target, sessionID, messageID); prior != "" {
		packet.WriteString("\n")
		packet.WriteString(prior)
	}
	packet.WriteString("\n【造物主的插话】")
	packet.WriteString(question)

	sem := s.askSem()
	sem <- struct{}{} // 进程并发上限(排队等待;每目标工作线已串行)
	defer func() { <-sem }()
	cctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.turnSeconds())*time.Second)
	defer cancel()

	usage := &swarm.UsageTracker{
		Store: s.Store, TaskID: taskID, Agent: agentName(key),
		Model: s.Model, ModelOf: s.ModelOf, Transcript: transcriptID(key),
	}
	_ = s.appendRec(context.Background(), taskID, ActorInterject, rings.InterjectRecord{
		Target: target, SessionID: sessionID, MessageID: messageID,
		Type: rings.InterjectRecMessageState, State: rings.InterjectMsgDelivered,
	})
	msg, _, u, err := s.Provider.Complete(cctx, llm.CompletionRequest{
		System: InterjectPromptSpec.Assemble(ctx, s.Store),
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: packet.String()}}},
		},
		MaxTokens: s.maxTokens(),
	})
	if err != nil {
		fail("应答中断: " + clip(err.Error(), 300))
		return
	}
	_ = usage.OnUsage(u)
	replyText := strings.TrimSpace(msg.Text())
	if replyText == "" {
		fail("回复为空(模型未产出正文;可重发)")
		return
	}
	_ = s.appendRec(context.Background(), taskID, ActorInterject, rings.InterjectRecord{
		Target: target, SessionID: sessionID, MessageID: messageID,
		Type: rings.InterjectRecReply, Text: clip(replyText, 16000),
	})
}

// clip 截头(超长加省略号)。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
