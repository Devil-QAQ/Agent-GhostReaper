package websearch

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestOfficialLimitDefaultsAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		kind        string
		qps         float64
		minute, day int
	}{{"fofa", .9, 0, 0}, {"brave", 45, 0, 0}, {"deepseek", 90, 0, 0}, {"tavily", 0, 90, 0}, {"zoomeye", .45, 0, 0}, {"zerozone", 0, 0, 225}, {"bing", 0, 0, 0}, {"ddgs", 0, 0, 0}, {"hunter", 0, 0, 0}, {"quake", 0, 0, 0}, {"shodan", 0, 0, 0}, {"daydaymap", 0, 0, 0}} {
		cfg, err := normalizeAsset(Config{Kind: tc.kind})
		if err != nil || cfg.QPS != tc.qps || cfg.PerMinute != tc.minute || cfg.PerDay != tc.day {
			t.Fatalf("%s: %+v %v", tc.kind, cfg, err)
		}
	}
	cfg, err := normalizeAsset(Config{Kind: "tavily", LimitPlan: "paid", LimitsVersion: 1})
	if err != nil || cfg.PerMinute != 900 {
		t.Fatal("plan 90% incorrect", cfg, err)
	}
	cfg, err = normalizeAsset(Config{Kind: "deepseek", CustomLimits: true, LimitsVersion: 1})
	if err != nil || cfg.QPS != 0 {
		t.Fatal("explicit blank not unlimited")
	}
	store := openStore(t)
	for i := 0; i < 15; i++ {
		if err := reserveAsset(t.Context(), store, cfg); err != nil {
			t.Fatal("unlimited rejected", err)
		}
	}
	cfg.QPS = .9
	v := assetQuotaView(assetUsage{Requests: []time.Time{time.Now()}}, cfg, time.Now())
	if v.RetryAt.IsZero() {
		t.Fatal("fractional QPS not enforced")
	}
}

func TestOldGenericLimitsMigrateAndKeySurvives(t *testing.T) {
	store := openStore(t)
	s := []Service{{ID: "old", Name: "鹰图", Enabled: true, Config: Config{Kind: "hunter", APIKey: "private-key", PerMinute: 6, PerDay: 100, PageSize: 10}}}
	b, _ := json.Marshal(s)
	if err := store.SetSetting(t.Context(), SettingKeyServices, string(b), "test"); err != nil {
		t.Fatal(err)
	}
	list, err := Services(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].PerMinute != 0 || list[0].PerDay != 0 || list[0].APIKey != "private-key" || list[0].LimitsVersion != 1 {
		t.Fatal("old invented defaults remained")
	}
	cfg, err := normalizeAsset(Config{Kind: "hunter", PerMinute: 12, PerDay: 100})
	if err != nil || !cfg.CustomLimits || cfg.PerMinute != 12 {
		t.Fatal("explicit old customization lost")
	}
}

func TestBraveActualPolicyOverridesPublicPlan(t *testing.T) {
	store := openStore(t)
	cfg, _ := normalizeAsset(Config{Kind: "brave", APIKey: "legacy-plan-key"})
	h := http.Header{"X-Ratelimit-Policy": []string{"1;w=1, 100;w=2592000"}, "X-Ratelimit-Remaining": []string{"0, 10"}, "X-Ratelimit-Reset": []string{"1, 3600"}}
	if err := recordSitePolicy(store, cfg, h); err != nil {
		t.Fatal(err)
	}
	m, err := readAssetUsage(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	u := m[assetAccount(cfg)]
	if u.OfficialQPS != .9 || !u.Blocked.After(time.Now().Add(3500*time.Second)) {
		t.Fatal("actual per-key/monthly policy ignored", u)
	}
}
