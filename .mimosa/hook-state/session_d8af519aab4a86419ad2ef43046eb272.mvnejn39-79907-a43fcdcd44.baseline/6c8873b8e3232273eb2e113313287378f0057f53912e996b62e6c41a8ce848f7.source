// Package secrets 承载静态加密(P3-09,第七章 §5「Key 安全:静态加密随
// P3 凭据管理落地」)的主密钥与对称封装:密钥落库旁独立文件(0600),
// 值以 AES-256-GCM 封装后入库——拿到数据库文件拿不到密钥文件时,敏感
// 值是不可读密文。纯标准库实现,零新依赖。
//
// 封装格式:"v1:" + base64(nonce[12] || AES-GCM 密文)。v1 前缀让密文
// 自描述(未带前缀的历史明文可识别,迁移与兼容判定都靠它)。
//
// 密钥文件不可丢:丢失后已封装的值不可解(加密的意义所在),启动时
// 读不出现有密钥文件属致命错误,如实失败。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// keyLen 主密钥长度(AES-256)。
const keyLen = 32

// sealPrefix 封装值前缀(版本自描述)。
const sealPrefix = "v1:"

// LoadOrCreateKeyFile 载入(或首用时创建)主密钥文件:32 字节随机,
// 0600 权限、O_EXCL 创建(已存在即读,不覆盖)。文件与库同目录由调用方
// 决定(惯例:<dbPath>.key)。
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != keyLen {
			return nil, fmt.Errorf("secrets: 密钥文件 %s 长度 %d 非法(须 %d 字节)", path, len(b), keyLen)
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("secrets: 读密钥文件 %s: %w", path, err)
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: 生成主密钥: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) { // 并发首创建:读回胜者
			return LoadOrCreateKeyFile(path)
		}
		return nil, fmt.Errorf("secrets: 建密钥文件 %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(key); err != nil {
		return nil, fmt.Errorf("secrets: 写密钥文件 %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("secrets: 同步密钥文件 %s: %w", path, err)
	}
	return key, nil
}

// Cipher 对称封装器(AES-256-GCM)。零值不可用;并发安全(GCM 每次
// Seal 现造随机 nonce,无内部状态竞争)。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 由主密钥构建;密钥长度非法即错。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("secrets: 主密钥长度 %d 非法(须 %d 字节)", len(key), keyLen)
	}
	a, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: 构造 AES: %w", err)
	}
	g, err := cipher.NewGCM(a)
	if err != nil {
		return nil, fmt.Errorf("secrets: 构造 GCM: %w", err)
	}
	return &Cipher{aead: g}, nil
}

// Seal 封装明文(每次随机 nonce,同明文两次封装密文不同)。
func (c *Cipher) Seal(plain string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secrets: 生成 nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plain), nil)
	return sealPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open 解封;值未带 v1 前缀(历史明文)原样返回——读路径不因迁移时序
// 拒绝服务,迁移(EnableAtRest)负责把明文补封装。
func (c *Cipher) Open(sealed string) (string, error) {
	if !IsSealed(sealed) {
		return sealed, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return "", fmt.Errorf("secrets: 密文 base64 解码失败: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns+c.aead.Overhead() {
		return "", errors.New("secrets: 密文长度不足(损坏或密钥不符)")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("secrets: 解封失败(密钥不符或密文被改): %w", err)
	}
	return string(plain), nil
}

// IsSealed 值是否已是封装形态(v1 前缀)。
func IsSealed(v string) bool {
	return strings.HasPrefix(v, sealPrefix)
}

// ---- 敏感键判定(存储层加密的作用域) ----
//
// 键值表里的敏感值:模型库配置(含 api_key)、搜索配置(key/代理)、
// 凭据库(整条含秘密)、镜 CA 私钥。判定按前缀/全键精确给出——新增
// 敏感键空间时必须同步此处,漏判即明文落库。

// sensitivePrefixes 前缀命中的键空间。
var sensitivePrefixes = []string{
	"model.library.", // 模型库配置(JSON 含 api_key)
	"search.",        // 搜索配置(key 与代理凭据)
	"cred.",          // 目标凭据库(整条含秘密)
}

// sensitiveExact 全键精确命中。
var sensitiveExact = map[string]bool{
	"mirror.ca.key": true, // 镜 CA 私钥(P3-01)
}

// SensitiveSetting 键是否属敏感值(存储层据此封装/解封)。
func SensitiveSetting(key string) bool {
	if sensitiveExact[key] {
		return true
	}
	for _, p := range sensitivePrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
