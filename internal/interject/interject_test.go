package interject

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- 进程内脚本模型(OpenAI 兼容 SSE;与 shepherd/swarm 测试同构) ----

type scriptedModel struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recordedReq
	script func(rr *recordedReq) string
	// delay 每次请求的处理延迟(并发上限测试用);starts/ends 记录
	// 每次请求的起止时刻(重叠检测)。
	delay time.Duration
	spans [][2]time.Time
}

func (m *scriptedModel) spansSnapshot() [][2]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][2]time.Time(nil), m.spans...)
}

type recordedReq struct {
	Index    int
	System   string
	LastUser string
}

func newScriptedModel(script func(rr *recordedReq) string) *scriptedModel {
	m := &scriptedModel{script: script}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *scriptedModel) Close() { m.srv.Close() }

func (m *scriptedModel) provider() (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: m.srv.URL,
		APIKey: "test-key", Model: "scripted-interject-model",
	})
}

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (m *scriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []oaMessage `json:"messages"`
		Stream   bool        `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rr := recordedReq{}
	for _, msg := range body.Messages {
		if msg.Role == "system" {
			rr.System += msg.Content
		} else if msg.Role == "user" {
			rr.LastUser = msg.Content
		}
	}
	start := time.Now()
	m.mu.Lock()
	rr.Index = len(m.reqs)
	m.reqs = append(m.reqs, rr)
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	text := m.script(&rr)
	m.mu.Lock()
	m.spans = append(m.spans, [2]time.Time{start, time.Now()})
	m.mu.Unlock()
	if body.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"content": text}}}}))
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{}, "finish_reason": "stop"}}}))
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"choices": []any{}, "usage": map[string]any{
			"prompt_tokens": 100, "completion_tokens": 20}}))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	// 非流式(P2-08f 快照直问走 Complete):一次性完整 JSON 响应。
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`, strconv.Quote(text))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (m *scriptedModel) requests() []recordedReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedReq(nil), m.reqs...)
}

// ---- 测试装配 ----

type ijEnv struct {
	store *rings.Store
	svc   *Service
	gate  *hiber.Gate
	dir   string
	model *scriptedModel
}

func newIJEnv(t *testing.T, script func(rr *recordedReq) string) *ijEnv {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	model := newScriptedModel(script)
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	gate := hiber.New()
	svc := &Service{
		Store: store, Provider: prov, RootDir: dir,
		Helm: swarm.NewHelm(store), Hiber: gate,
	}
	return &ijEnv{store: store, svc: svc, gate: gate, dir: dir, model: model}
}

// seedUnitTask 建任务 + 一只已结束单位(s1,会话 swarm-u1)+ 意图。
func (e *ijEnv) seedUnitTask(t *testing.T, task string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "插话验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "探测登录页", Status: "done"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd,
				Note: "reason=completed turns=3 status=done billable=5.0s gate_wait=0.0s block_wait=0.0s pause_wait=0.0s"}},
	); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
}

