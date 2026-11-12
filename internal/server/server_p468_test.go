package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-68 ①(问题清单 #86 B):范围卡第三键"允许访问"的面板端点侧 ----
// P4-70 ① 起卡面与批量入口移除:本组测试转为单卡决策端点保留的回归
//(sc- 渗透命令拦截卡 / a- 待批资产卡 action=allow_access 仍可裁决——
// host 入信息源清单、资产退役/零入图、挂等单位对账唤醒;红队可否决
// 卡同语义),批量入口清除断言见 TestP468BatchAllowAccess。

func p468PostJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// p468SeedScopeCard 种一张待决范围卡(sc-)＋挂等单位(冻结步骤)。
func p468SeedScopeCard(t *testing.T, store *rings.Store, task, host, port, stepID string) {
	t.Helper()
	ctx := context.Background()
	disp := host
	if port != "" {
		disp += ":" + port
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID(host, port), StepID: stepID, UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash", Intent: "查资料",
			ToolArgs: []byte(`{"command":"curl -s http://` + host + `/"}`),
			Note:     "渗透任务:命令目标 " + disp + " 在范围外,拦下等待批准"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: rings.StepScopeWait,
				Note: "scope_wait " + host + " " + port}},
	); err != nil {
		t.Fatal(err)
	}
}

// TestP468AllowAccessEndpointScCard 完成标准①(渗透命令拦截卡):第三键
// 端点瞬间返回——access 裁决记录、host 入清单、零资产入图、挂等单位
// 回队(scope_head);重复点击 409。
func TestP468AllowAccessEndpointScCard(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	const task = "p467-srv"
	p468SeedScopeCard(t, store, task, "gitee.com", "443", "s1")

	code, body := p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/sc-gitee.com:443",
		map[string]any{"action": "allow_access"})
	if code != http.StatusOK {
		t.Fatalf("第三键应 200: %d %v", code, body)
	}
	if body["access"] != true || body["woken_units"].(float64) != 1 {
		t.Fatalf("响应应含 access 与唤醒数: %v", body)
	}
	ctx := context.Background()
	// host 入清单;零资产入图(资产图只有种子)。
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.InfoSourceHit("gitee.com") {
		t.Fatalf("host 应入信息源清单: %+v", roe.InfoSources)
	}
	ag, _ := store.AssetGraph(ctx, task)
	if len(ag.Assets) != 1 {
		t.Fatalf("允许访问零资产入图: %+v", ag.Assets)
	}
	// 挂等单位回队队首。
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "dispatched" || step.Detail["scope_head"] != true {
		t.Fatalf("挂等单位应回队队首: %+v", step)
	}
	// 重复点击:卡已裁,409。
	code, _ = p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/sc-gitee.com:443",
		map[string]any{"action": "allow_access"})
	if code != http.StatusConflict {
		t.Fatalf("重复第三键应 409: %d", code)
	}
}

// TestP468AllowAccessEndpointAssetCard 完成标准①(待批资产卡):第三键
// ——资产退役(待批清单消)、host 入清单、access 裁决记录。
func TestP468AllowAccessEndpointAssetCard(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	ctx := context.Background()
	const task = "p467-srv"
	// 待批资产(渗透任务范围外发现入图)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-out", NodeType: rings.NodeAsset, Title: "pypi.org",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	pend, _ := store.PendingScopeAssets(ctx, task)
	if len(pend) != 1 || pend[0].ID != "a-out" {
		t.Fatalf("铺垫:应有一张待批资产卡: %+v", pend)
	}

	code, body := p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/a-out",
		map[string]any{"action": "allow_access"})
	if code != http.StatusOK {
		t.Fatalf("待批资产卡第三键应 200: %d %v", code, body)
	}
	// 资产退役:待批清单消、资产图消。
	if pend, _ := store.PendingScopeAssets(ctx, task); len(pend) != 0 {
		t.Fatalf("资产退役后待批清单应清空: %+v", pend)
	}
	ag, _ := store.AssetGraph(ctx, task)
	if len(ag.Assets) != 1 {
		t.Fatalf("退役资产不应出现在资产图: %+v", ag.Assets)
	}
	// 节点留痕(退役非删除,审计可查)。
	n, err := store.Node(ctx, task, "a-out")
	if err != nil || n.Status != rings.AssetStatusRetired {
		t.Fatalf("节点应留痕为退役态: %+v err=%v", n, err)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.InfoSourceHit("pypi.org") {
		t.Fatalf("host 应入清单: %+v", roe.InfoSources)
	}
	// 重复:非待批,409。
	code, _ = p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/a-out",
		map[string]any{"action": "allow_access"})
	if code != http.StatusConflict {
		t.Fatalf("重复处理应 409: %d", code)
	}
}

