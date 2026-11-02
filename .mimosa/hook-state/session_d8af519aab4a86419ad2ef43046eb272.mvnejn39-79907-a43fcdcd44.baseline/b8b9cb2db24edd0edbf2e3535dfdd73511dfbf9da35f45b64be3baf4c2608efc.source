package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P4-13 确定性单测:链上事实包装配(发现血缘/来源载体流量(发现步骤的
// 目标交换)/反证轮战役口径/仲裁条件词)、拼句兜底与叙述生成(scripted
// 模型走真实 agentcore 会话)。

// seedNarrativeClaim 铺一个"发现型资产 → 发现步骤 → 声明 → 反证"的最小
// 链(声明 detail 带发现单位/证据截头/冻结竞争解释)。
func seedNarrativeClaim(t *testing.T, l *Loop, task string) {
	t.Helper()
	seed := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-disc", NodeType: rings.NodeAsset, Title: "http://127.0.0.1:4461", Status: "active",
			AssetClass: rings.AssetService,
			Detail:     map[string]any{"source": rings.AssetSourceDiscovered, "unit_id": "u0", "discovered_fact": "f-src"}}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "拉取 127.0.0.1:4461 首页与 JS,判定服务类型", Status: "done",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-x", NodeType: rings.NodeTestClaim, Title: "127.0.0.1:4461 的 /static/app.js 暴露硬编码凭据",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1",
				"impact": "凭据可直连管理后台", "evidence_head": "var apiKey=\"sk-live-…\"",
				"standard": arbiter.DefaultClaimStandard(),
				"alternatives": []any{
					map[string]any{"id": "alt1", "label": "缓存污染"},
					map[string]any{"id": "alt2", "label": "不可复现"}}}}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-x", ToNode: "a-disc"}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-x", ToNode: "s1"}},
		// 反证步骤(当前战役)与写回结论。
		{Kind: rings.EventNodeAdded, Actor: "arbiter", Payload: rings.NodeAdded{
			NodeID: "s-f1", NodeType: rings.NodeStep, Title: "反证(第 1 轮): tc-x", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-x", "round": 1}}},
		{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", Type: rings.TestClaimRecFalsify, Round: 1, StepID: "s-f1", UnitID: "u2",
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high",
			Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}}}},
	}
	if _, err := l.Store.Append(context.Background(), task, seed...); err != nil {
		t.Fatalf("铺链失败: %v", err)
	}
}

// seedStepTraffic 给发现步骤铺镜交换:8 条 http(末条带 blob 正文)+ 1 条
// 隧道(无原文,来源载体不成立应跳过)。http/https 留痕账本物理强制带
// blob 引用(R18),每条配最小交换文档。
func seedStepTraffic(t *testing.T, l *Loop, task string) {
	t.Helper()
	for i := 0; i < 8; i++ {
		rec := rings.MirrorRecord{ExchangeID: "mx" + string(rune('0'+i)),
			Type: rings.MirrorRecHTTP, StepID: "s1", UnitID: "u1",
			Method: "GET", URL: "http://127.0.0.1:4461/p" + string(rune('0'+i)), Status: 200, RespBytes: 64}
		ex := mirror.Exchange{Type: rings.MirrorRecHTTP,
			Response: &mirror.Response{Status: 200, Body: []byte("page " + string(rune('0'+i)))}}
		if i == 7 {
			ex.Response = &mirror.Response{Status: 200,
				Headers: []mirror.Header{{Name: "Content-Type", Value: "application/javascript"}},
				Body:    []byte("var apiKey=\"sk-live-abcdef\"")}
			rec.URL = "http://127.0.0.1:4461/static/app.js"
		}
		doc, err := json.Marshal(ex)
		if err != nil {
			t.Fatalf("编码交换原文失败: %v", err)
		}
		if rec.BlobRef, err = l.Swarm.Blobs.Write(doc); err != nil {
			t.Fatalf("写 blob 失败: %v", err)
		}
		if _, err := l.Store.Append(context.Background(), task, rings.Event{
			Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rec}); err != nil {
			t.Fatalf("铺镜留痕失败: %v", err)
		}
	}
	if _, err := l.Store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rings.MirrorRecord{
			ExchangeID: "mt", Type: rings.MirrorRecTunnel, StepID: "s1", UnitID: "u1",
			URL: "http://elsewhere.example:443"}}); err != nil {
		t.Fatalf("铺隧道留痕失败: %v", err)
	}
}

