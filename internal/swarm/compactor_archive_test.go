package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

func TestUnitContextArchiveReadbackAndResume(t *testing.T) {
	for _, window := range []int{0, 1048576} {
		env, store := p439Env(t)
		msgs := append([]llm.Message{llm.UserText("当前意图、授权、可信事实 f-kept 和待完成事项保持原文")},
			p439Turns([]string{"bash", "read", "http_probe", "web_search", "submit_candidate_fact", "port_probe", "web_fetch", "bash", "read", "bash", "read", "bash", "read", "bash"}, 4096)...)
		before := append([]llm.Message(nil), msgs...)
		fresh := func() *unitCompactor {
			return newUnitCompactor(store, env.Executor.Blobs, env.TaskID, env.StepID, env.UnitID, window, 4096, nil)
		}
		out := fresh().Pre(t.Context(), msgs, 56000)
		if !reflect.DeepEqual(msgs, before) || !reflect.DeepEqual(out[0], msgs[0]) {
			t.Fatal("original transcript or task anchor changed")
		}
		archived := 0
		for i, m := range out {
			for j, b := range m.Content {
				if b.Type != llm.BlockToolResult || !strings.HasPrefix(b.Content[0].Text, microPrefix) {
					continue
				}
				archived++
				ref := extractBlobRef(b.Content[0].Text)
				original := msgs[i].Content[j].Content[0].Text
				back, err := env.Executor.Blobs.Read(ref)
				if err != nil || string(back) != original || !utf8.ValidString(b.Content[0].Text) {
					t.Fatalf("archive lost original tool output: %v", err)
				}
				// Read through the actual existing tool, including a value removed
				// from the working excerpt. No network request or re-probe.
				res, err := env.readTool().Call(t.Context(), []byte(`{"path":"`+ref+`","offset":1,"limit":1}`), nil)
				if err != nil || res.IsError || !strings.Contains(res.Flatten(), original[:100]) {
					t.Fatal("original is not reachable through the formal read tool")
				}
			}
		}
		if archived != 8 {
			t.Fatalf("expected eight old process outputs, got %d", archived)
		}
		// Reconstruct a new execution segment from the original raw history.
		resumed := fresh().Pre(t.Context(), msgs, 0)
		if !reflect.DeepEqual(out, resumed) {
			t.Fatal("resume reintroduced full old process bodies")
		}
		referenced, err := store.ReferencedBlobHashes(t.Context())
		if err != nil || len(referenced) != archived {
			t.Fatalf("archive references were not durably pinned: %d %v", len(referenced), err)
		}
		if removed, _, err := env.Executor.Blobs.Sweep(referenced); err != nil || removed != 0 {
			t.Fatal("live context archives were swept as unowned")
		}
	}
}

