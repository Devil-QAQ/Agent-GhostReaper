package main

// P2-06f 重启收养的二进制级 E2E(杀进程重启实测):真实构建单二进制、
// 真实 HTTP 面板、真实 SQLite 账本与任务工作目录,mock 仅为 LLM 网关
// (OpenAI 兼容 SSE)。场景:
//
//	gen1:t1/t2 运行中(虫群单位在飞挂住)→ t3/t4 排队(t4 已过整体
//	     超时线)→ SIGKILL(进程没了,单位无收场、步骤卡 running)。
//	gen2:同库同目录重启 → 启动收养固定序列 → 断言——
//	  · t1/t2 自动恢复:循环重拉、幽灵步骤重置并被重新领取,无永远转圈;
//	  · transcript 原会话续:重领单位从转录恢复原会话(模型看到崩溃前
//	    历史,收到的输入是续跑指令而非重注入任务包),同一文件续写;
//	  · t3 排队重新准入(名额释放后拉起),无并发名额死锁;
//	  · t4 已过期:重启后立即走收尾时序,终态 stopped(settled)。
//	  · 已暂停保持:无循环体路径的收养语义由 manager 包单测覆盖
//	    (TestRestartAdoptsRunningTasks);冬眠态属 P2-08,序列槽位已留。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- mock LLM 网关(OpenAI 兼容 SSE;脚本按代际与对话形态分支) ----

type mockLLM struct {
	srv  *http.Server
	addr string

	gen int // 1 = 崩溃前;2 = 重启后

	mu   sync.Mutex
	reqs []mockReq
	// holdHit:gen1 虫群第二回合决定挂住时通知(在飞信号)。
	holdHit chan struct{}
}

type mockReq struct {
	ToolsOffered int
	HasToolMsg   bool
	LastUser     string
	RawBody      string
}

var assetRe = regexp.MustCompile(`a-[0-9a-f]{8}`)

const (
	intentMarker = "P2-06F-INTENT"
	factMarker   = "P2-06F-FACT"
)

func newMockLLM(t *testing.T, gen int) *mockLLM {
	t.Helper()
	m := &mockLLM{gen: gen, holdHit: make(chan struct{}, 8)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock LLM 监听失败: %v", err)
	}
	m.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", m.handle)
	m.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	t.Cleanup(func() { _ = m.srv.Close() })
	return m
}

func (m *mockLLM) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(raw, &body)
	req := mockReq{ToolsOffered: len(body.Tools), RawBody: string(raw)}
	for _, msg := range body.Messages {
		if msg.Role == "tool" {
			req.HasToolMsg = true
		}
		if msg.Role == "user" {
			req.LastUser = msg.Content
		}
	}
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()

	var text string
	var toolCall string
	if req.ToolsOffered == 0 {
		// 拆解会话(无工具广告)。已有同名意图在图上(重启后的在飞/已派
		// 发工作)→ 空产物不新增;全新任务 → 派一条只读意图。
		if strings.Contains(string(raw), intentMarker) {
			text = `{"new_intents":[]}`
		} else {
			asset := assetRe.FindString(string(raw))
			text = fmt.Sprintf(`{"new_intents":[{"intent":"%s 对靶标域名做一次确认观察","action_level":"只读探测","covers":["%s"],"expected_observation":"域名解析可达"}]}`,
				intentMarker, asset)
		}
	} else if strings.Contains(req.LastUser, "重启收养 · 续跑") {
		text = "步骤完成:确认观察已写回,本步骤意图达成。"
	} else if !req.HasToolMsg {
		// 虫群首回合:提交候选事实(带资产锚点,收割可转正)。
		asset := assetRe.FindString(string(raw))
		toolCall = fmt.Sprintf(`[{"id":"call-1","type":"function","function":{"name":"submit_candidate_fact","arguments":"{\"statement\":\"%s 靶标域名可解析\",\"evidence\":\"dig mock.local A 127.0.0.1(命令与输出摘录)\",\"asset_id\":\"%s\"}"}}]`,
			factMarker, asset)
	} else if m.gen == 1 {
		// 崩溃前第二回合:挂住在飞(进程死亡窗口)。
		select {
		case m.holdHit <- struct{}{}:
		default:
		}
		time.Sleep(120 * time.Second)
		text = "(不应送达:进程已死)"
		return
	} else {
		text = "步骤完成。"
	}
	writeSSEReply(w, text, toolCall, len(raw))
}