// TestNarrativePack 链上事实包:发现血缘(单位/步骤意图/证据截头)、
// 锚定资产(发现型带出生单位)、来源载体流量(账本序最后 6 条,隧道
// 跳过,blob 正文截头)、反证轮(当前战役,幽灵步骤记录不混入)、
// 仲裁条件词;拼句兜底只述可数事实。
func TestNarrativePack(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p413-pack"
	mkTask(t, store, task)
	l := &Loop{TaskID: task, Store: store, Swarm: &swarm.Executor{Blobs: swarm.NewBlobStore(t.TempDir())}}
	seedNarrativeClaim(t, l, task)
	seedStepTraffic(t, l, task)
	// 幽灵反证记录:StepID 不在战役步骤集(步骤节点不存在),不混入。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "swarm:u9", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", Type: rings.TestClaimRecFalsify, Round: 9, StepID: "s-ghost", UnitID: "u9"}}); err != nil {
		t.Fatalf("铺幽灵反证失败: %v", err)
	}

	j := &arbiter.ClaimJudgement{Verdict: rings.TestClaimRecord{
		Conditions: []rings.ArbiterConditionResult{
			{Kind: "falsify_completed", Satisfied: true},
			{Kind: "alternatives_killed", Satisfied: true},
		}}}
	pack, err := l.narrativePack(ctx, "tc-x", j)
	if err != nil {
		t.Fatalf("事实包装配失败: %v", err)
	}
	if pack.Claim.Statement == "" || pack.Claim.Impact != "凭据可直连管理后台" {
		t.Fatalf("声明两维应入包: %+v", pack.Claim)
	}
	if len(pack.Discoveries) != 1 {
		t.Fatalf("普通声明应单发现环节: %+v", pack.Discoveries)
	}
	d := pack.Discoveries[0]
	if d.UnitID != "u1" || d.StepID != "s1" || !strings.Contains(d.Intent, "判定服务类型") || d.EvidenceHead != "var apiKey=\"sk-live-…\"" {
		t.Fatalf("发现环节字段错误: %+v", d)
	}
	if len(d.Assets) != 1 || d.Assets[0].Source != string(rings.AssetSourceDiscovered) || d.Assets[0].BirthUnit != "u0" {
		t.Fatalf("锚定资产应带发现型出生: %+v", d.Assets)
	}
	// 来源载体:8 条 http 取最后 6(首条=p2),隧道跳过;末条带正文截头
	// 与类型(与 blob 原文一致可对账)。
	if len(d.Exchanges) != 6 {
		t.Fatalf("交换应截最后 6 条: %+v", d.Exchanges)
	}
	if !strings.HasSuffix(d.Exchanges[0].URL, "/p2") {
		t.Fatalf("截留应为账本序最后 6 条: %+v", d.Exchanges[0])
	}
	last := d.Exchanges[5]
	if !strings.HasSuffix(last.URL, "/static/app.js") || last.RespType != "application/javascript" ||
		!strings.Contains(last.BodyHead, "sk-live-abcdef") {
		t.Fatalf("末条交换应带载体正文截头: %+v", last)
	}
	if len(pack.Falsify) != 1 || pack.Falsify[0].Round != 1 || pack.Falsify[0].UnitID != "u2" {
		t.Fatalf("反证轮应只含当前战役: %+v", pack.Falsify)
	}
	if len(pack.Falsify[0].Alternatives) != 2 || pack.Falsify[0].Alternatives[0].Label != "缓存污染" ||
		!pack.Falsify[0].Alternatives[0].Refuted {
		t.Fatalf("竞争解释应带冻结标签与排除结论: %+v", pack.Falsify[0].Alternatives)
	}
	if len(pack.Confirm) != 2 || pack.Confirm[0] != "反证完成" || pack.Confirm[1] != "竞争解释全排除" {
		t.Fatalf("仲裁条件应译为界面词: %+v", pack.Confirm)
	}

	base := deterministicNarrative(pack)
	for _, want := range []string{"单位 u1", "执行「", "1 轮反证", "排除竞争解释 2 条", "仲裁者判定成立"} {
		if !strings.Contains(base, want) {
			t.Fatalf("拼句兜底应含 %q: %s", want, base)
		}
	}
	if strings.Contains(base, "app.js") {
		t.Fatalf("拼句兜底不猜来源载体: %s", base)
	}
}

