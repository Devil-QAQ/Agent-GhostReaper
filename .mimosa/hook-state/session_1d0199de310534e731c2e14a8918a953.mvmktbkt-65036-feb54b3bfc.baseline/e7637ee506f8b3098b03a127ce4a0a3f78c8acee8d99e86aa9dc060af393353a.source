// password.go 面板密码的单向散列(P3-09):PBKDF2-HMAC-SHA256(标准库
// 手写,RFC 8018),盐随机、参数随记录持久——散列记录整体存键值表,
// 存储层不封装(单向散列非明文,不在敏感键清单;封装也不增加安全性)。
package secrets

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// 密码散列参数(记录内持久,升参只影响新记录)。
const (
	passwordAlgo    = "pbkdf2-sha256"
	passwordIters   = 600000 // OWASP 2023 对 PBKDF2-HMAC-SHA256 的建议量级
	passwordSaltLen = 16
	passwordKeyLen  = 32
)

// PasswordHash 密码散列记录(键值表 auth.password 的值)。
type PasswordHash struct {
	Algo      string `json:"algo"`
	Salt      string `json:"salt"` // hex
	Iters     int    `json:"iters"`
	Hash      string `json:"hash"` // hex
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// NewPasswordHash 由密码明文生成散列记录(随机盐)。
func NewPasswordHash(password string) (PasswordHash, error) {
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return PasswordHash{}, fmt.Errorf("secrets: 生成盐: %w", err)
	}
	dk := PBKDF2([]byte(password), salt, passwordIters, passwordKeyLen)
	return PasswordHash{
		Algo:  passwordAlgo,
		Salt:  hex.EncodeToString(salt),
		Iters: passwordIters,
		Hash:  hex.EncodeToString(dk),
	}, nil
}

// Verify 校验密码与散列记录是否相符。算法名不符即拒(不静默降级);
// 比较走常数时间(散列等长 hex,subtle 挡时序侧信道)。
func (h PasswordHash) Verify(password string) bool {
	if h.Algo != passwordAlgo || h.Iters <= 0 {
		return false
	}
	salt, err := hex.DecodeString(h.Salt)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(h.Hash)
	if err != nil {
		return false
	}
	got := PBKDF2([]byte(password), salt, h.Iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// PBKDF2 PBKDF2-HMAC-SHA256(RFC 8018;x/crypto/pbkdf2 的标准库手写,
// 避免为此引入新依赖)。
func PBKDF2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	blocks := (keyLen + hLen - 1) / hLen
	dk := make([]byte, 0, blocks*hLen)
	var be [4]byte
	u := make([]byte, hLen)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(be[:], uint32(block))
		prf.Write(be[:])
		dk = prf.Sum(dk)
		t := dk[len(dk)-hLen:]
		copy(u, t)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for x := range u {
				t[x] ^= u[x]
			}
		}
	}
	return dk[:keyLen]
}

// ValidatePasswordPolicy 密码策略(本机单用户的最小门槛):≥8 字符。
// 服务端为唯一权威,前端只做同款提示。
func ValidatePasswordPolicy(password string) error {
	if len([]rune(password)) < 8 {
		return errors.New("密码至少 8 位")
	}
	return nil
}
