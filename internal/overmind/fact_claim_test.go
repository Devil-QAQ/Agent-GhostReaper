package overmind

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestFactAttributionToolToHarvestSummaryAndReport(t *testing.T) {
	s := openStore(t)
	const task = "claim-attribution"
	seedDiscoveredGraph(t, s, task)
	_, err := s.Append(t.Context(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-original", NodeType: rings.NodeTestClaim, Title: "配置文件可匿名读取", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "tc-original", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "test", Payload: rings.TestClaimRecord{ClaimID: "tc-original", Type: rings.TestClaimRecRepro, Statement: "配置文件可匿名读取", Impact: "已读到部署信息，其他内容尚未验证", Severity: "high", Steps: []rings.ReproStep{{Seq: 1, Desc: "原始证明保留"}}}})
	if err != nil {
		t.Fatal(err)
	}
	original, _ := s.Node(t.Context(), task, "tc-original")
	oldSource, _ := s.ClaimTextSourceRev(t.Context(), task, "tc-original")
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "actual-attribution", Name: "submit_candidate_fact", Args: `{"statement":"配置中取得明文凭据，未验证凭据有效性","evidence":"人工固定原文：password=fixture-only","asset_id":"a-seed","claim_id":"tc-original","result_types":["data"]}`}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "已补充原漏洞。", Finish: "stop"}
	})
	model.release()
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := swarm.NewExecutor(s, provider, filepath.Join(t.TempDir(), "tasks"), swarm.Budget{})
	if result, err := ex.RunStep(t.Context(), task, "s1"); err != nil || result.StepStatus != "done" {
		t.Fatal(result, err)
	}
	harvest, err := Harvest(t.Context(), s, task)
	if err != nil || len(harvest.Confirmed) != 1 {
		t.Fatal(harvest, err)
	}
	id := harvest.Confirmed[0]
	check := func() {
		t.Helper()
		claims, _ := s.NodesByType(t.Context(), task, rings.NodeTestClaim, "")
		current, _ := s.Node(t.Context(), task, "tc-original")
		if len(claims) != 1 || current.Status != original.Status || current.ConfirmedRev != original.ConfirmedRev {
			t.Fatal("new evidence changed claim identity or confirmation", claims)
		}
		cs, err := report.ClaimCaseOf(t.Context(), s, nil, task, "tc-original")
		if err != nil || cs.Summary == nil || len(cs.Summary.Facts) != 1 || cs.Summary.Facts[0].FactID != id || cs.Repro == nil || cs.Repro.Steps[0].Desc != "原始证明保留" {
			t.Fatal("shared case lost facts or original proof", cs, err)
		}
	}
	check()
	if _, err := s.AppendDisplay(t.Context(), task, rings.Event{Kind: rings.EventClaimText, Actor: "test", Payload: rings.ClaimTextPolish{ClaimID: "tc-original", SourceRev: oldSource, Title: "旧稿", Statement: "旧稿", Impact: "尚未读到凭据"}}); err == nil {
		t.Fatal("stale generation overwrote new evidence")
	}
	// Explicit new sources must not disappear behind the eight-observation
	// context cap, even after the original discovery accumulated more facts.
	for i := 0; i < 12; i++ {
		fid := fmt.Sprintf("f-extra-%02d", i)
		if _, err := s.Append(t.Context(), task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: fid, NodeType: rings.NodeFact, Status: "confirmed", Title: "同根因的补充实际观察"}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "tc-original", ToNode: fid}}); err != nil {
			t.Fatal(err)
		}
	}
	l := &Loop{TaskID: task, Store: s}
	pack, err := l.summaryPack(t.Context(), "tc-original")
	if err != nil || len(pack.Facts) != 13 {
		t.Fatal("explicit attribution was truncated", pack, err)
	}
	if err := s.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, _, _ := s.ClaimCaseOf(t.Context(), task, "tc-original")
	if len(row.Summary.Facts) != 13 || row.Repro.Steps[0].Desc != "原始证明保留" {
		t.Fatal("replay lost supplemental evidence or original proof", row)
	}
}

func TestAttributedFactsRefreshExistingClaimAndRecoverAfterRestart(t *testing.T) {
	s := openStore(t)
	const task = "claim-refresh"
	mkTask(t, s, task)
	_, err := s.Append(t.Context(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-original", NodeType: rings.NodeTestClaim, Title: "配置可匿名读取", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "test", Payload: rings.TestClaimRecord{ClaimID: "tc-original", Type: rings.TestClaimRecRepro, Title: "配置可匿名读取", Statement: "匿名读取配置", Impact: "凭据尚未取得", Severity: "high", Steps: []rings.ReproStep{{Seq: 1, Desc: "原始证明"}}}})
	if err != nil {
		t.Fatal(err)
	}
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "事实原文与证据摘要:") {
			return cleanDisplayFactReply(t, rr)
		}
		return scriptedReply{Text: `{"title":"配置暴露明文凭据","statement":"匿名读取配置及其中的明文凭据","impact":"已取得配置内的明文凭据，未验证凭据有效性","facts":[]}`, Finish: "stop"}
	})
	model.release()
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	add := func(id string) {
		t.Helper()
		if _, err := s.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeFact, Status: "candidate", Title: "配置中取得明文凭据，未验证凭据有效性", Detail: map[string]any{"claim_id": "tc-original", "evidence": "固定实际原文：password=fixture"}}}); err != nil {
			t.Fatal(err)
		}
	}
	loop := &Loop{Store: s, TaskID: task, Provider: provider}
	add("f-first")
	if err := loop.processResults(t.Context()); err != nil {
		t.Fatal(err)
	}
	loop.polishWG.Wait()
	check := func(count int) {
		t.Helper()
		row, _, err := s.ClaimCaseOf(t.Context(), task, "tc-original")
		if err != nil || row.Summary.Impact != "已取得配置内的明文凭据，未验证凭据有效性" || len(row.Summary.Facts) != count || row.Repro.Steps[0].Desc != "原始证明" {
			t.Fatal("new evidence failed to refresh the existing case", row, err)
		}
	}
	check(1)
	add("f-after-restart")
	if _, err := Harvest(t.Context(), s, task); err != nil {
		t.Fatal(err)
	}
	restarted := &Loop{Store: s, TaskID: task, Provider: provider}
	restarted.recoverDisplay(t.Context())
	restarted.polishWG.Wait()
	check(2)
	before := len(model.requests())
	completed := &Loop{Store: s, TaskID: task, Provider: provider}
	completed.recoverDisplay(t.Context())
	completed.polishWG.Wait()
	if len(model.requests()) != before {
		t.Fatal("unchanged sources triggered repeated regeneration")
	}
}
