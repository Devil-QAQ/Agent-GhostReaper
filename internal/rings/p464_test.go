package rings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// P4-64(问题清单 #82)显示层包装族的账本与投影链路类级测试:
//   - claim.round_human 按轮 upsert(同轮覆盖、跨轮追加、轮号全局续号、
//     重开增量补)、空字段拒收;
//   - claim.summary 与 repro 事件同列 latest-wins(立账在先、定稿覆盖、
//     定稿空字段清空——取代语义既有);
//   - fact.text / hypo.text → node_polish latest-wins(整行覆盖);
//   - claim.retest_note 按轮 upsert;
//   - 重放确定性(同事件流重建投影一致)与存量兼容(无新事件零变化)。

func p464SeedClaim(t *testing.T, s *Store, task string) {
	t.Helper()
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
		NodeID: "tc-x", NodeType: NodeTestClaim, Title: "原始存在性判断原文", Status: TestClaimTesting,
		Detail: map[string]any{"unit_id": "u1"}}},
	)
}

func TestP464RoundHumanUpsertByRound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-rh"
	p464SeedClaim(t, s, task)
	mustAppend(t, s, task,
		Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: ClaimRoundHuman{
			ClaimID: "tc-x", Round: 3, Doubt: "会不会是缓存造成的假象?", How: "连续 8 次请求内容完全一致", Conclusion: "已排除"}},
		Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: ClaimRoundHuman{
			ClaimID: "tc-x", Round: 4, Doubt: "会不会是当时请求没有到达目标?", How: "改用直连路径重发 5 次", Conclusion: "未排除"}},
	)
	// 同轮覆盖(轮 3 重派后重新生成)、重开后新轮次(轮 7)增量补。
	mustAppend(t, s, task,
		Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: ClaimRoundHuman{
			ClaimID: "tc-x", Round: 3, Doubt: "会不会是缓存造成的假象?", How: "绕开缓存直接请求,连续 8 次内容一致", Conclusion: "已排除", Unpolished: true}},
		Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: ClaimRoundHuman{
			ClaimID: "tc-x", Round: 7, Doubt: "会不会是共享组件?", How: "对照相邻资产的同路径", Conclusion: "推翻"}},
	)
	proj, ok, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil || !ok {
		t.Fatalf("ClaimCaseOf: ok=%v err=%v", ok, err)
	}
	if len(proj.RoundHumans) != 3 {
		t.Fatalf("应按轮 upsert 为 3 轮(3/4/7),得到 %d: %+v", len(proj.RoundHumans), proj.RoundHumans)
	}
	if r := proj.RoundHumans[3]; !r.Unpolished || !strings.Contains(r.How, "绕开缓存") {
		t.Fatalf("轮 3 应为覆盖后的新稿(含 Unpolished): %+v", r)
	}
	if r := proj.RoundHumans[4]; r.Conclusion != "未排除" {
		t.Fatalf("轮 4 应保留: %+v", r)
	}
	if r := proj.RoundHumans[7]; r.Conclusion != "推翻" {
		t.Fatalf("轮 7(重开后新轮)应增量补: %+v", r)
	}
}

func TestP464RoundHumanRejectsEmpty(t *testing.T) {
	s := openTestStore(t)
	const task = "p464-rh-empty"
	p464SeedClaim(t, s, task)
	// 空三段(全空)与缺轮次一律拒收——不落空事件。
	for _, bad := range []ClaimRoundHuman{
		{ClaimID: "tc-x", Round: 1},
		{ClaimID: "tc-x"},
		{Round: 2, Doubt: "x"},
	} {
		if _, err := s.Append(ctxOf(), task, Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: bad}); err == nil {
			t.Fatalf("空字段负载应拒收: %+v", bad)
		}
	}
}

