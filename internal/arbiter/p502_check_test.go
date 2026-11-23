package arbiter

import (
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P5-02① 机器三查(ThreeChecks)类级:判定次序固定(查一→查二→
// 查三),各分支多形态样本集断言——真证据过/引用不命中拒/marker 不在
// 响应体拒/countercheck 空拒/判定标准被改拒;纯函数零 IO(账本级
// 集成形态见 overmind p502 交卷即查测试)。

// 三查全过的基准事实。
func passFacts() CheckFacts {
	return CheckFacts{
		RefsTotal: 1, RefsHit: 1, MarkerInBody: true,
		Countercheck: "排除了缓存假象:重复请求响应一致", StandardFrozen: true,
	}
}

func TestP502ThreeChecksPass(t *testing.T) {
	// 类级:多引用全命中 + marker 在 + 自查有记录 + 标准冻结 → 确认,
	// 条件三行全过,decision=confirm。
	for _, f := range []CheckFacts{
		passFacts(),
		{RefsTotal: 3, RefsHit: 3, MarkerInBody: true, Countercheck: "x", StandardFrozen: true},
		{RefsTotal: 2, RefsHit: 2, MarkerInBody: true, Countercheck: "检查了权限默认态", StandardFrozen: true},
	} {
		out := ThreeChecks(f)
		if out.Status != rings.TestClaimConfirmed {
			t.Fatalf("真证据应三查全过判确认: %+v → %s", f, out.Status)
		}
		if out.Verdict.Decision != rings.VerdictConfirm || !out.Verdict.Satisfied {
			t.Fatalf("确认裁决负载不符: %+v", out.Verdict)
		}
		if len(out.Verdict.Conditions) != 3 {
			t.Fatalf("裁决条件应三行(逐查一行): %+v", out.Verdict.Conditions)
		}
		for _, c := range out.Verdict.Conditions {
			if !c.Satisfied || c.Required != 1 || c.Actual != 1 {
				t.Fatalf("确认条件应全过: %+v", c)
			}
		}
	}
}

func TestP502ThreeChecksRefMissRejects(t *testing.T) {
	// 查一·引用不命中类级:总条数与命中数不齐(部分悬空/全悬空)→ 拒,
	// 失败原因逐字"证据不可追溯/证明物不匹配"。
	for _, f := range []CheckFacts{
		{RefsTotal: 1, RefsHit: 0, MarkerInBody: false, Countercheck: "x", StandardFrozen: true},
		{RefsTotal: 3, RefsHit: 2, MarkerInBody: true, Countercheck: "x", StandardFrozen: true},
		{RefsTotal: 2, RefsHit: 1, MarkerInBody: false, Countercheck: "y", StandardFrozen: true},
	} {
		out := ThreeChecks(f)
		if out.Status != rings.TestClaimCandidate {
			t.Fatalf("引用不命中应拒(回候选待补考): %+v", f)
		}
		if !strings.Contains(out.Verdict.PendingReason, ReasonUntraceableEvidence) {
			t.Fatalf("查一失败原因须逐字: %q", out.Verdict.PendingReason)
		}
		if out.Verdict.Decision != rings.VerdictRetake {
			t.Fatalf("三查不过的裁决词应为 retake: %+v", out.Verdict)
		}
	}
}

func TestP502ThreeChecksMarkerMissRejects(t *testing.T) {
	// 查一·证明物不匹配类级:引用全命中而 marker 不在任何响应体 → 拒
	// (同一失败原因句承载两因,机器不分修辞)。
	for _, f := range []CheckFacts{
		{RefsTotal: 1, RefsHit: 1, MarkerInBody: false, Countercheck: "x", StandardFrozen: true},
		{RefsTotal: 2, RefsHit: 2, MarkerInBody: false, Countercheck: "y", StandardFrozen: true},
	} {
		out := ThreeChecks(f)
		if out.Status != rings.TestClaimCandidate ||
			!strings.Contains(out.Verdict.PendingReason, ReasonUntraceableEvidence) {
			t.Fatalf("marker 不在响应体应拒且原因逐字: %+v → %s %q", f, out.Status, out.Verdict.PendingReason)
		}
	}
}

func TestP502ThreeChecksCountercheckRejects(t *testing.T) {
	// 查二类级:自查记录空(空串/纯空白)→ 拒,原因逐字"反证自查无记录";
	// 未消解推翻记录 → 拒,原因逐字"存在未消解的推翻记录"。
	for _, blank := range []string{"", "   ", "\t\n"} {
		f := passFacts()
		f.Countercheck = blank
		out := ThreeChecks(f)
		if out.Status != rings.TestClaimCandidate ||
			!strings.Contains(out.Verdict.PendingReason, ReasonCountercheckMissing) {
			t.Fatalf("countercheck 空应拒且原因逐字: %q → %s %q", blank, out.Status, out.Verdict.PendingReason)
		}
	}
	f := passFacts()
	f.RefutedOpen = true
	out := ThreeChecks(f)
	if out.Status != rings.TestClaimCandidate ||
		!strings.Contains(out.Verdict.PendingReason, ReasonRefutedOpen) {
		t.Fatalf("未消解推翻记录应拒且原因逐字: %s %q", out.Status, out.Verdict.PendingReason)
	}
}

func TestP502ThreeChecksStandardRejects(t *testing.T) {
	// 查三类级:判定标准未冻结(缺失/被改成不可解析形态——调用方按
	// 可解析性给 bool)→ 拒,原因逐字"判定标准未冻结"。
	f := passFacts()
	f.StandardFrozen = false
	out := ThreeChecks(f)
	if out.Status != rings.TestClaimCandidate ||
		!strings.Contains(out.Verdict.PendingReason, ReasonStandardUnfrozen) {
		t.Fatalf("标准未冻结应拒且原因逐字: %s %q", out.Status, out.Verdict.PendingReason)
	}
	// 条件行如实:查三不满足。
	kinds := map[string]bool{}
	for _, c := range out.Verdict.Conditions {
		if c.Kind == CondStandardFrozen && c.Satisfied {
			t.Fatalf("查三条件应不满足: %+v", c)
		}
		kinds[c.Kind] = true
	}
	for _, k := range []string{CondEvidenceTraceable, CondCountercheckDone, CondStandardFrozen} {
		if !kinds[k] {
			t.Fatalf("裁决条件缺 %s: %+v", k, out.Verdict.Conditions)
		}
	}
}

func TestP502ThreeChecksMultiFailReasons(t *testing.T) {
	// 多查同败:失败原因按判定次序逐条并列(分号分隔)——补考反馈注入
	// 的机器拼装,不自由发挥。
	f := CheckFacts{RefsTotal: 1, RefsHit: 0, Countercheck: "", StandardFrozen: false}
	out := ThreeChecks(f)
	want := ReasonUntraceableEvidence + ";" + ReasonCountercheckMissing + ";" + ReasonStandardUnfrozen
	if out.Verdict.PendingReason != want {
		t.Fatalf("多查同败原因应按次序逐条: got %q want %q", out.Verdict.PendingReason, want)
	}
}

// TestP502StatusWords 完成标准⑧:后端回传词按三态词表(工具回传摘要
// 与面板同源单一来源;显示键与机器词面分离)。
func TestP502StatusWords(t *testing.T) {
	cases := map[string]string{
		rings.TestClaimCandidate:    "待机器核验",
		rings.TestClaimConfirmed:    "已确认",
		rings.TestClaimInsufficient: "证据不足",
		rings.TestClaimRefuted:      "被推翻",
		rings.TestClaimDiscarded:    "已废弃",
		rings.TestClaimFixed:        "已修复",
	}
	for status, want := range cases {
		if got := rings.ClaimStatusWord(status); got != want {
			t.Errorf("ClaimStatusWord(%q) = %q, want %q", status, got, want)
		}
	}
	// 显示键:三态直映射,旧过程态原样退回。
	if k, _ := rings.ClaimDisplay(rings.TestClaimInsufficient); k != rings.ClaimDisplayInsufficient {
		t.Fatalf("证据不足显示键: %q", k)
	}
	if k, _ := rings.ClaimDisplay("testing"); k != "testing" {
		t.Fatalf("旧过程态应原样退回: %q", k)
	}
}
