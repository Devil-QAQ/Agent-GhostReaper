package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// ---- P4-44 ③(问题清单 #61):渗透任务的范围外硬拦 ----
// 目标提取(http_probe/port_probe 参数、bash URL 与裸 IPv4)、两处挂点
// (审批门回调 + 只读工具门卫补口)与拒绝留痕(DenyLine=roe_scope,
// 与内置拦截规则同级的确定性拒绝、熔断单位)、领取时闭包冻结。

func TestP444ScopeCallTargets(t *testing.T) {
	// bash(P4-50 ①收窄面):nmap 目标参数位、curl 目标 URL 提取;版本号
	// 形态(10.999.999.999)、for 变量列表值(127.0.0.1:8080)与变量拼接
	// URL(http://$hp/)不提取。
	ts := scopeCallTargets("bash", json.RawMessage(
		`{"command":"nmap -p 80,443 10.0.0.9 && curl http://www.example.com:8080/a && echo 2.4.1 rev 10.999.999.999 && for hp in 127.0.0.1:8080; do curl -s http://$hp/; done"}`))
	var hosts []string
	for _, tp := range ts {
		hosts = append(hosts, tp.host)
	}
	joined := strings.Join(hosts, ",")
	for _, want := range []string{"10.0.0.9", "www.example.com"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bash 目标提取缺 %q: %v", want, hosts)
		}
	}
	for _, bad := range []string{"10.999.999.999", "$hp", "127.0.0.1"} {
		if strings.Contains(joined, bad) {
			t.Errorf("非目标形态不应提取(%q): %v", bad, hosts)
		}
	}
	// nmap -p 端口绑定目标(端口写法粒度配套)。
	ts = scopeCallTargets("bash", json.RawMessage(`{"command":"nmap -p 8080 10.0.0.9"}`))
	if len(ts) != 1 || ts[0].host != "10.0.0.9" || ts[0].port != "8080" {
		t.Errorf("nmap -p 端口应绑定目标: %+v", ts)
	}
	// http_probe/port_probe 参数直取;web_fetch/read 无目标(不在硬拦面)。
	if ts := scopeCallTargets("http_probe", json.RawMessage(`{"url":"http://api.other.com:9090/x"}`)); len(ts) != 1 || ts[0].host != "api.other.com" {
		t.Errorf("http_probe 目标提取不符: %+v", ts)
	}
	if ts := scopeCallTargets("port_probe", json.RawMessage(`{"host":"10.0.0.9","port":8080}`)); len(ts) != 1 || ts[0].host != "10.0.0.9" || ts[0].port != "8080" {
		t.Errorf("port_probe 目标提取不符: %+v", ts)
	}
	if ts := scopeCallTargets("web_fetch", json.RawMessage(`{"url":"https://owasp.org/x"}`)); len(ts) != 0 {
		t.Errorf("web_fetch 不在硬拦面: %+v", ts)
	}
	if ts := scopeCallTargets("read", json.RawMessage(`{"path":"bg-1.output"}`)); len(ts) != 0 {
		t.Errorf("read 无目标: %+v", ts)
	}
}

// p444PentestEnv 渗透任务(种子 www.example.com)+ 执行器与单位环境。
func p444PentestEnv(t *testing.T, approved []string) (*rings.Store, *Executor, *unitEnv, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p444-task"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
	); err != nil {
		t.Fatal(err)
	}
	if approved != nil {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: approved},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	// DNS 打桩:解析恒失败(判定按保守方向)——测试不依赖公网 DNS 的
	// 实际解析结果(P4-50 ②同机扩展下,公网同 IP 会改变判定)。
	env.scopeResolve = func(string) []string { return nil }
	return store, ex, env, task
}

// p444PentestPack 领取渗透任务的任务包(闭包冻结进包)。
func p444PentestPack(t *testing.T, ex *Executor, store *rings.Store, task string) *TaskPack {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测意图", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
	); err != nil {
		t.Fatal(err)
	}
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestP444DenyScopeApprovalGate(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	pack := p444PentestPack(t, ex, store, task)
	if pack.TaskType != rings.TaskTypePentest || pack.ScopeClosure == nil || pack.ScopeClosure.Empty() {
		t.Fatalf("渗透任务包应冻结类型与闭包: %+v", pack)
	}
	gate := env.approvalGate(pack)
	// 范围外目标(bash 命令里的兄弟域名):P4-51 ①起拦下出待决卡＋单位
	// 冻结收场让位(不熔断不 denied),提示等待批准。
	d := gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://api.example.com/"}`), permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, "等待批准") {
		t.Fatalf("范围外目标应拦下等待批准: %+v", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("等待批准不是拒绝,不得熔断: %q", env.deniedReason)
	}
	if env.scopeHold == nil || env.scopeHold.host != "api.example.com" {
		t.Fatalf("应置位冻结目标: %+v", env.scopeHold)
	}
	// 留痕:approval.record scope_wait 待决卡(同一目标一张;http 默认
	// 端口补全 80,写法即授权粒度)。
	cards := p451ScopeWaitCards(t, store, task)
	if len(cards) != 1 || cards[0].RequestID != "sc-api.example.com:80" {
		t.Fatalf("应恰 1 张范围待决卡: %+v", cards)
	}
	// 范围内目标(种子域名任意端口)放行。
	d = gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://www.example.com:9443/app"}`), permission.Context{})
	if d.Behavior == permission.Deny && strings.Contains(d.Message, "任务范围外") {
		t.Fatalf("种子域名任意端口应放行: %+v", d)
	}
	// 无目标的本地命令放行。
	d = gate(context.Background(), "bash", json.RawMessage(`{"command":"ls -la"}`), permission.Context{})
	if d.Behavior == permission.Deny {
		t.Fatalf("本地命令应放行: %+v", d)
	}
}

