package rings

import (
	"database/sql"
	"fmt"
	"strings"
)

func NormalizeClaimText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Submission admission uses the same writer transaction as execution
// ownership. Parallel tools cannot both pass a read-before-write retake limit
// or create duplicate claims against the same asset.
func validateSwarmSubmissions(tx *sql.Tx, task string, events []Event) error {
	created := map[string]NodeAdded{}
	anchors := map[string]string{}
	for _, event := range events {
		switch p := event.Payload.(type) {
		case NodeAdded:
			if p.NodeType == NodeTestClaim {
				created[p.NodeID] = p
			}
		case EdgeAdded:
			if p.EdgeType == EdgeAbout {
				anchors[p.FromNode] = p.ToNode
			}
		}
	}
	seen := map[string]bool{}
	for _, event := range events {
		sub, ok := event.Payload.(TestClaimRecord)
		if !ok || sub.Type != TestClaimRecSubmit {
			continue
		}
		if seen[sub.ClaimID] {
			return fmt.Errorf("同一批写入不能重复提交同一份交卷")
		}
		seen[sub.ClaimID] = true
		if node, fresh := created[sub.ClaimID]; fresh {
			rows, err := tx.Query(`SELECT n.node_id,n.title FROM nodes n JOIN edges e ON e.task_id=n.task_id AND e.from_node=n.node_id WHERE n.task_id=? AND n.node_type='testclaim' AND e.edge_type='about' AND e.to_node=?`, task, anchors[sub.ClaimID])
			if err != nil {
				return err
			}
			var duplicate string
			for rows.Next() {
				var id, title string
				if err := rows.Scan(&id, &title); err != nil {
					rows.Close()
					return err
				}
				if NormalizeClaimText(title) == NormalizeClaimText(node.Title) {
					duplicate = id
					break
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if duplicate != "" {
				return fmt.Errorf("交卷重复：同一资产上的已有漏洞 %s", duplicate)
			}
			continue
		}
		var status string
		if err := tx.QueryRow(`SELECT status FROM nodes WHERE task_id=? AND node_id=? AND node_type='testclaim'`, task, sub.ClaimID).Scan(&status); err != nil {
			return err
		}
		if status != TestClaimCandidate {
			return fmt.Errorf("交卷 %s 已有核验结果，不能重交", sub.ClaimID)
		}
		var reopen, latestSubmit int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM events WHERE task_id=? AND kind='testclaim.record' AND json_extract(payload,'$.claim_id')=? AND json_extract(payload,'$.type')='reopen'`, task, sub.ClaimID).Scan(&reopen); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(MAX(revision),0) FROM events WHERE task_id=? AND kind='testclaim.record' AND json_extract(payload,'$.claim_id')=? AND json_extract(payload,'$.type')='submit' AND revision>?`, task, sub.ClaimID, reopen).Scan(&count, &latestSubmit); err != nil {
			return err
		}
		if count >= 2 {
			return fmt.Errorf("交卷 %s 的补做重交次数已用完", sub.ClaimID)
		}
		if count == 0 {
			if reopen == 0 {
				return fmt.Errorf("交卷 %s 尚无首次提交，不能按补做重交", sub.ClaimID)
			}
			continue // First real submission of a reopened verification.
		}
		var decision string
		if err := tx.QueryRow(`SELECT COALESCE(json_extract(payload,'$.decision'),'') FROM events WHERE task_id=? AND kind='testclaim.record' AND json_extract(payload,'$.claim_id')=? AND json_extract(payload,'$.type')='verdict' AND revision>? ORDER BY revision DESC LIMIT 1`, task, sub.ClaimID, latestSubmit).Scan(&decision); err != nil || decision != VerdictRetake {
			return fmt.Errorf("交卷 %s 尚未返回补做反馈，请等待核验结果", sub.ClaimID)
		}
	}
	return nil
}
