package shepherd

// P5-04⑥【模型】活体:真实模型(deepseek-flash,经 P504_LIVE_KEY 环境变量
// 注入,不落盘)成果追问"给我这个的数据包"——牧者用流量查询三件从镜
// 里捞出原始报文直接作答(不再"账本里没有"式答不上、不再登记急活
// 重放,问题清单 #94)。零 key 时跳过。
// 跑法:P504_LIVE_KEY=sk-... go test -run TestP504LiveShepherdMirror -v -timeout 15m

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestP504LiveShepherdMirror(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("P504_LIVE_KEY"))
	if key == "" {
		t.Skip("P504_LIVE_KEY 未配置,跳过活体(真实 LLM 行为验收)")
	}
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const task = "p504-shep-live"

	// 铺一条已确认漏洞 + 它的证明流量(镜留痕,含可对账的原始报文)。
	blobs := swarm.NewBlobStore(filepath.Join(dir, "blobs"))
	const exID = "ms-live.1"
	ex := mirror.Exchange{
		ID: exID, Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s1", UnitID: "u1",
		Request: &mirror.Request{Method: "POST", URL: "http://t.example.com/api/login", Proto: "HTTP/1.1",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "application/json"}},
			Body:    []byte(`{"user":"admin","pass":"ad@min2026"}`)},
		Response: &mirror.Response{Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "application/json"}},
			Body:    []byte(`{"token":"sk-live-88990011"}`)},
	}
	rawEx, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("编码交换文档失败: %v", err)
	}
	ref, err := blobs.Write(rawEx)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P5-04 牧者查镜活体"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: "t.example.com"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-live", NodeType: rings.NodeTestClaim, Status: "confirmed",
			Title: "t.example.com /api/login 弱口令 admin 可登录并返回令牌",
			Detail: map[string]any{"severity": "high", "standard": "s"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-live", ToNode: "web1"}},
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rings.MirrorRecord{
			ExchangeID: exID, Type: rings.MirrorRecHTTP, StepID: "s1", UnitID: "u1",
			Method: "POST", URL: "http://t.example.com/api/login", Status: 200, BlobRef: ref}},
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
	s := &Service{Store: store, Provider: prov, RootDir: dir, Model: "deepseek-flash", Blobs: blobs}

	prelude := "【成果】tc-live: t.example.com /api/login 弱口令 admin 可登录并返回令牌"
	question := "给我这个的数据包:请求方法和返回原文是什么?"
	if _, _, err := s.EnqueueClaim(ctx, task, "tc-live", question, false, prelude); err != nil {
		t.Fatalf("追问入队失败: %v", err)
	}

	// 等牧者应答(真实模型,给足窗口)。
	deadline := time.Now().Add(8 * time.Minute)
	var reply string
	for time.Now().Before(deadline) {
		v, verr := s.ViewClaim(ctx, task, "tc-live", 1)
		if verr == nil && len(v.Messages) > 0 && v.Messages[len(v.Messages)-1].Reply != "" {
			reply = v.Messages[len(v.Messages)-1].Reply
			break
		}
		time.Sleep(2 * time.Second)
	}
	if reply == "" {
		t.Fatalf("⑥ 牧者应答超时")
	}
	t.Logf("⑥ 牧者应答:\n%s", reply)

	// 直接作答:请求方法与原文关键内容在答复里(从镜里捞的,不是猜的)。
	lower := strings.ToLower(reply)
	if !strings.Contains(reply, "POST") {
		t.Fatalf("⑥ 答复应含请求方法 POST:\n%s", reply)
	}
	if !strings.Contains(reply, "88990011") && !strings.Contains(reply, "ad@min2026") {
		t.Fatalf("⑥ 答复应含原始报文关键内容(令牌或凭据原文):\n%s", reply)
	}
	if strings.Contains(lower, "账本里没有") || strings.Contains(reply, "没有存原始报文") {
		t.Fatalf("⑥ 牧者不得再答'账本里没有'(镜里有全量留痕,配了查镜工具)")
	}
	// 不再派急活重放:追问期间零新增步骤(urgent 派发会落 step 节点)。
	steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatalf("查步骤失败: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("⑥ 追问答出原文后不得登记急活重放(新步骤 %d 个)", len(steps))
	}
}
