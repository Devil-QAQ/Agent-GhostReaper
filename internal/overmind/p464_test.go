package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-64(问题清单 #82)生成管线类级确定性单测:
//   - ① 逐轮人话:零 Provider 拼句兜底、scripted 生成覆盖、解析失败回退
//     拼句、闸门重写一次、仍脏 Unpolished;
//   - ② 立账摘要:claim.summary 事件落账、坏回复不落;
//   - ③⑥ 事实文本:干净冻结、标题仍脏 Unpolished、描述仍脏不冻结;
//   - ④⑥⑧ 假设文本:干净冻结、字段仍脏不冻结;
//   - ⑦ 复测备注:干净冻结、坏回复 nil、仍脏 nil;
//   - 标题短句标准锚例自身过闸(锚例管上限的前提)。

func p464Task(seq int) string { return "p464-t-" + string(rune('a'+seq-1)) }

// p464TitleAnchorClean 标题短句标准与各系统段里模型会照抄的句子。
var p464TitleAnchorClean = []string{
	titleShortStandard,
	factTextSystem,
	factTextProofreadAsk,
	hypoTextSystem,
	hypoTextProofreadAsk,
	retestNoteSystem,
	"example.com:8080 的备份接口无需登录即可下载",
	"10.0.0.5 的备份包内带有后台管理员明文口令",
}

func TestP464TitleAnchorGateClean(t *testing.T) {
	for _, text := range p464TitleAnchorClean {
		if hits := ScanMachineTalk(text); len(hits) > 0 {
			t.Errorf("锚例未过闸(%s…): %+v", clipLine(text, 24), hits)
		}
	}
}

// p464SeedClaimWithAlts 铺带竞争解释冻结清单的判断(逐轮人话素材)。
func p464SeedClaimWithAlts(t *testing.T, l *Loop) {
	t.Helper()
	alts := []any{
		map[string]any{"id": "alt1", "label": "缓存污染"},
		map[string]any{"id": "alt2", "label": "归属错误"},
	}
	if _, err := l.Store.Append(context.Background(), l.TaskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-r", NodeType: rings.NodeTestClaim, Title: "原始存在性判断原文",
			Status: rings.TestClaimTesting,
			Detail: map[string]any{"unit_id": "u1", "alternatives": alts},
		}}); err != nil {
		t.Fatalf("铺判断失败: %v", err)
	}
}

func p464FalsifyRec() *rings.TestClaimRecord {
	return &rings.TestClaimRecord{
		ClaimID: "tc-r", StepID: "s9", UnitID: "u9", Type: rings.TestClaimRecFalsify, Round: 3,
		Alternatives: []rings.AltResult{
			{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
		EvidenceHead: "GET /old-backup/ 连续 8 次返回同样的备份列举",
	}
}

func TestP464RoundHumanZeroProviderFallback(t *testing.T) {
	store := openStore(t)
	l := &Loop{TaskID: p464Task(1), Store: store}
	p464SeedClaimWithAlts(t, l)
	out := l.GenRoundHumanFor(context.Background(), "tc-r", p464FalsifyRec())
	if out == nil {
		t.Fatalf("零 Provider 也应返回拼句兜底")
	}
	if !strings.Contains(out.Doubt, "缓存污染") || !strings.Contains(out.Doubt, "归属错误") {
		t.Fatalf("拼句疑点应为替代解释疑问句: %q", out.Doubt)
	}
	if !strings.Contains(out.How, "u9") || !strings.Contains(out.How, "备份列举") {
		t.Fatalf("拼句应含独立单位与证据: %q", out.How)
	}
	if out.Conclusion != "已排除" {
		t.Fatalf("全部排除应为已排除: %q", out.Conclusion)
	}
	if out.Round != 3 || out.ClaimID != "tc-r" {
		t.Fatalf("轮次与判断不符: %+v", out)
	}
}

func TestP464RoundHumanPipeline(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 0
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p464Task(seq+1), script)
	}
	const clean = `{"rounds":[{"round":3,"doubt":"会不会是 /old-backup/ 的列举来自共享缓存,而不是目标真实文件?","how":"直接向该路径发起 8 次请求,每次都返回同样的备份文件列举","conclusion":"已排除"}]}`
	const dirty = `{"rounds":[{"round":3,"doubt":"alt1 命中 tc-9a2b3c4d","how":"error EOF 重试 status=200","conclusion":"conclusion ok"}]}`

	t.Run("生成覆盖拼句", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			if strings.Contains(rr.LastUser, "校对改写") {
				return scriptedReply{Text: clean, Finish: "stop"}
			}
			return scriptedReply{Text: clean, Finish: "stop"}
		})
		defer model.Close()
		p464SeedClaimWithAlts(t, l)
		out := l.GenRoundHumanFor(ctx, "tc-r", p464FalsifyRec())
		if !strings.Contains(out.Doubt, "共享缓存") || !strings.Contains(out.Conclusion, "已排除") {
			t.Fatalf("模型稿应覆盖拼句: %+v", out)
		}
		if out.Unpolished {
			t.Fatalf("干净稿不应标注 Unpolished")
		}
	})

	t.Run("解析失败回退拼句", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "这不是 JSON", Finish: "stop"}
		})
		p464SeedClaimWithAlts(t, l)
		out := l.GenRoundHumanFor(ctx, "tc-r", p464FalsifyRec())
		if !strings.Contains(out.Doubt, "会不会是") || !strings.Contains(out.How, "独立单位") {
			t.Fatalf("解析失败应回退拼句: %+v", out)
		}
	})

	t.Run("闸门重写一次仍脏如实标注", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: dirty, Finish: "stop"}
		})
		p464SeedClaimWithAlts(t, l)
		out := l.GenRoundHumanFor(ctx, "tc-r", p464FalsifyRec())
		if !out.Unpolished {
			t.Fatalf("重写仍脏应如实标注 Unpolished: %+v", out)
		}
	})
}

