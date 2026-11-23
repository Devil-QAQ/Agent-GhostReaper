package shepherd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func TestShepherdResumeCompactionUsageRecordedSeparately(t *testing.T) {
	store := openStore(t)
	const task = "shepherd-compact-usage"
	seedTask(t, store, task)
	root := t.TempDir()
	ts := transcript.NewStore(filepath.Join(root, task, "transcripts"))
	w := ts.NewWriter(SessionID(task), "")
	for range 6 {
		w.RecordMessage(llm.UserText(strings.Repeat("observed history ", 1000)), llm.Usage{})
		w.RecordMessage(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("历史回复")}}, llm.Usage{})
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	summaryCalls := 0
	m := newScriptedModel(func(rr *recordedReq) scriptedRep {
		if strings.Contains(rr.System, "summarizing conversations") {
			summaryCalls++
			return scriptedRep{Text: "<summary>历史观察摘要</summary>"}
		}
		return scriptedRep{Text: "已读取会话"}
	})
	defer m.Close()
	prov, err := m.provider()
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store, RootDir: root, Provider: prov, Model: "scripted-shepherd-model", MaxTokens: 1000,
		WindowOf: func() int { return 10000 }}
	if _, err := s.Enqueue(context.Background(), task, "读取历史"); err != nil {
		t.Fatal(err)
	}
	waitView(t, s, task, func(v *View) bool { return len(v.Messages) == 1 && v.Messages[0].Reply != "" && !v.Busy })
	rows, err := store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 2 || summaryCalls != 1 {
		t.Fatalf("summary missing or main double counted: rows=%+v summaries=%d err=%v", rows, summaryCalls, err)
	}
	if rows[0].Purpose != "compaction" || rows[1].Purpose != "" || rows[0].InputTokens != 120 || rows[1].InputTokens != 120 || rows[0].OutputTokens != 30 || rows[1].OutputTokens != 30 || rows[1].Turn != 1 || rows[0].TranscriptID != SessionID(task) {
		t.Fatalf("wrong main/summary usage attribution: %+v", rows)
	}
}
