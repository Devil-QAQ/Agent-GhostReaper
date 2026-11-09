package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-59(问题清单 #80①③)确定性单测:① 报告级文风锚的四段正反样例自身
// 过模式闸门(锚例管上限的前提——模型照锚例写的句子不能再被闸门打回);
// ② 声明摘要族全链(scripted 模型走真实 agentcore 会话):生成 → 同会话
// 校对 → 闸门 → 重写一次 → 仍脏字段不冻结(读侧退节点原文);事实包
// 装配(组合链事实 + 发现步骤产出事实)与解析过滤(未知名条目丢弃)。

// p459AnchorClean 报告级锚里模型会照抄的句子(正例句 + 共用标准全文)。
var p459AnchorClean = []string{
	proofreadHumanStandard,
	summarySystem,
	summaryProofreadAsk,
	"10.0.0.5 的管理接口没有登录校验,任何知道地址的人都能进入管理后台",
	"服务器把访客提交的内容原样存进了页面文件,就是任何人都能把一段脚本存进去,从而其他用户打开该页面时会在自己浏览器里执行这段脚本",
	"无需任何账号口令,可下载全部 3,214 名用户的姓名、手机号和证件号",
	"向该接口直接发 GET 请求,不携带登录凭据;返回 200,内容是 1,847 条员工记录;说明该接口的数据不登录也能拿到",
	"仅获取 5 条展示",
	"对该接口增加登录与权限校验",
}

// TestP459AnchorGateClean 锚例自身过闸(改文风标准时的硬前提,同 P4-24
// 注意事项 4 的契约)。
func TestP459AnchorGateClean(t *testing.T) {
	for _, text := range p459AnchorClean {
		if hits := ScanMachineTalk(text); len(hits) > 0 {
			t.Errorf("锚例未过闸(%s…): %+v", clipLine(text, 24), hits)
		}
	}
}

// p459Clean / p459Dirty 摘要族的干净稿与机器语稿。
const p459Clean = `{"statement":"10.0.0.5 的管理接口没有登录校验,任何知道地址的人都能进入管理后台","impact":"无需任何账号口令,可查看全部 3,214 名用户的姓名与手机号","facts":[{"fact_id":"f-1","statement":"登录接口 /admin 不校验会话,未登录也能拿到管理数据"}]}`

const p459Dirty = `{"statement":"tc-9a2b3c4d 未授权,状态 error","impact":"泄露大量数据","facts":[{"fact_id":"f-1","statement":"接口暴露 alt2 命中"}]}`

// seedSummaryClaim 铺"发现步骤 → 声明(derived→step) → 步骤产出事实"
// 的最小链(普通声明的底层事实经 produces 边)。
func seedSummaryClaim(t *testing.T, l *Loop, task string) {
	t.Helper()
	seed := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测管理入口", Status: "done",
			Detail: map[string]any{"goal_id": "g1"}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-x", NodeType: rings.NodeTestClaim, Title: "原始存在性判断原文",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "impact": "原始影响面原文"}}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-x", ToNode: "s1"}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-1", NodeType: rings.NodeFact, Title: "事实原文:接口无凭据可访问", Status: "confirmed"}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-1"}},
	}
	if _, err := l.Store.Append(context.Background(), task, seed...); err != nil {
		t.Fatalf("铺链失败: %v", err)
	}
}

