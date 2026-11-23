package rings

import (
	"context"
	"fmt"
	"strings"
)

// SourceEvents records explicit provenance separately from execution dependencies.
// Sources must exist in this task. The caller appends these events atomically
// with the new intent/claim; existing ledger data is never inferred or rewritten.
func (s *Store) SourceEvents(ctx context.Context, taskID, nodeID, actor string, refs []string) ([]Event, error) {
	var events []Event
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == nodeID {
			return nil, fmt.Errorf("来源编号为空或引用了自身")
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		n, err := s.Node(ctx, taskID, ref)
		if err != nil {
			return nil, fmt.Errorf("来源节点 %s 不存在于当前任务: %w", ref, err)
		}
		if err := validSource(n.Type, n.Status); err != nil {
			return nil, fmt.Errorf("来源 %s: %w", ref, err)
		}
		events = append(events, Event{Kind: EventEdgeAdded, Actor: actor, Payload: EdgeAdded{EdgeType: EdgeSourcedFrom, FromNode: nodeID, ToNode: ref}})
	}
	return events, nil
}

func validSource(kind NodeType, status string) error {
	switch kind {
	case NodeAsset:
		if status == AssetStatusRetired {
			return fmt.Errorf("资产已退役")
		}
	case NodeStep:
	case NodeFact:
		if status != "confirmed" {
			return fmt.Errorf("事实尚未确认")
		}
	case NodeTestClaim:
		if status != TestClaimConfirmed && status != TestClaimFixed {
			return fmt.Errorf("漏洞尚未确认")
		}
	default:
		return fmt.Errorf("该类节点不能作为来源")
	}
	return nil
}
