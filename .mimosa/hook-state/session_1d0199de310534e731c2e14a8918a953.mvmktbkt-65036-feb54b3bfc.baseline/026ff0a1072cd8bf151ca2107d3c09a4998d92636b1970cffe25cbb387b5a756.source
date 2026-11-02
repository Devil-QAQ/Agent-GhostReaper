package shepherd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ---- P4-14 成果追问会话(问题清单 #24) ----

// TestEnqueueClaimLifecycle 追问入队的会话边界:一成果一线、每轮一会话
// (续接不加轮、新开一轮 +1);预载只随该轮首问落账;主对话与追问互不
// 混流;冬眠冻结入口。零 Provider(入队后异步落失败态,不影响断言)。
func TestEnqueueClaimLifecycle(t *testing.T) {
	store := openStore(t)
	const task = "sh-qa-life"
	seedTask(t, store, task)
	s := &Service{Store: store}
	const claim = "tc-qa1"
	const prelude = "【成果】tc-qa1:声明原文\n来龙去脉(定稿叙述):叙述文本"

	if _, _, err := s.EnqueueClaim(context.Background(), task, claim, "  ", false, prelude); err == nil {
		t.Fatalf("空追问应拒绝")
	}

	id1, r1, err := s.EnqueueClaim(context.Background(), task, claim, "第一问", false, prelude)
	if err != nil || r1 != 1 || id1 == "" {
		t.Fatalf("首问应开第 1 轮: id=%s r=%d err=%v", id1, r1, err)
	}
	id2, r2, err := s.EnqueueClaim(context.Background(), task, claim, "第二问", false, prelude)
	if err != nil || r2 != 1 || id2 == "" {
		t.Fatalf("续问应留在第 1 轮: r=%d err=%v", r2, err)
	}
	if _, r3, err := s.EnqueueClaim(context.Background(), task, claim, "新轮第一问", true, prelude); err != nil || r3 != 2 {
		t.Fatalf("新开一轮应到第 2 轮: r=%d err=%v", r3, err)
	}

	// 预载只随该轮首问落账(续问误传的 prelude 丢弃)。
	var preludes []string
	for _, rec := range shepherdEvents(t, store, task) {
		if rec.ClaimID == claim && rec.Type == rings.ShepherdRecUserMessage {
			preludes = append(preludes, rec.Prelude)
		}
	}
	if len(preludes) != 3 || preludes[0] != prelude || preludes[1] != "" || preludes[2] != prelude {
		t.Fatalf("预载应只在每轮首问: %q", preludes)
	}

	// 视图按轮分隔:最新轮只含第 2 轮一问,指定轮回看各自消息。
	v2, err := s.ViewClaim(context.Background(), task, claim, 0)
	if err != nil {
		t.Fatalf("ViewClaim: %v", err)
	}
	if v2.Round != 2 || v2.Rounds != 2 || v2.Questions != 1 || len(v2.Messages) != 1 ||
		v2.Messages[0].Text != "新轮第一问" {
		t.Fatalf("最新轮视图不符: round=%d rounds=%d q=%d msgs=%+v", v2.Round, v2.Rounds, v2.Questions, v2.Messages)
	}
	v1, err := s.ViewClaim(context.Background(), task, claim, 1)
	if err != nil || v1.Round != 1 || v1.Questions != 2 || len(v1.Messages) != 2 {
		t.Fatalf("第 1 轮回看视图不符: %+v err=%v", v1, err)
	}

	// 主对话零混入:牧者主视图不含追问记录;追问视图不含主对话。
	vm, err := s.View(context.Background(), task)
	if err != nil || len(vm.Messages) != 0 {
		t.Fatalf("主对话不应混入追问记录: %+v err=%v", vm, err)
	}
	if _, err := s.Enqueue(context.Background(), task, "主对话消息"); err != nil {
		t.Fatalf("主对话入队: %v", err)
	}
	vm, _ = s.View(context.Background(), task)
	if len(vm.Messages) != 1 {
		t.Fatalf("主对话应恰一条: %+v", vm.Messages)
	}
	v2, _ = s.ViewClaim(context.Background(), task, claim, 0)
	if len(v2.Messages) != 1 || v2.Questions != 1 {
		t.Fatalf("追问视图不应混入主对话: %+v", v2.Messages)
	}
	if got := s.CurrentRound(context.Background(), task, claim); got != 2 {
		t.Fatalf("当前轮应为 2,实为 %d", got)
	}
	if got := s.CurrentRound(context.Background(), task, "tc-other"); got != 0 {
		t.Fatalf("未追问的成果当前轮应为 0,实为 %d", got)
	}

	// 冬眠冻结:入口拒绝。
	gate := hiber.New()
	gate.Enter(task, "creator")
	s2 := &Service{Store: store, Hiber: gate}
	if _, _, err := s2.EnqueueClaim(context.Background(), task, claim, "冬眠中", false, ""); err != ErrHibernated {
		t.Fatalf("冬眠中追问应拒绝,实为 %v", err)
	}
}

// TestClaimQAAnswerScripted 追问应答主径(scripted 模型走真实 agentcore):
// 首问注入预载(成果追问预载 + 【造物主的追问】),续问不再注入;工具
// 调用折叠卡与回复落在追问消息上;转录与 token 行按该轮独立会话锚
// 记账(与牧者主对话分开对账)。
func TestClaimQAAnswerScripted(t *testing.T) {
	store := openStore(t)
	root := t.TempDir()
	const task = "sh-qa-answer"
	seedTask(t, store, task)
	const claim = "tc-qa2"
	const prelude = "【成果】tc-qa2:声明原文\n来龙去脉(定稿叙述):u2 在 JS 里发现硬编码凭据"

	m := newScriptedModel(func(rr *recordedReq) scriptedRep {
		return scriptedRep{Text: "回答" + rr.LastUser[len(rr.LastUser)-6:]}
	})
	defer m.Close()
	prov, err := m.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}
	s := &Service{Store: store, Provider: prov, RootDir: root, Model: "scripted-shepherd-model"}

	waitClaim := func(round, want int) *View {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v, err := s.ViewClaim(context.Background(), task, claim, round)
			if err == nil && len(v.Messages) == want && v.Messages[want-1].Reply != "" {
				return v
			}
			time.Sleep(40 * time.Millisecond)
		}
		v, _ := s.ViewClaim(context.Background(), task, claim, round)
		t.Fatalf("等待追问回复超时: %+v", v)
		return nil
	}

	if _, _, err := s.EnqueueClaim(context.Background(), task, claim, "怎么发现的?", false, prelude); err != nil {
		t.Fatalf("首问入队: %v", err)
	}
	v := waitClaim(1, 1)
	if v.Messages[0].State != "replied" {
		t.Fatalf("追问回复后状态应为 replied,实为 %s", v.Messages[0].State)
	}

	reqs := m.requests()
	if len(reqs) == 0 {
		t.Fatalf("无模型请求")
	}
	first := reqs[0].LastUser
	if !strings.Contains(first, "(成果追问预载)") || !strings.Contains(first, prelude) ||
		!strings.Contains(first, "【造物主的追问】怎么发现的?") {
		t.Fatalf("首问注入应含预载与追问原文: %q", first)
	}

	if _, _, err := s.EnqueueClaim(context.Background(), task, claim, "影响面还有吗?", false, ""); err != nil {
		t.Fatalf("续问入队: %v", err)
	}
	waitClaim(1, 2)
	reqs = m.requests()
	if len(reqs) < 2 {
		t.Fatalf("续问应有新的模型请求,实为 %d", len(reqs))
	}
	last := reqs[len(reqs)-1].LastUser
	if strings.Contains(last, "(成果追问预载)") {
		t.Fatalf("续问不应重复注入预载: %q", last)
	}
	if !strings.Contains(last, "影响面还有吗?") {
		t.Fatalf("续问原文应注入: %q", last)
	}

	// 转录落盘:该轮独立会话锚的 JSONL 在位。
	sid := ClaimQASID(claim, 1)
	if _, err := os.Stat(filepath.Join(root, task, "transcripts") + "/" + sid + ".jsonl"); err != nil {
		t.Fatalf("追问会话转录缺失(%s): %v", sid, err)
	}

	// token 行:agent=shepherd、转录锚 = 追问会话(与主对话分开对账)。
	rows, err := store.TokenUsageRows(context.Background(), task)
	if err != nil {
		t.Fatalf("读 token 行: %v", err)
	}
	qaRows, mainRows := 0, 0
	for _, x := range rows {
		if x.Agent != ActorShepherd {
			continue
		}
		switch x.TranscriptID {
		case sid:
			qaRows++
		case SessionID(task):
			mainRows++
		}
	}
	if qaRows == 0 {
		t.Fatalf("追问会话应有 token 行(锚 %s)", sid)
	}
	if mainRows != 0 {
		t.Fatalf("主对话无消息不应有 token 行,实为 %d", mainRows)
	}

	// 主对话视图仍为零(独立留存,互不污染)。
	vm, _ := s.View(context.Background(), task)
	if len(vm.Messages) != 0 {
		t.Fatalf("主对话不应混入追问: %+v", vm.Messages)
	}
}

