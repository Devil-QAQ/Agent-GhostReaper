package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func TestDisplayGenerationUsesOneAnswerOrOneConcreteRepair(t *testing.T) {
	for _, family := range []string{"steps", "narrative"} {
		for _, mode := range []string{"clean", "repair", "still-dirty"} {
			t.Run(family+"/"+mode, func(t *testing.T) {
				store := openStore(t)
				l, model := p424Loop(t, store, "display-"+family+mode, func(rr *recordedRequest) scriptedReply {
					repair := assistantCarries(*rr, "tc-8f2a1b3c")
					clean := mode == "clean" || (mode == "repair" && repair)
					text := p424DirtySteps
					if clean {
						text = p424CleanSteps
					}
					if family == "narrative" {
						text = `{"narrative":"` + p424DirtyNarrative + `"}`
						if clean {
							text = `{"narrative":"` + p424CleanNarrative + `"}`
						}
					}
					return scriptedReply{Text: text, Finish: "stop"}
				})
				defer model.Close()
				rec := mkP424Rec()
				if family == "steps" {
					l.pickReproSteps(context.Background(), "tc-x", rec, ReproPoolDiag{}, nil)
				} else {
					seedNarrativeClaim(t, l, l.TaskID)
					rec = &rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro}
					l.genClaimNarrative(context.Background(), "tc-x", nil, rec)
				}
				requests := model.requests()
				want := 1
				if mode != "clean" {
					want = 2
				}
				if len(requests) != want {
					t.Fatalf("%s: requests=%d want=%d", mode, len(requests), want)
				}
				if !strings.Contains(requests[0].System, proofreadHumanStandard) {
					t.Fatal("first generation missed the reading requirements")
				}
				if want == 2 && (!assistantCarries(requests[1], "tc-8f2a1b3c") || !strings.Contains(requests[1].LastUser, "内部编号")) {
					t.Fatal("repair did not retain the actual draft and concrete failure")
				}
				flag := rec.NarrativeUnpolished
				if family == "steps" {
					if len(rec.Steps) != 2 || rec.Steps[0].URL != "http://k3.local/admin" || rec.Steps[1].Method != "POST" {
						t.Fatal("text generation changed protocol facts")
					}
					flag = rec.Steps[0].Unpolished
				} else if mode != "still-dirty" && rec.Narrative != p424CleanNarrative {
					t.Fatal("clean narrative was lost")
				}
				if flag != (mode == "still-dirty") {
					t.Fatalf("honest fallback flag=%t mode=%s", flag, mode)
				}
			})
		}
	}
}
