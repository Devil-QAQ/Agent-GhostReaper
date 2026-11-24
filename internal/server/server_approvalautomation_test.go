package server

import (
	"ghostreaper/internal/rings"
	"testing"
)

func TestApprovalAutomationSwitchesAreIndependent(t *testing.T) {
	for _, flag := range []string{"auto_approve", "auto_join"} {
		t.Run(flag, func(t *testing.T) {
			srv, mux, _ := newInterjectTestServer(t)
			task := "auto-switches"
			seedSeparatedApprovals(t, srv, task)
			before, _ := srv.Store.ApprovalAutomation(t.Context(), task)
			if before.AutoApprove || before.AutoJoin {
				t.Fatal("automation defaulted on")
			}
			code, body := callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/automation", `{"`+flag+`":true}`)
			if code != 200 {
				t.Fatal(body)
			}
			pendingActions := srv.Gate.Pending(task)
			pendingAssets, _ := srv.Store.PendingAssetProposals(t.Context(), task)
			if flag == "auto_approve" && (len(pendingActions) != 0 || len(pendingAssets) != 1) {
				t.Fatal("action automation changed asset membership")
			}
			if flag == "auto_join" && (len(pendingActions) != 1 || len(pendingAssets) != 0) {
				t.Fatal("asset automation approved actions")
			}
			other, _ := srv.Store.ApprovalAutomation(t.Context(), "another-task")
			if other.AutoApprove || other.AutoJoin {
				t.Fatal("task options leaked")
			}
			rows, _ := srv.Store.ApprovalRows(t.Context(), task)
			for _, row := range rows {
				if row.Type == rings.ApprovalRecApproved && row.DecidedBy == "creator" {
					t.Fatal("automation created permanent manual authority")
				}
			}
			if code, body = callJSON(t, mux, "POST", "/api/tasks/"+task+"/approvals/automation", `{"`+flag+`":false}`); code != 200 {
				t.Fatal(body)
			}
			after, _ := srv.Store.ApprovalAutomation(t.Context(), task)
			if after.AutoApprove || after.AutoJoin {
				t.Fatal("switch did not turn off")
			}
		})
	}
}
