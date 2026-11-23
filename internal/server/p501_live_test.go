package server

// P5-01③ live 推送事件映射同步(影响面 1-28/5-11):新交卷事件
// (testclaim.record submit)沿用既有 EventTestClaimRecord kind,经
// onCommit 映射到 claims:<任务> 主题——发现页实时不断链;载荷协议
// 本身不受影响(dirty 零载荷)。

import (
	"encoding/json"
	"testing"

	"ghostreaper/internal/rings"
)

func TestP501LiveSubmitClaimMapsToClaimsTopic(t *testing.T) {
	h := newLiveHub(nil)

	// 交换单事件(submit):映射 claims 主题。
	submit := rings.Event{Kind: rings.EventTestClaimRecord, Payload: mustMarshal(t, rings.TestClaimRecord{
		ClaimID: "tc-x", StepID: "s1", UnitID: "u1", Type: rings.TestClaimRecSubmit,
		Summary: "声明", Severity: "high", EvidenceRefs: []string{"ms-a.1"},
		ProvingMarker: "m", CountercheckNote: "自查",
	})}
	h.onCommit("tk1", []rings.Event{submit})
	if !h.dirtyNow(liveTopicClaims + "tk1") {
		t.Fatalf("交卷事件应标脏 claims 主题(发现页实时)")
	}

	// 声明节点创建(交卷同批):同样映射 claims。
	h.mu.Lock()
	h.dirty = map[string]bool{}
	h.mu.Unlock()
	h.onCommit("tk2", []rings.Event{{Kind: rings.EventNodeAdded, Payload: mustMarshal(t,
		rings.NodeAdded{NodeID: "tc-y", NodeType: rings.NodeTestClaim, Status: rings.TestClaimCandidate})}})
	if !h.dirtyNow(liveTopicClaims + "tk2") {
		t.Fatalf("声明节点创建应标脏 claims 主题")
	}

	// 无关任务的事件不误伤他任务主题。
	h.mu.Lock()
	h.dirty = map[string]bool{}
	h.mu.Unlock()
	h.onCommit("tk3", []rings.Event{submit})
	if h.dirtyNow(liveTopicClaims + "tk1") {
		t.Fatalf("他任务事件不得标脏 tk1 主题")
	}
}

// dirtyNow 读主题当前标脏位(测试辅助;并发面与生产读同锁)。
func (h *liveHub) dirtyNow(topic string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dirty[topic]
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return b
}