func TestP444DenyScopeReadOnlyGuard(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	pack := p444PentestPack(t, ex, store, task)
	g := newToolGuard(env, pack, ex.Helm, 10, time.Time{}, 0)
	// http_probe 是只读工具(权限管线自动放行、不进审批门)——门卫
	// 补口拦:范围外目标拦下等待批准(P4-51 ①:出卡＋冻结,不熔断)。
	reason := g.guardReadOnlyCall("http_probe", json.RawMessage(
		`{"url":"http://api.example.com/admin"}`))
	if !strings.Contains(reason, "等待批准") {
		t.Fatalf("只读探测范围外目标应拦下等待批准: %q", reason)
	}
	if env.deniedReason != "" {
		t.Fatal("等待批准不是拒绝,不得熔断")
	}
	if env.scopeHold == nil {
		t.Fatal("补口拦截应置位冻结目标")
	}
	env.scopeHold = nil
	// port_probe 裸 IP 目标(种子是域名 → 该 IP 不在闭包)同样拦。
	reason = g.guardReadOnlyCall("port_probe", json.RawMessage(`{"host":"10.0.0.9","port":8080}`))
	if !strings.Contains(reason, "等待批准") {
		t.Fatalf("port_probe 范围外目标应拦下等待批准: %q", reason)
	}
	env.scopeHold = nil
	// 种子域名目标放行。
	if reason := g.guardReadOnlyCall("http_probe", json.RawMessage(
		`{"url":"http://www.example.com:8080/"}`)); reason != "" {
		t.Fatalf("种子域名目标应放行: %q", reason)
	}
}

func TestP444DenyScopeApprovedAndRedteam(t *testing.T) {
	// 放行清单并入闭包:api.example.com 放行后,新领取的单位包闭包含它。
	store, ex, env, task := p444PentestEnv(t, []string{"a-api"})
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-api", NodeType: rings.NodeAsset, Title: "api.example.com",
			Status: "active", AssetClass: rings.AssetSubdomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}}); err != nil {
		t.Fatal(err)
	}
	pack := p444PentestPack(t, ex, store, task)
	if !pack.ScopeClosure.CoversTarget("api.example.com", "", nil) {
		t.Fatal("放行资产应入闭包")
	}
	d := env.approvalGate(pack)(context.Background(), "bash", json.RawMessage(
		`{"command":"curl http://api.example.com/"}`), permission.Context{})
	if d.Behavior == permission.Deny && strings.Contains(d.Message, "任务范围外") {
		t.Fatalf("放行后目标应放行: %+v", d)
	}

	// 红队任务:闭包 nil,一切照旧(现状行为)。
	dir := t.TempDir()
	store2 := openStore(t, dir)
	const task2 = "p444-red"
	ctx := context.Background()
	if _, err := store2.Append(ctx, task2,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "意图", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
	); err != nil {
		t.Fatal(err)
	}
	ex2 := NewExecutor(store2, nil, filepath.Join(dir, "tasks"), Budget{})
	pack2, err := ex2.Claim(ctx, task2, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if pack2.TaskType != rings.TaskTypeRedteam || pack2.ScopeClosure != nil {
		t.Fatalf("红队任务包不冻结闭包: %+v", pack2)
	}
	env2 := &unitEnv{Executor: ex2, TaskID: task2, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	if reason := env2.denyScope(pack2, "http_probe", json.RawMessage(
		`{"url":"http://anything.example.net/"}`)); reason != "" {
		t.Fatalf("红队任务不拦: %q", reason)
	}
}

// ---- 测试辅助 ----

func p444DenyRecords(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.ApprovalRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var r rings.ApprovalRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == rings.ApprovalRecDenied {
			out = append(out, r)
		}
	}
	return out
}