func waitReply(t *testing.T, e *ijEnv, task, target, session string, deadline time.Duration) *View {
	t.Helper()
	start := time.Now()
	for time.Since(start) < deadline {
		v, err := e.svc.View(context.Background(), task, target, session)
		if err == nil {
			for _, m := range v.Messages {
				if m.Mode == "ask" && m.Reply != "" {
					return v
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待插话回答超时")
	return nil
}

// TestAskEndedUnitAnswersFromHistory 问(单位已结束):基于历史回答——
// 上下文含单位终因,回答声明其已结束;不改执行(无工具,问不进单位
// 上下文)。
func TestAskEndedUnitAnswersFromHistory(t *testing.T) {
	var gotPrompt, gotSystem string
	e := newIJEnv(t, func(rr *recordedReq) string {
		gotPrompt, gotSystem = rr.LastUser, rr.System
		return "该单位已结束:探测完成,发现登录页开放。"
	})
	const task = "ij-ask"
	e.seedUnitTask(t, task)

	msgID, ref, err := e.svc.Enqueue(context.Background(), task, "unit", "u1", "s1", "swarm-u1", "ask", "它做了什么?")
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if ref != "" || msgID == "" {
		t.Fatalf("问不应关联 ref: %q %q", msgID, ref)
	}
	v := waitReply(t, e, task, "unit", "swarm-u1", 5*time.Second)
	if len(v.Messages) != 1 || v.Messages[0].State != "replied" {
		t.Fatalf("应恰一条已回复消息: %+v", v.Messages)
	}
	if !strings.Contains(v.Messages[0].Reply, "已结束") {
		t.Fatalf("回答应基于历史(声明已结束): %s", v.Messages[0].Reply)
	}
	// 提示含目标上下文(步骤、单位、终因);system 为三段式(角色+纪律+文风)。
	if !strings.Contains(gotPrompt, "swarm-u1") || !strings.Contains(gotPrompt, "已结束") {
		t.Fatalf("上下文应含单位会话锚与终态: %s", gotPrompt)
	}
	if !strings.Contains(gotSystem, "插话子会话") || !strings.Contains(gotSystem, "输出文风(代码固定") {
		t.Fatalf("system 应为三段式(角色+纪律+文风): %s", gotSystem)
	}
}

// TestSteerToUnitRoutesHelm 改(对虫群):经舵杆排队,回执状态机汇合
// (queued);对已结束单位改向如实报错(无生效点)。
func TestSteerToUnitRoutesHelm(t *testing.T) {
	e := newIJEnv(t, func(rr *recordedReq) string { return "unused" })
	const task = "ij-steer"
	// 造一只在跑单位(s1 running)。
	ctx := context.Background()
	if _, err := e.store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "改向验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "在跑探测", Status: "running"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2",
				Type: rings.SwarmRecSessionStarted}},
	); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	msgID, ref, err := e.svc.Enqueue(ctx, task, "unit", "u2", "s1", "swarm-u2", "steer", "改查 22 端口")
	if err != nil {
		t.Fatalf("改向失败: %v", err)
	}
	if !strings.HasPrefix(ref, "st-") {
		t.Fatalf("改向应关联舵杆指令: %q", ref)
	}
	v, err := e.svc.View(ctx, task, "unit", "swarm-u2")
	if err != nil {
		t.Fatalf("读视图失败: %v", err)
	}
	if len(v.Messages) != 1 || v.Messages[0].RefID != ref || v.Messages[0].RefKind != "steer" {
		t.Fatalf("改向消息应关联回执: %+v", v.Messages)
	}
	if len(v.Steers) != 1 || v.Steers[0].State != rings.SteerStateQueued {
		t.Fatalf("回执应为已排队: %+v", v.Steers)
	}
	_ = msgID

	// 已结束单位改向:如实报错。
	e.seedUnitTask(t, "ij-steer-done")
	if _, _, err := e.svc.Enqueue(ctx, "ij-steer-done", "unit", "u1", "s1", "swarm-u1", "steer", "改"); err == nil {
		t.Fatalf("对已结束单位改向应报错(无生效点)")
	}
}

// TestAdviceToOvermindAndVerdict 改(对主宰):建议落 interject.record
// pending,唤醒触发;裁决(模拟主宰)写回同源事件,消息状态原地更新。
func TestAdviceToOvermindAndVerdict(t *testing.T) {
	wakes := 0
	e := newIJEnv(t, func(rr *recordedReq) string { return "unused" })
	e.svc.Wake = func(string) { wakes++ }
	const task = "ij-adv"
	if _, err := e.store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "建议验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	_, advID, err := e.svc.Enqueue(context.Background(), task, TargetOvermind, "", "", "", "steer", "优先验证上传接口")
	if err != nil {
		t.Fatalf("建议失败: %v", err)
	}
	if !strings.HasPrefix(advID, "av-") {
		t.Fatalf("应关联建议 ID: %q", advID)
	}
	if wakes != 1 {
		t.Fatalf("建议入账应唤醒主宰: %d", wakes)
	}
	// 模拟主宰裁决(采纳,带理由)。
	if _, err := e.store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventInterjectRecord, Actor: "overmind",
		Payload: rings.InterjectRecord{Target: TargetOvermind, Type: rings.InterjectRecAdviceVerdict,
			AdviceID: advID, State: rings.ShepherdAdviceAdopted, Reason: "图上未覆盖上传面,本轮派发验证"},
	}); err != nil {
		t.Fatalf("裁决入账失败: %v", err)
	}
	v, err := e.svc.View(context.Background(), task, TargetOvermind, "")
	if err != nil {
		t.Fatalf("读视图失败: %v", err)
	}
	if len(v.Messages) != 1 {
		t.Fatalf("应恰一条改向消息: %+v", v.Messages)
	}
	m := v.Messages[0]
	if m.AdvState != rings.ShepherdAdviceAdopted || !strings.Contains(m.AdvReason, "上传") {
		t.Fatalf("裁决应原地更新: %+v", m)
	}
}

