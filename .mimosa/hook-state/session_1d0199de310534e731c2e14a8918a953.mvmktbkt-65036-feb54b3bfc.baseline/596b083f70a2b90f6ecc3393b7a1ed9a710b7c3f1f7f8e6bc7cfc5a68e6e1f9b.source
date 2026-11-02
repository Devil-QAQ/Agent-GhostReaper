package secrets

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, keyLen)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestSealOpenRoundtrip(t *testing.T) {
	c := newTestCipher(t)
	for _, plain := range []string{"", "sk-abc123", "密码与中文🔐", strings.Repeat("x", 10000)} {
		sealed, err := c.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		if !IsSealed(sealed) {
			t.Fatalf("Seal 结果未带 v1 前缀: %q", sealed)
		}
		if strings.Contains(sealed, plain) && plain != "" {
			t.Fatalf("密文含明文: %q", sealed)
		}
		got, err := c.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plain {
			t.Fatalf("往返不一致: got %q want %q", got, plain)
		}
	}
}

func TestSealNonceUnique(t *testing.T) {
	c := newTestCipher(t)
	a, _ := c.Seal("same")
	b, _ := c.Seal("same")
	if a == b {
		t.Fatal("两次封装相同明文得相同密文(nonce 未随机)")
	}
}

func TestOpenTampered(t *testing.T) {
	c := newTestCipher(t)
	sealed, _ := c.Seal("secret-value")
	// 篡改密文尾部。
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := c.Open(tampered); err == nil {
		t.Fatal("篡改后的密文应解封失败")
	}
}

func TestOpenWrongKey(t *testing.T) {
	a, _ := newTestCipher(t).Seal("secret-value")
	other := make([]byte, keyLen)
	for i := range other {
		other[i] = byte(255 - i)
	}
	oc, err := NewCipher(other)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	if _, err := oc.Open(a); err == nil {
		t.Fatal("异密钥应解封失败")
	}
}

func TestOpenLegacyPlaintextPassthrough(t *testing.T) {
	c := newTestCipher(t)
	got, err := c.Open("历史明文")
	if err != nil || got != "历史明文" {
		t.Fatalf("未封装值应原样返回: got %q err %v", got, err)
	}
}

func TestNewCipherBadKeyLen(t *testing.T) {
	if _, err := NewCipher(make([]byte, 16)); err == nil {
		t.Fatal("短密钥应拒绝")
	}
}

func TestKeyFileCreateRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghostreaper.db.key")
	k1, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatalf("首建: %v", err)
	}
	if len(k1) != keyLen {
		t.Fatalf("密钥长度 %d", len(k1))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限 %o(须 0600)", fi.Mode().Perm())
	}
	k2, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatalf("重读: %v", err)
	}
	if string(k1) != string(k2) {
		t.Fatal("重读应得到同一密钥")
	}
}

func TestKeyFileBadLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeyFile(path); err == nil {
		t.Fatal("长度非法的密钥文件应报错,不得覆盖")
	}
}

func TestSensitiveSetting(t *testing.T) {
	yes := []string{
		"model.library.gpt-x", "model.library.中文配置",
		"search.backend", "search.brave.key", "search.proxy",
		"cred.c-1234", "mirror.ca.key",
	}
	no := []string{
		"model.active.primary", "model.keyview.gpt-x",
		"mirror.ca.cert", "auth.password", "auth.token.abc",
		"swarm.budget.max_turns", "guard.rule.1", "", "cred",
	}
	for _, k := range yes {
		if !SensitiveSetting(k) {
			t.Errorf("键 %q 应判敏感", k)
		}
	}
	for _, k := range no {
		if SensitiveSetting(k) {
			t.Errorf("键 %q 不应判敏感", k)
		}
	}
}

// ---- PBKDF2-HMAC-SHA256 标准测试向量(公开熟知向量) ----

func TestPBKDF2Vectors(t *testing.T) {
	cases := []struct {
		password, salt string
		iter           int
		want           string
	}{
		{"password", "salt", 1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(PBKDF2([]byte(c.password), []byte(c.salt), c.iter, 32))
		if got != c.want {
			t.Errorf("PBKDF2(%q,%q,%d) = %s, want %s", c.password, c.salt, c.iter, got, c.want)
		}
	}
}

func TestPasswordHashRoundtrip(t *testing.T) {
	h, err := NewPasswordHash("正确马电池桩")
	if err != nil {
		t.Fatalf("NewPasswordHash: %v", err)
	}
	if h.Algo != "pbkdf2-sha256" || h.Iters != passwordIters {
		t.Fatalf("散列记录参数异常: %+v", h)
	}
	if !h.Verify("正确马电池桩") {
		t.Fatal("正确密码应通过")
	}
	if h.Verify("错误密码") {
		t.Fatal("错误密码应拒绝")
	}
	// 盐随机:两次生成散列不同,但都通过校验。
	h2, _ := NewPasswordHash("正确马电池桩")
	if h.Salt == h2.Salt || h.Hash == h2.Hash {
		t.Fatal("盐未随机")
	}
	if !h2.Verify("正确马电池桩") {
		t.Fatal("第二次生成的散列也应通过")
	}
}

func TestPasswordHashAlgoGuard(t *testing.T) {
	h, _ := NewPasswordHash("some-password-8")
	h.Algo = "md5"
	if h.Verify("some-password-8") {
		t.Fatal("算法名不符应拒绝")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePasswordPolicy("12345678"); err != nil {
		t.Errorf("8 位密码应通过: %v", err)
	}
	if err := ValidatePasswordPolicy("1234567"); err == nil {
		t.Error("7 位密码应拒绝")
	}
}
