package rings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ClaimTextSourceRev 取得显示稿依据的业务版本，显示事件自身不抬高依据。
func (s *Store) ClaimTextSourceRev(ctx context.Context, task, id string) (int64, error) {
	n, err := s.Node(ctx, task, id)
	if err != nil {
		return 0, err
	}
	if n.Type != NodeTestClaim {
		return 0, fmt.Errorf("rings: 显示稿对象不是判断")
	}
	source := n.UpdatedRev
	row, ok, err := s.ClaimCaseOf(ctx, task, id)
	if err != nil {
		return 0, err
	}
	if ok {
		source = max(source, row.VerdictRev)
		if row.Repro != nil {
			source = max(source, row.Repro.Rev)
		}
	}
	return source, nil
}

// The accepted display event is the durable completion receipt. Use the
// existing event-kind index; no second job table or cached business state.
func (s *Store) HasClaimText(ctx context.Context, task, id string, source int64) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events
	 WHERE task_id=? AND kind=? AND json_extract(payload,'$.claim_id')=?
	 AND json_extract(payload,'$.source_rev')=?)`, task, EventClaimText, id, source).Scan(&found)
	return found, err
}

func applyClaimText(tx *sql.Tx, task string, rev int64, p ClaimTextPolish) error {
	if p.ClaimID == "" || p.SourceRev <= 0 || (p.Repro == nil && (strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Statement) == "")) {
		return fmt.Errorf("rings: 漏洞显示稿缺对象、依据版本、名称或说明")
	}
	var typ string
	var source int64
	if err := tx.QueryRow(`SELECT node_type,updated_rev FROM nodes WHERE task_id=? AND node_id=?`, task, p.ClaimID).Scan(&typ, &source); err != nil {
		return fmt.Errorf("rings: 显示稿对象不存在: %w", err)
	}
	if NodeType(typ) != NodeTestClaim {
		return fmt.Errorf("rings: 显示稿对象不是判断")
	}
	row, err := loadClaimCase(tx, task, p.ClaimID)
	if err != nil {
		return err
	}
	source = max(source, row.verdictRev)
	if row.repro != nil {
		source = max(source, row.repro.Rev)
	}
	if source != p.SourceRev {
		return fmt.Errorf("rings: 显示稿依据已变化")
	}
	if p.Repro != nil {
		if row.repro == nil || len(p.Repro.Steps) == 0 {
			return fmt.Errorf("rings: 复现显示稿缺少已封存的证明")
		}
		// The projection initially shows only mandatory proof. Auxiliary
		// selection is checked against the sealed original pool, not that
		// deliberately smaller visible projection.
		var raw []byte
		if err := tx.QueryRow(`SELECT payload FROM events WHERE task_id=? AND revision=?`, task, row.repro.Rev).Scan(&raw); err != nil {
			return err
		}
		var sealed TestClaimRecord
		if err := json.Unmarshal(raw, &sealed); err != nil {
			return err
		}
		base := map[int]ReproStep{}
		for _, st := range sealed.Steps {
			base[st.Seq] = st
		}
		seen := map[int]bool{}
		for _, st := range p.Repro.Steps {
			old, ok := base[st.Seq]
			if !ok || seen[st.Seq] || !reflect.DeepEqual(proofFields(old), proofFields(st)) {
				return fmt.Errorf("rings: 复现显示稿改变了原始证明")
			}
			seen[st.Seq] = true
		}
		for _, seq := range sealed.ProofSeqs {
			if !seen[seq] {
				return fmt.Errorf("rings: 复现显示稿遗漏了必选证明")
			}
		}
		row.repro.Steps = p.Repro.Steps
		row.repro.Narrative = p.Repro.Narrative
		row.repro.NarrativeUnpolished = p.Repro.NarrativeUnpolished
		row.repro.StepsIncomplete = p.Repro.StepsIncomplete
		row.reproSteps = len(p.Repro.Steps)
		row.changedRepro, row.changedDigest = true, true
	}
	// The original package event, source bytes and verdict remain unchanged.
	var facts []FactStatement
	if row.summary != nil {
		facts = row.summary.Facts
	}
	if p.Facts != nil {
		facts = p.Facts
	}
	if strings.TrimSpace(p.Title) != "" && strings.TrimSpace(p.Statement) != "" {
		row.summary = &SummaryDigest{Rev: rev, Title: p.Title, Statement: p.Statement, Impact: p.Impact, Facts: facts}
		row.changedSummary = true
	}
	if err := row.includeSourceFacts(tx, task); err != nil {
		return err
	}
	return row.save(tx, task)
}

func proofFields(st ReproStep) ReproStep {
	st.Op, st.Desc, st.Short, st.Purpose = "", "", "", ""
	st.Unpolished = false
	return st
}