func TestP464LaunchSummaryEvent(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 10
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p464Task(seq+1), script)
	}
	const clean = `{"statement":"10.0.0.5 的管理接口没有登录校验,任何知道地址的人都能进入管理后台","impact":"无需任何账号口令,可查看全部 3,214 名用户的姓名与手机号","facts":[]}`

	t.Run("立账摘要落账", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: clean, Finish: "stop"}
		})
		seedSummaryClaim(t, l, l.TaskID)
		l.polishClaimLaunchSummaries(ctx, []string{"tc-x"})
		digests, err := l.Store.ClaimDigests(ctx, l.TaskID)
		if err != nil {
			t.Fatalf("ClaimDigests: %v", err)
		}
		if !strings.HasPrefix(digests["tc-x"].Statement, "10.0.0.5 的管理接口") {
			t.Fatalf("立账摘要应落账并透出: %+v", digests["tc-x"])
		}
	})

	t.Run("坏回复不落事件", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "坏回复", Finish: "stop"}
		})
		seedSummaryClaim(t, l, l.TaskID)
		l.polishClaimLaunchSummaries(ctx, []string{"tc-x"})
		digests, _ := l.Store.ClaimDigests(ctx, l.TaskID)
		if digests["tc-x"].Statement != "" {
			t.Fatalf("坏回复不应落账: %+v", digests["tc-x"])
		}
	})

	t.Run("零 Provider 零外呼", func(t *testing.T) {
		l := &Loop{TaskID: p464Task(30), Store: store}
		l.polishClaimLaunchSummaries(ctx, []string{"tc-none"})
	})
}

func p464SeedFact(t *testing.T, l *Loop) {
	t.Helper()
	if _, err := l.Store.Append(context.Background(), l.TaskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "/old-backup/ 备份目录可公开匿名列举",
			Status: "confirmed",
			Detail: map[string]any{"unit_id": "u1", "evidence_head": "GET /old-backup/ 返回 200 与备份文件列举"},
		}}); err != nil {
		t.Fatalf("铺事实失败: %v", err)
	}
}

func TestP464FactTexts(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 40
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p464Task(seq+1), script)
	}

	t.Run("干净冻结且事件落账", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"facts":[{"fact_id":"f-1","title":"10.0.0.5 的备份目录无需登录即可列举","statement":"备份目录没有访问限制,任何知道地址的人都能看到备份文件清单"}]}`, Finish: "stop"}
		})
		p464SeedFact(t, l)
		l.polishConfirmedFacts(ctx, []string{"f-1"})
		np, err := l.Store.NodePolish(ctx, l.TaskID)
		if err != nil {
			t.Fatalf("NodePolish: %v", err)
		}
		if np["f-1"].Title == "" || np["f-1"].Statement == "" || np["f-1"].Unpolished {
			t.Fatalf("事实文本应冻结: %+v", np["f-1"])
		}
	})

	t.Run("标题仍脏 Unpolished 描述仍脏不冻结", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"facts":[{"fact_id":"f-1","title":"tc-9a2b3c4d 未授权 error","statement":"接口暴露 alt2 命中 status=200"}]}`, Finish: "stop"}
		})
		p464SeedFact(t, l)
		l.polishConfirmedFacts(ctx, []string{"f-1"})
		np, _ := l.Store.NodePolish(ctx, l.TaskID)
		p := np["f-1"]
		if !p.Unpolished {
			t.Fatalf("标题仍脏应 Unpolished: %+v", p)
		}
		if p.Statement != "" {
			t.Fatalf("描述仍脏应不冻结(读侧退节点标题): %+v", p)
		}
	})

	t.Run("未知名条目与零 Provider 不落", func(t *testing.T) {
		l := &Loop{TaskID: p464Task(50), Store: store}
		p464SeedFact(t, l)
		l.polishConfirmedFacts(ctx, []string{"f-1"}) // 零 Provider 零外呼
		np, _ := l.Store.NodePolish(ctx, l.TaskID)
		if len(np) != 0 {
			t.Fatalf("零 Provider 不应落事件: %+v", np)
		}
	})
}

