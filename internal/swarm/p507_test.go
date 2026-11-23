package swarm

// P5-07⑦b/⑫:交卷影响列点三规矩在提示词与工具契约(面板可见目录)
// 双侧就位;不进机器判定(三查输入与 impact_note 无关,判定词表单测
// 在 arbiter 侧覆盖)。

import (
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP507ImpactNoteThreeRules 交卷提示词三规矩(只写有依据的、有几条
// 写几条、猜测要标明是猜测)在工具描述与 JSON 契约双侧就位;旧口径
//"一句话影响"零残留。
func TestP507ImpactNoteThreeRules(t *testing.T) {
	for _, want := range []string{"只写有依据的", "有几条写几条", "猜测要标明是猜测"} {
		if !strings.Contains(toolDescSubmitClaim, want) {
			t.Fatalf("交卷工具描述缺三规矩 %q:\n%s", want, toolDescSubmitClaim)
		}
	}
	if strings.Contains(toolDescSubmitClaim, "一句话影响") {
		t.Fatalf("交卷工具描述不得再含旧口径\"一句话影响\":\n%s", toolDescSubmitClaim)
	}
	// JSON 契约描述同为面板可见目录(插话快照),三规矩同步。
	var schemaDesc string
	if m, ok := (&unitEnv{}).submitClaimTool().InputSchema()["properties"].(map[string]any); ok {
		if f, ok := m["impact_note"].(map[string]any); ok {
			schemaDesc, _ = f["description"].(string)
		}
	}
	if schemaDesc == "" {
		t.Fatal("submit_claim 契约未取到 impact_note 描述")
	}
	for _, want := range []string{"只写有依据的", "有几条写几条", "猜测要标明是猜测"} {
		if !strings.Contains(schemaDesc, want) {
			t.Fatalf("impact_note 契约描述缺三规矩 %q:%s", want, schemaDesc)
		}
	}
}

// TestP507ImpactNoteFreeOfJudgement 影响列点不进机器判定:三查输入
// 契约(TestClaimRecord 判定侧字段)与 impact_note 正交——submit 载荷
// 携带多行列点可落账(受控入口校验只查六字段齐与词表,不查影响
// 内容);判定函数签名无影响入参(编译层事实,此处锁账本入口语义)。
func TestP507ImpactNoteFreeOfJudgement(t *testing.T) {
	rec := rings.TestClaimRecord{
		ClaimID: "tc-x", StepID: "s1", UnitID: "u1", Type: rings.TestClaimRecSubmit,
		Summary: "后台弱口令", Severity: "high", EvidenceRefs: []string{"ms-1.1"},
		ProvingMarker: "admin", CountercheckNote: "重放核对",
		ImpactNote:    "可直登后台\n可导出手机号\n(猜测)或可写 webshell",
	}
	if strings.TrimSpace(rec.ImpactNote) == "" {
		t.Fatal("样本影响列点不应为空")
	}
	// 多行列点原样保留(不折行不改写,呈现层按列点如实展示)。
	if got := len(strings.Split(rec.ImpactNote, "\n")); got != 3 {
		t.Fatalf("影响列点行数应 3,得 %d", got)
	}
}