// writeSSEReply 最小 OpenAI 流式回包。
func writeSSEReply(w http.ResponseWriter, text, toolCallsRaw string, bodyChars int) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if text != "" {
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
	}
	if toolCallsRaw != "" {
		var calls []map[string]any
		_ = json.Unmarshal([]byte(toolCallsRaw), &calls)
		for i, tc := range calls {
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			head := map[string]any{
				"tool_calls": []any{map[string]any{
					"index": i, "id": tc["id"], "type": "function",
					"function": map[string]any{"name": name, "arguments": ""},
				}},
			}
			send(map[string]any{"choices": []any{map[string]any{"delta": head}}})
			body := map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    i,
					"function": map[string]any{"arguments": args},
				}},
			}
			send(map[string]any{"choices": []any{map[string]any{"delta": body}}})
		}
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
	completion := len(text)/4 + 8 + len(toolCallsRaw)/4
	send(map[string]any{"choices": []any{}, "usage": map[string]any{
		"prompt_tokens": bodyChars / 4, "completion_tokens": completion}})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func (m *mockLLM) requests() []mockReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mockReq(nil), m.reqs...)
}

// ---- 二进制驱动 ----

var binOnce sync.Once
var binPath string

func buildBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		// 进程级稳定路径(-count>1 复跑时不随单个测试的 TempDir 清理消失)。
		out := filepath.Join(os.TempDir(), fmt.Sprintf("ghostreaper-e2e-%d", os.Getpid()))
		cmd := exec.Command("go", "build", "-o", out, "./cmd/ghostreaper")
		cmd.Dir = "../.."
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("构建二进制失败: %v\n%s", err, b)
		}
		binPath = out
	})
	return binPath
}

type ghostProc struct {
	cmd   *exec.Cmd
	addr  string
	token string // P3-09:面板认证 token(Bearer 调 API 用)
}

// e2e 面板认证(P3-09):所有 e2e 进程共用同一测试密码;首进程 setup,
// 后续进程(同库重启)login。包级 token 供 apiJSON 附加(测试顺序执行)。
const e2ePanelPassword = "e2e-panel-pass-1"

var e2eToken string

// authInit 对新起的进程完成认证:首启 setup / 已设密码 login,token 记
// 包级变量供 apiJSON 使用。
func authInit(t *testing.T, addr string) {
	t.Helper()
	code, b := e2ePost(t, addr, "/api/auth/setup", map[string]string{"password": e2ePanelPassword})
	if code == http.StatusConflict {
		code, b = e2ePost(t, addr, "/api/auth/login", map[string]string{"password": e2ePanelPassword})
	}
	if code != 200 {
		t.Fatalf("面板认证失败 %d: %s", code, b)
	}
	var r struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(b), &r)
	if r.Token == "" {
		t.Fatalf("认证响应缺 token: %s", b)
	}
	e2eToken = r.Token
}

