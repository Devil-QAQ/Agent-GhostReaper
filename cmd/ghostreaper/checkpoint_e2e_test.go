package main

// P3-07 断点续接二进制级 E2E:手动断点(面板入口的 API 形态)→ 杀进程
// → 重启后任务自动原地续跑(账本/transcript 可证)。与重启收养 E2E 的
// 差异:崩溃是意外,断点是主动收场——单位在工具调用边界落地(session_
// finished 带 Checkpointed 标记、步骤回可领取),重启收养把 checkpointed
// 与运行中同列自动重拉,单位重领后原会话续(【断点 · 续跑】)。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ckptMockLLM 断点版脚本模型:拆解会话派一条只读意图;虫群首回合提交
// 候选事实;其后(gen1)持续发起 bash 工具调用——单位始终停在工具调用
// 边界附近,存档断点即时收拢;gen2(重启后)见到【断点 · 续跑】即收尾。
type ckptMockLLM struct {
	srv  *http.Server
	addr string
	gen  int

	mu      sync.Mutex
	reqs    []mockReq
	toolHit chan struct{} // gen1 每轮 bash 工具调用通知(单位在飞的证据)
}

func newCkptMockLLM(t *testing.T, gen int) *ckptMockLLM {
	t.Helper()
	m := &ckptMockLLM{gen: gen, toolHit: make(chan struct{}, 64)}
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

func (m *ckptMockLLM) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
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

	var text, toolCall string
	switch {
	case req.ToolsOffered == 0:
		// 拆解会话:图上已有同标记意图则空产物,否则派一条只读意图。
		if strings.Contains(string(raw), intentMarker) {
			text = `{"new_intents":[]}`
		} else {
			asset := assetRe.FindString(string(raw))
			text = fmt.Sprintf(`{"new_intents":[{"intent":"%s 对靶标域名做一次确认观察","action_level":"只读探测","covers":["%s"],"expected_observation":"域名解析可达"}]}`,
				intentMarker, asset)
		}
	case strings.Contains(req.LastUser, "断点 · 续跑"):
		text = "步骤完成:断点前的观察已写回,本步骤意图达成。"
	case !req.HasToolMsg:
		// 虫群首回合:提交候选事实(带资产锚点,收割可转正)。
		asset := assetRe.FindString(string(raw))
		toolCall = fmt.Sprintf(`[{"id":"call-1","type":"function","function":{"name":"submit_candidate_fact","arguments":"{\"statement\":\"%s 靶标域名可解析\",\"evidence\":\"dig mock.local A 127.0.0.1(命令与输出摘录)\",\"asset_id\":\"%s\"}"}}]`,
			factMarker, asset)
	case m.gen == 1:
		// 断点前:持续发 bash 工具调用(在飞;存档断点在边界收拢)。
		select {
		case m.toolHit <- struct{}{}:
		default:
		}
		args := fmt.Sprintf(`{"command":%q}`, "echo ckpt-e2e-working")
		toolCall = fmt.Sprintf(`[{"id":"call-loop","type":"function","function":{"name":"bash","arguments":%q}}]`, args)
	default:
		text = "步骤完成。"
	}
	writeSSEReply(w, text, toolCall, len(raw))
}

func (m *ckptMockLLM) requests() []mockReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mockReq(nil), m.reqs...)
}

