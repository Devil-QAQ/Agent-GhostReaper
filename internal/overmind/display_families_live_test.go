package overmind

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"github.com/Autumn-27/norma/llm"
)

// Identical inert, synthetic display material for before/after formatting.
// No requests or task tools are run, and these are not actual findings.
func TestDisplayLiveOtherFamilies(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_DISPLAY_MODEL_FILE")
	if file == "" {
		t.Skip("private display model configuration not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("private configuration unavailable")
	}
	var cfg struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Format  string `json:"format"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private configuration")
	}
	provider, err := llm.NewProvider(llm.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, Format: llm.Format(cfg.Format)})
	if err != nil {
		t.Fatal("provider initialization failed")
	}
	type result struct {
		Round   int             `json:"round"`
		Family  string          `json:"family"`
		Calls   int             `json:"calls"`
		Input   int             `json:"input_tokens"`
		Output  int             `json:"output_tokens"`
		Cache   int             `json:"cache_read_tokens"`
		Unknown int             `json:"unknown_usage"`
		Elapsed int64           `json:"elapsed_ms"`
		Text    json.RawMessage `json:"text"`
	}
	var report []result
	store := openStore(t)
	for round := 1; round <= 3; round++ {
		for _, family := range []string{"intent", "assessment", "summary", "repro", "narrative", "retest"} {
			if filter := os.Getenv("GHOSTREAPER_DISPLAY_FAMILIES"); filter != "" && !strings.Contains(","+filter+",", ","+family+",") {
				continue
			}
			task := fmt.Sprintf("display-%s-%d", family, round)
			l := &Loop{TaskID: task, Store: store, Provider: provider, Model: cfg.Model, RootDir: t.TempDir()}
			_, err := store.Append(t.Context(), task,
				rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created", Title: "人工合成文案样本"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Title: "只核对指定49项清单中的重复项，复用已取得清单，不检查其他材料", Status: "done"}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Title: "人工合成固定清单共49项，发现1项重复；未检查其他清单", Status: "confirmed", Detail: map[string]any{"unit_id": "u1"}}},
				rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-display", NodeType: rings.NodeTestClaim, Title: "人工合成固定清单含重复项", Status: "confirmed", Detail: map[string]any{"step_id": "s1", "unit_id": "u1", "impact": "仅本次49项清单中有1项重复；未检查其他清单", "evidence_head": "人工固定样例校验结果：49项中1项重复"}}},
				rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f1"}},
				rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-display", ToNode: "s1"}},
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			start := time.Now()
			var text any
			switch family {
			case "intent":
				l.polishStepTitles(ctx, []stepTitleMaterial{{StepID: "s1", Intent: "只核对指定49项清单中的重复项，复用已取得清单，不检查其他材料"}})
				evs, _ := store.EventsByKinds(ctx, task, rings.EventStepTitle)
				if len(evs) > 0 {
					text = json.RawMessage(evs[len(evs)-1].Payload)
				}
			case "assessment":
				rec := rings.OvermindRecord{Type: rings.OvermindRecAssessment, QAccess: "无新入口", QPrivilege: "无新权限", QCombination: "没有两条可组合线索", QUnlock: "已有49项清单可直接核对，无需重复获取", QOverturn: "确认1项重复，仅覆盖本次清单，不推翻其他结论", Note: "保留当前核对方向；不调查其他材料，也不扩展目标"}
				revs, e := store.Append(ctx, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: ActorOvermind, Payload: rec})
				if e != nil {
					t.Fatal(e)
				}
				l.polishAssessment(ctx, revs[0], rec)
				evs, _ := store.EventsByKinds(ctx, task, rings.EventOvermindPolish)
				if len(evs) > 0 {
					text = json.RawMessage(evs[len(evs)-1].Payload)
				}
			case "summary":
				text = l.genClaimSummaryText(ctx, "tc-display")
			case "repro":
				l.Swarm = swarm.NewExecutor(store, nil, filepath.Join(l.RootDir, "tasks"), swarm.Budget{})
				ex := mirror.Exchange{ID: "synthetic-display-only", TaskID: task, StepID: "s1", UnitID: "u1", Request: &mirror.Request{Method: "GET", URL: "http://fixture.example.test/materials"}, Response: &mirror.Response{Status: 200, Body: []byte(`{"material_count":49,"duplicate_count":1}`)}}
				doc, _ := json.Marshal(ex)
				ref, e := l.Swarm.Blobs.Write(doc)
				if e != nil {
					t.Fatal(e)
				}
				rec := &rings.TestClaimRecord{ClaimID: "tc-display", Type: rings.TestClaimRecRepro, Steps: []rings.ReproStep{{Seq: 1, Method: "GET", URL: "http://fixture.example.test/materials", Status: 200, BlobRef: ref, Desc: "人工合成样例：公开清单返回49项，其中1项重复；本次不访问该地址"}}}
				l.pickReproSteps(ctx, "tc-display", rec, ReproPoolDiag{}, []int{1})
				text = rec
				if len(rec.Steps) != 1 || rec.Steps[0].Method != "GET" || rec.Steps[0].URL != "http://fixture.example.test/materials" || rec.Steps[0].Status != 200 {
					t.Error("repro changed actual protocol facts or dropped the seed")
				} else {
					proof := rec.Steps[0].Desc + rec.Steps[0].Purpose
					if !regexp.MustCompile(`duplicate_count[^0-9]*1\b`).MatchString(proof) {
						t.Error("repro omitted the actual duplicate-count criterion")
					}
					saved, e := l.Swarm.Blobs.Read(rec.Steps[0].BlobRef)
					if e != nil || !bytes.Equal(saved, doc) {
						t.Error("repro lost the complete original observation")
					}
				}
			case "narrative":
				rec := &rings.TestClaimRecord{ClaimID: "tc-display", Type: rings.TestClaimRecRepro}
				l.genClaimNarrative(ctx, "tc-display", nil, rec)
				text = rec
			case "retest":
				text = l.PolishRetestNote(ctx, "tc-display", 1, "unchanged", "人工固定样例：重新核对同一份49项清单，仍有1项重复；未检查其他清单，不代表其他材料存在同样问题")
			}
			cancel()
			row := result{Round: round, Family: family, Elapsed: time.Since(start).Milliseconds()}
			row.Text, _ = json.Marshal(text)
			usage, e := store.TokenUsageRows(t.Context(), task)
			if e != nil {
				t.Fatal(e)
			}
			row.Calls = len(usage)
			for _, u := range usage {
				row.Input += u.InputTokens
				row.Output += u.OutputTokens
				row.Cache += u.CacheReadTokens
				if u.UsageMissing {
					row.Unknown++
				}
			}
			report = append(report, row)
			if out := os.Getenv("GHOSTREAPER_DISPLAY_EVIDENCE"); out != "" {
				if os.MkdirAll(filepath.Dir(out), 0700) != nil {
					t.Fatal("evidence directory failed")
				}
				b, _ := json.MarshalIndent(report, "", "  ")
				if os.WriteFile(out, b, 0600) != nil {
					t.Fatal("evidence write failed")
				}
			}
			if text == nil || string(row.Text) == "null" || row.Calls == 0 {
				t.Errorf("%s did not generate actual display content", family)
			}
			if family == "repro" && (strings.Contains(string(row.Text), "未授权") || strings.Contains(string(row.Text), "越权") || strings.Contains(string(row.Text), "权限绕过")) {
				t.Error("repro invented an access-control failure absent from the supplied observation")
			}
			if family != "repro" && !strings.Contains(string(row.Text), "49") {
				t.Errorf("%s lost exact material count", family)
			}
			t.Logf("round=%d family=%s calls=%d input=%d output=%d cache=%d unknown=%d elapsed_ms=%d", round, family, row.Calls, row.Input, row.Output, row.Cache, row.Unknown, row.Elapsed)
		}
	}
}
