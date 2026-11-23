package swarm

import (
	"context"
	"encoding/json"

	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP501KnownToolScopesClaimRegistered ③b② 同步点:新交卷工具名
// submit_claim 顶替 submit_falsify_result / submit_test_claim 在受控
// 入口清单(knownToolScopes)中的位置——不登记则新工具被硬规则当未知
// 工具误拦;旧名不得残留。
func TestP501KnownToolScopesClaimRegistered(t *testing.T) {
	if !knownToolScopes["submit_claim"] {
		t.Fatalf("submit_claim 必须登记受控入口清单,否则被硬规则误拦")
	}
	for _, old := range []string{"submit_falsify_result", "submit_test_claim"} {
		if knownToolScopes[old] {
			t.Fatalf("旧工具 %s 不得残留清单", old)
		}
	}
	// guardExemptTool 前缀豁免随命名自动覆盖(guard.go:195 对表)。
	if !guardExemptTool("submit_claim") {
		t.Fatalf("submit_ 前缀豁免应覆盖新交卷工具")
	}
}

// TestP501WorkerDutyAndSelfCheckClauses ① 职责与提示词改造:铁律含
// 职责句(探索→发现→证明→反证自查→交卷,做完即停)、反证自查自觉句
// (锚例:缓存假象/权限本来就有/环境因素)、交卷门槛句保留(真实触发
// +可复现证据);独立反证另派表述零残留。
func TestP501WorkerDutyAndSelfCheckClauses(t *testing.T) {
	for _, want := range []string{
		"探索 → 发现 → 证明 → 反证自查 → 交卷,做完即停",
		"这个结果会不会是缓存假象、权限本来就有、环境因素导致的?换个角度验证一下再交",
		"必须本次真实触发过、拿到可复现证据,才许交卷",
		"submit_claim",
	} {
		if !strings.Contains(workerSafetyRules, want) {
			t.Fatalf("虫群铁律缺关键句 %q:\n%s", want, workerSafetyRules)
		}
	}
	for _, gone := range []string{"submit_test_claim", "submit_falsify_result", "独立反证", "尽力列出竞争性替代解释", "不得自报严重级别"} {
		if strings.Contains(workerSafetyRules, gone) {
			t.Fatalf("铁律不得再含旧口径 %q", gone)
		}
	}
	for _, want := range []string{"实际观察", "证据", "未验证的想法不要作为事实提交"} {
		if !strings.Contains(wrapupPrompt, want) {
			t.Fatalf("收尾指令缺外溢句 %q:\n%s", want, wrapupPrompt)
		}
	}
}

// TestP501LegacyPayloadTolerance ③ 读旧载荷容错:旧格式 falsify_result
// JSON(带已物理删除的结构化字段)解码不崩、被删字段自然丢弃、账本
// 重放与投影读侧不炸(P5-03④ 口径:缺字段按空处理,不做翻译)。
func TestP501LegacyPayloadTolerance(t *testing.T) {
	legacyJSON := `{"claim_id":"tc-old","step_id":"s9","unit_id":"u9","type":"falsify_result",
		"round":1,"existence_refuted":false,"impact_supported":true,"impact_level":"high",
		"alternatives":[{"id":"alt1","refuted":true}],"evidence_exchanges":["ms-old.1"],
		"evidence_head":"旧证据","note":"旧结论"}`
	var rec rings.TestClaimRecord
	if err := json.Unmarshal([]byte(legacyJSON), &rec); err != nil {
		t.Fatalf("旧载荷解码不得崩: %v", err)
	}
	// 被删字段丢弃:保留字段照读,已删字段按零值。
	if rec.ClaimID != "tc-old" || rec.StepID != "s9" || rec.UnitID != "u9" ||
		rec.Type != rings.TestClaimRecFalsify || rec.Round != 1 || rec.EvidenceHead != "旧证据" {
		t.Fatalf("旧载荷保留字段应照读: %+v", rec)
	}
	// 已删维度零值(结构体物理无字段——此断言防字段回潮)。

	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p501-legacy"
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventTaskLifecycle,
		Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "t"}}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	// 原样载荷直落账本(受控入口校验只看保留字段,通过)。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventTestClaimRecord,
		Actor: "swarm:u9", Payload: json.RawMessage(legacyJSON)}); err != nil {
		t.Fatalf("旧载荷事件入账不得崩: %v", err)
	}
	// 事件读回不崩。
	evs, err := store.Events(ctx, task, 0, 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("读事件失败: %v", err)
	}
}
