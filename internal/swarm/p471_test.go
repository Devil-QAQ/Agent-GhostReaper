package swarm

// P4-71(问题清单 #87 ④⑤⑥,RoE 复核修正批次第二阶·末阶)swarm 层:
// 退役资产复活(纳入遇 retired 一律复活——写法粒度、互斥、幂等,
// "本就范围内"误答根治)与攻击档反向调档(Executor 在飞登记表:
// 撤销允许=档移除重新出卡 / 转为允许=授档免卡,单位终态档失效)。

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
	"time"

	"ghostreaper/internal/rings"
)

// p471LocalPaths 本机短命靶标:同一 host 落地多条路径(--resolve 到
// 127.0.0.1),每条路径回各自 marker(drive 级攻击命令的判别物)。
// 返回 marker → 命令。
func p471LocalPaths(t *testing.T, host string, paths map[string]string) map[string]string {
	t.Helper()
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m, ok := paths[r.URL.Path]; ok {
			w.Write([]byte(m))
			return
		}
		w.Write([]byte("other"))
	}))
	t.Cleanup(out.Close)
	u, _ := url.Parse(out.URL)
	port := u.Port()
	cmds := map[string]string{}
	for p := range paths {
		cmds[p] = fmt.Sprintf("curl -s --max-time 5 --resolve %s:%s:127.0.0.1 http://%s:%s%s", host, port, host, port, p)
	}
	return cmds
}

