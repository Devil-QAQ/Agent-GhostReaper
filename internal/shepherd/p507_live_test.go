package shepherd

// P5-07②【模型】活体:单一追问入口问与吩咐分流——真实模型
//(deepseek-flash,经 P507_LIVE_KEY 环境变量注入,不落盘)锚定一条
// 已确认漏洞:①问一句,牧者带链路上下文作答(零动作);②吩咐一句
// "从这里继续深入,方向拿账号",牧者落实成动作(urgent_intent 派发,
// 图上新增高优先级意图步骤)。零 key 时跳过。
// 跑法:P507_LIVE_KEY=sk-... go test -run TestP507LiveFollowupAskAndOrder -v -timeout 15m

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

func TestP507LiveFollowupAskAndOrder(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("P507_LIVE_KEY"))
	if key == "" {
		t.Skip("P507_LIVE_KEY 未配置,跳过活体(真实 LLM 行为验收)")
	}
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const task = "p507-shep-live"

	// 铺一条已确认漏洞(链路上下文:接口、凭据、单位、确认时刻齐备)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P5-07 追问活体"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: "t.example.com"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "验证后台弱口令", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-live", NodeType: rings.NodeTestClaim, Status: rings.TestClaimCandidate,
			Title: "t.example.com /api/login 弱口令 admin 可登录并返回令牌",
			Detail: map[string]any{"severity": "high", "standard": "s", "step_id": "s1", "unit_id": "u1"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-live", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-live", ToNode: "web1"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	prov, err := llm.NewProvider(llm.Config{
		APIKey: key, BaseURL: "https://api.deepseek.com",
		Format: "openai", Model: "deepseek-flash",
	})
	if err != nil {
		t.Fatalf("构建真实模型 provider 失败: %v", err)
	}
	s := &Service{Store: store, Provider: prov, RootDir: dir, Model: "deepseek-flash"}

	// 预载与面板追问窗同源口径(链路摘要 + 双语义接待纪律)。
	prelude := "【成果】tc-live: t.example.com /api/login 弱口令 admin 可登录并返回令牌\n" +
		"发现单位:u1(步骤 s1)\n确认时刻:已在账本\n" +
		"应答纪律:围绕该条目接待造物主。造物主问:先查账本(read_findings / read_progress)再答。造物主吩咐(如\"继续深入\"\"方向拿账号\"):用 urgent_intent 登记高优先级意图落实成动作,并在答复里说明派发了什么。"

	waitReply := func(round int) string {
		deadline := time.Now().Add(6 * time.Minute)
		for time.Now().Before(deadline) {
			v, verr := s.ViewClaim(ctx, task, "tc-live", round)
			if verr == nil && len(v.Messages) > 0 && v.Messages[len(v.Messages)-1].Reply != "" {
				return v.Messages[len(v.Messages)-1].Reply
			}
			time.Sleep(2 * time.Second)
		}
		return ""
	}
	stepCount := func() int {
		steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
		if err != nil {
			t.Fatalf("查步骤失败: %v", err)
		}
		return len(steps)
	}

	// ---- ① 问一句:牧者答(带链路上下文),零动作 ----
	question := "这个漏洞在哪个接口?用什么凭据可以登录?"
	if _, _, err := s.EnqueueClaim(ctx, task, "tc-live", question, false, prelude); err != nil {
		t.Fatalf("追问入队失败: %v", err)
	}
	reply := waitReply(1)
	if reply == "" {
		t.Fatalf("① 牧者应答超时")
	}
	t.Logf("① 问的应答:\n%s", reply)
	if !strings.Contains(reply, "/api/login") {
		t.Fatalf("① 答复应含链路上下文里的接口 /api/login:\n%s", reply)
	}
	if !strings.Contains(reply, "admin") {
		t.Fatalf("① 答复应含凭据账号 admin:\n%s", reply)
	}
	if n := stepCount(); n != 1 {
		t.Fatalf("① 问句不得产生动作(步骤应仍 1 个,得 %d)", n)
	}

	// ---- ② 吩咐一句:牧者落实成动作(急活派发,图上新增意图步骤) ----
	order := "从这里继续深入,方向拿账号"
	if _, round, err := s.EnqueueClaim(ctx, task, "tc-live", order, true, prelude); err != nil {
		t.Fatalf("吩咐入队失败: %v", err)
	} else if round != 2 {
		t.Logf("吩咐落第 %d 轮(预期新开第 2 轮)", round)
	}
	orderReply := waitReply(2)
	if orderReply == "" {
		t.Fatalf("② 牧者应答超时")
	}
	t.Logf("② 吩咐的应答:\n%s", orderReply)
	// 动作落地:新意图步骤入图(高优先级急活)。
	if n := stepCount(); n <= 1 {
		t.Fatalf("② 吩咐应落实成动作(urgent_intent 派发,步骤应新增,得 %d):\n%s", n, orderReply)
	}
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	var urgent *rings.NodeFull
	for i := range steps {
		if steps[i].ID != "s1" {
			urgent = &steps[i]
		}
	}
	if urgent == nil {
		t.Fatalf("② 未找到新派意图步骤:\n%s", orderReply)
	}
	t.Logf("② 落实的动作:步骤 %s(意图:%s)", urgent.ID, urgent.Title)
	if v, _ := urgent.Detail["urgent"].(bool); !v {
		t.Fatalf("② 新步骤应为高优先级急活(detail.urgent): %+v", urgent.Detail)
	}
}