// TestInterjectFrozenDuringHibernation 冬眠中插话入口冻结(ErrHibernated)。
func TestInterjectFrozenDuringHibernation(t *testing.T) {
	e := newIJEnv(t, func(rr *recordedReq) string { return "unused" })
	const task = "ij-frozen"
	if _, err := e.store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "冻结验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	e.gate.Enter(task, "started")
	if _, _, err := e.svc.Enqueue(context.Background(), task, TargetOvermind, "", "", "", "ask", "在吗"); err == nil {
		t.Fatalf("冬眠中问应被拒")
	} else if !strings.Contains(err.Error(), "冬眠") {
		t.Fatalf("拒绝文案应提示冬眠: %v", err)
	}
	v, err := e.svc.View(context.Background(), task, TargetOvermind, "")
	if err != nil || !v.Frozen {
		t.Fatalf("视图应标记冻结: %+v %v", v, err)
	}
}

// seedUnitTranscript 造单位的转录 JSONL(任务包输入 + 一轮工具调用)。
func seedUnitTranscript(t *testing.T, dir, task, sessionID string) {
	t.Helper()
	ts := transcript.NewStore(filepath.Join(dir, task, "transcripts"))
	mk := func(uuid, ts2 string, role llm.Role, blocks []llm.ContentBlock) transcript.Record {
		return transcript.Record{UUID: uuid, SessionID: sessionID, Timestamp: ts2, Type: "message",
			Message: &llm.Message{Role: role, Content: blocks}}
	}
	recs := []transcript.Record{
		mk("r1", "2026-09-30T01:00:00Z", "user", []llm.ContentBlock{
			{Type: llm.BlockText, Text: "【任务包 · 七要素】\n任务 " + task + " / 步骤 s1\n二、本步骤意图(只做这一条)\n  探测登录页并提交候选"},
		}),
		mk("r2", "2026-09-30T01:00:01Z", "assistant", []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "b1", Name: "http_probe", Input: []byte(`{"url":"http://t/login"}`)},
		}),
		mk("r3", "2026-09-30T01:00:02Z", "user", []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: "b1", Content: []llm.ContentBlock{
				{Type: llm.BlockText, Text: "url=http://t/login method=GET status=200"},
			}},
		}),
	}
	if err := ts.Append(ts.MainPath(sessionID), recs...); err != nil {
		t.Fatalf("造转录失败: %v", err)
	}
}

