package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-65(问题清单 #82 A⑨⑩)生成管线类级确定性单测:
//   - ⑨ 研判叙述:零 Provider 零外呼、scripted 干净冻结(事件落账)、
//     解析失败不落、字段仍脏不冻结(读侧退原文)、全字段未过闸不落
//     空事件(物理校验拒空的生成侧保证);
//   - ⑩ 意图标题:干净冻结、未知条目丢弃、仍脏条目不落、多形态机器语
//     类级命中(规则 15:判定语义为契约,不按样本特判);
//   - 叙述锚与意图标题锚例自身过闸(锚例管上限的前提);
//   - 机器消费对账:校对稿落账后节点 Title 与任务包意图原文一字不动。

func p465Task(seq int) string { return "p465-t-" + string(rune('a'+seq-1)) }

// p465AssessRec 研判记录样本(六字段全量)。
func p465AssessRec() rings.OvermindRecord {
	return rings.OvermindRecord{
		Type:         rings.OvermindRecAssessment,
		SessionID:    "overmind-p465-r1",
		QAccess:      "凭 s2 拿到的口令可登录 10.0.0.5:8080 管理后台",
		QPrivilege:   "无新权限",
		QCombination: "无",
		QUnlock:      "无",
		QOverturn:    "无",
		Note:         "下一轮验证口令在 10.0.0.6 后台是否通用",
	}
}

// TestP465AnchorGateClean 锚例与系统段里模型会照抄的句子自身过闸。
func TestP465AnchorGateClean(t *testing.T) {
	for _, text := range []string{
		assessPolishSystem,
		assessPolishProofreadAsk,
		stepTitleSystem,
		stepTitleProofreadAsk,
		intentTitleStandard,
		"10.0.0.5:8080 的管理接口凭 s2 拿到的口令可以登录,原判断不再成立;下一步验证该口令在 10.0.0.6 上的同一套后台是否通用。",
		"对 10.0.0.5:8080 做目录枚举,找未授权入口",
		"用 s2 拿到的口令登录 10.0.0.6 后台,验证是否通用",
	} {
		if hits := ScanMachineTalk(text); len(hits) > 0 {
			t.Errorf("锚例未过闸(%s…): %+v", clipLine(text, 24), hits)
		}
	}
}

// TestP465AssessPolishZeroProvider 零 Provider 零外呼:异步直接返回,
// 账本无 overmind.record_polish 事件(读侧退原文)。
func TestP465AssessPolishZeroProvider(t *testing.T) {
	store := openStore(t)
	l := &Loop{TaskID: p465Task(1), Store: store}
	l.polishAssessmentAsync(context.Background(), 7, p465AssessRec())
	l.WaitPolish()
	if evs := p465PolishEvents(t, store, l.TaskID); len(evs) != 0 {
		t.Fatalf("零 Provider 不应落校对稿事件: %d", len(evs))
	}
	l.polishStepTitlesAsync(context.Background(), []stepTitleMaterial{{StepID: "s1", Intent: "对 10.0.0.5:8080 做目录枚举"}})
	l.WaitPolish()
	if evs := p465TitleEvents(t, store, l.TaskID); len(evs) != 0 {
		t.Fatalf("零 Provider 不应落标题校对稿事件: %d", len(evs))
	}
}

// p465PolishEvents / p465TitleEvents 读两类包装事件(测试观测器)。
func p465PolishEvents(t *testing.T, store *rings.Store, task string) []rings.OvermindPolish {
	t.Helper()
	evs, err := store.EventsByKinds(context.Background(), task, rings.EventOvermindPolish)
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.OvermindPolish
	for _, ev := range evs {
		var p rings.OvermindPolish
		if json.Unmarshal(ev.Payload, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

func p465TitleEvents(t *testing.T, store *rings.Store, task string) []rings.StepTitlePolish {
	t.Helper()
	evs, err := store.EventsByKinds(context.Background(), task, rings.EventStepTitle)
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.StepTitlePolish
	for _, ev := range evs {
		var p rings.StepTitlePolish
		if json.Unmarshal(ev.Payload, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// TestP465AssessPolishPipeline 研判叙述管线(类级:干净冻结/解析失败/
// 字段仍脏不冻结/全空不落)。
func TestP465AssessPolishPipeline(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 0
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p465Task(seq+1), script)
	}
	const clean = `{"q_access":"凭 s2 拿到的口令可以登录 10.0.0.5:8080 的管理后台","q_privilege":"无新权限","q_combination":"无","q_unlock":"无","q_overturn":"无","note":"下一轮验证该口令在 10.0.0.6 的同一套后台是否通用"}`
	// 机器语多形态类级(规则 15:特征是契约,非样本特判;闸门只兜下限,
	// 形态取特征表实有的三类——内部编号、程序报错原文、英文状态词)。
	dirtyForms := []string{
		`{"q_access":"tc-9a2b3c4d 判断已确认状态","note":"下一轮验证口令在 10.0.0.6 的后台是否通用"}`,
		`{"q_access":"error EOF 重试 3 次后仍然失败","note":"下一轮验证口令在 10.0.0.6 的后台是否通用"}`,
		`{"q_access":"研判结论为 confirmed 状态","note":"下一轮验证口令在 10.0.0.6 的后台是否通用"}`,
	}

	t.Run("干净冻结", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: clean, Finish: "stop"}
		})
		defer model.Close()
		l.polishAssessment(ctx, 11, p465AssessRec())
		evs := p465PolishEvents(t, store, l.TaskID)
		if len(evs) != 1 || evs[0].RecRev != 11 {
			t.Fatalf("应落一条锚定 rev11 的校对稿: %+v", evs)
		}
		if !strings.Contains(evs[0].QAccess, "10.0.0.5:8080") || evs[0].QPrivilege != "无新权限" {
			t.Fatalf("字段冻结不符: %+v", evs[0])
		}
	})

	t.Run("解析失败不落", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "不是 JSON", Finish: "stop"}
		})
		defer model.Close()
		l.polishAssessment(ctx, 12, p465AssessRec())
		if evs := p465PolishEvents(t, store, l.TaskID); len(evs) != 0 {
			t.Fatalf("解析失败不应落事件: %d", len(evs))
		}
	})

	t.Run("字段仍脏不冻结", func(t *testing.T) {
		for i, dirty := range dirtyForms {
			l, model := mk(t, func(rr *recordedRequest) scriptedReply {
				return scriptedReply{Text: dirty, Finish: "stop"} // 重写仍回脏稿
			})
			l.polishAssessment(ctx, int64(13+i), p465AssessRec())
			model.Close()
			evs := p465PolishEvents(t, store, l.TaskID)
			if len(evs) != 1 {
				t.Fatalf("形态 %d 应恰落一条(脏字段退、干净字段冻): %+v", i, evs)
			}
			if evs[0].QAccess != "" {
				t.Fatalf("形态 %d 脏字段不应冻结: %+v", i, evs[0])
			}
			if evs[0].Note == "" {
				t.Fatalf("形态 %d 干净字段应冻结: %+v", i, evs[0])
			}
		}
	})

	t.Run("部分字段过闸只冻结干净字段", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: dirtyForms[0], Finish: "stop"}
		})
		defer model.Close()
		l.polishAssessment(ctx, 17, rings.OvermindRecord{
			Type:    rings.OvermindRecAssessment,
			QAccess: "凭 s2 拿到的口令可以登录 10.0.0.5:8080 的管理后台",
			Note:    "下一轮验证口令在 10.0.0.6 的同一套后台是否通用",
		})
		evs := p465PolishEvents(t, store, l.TaskID)
		if len(evs) != 1 {
			t.Fatalf("应落一条: %+v", evs)
		}
		if evs[0].QAccess != "" {
			t.Fatalf("脏字段不应冻结: %+v", evs[0])
		}
		if evs[0].Note == "" || strings.Contains(evs[0].Note, "tc-9a2b3c4d") {
			t.Fatalf("干净字段应冻结且不含脏原文: %+v", evs[0])
		}
	})

	t.Run("全字段未过闸不落空事件", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"q_access":"tc-9a2b3c4d error EOF confirmed 三连"}`, Finish: "stop"}
		})
		defer model.Close()
		l.polishAssessment(ctx, 18, p465AssessRec())
		if evs := p465PolishEvents(t, store, l.TaskID); len(evs) != 0 {
			t.Fatalf("全部字段未过闸不应落空事件: %+v", evs)
		}
	})
}

// TestP465StepTitlePipeline 意图标题管线(类级:干净冻结/未知条目丢弃/
// 仍脏不落/节点原文与任务包不受影响)。
func TestP465StepTitlePipeline(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 0
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p465Task(seq+10), script)
	}
	const clean = `{"steps":[{"step_id":"s1","title":"对 10.0.0.5:8080 做目录枚举,找未授权入口"},{"step_id":"s2","title":"用 s2 的口令登录 10.0.0.6 后台,验证是否通用"},{"step_id":"s9","title":"对未知步骤的条目"}]}`
	// 机器语标题多形态类级(规则 15;闸门只兜下限,形态取特征表实有的
	// 三类——内部编号、程序报错原文、英文状态词)。
	dirtyForms := []string{
		`{"steps":[{"step_id":"s1","title":"探测 tc-9a2b3c4d 的备份接口"}]}`,
		`{"steps":[{"step_id":"s1","title":"对备份接口发请求后 error EOF"}]}`,
		`{"steps":[{"step_id":"s1","title":"验证接口状态为 confirmed 的绕过入口"}]}`,
	}

	t.Run("干净冻结与未知条目丢弃", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: clean, Finish: "stop"}
		})
		defer model.Close()
		l.polishStepTitles(ctx, []stepTitleMaterial{{StepID: "s1", Intent: "原始意图一"}, {StepID: "s2", Intent: "原始意图二"}})
		evs := p465TitleEvents(t, store, l.TaskID)
		if len(evs) != 2 {
			t.Fatalf("未知条目应丢弃,应恰 2 条: %+v", evs)
		}
		for _, ev := range evs {
			if ev.StepID == "s9" {
				t.Fatalf("未知条目混入: %+v", ev)
			}
		}
	})

	t.Run("解析失败不落", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "(空)", Finish: "stop"}
		})
		defer model.Close()
		l.polishStepTitles(ctx, []stepTitleMaterial{{StepID: "s1", Intent: "原始意图"}})
		if evs := p465TitleEvents(t, store, l.TaskID); len(evs) != 0 {
			t.Fatalf("解析失败不应落: %d", len(evs))
		}
	})

	t.Run("仍脏条目不落多形态", func(t *testing.T) {
		for i, dirty := range dirtyForms {
			l, model := mk(t, func(rr *recordedRequest) scriptedReply {
				return scriptedReply{Text: dirty, Finish: "stop"}
			})
			l.polishStepTitles(ctx, []stepTitleMaterial{{StepID: "s1", Intent: "原始意图"}})
			model.Close()
			if evs := p465TitleEvents(t, store, l.TaskID); len(evs) != 0 {
				t.Fatalf("形态 %d 仍脏不应落: %+v", i, evs)
			}
		}
	})
}

// TestP465MachineConsumeIntact 机器消费对账(完成标准④):校对稿落账后
// 步骤节点 Title 原文一字不动,任务包意图原文=节点原文(polishStepTitles
// 全链跑过且事件在账)。
func TestP465MachineConsumeIntact(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, model := p424Loop(t, store, p465Task(30), func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: `{"steps":[{"step_id":"s1","title":"对 10.0.0.5:8080 做目录枚举,找未授权入口"}]}`, Finish: "stop"}
	})
	defer model.Close()
	const intent = "对 web1 的 8080 端口做目录探测(原文意图,机器消费锚)"
	stepID, err := l.dispatch(ctx, IntentPlan{Intent: intent, ActionLevel: "只读探测"}, sourceExplore)
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Node(ctx, l.TaskID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Title != intent {
		t.Fatalf("节点标题应为意图原文: %q", node.Title)
	}
	l.polishStepTitles(ctx, []stepTitleMaterial{{StepID: stepID, Intent: intent}})
	if evs := p465TitleEvents(t, store, l.TaskID); len(evs) != 1 {
		t.Fatalf("校对稿应落账: %+v", evs)
	}
	after, err := store.Node(ctx, l.TaskID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != intent {
		t.Fatalf("校对稿落账后节点原文一字不动: %q → %q", intent, after.Title)
	}
	np, err := store.NodePolish(ctx, l.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if np[stepID].Title != "对 10.0.0.5:8080 做目录枚举,找未授权入口" {
		t.Fatalf("node_polish 投影应持显示稿: %+v", np[stepID])
	}
}
