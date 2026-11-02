package mirror

// CA 与信任(P3-01,HTTPS 强制接管的解密侧基础设施):
//   - CA 一次生成、设置表持久化(重启不轮换,信任一次安装长期有效);
//   - 按目标域签发叶证书(SNI 缺席时退 CONNECT 目标,IP 目标走 IP SAN);
//   - 信任安装:工具客户端自动注入——Go 工具拿 RootCAs 池(系统根+CA),
//     子进程工具拿 SSL_CERT_FILE/REQUESTS_CA_BUNDLE 指向的信任包文件
//     (系统根+CA 拼合,公开 CA 目标经镜仍可用);
//   - 目标证书校验结论如实分类(可信/自签/过期/主机名不符/未知签发/
//     校验未过):镜是审计通道不是信任闸——不据此拒绝流量,裁决在审计侧。

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"ghostreaper/internal/runlog"
)

// CA 持久化设置键(值均为 PEM 文本;私钥入设置表待 P3-09 静态加密收口)。
const (
	settingCACert = "mirror.ca.cert"
	settingCAKey  = "mirror.ca.key"
)

// 目标证书校验结论 token(交换文档与账本摘要共用;标签见 TLSLabel)。
const (
	TLSValidTrusted    = "trusted"           // 系统信任链内验证通过
	TLSValidSelfSigned = "self_signed"       // 自签(链未知且叶证书自验通过)
	TLSValidExpired    = "expired"           // 过期或未生效
	TLSValidHostname   = "hostname_mismatch" // 证书与目标主机名不符
	TLSValidUntrusted  = "untrusted"         // 未知签发方(非自签)
	TLSValidInvalid    = "invalid"           // 其余校验未过情形
)

// TLSLabel 校验结论 token → 操作者语言(报告复现步骤与 P3-02 流量页共用)。
func TLSLabel(v string) string {
	switch v {
	case TLSValidTrusted:
		return "可信"
	case TLSValidSelfSigned:
		return "自签"
	case TLSValidExpired:
		return "过期"
	case TLSValidHostname:
		return "主机名不符"
	case TLSValidUntrusted:
		return "未知签发"
	default:
		return "校验未过"
	}
}

// TLSInfo 目标侧 TLS 事实,入交换文档 tls 字段(回放查证的一部分)。
type TLSInfo struct {
	Host        string `json:"host"`                   // 校验所对的目标主机(SNI 或 CONNECT 目标)
	Validity    string `json:"validity"`               // 结论 token(见上)
	PeerSubject string `json:"peer_subject,omitempty"` // 目标证书主体
	PeerIssuer  string `json:"peer_issuer,omitempty"`  // 目标证书签发方
	VerifyError string `json:"verify_error,omitempty"` // 非 trusted 时的原始校验错误文本
}

// 叶证书参数(短命+按域缓存:签发快,泄漏面小)。
const (
	leafValidity = 90 * 24 * time.Hour
	leafRegenIn  = 24 * time.Hour // 距到期不足此时长即重签
	caValidity   = 10 * 365 * 24 * time.Hour
)

// systemBundlePaths 常见系统根证书束路径(信任包拼合用;命中即含公开 CA)。
var systemBundlePaths = []string{
	"/etc/ssl/cert.pem",                                 // macOS / 部分 BSD
	"/etc/ssl/certs/ca-certificates.crt",                // Debian 系
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora 旧位
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // RHEL 系
	"/etc/ssl/ca-bundle.pem",                            // SUSE
}

// trustBundleFile 信任包文件名(落 New 传入的数据目录;每次启动重写)。
const trustBundleFile = "mirror-ca-bundle.pem"

