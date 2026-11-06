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

// Write 写入内容并返回引用("sha256:<hex>")。永远完整重写(P4-40 续做
// ②):临时文件 + 同目录原子改名——不做"存在即跳过",特权清除删无主
// blob 与在跑任务写同内容 blob 并发时,跳过分支会把"刚被删除的文件"
// 当作"内容已在"而漏写,引用悬空;同 hash 即同内容,重写幂等,原子
// 改名保证读者见到的文件恒完整(无半截)。
func (b *BlobStore) Write(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hex := hex.EncodeToString(sum[:])
	dir := filepath.Join(b.root, hex[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("swarm: 建 blob 目录: %w", err)
	}
	path := filepath.Join(dir, hex)
	tmp, err := os.CreateTemp(dir, hex+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("swarm: 建 blob 临时文件: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("swarm: 写 blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("swarm: 关 blob 临时文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("swarm: blob 原子改名: %w", err)
	}
	return BlobRefPrefix + hex, nil
}

// Sweep 清除无主 blob(P4-40 续做 ①):遍历仓内现存文件,凡 hash 不在
// referenced(剩余任务事件负载引用的全集)即删除——引用归零才删,有
// 任何剩余任务引用的一律保留。返回删除项数与字节总数(运行日志留痕
// 用)。仓目录不存在返回零值(全新部署无 blob 可清)。分片目录与写途
// 临时文件(.tmp-*)不在此路径:前者至多 256 个空目录无负担,后者由
// Write 的失败路径自清——删它们会与并发写者竞争临时文件。
func (b *BlobStore) Sweep(referenced map[string]bool) (int, int64, error) {
	entries, err := os.ReadDir(b.root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("swarm: 读 blob 根目录: %w", err)
	}
	var removed int
	var removedBytes int64
	for _, shard := range entries {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(b.root, shard.Name()))
		if err != nil {
			return 0, 0, fmt.Errorf("swarm: 读 blob 分片 %s: %w", shard.Name(), err)
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			// 写途临时文件不是正式 blob,不当作无主内容删(见注释)。
			if strings.Contains(f.Name(), ".tmp-") {
				continue
			}
			if referenced[f.Name()] {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue // 元信息读不出:不动它,如实留仓
			}
			if err := os.Remove(filepath.Join(b.root, shard.Name(), f.Name())); err != nil {
				continue // 删失败不中断整轮:余项照清,失败项留仓
			}
			removed++
			removedBytes += info.Size()
		}
	}
	return removed, removedBytes, nil
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
