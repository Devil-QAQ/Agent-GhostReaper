package overmind

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"github.com/Autumn-27/norma/llm"
)

// A real model uses production tools against an owned, synthetic HTTP service.
// The intent asks for discovery, not a scripted instruction to call submit_claim.
func intentLiveProvider(t *testing.T) llm.Provider {
	t.Helper()
	file := os.Getenv("GHOSTREAPER_INTENT_MODEL_FILE")
	if file == "" {
		t.Skip("real-model configuration not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("cannot read private model configuration")
	}
	var cfg struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Format  string `json:"format"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private model configuration")
	}
	provider, err := llm.NewProvider(llm.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, Format: llm.Format(cfg.Format), MaxRetries: -1})
	if err != nil {
		t.Fatal("cannot initialize private model provider")
	}
	return provider
}

func TestIntentCompletionLiveOriginalConversation(t *testing.T) {
	provider := intentLiveProvider(t)
	for _, scenario := range []string{"independent", "existing-root", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if scenario == "ordinary" {
					w.Write([]byte(`{"service":"FixtureDocs","version":"1.0","visibility":"public documentation"}`))
					return
				}
				w.Write([]byte(`{"policy":"private records require authentication","private_test_record":{"note":"fixture_private_604","record_id":"synthetic_17"}}`))
			}))
			defer target.Close()
			store := openStore(t)
			const task = "intent-live"
			mkTask(t, store, task)
			intent := "探索这个测试网站的访问控制，确认实际暴露的记录及其影响"
			if scenario == "ordinary" {
				intent = "核对这个测试网站公开的服务名称与版本信息"
			}
			events := []rings.Event{
				{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{AllowedTools: []string{"http_probe", "submit_candidate_fact", "submit_claim", "search_facts", "list_claims", "search_trails"}, CreatorNotes: new("仅访问本任务明确给出的本机合成服务，所有记录都是固定测试数据，不访问其他对象。")}},
				{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a-fixture", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: target.URL, Status: "active", Detail: map[string]any{"operator_target": true}}},
				{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: "dispatched", Title: intent, Detail: map[string]any{"action_level": "只读探测", "expected_observation": "实际响应与访问前提"}}},
				{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a-fixture"}},
			}
			if scenario == "existing-root" {
				events = append(events,
					rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-existing", NodeType: rings.NodeTestClaim, Status: rings.TestClaimConfirmed, Title: "本系统未登录可读取私有测试记录", Detail: map[string]any{"severity": "high"}}},
					rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "tc-existing", ToNode: "a-fixture"}},
				)
			}
			if _, err := store.Append(t.Context(), task, events...); err != nil {
				t.Fatal(err)
			}
			ex := swarm.NewExecutor(store, provider, t.TempDir(), swarm.Budget{MaxTurns: 12, RunSeconds: 90})
			ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())
			loop := &Loop{Store: store, TaskID: task, Swarm: ex}
			ex.RegisterSubmissionHandlers(task, swarm.SubmissionHandlers{ClaimCheck: loop.CheckClaimOnSubmit})
			defer ex.UnregisterSubmissionHandlers(task)
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
			defer cancel()
			result, err := ex.RunStep(ctx, task, "s1")
			if err != nil || result.StepStatus != "done" {
				t.Fatalf("original intent did not finish: error=%t", err != nil)
			}
			if _, err := Harvest(t.Context(), store, task); err != nil {
				t.Fatal(err)
			}
			claims, err := store.NodesByType(t.Context(), task, rings.NodeTestClaim, "")
			if err != nil {
				t.Fatal(err)
			}
			facts, err := store.NodesByType(t.Context(), task, rings.NodeFact, "confirmed")
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "independent":
				if len(claims) != 1 || claims[0].Status != rings.TestClaimConfirmed {
					t.Fatalf("independent result: claims=%d facts=%d final=%s", len(claims), len(facts), result.FinalText)
				}
			case "existing-root":
				attributed := false
				for _, fact := range facts {
					if fact.Detail["claim_id"] == "tc-existing" {
						attributed = true
					}
				}
				if len(claims) != 1 || !attributed {
					t.Fatal("same-root evidence was duplicated or left unattributed")
				}
			case "ordinary":
				if len(claims) != 0 || len(facts) == 0 {
					t.Fatal("ordinary observation did not finish as a fact")
				}
			}
			steps, _ := store.NodesByType(t.Context(), task, rings.NodeStep, "")
			if len(steps) != 1 {
				t.Fatal("completion created a hand-off intent")
			}
			rows, _ := store.UnitSessions(t.Context(), task)
			if len(rows) != 1 {
				t.Fatalf("intent changed conversation: sessions=%d", len(rows))
			}
			t.Logf("原意图完成：会话%d，事实%d，漏洞%d，轮数%d", len(rows), len(facts), len(claims), result.Turns)
		})
	}
}

func TestIntentRecoveryLivePlannerWaitsForOriginalWork(t *testing.T) {
	provider := intentLiveProvider(t)
	for _, status := range []string{"running", rings.StepScopeWait, rings.StepGateWait, rings.StepPauseWait, rings.StepModelWait} {
		t.Run(status, func(t *testing.T) {
			const task = "intent-wait-live"
			store := openStore(t)
			mkTask(t, store, task)
			if _, err := store.Append(t.Context(), task,
				rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{CreatorNotes: new("任务只核查指定测试记录的访问控制，不扩展其他目标或功能。")}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "g1", NodeType: rings.NodeGoal, Status: GoalStatusActive, Title: "完成指定测试记录的访问控制核查"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: "https://fixture.example.test:8443"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: status, Title: "核查指定测试记录的访问控制并完成自查及结果写回", Detail: map[string]any{"action_level": "只读探测"}}},
				rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Status: "confirmed", Title: "已取得一份测试记录，原意图尚待完成访问前提自查和结果写回"}},
				rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f1"}},
			); err != nil {
				t.Fatal(err)
			}
			l := &Loop{Store: store, TaskID: task, Provider: provider, RootDir: t.TempDir()}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			reply, _, err := l.decompose(ctx)
			if err != nil {
				t.Fatal("planner model or output contract failed; private errors withheld")
			}
			if len(reply.NewIntents) != 0 {
				t.Fatalf("waiting original work was duplicated: state=%s new=%d", status, len(reply.NewIntents))
			}
			t.Log("原工作仍持有意图，未派发现／复核／交卷接力")
		})
	}
}
