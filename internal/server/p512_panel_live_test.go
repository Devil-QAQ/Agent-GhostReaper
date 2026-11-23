package server

import (
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/secrets"
	"ghostreaper/internal/webui"
)

// Opt-in formal-panel conversation test. The normal router, embedded UI,
// model chain, conversation services, ledger and live hub are real. No task
// scheduler/model executor is attached: this isolates dialogue UX from task
// autonomy, which is verified separately by R9.
func TestThinkingPanelLive(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_PANEL_MODEL_FILE")
	if file == "" {
		t.Skip("private panel model configuration not supplied")
	}
	stop := os.Getenv("GHOSTREAPER_PANEL_STOP_FILE")
	evidence := os.Getenv("GHOSTREAPER_PANEL_EVIDENCE")
	if stop == "" || evidence == "" {
		t.Fatal("isolated stop/evidence paths required")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("private model configuration unavailable")
	}
	var cfg failover.ModelConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private configuration")
	}
	srv, _, _ := newInterjectTestServer(t)
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal("test storage key unavailable")
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal("test encryption unavailable")
	}
	if _, err = srv.Store.EnableAtRest(t.Context(), cipher); err != nil {
		t.Fatal("test storage encryption failed")
	}
	cfg.Name = "对话验收专用"
	cfg.Thinking = "enabled"
	cfg.ContextWindow = 1048576
	if err = failover.SaveModelConfig(t.Context(), srv.Store, cfg, "test"); err != nil {
		t.Fatal("private model save failed")
	}
	if err = failover.SetActive(t.Context(), srv.Store, failover.RolePrimary, cfg.Name, "test"); err != nil {
		t.Fatal("private model activation failed")
	}
	srv.Chain = failover.NewChain(srv.Store, failover.EnvConfig{})
	srv.Shepherd.Provider, srv.Interject.Provider = srv.Chain, srv.Chain
	srv.Shepherd.Model, srv.Interject.Model = cfg.Model, cfg.Model
	const task = "p512-dialogue-only"
	seedIJUnit(t, srv, task)
	if _, err = srv.Store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f-dialogue", NodeType: rings.NodeFact, Title: "人工固定材料共有49项，其中1项重复；未检查其他材料", Status: "confirmed"}}); err != nil {
		t.Fatal(err)
	}
	mux := srv.Handler()
	mux.Handle("/", webui.Handler())
	listener, err := net.Listen("tcp", "127.0.0.1:28314")
	if err != nil {
		t.Fatal("isolated panel port unavailable")
	}
	ts := httptest.NewUnstartedServer(mux)
	ts.Listener = listener
	ts.Start()
	defer ts.Close()
	if err = os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
		t.Fatal(err)
	}
	info, _ := json.Marshal(map[string]any{"url": ts.URL, "task": task, "root": srv.RootDir, "scheduler": false, "model": cfg.Model})
	if err = os.WriteFile(evidence, info, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("formal conversation panel ready at 127.0.0.1:28314; no autonomous task execution attached")
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("panel review timeout")
		case <-tick.C:
			if _, err = os.Stat(stop); err == nil {
				return
			}
		}
	}
}
