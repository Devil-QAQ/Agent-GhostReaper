package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// ---- P4-51(问题清单 #73②③④⑦):渗透范围拦截改阻塞等批准 ----
// 冻结出卡(同一目标一张卡)、已排除直接拒(防骚扰)、已获准重领、
// e2e 冻结→批准→原会话续跑执行原命令(transcript 连续可证)。

// p451ScopeWaitCards 事件流里的范围待决卡(scope_wait 记录)。
func p451ScopeWaitCards(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	recs := approvalEvents(t, store, task)
	var out []rings.ApprovalRecord
	for _, r := range recs {
		if r.Type == rings.ApprovalRecScopeWait {
			out = append(out, r)
		}
	}
	return out
}

// TestP451FreezeOpensCardAndWait 单位级:范围外命令拦下出待决卡(不熔断
// 不拒绝)、同一目标一张卡去重;范围内目标照旧放行(P4-50 样本回归)。
func TestP451FreezeOpensCardAndWait(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	pack := p444PentestPack(t, ex, store, task)
	gate := env.approvalGate(pack)

	// 范围外目标:拦下等待批准——不设 deniedReason(不熔断)、出待决卡。
	d := gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://api.example.com:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, "等待批准") {
		t.Fatalf("范围外目标应拦下等待批准: %+v", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("等待批准不是拒绝,不得熔断: %q", env.deniedReason)
	}
	if env.scopeHold == nil || env.scopeHold.host != "api.example.com" || env.scopeHold.port != "8080" {
		t.Fatalf("应置位冻结目标: %+v", env.scopeHold)
	}
	cards := p451ScopeWaitCards(t, store, task)
	if len(cards) != 1 || cards[0].RequestID != "sc-api.example.com:8080" {
		t.Fatalf("应恰 1 张范围待决卡(请求号按目标): %+v", cards)
	}
	if cards[0].StepID != pack.StepID || cards[0].ToolName != "bash" || !strings.Contains(string(cards[0].ToolArgs), "api.example.com") {
		t.Fatalf("卡面应带首次拦截上下文(步骤/工具/参数): %+v", cards[0])
	}

	// 同一目标再次命中(另一只单位/复挂):并入等待不重复出卡。
	env2 := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u2", TranscriptID: "swarm-u2"}
	env2.scopeResolve = func(string) []string { return nil }
	d2 := env2.approvalGate(pack)(context.Background(), "http_probe", json.RawMessage(
		`{"url":"http://api.example.com:8080/admin"}`), permission.Context{})
	if d2.Behavior != permission.Deny || !strings.Contains(d2.Message, "等待批准") {
		t.Fatalf("只读探测范围外同样拦下等待: %+v", d2)
	}
	if got := len(p451ScopeWaitCards(t, store, task)); got != 1 {
		t.Fatalf("同一目标应只出一张卡,得 %d 张", got)
	}

	// 范围内目标(种子域名任意端口,P4-50 样本形态)照旧放行、零卡。
	d3 := gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://www.example.com:9443/app"}`), permission.Context{})
	if d3.Behavior == permission.Deny && strings.Contains(d3.Message, "任务范围外") {
		t.Fatalf("种子域名任意端口应放行: %+v", d3)
	}
	if got := len(p451ScopeWaitCards(t, store, task)); got != 1 {
		t.Fatalf("范围内放行不得出卡: %d", got)
	}
}

// TestP451ExcludedTargetDirectDeny 防骚扰:已排除(禁止测试/摘除)目标的
// 命令直接拒留痕熔断,不出卡;通配/端口粒度按写法覆盖。
func TestP451ExcludedTargetDirectDeny(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	// 图上已摘除资产 evil.com(整台)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-evil", NodeType: rings.NodeAsset, Title: "evil.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-evil"}}},
	); err != nil {
		t.Fatal(err)
	}
	pack := p444PentestPack(t, ex, store, task)
	gate := env.approvalGate(pack)

	d := gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://evil.com:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, "禁止测试") {
		t.Fatalf("已排除目标应直接拒(禁止测试): %+v", d)
	}
	if env.deniedReason == "" || !strings.Contains(env.deniedReason, "禁止测试") {
		t.Fatalf("已排除目标拒绝应熔断: %q", env.deniedReason)
	}
	if got := len(p451ScopeWaitCards(t, store, task)); got != 0 {
		t.Fatalf("已排除目标不得出卡: %d", got)
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecDenied || recs[0].DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("应留痕一笔 roe_scope 拒绝: %+v", recs)
	}

	// 通配摘除(*.域)覆盖子域与基域。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-wild", NodeType: rings.NodeAsset, Title: "*.banned.org",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-evil", "a-wild"}}},
	); err != nil {
		t.Fatal(err)
	}
	env.scopeNow = nil // 现值缓存重读
	d = gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://sub.banned.org/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, "禁止测试") {
		t.Fatalf("通配摘除应覆盖子域: %+v", d)
	}
}

