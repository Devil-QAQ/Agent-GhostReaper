package server

import (
	"context"
	"encoding/json"
	"fmt"
	"ghostreaper/internal/failover"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskHelpersOutputLimitFollowsLibrary(t *testing.T) {
	for _, limit := range []int{0, 1024, 8192, 65536} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			srv, _ := newSettingsServer(t)
			ctx := context.Background()
			var got []int
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					MaxTokens int `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				got = append(got, req.MaxTokens)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"title":"测试","assets":[{"class":"domain","title":"example.test"}],"goals":["核对授权服务"]}`}, "finish_reason": "stop"}}})
			}))
			defer endpoint.Close()
			if err := failover.SaveModelConfig(ctx, srv.Store, failover.ModelConfig{Name: "limits", Model: "limits", Format: "openai", APIKey: "test", BaseURL: endpoint.URL, StreamDisabled: true, MaxTokens: limit}, "test"); err != nil {
				t.Fatal(err)
			}
			if err := failover.SetActive(ctx, srv.Store, failover.RolePrimary, "limits", "test"); err != nil {
				t.Fatal(err)
			}
			srv.Chain = failover.NewChain(srv.Store, failover.EnvConfig{})
			for _, tc := range []struct{ path, body string }{{"/api/tasks/extract", `{"text":"核对 example.test 授权服务"}`}, {"/api/tasks/assets-organize", `{"lines":["example.test"]}`}} {
				post(t, srv.Handler(), "POST", tc.path, tc.body, http.StatusOK, &struct{}{})
			}
			if len(got) != 2 {
				t.Fatalf("requests=%d", len(got))
			}
			for _, n := range got {
				if n != limit {
					t.Fatalf("configured=%d sent=%d", limit, n)
				}
			}
		})
	}
}
