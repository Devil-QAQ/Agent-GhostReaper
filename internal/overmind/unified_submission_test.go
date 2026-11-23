package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// This is a scripted orchestration test, not evidence of autonomous discovery.
// The ordinary unit reuses two explicitly cited observations without probing
// them again and submits through the same tool used for single-step output.
func TestCombinedDirectionUsesOrdinaryExecutionAndSubmission(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const task = "unified-combination"
	mkTask(t, store, task)
	p462SeedAsset(t, store, task, "asset", "t.example:8080")
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			return scriptedReply{Text: `{"assessment":{"q_combination":"两项已确认观察可复用，整体结论仍由执行单位核对交卷"},"new_intents":[{"intent":"核对两项观察的关联结果","expected_observation":"两份原始响应共同支持结论","action_level":"只读探测","covers":["asset"],"source_refs":["fact-one","fact-two"]}]}`, Finish: "stop"}
		}
		if seenToolResult(rr, "submit_claim", "已确认") {
			return scriptedReply{Text: "已复用两份原始证据并完成交卷。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "combined-submit", Name: "submit_claim", Args: `{"summary":"两项观察共同支持的测试结论","severity":"low","asset_id":"asset","evidence_refs":["http://t.example:8080/proof-one","http://t.example:8080/proof-two"],"proving_marker":"fixture-marker","countercheck_note":"核对两份固定响应的不同记录编号，未把同一响应重复计作两个观察","source_refs":["fact-one","fact-two"]}`}}, Finish: "tool_calls"}
	})
	defer model.Close()
	model.release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := swarm.NewExecutor(store, provider, t.TempDir(), swarm.Budget{MaxTurns: 4})
	for _, suffix := range []string{"one", "two"} {
		step, fact := "observe-"+suffix, "fact-"+suffix
		ref, err := ex.Blobs.Write([]byte("HTTP/1.1 200 OK\r\n\r\nfixture-marker record-" + suffix))
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Append(ctx, task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: step, NodeType: rings.NodeStep, Title: "原观察", Status: "done"}},
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: fact, NodeType: rings.NodeFact, Title: "固定响应 " + suffix, Status: "confirmed", Detail: map[string]any{"evidence": "固定响应记录 " + suffix}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: step, ToNode: fact}},
			rings.Event{Kind: rings.EventSwarmRecord, Actor: "test", Payload: rings.SwarmRecord{StepID: step, UnitID: step, Type: rings.SwarmRecToolCall, ToolName: "http_probe", ToolArgs: json.RawMessage(`{"url":"http://t.example:8080/proof-` + suffix + `"}`), ResultRef: ref}},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: provider}
	defer loop.polishWG.Wait()
	reply, _, err := loop.decompose(ctx)
	if err != nil || len(reply.NewIntents) != 1 {
		t.Fatalf("planning: %+v %v", reply, err)
	}
	if claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, ""); len(claims) != 0 {
		t.Fatal("planning created a candidate without execution")
	}
	id, err := loop.dispatch(ctx, reply.NewIntents[0], sourceExplore)
	if err != nil {
		t.Fatal(err)
	}
	ex.RegisterSubmissionHandlers(task, swarm.SubmissionHandlers{ClaimCheck: loop.CheckClaimOnSubmit})
	defer ex.UnregisterSubmissionHandlers(task)
	if _, err := ex.RunStep(ctx, task, id); err != nil {
		t.Fatal(err)
	}
	claims, err := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
	if err != nil || len(claims) != 1 || claims[0].Status != rings.TestClaimConfirmed {
		t.Fatalf("unified output: %+v %v", claims, err)
	}
	claim := claims[0]
	if claim.Detail["unit_id"] == ActorOvermind || claim.Detail["combined"] != nil {
		t.Fatal("retired direct-combination path was used")
	}
	repro := p473LatestRepro(t, store, task, claim.ID)
	if repro == nil || len(repro.Steps) != 2 {
		t.Fatalf("multi-reference proof lost: %+v", repro)
	}
	events, _ := store.Events(ctx, task, 0, 0)
	submits, verdicts, probes := 0, 0, 0
	for _, ev := range events {
		if ev.Kind == rings.EventTestClaimRecord {
			var r rings.TestClaimRecord
			_ = json.Unmarshal(ev.Payload, &r)
			if r.ClaimID == claim.ID && r.Type == rings.TestClaimRecSubmit {
				submits++
			}
			if r.ClaimID == claim.ID && r.Type == rings.TestClaimRecVerdict {
				verdicts++
			}
		}
		if ev.Kind == rings.EventSwarmRecord {
			var r rings.SwarmRecord
			_ = json.Unmarshal(ev.Payload, &r)
			if r.Type == rings.SwarmRecToolCall && (r.ToolName == "http_probe" || r.ToolName == "bash") {
				probes++
			}
		}
	}
	if submits != 1 || verdicts != 1 || probes != 2 {
		t.Fatalf("submits=%d verdicts=%d probes=%d; existing observations must not be repeated", submits, verdicts, probes)
	}
}

func TestPlannerHasOneSubmissionContractAndCurrentActionRules(t *testing.T) {
	for _, retired := range []string{"combined_claims", "执行时逐次需批准", "无论审批门槛多宽都直接拒绝"} {
		if strings.Contains(overmindOutputContract+overmindDisciplineRules, retired) {
			t.Fatalf("retired planner instruction: %s", retired)
		}
	}
	for _, want := range []string{"submit_claim", "只补缺失验证", "硬规则", "具体动作"} {
		if !strings.Contains(overmindDisciplineRules, want) {
			t.Fatalf("missing unified semantics: %s", want)
		}
	}
}