func p464SeedHypo(t *testing.T, l *Loop) {
	t.Helper()
	if _, err := l.Store.Append(context.Background(), l.TaskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h-1", NodeType: rings.NodeHypothesis, Title: "管理后台可能有弱口令",
			Status: rings.HypothesisCandidate,
			Detail: map[string]any{"unit_id": "u1", "basis": "登录页无验证码"}}}); err != nil {
		t.Fatalf("铺假设失败: %v", err)
	}
}

func TestP464HypoTexts(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 60
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p464Task(seq+1), script)
	}

	t.Run("干净冻结", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"hypotheses":[{"hypothesis_id":"h-1","title":"example.com:8080 的管理后台可能有弱口令","basis":"登录页无验证码,口令策略宽松","reason":"值得跟:先验证弱口令是否真实可登录"}]}`, Finish: "stop"}
		})
		p464SeedHypo(t, l)
		plans := []hypoEvalPlan{{HypothesisID: "h-1", Decision: rings.HypothesisVerify,
			Reason: "值得跟:先验证", VerifyIntent: "验证弱口令", ActionLevel: "只读探测"}}
		out := l.polishHypoTexts(ctx, plans)
		if out["h-1"].Title == "" || out["h-1"].Basis == "" || out["h-1"].Reason == "" {
			t.Fatalf("假设文本应冻结: %+v", out["h-1"])
		}
	})

	t.Run("字段仍脏不冻结且未评估保持原文", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"hypotheses":[{"hypothesis_id":"h-1","title":"h-1 猜测 alt2","basis":"basis ok","reason":"EOF error"}]}`, Finish: "stop"}
		})
		p464SeedHypo(t, l)
		out := l.polishHypoTexts(ctx, []hypoEvalPlan{{HypothesisID: "h-1", Decision: "hold", Reason: "x"}})
		p := out["h-1"]
		if !p.Unpolished {
			t.Fatalf("标题仍脏应 Unpolished: %+v", p)
		}
		if p.Reason != "" {
			t.Fatalf("理由仍脏应不冻结: %+v", p)
		}
		if p.Basis == "" {
			t.Fatalf("干净的依据应保留: %+v", p)
		}
	})

	t.Run("零 Provider 空表", func(t *testing.T) {
		l := &Loop{TaskID: p464Task(70), Store: store}
		if out := l.polishHypoTexts(ctx, []hypoEvalPlan{{HypothesisID: "h-1"}}); len(out) != 0 {
			t.Fatalf("零 Provider 应空表: %+v", out)
		}
	})
}

func TestP464RetestNote(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 80
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p464Task(seq+1), script)
	}

	t.Run("干净冻结", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"note":"按复现步骤重发了请求,备份接口仍返回同样的文件列举,说明该漏洞未修复"}`, Finish: "stop"}
		})
		out := l.PolishRetestNote(ctx, "tc-r", 1, "unfixed", "复测:GET /old-backup/ 200")
		if out == nil || !strings.Contains(out.Note, "备份接口") {
			t.Fatalf("复测备注应校对冻结: %+v", out)
		}
	})

	t.Run("坏回复与空备注返回 nil", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "坏回复", Finish: "stop"}
		})
		if out := l.PolishRetestNote(ctx, "tc-r", 1, "fixed", "note"); out != nil {
			t.Fatalf("坏回复应返回 nil: %+v", out)
		}
		if out := l.PolishRetestNote(ctx, "tc-r", 1, "fixed", "  "); out != nil {
			t.Fatalf("空备注应返回 nil: %+v", out)
		}
	})

	t.Run("机器语重写仍脏返回 nil", func(t *testing.T) {
		l, _ := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"note":"tc-9a2b3c4d error EOF status=200"}`, Finish: "stop"}
		})
		if out := l.PolishRetestNote(ctx, "tc-r", 1, "fixed", "原始备注"); out != nil {
			t.Fatalf("仍脏应返回 nil(读侧退原文): %+v", out)
		}
	})

	t.Run("零 Provider 返回 nil", func(t *testing.T) {
		l := &Loop{TaskID: p464Task(90), Store: store}
		if out := l.PolishRetestNote(ctx, "tc-r", 1, "fixed", "note"); out != nil {
			t.Fatalf("零 Provider 应返回 nil: %+v", out)
		}
	})
}
