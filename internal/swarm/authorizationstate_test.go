package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
)

func TestApprovalSurvivesConcurrentInventoryAndUnrelatedDecision(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	seedReviewAuthorization(t, env)
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{Decision: ActionApproval, Reason: "需确认操作影响"}, nil
	}
	callActionGate(env, pack, "bash", `{"command":"echo first"}`)
	request := env.park.Req
	if err := gate.Decide(t.Context(), pack.TaskID, request.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	env.park = nil
	env.Executor.ReviewAction = func(_ context.Context, in ActionReviewInput) (ActionReview, error) {
		if len(in.Boundaries.Approvals) != 1 {
			t.Fatal("earlier permission disappeared")
		}
		_, err := env.Executor.Store.Append(t.Context(), pack.TaskID,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "new-port", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://example.test:9443", Detail: map[string]any{"source": "discovered", "adopted": true}}},
			rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{RequestID: "another", StepID: pack.StepID, UnitID: env.UnitID, ActionLevel: pack.ActionLevel, ToolName: "bash", ToolArgs: json.RawMessage(`{"command":"echo other"}`), Type: rings.ApprovalRecDenied, DecidedBy: "creator", Reason: "拒绝另一项操作"}})
		if err != nil {
			t.Fatal(err)
		}
		return ActionReview{Decision: ActionAllow, Reason: "沿用原批准的目的、对象与影响"}, nil
	}
	if d := callActionGate(env, pack, "bash", `{"command":"printf second"}`); d.Behavior != permission.Allow || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatal("inventory invalidated review", d)
	}
}

