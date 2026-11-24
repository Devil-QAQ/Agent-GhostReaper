package server

import (
	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"ghostreaper/internal/webui"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Opt-in acceptance serves the real panel and APIs over an isolated snapshot.
// No manager, planning loop or worker is started by this fixture.
func TestSystemSubjectsPanelSnapshot(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_PANEL_SNAPSHOT")
	if file == "" {
		t.Skip("isolated panel snapshot not supplied")
	}
	store, err := rings.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	mgr, err := manager.New(manager.Config{Store: store, RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(store, mgr, swarm.NewLedgerGate(store))
	if err != nil {
		t.Fatal(err)
	}
	srv.Loops = overmind.NewRegistry()
	srv.RootDir = root
	if os.Getenv("GHOSTREAPER_PANEL_EXAMPLES") == "1" {
		seedSeparatedApprovals(t, srv, "approval-separation-check")
		if err := srv.savePassword(httptest.NewRequest("POST", "/api/auth/setup", nil), "IsolatedPanel2026!"); err != nil {
			t.Fatal(err)
		}
	}
	mux := srv.Handler()
	mux.Handle("/", webui.Handler())
	ln, err := net.Listen("tcp", "127.0.0.1:28333")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: srv.WrapAuth(mux), ReadHeaderTimeout: 5 * time.Second}
	go httpServer.Serve(ln)
	defer httpServer.Close()
	t.Log("isolated formal panel listening on 28333; execution disabled")
	select {
	case <-t.Context().Done():
	case <-time.After(8 * time.Minute):
	}
}
