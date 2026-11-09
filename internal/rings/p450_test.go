package rings

import (
	"testing"
)

// ---- P4-50 ③(问题清单 #73 ①):范围写法即授权粒度 ----
// 五种写法各自边界、多种子并集、种子域名当前解析 IP 并入(②,DNS 打桩;
// 解析失败按范围外保守)。

func p450Closure(seeds ...string) *ScopeClosure {
	out := make([]ScopeSeed, 0, len(seeds))
	for i, s := range seeds {
		out = append(out, ScopeSeed{ID: string(rune('a' + i)), Title: s, Source: AssetSourceSeed})
	}
	return BuildScopeClosure(out, nil, nil)
}

func TestP450BareDomainGranularity(t *testing.T) {
	// 裸域名 = 该域名全部端口;子域不在内。
	c := p450Closure("x.example.com")
	for _, host := range []string{"x.example.com"} {
		if !c.CoversTarget(host, "", nil) {
			t.Fatalf("裸域名自身应覆盖: %s", host)
		}
	}
	for _, port := range []string{"80", "443", "8080", "1234"} {
		if !c.CoversTarget("x.example.com", port, nil) {
			t.Fatalf("裸域名 = 全端口: x.example.com:%s", port)
		}
	}
	for _, host := range []string{"sub.x.example.com", "y.example.com", "example.com"} {
		if c.CoversTarget(host, "", nil) {
			t.Fatalf("子域/兄弟域不在裸域名授权内: %s", host)
		}
	}
}

func TestP450WildcardGranularity(t *testing.T) {
	// *.域 = 基域 + 任意层级子域,全部端口。
	c := p450Closure("*.example.com")
	for _, host := range []string{"example.com", "a.example.com", "a.b.example.com"} {
		if !c.CoversTarget(host, "", nil) || !c.CoversTarget(host, "8443", nil) {
			t.Fatalf("*.域应含 %s(基域与子域,全端口)", host)
		}
	}
	for _, host := range []string{"example.org", "notexample.com", "a.b.c.example.net"} {
		if c.CoversTarget(host, "", nil) {
			t.Fatalf("*.域之外不在内: %s", host)
		}
	}
}

func TestP450BareIPGranularity(t *testing.T) {
	// 裸 IP = 整台全部端口;其他 IP 不在内。
	c := p450Closure("10.0.0.9")
	if !c.CoversTarget("10.0.0.9", "", nil) || !c.CoversTarget("10.0.0.9", "9999", nil) {
		t.Fatal("裸 IP = 整台全端口")
	}
	if c.CoversTarget("10.0.0.10", "", nil) {
		t.Fatal("其他 IP 不在裸 IP 授权内")
	}
}

func TestP450HostPortGranularity(t *testing.T) {
	// IP:端口 / 域名:端口 = 仅此端口;他端口越界;裸目标(全端口意图,
	// 如 nmap 无 -p)不放行。
	ipPort := p450Closure("10.0.0.9:8080")
	if !ipPort.CoversTarget("10.0.0.9", "8080", nil) {
		t.Fatal("IP:端口 授权端口应覆盖")
	}
	if ipPort.CoversTarget("10.0.0.9", "9090", nil) {
		t.Fatal("IP:端口 他端口越界")
	}
	if ipPort.CoversTarget("10.0.0.9", "", nil) {
		t.Fatal("裸 IP 目标(全端口意图)不在 IP:端口 授权内")
	}
	hostPort := p450Closure("api.example.com:443")
	if !hostPort.CoversTarget("api.example.com", "443", nil) {
		t.Fatal("域名:端口 授权端口应覆盖")
	}
	if hostPort.CoversTarget("api.example.com", "8080", nil) {
		t.Fatal("域名:端口 他端口越界")
	}
	if hostPort.CoversTarget("api.example.com", "", nil) {
		t.Fatal("裸域名目标(全端口意图)不在 域名:端口 授权内")
	}
}

func TestP450URLSeedEqualsDomain(t *testing.T) {
	// URL 种子 = 提取 host 等同裸域名(全端口;URL 里的端口忽略)。
	c := p450Closure("https://app.example.com:8443/path")
	if !c.CoversTarget("app.example.com", "", nil) || !c.CoversTarget("app.example.com", "9090", nil) {
		t.Fatal("URL 种子应等同裸域名(全端口)")
	}
	if c.CoversTarget("other.example.com", "", nil) {
		t.Fatal("URL 种子不含兄弟域")
	}
}

func TestP450SeedUnion(t *testing.T) {
	// 多条种子取并集。
	c := p450Closure("www.example.com", "10.0.0.5", "*.other.net", "api.example.com:8443")
	for host, port := range map[string]string{
		"www.example.com": "",
		"10.0.0.5":        "7070",
		"a.other.net":     "",
		"api.example.com": "8443",
	} {
		if !c.CoversTarget(host, port, nil) {
			t.Fatalf("并集成员应覆盖 %s:%s", host, port)
		}
	}
}