// TestP451ApprovedTargetReclaims 目标已获准但本单位闭包是领取时快照——
// 步骤回可领取重领新包(不冻结不出卡)。
func TestP451ApprovedTargetReclaims(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	// 先领取(闭包快照不含后来放行的资产)。
	pack := p444PentestPack(t, ex, store, task)
	// 图上已放行资产 api.example.com(本包领取在其后——闭包过期)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-api", NodeType: rings.NodeAsset, Title: "api.example.com",
			Status: "active", AssetClass: rings.AssetSubdomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-api"}}},
	); err != nil {
		t.Fatal(err)
	}
	if pack.ScopeClosure.CoversTarget("api.example.com", "", nil) {
		t.Fatal("前置:领取时闭包应不含该资产(快照过期形态)")
	}
	gate := env.approvalGate(pack)
	d := gate(context.Background(), "bash", json.RawMessage(
		`{"command":"curl -s http://api.example.com/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, "已加入任务范围") {
		t.Fatalf("已获准目标应走重领路径: %+v", d)
	}
	if !env.scopeReclaim || env.scopeHold != nil {
		t.Fatalf("应置位重领而非冻结: reclaim=%v hold=%+v", env.scopeReclaim, env.scopeHold)
	}
	if got := len(p451ScopeWaitCards(t, store, task)); got != 0 {
		t.Fatalf("重领路径不得出卡: %d", got)
	}
}

// TestP451FreezeApproveResumeE2E e2e:范围外命令 → 单位冻结收场(步骤
// scope_wait 带 target、会话保留 ScopeWait、名额让位)→ 目标入图放行
// ＋步骤唤醒(对账通道)→ 重领原会话续跑,原命令真实执行(transcript
// 连续可证)。
func TestP451FreezeApproveResumeE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p451-e2e"

	// 范围外靶标:本机短命服务(命中后批准,续跑时真实执行一次)。
	var hits int32
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("p451-ok"))
	}))
	defer out.Close()
	outURL, uerr := url.Parse(out.URL)
	if uerr != nil {
		t.Fatal(uerr)
	}
	outHost, outPort := outURL.Hostname(), outURL.Port()

	// 渗透任务:种子 www.example.com(DNS 打桩——解析恒失败,保判定确定性)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created",
			Detail: map[string]any{"task_type": rings.TaskTypePentest},
		}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "验证冻结续跑", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测范围外靶标",
			Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
	); err != nil {
		t.Fatal(err)
	}

	cmd := fmt.Sprintf(`{"command":"curl -s --max-time 5 http://%s:%s/frozen"}`, outHost, outPort)
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		// 会话内未见成功执行结果即(重新)发起——冻结段的拒绝提示不是
		// 执行结果,续跑段重发同一命令;执行成功后收尾。
		if !seenToolResult(rr, "bash", "p451-ok") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash", Args: cmd,
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "命令已执行,探测完成。", Finish: "stop"}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 8})
	ex.Gate = NewLedgerGate(store)

	// ---- 冻结:范围外命令拦下,单位收场让位 ----
	res, rerr := ex.RunStep(ctx, task, "s1")
	if rerr != nil || res == nil {
		t.Fatalf("冻结收场不是错误: %v %+v", rerr, res)
	}
	if res.StepStatus != rings.StepScopeWait || !res.ScopeWait {
		t.Fatalf("应落 scope_wait 收场: %+v", res)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != rings.StepScopeWait ||
		step.Detail["scope_wait_host"] != outHost || step.Detail["scope_wait_port"] != outPort {
		t.Fatalf("步骤应带冻结目标: %+v", step)
	}
	cards := p451ScopeWaitCards(t, store, task)
	if len(cards) != 1 || cards[0].RequestID != "sc-"+outHost+":"+outPort {
		t.Fatalf("应出一张范围待决卡: %+v", cards)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("批准前不得执行命令")
	}
	// 会话冻结保留:session_finished 带 ScopeWait,可续会话来源为范围批准。
	var scopeWaitFinish bool
	for _, r := range swarmRecords(t, store, task) {
		if r.Type == rings.SwarmRecSessionEnd && r.ScopeWait {
			scopeWaitFinish = true
		}
	}
	if !scopeWaitFinish {
		t.Fatal("session_finished 应带 ScopeWait 标记")
	}
	tid, kind := ex.pendingSession(ctx, task, "s1")
	if tid == "" || kind != resumeScope {
		t.Fatalf("可续会话应为原会话(范围批准来源): %q %q", tid, kind)
	}

	// ---- 批准(目标入图转范围)＋唤醒(对账通道,同 overmind.ResolveScopeWaits)----
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-out", NodeType: rings.NodeAsset, Title: outHost + ":" + outPort,
			Status: "active", AssetClass: rings.AssetHost,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-out"}, Note: "加入资产"}},
		// 对账唤醒:步骤回可领取＋队首标记(overmind 侧同款事件)。
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "dispatched", Note: "scope_head"}},
	); err != nil {
		t.Fatal(err)
	}

	// ---- 续跑:原会话 Resume,原命令真实执行 ----
	res2, rerr2 := ex.RunStep(ctx, task, "s1")
	if rerr2 != nil || res2 == nil || res2.StepStatus != "done" {
		t.Fatalf("续跑应完成: %v %+v", rerr2, res2)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("原命令应在批准后真实执行一次,得 %d 次", hits)
	}
	step2, _ := store.Node(ctx, task, "s1")
	if step2.Status != "done" {
		t.Fatalf("步骤应完成: %+v", step2)
	}
	// 同一会话续跑(transcript 连续):第二次 session_started 沿用原转录。
	started := 0
	for _, r := range swarmRecords(t, store, task) {
		if r.Type == rings.SwarmRecSessionStarted && r.TranscriptID == tid {
			started++
		}
	}
	if started != 2 {
		t.Fatalf("原会话应被重领续跑(两次起飞同一转录): %d", started)
	}
}
