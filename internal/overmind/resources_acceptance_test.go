package overmind

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// Continue the opt-in real worker acceptance through the production harvest
// entry; no candidate is fabricated and no resource text is promoted as proof.
func TestResourceLiveWorkflowHarvest(t *testing.T) {
	dir := os.Getenv("GHOSTREAPER_RESOURCE_HARVEST_EVIDENCE")
	if dir == "" {
		t.Skip("real worker acceptance evidence not supplied")
	}
	s, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	facts, err := s.NodesByType(t.Context(), "e-workflow", rings.NodeFact, "")
	if err != nil || len(facts) == 0 {
		t.Fatal("real worker observation missing", err)
	}
	for _, f := range facts {
		if !strings.Contains(fmt.Sprint(f.Detail["evidence"]), "E_ACCEPTED") {
			t.Fatal("missing actual observed response")
		}
	}
	if _, err = Harvest(t.Context(), s, "e-workflow"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.NodesByType(t.Context(), "e-workflow", rings.NodeFact, "confirmed")
	if err != nil || len(confirmed) != len(facts) {
		t.Fatal("normal observation not harvested", err)
	}
	claims, err := s.NodesByType(t.Context(), "e-workflow", rings.NodeTestClaim, "")
	if err != nil || len(claims) != 0 {
		t.Fatal("normal health observation became vulnerability", err)
	}
	t.Logf("production harvest confirmed %d normal observations; no vulnerability", len(confirmed))
}
