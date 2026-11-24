package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/webui"
)

func TestFactAttributionViewsAndExport(t *testing.T) {
	srv, h, _ := newInterjectTestServer(t)
	s := srv.Store
	const task = "fact-claim-views"
	appendEvents(t, s, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created", Title: "漏洞事实归属验收"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "g-main", NodeType: rings.NodeGoal, Title: "核对已有证据", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "a-site", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://fixture.example.test:8443", Status: "active", Detail: map[string]any{"source": "seed"}}},
	)
	for _, id := range []string{"s-proof", "s-supplement"} {
		appendEvents(t, s, task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeStep, Title: "核对配置读取结果", Status: "done", Detail: map[string]any{"action_level": "只读探测"}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: id, ToNode: "a-site"}},
		)
	}
	appendEvents(t, s, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-original", NodeType: rings.NodeTestClaim, Title: "配置可匿名读取", Status: rings.TestClaimConfirmed, Detail: map[string]any{"severity": "high", "step_id": "s-proof", "vulnclass": "信息暴露"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "tc-original", ToNode: "a-site"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-original", ToNode: "s-proof"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "test", Payload: rings.TestClaimRecord{ClaimID: "tc-original", Type: rings.TestClaimRecRepro, Title: "配置可匿名读取", Statement: "匿名访问可读取配置", Impact: "已读取部署信息", Severity: "high", Steps: []rings.ReproStep{{Seq: 1, Desc: "原始证明"}}}},
	)
	proof, _ := s.Node(t.Context(), task, "tc-original")
	_, previousVersion, _ := s.EvidenceVersions(t.Context(), task)
	blob, err := srv.Swarm.Blobs.Write([]byte("完整固定证据：password=fixture-only；未验证凭据有效性"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		id, title string
		detail    map[string]any
	}{
		{"f-secret", "配置包含明文凭据", map[string]any{"claim_id": "tc-original", "evidence_ref": blob}},
		{"f-env", "另一个环境的配置也可读取", map[string]any{"claim_id": "tc-original", "evidence": "固定实际结果：env=fixture-two"}},
		{"f-plain", "普通观察不属于该漏洞", map[string]any{"evidence": "固定观察：状态码404"}},
	} {
		appendEvents(t, s, task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: f.id, NodeType: rings.NodeFact, Title: f.title, Status: "candidate", Detail: f.detail}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s-supplement", ToNode: f.id}},
		)
	}
	if _, err := overmind.Harvest(t.Context(), s, task); err != nil {
		t.Fatal(err)
	}
	if _, current, err := s.EvidenceVersions(t.Context(), task); err != nil || current != previousVersion+2 {
		t.Fatal("new attributed evidence did not advance the shared report version", current, err)
	}
	if _, err := report.ArchiveTask(t.Context(), s, srv.Swarm.Blobs, task, previousVersion, "test"); err == nil {
		t.Fatal("archive accepted a stale evidence version")
	}
	var before []claimView
	get(t, h, "/api/tasks/"+task+"/claims", 200, &before)
	if len(before) != 1 || len(before[0].FactRefs) != 2 {
		t.Fatal("source facts unavailable without a display model", before)
	}
	// The normal display event updates the existing record. It may select
	// wording but cannot erase either explicit source by omitting facts.
	source, _ := s.ClaimTextSourceRev(t.Context(), task, "tc-original")
	appendEvents(t, s, task, rings.Event{Kind: rings.EventClaimText, Actor: "test", Payload: rings.ClaimTextPolish{ClaimID: "tc-original", SourceRev: source, Title: "配置暴露明文凭据", Statement: "匿名访问可读取多环境配置及明文凭据", Impact: "已取得配置内的明文凭据，未验证凭据有效性", Facts: []rings.FactStatement{}}})
	checkViews := func() {
		t.Helper()
		var list []claimView
		get(t, h, "/api/tasks/"+task+"/claims", 200, &list)
		var detail claimDetailView
		get(t, h, "/api/tasks/"+task+"/claims/tc-original", 200, &detail)
		if len(list) != 1 || list[0].ID != "tc-original" || len(list[0].FactRefs) != 2 || len(detail.Claim.FactRefs) != 2 || list[0].Impact != detail.Claim.Impact || list[0].Title != detail.Claim.Title || detail.EvidenceVersion != previousVersion+2 {
			t.Fatal("list and detail diverged", list, detail)
		}
		var graph panoSkeletonView
		get(t, h, "/api/tasks/"+task+"/pview/skeleton", 200, &graph)
		claims, linked := 0, false
		for _, n := range graph.Nodes {
			if n.T == "claim" {
				claims++
				if n.ID != list[0].ID || n.Ti != list[0].Title {
					t.Fatal("graph uses another claim or display title", n)
				}
			}
		}
		for _, e := range graph.Edges {
			if e.From == "s-supplement" && e.To == "tc-original" {
				linked = true
			}
		}
		if claims != 1 || !linked {
			t.Fatal("supplement has no provenance connection to the original finding", graph)
		}
		for _, format := range []string{report.FormatMD, report.FormatCSV, report.FormatJSON, report.FormatZip} {
			exported, err := report.TaskExport(t.Context(), s, srv.Swarm.Blobs, task, format, nil)
			if err != nil {
				t.Fatal(format, err)
			}
			if format != report.FormatZip {
				if !bytes.Contains(exported.Body, []byte(list[0].Impact)) {
					t.Fatalf("%s export retained old impact", format)
				}
				continue
			}
			zr, err := zip.NewReader(bytes.NewReader(exported.Body), int64(len(exported.Body)))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range zr.File {
				if strings.HasSuffix(f.Name, "/facts/f-secret.json") {
					r, _ := f.Open()
					raw, _ := io.ReadAll(r)
					r.Close()
					found = bytes.Contains(raw, []byte("password=fixture-only"))
				}
			}
			if !found {
				t.Fatal("evidence export dropped the supplemental original bytes")
			}
		}
		after, _ := s.Node(t.Context(), task, "tc-original")
		if after.Status != proof.Status || after.ConfirmedRev != proof.ConfirmedRev {
			t.Fatal("supplement changed original confirmation", after)
		}
	}
	checkViews()
	if err := s.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkViews()
	// Opt-in browser acceptance of these exact formal panel endpoints. No
	// scheduler or model execution is started; all addresses are synthetic.
	if ready := os.Getenv("GHOSTREAPER_FACT_PANEL_READY"); ready != "" {
		h.Handle("/", webui.Handler())
		ts := httptest.NewServer(h)
		defer ts.Close()
		info, _ := json.Marshal(map[string]string{"url": ts.URL, "task": task})
		if err := os.WriteFile(ready, info, 0600); err != nil {
			t.Fatal(err)
		}
		stop := os.Getenv("GHOSTREAPER_FACT_PANEL_STOP")
		deadline := time.Now().Add(15 * time.Minute)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(stop); err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("isolated panel acceptance timed out")
	}
}

func TestFactAttributionNotifiesFindingsAndPanorama(t *testing.T) {
	hub := &liveHub{dirty: map[string]bool{}, poke: make(chan struct{}, 1)}
	hub.onCommit("task", []rings.Event{{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "tc-original", ToNode: "f-new"}}})
	if !hub.dirty[liveTopicClaims+"task"] || !hub.dirty[liveTopicPview+"task"] {
		t.Fatal("source updates failed to refresh both formal views", hub.dirty)
	}
}