// initCA 装配 CA 与信任包:设置表优先,缺则生成并持久化;持久化失败
// 退进程内存 CA(重启轮换,如实打日志)。New 内调用,失败不致命——
// HTTPS 接管降级为当次进程可用。
func (m *Mirror) initCA(dir string) {
	cert, key, err := m.loadCA()
	if err != nil {
		runlog.Warnf("镜 CA 载入失败,生成新 CA: %v", err)
		cert, key, err = genCA()
		if err != nil {
			runlog.Errorf("镜 CA 生成失败,HTTPS 接管不可用: %v", err)
			return
		}
		if err := m.persistCA(cert, key); err != nil {
			runlog.Warnf("镜 CA 持久化失败,重启后将轮换: %v", err)
		}
	}
	m.caCert = cert
	m.ca = tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key}
	m.leaves = map[string]*tls.Certificate{}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
		runlog.Warnf("读系统根证书池失败,信任池仅含镜 CA: %v", err)
	}
	pool.AddCert(cert)
	m.pool = pool
	m.writeTrustBundle(dir)
}

// loadCA 从设置表读回 CA(证书与私钥须配对,否则视为损坏)。
func (m *Mirror) loadCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	ctx := context.Background()
	certE, okC, err := m.store.Setting(ctx, settingCACert)
	if err != nil {
		return nil, nil, fmt.Errorf("读 %s: %w", settingCACert, err)
	}
	keyE, okK, err := m.store.Setting(ctx, settingCAKey)
	if err != nil {
		return nil, nil, fmt.Errorf("读 %s: %w", settingCAKey, err)
	}
	if !okC || !okK {
		return nil, nil, fmt.Errorf("未配置")
	}
	der, err := firstPEM([]byte(certE.Value), "CERTIFICATE")
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	if time.Now().After(cert.NotAfter) {
		return nil, nil, fmt.Errorf("CA 已过期(%s 前)", time.Since(cert.NotAfter).Round(time.Minute))
	}
	kder, err := firstPEM([]byte(keyE.Value), "PRIVATE KEY")
	if err != nil {
		return nil, nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(kder)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CA 私钥: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("CA 私钥类型不符: %T", k)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.X.Cmp(key.PublicKey.X) != 0 || pub.Y.Cmp(key.PublicKey.Y) != 0 {
		return nil, nil, fmt.Errorf("CA 证书与私钥不配对")
	}
	return cert, key, nil
}

// persistCA 写设置表(先写私钥后写证书:崩溃中断时宁可整体视为未配置)。
func (m *Mirror) persistCA(cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("编组私钥: %w", err)
	}
	ctx := context.Background()
	if err := m.store.SetSetting(ctx, settingCAKey, pemEncode("PRIVATE KEY", kder), "mirror"); err != nil {
		return fmt.Errorf("写 %s: %w", settingCAKey, err)
	}
	return m.store.SetSetting(ctx, settingCACert, pemEncode("CERTIFICATE", cert.Raw), "mirror")
}

// genCA 生成自签 CA(ECDSA P-256)。
func genCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成密钥: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          randSerial(),
		Subject:               pkix.Name{CommonName: "GhostReaper Mirror CA", Organization: []string{"GhostReaper"}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("自签: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("解析: %w", err)
	}
	return cert, key, nil
}