// p471RetireAssetOfHost 经 AllowScopeAccess 把 host 的图上资产退役
// (host 入信息源清单 + 同主机资产 retired——P4-68 统一入口,测试用)。
func p471RetireAssetOfHost(t *testing.T, store *rings.Store, task, host string) {
	t.Helper()
	if err := AllowScopeAccess(context.Background(), store, task, host, "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
}

// TestP471IncludeRevivesRetired 完成标准①·牧者入口(IncludeAssets 单源)
// +"本就范围内"误答根治:retired 资产纳入 → 复活(retired→active 留痕
// Note"纳入复活")＋入批准清单,不再落 AlreadyIn;资产图复现(status
// 过滤按现值走)。
func TestP471IncludeRevivesRetired(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	// 范围外发现资产入图 → 允许访问退役(host 级)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-src", NodeType: rings.NodeAsset, Title: "gitee.com:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	p471RetireAssetOfHost(t, store, task, "gitee.com")
	if a, _ := store.Node(ctx, task, "a-src"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("前置:资产应已退役: %s", a.Status)
	}
	// 资产图已不含它(读面过滤)。
	ag, _ := store.AssetGraph(ctx, task)
	for _, n := range ag.Assets {
		if n.ID == "a-src" {
			t.Fatal("前置:退役资产不应在资产图")
		}
	}

	res, err := IncludeAssets(ctx, store, task, []string{"a-src"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	// 误答根治:retired 不再落 AlreadyIn,归 Revived。
	if len(res.Revived) != 1 || res.Revived[0] != "a-src" || len(res.AlreadyIn) != 0 {
		t.Fatalf("retired 纳入应复活(不落 AlreadyIn): %+v", res)
	}
	a, _ := store.Node(ctx, task, "a-src")
	if a.Status != "active" {
		t.Fatalf("资产应复活为 active: %s", a.Status)
	}
	// 复活留痕:NodeStateChanged retired→active,Note"纳入复活"。
	evs, _ := store.Events(ctx, task, 0, 0)
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.NodeID == "a-src" && p.Status == "active" && strings.Contains(p.Note, "纳入复活") {
			found = true
		}
	}
	if !found {
		t.Fatal("复活应留痕 NodeStateChanged(retired→active,Note 纳入复活)")
	}
	// 复活即入批准清单(条目契约)。
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.ScopeApprovedSet()["a-src"] {
		t.Fatalf("复活资产应入批准清单: %+v", roe.ScopeApproved)
	}
	// 资产图复现。
	ag2, _ := store.AssetGraph(ctx, task)
	found = false
	for _, n := range ag2.Assets {
		if n.ID == "a-src" {
			found = true
		}
	}
	if !found {
		t.Fatal("复活后资产应在资产图复现")
	}
}

// TestP471ReviveGranularity 复活粒度＝按纳入的资产节点(写法粒度):
// 同 host 其余退役节点不连带复活——要收编逐个纳入、各自复活留痕。
func TestP471ReviveGranularity(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-80", NodeType: rings.NodeAsset, Title: "multi.io:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-443", NodeType: rings.NodeAsset, Title: "multi.io:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	// host 级退役:两端口资产一并退役(P4-68 语义)。
	p471RetireAssetOfHost(t, store, task, "multi.io")
	for _, id := range []string{"a-80", "a-443"} {
		if a, _ := store.Node(ctx, task, id); a.Status != rings.AssetStatusRetired {
			t.Fatalf("前置 %s 应退役", id)
		}
	}
	// 纳入其一:只它复活。
	if _, err := IncludeAssets(ctx, store, task, []string{"a-80"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Node(ctx, task, "a-80"); a.Status != "active" {
		t.Fatalf("纳入的 a-80 应复活: %s", a.Status)
	}
	if a, _ := store.Node(ctx, task, "a-443"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("同 host 其余退役节点不连带复活: %s", a.Status)
	}
	// 逐个纳入:各自复活留痕。
	if _, err := IncludeAssets(ctx, store, task, []string{"a-443"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Node(ctx, task, "a-443"); a.Status != "active" {
		t.Fatalf("第二个纳入的 a-443 应复活: %s", a.Status)
	}
}

// TestP471RevivedPurgesExcluded 复活＋互斥:retired 资产因历史脏数据
// 并存于摘除清单的,纳入复活时一并移回(排除与批准不并存)。
func TestP471RevivedPurgesExcluded(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-dirty", NodeType: rings.NodeAsset, Title: "dirty.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 脏起点:既被摘除又被退役(旧机制写侧无互斥时的并存形态)。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-dirty"}, Note: "摘除"}},
	); err != nil {
		t.Fatal(err)
	}
	p471RetireAssetOfHost(t, store, task, "dirty.com")
	res, err := IncludeAssets(ctx, store, task, []string{"a-dirty"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Revived) != 1 {
		t.Fatalf("retired+excluded 并存也应按复活整体处理: %+v", res)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.ExcludedAssetSet()["a-dirty"] {
		t.Fatalf("复活应移出摘除清单(互斥): %+v", roe.ExcludedAssets)
	}
	if !roe.ScopeApprovedSet()["a-dirty"] {
		t.Fatalf("复活应入批准清单: %+v", roe.ScopeApproved)
	}
}

// TestP471ReviveRetiredIdempotent ReviveRetiredAssets 幂等:非 retired
// 资产零落笔(重复纳入零追加事件)。
func TestP471ReviveRetiredIdempotent(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	p471RetireAssetOfHost(t, store, task, "www.example.com") // 种子资产退役
	before, _ := store.CurrentRevision(ctx, task)
	if revived, err := ReviveRetiredAssets(ctx, store, task, []string{"a-seed"}, "creator"); err != nil || len(revived) != 1 {
		t.Fatalf("退役资产应复活: %+v %v", revived, err)
	}
	after1, _ := store.CurrentRevision(ctx, task)
	revived2, err := ReviveRetiredAssets(ctx, store, task, []string{"a-seed"}, "creator")
	if err != nil || len(revived2) != 0 {
		t.Fatalf("非退役资产零复活: %+v %v", revived2, err)
	}
	after2, _ := store.CurrentRevision(ctx, task)
	if !(after1 > before && after2 == after1) {
		t.Fatalf("幂等:第一次落账推进版本,第二次零落笔: %d→%d→%d", before, after1, after2)
	}
}

// TestP471AttackRulingReverseRegistry Executor 调档登记表:在册单位
// UnitActive、撤销档(attackRulingGate 回未定档)、授档(免卡)、
// 出册后 ErrUnitFinished(档已随单位销毁,如实)。
func TestP471AttackRulingReverseRegistry(t *testing.T) {
	store, ex, env, pack := p450Env(t)
	ctx := context.Background()
	_ = store
	// 未登记:单位不在册。
	if ex.UnitActive(pack.TaskID, env.UnitID) {
		t.Fatal("未登记单位不应在册")
	}
	if err := ex.SetUnitAttackRuling(pack.TaskID, env.UnitID, "gitee.com", nil); err == nil {
		t.Fatal("未登记单位调档应报错")
	}
	// 登记(在飞模拟:finishUnit 起飞登记的同一写面)。
	ex.rulingMu.Lock()
	ex.rulingUnits[env.UnitID] = env
	ex.rulingMu.Unlock()
	if !ex.UnitActive(pack.TaskID, env.UnitID) {
		t.Fatal("登记单位应在册")
	}
	// 授档(转为允许):gate 放行。
	env.setAttackRuling("gitee.com", true)
	if ruling, _ := env.attackRulingGate(pack, "bash", nil, "gitee.com", "gitee.com"); ruling != listedRulingAllow {
		t.Fatal("允许档应放行(免卡)")
	}
	// 撤销允许:档移除,gate 回未定档(重新出卡)。
	if err := ex.SetUnitAttackRuling(pack.TaskID, env.UnitID, "gitee.com:443", nil); err != nil {
		t.Fatalf("撤销允许: %v", err)
	}
	if ruling, _ := env.attackRulingGate(pack, "bash", nil, "gitee.com", "gitee.com"); ruling != listedRulingUnsure {
		t.Fatal("撤销档后应回未定档(后续同类攻击重新出卡)")
	}
	// 授档经登记表通道(反向"转为允许"的执行面)。
	allowed := true
	if err := ex.SetUnitAttackRuling(pack.TaskID, env.UnitID, "gitee.com", &allowed); err != nil {
		t.Fatalf("转为允许: %v", err)
	}
	if ruling, _ := env.attackRulingGate(pack, "bash", nil, "gitee.com", "gitee.com"); ruling != listedRulingAllow {
		t.Fatal("转为允许后应免卡")
	}
	// 出册(单位落地):档失效,反向如实报错。
	ex.rulingMu.Lock()
	delete(ex.rulingUnits, env.UnitID)
	ex.rulingMu.Unlock()
	if ex.UnitActive(pack.TaskID, env.UnitID) {
		t.Fatal("落地单位应出册")
	}
	if err := ex.SetUnitAttackRuling(pack.TaskID, env.UnitID, "gitee.com", &allowed); err == nil ||
		!strings.Contains(err.Error(), "已结束") {
		t.Fatalf("落地单位调档应如实报错: %v", err)
	}
	_ = ctx
}

// p471WaitAttackTicketFor 等待指定信息源 host 的攻击行为卡票出现
// (drive 级测试时序;按 AttackTarget 的 host 前缀匹配)。
func p471WaitAttackTicketFor(t *testing.T, ex *Executor, task, host string) string {
	t.Helper()
	for i := 0; i < 400; i++ {
		for _, p := range ex.Gate.Pending(task) {
			if p.AttackTarget == host || strings.HasPrefix(p.AttackTarget, host+":") {
				return p.ID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("攻击行为卡票(%s)未出现", host)
	return ""
}

// TestP471DriveUnallowReplacesRuling 完成标准④·撤销允许(drive 级全链):
// 攻击卡批准落允许档 → 同单位第二次攻击免卡直接执行(撤销前对照)→
// 反向"撤销允许"(单位在册,档移除)→ 第三次攻击重新出卡。
func TestP471DriveUnallowReplacesRuling(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p471-unallow"
	ctx := context.Background()
	// 本机靶标:gitee 三条路径(三次攻击各打一条)、pypi 一条。
	gcmds := p471LocalPaths(t, "gitee.com", map[string]string{
		"/g1": "g1-hit", "/g2": "g2-hit", "/g3": "g3-hit",
	})
	pcmds := p471LocalPaths(t, "pypi.org", map[string]string{"/p1": "p1-hit"})
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "对信息源发起扫描", Status: "dispatched",
			Detail: map[string]any{"action_level": "主动测试"}}},
	); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "pypi.org", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	var triageCalls atomic.Int32
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if p470IsTriageReq(rr) {
			if strings.Contains(rr.LastUser, "已在任务信息源清单") {
				triageCalls.Add(1)
				return scriptedReply{Text: `{"verdict":"attack_action","basis":"对信息源的扫描动作"}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"verdict":"related_candidate","basis":"无关"}`, Finish: "stop"}
		}
		// 阶段分派:被拒命令(pypi 卡拒绝)无工具结果,以注入的拒绝反馈
		//文本判进(阶段 4);免卡与批准执行的以工具结果判进;末阶段收尾。
		switch {
		case !seenToolResult(rr, "bash", "g1-hit"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b1", Name: "bash", Args: mustJSON(t, gcmds["/g1"])}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "g2-hit"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b2", Name: "bash", Args: mustJSON(t, gcmds["/g2"])}}, Finish: "tool_calls"}
		case !seenText(rr, "的攻击动作不批准"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b3", Name: "bash", Args: mustJSON(t, pcmds["/p1"])}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "g3-hit"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b4", Name: "bash", Args: mustJSON(t, gcmds["/g3"])}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "三次攻击与一次被拒均已处理,收尾。", Finish: "stop"}
		}
	})
	t.Cleanup(func() { model.Close() })
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)

	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	// 第一次 gitee 攻击 → 卡 1 → 批准(落允许档)。
	rid1 := p471WaitAttackTicketFor(t, ex, task, "gitee.com")
	pend := p471PendingOf(t, ex, task, rid1)
	if err := ex.Gate.Decide(ctx, task, rid1, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	// 第二次 gitee 攻击:档命中免卡直接执行(g2 工具记录出现即免卡
	// 已发生——否则单位 park 在 gitee 票上,g2 永不执行)。
	p471WaitMarker(t, store, task, "g2-hit")
	// pypi 攻击 → 卡 2(park 稳定窗口)。
	rid2 := p471WaitAttackTicketFor(t, ex, task, "pypi.org")
	// 反向"撤销允许":单位在册,档移除(后续同类攻击重新出卡)。
	if err := ex.SetUnitAttackRuling(task, pend.UnitID, "gitee.com", nil); err != nil {
		t.Fatalf("撤销允许: %v", err)
	}
	// 拒绝 pypi 卡(只杀动作,单位继续)。
	if err := ex.Gate.Decide(ctx, task, rid2, GateDecision{Approved: false, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	// 第三次 gitee 攻击:档已撤 → 重新出卡(卡 3)。
	rid3 := p471WaitAttackTicketFor(t, ex, task, "gitee.com")
	if rid3 == rid1 {
		t.Fatalf("重新出卡应为新票: %s", rid3)
	}
	if err := ex.Gate.Decide(ctx, task, rid3, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("单位应完成: %+v", res)
	}
	// 账面对账:gitee 攻击卡 2 张(批准 1 + 撤销后重新出卡 1)、pypi 1 张。
	recs := approvalEvents(t, store, task)
	giteeWaits, pypiWaits := 0, 0
	for _, r := range recs {
		if r.Type == rings.ApprovalRecAttackWait {
			if strings.Contains(r.Note, "攻击行为卡:gitee.com") {
				giteeWaits++
			}
			if strings.Contains(r.Note, "攻击行为卡:pypi.org") {
				pypiWaits++
			}
		}
	}
	if giteeWaits != 2 || pypiWaits != 1 {
		t.Fatalf("gitee 应 2 张卡(撤销后重新出卡 1),pypi 1 张: g=%d p=%d", giteeWaits, pypiWaits)
	}
	// 第三次攻击经卡 3 批准真实执行(g3-hit 在账)。
	p471WaitMarker(t, store, task, "g3-hit")
	// 撤销前第二次攻击零判定(档命中);撤销后第三次攻击重新判定:
	// 判定共 3 次(卡 1、卡 2、卡 3 各一次)。
	if n := triageCalls.Load(); n != 3 {
		t.Fatalf("动作判定应共 3 次(卡1/卡2/卡3),第二次免卡零判定: %d", n)
	}
}

// TestP471DriveAllowAfterDeny 完成标准④·转为允许(drive 级全链):
// 攻击卡拒绝(落禁止档+只杀动作)→ 反向"转为允许"(授档)→ 同单位
// 第二次攻击免卡直接执行(零新卡)。
func TestP471DriveAllowAfterDeny(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p471-allow"
	ctx := context.Background()
	gcmds := p471LocalPaths(t, "gitee.com", map[string]string{"/a1": "a1-hit", "/a2": "a2-hit"})
	pcmds := p471LocalPaths(t, "pypi.org", map[string]string{"/p1": "pp-hit"})
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "对信息源发起扫描", Status: "dispatched",
			Detail: map[string]any{"action_level": "主动测试"}}},
	); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "pypi.org", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	var triageCalls atomic.Int32
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if p470IsTriageReq(rr) {
			if strings.Contains(rr.LastUser, "已在任务信息源清单") {
				triageCalls.Add(1)
				return scriptedReply{Text: `{"verdict":"attack_action","basis":"对信息源的扫描动作"}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"verdict":"related_candidate","basis":"无关"}`, Finish: "stop"}
		}
		// 阶段分派按注入反馈与工具结果判进(注入反馈的 host 带端口,
		// marker 用不带 host 的短语防端口干扰;被拒命令不产生工具结果,
		// 拒绝反馈文本是阶段 2 的依据;免卡执行的工具结果是阶段 3 的
		// 依据;末阶段收尾)。
		switch {
		case !seenText(rr, "的攻击动作不批准") && !seenToolResult(rr, "bash", "a1-hit"):
			// 阶段 1:首次 gitee 攻击(出卡被拒,无工具结果)。
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b1", Name: "bash", Args: mustJSON(t, gcmds["/a1"])}}, Finish: "tool_calls"}
		case !seenText(rr, "的攻击动作已获准"):
			// 阶段 2:gitee 已被拒(注入反馈在会话),转攻 pypi(出卡)。
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b2", Name: "bash", Args: mustJSON(t, pcmds["/p1"])}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "a2-hit"):
			// 阶段 3:pypi 已批准执行,再攻 gitee(档已转为允许→免卡)。
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b3", Name: "bash", Args: mustJSON(t, gcmds["/a2"])}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "攻击动作已全部处理,收尾。", Finish: "stop"}
		}
	})
	t.Cleanup(func() { model.Close() })
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)

	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	// 第一次 gitee 攻击 → 卡 1 → 拒绝(落禁止档,只杀动作)。
	rid1 := p471WaitAttackTicketFor(t, ex, task, "gitee.com")
	pend := p471PendingOf(t, ex, task, rid1)
	if err := ex.Gate.Decide(ctx, task, rid1, GateDecision{Approved: false, By: "creator", Reason: "不要攻击它"}); err != nil {
		t.Fatal(err)
	}
	// pypi 攻击 → 卡 2(park 稳定窗口)。
	rid2 := p471WaitAttackTicketFor(t, ex, task, "pypi.org")
	// 反向"转为允许":授档(该单位后续同类攻击免卡)。
	allowed := true
	if err := ex.SetUnitAttackRuling(task, pend.UnitID, "gitee.com", &allowed); err != nil {
		t.Fatalf("转为允许: %v", err)
	}
	if err := ex.Gate.Decide(ctx, task, rid2, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	// 第二次 gitee 攻击:档=允许(已转)→ 免卡直接执行(单位自然完成)。
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("单位应完成: %+v", res)
	}
	recs := approvalEvents(t, store, task)
	waits := 0
	for _, r := range recs {
		if r.Type == rings.ApprovalRecAttackWait && strings.Contains(r.Note, "攻击行为卡:gitee.com") {
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("gitee 应恰 1 张卡(第二次免卡),得 %d", waits)
	}
	// 判定恰 2 次(卡 1、卡 2);第二次 gitee 攻击档命中零判定。
	if n := triageCalls.Load(); n != 2 {
		t.Fatalf("动作判定应恰 2 次,第二次 gitee 攻击免卡零判定: %d", n)
	}
	// 第二次 gitee 攻击真实执行(a2-hit 在 bash 记录里)。
	calls := toolCallRecords(t, store, task, "bash")
	head := ""
	for _, c := range calls {
		head += c.ResultHead
	}
	if !strings.Contains(head, "a2-hit") {
		t.Fatalf("转为允许后的攻击应免卡真实执行: %+v", calls)
	}
}

// p471PendingOf 取待决票(测试用)。
func p471PendingOf(t *testing.T, ex *Executor, task, rid string) ApprovalRequest {
	t.Helper()
	for _, p := range ex.Gate.Pending(task) {
		if p.ID == rid {
			return p
		}
	}
	t.Fatalf("票 %s 不在待决表", rid)
	return ApprovalRequest{}
}

// p471WaitMarker 等待某 marker 的工具记录落账(drive 时序)。
func p471WaitMarker(t *testing.T, store *rings.Store, task, marker string) {
	t.Helper()
	for i := 0; i < 400; i++ {
		for _, c := range toolCallRecords(t, store, task, "bash") {
			if strings.Contains(c.ResultHead, marker) {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("工具记录(%s)未出现", marker)
}

// seenText 会话历史(任意消息)是否含文本片段(被拒命令无工具结果,
// 注入的审批反馈是阶段推进的判据)。
func seenText(rr *recordedRequest, marker string) bool {
	for _, m := range rr.Messages {
		if strings.Contains(m.Content, marker) {
			return true
		}
	}
	return false
}