func TestP464LaunchSummaryLatestWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-sum"
	p464SeedClaim(t, s, task)
	// 立账摘要(claim.summary)先落。
	mustAppend(t, s, task, Event{Kind: EventClaimSummaryPol, Actor: ActorOvermindSafe, Payload: ClaimSummaryPolish{
		ClaimID: "tc-x", Statement: "立账批次的摘要:管理接口没有登录校验"}})
	digests, err := s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("ClaimDigests: %v", err)
	}
	if digests["tc-x"].Statement != "立账批次的摘要:管理接口没有登录校验" {
		t.Fatalf("立账摘要应透出: %+v", digests["tc-x"])
	}
	// 定稿 repro(带摘要)后落 → 覆盖(账本次序)。
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: TestClaimRecord{
		ClaimID: "tc-x", Type: TestClaimRecRepro, Severity: "high",
		Statement: "定稿批次的摘要", Impact: "影响面定稿",
		Steps: []ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2", Method: "GET",
			URL: "http://k3.local/admin", Status: 200, BlobRef: "b1"}}}})
	digests, err = s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("ClaimDigests(定稿): %v", err)
	}
	if digests["tc-x"].Statement != "定稿批次的摘要" || digests["tc-x"].Impact != "影响面定稿" {
		t.Fatalf("定稿摘要应覆盖立账稿: %+v", digests["tc-x"])
	}
	// 再定稿未生成摘要 → 清空(取代语义既有,P4-59 口径不破)。
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: TestClaimRecord{
		ClaimID: "tc-x", Type: TestClaimRecRepro, Severity: "high",
		Steps: []ReproStep{{Seq: 1, StepID: "s2", UnitID: "u3", Method: "GET",
			URL: "http://k3.local/admin", Status: 200, BlobRef: "b2"}}}})
	digests, err = s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("ClaimDigests(清空): %v", err)
	}
	if digests["tc-x"].Statement != "" {
		t.Fatalf("定稿未生成摘要应清空(不残留立账稿): %+v", digests["tc-x"])
	}
	// 空字段 claim.summary 拒收。
	if _, err := s.Append(ctxOf(), task, Event{Kind: EventClaimSummaryPol, Actor: ActorOvermindSafe,
		Payload: ClaimSummaryPolish{ClaimID: "tc-x"}}); err == nil {
		t.Fatalf("空字段摘要应拒收")
	}
}

func TestP464NodePolishLatestWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-np"
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f-1", NodeType: NodeFact, Title: "原始事实陈述", Status: "confirmed"}},
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "h-1", NodeType: NodeHypothesis, Title: "原始猜测陈述", Status: HypothesisCandidate,
			Detail: map[string]any{"basis": "弱依据原文"}}},
		Event{Kind: EventFactText, Actor: ActorOvermindSafe, Payload: FactTextPolish{
			FactID: "f-1", Title: "10.0.0.5 的备份接口无需登录即可下载", Statement: "备份接口没有访问限制,任何知道地址的人都能下载"}},
		Event{Kind: EventHypoText, Actor: ActorOvermindSafe, Payload: HypoTextPolish{
			HypothesisID: "h-1", Title: "example.com:8080 的管理后台可能有弱口令", Basis: "登录页无验证码且口令策略宽松", Reason: "值得跟:先验弱口令再验影响面"}},
	)
	np, err := s.NodePolish(ctx, task)
	if err != nil {
		t.Fatalf("NodePolish: %v", err)
	}
	if len(np) != 2 {
		t.Fatalf("应有两行包装: %+v", np)
	}
	if np["f-1"].Title == "" || np["f-1"].Statement == "" || np["f-1"].Basis != "" || np["f-1"].Reason != "" {
		t.Fatalf("事实包装只应有标题与描述: %+v", np["f-1"])
	}
	if np["h-1"].Title == "" || np["h-1"].Basis == "" || np["h-1"].Reason == "" || np["h-1"].Statement != "" {
		t.Fatalf("假设包装只应有标题/依据/理由: %+v", np["h-1"])
	}
	// 二次评估(latest-wins 整行覆盖):新事件覆盖旧包装。
	mustAppend(t, s, task, Event{Kind: EventHypoText, Actor: ActorOvermindSafe, Payload: HypoTextPolish{
		HypothesisID: "h-1", Title: "example.com:8080 的管理后台存在弱口令", Reason: "验证成立,迁入事实页"}})
	np, err = s.NodePolish(ctx, task)
	if err != nil {
		t.Fatalf("NodePolish(覆盖): %v", err)
	}
	if np["h-1"].Title != "example.com:8080 的管理后台存在弱口令" || np["h-1"].Basis != "" {
		t.Fatalf("假设包装应整行覆盖(旧 basis 不残留): %+v", np["h-1"])
	}
	// 全空字段拒收。
	if _, err := s.Append(ctxOf(), task, Event{Kind: EventFactText, Actor: ActorOvermindSafe,
		Payload: FactTextPolish{FactID: "f-1"}}); err == nil {
		t.Fatalf("空字段事实文本应拒收")
	}
}

