package rings

import (
	"path/filepath"
	"testing"
)

func TestAuxiliaryUsageMigrationPreservesLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTokens(t.Context(), TokenUsage{TaskID: "legacy", Agent: "overmind", InputTokens: 123, Turn: 2}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE token_usage DROP COLUMN purpose`,
		`ALTER TABLE token_usage DROP COLUMN usage_missing`,
		`DROP INDEX idx_events_execution`,
		`PRAGMA user_version = 16`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.TokenUsageRows(t.Context(), "legacy")
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 123 || rows[0].Turn != 2 || rows[0].Purpose != "" || rows[0].UsageMissing {
		t.Fatalf("legacy usage altered: %+v err=%v", rows, err)
	}
	if err := s.RecordTokens(t.Context(), TokenUsage{TaskID: "legacy", Agent: "overmind", Purpose: "triage", UsageMissing: true}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.AllTokenUsageRows(t.Context())
	if err != nil || len(rows) != 2 || rows[1].Purpose != "triage" || !rows[1].UsageMissing {
		t.Fatalf("new accounting columns not persisted: %+v err=%v", rows, err)
	}
}