// TestNarrativePackCombined 组合声明:derived_from → Fact 逐环节入包,
// 事实的发现步骤经 produces 边(对象出生同源);拼句为组合口径。
func TestNarrativePackCombined(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p413-comb"
	mkTask(t, store, task)
	l := &Loop{TaskID: task, Store: store}
	seed := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-1", NodeType: rings.NodeAsset, Title: "http://k3.local", Status: "active", AssetClass: rings.AssetService}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-fa", NodeType: rings.NodeStep, Title: "枚举 k3.local 通用路径", Status: "done",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-fb", NodeType: rings.NodeStep, Title: "验证 k3.local 端点无需凭据", Status: "done",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "/admin/ 无需凭据可访问", Status: "confirmed",
			Detail: map[string]any{"unit_id": "u1", "evidence_head": "HTTP 200 /admin/"}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "f2", NodeType: rings.NodeFact, Title: "/portal 暴露会话令牌", Status: "confirmed",
			Detail: map[string]any{"unit_id": "u2", "evidence_head": "token=... 200"}}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s-fa", ToNode: "f1"}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:u2", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s-fb", ToNode: "f2"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "tc-c", NodeType: rings.NodeTestClaim, Title: "凭据链组合可接管管理后台",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"combined": true, "impact": "接管后台",
				"chain":   []any{map[string]any{"fact_id": "f1", "role": "入口"}, map[string]any{"fact_id": "f2", "role": "凭据"}},
				"unit_id": "overmind", "standard": arbiter.DefaultClaimStandard()}}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-c", ToNode: "f1"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-c", ToNode: "f2"}},
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "tc-c", ToNode: "a-1"}},
	}
	if _, err := store.Append(ctx, task, seed...); err != nil {
		t.Fatalf("铺组合链失败: %v", err)
	}
	pack, err := l.narrativePack(ctx, "tc-c", nil)
	if err != nil {
		t.Fatalf("事实包装配失败: %v", err)
	}
	if !pack.Claim.Combined || len(pack.Discoveries) != 2 {
		t.Fatalf("组合声明应逐链环节入包: %+v", pack)
	}
	if pack.Discoveries[0].FactTitle != "/admin/ 无需凭据可访问" || pack.Discoveries[0].StepID != "s-fa" ||
		pack.Discoveries[0].UnitID != "u1" {
		t.Fatalf("环节应带事实陈述与发现血缘: %+v", pack.Discoveries[0])
	}
	if pack.Discoveries[1].StepID != "s-fb" || !strings.Contains(pack.Discoveries[1].Intent, "验证") {
		t.Fatalf("第二环节血缘错误: %+v", pack.Discoveries[1])
	}
	base := deterministicNarrative(pack)
	if !strings.Contains(base, "主宰组合链上 2 条事实") || !strings.Contains(base, "无反证轮") {
		t.Fatalf("组合拼句口径错误: %s", base)
	}
}

