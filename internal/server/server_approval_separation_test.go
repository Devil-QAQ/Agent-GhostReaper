package server

import (
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"testing"
)

func seedSeparatedApprovals(t *testing.T, srv *Server, task string) *swarm.GateTicket {
	t.Helper()
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "隔离审批验收样本"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "site", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: "https://example.test:1122/"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "new-entry", NodeType: rings.NodeFact, Status: "candidate", Title: "新入口归属尚不明确", Detail: map[string]any{"evidence": "passive observation of a distinct entry", "new_asset": map[string]any{"class": "service", "title": "https://example.test:1123/"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overmind.Harvest(t.Context(), srv.Store, task); err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.Gate.Enter(swarm.ApprovalRequest{TaskID: task, StepID: "write-step", ActionLevel: rings.ActionModifyPersist, UnitID: "isolated", ToolName: "http_probe", ToolArgs: `{"url":"https://example.test:1122/upload"}`, Permission: &rings.PurposePermission{Actions: []string{"create"}, Resource: "上传文件", Purpose: "新增测试文件", Effects: "新增测试文件", Targets: []string{"https://example.test:1122"}}})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestAssetChoiceAndBusinessPermissionRemainSeparate(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := "separate-approval"
	ticket := seedSeparatedApprovals(t, srv, task)
	code, body := callJSON(t, mux, "GET", "/api/tasks/"+task+"/approvals", "")
	if code != 200 {
		t.Fatal(body)
	}
	pending := body["pending"].([]any)
	if len(pending) != 2 {
		t.Fatal(pending)
	}
	assetID := ""
	for _, row := range pending {
		p := row.(map[string]any)
		if p["kind"] == "asset" {
			assetID = p["id"].(string)
			if p["target"] != "https://example.test:1123/" {
				t.Fatal(p)
			}
		}
	}
	if assetID == "" {
		t.Fatal("missing asset choice", pending)
	}
	if code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/"+ticket.Req.ID, `{"approved":true}`); code != 200 {
		t.Fatal(out)
	}
	choices, _ := srv.Store.PendingAssetProposals(t.Context(), task)
	if len(choices) != 1 {
		t.Fatal("action grant decided asset membership", choices)
	}
	if code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/"+assetID, `{"approved":true}`); code != 200 {
		t.Fatal(out)
	}
	rows, _ := srv.Store.ApprovalRows(t.Context(), task)
	grants := 0
	for _, r := range rows {
		if r.Type == rings.ApprovalRecApproved {
			grants++
		}
	}
	if grants != 1 {
		t.Fatal("asset join generated a business permission", rows)
	}
}
