package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// ---- P2-06e 意图级模型错误退避重跑 / blocked 态 / 时间账 ----

// flakyGateway 模型故障注入网关:前 failN 个请求回指定状态码,其后
// 透传到真实脚本模型(SSE 原样转发)。SDK 建连重试在测试 provider 上
// 关闭(MaxRetries=-1),让每次"模型错误"只打一枪——重试语义归本层。
type flakyGateway struct {
	srv    *httptest.Server
	target *scriptedModel
	hits   atomic.Int64
	failN  int
	mode   int
}

func newFlakyGateway(t *testing.T, target *scriptedModel, failN, mode int) *flakyGateway {
	t.Helper()
	g := &flakyGateway{target: target, failN: failN, mode: mode}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *flakyGateway) handle(w http.ResponseWriter, r *http.Request) {
	n := g.hits.Add(1)
	if int(n) <= g.failN {
		w.WriteHeader(g.mode)
		_, _ = w.Write([]byte(`{"error":{"message":"injected failure"}}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.target.srv.URL, strings.NewReader(string(body)))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// gatewayProvider 经网关构建 provider(SDK 重试关闭,故障语义归本层)。
func (g *flakyGateway) provider(model string) (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format: llm.FormatOpenAI, BaseURL: g.srv.URL, APIKey: "test-key",
		Model: model, MaxRetries: -1,
	})
}

// activeModelProvider 包一层按请求模型归属(stub failover 链的记账面)。
type activeModelProvider struct {
	llm.Provider
	now atomic.Value // string
}

func (p *activeModelProvider) ActiveModel() string {
	if v, ok := p.now.Load().(string); ok && v != "" {
		return v
	}
	return "stub-model"
}

// seedBlockedGraph 最小图:目标 + 意图(只读探测)。
func seedBlockedGraph(t *testing.T, store *rings.Store, task, intent string) {
	t.Helper()
	seedGraph(t, store, task, intent)
}

// TestUnitModelRetryThenDone 模型错误退避重跑后成功:前两枪 503,第三枪
// 成功——步骤 done;任务包原文在会话历史中只出现一次(防续跑消息重复
// 注入),重试以短指令追发。
func TestUnitModelRetryThenDone(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "retry-done"
	seedBlockedGraph(t, store, task, "探测本地靶标端口状态")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "意图执行完成。", Finish: "stop"}
	})
	defer model.Close()
	gate := newFlakyGateway(t, model, 2, http.StatusServiceUnavailable)
	prov, err := gate.provider("scripted-flaky")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}

	root := filepath.Join(dir, "tasks")
	ex := NewExecutor(store, prov, root, Budget{})
	ex.ModelRetryLimit, ex.ModelRetryBackoff = 3, time.Millisecond

	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" || res.Reason != "completed" {
		t.Fatalf("重跑后应 done: %+v", res)
	}
	if got := gate.hits.Load(); got != 3 {
		t.Fatalf("应恰 3 次模型调用(2 失败 + 1 成功),实 %d", got)
	}

	// 防续跑消息重复注入:任务包原文(七要素首节)在会话历史中恰一条。
	reqs := model.requests()
	if len(reqs) != 1 {
		t.Fatalf("成功会话应恰 1 个到达模型的请求(失败枪未到模型),实 %d", len(reqs))
	}
	packCount := 0
	noteCount := 0
	for _, msg := range reqs[0].Messages {
		if msg.Role == "user" {
			if strings.Contains(msg.Content, "一、当前目标") {
				packCount++
			}
			if strings.Contains(msg.Content, "【模型重试 · req-") {
				noteCount++
			}
		}
	}
	if packCount != 1 {
		t.Fatalf("任务包原文应恰出现一次,实 %d", packCount)
	}
	if noteCount != 2 {
		t.Fatalf("两次重试应各追发一条短指令,实 %d", noteCount)
	}
	// 步骤终态与时间账:成功路径 block_wait 记录了失败段等待。
	node, err := store.Node(ctx, task, "s1")
	if err != nil || node.Status != "done" {
		t.Fatalf("步骤终态应为 done: %v %+v", err, node)
	}
}

// TestUnitModelRetriesExhaustedBlocked 重跑耗尽落 blocked:步骤终态
// blocked + 原因;时间账 billable≈0、block_wait>0(故障不吃预算);
// 黑匣子 unit_blocked 留痕;token 零记账(无成功模型轮)。
func TestUnitModelRetriesExhaustedBlocked(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "blocked-task"
	seedBlockedGraph(t, store, task, "探测本地靶标端口状态")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "不可达", Finish: "stop"}
	})
	defer model.Close()
	gate := newFlakyGateway(t, model, 1<<30, http.StatusServiceUnavailable)
	prov, err := gate.provider("scripted-dead")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}

	root := filepath.Join(dir, "tasks")
	ex := NewExecutor(store, prov, root, Budget{MaxTurns: 4, RunSeconds: 300, WrapupMaxTurns: 2})
	ex.ModelRetryLimit, ex.ModelRetryBackoff = 2, 80*time.Millisecond

	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("受阻不是执行器错误(是记账终态): %v", err)
	}
	if res.StepStatus != StepBlocked {
		t.Fatalf("应落 blocked: %+v", res)
	}
	if !strings.Contains(res.Reason, "模型连续失败(3 次)") || !strings.Contains(res.Reason, "503") {
		t.Fatalf("受阻原因应带次数与状态码: %s", res.Reason)
	}
	if got := gate.hits.Load(); got != 3 {
		t.Fatalf("应恰 3 次尝试(首发+2 重跑),实 %d", got)
	}

	// 步骤终态 + 时间账(账本可对账)。
	node, err := store.Node(ctx, task, "s1")
	if err != nil || node.Status != StepBlocked {
		t.Fatalf("步骤终态应为 blocked: %v %+v", err, node)
	}
	note := swarmRecNote(t, store, task, "u1")
	for _, field := range []string{"status=blocked", "block_wait="} {
		if !strings.Contains(note, field) {
			t.Fatalf("session_finished note 缺 %s: %s", field, note)
		}
	}
	if b, bl := noteFieldFloat(note, "billable"), noteFieldFloat(note, "block_wait"); b > 0.5 || bl <= 0 {
		t.Fatalf("时间账不符(故障不吃预算): billable=%.3f block_wait=%.3f note=%s", b, bl, note)
	}
	// 黑匣子留痕。
	if !hasBlackbox(t, store, task, rings.BlackboxUnitBlocked) {
		t.Fatal("黑匣子应记 unit_blocked")
	}
	// token 零记账:无成功模型轮,不产生用量行。
	rows, err := store.TokenUsageRows(ctx, task)
	if err != nil || len(rows) != 0 {
		t.Fatalf("受阻单位不应有 token 行: %v %d", err, len(rows))
	}
}

// TestUnitModelAttributionByRequest 按请求模型记账:第二个模型轮起切到
// 备用名,token 行按服务模型分别记账(主备切换同会话分账的最小直证)。
func TestUnitModelAttributionByRequest(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "attrib-task"
	seedBlockedGraph(t, store, task, "探测本地靶标端口状态")

	var amp *activeModelProvider
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "port_probe", "") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe", Args: `{"host":"127.0.0.1","port":9}`,
			}}, Finish: "tool_calls"}
		}
		// 第二个模型轮已到:切到备用名(确定性时序,不靠 sleep)。
		amp.now.Store("model-backup")
		return scriptedReply{Text: "意图执行完成。", Finish: "stop"}
	})
	defer model.Close()
	gate := newFlakyGateway(t, model, 0, 0)
	prov, err := gate.provider("scripted-attr")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	amp = &activeModelProvider{Provider: prov}
	amp.now.Store("model-primary")

	root := filepath.Join(dir, "tasks")
	ex := NewExecutor(store, amp, root, Budget{})
	ex.Model = "static-model" // 静态字段应被按请求归属覆盖

	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("应 done: %+v", res)
	}
	rows, err := store.TokenUsageRows(ctx, task)
	if err != nil {
		t.Fatalf("读 token 行: %v", err)
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Model]++
	}
	if len(seen) != 2 || seen["model-primary"] == 0 || seen["model-backup"] == 0 {
		t.Fatalf("应按模型分开记账: %v", seen)
	}
	if seen["static-model"] != 0 {
		t.Fatalf("静态模型名不应再出现在行里: %v", seen)
	}
}

// ---- 测试小件 ----

func swarmRecNote(t *testing.T, store *rings.Store, task, unitID string) string {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil {
			continue
		}
		if rec.UnitID == unitID && rec.Type == rings.SwarmRecSessionEnd {
			return rec.Note
		}
	}
	return ""
}

func noteFieldFloat(note, field string) float64 {
	i := strings.Index(note, field+"=")
	if i < 0 {
		return -1
	}
	rest := note[i+len(field)+1:]
	if j := strings.IndexAny(rest, " \n"); j >= 0 {
		rest = rest[:j]
	}
	var v float64
	_, _ = fmt.Sscanf(rest, "%f", &v)
	return v
}

func hasBlackbox(t *testing.T, store *rings.Store, task, typ string) bool {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == typ {
			return true
		}
	}
	return false
}