// TestGenClaimNarrative 叙述生成(scripted 模型走真实 agentcore 会话):
// 模型人话覆盖拼句并冻结;坏回复/空 narrative 回退拼句;无 Provider
// 保留拼句零外呼;超长截断。
func TestGenClaimNarrative(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	const modelText = "单位 u1 执行「拉取 127.0.0.1:4461 首页与 JS,判定服务类型」时,在 /static/app.js 的 200 响应正文里发现硬编码凭据;u2 独立反证 1 轮排除缓存污染与不可复现两条解释,仲裁者判定成立。"

	// 每子测独立任务(同库同节点 ID 不冲突)。
	taskSeq := 0
	mkLoop := func(t *testing.T) (*Loop, string) {
		t.Helper()
		taskSeq++
		task := fmt.Sprintf("p413-gen-%d", taskSeq)
		mkTask(t, store, task)
		l := &Loop{TaskID: task, Store: store, Swarm: &swarm.Executor{Blobs: swarm.NewBlobStore(t.TempDir())}}
		seedNarrativeClaim(t, l, task)
		return l, task
	}

	t.Run("正常生成", func(t *testing.T) {
		l, _ := mkLoop(t)
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: "```json\n{\"narrative\":\"" + modelText + "\"}\n```", Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l.Provider = prov
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec)
		if rec.Narrative != modelText {
			t.Fatalf("叙述应为模型生成人话: %q", rec.Narrative)
		}
		reqs := model.requests()
		if len(reqs) == 0 || !strings.Contains(reqs[0].System, "来龙去脉") ||
			!strings.Contains(reqs[0].LastUser, `"discoveries"`) ||
			!strings.Contains(reqs[0].LastUser, "暴露硬编码凭据") {
			t.Fatalf("应向模型提供链上事实包与文风契约: %+v", reqs)
		}
	})

	t.Run("坏回复与空叙述回退拼句", func(t *testing.T) {
		for _, reply := range []string{"这不是 JSON", `{"narrative":"  "}`} {
			l, _ := mkLoop(t)
			model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
				return scriptedReply{Text: reply, Finish: "stop"}
			})
			defer model.Close()
			prov, err := model.provider()
			if err != nil {
				t.Fatalf("起 scripted 模型失败: %v", err)
			}
			l.Provider = prov
			rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
			l.genClaimNarrative(ctx, "tc-x", nil, rec)
			if !strings.Contains(rec.Narrative, "单位 u1") || !strings.Contains(rec.Narrative, "仲裁者判定成立") {
				t.Fatalf("失败应回退确定性拼句(回复 %q): %q", reply, rec.Narrative)
			}
		}
	})

	t.Run("无Provider零外呼", func(t *testing.T) {
		l, _ := mkLoop(t)
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec) // 不应 panic/外呼
		if !strings.Contains(rec.Narrative, "单位 u1") {
			t.Fatalf("无 Provider 应保留拼句: %q", rec.Narrative)
		}
	})

	t.Run("超长截断", func(t *testing.T) {
		l, _ := mkLoop(t)
		long := strings.Repeat("叙述", 400) // 800 字节 > narrativeClip
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"narrative":"` + long + `"}`, Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l.Provider = prov
		rec := &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
		l.genClaimNarrative(ctx, "tc-x", nil, rec)
		if len(rec.Narrative) > narrativeClip+3 || !strings.HasSuffix(rec.Narrative, "…") {
			t.Fatalf("超长应按落账上限截断(len=%d): %q", len(rec.Narrative), rec.Narrative)
		}
	})
}

// TestParseClaimNarrative 解析宽容度:围栏/杂文剥壳,空回复与坏 JSON
// 报错(调用方回退拼句)。
func TestParseClaimNarrative(t *testing.T) {
	if got, err := parseClaimNarrative("```json\n{\"narrative\":\"前言 之后\"}\n```"); err != nil || got != "前言 之后" {
		t.Fatalf("围栏应剥壳: %q %v", got, err)
	}
	if _, err := parseClaimNarrative(""); err == nil {
		t.Fatal("空回复应报错")
	}
	if _, err := parseClaimNarrative("{bad"); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := parseClaimNarrative(`{"narrative":""}`); err == nil {
		t.Fatal("空叙述应报错(回退拼句)")
	}
}
