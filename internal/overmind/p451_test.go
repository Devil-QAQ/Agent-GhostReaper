package overmind

import (
	"testing"

	"ghostreaper/internal/rings"
)

// TestP451StepLaunchLessScopeHeadFirst 完成标准⑤:范围批准回队步骤排
// 队首——先于一切新排队者(含 high 优先级与反证)。
func TestP451StepLaunchLessScopeHeadFirst(t *testing.T) {
	plain := rings.NodeFull{NodeView: rings.NodeView{ID: "s-new"}, Detail: map[string]any{"priority": "high"}}
	head := rings.NodeFull{NodeView: rings.NodeView{ID: "s-woken"}, Detail: map[string]any{"scope_head": true}}
	if !StepLaunchLess(head, plain) {
		t.Fatal("范围批准回队步骤应排在 high 优先级新步骤之前(队首)")
	}
	falsify := rings.NodeFull{NodeView: rings.NodeView{ID: "s-fal"}, Detail: map[string]any{"kind": "falsify", "priority": "high"}}
	if !StepLaunchLess(head, falsify) {
		t.Fatal("范围批准回队步骤应排在反证之前(队首)")
	}
	// 同为范围批准回队:按创建序稳定排序。
	a := rings.NodeFull{NodeView: rings.NodeView{ID: "s-a", CreatedRev: 3}, Detail: map[string]any{"scope_head": true}}
	b := rings.NodeFull{NodeView: rings.NodeView{ID: "s-b", CreatedRev: 5}, Detail: map[string]any{"scope_head": true}}
	if !StepLaunchLess(a, b) {
		t.Fatal("同为队首标记应按创建序")
	}
}