// leafFor 为目标主机签发叶证书(带缓存;距到期不足 leafRegenIn 重签)。
func (m *Mirror) leafFor(host string) (*tls.Certificate, error) {
	host = hostOf(host)
	m.leafMu.Lock()
	defer m.leafMu.Unlock()
	if leaf, ok := m.leaves[host]; ok {
		if time.Now().Before(m.caCert.NotAfter.Add(-leafRegenIn)) &&
			time.Now().Before(leaf.Leaf.NotAfter.Add(-leafRegenIn)) {
			return leaf, nil
		}
		delete(m.leaves, host) // 临近到期:重签保新鲜
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成叶密钥: %w", err)
	}
	now := time.Now()
	notAfter := now.Add(leafValidity)
	if m.caCert.NotAfter.Before(notAfter) {
		notAfter = m.caCert.NotAfter // 不越过 CA 有效期
	}
	tmpl := &x509.Certificate{
		SerialNumber: randSerial(),
		Subject:      pkix.Name{CommonName: host, Organization: []string{"GhostReaper Mirror"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, m.caCert, &key.PublicKey, m.ca.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("签发叶证书: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析叶证书: %w", err)
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	m.leaves[host] = cert
	return cert, nil
}

// writeTrustBundle 铺信任包文件(系统根+镜 CA):子进程工具的信任安装
// 落点,SSL_CERT_FILE/REQUESTS_CA_BUNDLE 指向它。写失败仅打日志——
// ProxyEnv 会因 bundlePath 为空而不注入,HTTPS 经镜在子进程侧不可信,
// 属显式可见的降级而非静默。
func (m *Mirror) writeTrustBundle(dir string) {
	var sys []byte
	for _, p := range systemBundlePaths {
		if b, err := os.ReadFile(p); err == nil {
			sys = b
			break
		}
	}
	caPEM := pemEncode("CERTIFICATE", m.caCert.Raw)
	bundle := caPEM
	if len(sys) > 0 {
		bundle = string(sys) + "\n" + caPEM
	} else {
		runlog.Warnf("未找到系统根证书束,信任包仅含镜 CA(公开 CA 目标经镜子进程侧不可信)")
	}
	path := filepath.Join(dir, trustBundleFile)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs // 子进程工作目录各不相同,须绝对路径
	}
	if err := os.WriteFile(path, []byte(bundle), 0o644); err != nil {
		runlog.Warnf("写信任包失败,子进程不注入 CA 信任: %v", err)
		return
	}
	m.bundlePath = path
}

// classifyConnState 目标证书校验分类:按系统信任链(roots 为 nil 时)对
// 目标证书做标准验证,把结果如实归类。镜不据此放行或拒绝——结论入档,
// 裁决在审计侧。分类按自足的有序判定(窗口期 → 主机名 → 信任链 →
// 自签与否),不依赖校验错误的类型学(平台验证器常返回无类型错误);
// 原始错误文本恒随档(VerifyError)。
func classifyConnState(st tls.ConnectionState, host string, roots *x509.CertPool) TLSInfo {
	host = hostOf(host)
	info := TLSInfo{Host: host}
	if len(st.PeerCertificates) == 0 {
		info.Validity, info.VerifyError = TLSValidInvalid, "对端未出示证书"
		return info
	}
	leaf := st.PeerCertificates[0]
	info.PeerSubject, info.PeerIssuer = leaf.Subject.String(), leaf.Issuer.String()
	if roots == nil {
		if sys, err := x509.SystemCertPool(); err == nil {
			roots = sys
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err == nil {
		info.Validity = TLSValidTrusted
		return info
	} else {
		info.VerifyError = err.Error()
		now := time.Now()
		switch {
		case now.Before(leaf.NotBefore) || now.After(leaf.NotAfter):
			info.Validity = TLSValidExpired
		case leaf.VerifyHostname(host) != nil:
			info.Validity = TLSValidHostname
		case isSelfSigned(leaf):
			info.Validity = TLSValidSelfSigned
		default:
			info.Validity = TLSValidUntrusted
		}
	}
	return info
}

// isSelfSigned 自签判定:主体与签发方编码一致,且签名可用自身公钥验过
// (不依赖 CA 基本约束——真实自签服务证书两种形态都存在)。
func isSelfSigned(leaf *x509.Certificate) bool {
	return bytes.Equal(leaf.RawSubject, leaf.RawIssuer) &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// hostOf host:port → host(port 缺失或解析失败原样返回)。
func hostOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// randSerial 128 位随机序列号(避开顺序可猜测)。
func randSerial() *big.Int {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("mirror: crypto/rand 不可用: " + err.Error())
	}
	return new(big.Int).SetBytes(b[:])
}

func pemEncode(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

func firstPEM(pemBytes []byte, blockType string) ([]byte, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("无 %s PEM 块", blockType)
		}
		if block.Type == blockType {
			return block.Bytes, nil
		}
	}
}
