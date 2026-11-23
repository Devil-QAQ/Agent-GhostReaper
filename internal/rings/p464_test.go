package rings

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// P4-64(问题清单 #82)显示层包装族的账本与投影链路类级测试:
//   - claim.round_human 按轮 upsert(同轮覆盖、跨轮追加、轮号全局续号、
//     重开增量补)、空字段拒收;
//   - claim.summary 与 repro 事件同列 latest-wins(立账在先、定稿覆盖、
//     定稿空字段清空——取代语义既有);
//   - fact.text / step.title → node_polish latest-wins(整行覆盖);
//   - claim.retest_note 按轮 upsert;
//   - 重放确定性(同事件流重建投影一致)与存量兼容(无新事件零变化)。

func p464SeedClaim(t *testing.T, s *Store, task string) {
	t.Helper()
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
		NodeID: "tc-x", NodeType: NodeTestClaim, Title: "原始存在性判断原文", Status: TestClaimCandidate,
		Detail: map[string]any{"unit_id": "u1"}}},
	)
}

// (TestP464RoundHumanUpsertByRound 已随逐轮人话链路退役删除,P5-03③。)
// (TestP464RoundHumanRejectsEmpty 已随逐轮人话链路退役删除,P5-03③。)
// (TestP464LaunchSummaryLatestWins 已随立账摘要批次退役删除,P5-03③
// ——摘要只留确认定稿批次。)

func TestP464NodePolishLatestWins(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p464-np"
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f-1", NodeType: NodeFact, Title: "原始事实陈述", Status: "confirmed"}},

		Event{Kind: EventFactText, Actor: ActorOvermindSafe, Payload: FactTextPolish{
			FactID: "f-1", Title: "10.0.0.5 的备份接口无需登录即可下载", Statement: "备份接口没有访问限制,任何知道地址的人都能下载"}},
	)
	np, err := s.NodePolish(ctx, task)
	if err != nil {
		t.Fatalf("NodePolish: %v", err)
	}
	if len(np) != 1 {
		t.Fatalf("应有两行包装: %+v", np)
	}
	if np["f-1"].Title == "" || np["f-1"].Statement == "" {
		t.Fatalf("事实包装只应有标题与描述: %+v", np["f-1"])
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
		// (旧 EventClaimRoundHuman 已退役,P5-03③;重放确定性由其余
		// 包装事件族覆盖,未知旧种类在投影按忽略处理。)
		// (旧 EventClaimSummaryPol 立账批次事件已退役,P5-03③;摘要随
		// repro 定稿写 summary 列,重放确定性由其余包装事件族覆盖。)
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
	if len(proj.RetestNotes) != 0 || proj.Summary != nil {
		t.Fatalf("存量账本应零包装: %+v", proj)
	}
	if np, _ := s.NodePolish(ctx, "p464-legacy"); len(np) != 0 {
		t.Fatalf("存量账本应零节点包装: %+v", np)
	}
}

// TestP503LegacyPayloadTolerance P5-03④ 选 A 定案:不做旧数据搬家、不
// 留兼容层——读旧载荷仅做"缺字段按空处理不崩"的容错(不做翻译)。
// 旧反证写回(结构化字段已删的空值形态)、旧逐轮人话事件(种类已
// 退役)、旧 repro rounds 字段:重放投影与卷宗/报告读侧不崩、如实
// 退空。
func TestP503LegacyPayloadTolerance(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p503-legacy"
	p464SeedClaim(t, s, task)
	mustAppend(t, s, task,
		// 旧反证写回:结构化结论字段已物理删除,旧形态按空值解码。
		Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: TestClaimRecord{
			ClaimID: "tc-x", StepID: "s-f1", UnitID: "u2", Type: TestClaimRecFalsify, Round: 1,
			EvidenceHead: "旧格式证据截头"}},
		// 旧 repro 定稿:携带已删除的 rounds 字段,解码按未知字段忽略。
		Event{Kind: EventTestClaimRecord, Actor: ActorOvermindSafe, Payload: json.RawMessage(
			`{"claim_id":"tc-x","type":"repro","severity":"high","statement":"定稿描述",
			  "rounds":[{"round":1,"doubt":"…","how":"…","conclusion":"已排除"}],
			  "steps":[{"seq":1,"step_id":"s1","unit_id":"u2","method":"GET","url":"http://t/","status":200}]}`)},
	)
	// 旧逐轮人话事件:种类已退役(当前入口拒收),存量旧账本里已有——
	// 直插事件行模拟旧库(重放按未知种类忽略,不崩)。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(
		`INSERT INTO events (task_id, revision, kind, actor, payload, occurred_at, recorded_at)
		 VALUES (?, (SELECT IFNULL(MAX(revision),0)+1 FROM events e WHERE e.task_id = ?),
		         'claim.round_human', 'overmind', ?, ?, ?)`,
		task, task,
		`{"claim_id":"tc-x","round":1,"doubt":"会不会是缓存?","how":"直连重发","conclusion":"已排除"}`,
		now, now); err != nil {
		t.Fatalf("直插旧种类事件失败: %v", err)
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("旧载荷重放投影不崩: %v", err)
	}
	proj, ok, err := s.ClaimCaseOf(ctx, task, "tc-x")
	if err != nil || !ok {
		t.Fatalf("旧载荷卷宗读侧不崩: ok=%v err=%v", ok, err)
	}
	if proj.Repro == nil || proj.Repro.Severity != "high" || len(proj.Repro.Steps) != 1 {
		t.Fatalf("旧 repro 定稿应照读(忽略 rounds 字段): %+v", proj.Repro)
	}
	if proj.Summary == nil || proj.Summary.Statement != "定稿描述" {
		t.Fatalf("定稿摘要应照读: %+v", proj.Summary)
	}
}

func ctxOf() context.Context { return context.Background() }