func TestApprovalSourceSnapshotIsDurableAndChangeOnlyRevokesThatContext(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.RootDir = t.TempDir()
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "work.py")
	if err := os.WriteFile(path, []byte("print('local')"), 0600); err != nil {
		t.Fatal(err)
	}
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		_, err := env.readReviewSource(path)
		return ActionReview{Decision: ActionApproval, Reason: "核对影响"}, err
	}
	callActionGate(env, pack, "bash", `{"command":"python3 work.py"}`)
	request := env.park.Req
	if err := gate.Decide(t.Context(), pack.TaskID, request.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	snapshots, err := env.Executor.Store.ApprovalSources(t.Context(), pack.TaskID)
	if err != nil || len(snapshots[request.ID]) != 1 {
		t.Fatal("snapshot not recorded", err)
	}
	roe, _ := env.Executor.Store.CurrentRoE(t.Context(), pack.TaskID)
	before, err := env.actionBoundary(t.Context(), pack, roe)
	if err != nil || len(before.Approvals) != 1 {
		t.Fatal("valid approval lost", err)
	}
	if err := os.WriteFile(path, []byte("import urllib.request; urllib.request.urlopen('https://elsewhere.test')"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := env.actionBoundary(t.Context(), pack, roe)
	if err != nil || len(after.Approvals) != 0 || approvalsStillCurrent(before, after) {
		t.Fatal("changed script inherited old permission", err)
	}
}

func TestActualInvocationReviewDoesNotSendWorkerPlan(t *testing.T) {
	provider := contextReviewProvider{run: func(request llm.CompletionRequest) llm.Message {
		var body map[string]json.RawMessage
		if json.Unmarshal([]byte(request.Messages[0].Content[0].Text), &body) != nil || body["intent"] != nil || body["goal"] != nil {
			t.Fatal("worker plan reached action adjudication")
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(`{"reason":"实际调用只读本地文件","changes":[]}`)}}
	}}
	_, err := ActionReviewFor(provider)(t.Context(), ActionReviewInput{Tool: "read", Arguments: json.RawMessage(`{"path":"/tmp/report.txt"}`), Goal: "扫描外部", Intent: "连接其他端口"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInternalOutputReadIsPlatformCapability(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	env.Executor.Blobs = NewBlobStore(t.TempDir())
	ref, err := env.Executor.Blobs.Write([]byte("contains network plans and commands"))
	if err != nil {
		t.Fatal(err)
	}
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		t.Fatal("internal artifact treated as environment operation")
		return ActionReview{}, nil
	}
	args, _ := json.Marshal(map[string]any{"path": ref, "offset": 60, "limit": 200})
	if d := callActionGate(env, pack, "read", string(args)); d.Behavior != permission.Allow || len(gate.Pending(pack.TaskID)) != 0 {
		t.Fatal(d)
	}
	if env.internalArtifactRead("bash", args) || env.internalArtifactRead("read", json.RawMessage(`{"path":"sha256:bad"}`)) {
		t.Fatal("invalid capability exemption")
	}
}

func TestPurposePermissionPersistsAndRevocationIsSpecific(t *testing.T) {
	env, pack, gate := actionGateFixture(t)
	scope := &rings.PurposePermission{Purpose: "新增测试用户", Targets: []string{"https://example.test"}, Effects: "创建测试用户，不修改已有数据"}
	env.Executor.ReviewAction = func(context.Context, ActionReviewInput) (ActionReview, error) {
		return ActionReview{Decision: ActionApproval, Reason: "新增用户需确认", Permission: scope}, nil
	}
	callActionGate(env, pack, "bash", `{"command":"curl -X POST -d name=test-user https://example.test/users"}`)
	request := env.park.Req
	if request.Permission == nil || request.Note == "新增用户需确认" {
		t.Fatal("approval card does not state reusable scope")
	}
	if err := gate.Decide(t.Context(), pack.TaskID, request.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatal(err)
	}
	// Fresh environment: reconstruction comes only from durable state.
	restored := &unitEnv{Executor: env.Executor, TaskID: env.TaskID, StepID: env.StepID, UnitID: "new-unit"}
	roe, _ := env.Executor.Store.CurrentRoE(t.Context(), pack.TaskID)
	boundary, err := restored.actionBoundary(t.Context(), pack, roe)
	if err != nil || len(boundary.Approvals) != 1 || boundary.Approvals[0].Permission == nil || boundary.Approvals[0].Permission.Purpose != scope.Purpose {
		t.Fatal("purpose grant was lost across recovery", err)
	}
	_, err = env.Executor.Store.Append(t.Context(), pack.TaskID, rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{RequestID: request.ID, Type: rings.ApprovalRecReversed, DecidedBy: "creator", Reason: "撤回此类新增"}})
	if err != nil {
		t.Fatal(err)
	}
	boundary, err = restored.actionBoundary(t.Context(), pack, roe)
	if err != nil || len(boundary.Approvals) != 0 {
		t.Fatal("revoked purpose still reusable", err)
	}
}

func TestWithdrawingOneWebsiteStopsItsResourcesOnly(t *testing.T) {
	env, pack, _ := actionGateFixture(t)
	seedReviewAuthorization(t, env)
	_, err := env.Executor.Store.Append(t.Context(), pack.TaskID,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "other-site", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://example.test:9443", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "major-interface", NodeType: rings.NodeAsset, AssetClass: rings.AssetAPI, Title: "POST https://example.test:8443/login", Status: "active"}})
	if err != nil {
		t.Fatal(err)
	}
	dispatchStepAtLevel(t, env.Executor.Store, pack.TaskID, "site-step", "入口验证", "g1", "只读探测", "a1")
	dispatchStepAtLevel(t, env.Executor.Store, pack.TaskID, "other-step", "另一网站验证", "g1", "只读探测", "other-site")
	dispatchStepAtLevel(t, env.Executor.Store, pack.TaskID, "interface-step", "接口验证", "g1", "只读探测", "major-interface")
	impact, err := ScopeImpactOf(t.Context(), env.Executor.Store, pack.TaskID, []string{"a1"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range impact.Queued {
		ids[s.StepID] = true
	}
	if len(ids) != 2 || !ids["site-step"] || !ids["interface-step"] || ids["other-step"] {
		t.Fatal("website withdrawal crossed port boundary", ids)
	}
}