// TestAskSnapshotDirectAsk P2-08f 快照直问:问话不建会话(零转录文件)、
// 不进目标 transcript(目标转录内容原样未动);请求携带目标快照
// (身份/系统提示重建/工具目录/消息重放);单问单次 Complete;
// token 行独立记账(agent=interject:<key>)。
func TestAskSnapshotDirectAsk(t *testing.T) {
	var gotPrompt, gotSystem string
	e := newIJEnv(t, func(rr *recordedReq) string {
		gotPrompt, gotSystem = rr.LastUser, rr.System
		return "该单位已结束:探测过登录页(200)。"
	})
	const task = "ij-snap"
	e.seedUnitTask(t, task)
	seedUnitTranscript(t, e.dir, task, "swarm-u1")
	before, _ := os.ReadFile(filepath.Join(e.dir, task, "transcripts", "swarm-u1.jsonl"))

	if _, _, err := e.svc.Enqueue(context.Background(), task, "unit", "u1", "s1", "swarm-u1", "ask", "它做了什么探测?"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	v := waitReply(t, e, task, "unit", "swarm-u1", 5*time.Second)
	if v.Messages[0].State != "replied" {
		t.Fatalf("应已回复: %+v", v.Messages[0])
	}
	// 不建会话:插话自身零转录文件(interject-* 不存在)。
	ents, _ := os.ReadDir(filepath.Join(e.dir, task, "transcripts"))
	for _, x := range ents {
		if strings.HasPrefix(x.Name(), "interject-") {
			t.Fatalf("快照直问不得创建会话转录: %s", x.Name())
		}
	}
	// 不进目标 transcript:目标转录逐字节未动。
	after, _ := os.ReadFile(filepath.Join(e.dir, task, "transcripts", "swarm-u1.jsonl"))
	if string(before) != string(after) {
		t.Fatalf("问话不得写目标 transcript")
	}
	// 快照要素:身份(已结束声明)、系统提示重建(RoE 约束块 + 虫群基线)、
	// 工具目录、消息重放(任务包/工具调用/结果)、问题本身。
	for _, want := range []string{
		"已结束", "【目标的系统提示", "【授权约束 · RoE】", "渗透测试虫群单位",
		"【目标的可用工具", "list_claims", "【会话消息重放", "任务包 · 七要素", "http_probe",
		"【造物主的插话】它做了什么探测?",
	} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("快照缺要素 %q:\n%s", want, gotPrompt)
		}
	}
	if !strings.Contains(gotSystem, "插话子会话") || !strings.Contains(gotSystem, "输出文风(代码固定") {
		t.Fatalf("system 应为三段式: %s", gotSystem)
	}
	// 单问单次请求。
	if reqs := len(e.model.requests()); reqs != 1 {
		t.Fatalf("快照直问应恰一次模型请求,得到 %d", reqs)
	}
	// token 行独立记账。
	rows, err := e.store.TokenUsageRows(context.Background(), task)
	if err != nil {
		t.Fatalf("读 token 行: %v", err)
	}
	found := false
	for _, x := range rows {
		if x.Agent == "interject:u:swarm-u1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应落独立用量记账行(agent=interject:u:swarm-u1): %+v", rows)
	}
}

