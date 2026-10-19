package mirror

// CA 与目标证书分类的单元验证(P3-01):
//   - CA 设置表持久化,跨镜实例稳定(重启不轮换);
//   - 叶证书按目标域/IP 签发并缓存;
//   - 校验分类五类结论(可信/自签/过期/主机名不符/未知签发)如实可辨。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// testCA 生成一对测试 CA(自签,非镜 CA)。
func testCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 CA 密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签 CA 失败: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析 CA 失败: %v", err)
	}
	return cert, key
}

// testLeaf 由 CA 签发叶证书(DNS 或 IP SAN)。
func testLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成叶密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发叶证书失败: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析叶证书失败: %v", err)
	}
	return leaf, key
}

func TestCAPersistedAcrossInstances(t *testing.T) {
	tmp := t.TempDir()
	store, err := rings.Open(filepath.Join(tmp, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	m1 := New(store, newMemBlobs(), tmp)
	b1, err1 := os.ReadFile(filepath.Join(tmp, trustBundleFile))
	if err1 != nil {
		t.Fatalf("信任包应已生成: %v", err1)
	}
	// 第二个镜实例(同库):CA 不轮换——信任包字节级一致。
	m2 := New(store, newMemBlobs(), tmp)
	b2, err2 := os.ReadFile(filepath.Join(tmp, trustBundleFile))
	if err2 != nil {
		t.Fatalf("二次读信任包失败: %v", err2)
	}
	if string(b1) != string(b2) {
		t.Fatal("CA 跨实例应稳定(重启不轮换),信任包内容不一致")
	}
	// 设置表里确实落了 CA(证书+私钥)。
	if _, ok, _ := store.Setting(t.Context(), settingCACert); !ok {
		t.Fatal("CA 证书未持久化到设置表")
	}
	if _, ok, _ := store.Setting(t.Context(), settingCAKey); !ok {
		t.Fatal("CA 私钥未持久化到设置表")
	}
	// m2 的信任池应能验 m1 签的叶(同一 CA)。
	leaf, err := m1.leafFor("127.0.0.1")
	if err != nil {
		t.Fatalf("签发叶证书失败: %v", err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "127.0.0.1", Roots: m2.pool}); err != nil {
		t.Fatalf("跨实例叶证书应可验: %v", err)
	}
}

func TestLeafForDNSAndIPAndCache(t *testing.T) {
	tmp := t.TempDir()
	store, err := rings.Open(filepath.Join(tmp, "rings.db"))
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	defer func() { _ = store.Close() }()
	m := New(store, newMemBlobs(), tmp)

	ipLeaf, err := m.leafFor("127.0.0.1:58631") // 带 port 亦归一到主机
	if err != nil {
		t.Fatalf("签发 IP 叶失败: %v", err)
	}
	if len(ipLeaf.Leaf.IPAddresses) != 1 || !ipLeaf.Leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IP 目标应走 IP SAN: %+v", ipLeaf.Leaf.IPAddresses)
	}
	dnsLeaf, err := m.leafFor("mock.internal")
	if err != nil {
		t.Fatalf("签发 DNS 叶失败: %v", err)
	}
	if len(dnsLeaf.Leaf.DNSNames) != 1 || dnsLeaf.Leaf.DNSNames[0] != "mock.internal" {
		t.Fatalf("DNS 目标应走 DNS SAN: %+v", dnsLeaf.Leaf.DNSNames)
	}
	again, err := m.leafFor("mock.internal")
	if err != nil {
		t.Fatalf("二次取叶失败: %v", err)
	}
	if again != dnsLeaf {
		t.Fatal("同主机叶证书应命中缓存")
	}
	if _, err := dnsLeaf.Leaf.Verify(x509.VerifyOptions{DNSName: "mock.internal", Roots: m.pool}); err != nil {
		t.Fatalf("叶证书应可由镜信任池验过: %v", err)
	}
}

func TestClassifyConnState(t *testing.T) {
	ca, caKey := testCA(t, "Test Roots CA")
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	mk := func(host string, notBefore, notAfter time.Time) tls.ConnectionState {
		leaf, _ := testLeaf(t, ca, caKey, host, notBefore, notAfter)
		return tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	}
	now := time.Now()

	if got := classifyConnState(mk("good.test", now.Add(-time.Hour), now.Add(time.Hour)), "good.test", pool); got.Validity != TLSValidTrusted {
		t.Fatalf("信任链内应判 trusted: %+v", got)
	}
	info := classifyConnState(mk("good.test", now.Add(-time.Hour), now.Add(time.Hour)), "other.test", pool)
	if info.Validity != TLSValidHostname {
		t.Fatalf("主机名不符应判 hostname_mismatch: %+v", info)
	}
	info = classifyConnState(mk("expired.test", now.Add(-48*time.Hour), now.Add(-24*time.Hour)), "expired.test", pool)
	if info.Validity != TLSValidExpired || info.VerifyError == "" {
		t.Fatalf("过期应判 expired 且留原始错误: %+v", info)
	}
	// 自签:链未知 + 叶自验通过。
	selfKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	selfTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "self.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"self.test"},
	}
	selfDER, err := x509.CreateCertificate(rand.Reader, selfTmpl, selfTmpl, &selfKey.PublicKey, selfKey)
	if err != nil {
		t.Fatalf("自签失败: %v", err)
	}
	selfLeaf, _ := x509.ParseCertificate(selfDER)
	info = classifyConnState(tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfLeaf}}, "self.test", pool)
	if info.Validity != TLSValidSelfSigned {
		t.Fatalf("自签应判 self_signed: %+v", info)
	}
	// 未知签发方(非自签):换一个 CA 签。
	otherCA, otherKey := testCA(t, "Other CA")
	otherLeaf, _ := testLeaf(t, otherCA, otherKey, "unknown.test", now.Add(-time.Hour), now.Add(time.Hour))
	info = classifyConnState(tls.ConnectionState{PeerCertificates: []*x509.Certificate{otherLeaf}}, "unknown.test", pool)
	if info.Validity != TLSValidUntrusted {
		t.Fatalf("未知签发应判 untrusted(与自签区分): %+v", info)
	}
}
