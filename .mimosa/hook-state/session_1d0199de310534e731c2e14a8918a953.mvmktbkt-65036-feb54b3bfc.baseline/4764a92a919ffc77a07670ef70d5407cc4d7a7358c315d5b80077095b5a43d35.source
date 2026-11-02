package server

import (
	"path/filepath"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/rings"
)

// ---- P2-08b 会话页数据面:会话原文读 / 主宰研判轨迹 / 单位信息条 ----

// seedSessionsTask 造一个带三轮研判的任务(含窗口内的派发步骤 / 事实
// 转正 / 声明裁决 / 假设裁决),供 overmind rounds 派生断言。
func seedSessionsTask(t *testing.T, srv *Server) string {
	t.Helper()
	task := "t-sessions"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "会话页验证"}},
		// 首轮研判前的既有图(节点类型预建的覆盖对象:种子声明在首轮前入账)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "既有声明"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "f-1", NodeType: rings.NodeFact, Title: "候选事实"}},
		// 第 1 轮研判(读了 rev 3)。
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-t-sessions-d1",
				EvaluatedRev: 3, QAccess: "新身份可达 a-1", Dispatched: 1}},
		// 窗口 1:派发 s1 + 收割 f-1 转正 + tc-1 仲裁确认。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "探测 a-1", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "f-1", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "running"}},
		// 第 2 轮研判(读了 rev 7)。
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-t-sessions-d2",
				EvaluatedRev: 7, QUnlock: "新可达解锁 s2", Combined: []string{"tc-chain-9"}}},
		// 窗口 2:派发 s2 + 假设 h-1 收割挂账。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2",
			Payload: rings.NodeAdded{NodeID: "h-1", NodeType: rings.NodeHypothesis, Title: "直觉"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep, Title: "验证假设", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "h-1", Status: "held"}},
		// 步骤终态(当前投影)。
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}},
	)
	if err != nil {
		t.Fatalf("seed 任务失败: %v", err)
	}
	return task
}

func TestOvermindRoundsDerivation(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := seedSessionsTask(t, srv)
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != 200 {
		t.Fatalf("GET /overmind = %d: %v", code, out)
	}
	rounds, ok := out["rounds"].([]any)
	if !ok || len(rounds) != 2 {
		t.Fatalf("研判轮数 = %v, want 2", out["rounds"])
	}
	r1 := rounds[0].(map[string]any)
	if r1["session_id"] != "overmind-t-sessions-d1" || r1["q_access"] != "新身份可达 a-1" {
		t.Fatalf("第 1 轮锚点/五问不符: %v", r1)
	}
	steps1, _ := r1["steps"].([]any)
	if len(steps1) != 1 || steps1[0].(map[string]any)["id"] != "s1" {
		t.Fatalf("第 1 轮派发步骤不符: %v", r1["steps"])
	}
	if steps1[0].(map[string]any)["status"] != "done" {
		t.Fatalf("步骤当前态应为 done: %v", steps1[0])
	}
	facts1, _ := r1["facts_confirmed"].([]any)
	if len(facts1) != 1 || facts1[0] != "f-1" {
		t.Fatalf("第 1 轮收割转正不符: %v", r1["facts_confirmed"])
	}
	claims1, _ := r1["claims_judged"].([]any)
	if len(claims1) != 1 || claims1[0].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("第 1 轮声明裁决不符: %v", r1["claims_judged"])
	}
	r2 := rounds[1].(map[string]any)
	if r2["q_unlock"] != "新可达解锁 s2" {
		t.Fatalf("第 2 轮五问不符: %v", r2)
	}
	steps2, _ := r2["steps"].([]any)
	if len(steps2) != 1 || steps2[0].(map[string]any)["id"] != "s2" {
		t.Fatalf("第 2 轮派发步骤不符: %v", r2["steps"])
	}
	hypos2, _ := r2["hypotheses"].([]any)
	if len(hypos2) != 1 || hypos2[0].(map[string]any)["status"] != "held" {
		t.Fatalf("第 2 轮假设收割不符: %v", r2["hypotheses"])
	}
	combined, _ := r2["combined"].([]any)
	if len(combined) != 1 || combined[0] != "tc-chain-9" {
		t.Fatalf("第 2 轮组合声明不符: %v", r2["combined"])
	}
	// 第 1 轮不得吃到窗口 2 的内容(步骤 s2 / 假设 h-1)。
	for _, s := range steps1 {
		if s.(map[string]any)["id"] == "s2" {
			t.Fatalf("s2 泄入第 1 轮窗口")
		}
	}
}

