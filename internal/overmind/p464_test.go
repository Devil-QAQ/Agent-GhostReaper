package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func p464Task(seq int) string { return "p464-t-" + string(rune('a'+seq-1)) }

// p464TitleAnchorClean 标题短句标准与各系统段里模型会照抄的句子。
var p464TitleAnchorClean = []string{
	titleShortStandard,
	factTextSystem,
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
			Status: "testing",
			Detail: map[string]any{"unit_id": "u1", "alternatives": alts},
		}}); err != nil {
		t.Fatalf("铺判断失败: %v", err)
	}
}

func p464FalsifyRec() *rings.TestClaimRecord {
	return &rings.TestClaimRecord{
		ClaimID: "tc-r", StepID: "s9", UnitID: "u9", Type: rings.TestClaimRecFalsify, Round: 3,
		EvidenceHead: "GET /old-backup/ 连续 8 次返回同样的备份列举",
	}
}

// (TestP464RoundHumanZeroProviderFallback 已随逐轮人话链路退役删除,P5-03③。)
// (TestP464RoundHumanPipeline 已随逐轮人话链路退役删除,P5-03③。)
// (TestP464LaunchSummaryEvent 已随立账摘要批次退役删除,P5-03③——
// 声明摘要只留确认定稿批次。)

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
	for _, version := range []string{"2.4.1", "42", "3.0.0-rc.1"} {
		t.Run("产品版本完整入库_"+version, func(t *testing.T) {
			statement := "服务名称为 demo，版本 " + version + "。"
			body, _ := json.Marshal(map[string]any{"facts": []map[string]string{{"fact_id": "f-1", "title": "服务公开提供版本信息", "statement": statement}}})
			l, model := mk(t, func(rr *recordedRequest) scriptedReply {
				return scriptedReply{Text: string(body), Finish: "stop"}
			})
			_, err := l.Store.Append(ctx, l.TaskID, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm", Payload: rings.NodeAdded{NodeID: "f-1", NodeType: rings.NodeFact, Title: statement, Status: "confirmed"}})
			if err != nil {
				t.Fatal(err)
			}
			l.polishConfirmedFacts(ctx, []string{"f-1"})
			polish, err := l.Store.NodePolish(ctx, l.TaskID)
			if err != nil || polish["f-1"].Statement != statement || polish["f-1"].Unpolished {
				t.Fatalf("产品版本不应被文风闸门丢弃: %+v %v", polish, err)
			}
			if len(model.requests()) != 1 {
				t.Fatal("正常产品版本不应触发机器语重写")
			}
		})
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