// TestClaimQAAdoption 重启收养:已送达未回复的追问消息(进程中断签名)
// 由新进程结案为失败;排队中的续答。手工铺事件驱动,确定性。
func TestClaimQAAdoption(t *testing.T) {
	store := openStore(t)
	root := t.TempDir()
	const task = "sh-qa-adopt"
	seedTask(t, store, task)
	const claim = "tc-qa3"

	m := newScriptedModel(func(rr *recordedReq) scriptedRep { return scriptedRep{Text: "回答"} })
	defer m.Close()
	prov, err := m.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}

	// 旧进程形态:一问已送达未回复(中断),一问仍排队。
	append := func(rec rings.ShepherdRecord) {
		if _, err := store.Append(context.Background(), task, rings.Event{
			Kind: rings.EventShepherdRecord, Actor: ActorShepherd, Payload: rec,
		}); err != nil {
			t.Fatalf("铺事件: %v", err)
		}
	}
	append(rings.ShepherdRecord{MessageID: "sm-broken", Type: rings.ShepherdRecUserMessage,
		Text: "中断的一问", ClaimID: claim, Round: 1})
	append(rings.ShepherdRecord{MessageID: "sm-broken", Type: rings.ShepherdRecMessageState,
		ClaimID: claim, Round: 1, State: rings.ShepherdMsgDelivered})
	append(rings.ShepherdRecord{MessageID: "sm-queued", Type: rings.ShepherdRecUserMessage,
		Text: "排队的一问", ClaimID: claim, Round: 1})

	s := &Service{Store: store, Provider: prov, RootDir: root, Model: "scripted-shepherd-model"}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("收养启动: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		v, err := s.ViewClaim(context.Background(), task, claim, 1)
		if err == nil && len(v.Messages) == 2 &&
			v.Messages[0].State == "failed" && strings.Contains(v.Messages[0].Note, "应答中断") &&
			v.Messages[1].Reply != "" {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	v, _ := s.ViewClaim(context.Background(), task, claim, 1)
	t.Fatalf("收养结果不符: %+v", v.Messages)
}