func TestP464RetestNoteUpsertByRound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-rn"
	p464SeedClaim(t, s, task)
	mustAppend(t, s, task,
		Event{Kind: EventClaimRetextNote, Actor: ActorOvermindSafe, Payload: RetestNotePolish{
			ClaimID: "tc-x", Round: 1, Note: "第 1 轮复测:备份列举仍可访问,未修复"}},
		Event{Kind: EventClaimRetextNote, Actor: ActorOvermindSafe, Payload: RetestNotePolish{
			ClaimID: "tc-x", Round: 2, Note: "第 2 轮复测:备份接口已加登录校验"}},
	)
	proj, ok, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil || !ok {
		t.Fatalf("ClaimCaseOf: ok=%v err=%v", ok, err)
	}
	if len(proj.RetestNotes) != 2 || proj.RetestNotes[1] == "" || proj.RetestNotes[2] == "" {
		t.Fatalf("复测备注应按轮 upsert: %+v", proj.RetestNotes)
	}
	// 空备注/缺轮次拒收。
	if _, err := s.Append(ctxOf(), task, Event{Kind: EventClaimRetextNote, Actor: ActorOvermindSafe,
		Payload: RetestNotePolish{ClaimID: "tc-x", Round: 3}}); err == nil {
		t.Fatalf("空备注应拒收")
	}
}

func TestP464ReplayDeterminismAndLegacyCompat(t *testing.T) {
	// 重放确定性:同一事件流重建投影与增量投影一致;存量兼容:迁移后
	// 无新事件的账本读侧零变化(空表/空列)。
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-replay"
	p464SeedClaim(t, s, task)
	mustAppend(t, s, task,
		Event{Kind: EventClaimRoundHuman, Actor: ActorOvermindSafe, Payload: ClaimRoundHuman{
			ClaimID: "tc-x", Round: 1, Doubt: "会不会是缓存?", How: "直连重发 5 次一致", Conclusion: "已排除"}},
		Event{Kind: EventClaimSummaryPol, Actor: ActorOvermindSafe, Payload: ClaimSummaryPolish{
			ClaimID: "tc-x", Statement: "立账摘要"}},
		Event{Kind: EventFactText, Actor: ActorOvermindSafe, Payload: FactTextPolish{
			FactID: "tc-x", Title: "标题短句", Statement: "描述"}},
		Event{Kind: EventClaimRetextNote, Actor: ActorOvermindSafe, Payload: RetestNotePolish{
			ClaimID: "tc-x", Round: 1, Note: "复测备注校对稿"}},
	)
	before, _, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil {
		t.Fatalf("ClaimCaseOf(before): %v", err)
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("RebuildProjections: %v", err)
	}
	after, _, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil {
		t.Fatalf("ClaimCaseOf(after): %v", err)
	}
	bb, _ := json.Marshal(before)
	ab, _ := json.Marshal(after)
	if string(bb) != string(ab) {
		t.Fatalf("重放投影不一致:\n%s\n%s", bb, ab)
	}
	np, err := s.NodePolish(ctx, task)
	if err != nil || len(np) != 1 || np["tc-x"].Title != "标题短句" {
		t.Fatalf("node_polish 重建不符: %+v err=%v", np, err)
	}
	// 存量兼容:全新任务无新事件 → 零包装、零人话,读侧退原文。
	mustAppend(t, s, "p464-legacy", Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
		NodeID: "tc-old", NodeType: NodeTestClaim, Title: "旧判断原文", Status: TestClaimConfirmed}})
	proj, _, err := s.ClaimCaseOf(ctx, "p464-legacy", "tc-old")
	if err != nil {
		t.Fatalf("ClaimCaseOf(legacy): %v", err)
	}
	if len(proj.RoundHumans) != 0 || len(proj.RetestNotes) != 0 || proj.Summary != nil {
		t.Fatalf("存量账本应零包装: %+v", proj)
	}
	if np, _ := s.NodePolish(ctx, "p464-legacy"); len(np) != 0 {
		t.Fatalf("存量账本应零节点包装: %+v", np)
	}
}

func ctxOf() context.Context { return context.Background() }
