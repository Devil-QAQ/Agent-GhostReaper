package overmind

import (
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"os"
	"path/filepath"
	"testing"
)

func TestInventorySnapshotPassiveRecovery(t *testing.T) {
	source, task := os.Getenv("GHOSTREAPER_INVENTORY_SNAPSHOT"), os.Getenv("GHOSTREAPER_SNAPSHOT_TASK")
	if source == "" || task == "" {
		t.Skip("isolated snapshot not supplied")
	}
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "passive.db")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := rings.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loop := &Loop{Store: store, TaskID: task, Provider: noAssetModel{t}, Swarm: &swarm.Executor{Blobs: swarm.NewBlobStore(os.Getenv("GHOSTREAPER_SNAPSHOT_BLOBS"))}}
	loop.syncRecordedAssets(t.Context())
	graph, err := store.AssetGraph(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range graph.Assets {
		if a.AssetClass == rings.AssetAPI {
			t.Fatal("resource in inventory")
		}
	}
	t.Logf("subjects and supporting addresses=%d, relationships=%d", len(graph.Assets), len(graph.Relations))
	if out := os.Getenv("GHOSTREAPER_SNAPSHOT_OUTPUT"); out != "" {
		if err := store.SnapshotTo(t.Context(), out); err != nil {
			t.Fatal(err)
		}
	}
}
