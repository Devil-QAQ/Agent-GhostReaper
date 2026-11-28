package websearch

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func pointer(v string) *string { return &v }

func TestServiceCatalogEmptyCRUDAndSelection(t *testing.T) {
	store := openStore(t)
	list, err := Services(t.Context(), store)
	if err != nil || len(list) != 0 {
		t.Fatal("new catalog not empty")
	}
	if _, err := ResolveService(t.Context(), store, ""); err != ErrNotConfigured {
		t.Fatal("implicit default remained")
	}
	list, err = SaveService(t.Context(), store, ServiceChange{Name: "custom one", Enabled: true, Config: testConfig("https://one.example/search"), APIKey: pointer("private-secret"), Proxy: pointer("http://user:pass@proxy.example:8080")}, "test")
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	first := list[0].ID
	resolved, err := ResolveService(t.Context(), store, "")
	if err != nil || resolved.ServiceID != first || resolved.APIKey != "private-secret" {
		t.Fatal("single service did not resolve")
	}
	list, err = SaveService(t.Context(), store, ServiceChange{ID: first, Name: "renamed", Enabled: true, Config: testConfig("https://changed.example/query")}, "test")
	if err != nil || list[0].APIKey != "private-secret" || list[0].Proxy == "" {
		t.Fatal("omitted credentials were removed")
	}
	list, err = SaveService(t.Context(), store, ServiceChange{Name: "custom two", Enabled: true, Config: testConfig("https://two.example/api")}, "test")
	if err != nil || len(list) != 2 {
		t.Fatal(err)
	}
	if _, err := ResolveService(t.Context(), store, ""); err != ErrServiceRequired {
		t.Fatal("multiple services were silently selected")
	}
	selected, err := ResolveService(t.Context(), store, first)
	if err != nil || selected.ID != first {
		t.Fatal("explicit selection failed")
	}
	_, err = SaveService(t.Context(), store, ServiceChange{ID: first, Name: "renamed", Enabled: false, Config: testConfig("https://changed.example/query"), APIKey: pointer(""), Proxy: pointer("")}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveService(t.Context(), store, first); err == nil {
		t.Fatal("disabled service was selected")
	}
	services, _ := Services(t.Context(), store)
	if services[0].APIKey != "" || services[0].Proxy != "" {
		t.Fatal("explicit clear failed")
	}
	for _, item := range services {
		if _, err := DeleteService(t.Context(), store, item.ID, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := Services(t.Context(), store); err != nil || len(list) != 0 {
		t.Fatal("deleted services returned")
	}
	// A durable empty registry must suppress stale legacy settings.
	store.SetSetting(t.Context(), "search.backend", "ddgs", "test")
	if list, err := Services(t.Context(), store); err != nil || len(list) != 0 {
		t.Fatal("empty catalog fell back to old settings")
	}
}

func TestLegacySettingsBecomeEditableHTTPData(t *testing.T) {
	store := openStore(t)
	store.SetSetting(t.Context(), "search.backend", "brave", "test")
	store.SetSetting(t.Context(), "search.brave.key", "legacy-key", "test")
	store.SetSetting(t.Context(), "search.proxy", "http://user:pass@proxy.example:8080", "test")
	list, err := Services(t.Context(), store)
	if err != nil || len(list) != 1 || list[0].APIKey != "legacy-key" || list[0].Headers["X-Subscription-Token"] != "{{api_key}}" || list[0].ResultsPath != "web.results" {
		t.Fatalf("migration %+v %v", list, err)
	}
	for _, key := range legacyKeys {
		if _, ok, err := store.Setting(t.Context(), key); err != nil || ok {
			t.Fatal("legacy record retained")
		}
	}
	if _, err := DeleteService(t.Context(), store, list[0].ID, "test"); err != nil {
		t.Fatal(err)
	}
	if list, _ := Services(t.Context(), store); len(list) != 0 {
		t.Fatal("deleted migrated service was recreated")
	}
}

func TestServiceWritesAreSerialized(t *testing.T) {
	store := openStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := SaveService(t.Context(), store, ServiceChange{Name: "parallel", Enabled: true, Config: testConfig("https://example.test/search")}, "test"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := Services(t.Context(), store)
	if err != nil || len(list) != 12 {
		t.Fatal("concurrent saves lost services")
	}
}

func TestPublicConfigurationKeepsSecretsPrivate(t *testing.T) {
	cfg := testConfig("https://example.test/search")
	cfg.APIKey = "private-value"
	cfg.Proxy = "http://user:pass@proxy.example:8080"
	cfg.Headers["X-Key"] = "private-value"
	cfg.Params["token"] = "private-value"
	public := PublicConfig(cfg)
	data, _ := json.Marshal(public)
	if strings.Contains(string(data), "private-value") || strings.Contains(string(data), "user:pass") || public.Headers["X-Key"] != "{{api_key}}" {
		t.Fatal("public view leaked a secret")
	}
	if cfg.Headers["X-Key"] != "private-value" {
		t.Fatal("masking mutated the execution configuration")
	}
}
