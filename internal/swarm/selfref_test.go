package swarm

// P4-25 ①(问题清单 #38 镜自指):环回自指排除——本机环回与系统自身
// 设施(镜/面板等)不作为发现对象;锚点或证据流量指向本机环回即不立,
// 拒收并记环境观察(信息不丢);操作者声明的环回目标(种子/手动)不受
// 排除,真实目标的发现与判断回归无损。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func TestSelfRefScanLoopbackTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"http://127.0.0.1:60437/proxy 拦截了请求", []string{"127.0.0.1:60437"}},
		{"localhost:8737 与 127.0.0.1 都在本机", []string{"localhost:8737", "127.0.0.1"}},
		{"[::1]:8080 与 ::1 同机", []string{"[::1]:8080", "::1"}},
		{"fe80::1 是链路本地不是环回", nil},
		{"10.0.0.5 与 example.com 不是环回", nil},
		{"x127.0.0.1:99 与 1.127.0.0.1:99 是前缀污染", nil},
	}
	for _, c := range cases {
		var got []string
		for _, tok := range scanLoopbackTokens(c.in) {
			got = append(got, tok.raw)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q: 提法数不符: got=%v want=%v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: 提法 %d 不符: got=%v want=%v", c.in, i, got, c.want)
			}
		}
	}
}

// selfRefEnv 环回门测试环境:种子靶标 127.0.0.1:4481(操作者声明的
// 本机测试目标)+ 发现步骤 s1。
func selfRefEnv(t *testing.T, task string) (*rings.Store, *unitEnv) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "target", NodeType: rings.NodeAsset, Title: "127.0.0.1:4481",
			Status: "active", AssetClass: rings.AssetHost,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测靶标暴露面", Status: "dispatched"}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	return store, &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
}

// envObservations 任务内环境观察记录(env_observation)。
func envObservations(t *testing.T, store *rings.Store, task string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码黑匣子记录: %v", err)
		}
		if r.Type == rings.BlackboxEnvObservation {
			out = append(out, r)
		}
	}
	return out
}

func nodeCount(t *testing.T, store *rings.Store, task string, typ rings.NodeType) int {
	t.Helper()
	ns, err := store.NodesByType(context.Background(), task, typ, "")
	if err != nil {
		t.Fatalf("查 %s 节点失败: %v", typ, err)
	}
	return len(ns)
}