// TestP468BatchAllowAccess 批量第三键入口已清除(P4-70 ①):批量端点不再
// 收 action=allow_access(400);批量两键照常(加入资产逐笔留痕、失败项
// 如实回传)。
func TestP468BatchAllowAccess(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	const task = "p467-srv"
	p468SeedScopeCard(t, store, task, "gitee.com", "443", "s1")
	p468SeedScopeCard(t, store, task, "docs.python.org", "", "s2")

	code, _ := p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/batch",
		map[string]any{"ids": []string{"sc-gitee.com:443", "sc-docs.python.org"},
			"action": "allow_access"})
	if code != http.StatusBadRequest {
		t.Fatalf("批量 allow_access 入口已清除,应 400: %d", code)
	}
	// 批量两键照常:未知卡如实失败回传。
	code, body := p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/approvals/batch",
		map[string]any{"ids": []string{"sc-gitee.com:443", "sc-docs.python.org", "sc-unknown.net"},
			"approved": true})
	if code != http.StatusOK {
		t.Fatalf("批量两键应 200: %d %v", code, body)
	}
	if body["handled"].(float64) != 2 {
		t.Fatalf("应处理 2 张: %v", body)
	}
	if fails, _ := body["failed"].([]any); len(fails) != 1 {
		t.Fatalf("未知卡应如实失败回传: %v", body["failed"])
	}
	ctx := context.Background()
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.InfoSourceHit("gitee.com") || roe.InfoSourceHit("docs.python.org") {
		t.Fatalf("批量两键(加入资产)不得入信息源清单: %+v", roe.InfoSources)
	}
}

// TestP468RedteamCardAllowAccess 完成标准①(红队可否决卡):第三键——
// 不收编(资产退役,不进正式分组)、消卡、host 入清单;红队命令路本无
// 拦截,照常可测。
func TestP468RedteamCardAllowAccess(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	ctx := context.Background()
	const task = "p467-srv"
	// 改任务形态为红队(created detail 不可改,另起任务走 manager 建
	// 不必要——直接种红队任务同款事件:本测试另建任务)。
	const rtask = "p468-red"
	if _, err := store.Append(ctx, rtask,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		// 红队发现路入图的范围外资产＋可否决卡(sc-)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-out", NodeType: rings.NodeAsset, Title: "gitee.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "overmind", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("gitee.com", ""), StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait,
			Note: "红队任务:范围外新资产已直接开始测试,可随时禁止"}},
	); err != nil {
		t.Fatal(err)
	}
	code, body := p468PostJSON(t, ts.URL+"/api/tasks/"+rtask+"/approvals/sc-gitee.com",
		map[string]any{"action": "allow_access"})
	if code != http.StatusOK {
		t.Fatalf("红队卡第三键应 200: %d %v", code, body)
	}
	// 不收编:资产退役(不进资产树正式分组)、host 入清单。
	n, err := store.Node(ctx, rtask, "a-out")
	if err != nil || n.Status != rings.AssetStatusRetired {
		t.Fatalf("红队允许访问＝不收编,资产应退役: %+v err=%v", n, err)
	}
	roe, _ := store.CurrentRoE(ctx, rtask)
	if !roe.InfoSourceHit("gitee.com") {
		t.Fatalf("host 应入清单: %+v", roe.InfoSources)
	}
}

// TestP468RoEMarkEndpoint 三通道同源(RoE 面板标记入口):mark 端点＝
// 卡第三键同一账本动作(入清单＋资产退役＋挂等单位唤醒);坏地址人话拒。
func TestP468RoEMarkEndpoint(t *testing.T) {
	_, ts, store := p467ServerEnv(t)
	const task = "p467-srv"
	p468SeedScopeCard(t, store, task, "gitee.com", "443", "s1")

	code, body := p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/roe/infosources/mark",
		map[string]any{"host": "gitee.com:443"})
	if code != http.StatusOK {
		t.Fatalf("mark 端点应 200: %d %v", code, body)
	}
	if body["host"] != "gitee.com" || body["woken_units"].(float64) != 1 {
		t.Fatalf("应归一 host 并唤醒挂等单位: %v", body)
	}
	ctx := context.Background()
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.InfoSourceHit("gitee.com") || len(roe.InfoSources) != 1 ||
		!strings.Contains(roe.InfoSources[0].Basis, "操作者标记") {
		t.Fatalf("mark 应与卡第三键同源入清单: %+v", roe.InfoSources)
	}
	// 未决卡自动按允许访问收口(对账在 mark 内触发)。
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("gitee.com", "443"))
	if last := rows[len(rows)-1]; last.Type != rings.ApprovalRecAccess {
		t.Fatalf("未决卡应自动收口为 access: %s", last.Type)
	}
	// 坏地址人话拒。
	code, body = p468PostJSON(t, ts.URL+"/api/tasks/"+task+"/roe/infosources/mark",
		map[string]any{"host": "not a host"})
	if code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "不是可识别的主机地址") {
		t.Fatalf("坏地址应人话拒: %d %v", code, body)
	}
}
