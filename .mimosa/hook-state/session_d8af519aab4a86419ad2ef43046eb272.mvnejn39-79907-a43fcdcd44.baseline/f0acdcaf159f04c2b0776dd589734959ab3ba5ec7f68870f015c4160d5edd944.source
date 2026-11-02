package swarm

// P4-12 ①(问题清单 #21)受控入口:submit_candidate_fact 的新对象声明
// new_asset——与 asset_id 互斥、12 类词表校验、标识必填、父资产须在图上;
// 声明随事实入 detail(收割时由主宰入图)。

import (
	"context"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

func TestSubmitCandidateFactNewAsset(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p412-entry"
	seedGraph(t, store, task, "枚举关联资产")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 合法声明:入 detail,事实本身不锚定资产(对象由收割入图)。
	fid, err := env.SubmitCandidateFact(ctx, "发现管理后台子域", "DNS 应答原文",
		"", &NewAssetDecl{Class: "subdomain", Title: "admin.example.com", Parent: "target"})
	if err != nil {
		t.Fatalf("带新对象声明的候选提交失败: %v", err)
	}
	n, err := store.Node(ctx, task, fid)
	if err != nil {
		t.Fatalf("查候选失败: %v", err)
	}
	decl, ok := n.Detail["new_asset"].(map[string]any)
	if !ok || decl["class"] != "subdomain" || decl["title"] != "admin.example.com" || decl["parent"] != "target" {
		t.Fatalf("new_asset 声明应随事实入 detail: %+v", n.Detail)
	}

	// asset_id 与 new_asset 互斥。
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "target", &NewAssetDecl{Class: "host", Title: "x.example.com"}); err == nil {
		t.Fatalf("asset_id 与 new_asset 同时给应拒绝")
	}
	// 类不在 12 类词表。
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "", &NewAssetDecl{Class: "galaxy", Title: "x.example.com"}); err == nil {
		t.Fatalf("非法类应拒绝")
	}
	// 标识缺失。
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "", &NewAssetDecl{Class: "host"}); err == nil {
		t.Fatalf("缺标识应拒绝")
	}
	// 父资产不在图上。
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "", &NewAssetDecl{Class: "host", Title: "x.example.com", Parent: "ghost"}); err == nil {
		t.Fatalf("幽灵父资产应拒绝")
	}
	// 非法声明不落任何候选(五次拒绝,加上首条合法共 1 条)。
	facts, _ := store.NodesByType(ctx, task, rings.NodeFact, "")
	if len(facts) != 1 {
		t.Fatalf("被拒提交不得落账: 实际 %d 条候选", len(facts))
	}
}