func TestSelfRefGateFactAndClaim(t *testing.T) {
	const task = "selfref-gate"
	store, env := selfRefEnv(t, task)
	ctx := context.Background()

	// ① 新对象申报环回(镜端口 60437):事实拒收,无事实节点,环境观察留痕。
	if _, err := env.SubmitCandidateFact(ctx, "本机代理可改写响应",
		"curl http://127.0.0.1:60437/ 实测", "",
		&NewAssetDecl{Class: "service", Title: "127.0.0.1:60437"}); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("环回新对象应拒收: %v", err)
	}
	if n := nodeCount(t, store, task, rings.NodeFact); n != 0 {
		t.Fatalf("拒收后不得有事实节点: %d", n)
	}
	obs := envObservations(t, store, task)
	if len(obs) != 1 || obs[0].UnitID != "u1" || obs[0].StepID != "s1" {
		t.Fatalf("应恰 1 条环境观察并带单位/步骤: %+v", obs)
	}
	if note, _ := obs[0].Detail["note"].(string); !strings.Contains(note, "拒收新进事实") || !strings.Contains(note, "127.0.0.1:60437") {
		t.Fatalf("环境观察 note 应含提交类与命中提法: %q", note)
	}

	// ② 证据流量指向环回非目标端口:锚点是真实靶标也不立(如实记环境观察)。
	if _, err := env.SubmitCandidateFact(ctx, "靶标 400 由出口代理产生",
		"curl 127.0.0.1:4481 返回 400,链路上有 127.0.0.1:60437", "target", nil); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("证据指向环回应拒收: %v", err)
	}

	// ③ 判断锚定发现型环回资产:不立判断、无判断节点。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "dloop", NodeType: rings.NodeAsset, Title: "127.0.0.1:60437",
			Status: "active", AssetClass: rings.AssetService,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered}}}); err != nil {
		t.Fatalf("种发现型环回资产失败: %v", err)
	}
	if _, err := env.SubmitTestClaim(ctx, "代理 60437 可改写响应", "本机请求链可被篡改",
		"curl http://127.0.0.1:60437/ 返回 200", "dloop", "", nil); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("发现型环回锚点的判断应拒收: %v", err)
	}
	if n := nodeCount(t, store, task, rings.NodeTestClaim); n != 0 {
		t.Fatalf("拒收后不得有判断节点: %d", n)
	}

	// ④ 判断锚定真实靶标但证据指向环回非目标端口:拒收。
	if _, err := env.SubmitTestClaim(ctx, "靶标响应被代理改写", "响应不可信",
		"GET http://127.0.0.1:4481/ 经 127.0.0.1:60437 转发", "target", "", nil); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("证据指向环回的判断应拒收: %v", err)
	}
	if n := len(envObservations(t, store, task)); n != 4 {
		t.Fatalf("①~④ 各应留 1 条环境观察,共 4: %d", n)
	}

	// ⑤ 回归无损:真实靶标(种子 127.0.0.1:4481)上的事实与判断照常入图,
	//    localhost:4481 与种子同主机同端口,归一放行。
	if _, err := env.SubmitCandidateFact(ctx, "靶标备份目录可列举",
		"curl http://127.0.0.1:4481/old-backup/ 返回目录列举", "target", nil); err != nil {
		t.Fatalf("真实靶标事实不得误拦: %v", err)
	}
	tcID, err := env.SubmitTestClaim(ctx, "备份目录可公开列举", "备份可被未授权下载",
		"curl http://localhost:4481/old-backup/ 实测列举 db.sql.gz", "target", "信息暴露", nil)
	if err != nil {
		t.Fatalf("真实靶标判断不得误拦: %v", err)
	}
	n, err := store.Node(ctx, task, tcID)
	if err != nil || n.Status != rings.TestClaimCandidate {
		t.Fatalf("真实靶标判断应入图为候选: %v %+v", err, n)
	}
	if got := len(envObservations(t, store, task)); got != 4 {
		t.Fatalf("真实靶标提交不产生环境观察: %d", got)
	}
}

func TestSelfRefGateHypothesisAndBareMention(t *testing.T) {
	// 远程靶标任务(无环回种子):裸环回提法(无端口)不是流量指向,
	// 不拦——防误伤真实发现(如目标响应回显客户端地址)。
	const remote = "selfref-remote"
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, remote,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "rt", NodeType: rings.NodeAsset, Title: "example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测", Status: "dispatched"}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: remote, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	if _, err := env.SubmitCandidateFact(ctx, "目标回显客户端地址",
		"响应头 X-Forwarded-For: 127.0.0.1(目标内容回显)", "rt", nil); err != nil {
		t.Fatalf("裸环回提法(目标内容)不得误拦: %v", err)
	}
	// 带端口的环回提法(镜)在无环回种子的任务里照样拦。
	if _, err := env.SubmitCandidateFact(ctx, "链路上有本机代理",
		"请求经 127.0.0.1:60437 转发", "rt", nil); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("环回端口提法应拒收: %v", err)
	}
	// 假设指向发现型环回资产:拒收(不作为发现对象)。
	if _, err := store.Append(ctx, remote, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "dl", NodeType: rings.NodeAsset, Title: "localhost:8737",
			Status: "active", AssetClass: rings.AssetService,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered}}}); err != nil {
		t.Fatalf("种发现型环回资产失败: %v", err)
	}
	if _, err := env.SubmitHypothesis(ctx, "该服务可能是管理面板", "端口与面板一致", "dl"); err == nil || !strings.Contains(err.Error(), "本机环回") {
		t.Fatalf("指向发现型环回资产的猜测应拒收: %v", err)
	}
	if n := nodeCount(t, store, remote, rings.NodeHypothesis); n != 0 {
		t.Fatalf("拒收后不得有假设节点: %d", n)
	}
}