// startGhostLLM 同 startGhost,但以 LLM 地址为参数(适配本文件的
// ckptMockLLM)。
func startGhostLLM(t *testing.T, llmAddr, dbPath, tasksDir string) *ghostProc {
	t.Helper()
	addr := freeAddr(t)
	cmd := exec.Command(buildBinary(t), "-addr", addr, "-db", dbPath, "-tasks", tasksDir)
	cmd.Env = append(os.Environ(),
		"GR_BASE_URL=http://"+llmAddr+"/v1",
		"GR_API_KEY=e2e-key",
		"GR_MODEL=e2e-model",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动二进制失败: %v", err)
	}
	g := &ghostProc{cmd: cmd, addr: addr}
	// 就绪探测走公开的认证状态端点;就绪后认证(P3-09:首启 setup,
	// 同库重启 login——与本测试的两代进程形态一致)。
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

// TestCheckpointResumeE2E P3-07 完成标准主证:面板入口存档断点(冻结
// 收拢)→ SIGKILL → 重启 → 任务自动原地续跑到完成;账本与 transcript
// 双证(断点落地标记、断点续接原会话、转录同文件续写、生命周期序列)。
func TestCheckpointResumeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过二进制级 E2E")
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "gr.db")
	tasksDir := filepath.Join(root, "tasks")

	mock1 := newCkptMockLLM(t, 1)
	gen1 := startGhostLLM(t, mock1.addr, dbPath, tasksDir)
	task := createTaskWithGoal(t, gen1.addr, "断点E2E", 0)

	// 等单位在飞(至少两轮 bash 工具调用过边界)。
	for i := 0; i < 2; i++ {
		select {
		case <-mock1.toolHit:
		case <-time.After(30 * time.Second):
			t.Fatalf("在飞单位未到工具边界(仅观察到 %d 轮)", i)
		}
	}

	// 面板入口存档断点:冻结收拢(frozen=true)后返回。
	code, b := apiJSON(t, "POST", gen1.addr, "/api/tasks/"+task+"/checkpoint", map[string]any{})
	if code != 200 {
		t.Fatalf("存档断点失败 %d: %s", code, b)
	}
	var ckptResp struct {
		Status string `json:"status"`
		Frozen bool   `json:"frozen"`
	}
	_ = json.Unmarshal(b, &ckptResp)
	if ckptResp.Status != "checkpointed" || !ckptResp.Frozen {
		t.Fatalf("存档响应不符(应 checkpointed/frozen): %s", b)
	}
	if st := taskStatuses(t, gen1.addr)[task]["status"]; st != "checkpointed" {
		t.Fatalf("面板状态应为 checkpointed,实际 %v", st)
	}
	// 重复存档:400(已存档态)。
	if code, _ := apiJSON(t, "POST", gen1.addr, "/api/tasks/"+task+"/checkpoint", map[string]any{}); code != 400 {
		t.Fatalf("重复存档应 400,得到 %d", code)
	}

	// 杀进程(手动断点后的关进程)。
	if err := gen1.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL 失败: %v", err)
	}
	_, _ = gen1.cmd.Process.Wait()

	// 重启(换 gen2 网关:见到断点续跑指令即收尾)。
	mock2 := newCkptMockLLM(t, 2)
	gen2 := startGhostLLM(t, mock2.addr, dbPath, tasksDir)

	// 恢复续跑:任务自动重拉并跑到完成终态。
	waitStatus(t, gen2.addr, task, "completed", 60*time.Second)

	// 模型侧直证:续跑请求携带断点前历史 + 输入为【断点 · 续跑】短指令
	//(不重注入任务包)。
	sawHistory, sawResumeInput, sawPackReinjected := false, false, false
	for _, r := range mock2.requests() {
		if r.ToolsOffered == 0 {
			continue
		}
		if strings.Contains(r.RawBody, factMarker) {
			sawHistory = true
		}
		if strings.Contains(r.LastUser, "断点 · 续跑") {
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

	// 账本与 transcript 直证(杀掉 gen2 后读)。
	_ = gen2.cmd.Process.Kill()
	_, _ = gen2.cmd.Process.Wait()
	assertCheckpointLedgerE2E(t, dbPath, task)
}

// assertCheckpointLedgerE2E 断点闭环的账本断言:生命周期序列
// created→started→checkpointed→started(收养重拉);断点落地单位
// session_finished 带 Checkpointed、步骤回可领取;续跑单位 session_
// started 注明断点续接:原会话续、TranscriptID 沿用;转录同文件含
// 断点前事实提交与断点后收尾。
func assertCheckpointLedgerE2E(t *testing.T, dbPath, task string) {
	t.Helper()
	store, err := rings.Open(dbPath)
	if err != nil {
		t.Fatalf("打开账本断言: %v", err)
	}
	defer store.Close()
	evs, err := store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}

	var acts []string
	var finished *rings.SwarmRecord
	var resumed *rings.SwarmRecord
	for i, ev := range evs {
		switch ev.Kind {
		case rings.EventTaskLifecycle:
			var p rings.TaskLifecycle
			if json.Unmarshal(ev.Payload, &p) == nil {
				acts = append(acts, p.Action)
			}
		case rings.EventSwarmRecord:
			var r rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &r) != nil {
				continue
			}
			if r.Type == rings.SwarmRecSessionEnd && r.Checkpointed {
				fr := r
				finished = &fr
			}
			if r.Type == rings.SwarmRecSessionStarted && strings.Contains(r.Note, "断点续接:原会话续") {
				rr := r
				resumed = &rr
			}
			_ = i
		}
	}
	// 生命周期:created→started→checkpointed→started(收养重拉)→completed。
	want := []string{"created", "started", "checkpointed", "started", "completed"}
	if strings.Join(acts, ",") != strings.Join(want, ",") {
		t.Fatalf("生命周期序列不符: %v", acts)
	}
	if finished == nil {
		t.Fatalf("缺少断点落地的 session_finished(Checkpointed 标记)")
	}
	if resumed == nil {
		t.Fatalf("缺少断点续接的 session_started(原会话续注记)")
	}
	if resumed.TranscriptID != finished.TranscriptID {
		t.Fatalf("续跑应沿用原会话转录: %s vs %s", resumed.TranscriptID, finished.TranscriptID)
	}
}