func TestOvermindRoundsEmpty(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "t-empty"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "无研判"}})
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != 200 {
		t.Fatalf("GET = %d", code)
	}
	if rounds, _ := out["rounds"].([]any); len(rounds) != 0 {
		t.Fatalf("无研判任务应得空轮次表: %v", out["rounds"])
	}
}

func TestTranscriptGet(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := seedSessionsTask(t, srv)
	// 用引擎同款转录库写一份会话原文(user 任务包 + assistant 思考/文本/
	// 工具调用 + 工具结果 + 压缩边界)。
	tstore := transcript.NewStore(filepath.Join(srv.RootDir, task, "transcripts"))
	w := tstore.NewWriter("swarm-u9", "")
	appendRec := func(m llm.Message) {
		w.RecordMessage(m, llm.Usage{})
	}
	appendRec(llm.UserText("任务包:探测目标"))
	appendRec(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "先看端口"},
		llm.TextBlock("开始探测"),
		{Type: llm.BlockToolUse, ID: "tu-1", Name: "http_probe", Input: []byte(`{"url":"http://127.0.0.1:1"}`)},
	}})
	appendRec(llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{
		llm.ToolResultText("tu-1", "404 not found", true),
	}})
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/transcripts/swarm-u9", "")
	if code != 200 {
		t.Fatalf("GET transcripts = %d: %v", code, out)
	}
	if out["exists"] != true {
		t.Fatalf("exists 应为 true: %v", out)
	}
	entries, _ := out["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d, want 3: %v", len(entries), entries)
	}
	asst := entries[1].(map[string]any)
	if asst["kind"] != "assistant" || asst["thinking"] != "先看端口" || asst["text"] != "开始探测" {
		t.Fatalf("assistant 分解不符: %v", asst)
	}
	tools, _ := asst["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "http_probe" {
		t.Fatalf("工具调用不符: %v", asst["tools"])
	}
	usr := entries[2].(map[string]any)
	results, _ := usr["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["is_error"] != true {
		t.Fatalf("工具结果不符: %v", usr["results"])
	}
	// 不存在的会话:exists=false 如实呈现。
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/transcripts/swarm-nope", "")
	if code != 200 || out["exists"] != false {
		t.Fatalf("不存在会话应 exists=false: %d %v", code, out)
	}
	// 路径穿越拒绝。
	code, _ = callJSON(t, mux, "GET", "/api/tasks/"+task+"/transcripts/..%2Fescape", "")
	if code != 400 {
		t.Fatalf("路径穿越应 400, got %d", code)
	}
}

func TestUnitsListInfoBar(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	seedIJUnit(t, srv, "t-uinfo") // s1/swarm-u1 已结束
	if err := srv.Store.RecordTokens(t.Context(),
		rings.TokenUsage{TaskID: "t-uinfo", Agent: "swarm:u1", Model: "m-a", TranscriptID: "swarm-u1", InputTokens: 100, OutputTokens: 10},
		rings.TokenUsage{TaskID: "t-uinfo", Agent: "swarm:u2", Model: "m-b", TranscriptID: "swarm-u1", InputTokens: 50},
		rings.TokenUsage{TaskID: "t-uinfo", Agent: "swarm:u9", Model: "m-c", TranscriptID: "swarm-u9", InputTokens: 999},
	); err != nil {
		t.Fatalf("seed token 行失败: %v", err)
	}
	code, out := callJSON(t, mux, "GET", "/api/tasks/t-uinfo/units", "")
	if code != 200 {
		t.Fatalf("GET units = %d", code)
	}
	units, _ := out["units"].([]any)
	if len(units) != 1 {
		t.Fatalf("单位数 = %d, want 1", len(units))
	}
	u1 := units[0].(map[string]any)
	info := u1["info"].(map[string]any)
	// 重启接力的同名单位(u2 续写 swarm-u1)token 行按会话锚合并。
	if info["input_tokens"].(float64) != 150 || info["output_tokens"].(float64) != 10 || info["calls"].(float64) != 2 {
		t.Fatalf("单位信息条聚合不符: %v", info)
	}
	if info["model"] != "m-b" { // 最近一次调用
		t.Fatalf("信息条模型应为最近模型: %v", info)
	}
}
