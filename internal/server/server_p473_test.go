package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestServerP473ReproPageFlowIncomplete P4-73 ④:复现步骤分段端点随页下发
// steps_incomplete(包内只有证明交换种子、流程步骤补全失败时的如实标注,
// 面板同句渲染);旧记录无标注零下发。
func TestServerP473ReproPageFlowIncomplete(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"P473 验证"}`, http.StatusOK, &created)
	task := created["id"]
	seedFullClaim(t, store, task)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-f", Type: rings.TestClaimRecRepro, Severity: "high",
			StepsIncomplete: "流程步骤未生成（挑选失败）",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 2, StepID: "sf2", UnitID: "u3",
				Short: "拿备份列举", Desc: "向 mock 发 GET /old-backup/,无需凭据;返回 200 且含备份列举即复现",
				Method: "GET", URL: "http://mock/old-backup/", Status: 200, BlobRef: "sha256:p473",
				ExchangeID: "ms-p473",
			}},
		}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-f", Status: rings.TestClaimConfirmed}},
	)

	var page struct {
		StepsIncomplete string `json:"steps_incomplete"`
		NoReproReason   string `json:"no_repro_reason"`
		Total           int    `json:"total"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-f/repro", http.StatusOK, &page)
	if page.StepsIncomplete != "流程步骤未生成（挑选失败）" {
		t.Fatalf("分段页应随页下发流程步骤未生成标注: %+v", page)
	}
	if page.NoReproReason != "" || page.Total != 1 {
		t.Fatalf("有包不得带无复现包原因,步骤应在: %+v", page)
	}
}