func TestP450SeedResolveIPMerge(t *testing.T) {
	// ②种子域名当前解析 IP 并入范围(DNS 打桩):直连解析 IP 视为范围
	// 内(全端口);解析失败按范围外保守;端口限定域名种子仅并入同端口;
	// 通配基域的解析 IP 同样并入;闭包重建后按新解析。
	resolve := func(host string) []string {
		switch host {
		case "x.example.com", "example.com":
			return []string{"1.2.3.4"}
		case "api.example.org":
			return []string{"5.6.7.8"}
		}
		return nil
	}
	c := p450Closure("x.example.com")
	if !c.CoversTarget("1.2.3.4", "", resolve) || !c.CoversTarget("1.2.3.4", "8080", resolve) {
		t.Fatal("种子域名解析 IP 直连应视为范围内(全端口)")
	}
	if c.CoversTarget("9.9.9.9", "", resolve) {
		t.Fatal("解析 IP 之外的直连不在内")
	}
	if c.CoversTarget("1.2.3.5", "", resolve) {
		t.Fatal("解析失败(无该 IP)按范围外保守")
	}
	// 同机扩展:域名解析到种子域名的解析 IP 视为同机(同 IP 共站风险
	// 已接受)。
	if !c.CoversTarget("vhost.example.net", "", func(host string) []string {
		if host == "vhost.example.net" {
			return []string{"1.2.3.4"}
		}
		return resolve(host)
	}) {
		t.Fatal("解析到种子域名解析 IP 的域名应视为同机")
	}
	// 端口限定域名种子:仅并入同端口,不做同机扩展。
	cp := p450Closure("api.example.org:443")
	if !cp.CoversTarget("5.6.7.8", "443", resolve) {
		t.Fatal("端口限定种子的解析 IP 同端口应覆盖")
	}
	if cp.CoversTarget("5.6.7.8", "8080", resolve) {
		t.Fatal("端口限定种子的解析 IP 他端口不覆盖")
	}
	if cp.CoversTarget("5.6.7.8", "", resolve) {
		t.Fatal("端口限定种子的解析 IP 裸目标(全端口意图)不覆盖")
	}
	// 通配基域解析 IP 并入(*.域 含基域)。
	cw := p450Closure("*.example.com")
	if !cw.CoversTarget("1.2.3.4", "", resolve) {
		t.Fatal("通配基域的解析 IP 应并入")
	}
	// 解析变更:闭包重建后按新解析(缓存寿命=闭包)。
	c2 := p450Closure("x.example.com")
	newResolve := func(string) []string { return []string{"1.1.1.1"} }
	if !c2.CoversTarget("1.1.1.1", "", newResolve) {
		t.Fatal("重建闭包应按新解析判定")
	}
	if c2.CoversTarget("1.2.3.4", "", newResolve) {
		t.Fatal("旧解析 IP 在重建闭包下不再覆盖")
	}
}

func TestP450JudgeAssetScopePortAware(t *testing.T) {
	// 资产标题带端口按端口限定判定;无锚点仍按范围内。
	c := p450Closure("www.example.com")
	if got := JudgeAssetScope(c, "www.example.com:8080", nil); got != AssetScopeIn {
		t.Errorf("种子域名资产带端口应 in,得 %q", got)
	}
	if got := JudgeAssetScope(c, "api.example.com:8080", nil); got != AssetScopeOut {
		t.Errorf("兄弟子域资产应 out,得 %q", got)
	}
	// 端口限定种子下,同 host 他端口资产 out;同 host 裸域资产(全端口
	// 意图)out。
	cp := p450Closure("api.example.com:8443")
	if got := JudgeAssetScope(cp, "api.example.com:8443", nil); got != AssetScopeIn {
		t.Errorf("端口限定种子同 host 同端口资产应 in,得 %q", got)
	}
	if got := JudgeAssetScope(cp, "api.example.com:9090", nil); got != AssetScopeOut {
		t.Errorf("端口限定种子他端口资产应 out,得 %q", got)
	}
	if got := JudgeAssetScope(cp, "api.example.com", nil); got != AssetScopeOut {
		t.Errorf("端口限定种子裸域资产应 out,得 %q", got)
	}
	if got := JudgeAssetScope(cp, "张三(测试账号)", nil); got != AssetScopeIn {
		t.Errorf("无锚点资产应 in,得 %q", got)
	}
}

func TestP450WildcardAnchorForms(t *testing.T) {
	// 通配写法识别:大小写归一、与端口组合不支持(无锚点)。
	a, ok := ScopeAnchorOf("*.Example.COM")
	if !ok || a.Host != "example.com" || !a.Wildcard {
		t.Fatalf("*.域 锚点: %+v %v", a, ok)
	}
	if _, ok := ScopeAnchorOf("*.example.com:443"); ok {
		t.Fatal("通配+端口不支持")
	}
	// 闭包内通配种子的资产标题判定。
	c := p450Closure("*.example.com")
	if got := JudgeAssetScope(c, "deep.sub.example.com", nil); got != AssetScopeIn {
		t.Errorf("通配种子子域资产应 in,得 %q", got)
	}
	if got := JudgeAssetScope(c, "example.org", nil); got != AssetScopeOut {
		t.Errorf("通配外资产应 out,得 %q", got)
	}
}
