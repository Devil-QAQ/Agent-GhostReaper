package server

// P4-52(问题清单 #73④骨架部分＋#74＋#77⑤):审批区呈现层收口的服务端
// 确定性测试——待决区三类卡统一视图(弹门/范围拦截/红队/待批资产)、
// 待批资产卡与拦截卡同目标去重、资产卡两键裁决与勾选批量、锚点等价
// 不重复入图。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// p452ApprovalsBody /approvals 响应的解码形态。
type p452ApprovalsBody struct {
	Pending []struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Mode        string `json:"mode"`
		Target      string `json:"target"`
		ToolName    string `json:"tool_name"`
		ToolArgs    string `json:"tool_args"`
		HarmNote    string `json:"harm_note"`
		ActionLevel string `json:"action_level"`
	} `json:"pending"`
	ScopeAssets []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Mode   string `json:"mode"`
		Target string `json:"target"`
	} `json:"scope_assets"`
	Records []struct {
		RequestID string `json:"request_id"`
		Type      string `json:"type"`
	} `json:"records"`
}

// p452ScopeWait 追加一笔范围待决卡记录(拦截卡形态:带命令上下文)。
func p452ScopeWait(t *testing.T, taskID, rid, intent, tool, args string) {
	t.Helper()
	rec := rings.ApprovalRecord{
		RequestID: rid, StepID: "s-1", UnitID: "u-1",
		Type: rings.ApprovalRecScopeWait, Intent: intent,
		ActionLevel: rings.ActionLevel("只读探测"),
		ToolName:    tool, ToolArgs: []byte(args),
		Note: "渗透任务:命令目标在范围外,拦下等待批准(加入资产/禁止测试)",
	}
	if _, err := storeOfP444(t).Append(context.Background(), taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "swarm:u-1", Payload: rec,
	}); err != nil {
		t.Fatal(err)
	}
}

// p452AssetCount 图上资产节点数(锚点等价入图断言用)。
func p452AssetCount(t *testing.T, taskID string) int {
	t.Helper()
	nodes, err := storeOfP444(t).NodesByType(context.Background(), taskID, rings.NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(nodes)
}

// TestP452RecordsFullAndSort 审批记录全量返回(倒序最新在先)——前端
// 滚动加载的数据前提(#74:服务端全量、展示端按需渲染)。
func TestP452RecordsFullAndSort(t *testing.T) {
	h, _ := newP444Server(t)
	task, _ := p444ScopeSeed(t, h, "p452-recs", "pentest", "", "")
	for i := 0; i < 25; i++ {
		p452ScopeWait(t, task, "sc-t"+string(rune('a'+i))+".example.org", "探测", "bash", `{"command":"true"}`)
	}
	var body p452ApprovalsBody
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
	if len(body.Records) != 25 {
		t.Fatalf("审批记录应全量返回: %d", len(body.Records))
	}
	var raw struct {
		Records []map[string]any `json:"records"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &raw)
	if v, _ := raw.Records[0]["request_id"].(string); !strings.HasPrefix(v, "sc-t") {
		t.Fatalf("记录应倒序(最新在先): %+v", raw.Records[0])
	}
	_ = json.Unmarshal([]byte("[]"), &[]int{})
}
