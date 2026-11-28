package rings

import "testing"

func TestNetworkAssetIdentity(t *testing.T) {
	for _, tc := range []struct {
		class                                AssetClass
		input, wantClass, wantTitle, wantKey string
	}{
		{AssetService, "APP.example.org:0080", "service", "app.example.org:80", "service:app.example.org:80"},
		{AssetDomain, "192.0.2.1", "host", "192.0.2.1", "ip:192.0.2.1"},
		{AssetSubdomain, "API.Example.org.", "domain", "api.example.org", "domain:api.example.org"},
		{AssetDomain, "例子.中国", "domain", "xn--fsqu00a.xn--fiqs8s", "domain:xn--fsqu00a.xn--fiqs8s"},
		{AssetHost, "[2001:db8::1]", "host", "2001:db8::1", "ip:2001:db8::1"},
		{AssetHost, "::ffff:192.0.2.1", "host", "192.0.2.1", "ip:192.0.2.1"},
		{AssetAPI, "https://API.Example.org:443/v1/status?view=short", "service", "https://api.example.org", "service:api.example.org:443"},
		{AssetService, "http://[2001:db8::1]:8080/app", "service", "http://[2001:db8::1]:8080/app", "service:[2001:db8::1]:8080"},
	} {
		c, title, key, err := NetworkAssetIdentity(tc.class, tc.input)
		if err != nil || string(c) != tc.wantClass || title != tc.wantTitle || key != tc.wantKey {
			t.Fatalf("%s %q => %s %q %q %v", tc.class, tc.input, c, title, key, err)
		}
	}
}

func TestNetworkAssetIdentityDoesNotMergeDistinctServices(t *testing.T) {
	keys := map[string]bool{}
	for _, title := range []string{"http://one.example.org:8080/", "http://two.example.org:8080/", "http://one.example.org:8081/"} {
		_, _, key, err := NetworkAssetIdentity(AssetService, title)
		if err != nil || keys[key] {
			t.Fatalf("distinct service merged: %s %v", key, err)
		}
		keys[key] = true
	}
	_, _, a, _ := NetworkAssetIdentity(AssetService, "https://one.example.org/app/a")
	_, _, b, _ := NetworkAssetIdentity(AssetService, "https://one.example.org/app/b")
	if a != b {
		t.Fatal("service paths became distinct service assets")
	}
}

func TestNetworkAssetIdentityRejectsBackgroundAndMalformedAddresses(t *testing.T) {
	for _, tc := range []struct {
		class AssetClass
		title string
	}{
		{AssetOrganization, "登记机构"}, {AssetPerson, "联系人"}, {AssetLeakInfo, "本地报告"},
		{AssetHost, "example.org"}, {AssetDomain, "https://example.org/a"}, {AssetDomain, "a..example.org"},
		{AssetService, "example.org/app"}, {AssetService, "https://name:private@example.org/"},
		{AssetService, "http://example.org:70000/"},
	} {
		if _, _, _, err := NetworkAssetIdentity(tc.class, tc.title); err == nil {
			t.Fatalf("accepted background/malformed asset: %s", tc.class)
		}
	}
}
