package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
)

func TestContentDisplayParsingPreservesExplanation(t *testing.T) {
	for _, text := range []string{
		"尚未产出结果；这是一项待执行的检查。",
		"本轮49次尝试未出现注入迹象，不能据此排除其他接口。",
		strings.Repeat("不同结果分别说明，原始证据另存。", 30) + "仍需确认身份条件。",
	} {
		t.Run(text[:12], func(t *testing.T) {
			step, _ := json.Marshal(stepTitleReply{Steps: []rings.StepTitlePolish{{StepID: "s1", Title: "检查入口", Statement: text}, {StepID: "unknown", Title: "不应混入"}}})
			got := parseStepTitles(string(step), map[string]bool{"s1": true})
			if len(got) != 1 || got["s1"].Statement != text {
				t.Fatalf("意图说明被截断或未知条目混入: %+v", got)
			}
			fact, _ := json.Marshal(factTextReply{Facts: []rings.FactTextPolish{{FactID: "f1", Title: "观察结果", Statement: text}, {FactID: "unknown", Title: "不应混入"}}})
			facts := parseFactTexts(string(fact), map[string]bool{"f1": true})
			if len(facts) != 1 || facts["f1"].Statement != text {
				t.Fatalf("事实说明被截断或边界丢失: %+v", facts)
			}
		})
	}
	old := parseStepTitles(`{"steps":[{"step_id":"s1","title":"旧标题稿"}]}`, map[string]bool{"s1": true})
	if old["s1"].Statement != "" || old["s1"].Title != "旧标题稿" {
		t.Fatal("旧显示稿不兼容")
	}
}

func TestContentDisplayGateRespectsContext(t *testing.T) {
	for _, text := range []string{"同时允许携带凭据", "80 端口仅返回默认欢迎页。", "443 是业务入口，80 未跳转。", "验证短信验证码锁定机制可绕过性"} {
		if hits := scanDisplayText(text); len(hits) > 0 {
			t.Errorf("正常上下文说明被误判: %s %+v", text, hits)
		}
	}
	for _, text := range []string{"tc-9a2b3c4d 的内部判断", "请求返回 error EOF", "当前状态为 confirmed"} {
		if len(scanDisplayText(text)) == 0 {
			t.Errorf("机器内容未拦截: %s", text)
		}
	}
}

func TestContentClaimSummaryKeepsNameAndFullExplanation(t *testing.T) {
	text := strings.Repeat("说明与证据分开，访问前提准确。", 30) + "尚未验证其他入口。"
	raw, _ := json.Marshal(summaryReply{Title: "备份可匿名读取", Statement: text, Impact: text, Facts: []rings.FactStatement{{FactID: "f1", Statement: text}, {FactID: "unknown", Statement: "不应混入"}}})
	got := parseSummary(string(raw), &summaryPack{Facts: []summaryFactMaterial{{FactID: "f1"}}})
	if got.Title != "备份可匿名读取" || got.Statement != text || got.Impact != text || len(got.Facts) != 1 || got.Facts[0].Statement != text {
		t.Fatal("名称或完整说明、范围丢失")
	}
}

func TestContentConfirmingSummaryUsesCheckedDecision(t *testing.T) {
	const task, claim, url = "content-confirming", "tc-summary", "http://t.example:8080/proof"
	store, ex := p502Base(t, task, claim, url, "fixture-marker")
	p502Submit(t, store, task, claim, "s1", url, "fixture-marker")
	ctx := context.Background()
	var sawSummary atomic.Bool
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.HasPrefix(rr.LastUser, "判断原文与相关事实:") {
			sawSummary.Store(true)
			n, err := store.Node(ctx, task, claim)
			if !strings.Contains(rr.LastUser, `"status":"confirmed"`) || err != nil || n.Status != rings.TestClaimConfirmed {
				t.Error("显示生成必须在真实机器确认后读取已确认状态")
			}
		}
		return scriptedReply{Text: `{"title":"备份无需登录可读","statement":"未登录也可读取这份备份。","impact":"备份中的配置可被读取。","facts":[]}`, Finish: "stop"}
	})
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: provider}
	out, err := loop.CheckClaimOnSubmit(ctx, task, claim)
	if err != nil || out.Status != rings.TestClaimConfirmed {
		t.Fatalf("真实机器确认失败: %+v %v", out, err)
	}
	loop.polishWG.Wait()
	row, ok, err := store.ClaimCaseOf(ctx, task, claim)
	if err != nil || !ok || !sawSummary.Load() || row.Summary == nil || row.Summary.Title != "备份无需登录可读" {
		t.Fatalf("名称未进入正式显示结果: %+v %v", row, err)
	}
}
