package overmind

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

func TestClaimRepeatedConcurrentNotificationsHaveOneVerdict(t *testing.T) {
	for _, marker := range []string{"fixture-marker", "wrong-marker"} {
		t.Run(marker, func(t *testing.T) {
			const task, claim, url = "claim-concurrent", "tc-one", "http://t.example:8080/proof"
			store, ex := p502Base(t, task, claim, url, "fixture-marker")
			p502Submit(t, store, task, claim, "s1", url, marker)
			loop := &Loop{TaskID: task, Store: store, Swarm: ex}
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					out, err := loop.CheckClaimOnSubmit(t.Context(), task, claim)
					if err != nil || out == nil {
						t.Errorf("repeated check: %+v %v", out, err)
					}
				})
			}
			wg.Wait()
			events, _ := store.Events(t.Context(), task, 0, 0)
			verdicts, packages, retakes := 0, 0, 0
			for _, event := range events {
				if event.Kind == rings.EventTestClaimRecord {
					var r rings.TestClaimRecord
					_ = json.Unmarshal(event.Payload, &r)
					if r.ClaimID != claim {
						continue
					}
					if r.Type == rings.TestClaimRecVerdict {
						verdicts++
					}
					if r.Type == rings.TestClaimRecRepro {
						packages++
					}
				}
				if event.Kind == rings.EventNodeAdded {
					var n rings.NodeAdded
					_ = json.Unmarshal(event.Payload, &n)
					if n.Detail["kind"] == retakeStepKind {
						retakes++
					}
				}
			}
			if verdicts != 1 {
				t.Fatalf("same submission had %d verdicts", verdicts)
			}
			if marker == "fixture-marker" && (packages != 1 || retakes != 0) {
				t.Fatalf("packages=%d retakes=%d", packages, retakes)
			}
			if marker != "fixture-marker" && (packages != 0 || retakes != 1) {
				t.Fatalf("packages=%d retakes=%d", packages, retakes)
			}
		})
	}
}

func TestClaimMachineConfirmationDoesNotWaitForDisplayModel(t *testing.T) {
	const task, claim, url = "claim-slow-display", "tc-one", "http://t.example:8080/proof"
	store, ex := p502Base(t, task, claim, url, "fixture-marker")
	p502Submit(t, store, task, claim, "s1", url, "fixture-marker")
	model := newP442HangModel()
	defer model.Close()
	release := sync.OnceFunc(model.release)
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: model.provider(t)}
	defer loop.polishWG.Wait()
	// Release precedes WaitPolish even if an assertion fails.
	defer release()
	start := time.Now()
	out, err := loop.CheckClaimOnSubmit(t.Context(), task, claim)
	if err != nil || out.Status != rings.TestClaimConfirmed {
		t.Fatalf("machine check: %+v %v", out, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("confirmation waited for display: %s", elapsed)
	}
	waitFor(t, time.Second, "后台显示模型已收到请求但仍未返回", func() bool { return model.requests() > 0 })
	n, _ := store.Node(context.Background(), task, claim)
	row, _, _ := store.ClaimCaseOf(t.Context(), task, claim)
	if n.Status != rings.TestClaimConfirmed || row.Repro == nil || len(row.Repro.Steps) == 0 {
		t.Fatal("held model prevented atomic deliverable")
	}
}