// TestP459SummaryPipeline 摘要族全链四路。
func TestP459SummaryPipeline(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seq := 0
	mk := func(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*Loop, *gatedScriptedModel) {
		seq++
		return p424Loop(t, store, p459Task(seq), script)
	}

	t.Run("生成干净直接冻结", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			if strings.Contains(rr.LastUser, "校对改写") {
				return scriptedReply{Text: p459Clean, Finish: "stop"}
			}
			return scriptedReply{Text: p459Dirty, Finish: "stop"}
		})
		defer model.Close()
		seedSummaryClaim(t, l, l.TaskID)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimSummary(ctx, "tc-x", rec)
		if !strings.HasPrefix(rec.Statement, "10.0.0.5 的管理接口没有登录校验") ||
			rec.Impact != "无需任何账号口令,可查看全部 3,214 名用户的姓名与手机号" {
			t.Fatalf("校对稿应冻结: %+v", rec)
		}
		if len(rec.Facts) != 1 || rec.Facts[0].FactID != "f-1" ||
			!strings.HasPrefix(rec.Facts[0].Statement, "登录接口 /admin 不校验会话") {
			t.Fatalf("事实描述应冻结: %+v", rec.Facts)
		}
		if n := len(model.requests()); n != 2 {
			t.Fatalf("干净稿恰 2 次请求(生成+校对),得到 %d", n)
		}
		// 同会话铁证:系统段为判断文本契约,校对请求可见生成轮原稿。
		reqs := model.requests()
		if !strings.Contains(reqs[0].System, "判断文本") || !strings.Contains(reqs[1].LastUser, "校对改写") {
			t.Fatalf("摘要族会话形态不符: system=%q ask=%q", reqs[0].System, reqs[1].LastUser)
		}
	})

	t.Run("闸门重写一次成功", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			if strings.Contains(rr.LastUser, "逐处改写掉") {
				return scriptedReply{Text: p459Clean, Finish: "stop"}
			}
			return scriptedReply{Text: p459Dirty, Finish: "stop"}
		})
		defer model.Close()
		seedSummaryClaim(t, l, l.TaskID)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimSummary(ctx, "tc-x", rec)
		if !strings.HasPrefix(rec.Statement, "10.0.0.5") || len(rec.Facts) != 1 {
			t.Fatalf("重写成功应冻结干净稿: %+v", rec)
		}
		if n := len(model.requests()); n != 3 {
			t.Fatalf("至多 3 次请求(生成+校对+重写),得到 %d", n)
		}
		if !strings.Contains(model.requests()[2].LastUser, "内部编号") {
			t.Fatalf("重写续问应点名命中特征: %s", model.requests()[2].LastUser)
		}
	})

	t.Run("重写仍脏字段不冻结", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: p459Dirty, Finish: "stop"} // 三轮全给机器语
		})
		defer model.Close()
		seedSummaryClaim(t, l, l.TaskID)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimSummary(ctx, "tc-x", rec)
		if n := len(model.requests()); n != 3 {
			t.Fatalf("恰 3 次请求封顶,得到 %d", n)
		}
		// 含机器语的字段(内部编号/英文状态词/解释编号)不冻结;闸门只
		// 兜下限——"泄露大量数据"不含机器语特征(空话归锚例管),保留
		// 冻结,读侧呈现该生成稿。
		if rec.Statement != "" || len(rec.Facts) != 0 {
			t.Fatalf("仍脏字段应置空退原文: %+v", rec)
		}
		if rec.Impact != "泄露大量数据" {
			t.Fatalf("无机器语特征字段应保留: %+v", rec)
		}
	})

	t.Run("生成解析失败不阻塞", func(t *testing.T) {
		l, model := mk(t, func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "不是 JSON", Finish: "stop"}
		})
		defer model.Close()
		seedSummaryClaim(t, l, l.TaskID)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimSummary(ctx, "tc-x", rec)
		if rec.Statement != "" || rec.Impact != "" || len(rec.Facts) != 0 {
			t.Fatalf("解析失败应零冻结(读侧按原文呈现): %+v", rec)
		}
		if n := len(model.requests()); n != 1 {
			t.Fatalf("生成失败即止(1 次请求),得到 %d", n)
		}
	})

	t.Run("零模型零外呼", func(t *testing.T) {
		l, _ := mk(t, nil)
		l.Provider = nil
		seedSummaryClaim(t, l, l.TaskID)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimSummary(ctx, "tc-x", rec)
		if rec.Statement != "" || rec.Impact != "" || len(rec.Facts) != 0 {
			t.Fatalf("零 Provider 应零冻结: %+v", rec)
		}
	})
}

// p459Task 任务名生成(每子测试独立任务)。
func p459Task(seq int) string { return "p459-sum-" + string(rune('a'+seq-1)) }

// TestP459SummaryPackAssemble 事实包装配:普通声明收发现步骤产出事实
// (produces),组合声明收 derived_from 事实;未知 fact_id 的回复条目被
// 解析过滤。
func TestP459SummaryPackAssemble(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	l, _ := p424Loop(t, store, "p459-pack", func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "{}", Finish: "stop"}
	})
	seedSummaryClaim(t, l, l.TaskID)
	pack, err := l.summaryPack(ctx, "tc-x")
	if err != nil {
		t.Fatalf("装配事实包失败: %v", err)
	}
	if pack.Statement != "原始存在性判断原文" || pack.Impact != "原始影响面原文" {
		t.Fatalf("声明两维原文应入包: %+v", pack)
	}
	if len(pack.Facts) != 1 || pack.Facts[0].FactID != "f-1" || pack.Facts[0].Statement != "事实原文:接口无凭据可访问" {
		t.Fatalf("产出事实应入包: %+v", pack.Facts)
	}
	// 解析过滤:未知名条目丢弃,知名条目截断保留。
	r := parseSummary(`{"facts":[{"fact_id":"f-x","statement":"未知名"},{"fact_id":"f-1","statement":" 事实改写 "}],"statement":"s","impact":"i"}`, pack)
	if len(r.Facts) != 1 || r.Facts[0].FactID != "f-1" || r.Facts[0].Statement != "事实改写" {
		t.Fatalf("未知名应丢弃、空白应修剪: %+v", r.Facts)
	}
}
