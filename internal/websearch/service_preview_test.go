package websearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDraftQueryDoesNotSaveAndKeepsBudget(t *testing.T) {
	for _, p := range AssetProviders() {
		t.Run(p.Kind, func(t *testing.T) {
			store := openStore(t)
			var got map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = map[string]any{}
				if r.Method == "GET" {
					for k, v := range r.URL.Query() {
						got[k] = v[0]
					}
				} else {
					json.NewDecoder(r.Body).Decode(&got)
				}
				switch p.Kind {
				case "fofa":
					w.Write([]byte(`{"error":false,"results":[]}`))
				case "shodan":
					w.Write([]byte(`{"matches":[]}`))
				case "hunter", "daydaymap":
					w.Write([]byte(`{"code":200,"data":[]}`))
				case "zoomeye":
					w.Write([]byte(`{"code":60000,"data":[]}`))
				default:
					w.Write([]byte(`{"code":0,"data":[]}`))
				}
			}))
			defer api.Close()
			change := ServiceChange{Name: p.Name, Enabled: true, APIKey: pointer("draft-key"), Config: Config{Kind: p.Kind, Endpoint: api.URL, PerMinute: 1}}
			if _, err := TestService(t.Context(), store, change); err != nil {
				t.Fatal(err)
			}
			list, _ := Services(t.Context(), store)
			if len(list) != 0 {
				t.Fatal("draft was saved")
			}
			preset := TestPresets()[p.Kind]
			if preset.Query == "" {
				t.Fatal("empty preset")
			}
			size := got["size"]
			if p.Kind == "hunter" || p.Kind == "daydaymap" {
				size = got["page_size"]
			}
			if p.Kind == "zoomeye" || p.Kind == "zerozone" {
				size = got["pagesize"]
			}
			if p.Kind != "shodan" && fmt.Sprint(size) != fmt.Sprint(preset.Size) {
				t.Fatal("oversized preview", size)
			}
			saved, err := SaveService(t.Context(), store, change, "test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AssetSearch(t.Context(), store, saved[0].ID, AssetQuery{Query: "x"}); err == nil {
				t.Fatal("saved/agent budget separated from test")
			}
			change.ID = saved[0].ID
			change.APIKey = nil
			change.PerMinute = 2
			if _, err := TestService(t.Context(), store, change); err != nil {
				t.Fatal("saved secret not retained", err)
			}
			list, _ = Services(t.Context(), store)
			if list[0].PerMinute != 1 {
				t.Fatal("preview changed saved config")
			}
		})
	}
}
