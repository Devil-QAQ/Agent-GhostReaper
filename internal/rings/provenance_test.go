package rings

import (
	"context"
	"errors"
	"testing"
)

func TestProvenanceSourcesAndRules(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustAppend(t, s, "t-source",
		na(NodeAsset, "a", "资产", "active", "domain", nil),
		na(NodeStep, "s", "意图", "running", "", nil),
		na(NodeFact, "f", "事实", "confirmed", "", nil),
		na(NodeTestClaim, "c", "漏洞", TestClaimConfirmed, "", nil),
		na(NodeStep, "next", "后续", "dispatched", "", nil),
		na(NodeFact, "unverified", "未确认", "candidate", "", nil),
		na(NodeGoal, "g", "目标", "active", "", nil),
	)
	refs := []string{"a", "s", "f", "c", "f"}
	events, err := s.SourceEvents(ctx, "t-source", "next", "test", refs)
	if err != nil || len(events) != 4 {
		t.Fatalf("来源去重: %d, %v", len(events), err)
	}
	mustAppend(t, s, "t-source", events...)
	edges, err := s.Edges(ctx, "t-source", EdgeSourcedFrom)
	if err != nil || len(edges) != 4 {
		t.Fatalf("混合来源未完整记账: %v %v", edges, err)
	}
	deps, err := s.Edges(ctx, "t-source", EdgeDependsOn)
	if err != nil || len(deps) != 0 {
		t.Fatal("来源引用改变了调度依赖")
	}
	for _, ref := range []string{"", "next", "missing", "unverified", "g"} {
		if _, err := s.SourceEvents(ctx, "t-source", "next", "test", []string{ref}); err == nil {
			t.Errorf("非法来源未拒绝: %q", ref)
		}
	}
	if _, err := s.Append(ctx, "t-source", ea(EdgeSourcedFrom, "s", "next")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("来源环未拒绝: %v", err)
	}
	if _, err := s.Append(ctx, "t-source", ea(EdgeSourcedFrom, "g", "a")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("来源类型未拒绝: %v", err)
	}
	if _, err := s.Append(ctx, "t-source", ea(EdgeSourcedFrom, "next", "unverified")); !errors.Is(err, ErrEdgeRuleViolation) {
		t.Fatalf("直接入账绕过已确认事实校验: %v", err)
	}
	if _, err := s.SourceEvents(ctx, "another-task", "next", "test", []string{"a"}); err == nil {
		t.Fatal("跨任务来源被接受")
	}
}
