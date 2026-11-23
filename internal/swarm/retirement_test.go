package swarm

import (
	"context"
	"encoding/json"
	"ghostreaper/internal/rings"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservationSubmissionAfterRetirement(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	ctx := context.Background()
	const task = "observation-retirement"
	seedGraph(t, s, task, "读取响应中的服务标识")
	env := &unitEnv{Executor: NewExecutor(s, nil, filepath.Join(dir, "tasks"), Budget{}), TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	for _, tool := range env.toolset() {
		if strings.Contains(tool.Name(), "hypothesis") {
			t.Fatal("retired tool registered")
		}
	}
	if knownToolScopes["submit_hypothesis"] {
		t.Fatal("retired tool scope registered")
	}
	submit := env.submitCandidateFactTool()
	for _, field := range []string{`"inferred":true`, `"inferred":false`, `"hypothesis_id":"h-1"`} {
		before, _ := s.CurrentRevision(ctx, task)
		result, err := submit.Call(ctx, []byte(`{"statement":"响应显示服务标识","evidence":"Server: example",`+field+`}`), nil)
		if err != nil || !result.IsError {
			t.Fatalf("retired input accepted %s: %+v %v", field, result, err)
		}
		after, _ := s.CurrentRevision(ctx, task)
		if after != before {
			t.Fatal("rejection left partial event")
		}
	}
	for _, statement := range []string{"响应包含服务标识 example", "页面列出另一个地址，但本次没有请求该地址", "本次请求返回404，未取得页面内容"} {
		raw, _ := json.Marshal(map[string]any{"statement": statement, "evidence": "原始响应记录", "asset_id": "target", "result_types": []string{"plain"}})
		r, err := submit.Call(ctx, raw, nil)
		if err != nil || r.IsError {
			t.Fatalf("actual observation rejected: %+v %v", r, err)
		}
	}
	facts, err := s.NodesByType(ctx, task, rings.NodeFact, "candidate")
	if err != nil || len(facts) != 3 {
		t.Fatalf("observations lost: %+v %v", facts, err)
	}
	edges, _ := s.Edges(ctx, task, rings.EdgeProduces)
	if len(edges) != 3 {
		t.Fatalf("observation provenance lost: %+v", edges)
	}
}
