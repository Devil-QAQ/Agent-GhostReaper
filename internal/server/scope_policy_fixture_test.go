package server

import (
	"context"
	"ghostreaper/internal/rings"
	"net/http"
	"testing"
)

var p444StoreRef *rings.Store

func newP444Server(t *testing.T) (http.Handler, *rings.Store) {
	t.Helper()
	h, store := newP405Server(t)
	p444StoreRef = store
	t.Cleanup(func() { p444StoreRef = nil })
	return h, store
}

func storeOfP444(t *testing.T) *rings.Store {
	t.Helper()
	if p444StoreRef == nil {
		t.Fatal("测试脚手架未初始化 store")
	}
	return p444StoreRef
}

func p444ScopeSeed(t *testing.T, h http.Handler, taskID, taskType, discoverTitle, frozen string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks",
		`{"task_type":"`+taskType+`","id":"`+taskID+`","goals":["目标"],"assets":[{"class":"domain","title":"www.example.com"}]}`,
		http.StatusOK, &created)
	if discoverTitle == "" {
		return taskID, ""
	}
	s := storeOfP444(t)
	detail := map[string]any{"source": rings.AssetSourceDiscovered}
	if frozen != "" {
		detail["scope"] = frozen
	}
	if _, err := s.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: "a-out", NodeType: rings.NodeAsset,
			Title: discoverTitle, Status: "active", AssetClass: rings.AssetHost, Detail: detail},
	}); err != nil {
		t.Fatal(err)
	}
	return taskID, "a-out"
}
