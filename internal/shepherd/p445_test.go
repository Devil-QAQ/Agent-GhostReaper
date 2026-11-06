package shepherd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-45 ②:牧者资产范围工具(set_asset_scope) ----
// 纳入(渗透任务待批放行/摘除移回;红队如实"本就全开")与摘除(触发
// 止损链路)两向;与 RoE 面板同源(swarm 资产范围操作)。

// runScopeTool 直接调 set_asset_scope 工具(工具逻辑确定性单测;对话
// 编排在既有 scripted 测试模式覆盖,活体在验收实证)。返回(正文, 是否错误)。
func runScopeTool(t *testing.T, s *Service, task string, args string) (string, bool) {
	t.Helper()
	for _, tl := range s.toolset(task, "sm-test") {
		if tl.Name() != "set_asset_scope" {
			continue
		}
		res, err := tl.Call(context.Background(), json.RawMessage(args), nil)
		if err != nil {
			t.Fatalf("工具执行失败: %v", err)
		}
		return res.Flatten(), res.IsError
	}
	t.Fatal("工具集应含 set_asset_scope")
	return "", false
}

// p445ShepherdEnv 牧者测试任务(类型可选)+ 种子 a-x + 范围外发现
// a-disc + 覆盖 a-x 的排队步骤;返回服务、store、任务 ID、唤醒通道。
func p445ShepherdEnv(t *testing.T, taskType string) (*Service, *rings.Store, string, chan string) {
	t.Helper()
	store := openStore(t)
	const task = "sh-p445"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": taskType}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-disc", NodeType: rings.NodeAsset, Title: "api.other.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-q", NodeType: rings.NodeStep, Title: "排队探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-q", ToNode: "a-x"}},
	); err != nil {
		t.Fatal(err)
	}
	woken := make(chan string, 8)
	s := &Service{Store: store, RootDir: t.TempDir(), Wake: func(id string) { woken <- id }}
	return s, store, task, woken
}

func TestP445SetAssetScopeExclude(t *testing.T) {
	s, store, task, woken := p445ShepherdEnv(t, rings.TaskTypeRedteam)
	ctx := context.Background()
	out, isErr := runScopeTool(t, s, task, `{"action":"exclude","assets":["a-x"]}`)
	if isErr {
		t.Fatalf("摘除不应报错: %s", out)
	}
	if !strings.Contains(out, "已摘除 a-x") || !strings.Contains(out, "终止在途单位 0 个,取消排队步骤 1 个") {
		t.Fatalf("人话答复应含摘除与止损笔数: %s", out)
	}
	// 同源:roe 状态与步骤终态(RoE 面板读同一账本)。
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ExcludedAssets) != 1 || roe.ExcludedAssets[0] != "a-x" {
		t.Fatalf("摘除清单应含 a-x: %+v", roe.ExcludedAssets)
	}
	if st, _ := store.Node(ctx, task, "s-q"); st.Status != "aborted" {
		t.Fatalf("覆盖步骤应已中止: %+v", st)
	}
	// 唤醒被触发(纳入/摘除都要唤醒主宰)。
	select {
	case id := <-woken:
		if id != task {
			t.Fatalf("唤醒了错误任务: %s", id)
		}
	default:
		t.Fatal("摘除后任务未被唤醒")
	}
	// 重复摘除:如实答复已在范围外。
	out, isErr = runScopeTool(t, s, task, `{"action":"exclude","assets":["a-x"]}`)
	if isErr || !strings.Contains(out, "已在范围外") {
		t.Fatalf("重复摘除应如实: %s", out)
	}
	// 未知资产:报错带可用资产(模型自纠面)。
	out, isErr = runScopeTool(t, s, task, `{"action":"exclude","assets":["a-nonexist"]}`)
	if !isErr || !strings.Contains(out, "不在本任务图上") {
		t.Fatalf("未知资产应报错: %s", out)
	}
}

func TestP445SetAssetScopeIncludePentest(t *testing.T) {
	s, store, task, _ := p445ShepherdEnv(t, rings.TaskTypePentest)
	ctx := context.Background()
	// 渗透任务:a-disc 待批 → 纳入即放行;种子 a-x 纳入如实"本就在范围内"。
	if pending, _ := store.PendingScopeAssets(ctx, task); len(pending) != 1 {
		t.Fatalf("前置:a-disc 应待批: %+v", pending)
	}
	out, isErr := runScopeTool(t, s, task, `{"action":"include","assets":["a-disc","a-x"]}`)
	if isErr {
		t.Fatalf("纳入不应报错: %s", out)
	}
	if !strings.Contains(out, "已放行待批资产 a-disc") || !strings.Contains(out, "a-x(x.example.com) 本就在测试范围内") {
		t.Fatalf("纳入答复应分待批放行与本就在范围内: %s", out)
	}
	if pending, _ := store.PendingScopeAssets(ctx, task); len(pending) != 0 {
		t.Fatalf("放行后应无待批: %+v", pending)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ScopeApproved) != 1 || roe.ScopeApproved[0] != "a-disc" {
		t.Fatalf("放行清单应含 a-disc: %+v", roe.ScopeApproved)
	}
}

func TestP445SetAssetScopeIncludeRedTeam(t *testing.T) {
	s, store, task, _ := p445ShepherdEnv(t, rings.TaskTypeRedteam)
	ctx := context.Background()
	// 红队:范围外发现资产本就可测,纳入如实答复"本就全部可测"。
	out, isErr := runScopeTool(t, s, task, `{"action":"include","assets":["a-disc"]}`)
	if isErr {
		t.Fatalf("纳入不应报错: %s", out)
	}
	if !strings.Contains(out, "本就全部可测") {
		t.Fatalf("红队纳入应如实答复本就全开: %s", out)
	}
	// 零变更不落账。
	before, _ := store.CurrentRevision(ctx, task)
	runScopeTool(t, s, task, `{"action":"include","assets":["a-x"]}`)
	after, _ := store.CurrentRevision(ctx, task)
	if after != before {
		t.Fatalf("无变更不应落账: %d → %d", before, after)
	}
	// action 词表外:报错。
	if _, isErr := runScopeTool(t, s, task, `{"action":"toggle","assets":["a-x"]}`); !isErr {
		t.Fatal("词表外 action 应报错")
	}
	// 空清单:报错。
	if _, isErr := runScopeTool(t, s, task, `{"action":"include","assets":[]}`); !isErr {
		t.Fatal("空清单应报错")
	}
}
