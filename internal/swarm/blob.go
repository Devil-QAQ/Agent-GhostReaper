package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BlobRefPrefix 内容寻址引用前缀(R18:账本只存引用)。
const BlobRefPrefix = "sha256:"

// BlobStore 内容寻址文件存储(第六章 §2 文件存储层):大对象(工具输出、
// 证据原文)按 sha256 命名落盘,天然去重、引用不可变——与年轮只追加
// 哲学同构。主库(事件账本)只存 "sha256:<hex>" 引用。
type BlobStore struct {
	root string // blobs/<前2字符>/<全hash>
}

// NewBlobStore 在 root 目录下建内容寻址存储。
func NewBlobStore(root string) *BlobStore { return &BlobStore{root: root} }

// Write 写入内容并返回引用("sha256:<hex>")。同内容重复写天然幂等
// (同 hash 覆盖同文件,内容不变)。
func (b *BlobStore) Write(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hex := hex.EncodeToString(sum[:])
	dir := filepath.Join(b.root, hex[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("swarm: 建 blob 目录: %w", err)
	}
	path := filepath.Join(dir, hex)
	if _, err := os.Stat(path); err == nil {
		return BlobRefPrefix + hex, nil // 已存在:内容寻址下同 hash 即同内容
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("swarm: 写 blob: %w", err)
	}
	return BlobRefPrefix + hex, nil
}

// Path 返回引用对应的文件路径(引用不存在时也返回拼出的路径,
// 调用方自行检查存在性)。
func (b *BlobStore) Path(ref string) (string, error) {
	if !strings.HasPrefix(ref, BlobRefPrefix) {
		return "", fmt.Errorf("swarm: 非法 blob 引用 %q", ref)
	}
	hex := strings.TrimPrefix(ref, BlobRefPrefix)
	if len(hex) != sha256.Size*2 {
		return "", fmt.Errorf("swarm: 非法 blob hash %q", ref)
	}
	return filepath.Join(b.root, hex[:2], hex), nil
}

// Exists 检查引用指向的内容是否存在(收割/审计校验用)。
func (b *BlobStore) Exists(ref string) bool {
	path, err := b.Path(ref)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Read 读回引用内容。
func (b *BlobStore) Read(ref string) ([]byte, error) {
	path, err := b.Path(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("swarm: 读 blob %s: %w", ref, err)
	}
	return data, nil
}

// blobThreshold 大段输出入 blob 的阈值:超过即整段落内容寻址文件,
// 账本与模型上下文只留截头 + 引用(R18)。
const blobThreshold = 4096

// spill 超阈值内容入 blob,返回 (引用, 截头)。未超阈值返回 ("", 全文)。
func (b *BlobStore) spill(s string) (ref, head string) {
	if len(s) <= blobThreshold {
		return "", s
	}
	head = s[:blobThreshold]
	r, err := b.Write([]byte(s))
	if err != nil {
		// blob 落盘失败不阻断执行记录:退化为只存截头(审计可查故障)。
		return "", head
	}
	return r, head
}
