package swarm

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func TestCandidateContractErrorsGiveExactFieldsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedGraph(t, store, "contract", "核对实际材料")
	e := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: e, TaskID: "contract", StepID: "s1", UnitID: "u1", TranscriptID: "contract-session"}
	entry := env.submitCandidateFactTool()
	for _, input := range []string{
		`{"statement":"实际观察","evidence":"实际证据","asset":"target"}`,
		`{"statement":"实际观察","evidence":"实际证据","new_asset":{"class":"domain","title":"demo.example.test","parent_id":"target"}}`,
		`{"statement":"实际观察","evidence":"实际证据"} {"statement":"other"}`,
	} {
		res, err := entry.Call(t.Context(), json.RawMessage(input), nil)
		if err != nil || !res.IsError || !strings.Contains(res.Flatten(), "可用字段:") || !strings.Contains(res.Flatten(), "asset_id") || !strings.Contains(res.Flatten(), "new_asset字段: class, independent, parent, title") {
			t.Fatalf("contract feedback lacks actual supported fields: %v %s", err, res.Flatten())
		}
	}
	facts, _ := store.NodesByType(t.Context(), "contract", rings.NodeFact, "")
	if len(facts) != 0 {
		t.Fatal("malformed submission partially wrote a fact")
	}
	// Correct the field once, keeping the actual observation and evidence.
	res, err := entry.Call(t.Context(), json.RawMessage(`{"statement":"实际观察","evidence":"实际证据","asset_id":"target"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("corrected contract failed: %v %s", err, res.Flatten())
	}
	facts, _ = store.NodesByType(t.Context(), "contract", rings.NodeFact, "")
	if len(facts) != 1 || facts[0].Title != "实际观察" || facts[0].Detail["evidence"] != "实际证据" {
		t.Fatal("correcting parameters changed the observation")
	}
}

func TestCandidateAssetContractUsesInventoryIdentity(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedGraph(t, store, "identity", "实际观察")
	e := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: e, TaskID: "identity", StepID: "s1", UnitID: "u1"}
	props := candidateFactSchema()["properties"].(map[string]any)
	classes := props["new_asset"].(map[string]any)["properties"].(map[string]any)["class"].(map[string]any)["enum"]
	if !reflect.DeepEqual(classes, []string{"domain", "host", "service", "miniprogram", "app"}) {
		t.Fatal("schema invites background records as assets")
	}
	id, err := env.SubmitCandidateFact(t.Context(), "服务返回实际内容", "固定证据", "", &NewAssetDecl{Class: "service", Title: "https://DEMO.example.test/path?a=1"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.Node(t.Context(), "identity", id)
	if err != nil {
		t.Fatal(err)
	}
	decl := n.Detail["new_asset"].(map[string]any)
	if decl["class"] != "service" || decl["title"] != "https://demo.example.test/path?a=1" {
		t.Fatalf("service identity drift: %+v", decl)
	}
	_, err = env.SubmitCandidateFact(t.Context(), "登记机构名称出现", "固定背景证据", "", &NewAssetDecl{Class: "organization", Title: "测试组织"})
	if err == nil || !strings.Contains(err.Error(), "背景资料保留在statement和evidence") {
		t.Fatal("background asset declaration accepted or lost the correction route")
	}
	if _, err := env.SubmitCandidateFact(t.Context(), "登记机构名称出现", "固定背景证据", "", nil); err != nil {
		t.Fatal("background observation itself was blocked")
	}
}
