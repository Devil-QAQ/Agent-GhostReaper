package overmind

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-35 ④(问题清单 #57 ②):存储瘦身确定性单测——枚举型反证(成千
// 万条同型交换 + 同一 Step 的海量工具调用)下定稿记录保持 KB 级:
// 过程流量只记引用清单、命令串总量封顶、变体不再内嵌。

// TestAssembleReproPayloadSlim 枚举形态(300 条同型交换 + 2000 条工具
// 调用)下定稿 payload 有界:Steps 恰 1(同型收一条)、Variants 零、
// Process 全量 300 条引用、Op 封顶、JSON 落账体积 KB 级。
func TestAssembleReproPayloadSlim(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p435-slim"
	mkTask(t, store, task)
	seedScopeAssets(t, store, task, "http://enum.local")

	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "manager", Payload: rings.NodeAdded{
			NodeID: "s-e", NodeType: rings.NodeStep, Status: "done",
			Detail: map[string]any{"testclaim_id": "tc-e", "round": 1},
		},
	}); err != nil {
		t.Fatalf("铺反证步骤失败: %v", err)
	}
	// 2000 条工具调用(枚举试错)批量入账。
	calls := make([]rings.Event, 0, 2000)
	for i := 0; i < 2000; i++ {
		calls = append(calls, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s-e", UnitID: "u1", Type: rings.SwarmRecToolCall,
				ToolName: "http_probe",
				ToolArgs: json.RawMessage(`{"url":"http://enum.local/code?n=` + strconv.Itoa(i) + `"}`)}})
	}
	if _, err := store.Append(ctx, task, calls...); err != nil {
		t.Fatalf("铺工具调用失败: %v", err)
	}
	// 300 条同型交换(同方法+路径+状态,查询串值差异)。
	exs := make([]rings.Event, 0, 300)
	for i := 0; i < 300; i++ {
		exs = append(exs, rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror",
			Payload: rings.MirrorRecord{ExchangeID: "e" + strconv.Itoa(i), Type: rings.MirrorRecHTTP,
				StepID: "s-e", UnitID: "u1", Method: "GET",
				URL: "http://enum.local/code?n=" + strconv.Itoa(i), Status: 200, BlobRef: "b" + strconv.Itoa(i)}})
	}
	if _, err := store.Append(ctx, task, exs...); err != nil {
		t.Fatalf("铺镜留痕失败: %v", err)
	}

	got, _, err := AssembleRepro(ctx, store, nil, nil, task, "tc-e", "high")
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("同型 300 条应收恰 1 条候选: %d", len(got.Steps))
	}
	if len(got.Variants) != 0 {
		t.Fatalf("新口径不填充 Variants: %d", len(got.Variants))
	}
	if len(got.Process) != 300 {
		t.Fatalf("过程清单应全量 300 条: %d", len(got.Process))
	}
	op := got.Steps[0].Op
	if op == "" || len(op) > reproOpClip+64 {
		t.Fatalf("命令串应封顶在 %d 字符内(含省略标注): %d", reproOpClip, len(op))
	}
	if !strings.Contains(op, "共 2000 条命令,余略") {
		t.Fatalf("超长命令串应如实标注总条数: %s", clipLine(op, 80))
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化定稿失败: %v", err)
	}
	if len(payload) > 128*1024 {
		t.Fatalf("定稿 payload 应 KB 级(旧口径同形态为百 MB 级): %d 字节", len(payload))
	}
}

// TestJoinOps 命令拼接:少量命令全量、超长封顶标注总条数。
func TestJoinOps(t *testing.T) {
	if got := joinOps([]string{"a", "b"}); got != "a; b" {
		t.Fatalf("少量命令应全量拼接: %q", got)
	}
	long := make([]string, 100)
	for i := range long {
		long[i] = strings.Repeat("x", 100)
	}
	got := joinOps(long)
	if len(got) > reproOpClip+64 || !strings.Contains(got, "共 100 条命令,余略") {
		t.Fatalf("超长应封顶并标注: len=%d 尾=%q", len(got), clipLine(got, 60))
	}
}

// TestReproStepShort 确定性短名兜底:方法+路径(含查询串),无方法退路径。
func TestReproStepShort(t *testing.T) {
	cases := []struct {
		m    rings.MirrorRecord
		want string
	}{
		{rings.MirrorRecord{Method: "GET", URL: "http://k3.local/admin"}, "GET /admin"},
		{rings.MirrorRecord{Method: "POST", URL: "http://k3.local/login?next=/"}, "POST /login?next=/"},
		{rings.MirrorRecord{URL: "http://k3.local/"}, "/"},
	}
	for _, c := range cases {
		if got := reproStepShort(c.m); got != c.want {
			t.Fatalf("短名兜底不符: 得 %q 期望 %q", got, c.want)
		}
	}
}