func TestUnitContextArchiveFailurePreservesBodies(t *testing.T) {
	env, store := p439Env(t)
	msgs := p439Turns([]string{"bash", "read", "http_probe", "web_search", "port_probe", "web_fetch", "bash", "read", "bash", "read", "bash", "read"}, 4096)
	_, count, _, _ := microCompactGR(msgs, microCandidates(msgs), func([]byte) (string, error) { return "", errors.New("archive unavailable") })
	if count != 0 {
		t.Fatal("failed archive shortened history")
	}
	badRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badRoot, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newUnitCompactor(store, NewBlobStore(badRoot), env.TaskID, env.StepID, env.UnitID, 1048576, 4096, nil)
	if out := c.Pre(t.Context(), msgs, 56000); !reflect.DeepEqual(out, msgs) || len(p439Blackbox(t, store, env.TaskID)) != 0 {
		t.Fatal("disk failure discarded bodies or recorded successful archival")
	}
	c = newUnitCompactor(store, env.Executor.Blobs, env.TaskID, env.StepID, env.UnitID, 1048576, 4096, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if out := c.Pre(ctx, msgs, 56000); !reflect.DeepEqual(out, msgs) {
		t.Fatal("missing durable archive ownership still shortened the request")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if out := c.Pre(t.Context(), msgs, 56000); !reflect.DeepEqual(out, msgs) {
		t.Fatal("failed durable archive ownership still shortened the request")
	}
}

func TestUnitContextMicroProjectionPreservesTextAndFreshResults(t *testing.T) {
	env, _ := p439Env(t)
	msgs := p439Turns([]string{"bash", "read", "read", "submit_claim", "read", "read", "read", "read", "read"}, 3000)
	// A middle proof value must survive verbatim in the archive; preview UTF-8
	// and all receipt/result metadata must remain valid.
	msgs[1].Content[0].Content = []llm.ContentBlock{llm.TextBlock(strings.Repeat("中文", 400) + "唯一中间值 92817" + strings.Repeat("末尾", 400))}
	msgs[1].Content[0].IsError = true
	out, _, _, _ := microCompactGR(msgs, microCandidates(msgs), env.Executor.Blobs.Write)
	b := out[1].Content[0]
	if !b.IsError || !utf8.ValidString(b.Content[0].Text) {
		t.Fatal("result metadata or encoding changed")
	}
	raw, err := env.Executor.Blobs.Read(extractBlobRef(b.Content[0].Text))
	if err != nil || !strings.Contains(string(raw), "唯一中间值 92817") {
		t.Fatal("middle evidence discarded")
	}
	res, err := env.readTool().Call(t.Context(), []byte(`{"path":"`+extractBlobRef(b.Content[0].Text)+`","offset":1,"column":795,"limit":1}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "唯一中间值 92817") || !utf8.ValidString(res.Flatten()) {
		t.Fatal("archived middle content cannot be read without repeating the original probe")
	}
	for i := 6; i < len(msgs); i++ {
		if !reflect.DeepEqual(msgs[i], out[i]) {
			t.Fatal("receipt or recent result changed")
		}
	}
}

func TestUnitContextActualRequestsAfterRepeatedResume(t *testing.T) {
	env, store := p439Env(t)
	names := make([]string, 20)
	for i := range names {
		names[i] = "read"
	}
	raw := append([]llm.Message{llm.UserText("当前意图：核对固定材料；可信事实 f-stable；未完成项保持原样")}, p439Turns(names, 4096)...)
	ts := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	w := ts.NewWriter("context-resume", "")
	for _, msg := range raw {
		w.RecordMessage(msg, llm.Usage{})
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(ts.MainPath("context-resume"))
	if err != nil {
		t.Fatal(err)
	}
	m := newScriptedModel(func(*recordedRequest) scriptedReply {
		return scriptedReply{Text: "继续依据已有材料，不重新探测"}
	})
	defer m.Close()
	provider, err := m.provider()
	if err != nil {
		t.Fatal(err)
	}
	for segment := 0; segment < 3; segment++ {
		sess := agentcore.NewSession(agentcore.Options{
			Provider: provider, SessionID: "context-resume", Transcript: ts, SystemPrompt: []string{"当前授权不变；仅处理固定材料"},
			Compactor: newUnitCompactor(store, env.Executor.Blobs, env.TaskID, env.StepID, env.UnitID, 1048576, 4096, nil),
			Tools:     []tool.CoreTool{env.readTool()}, MaxTurns: 2, DisableBackgroundTasks: true,
		})
		if err := sess.Resume("context-resume"); err != nil {
			t.Fatal(err)
		}
		for ev, err := range sess.Prompt(t.Context(), "按现有记录继续") {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Kind == harness.KindResult && ev.Terminal != nil && ev.Terminal.Err != nil {
				t.Fatal(ev.Terminal.Err)
			}
		}
		sess.Close()
		rr := m.requests()[segment]
		old, recent := 0, 0
		for _, msg := range rr.Messages {
			if msg.Role != "tool" {
				continue
			}
			if strings.HasPrefix(msg.Content, microPrefix) {
				old++
			} else if len(msg.Content) >= 4096 {
				recent++
			}
		}
		if old != 15 || recent != 5 || !strings.Contains(rr.System, "当前授权不变") {
			t.Fatalf("actual outgoing request reintroduced history or lost anchors: old=%d recent=%d", old, recent)
		}
		if segment == 0 {
			before, _ := json.Marshal(raw)
			after, _ := json.Marshal(rr.Messages)
			t.Logf("fixed replay original messages=%d bytes, outgoing messages=%d bytes (protocols differ; byte sizes only)", len(before), len(after))
		}
	}
	after, err := os.ReadFile(ts.MainPath("context-resume"))
	if err != nil || !strings.HasPrefix(string(after), string(original)) {
		t.Fatal("raw transcript was rewritten to shrink context")
	}
}

func TestReadArchiveLongLineCanBeContinued(t *testing.T) {
	env, _ := p439Env(t)
	ref, err := env.Executor.Blobs.Write([]byte(strings.Repeat("域", 2500) + "middle-proof-92817" + strings.Repeat("尾", 2500)))
	if err != nil {
		t.Fatal(err)
	}
	first := callTool(t, env.readTool(), `{"path":"`+ref+`","limit":1}`)
	if first.IsError || !strings.Contains(first.Flatten(), "column=2001") || strings.Contains(first.Flatten(), "middle-proof-92817") {
		t.Fatal("long first line has no truthful continuation")
	}
	next := callTool(t, env.readTool(), `{"path":"`+ref+`","offset":1,"column":2001,"limit":1}`)
	if next.IsError || !utf8.ValidString(next.Flatten()) || !strings.Contains(next.Flatten(), "middle-proof-92817") {
		t.Fatal("long-line middle proof is unreachable")
	}
	bad := callTool(t, env.readTool(), `{"path":"`+ref+`","column":99999}`)
	if !bad.IsError || !strings.Contains(bad.Flatten(), "超出范围") {
		t.Fatal("column outside actual content accepted")
	}
}