// e2ePost 无认证 JSON POST(认证端点用)。
func e2ePost(t *testing.T, addr, path string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post("http://"+addr+path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, rb
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func startGhost(t *testing.T, mock *mockLLM, dbPath, tasksDir string) *ghostProc {
	t.Helper()
	addr := freeAddr(t)
	cmd := exec.Command(buildBinary(t), "-addr", addr, "-db", dbPath, "-tasks", tasksDir)
	cmd.Env = append(os.Environ(),
		"GR_BASE_URL=http://"+mock.addr+"/v1",
		"GR_API_KEY=e2e-key",
		"GR_MODEL=e2e-model",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动二进制失败: %v", err)
	}
	g := &ghostProc{cmd: cmd, addr: addr}
	// 等面板就绪(认证状态端点公开可探;healthz 已在认证门内)。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/auth/status")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				authInit(t, addr)
				g.token = e2eToken
				return g
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatalf("面板未就绪")
	return nil
}

func apiJSON(t *testing.T, method, addr, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求: %v", err)
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		t.Fatalf("建请求: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e2eToken != "" { // P3-09:认证 token
		req.Header.Set("Authorization", "Bearer "+e2eToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// createTaskWithGoal 建任务(带域名资产种子 + fact_confirmed 目标)。
func createTaskWithGoal(t *testing.T, addr, title string, timeoutSeconds int) string {
	t.Helper()
	code, b := apiJSON(t, "POST", addr, "/api/tasks", map[string]any{
		"title":           title,
		"timeout_seconds": timeoutSeconds,
		"assets":          []map[string]any{{"class": "domain", "title": "mock.local"}},
	})
	if code != 200 {
		t.Fatalf("建任务失败 %d: %s", code, b)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &created)
	// 资产 ID 从资产树读(面板同源)。
	_, ab := apiJSON(t, "GET", addr, "/api/tasks/"+created.ID+"/assets", nil)
	var assets struct {
		Assets []struct {
			ID string `json:"id"`
		} `json:"assets"`
	}
	_ = json.Unmarshal(ab, &assets)
	if len(assets.Assets) == 0 {
		t.Fatalf("任务 %s 无资产种子", created.ID)
	}
	code, b = apiJSON(t, "POST", addr, "/api/tasks/"+created.ID+"/goals", map[string]any{
		"title": "确认靶标暴露面",
		"predicate": map[string]any{
			"op": "any_of",
			"conditions": []map[string]any{
				{"kind": "fact_confirmed", "asset_id": assets.Assets[0].ID, "min": 1},
			},
		},
	})
	if code != 200 {
		t.Fatalf("建目标失败 %d: %s", code, b)
	}
	return created.ID
}

func taskStatuses(t *testing.T, addr string) map[string]map[string]any {
	t.Helper()
	_, b := apiJSON(t, "GET", addr, "/api/tasks", nil)
	var list []map[string]any
	_ = json.Unmarshal(b, &list)
	out := map[string]map[string]any{}
	for _, v := range list {
		out[v["id"].(string)] = v
	}
	return out
}

func waitStatus(t *testing.T, addr, id, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := taskStatuses(t, addr)
		if v, ok := st[id]; ok && v["status"] == want {
			return v
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未在 %s 内到 %s(当前 %v)", id, timeout, want, taskStatuses(t, addr)[id])
	return nil
}

// ---- E2E 本体 ----

func TestRestartAdoptionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过二进制级 E2E")
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "gr.db")
	tasksDir := filepath.Join(root, "tasks")

	mock1 := newMockLLM(t, 1)
	gen1 := startGhost(t, mock1, dbPath, tasksDir)

	// 四个任务:t1/t2 先建占满并发(默认 2),t3 排队,t4 排队且 1 秒
	// 整体超时(停机期间过线)。
	t1 := createTaskWithGoal(t, gen1.addr, "重启收养E2E-t1", 0)
	t2 := createTaskWithGoal(t, gen1.addr, "重启收养E2E-t2", 0)
	t3 := createTaskWithGoal(t, gen1.addr, "重启收养E2E-t3", 0)
	t4 := createTaskWithGoal(t, gen1.addr, "重启收养E2E-t4", 1)

	// 等两个在飞单位挂住(t1/t2 的虫群第二回合)。
	for i := 0; i < 2; i++ {
		select {
		case <-mock1.holdHit:
		case <-time.After(30 * time.Second):
			t.Fatalf("在飞单位未挂住(仅观察到 %d 个 hold)", i)
		}
	}
	// 面板可见的崩溃前状态:两个运行中、两个排队中。
	st := taskStatuses(t, gen1.addr)
	for id, want := range map[string]string{t1: "started", t2: "started", t3: "queued", t4: "queued"} {
		if st[id]["status"] != want {
			t.Fatalf("崩溃前 %s 应为 %s,实际 %v", id, want, st[id]["status"])
		}
	}

	// 杀进程(进程没了)。
	if err := gen1.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL 失败: %v", err)
	}
	_, _ = gen1.cmd.Process.Wait()
	// 等 t4 的整体超时线过去(已过期任务重启后立即收尾的前提)。
	time.Sleep(1200 * time.Millisecond)

	// 重启(换 mock 网关:第二回合不再挂住,续跑即完成)。
	mock2 := newMockLLM(t, 2)
	gen2 := startGhost(t, mock2, dbPath, tasksDir)
	defer func() { _ = gen2.cmd.Process.Kill() }()

	// ① 运行中任务自动恢复并跑到底(幽灵步骤重置、重领、原会话续)。
	waitStatus(t, gen2.addr, t1, "completed", 60*time.Second)
	waitStatus(t, gen2.addr, t2, "completed", 60*time.Second)
	// ② 排队任务重新准入(t1/t2 完成释放名额后 t3 拉起),无死锁。
	waitStatus(t, gen2.addr, t3, "completed", 60*time.Second)
	// ③ 已过期任务重启后立即走收尾时序:终态 stopped 且 settled。
	v4 := waitStatus(t, gen2.addr, t4, "stopped", 60*time.Second)
	if v4["settled"] != true {
		t.Fatalf("t4 终态应为整体超时有序收尾(settled),实际 %v", v4)
	}

	// ④ transcript 连续性:gen2 的续跑请求携带崩溃前历史(候选事实
	// 的工具调用原文),输入为续跑指令而非重注入任务包。
	sawHistory, sawResumeInput, sawPackReinjected := false, false, false
	for _, r := range mock2.requests() {
		if r.ToolsOffered == 0 {
			continue
		}
		if strings.Contains(r.RawBody, factMarker) {
			sawHistory = true
		}
		if strings.Contains(r.LastUser, "重启收养 · 续跑") {
			sawResumeInput = true
			if strings.Contains(r.LastUser, "【任务包 · 七要素】") {
				sawPackReinjected = true
			}
		}
	}
	if !sawHistory || !sawResumeInput {
		t.Fatalf("原会话续证据不足: 历史=%v 续跑指令=%v", sawHistory, sawResumeInput)
	}
	if sawPackReinjected {
		t.Fatalf("续跑段不应重注入完整任务包")
	}

	// ⑤ 账本收养审计(杀掉 gen2 后直读 SQLite)。
	_ = gen2.cmd.Process.Kill()
	_, _ = gen2.cmd.Process.Wait()
	assertAdoptionLedger(t, dbPath, tasksDir, t1, t2, t3, t4)
}

// assertAdoptionLedger 重启收养的账本断言:黑匣子 restart_adoption、幽灵
// 步骤重置、原会话续的 session_started、t4 的收尾时序、转录文件续写。
func assertAdoptionLedger(t *testing.T, dbPath, tasksDir string, ids ...string) {
	t.Helper()
	store, err := rings.Open(dbPath)
	if err != nil {
		t.Fatalf("打开账本断言: %v", err)
	}
	defer store.Close()
	t1, t2, t3, t4 := ids[0], ids[1], ids[2], ids[3]
	ctx := context.Background()

	for _, id := range ids {
		evs, err := store.Events(ctx, id, 0, 0)
		if err != nil {
			t.Fatalf("查 %s 事件流: %v", id, err)
		}
		var adoptedBlackbox, ghostReset bool
		for _, ev := range evs {
			switch ev.Kind {
			case rings.EventBlackboxRecord:
				if strings.Contains(string(ev.Payload), rings.BlackboxRestartAdoption) {
					adoptedBlackbox = true
				}
			case rings.EventNodeStateChanged:
				if ev.Actor == "manager" && strings.Contains(string(ev.Payload), `"dispatched"`) {
					ghostReset = true
				}
			}
		}
		if !adoptedBlackbox {
			t.Fatalf("任务 %s 缺 restart_adoption 黑匣子审计", id)
		}
		if (id == t1 || id == t2) && !ghostReset {
			t.Fatalf("任务 %s 缺幽灵步骤重置事件(running→dispatched)", id)
		}
	}
	// t1 的原会话续入账:收养单位的 session_started 注明原会话续。
	evs, err := store.Events(ctx, t1, 0, 0)
	if err != nil {
		t.Fatalf("查 t1 事件流: %v", err)
	}
	sawResumedNote := false
	for _, ev := range evs {
		if ev.Kind == rings.EventSwarmRecord && strings.Contains(string(ev.Payload), "原会话续") {
			sawResumedNote = true
		}
	}
	if !sawResumedNote {
		t.Fatalf("t1 缺原会话续的 session_started 留痕")
	}
	// 转录文件续写:同一文件既有崩溃前的任务包原文,又有续跑指令与
	// 续跑后的完成回复(单位序号随领取次序不定,取该任务转录目录下
	// 唯一的会话文件)。
	files, err := filepath.Glob(filepath.Join(tasksDir, t1, "transcripts", "swarm-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("t1 应恰一份虫群会话文件(主宰拆解转录另存),得到 %v(err=%v)", files, err)
	}
	tr, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读 t1 转录: %v", err)
	}
	text := string(tr)
	for _, marker := range []string{"【任务包 · 七要素】", factMarker, "重启收养 · 续跑", "步骤完成"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("t1 转录续写缺 %q(连续性破坏)", marker)
		}
	}
	// t4 重启后的收尾时序:settling 与 task_gate 终态都在账本可查。
	evs4, err := store.Events(ctx, t4, 0, 0)
	if err != nil {
		t.Fatalf("查 t4 事件流: %v", err)
	}
	sawSettling, sawGate := false, false
	for _, ev := range evs4 {
		if strings.Contains(string(ev.Payload), "task_settling") {
			sawSettling = true
		}
		if strings.Contains(string(ev.Payload), "task_gate") {
			sawGate = true
		}
	}
	if !sawSettling || !sawGate {
		t.Fatalf("t4 缺重启后立即收尾的证据(settling=%v gate=%v)", sawSettling, sawGate)
	}
	_ = t2
	_ = t3
}
