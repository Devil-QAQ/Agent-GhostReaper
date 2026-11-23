package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-24 确定性单测(scripted 模型走真实 agentcore 会话):① 定稿校对
// ——同会话续问(第二个请求可见第一轮的助手回复)改写为锚例口径;
// 校对失败保留生成稿;② 模式闸门——生成稿含机器语被拦、同会话重写
// 一次成功与仍失败如实标注(Unpolished / NarrativeUnpolished)两条路;
// 成本有界(描述族至多 3 次请求、叙述族至多 3 次)。

const p424CleanSteps = `{"steps":[{"seq":1,"desc":"向 k3.local 发 GET /admin,无需凭据;返回 200 即复现"},{"seq":2,"desc":"向 k3.local 发 POST /login,请求体 user=admin&pass=admin,无需凭据;返回 200 即复现"}]}`

const p424DirtySteps = `{"steps":[{"seq":1,"desc":"GET /admin 200,tc-8f2a1b3c 复现"},{"seq":2,"desc":"POST /login,状态 success"}]}`

const p424CleanNarrative = `单位 u1 执行「拉取首页与 JS」时,在 /static/app.js 的 200 响应正文里发现硬编码凭据;u2 独立反证 1 轮排除缓存污染与不可复现两条解释,仲裁者判定成立。`

const p424DirtyNarrative = `tc-8f2a1b3c:单位u1发现硬编码凭据,反证1轮,alt2已排除,verdict为confirmed。`

// mkP424Rec 两条步骤的定稿记录(确定性兜底描述在位)。
func mkP424Rec() *rings.TestClaimRecord {
	return &rings.TestClaimRecord{
		ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
		Steps: []rings.ReproStep{
			{Seq: 1, StepID: "s1", UnitID: "u2", Desc: "请求 /admin,目标返回 200",
				Method: "GET", URL: "http://k3.local/admin", Status: 200, BlobRef: "b1"},
			{Seq: 2, StepID: "s1", UnitID: "u2", Desc: "提交 /login(请求体 24 字节),目标返回 200",
				Method: "POST", URL: "http://k3.local/login", Status: 200, BlobRef: "b5"},
		},
	}
}

func p424Loop(t *testing.T, store *rings.Store, task string, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
	t.Helper()
	mkTask(t, store, task)
	model := newGatedScriptedModel(script)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起 scripted 模型失败: %v", err)
	}
	return &Loop{TaskID: task, Store: store, Provider: prov}, model
}

// assistantCarries 报告会话历史里是否有助手回复携带 marker(同会话铁证)。
func assistantCarries(rr recordedRequest, marker string) bool {
	for _, m := range rr.Messages {
		if m.Role == "assistant" && strings.Contains(m.Content, marker) {
			return true
		}
	}
	return false
}

// TestP424ReproLedgerRoundTrip 标注字段经账本受控入口落账与回读:
// 带标注的定稿记录被接受,JSON 字段名与读侧约定一致(旧记录无字段,
// 读侧零值不变)。
func TestP424ReproLedgerRoundTrip(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p424-ledger"
	mkTask(t, store, task)
	rec := &rings.TestClaimRecord{
		ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
		Narrative:           "叙述原文(含机器语样例 tc-8f2a1b3c)",
		NarrativeUnpolished: true,
		Steps: []rings.ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2", Desc: "描述原文",
			Method: "GET", URL: "http://k3.local/admin", Status: 200, BlobRef: "b1", Unpolished: true}},
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: *rec}); err != nil {
		t.Fatalf("带标注定稿应被账本接受: %v", err)
	}
	var back rings.TestClaimRecord
	if err := json.Unmarshal(mustLatestReproPayload(t, store, task), &back); err != nil {
		t.Fatalf("回读定稿失败: %v", err)
	}
	if !back.NarrativeUnpolished || len(back.Steps) != 1 || !back.Steps[0].Unpolished {
		t.Fatalf("标注字段应回读: %+v", back)
	}
}

// mustLatestReproPayload 读任务事件流里最新 repro 记录的原始负载。
func mustLatestReproPayload(t *testing.T, store *rings.Store, task string) []byte {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != rings.EventTestClaimRecord {
			continue
		}
		var p rings.TestClaimRecord
		if json.Unmarshal(evs[i].Payload, &p) == nil && p.Type == rings.TestClaimRecRepro {
			return evs[i].Payload
		}
	}
	t.Fatal("未找到 repro 记录")
	return nil
}