// TestAskFollowUpChainsPriorQA 追问支持:第二次问话的请求回放第一次
// 问答(链在提示里,不建会话)。
func TestAskFollowUpChainsPriorQA(t *testing.T) {
	e := newIJEnv(t, func(rr *recordedReq) string {
		return "第一答:做了端口探测。"
	})
	const task = "ij-follow"
	e.seedUnitTask(t, task)
	seedUnitTranscript(t, e.dir, task, "swarm-u1")

	q := func(text string) {
		t.Helper()
		if _, _, err := e.svc.Enqueue(context.Background(), task, "unit", "u1", "s1", "swarm-u1", "ask", text); err != nil {
			t.Fatalf("入队失败: %v", err)
		}
	}
	q("它做了什么?")
	waitReply(t, e, task, "unit", "swarm-u1", 5*time.Second)
	q("那结果呢?")
	deadline := time.Now().Add(5 * time.Second)
	var second recordedReq
	for time.Now().Before(deadline) {
		reqs := e.model.requests()
		if len(reqs) >= 2 {
			second = reqs[1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second.LastUser == "" {
		t.Fatalf("应有第二次请求")
	}
	if !strings.Contains(second.LastUser, "【此前的插话问答") ||
		!strings.Contains(second.LastUser, "它做了什么?") ||
		!strings.Contains(second.LastUser, "第一答") {
		t.Fatalf("追问请求应回放此前问答: %s", second.LastUser)
	}
}

// TestAskProcessConcurrencyCap 进程并发上限:MaxInFlight=1 时两个不同
// 目标的问话不重叠(第二个排队等第一个完成)。
func TestAskProcessConcurrencyCap(t *testing.T) {
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	model := newScriptedModel(func(rr *recordedReq) string { return "答。" })
	model.delay = 250 * time.Millisecond
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	svc := &Service{Store: store, Provider: prov, RootDir: dir,
		Helm: swarm.NewHelm(store), Hiber: hiber.New(), MaxInFlight: 1}

	for _, task := range []string{"ij-cap-a", "ij-cap-b"} {
		e := &ijEnv{store: store, svc: svc, dir: dir, gate: nil}
		e.seedUnitTask(t, task)
		seedUnitTranscript(t, dir, task, "swarm-u1")
		if _, _, err := svc.Enqueue(context.Background(), task, "unit", "u1", "s1", "swarm-u1", "ask", "在吗"); err != nil {
			t.Fatalf("入队失败: %v", err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(model.spansSnapshot()) == 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	spans := model.spansSnapshot()
	if len(spans) != 2 {
		t.Fatalf("应恰两次请求,得到 %d", len(spans))
	}
	if spans[0][1].After(spans[1][0]) {
		t.Fatalf("MaxInFlight=1 时两次请求不得重叠: %v vs %v", spans[0], spans[1])
	}
}

// TestAskOvermindSnapshot 主宰目标:快照含近轮研判概览 + 主宰系统提示
// 重建 + 最近一次研判会话重放。
func TestAskOvermindSnapshot(t *testing.T) {
	var gotPrompt string
	e := newIJEnv(t, func(rr *recordedReq) string {
		gotPrompt = rr.LastUser
		return "主宰研判过一轮,派发了探测。"
	})
	const task = "ij-om"
	ctx := context.Background()
	if _, err := e.store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "主宰快照"}},
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment,
				QAccess: "无新身份", Dispatched: 2}},
	); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	// 最近一次研判会话转录(两个文件,后写的更新)。
	ts := transcript.NewStore(filepath.Join(e.dir, task, "transcripts"))
	_ = ts.Append(ts.MainPath("overmind-"+task+"-d1"), transcript.Record{
		UUID: "o1", SessionID: "overmind-" + task + "-d1", Timestamp: "2026-09-30T01:00:00Z",
		Type: "message", Message: &llm.Message{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "旧一轮态势快照"}}}})
	time.Sleep(10 * time.Millisecond) // 保证 mtime 顺序
	_ = ts.Append(ts.MainPath("overmind-"+task+"-d2"), transcript.Record{
		UUID: "o2", SessionID: "overmind-" + task + "-d2", Timestamp: "2026-09-30T02:00:00Z",
		Type: "message", Message: &llm.Message{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "最新一轮态势快照"}}}})

	if _, _, err := e.svc.Enqueue(ctx, task, TargetOvermind, "", "", "", "ask", "研判到哪了?"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	waitReply(t, e, task, TargetOvermind, "", 5*time.Second)
	for _, want := range []string{
		"主宰(计划权威)", "研判 #1", "【目标的系统提示", "唯一计划权威",
		"【会话消息重放(最近一次研判请求", "overmind-" + task + "-d2", "最新一轮态势快照",
	} {
		if !strings.Contains(gotPrompt, want) {
			t.Fatalf("主宰快照缺要素 %q:\n%s", want, gotPrompt)
		}
	}
	if strings.Contains(gotPrompt, "旧一轮态势快照") {
		t.Fatalf("应只重放最近一次研判会话(d2),不得混入 d1")
	}
}
