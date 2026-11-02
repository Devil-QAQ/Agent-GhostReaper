package overmind

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestP424ReproProofreadSameSession ①:生成 → 校对改写发生在同一会话
// (第二个请求携带第一轮的助手回复与校对续问),校对稿冻结。
func TestP424ReproProofreadSameSession(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, model := p424Loop(t, store, "p424-desc-ok", func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "校对改写") { // 校对续问
			return scriptedReply{Text: p424CleanSteps, Finish: "stop"}
		}
		return scriptedReply{Text: "```json\n" + p424DirtySteps + "\n```", Finish: "stop"}
	})
	defer model.Close()
	rec := mkP424Rec()
	l.genReproDescs(ctx, "tc-x", rec)
	if !strings.HasPrefix(rec.Steps[0].Desc, "向 k3.local 发 GET /admin") {
		t.Fatalf("校对稿应冻结: %+v", rec.Steps[0])
	}
	if rec.Steps[0].Unpolished || rec.Steps[1].Unpolished {
		t.Fatalf("过闸文本不应标注: %+v", rec.Steps)
	}
	reqs := model.requests()
	if len(reqs) != 2 {
		t.Fatalf("成本有界:恰 2 次请求(生成+校对),得到 %d", len(reqs))
	}
	// 同会话铁证:校对请求携带第一轮的助手回复(机器语原稿)与校对续问。
	if !strings.Contains(reqs[1].System, "复现步骤写描述") ||
		!assistantCarries(reqs[1], "tc-8f2a1b3c") ||
		!strings.Contains(reqs[1].LastUser, "校对改写") {
		t.Fatalf("校对应为同会话续问(system/助手原稿/续问): %+v", reqs[1])
	}
}

// TestP424ReproGateRewrite ②:校对稿仍含机器语 → 闸门拦下 → 同会话
// 重写一次成功,无标注。
func TestP424ReproGateRewrite(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, model := p424Loop(t, store, "p424-desc-rewrite", func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "逐处改写掉") { // 闸门重写续问
			return scriptedReply{Text: p424CleanSteps, Finish: "stop"}
		}
		return scriptedReply{Text: p424DirtySteps, Finish: "stop"} // 生成与校对都给机器语稿
	})
	defer model.Close()
	rec := mkP424Rec()
	l.genReproDescs(ctx, "tc-x", rec)
	if !strings.HasPrefix(rec.Steps[0].Desc, "向 k3.local 发 GET /admin") || rec.Steps[0].Unpolished {
		t.Fatalf("重写成功应过闸无标注: %+v", rec.Steps[0])
	}
	reqs := model.requests()
	if len(reqs) != 3 {
		t.Fatalf("成本有界:至多 3 次请求(生成+校对+重写),得到 %d", len(reqs))
	}
	if !strings.Contains(reqs[2].LastUser, "内部编号") || !strings.Contains(reqs[2].LastUser, "tc-8f2a1b3c") {
		t.Fatalf("重写续问应点名命中特征与样例: %s", reqs[2].LastUser)
	}
}

// TestP424ReproGateAnnotate ②:重写一次仍含机器语 → 如实标注 Unpolished
// 原文保留,不阻塞(描述仍在、步骤不缺)。
func TestP424ReproGateAnnotate(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, model := p424Loop(t, store, "p424-desc-flag", func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: p424DirtySteps, Finish: "stop"} // 三轮全给机器语
	})
	defer model.Close()
	rec := mkP424Rec()
	l.genReproDescs(ctx, "tc-x", rec)
	if n := len(model.requests()); n != 3 {
		t.Fatalf("恰 3 次请求后停(重写一次封顶),得到 %d", n)
	}
	if !rec.Steps[0].Unpolished || rec.Steps[0].Desc == "" {
		t.Fatalf("仍不合格应如实标注且原文保留: %+v", rec.Steps[0])
	}
	if !rec.Steps[1].Unpolished {
		t.Fatalf("英文状态词步骤同样标注: %+v", rec.Steps[1])
	}
}

// TestP424ReproProofreadFallback ①:校对失败(坏回复)保留生成稿,
// 业务不阻塞;生成稿干净则直接过闸(无重写请求)。
func TestP424ReproProofreadFallback(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, model := p424Loop(t, store, "p424-desc-fallback", func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "校对改写") {
			return scriptedReply{Text: "这不是 JSON", Finish: "stop"}
		}
		return scriptedReply{Text: p424CleanSteps, Finish: "stop"}
	})
	defer model.Close()
	rec := mkP424Rec()
	l.genReproDescs(ctx, "tc-x", rec)
	if !strings.HasPrefix(rec.Steps[0].Desc, "向 k3.local 发 GET /admin") || rec.Steps[0].Unpolished {
		t.Fatalf("校对失败应保留生成稿: %+v", rec.Steps[0])
	}
	if n := len(model.requests()); n != 2 {
		t.Fatalf("干净生成稿不触发重写(2 次请求),得到 %d", n)
	}
}

// TestP424NarrativePipeline 叙述族全链:同会话校对改写;脏稿被闸门
// 拦下重写;重写仍脏如实标注 NarrativeUnpolished。
func TestP424NarrativePipeline(t *testing.T) {
	store := openStore(t)
	taskSeq := 0
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		taskSeq++
		return p424Loop(t, store, fmt.Sprintf("p424-nar-%d", taskSeq), script)
	}
	ctx := context.Background()

	seed := func(t *testing.T, l *Loop) { seedNarrativeClaim(t, l, l.TaskID) }

	t.Run("校对改写干净过闸", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			if strings.Contains(rr.LastUser, "校对改写") {
				return scriptedReply{Text: `{"narrative":"` + p424CleanNarrative + `"}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"narrative":"` + p424DirtyNarrative + `"}`, Finish: "stop"}
		})
		defer model.Close()
		seed(t, l)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec)
		if rec.Narrative != p424CleanNarrative {
			t.Fatalf("校对稿应冻结: %q", rec.Narrative)
		}
		if hits := ScanMachineTalk(rec.Narrative); len(hits) > 0 {
			t.Fatalf("过闸文本应零命中: %+v", hits)
		}
		if rec.NarrativeUnpolished {
			t.Fatalf("过闸不应标注: %q", rec.Narrative)
		}
		if n := len(model.requests()); n != 2 {
			t.Fatalf("恰 2 次请求,得到 %d", n)
		}
	})

	t.Run("重写仍脏如实标注", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"narrative":"` + p424DirtyNarrative + `"}`, Finish: "stop"}
		})
		defer model.Close()
		seed(t, l)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec)
		if !rec.NarrativeUnpolished || !strings.Contains(rec.Narrative, "tc-8f2a1b3c") {
			t.Fatalf("重写仍脏应如实标注且原文保留: %q(flag=%v)", rec.Narrative, rec.NarrativeUnpolished)
		}
		if n := len(model.requests()); n != 3 {
			t.Fatalf("恰 3 次请求封顶,得到 %d", n)
		}
	})

	t.Run("校对失败保留生成稿", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			if strings.Contains(rr.LastUser, "校对改写") {
				return scriptedReply{Text: "坏回复", Finish: "stop"}
			}
			return scriptedReply{Text: `{"narrative":"` + p424CleanNarrative + `"}`, Finish: "stop"}
		})
		defer model.Close()
		seed(t, l)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec)
		if rec.Narrative != p424CleanNarrative || rec.NarrativeUnpolished {
			t.Fatalf("校对失败应保留生成稿: %q", rec.Narrative)
		}
	})
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
