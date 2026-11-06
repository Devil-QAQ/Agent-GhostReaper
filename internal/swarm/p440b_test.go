package swarm

// P4-40 续做 ②③:blob 写入永远完整重写(临时文件+原子改名)的并发
// 竞态自测,与无主 blob 按引用清除(Sweep)的单元验证。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestBlobWriteAtomicUnderConcurrentRemove 竞态自测(P4-40 续做 ④):
// 写线程持续 Write 同内容、删线程持续删目标文件(模拟特权清除并发)
// ——Write 全程成功,结束后文件内容恒完整(原子改名,无半截),
// 无 .tmp 残留。
func TestBlobWriteAtomicUnderConcurrentRemove(t *testing.T) {
	dir := t.TempDir()
	b := NewBlobStore(filepath.Join(dir, "blobs"))
	data := bytes.Repeat([]byte("orphan-sweep-proof"), 512) // 9KB 定值内容
	ref, err := b.Write(data)
	if err != nil {
		t.Fatalf("初次写入失败: %v", err)
	}
	path, err := b.Path(ref)
	if err != nil {
		t.Fatalf("引用路径: %v", err)
	}

	var wg sync.WaitGroup
	var writeErr error
	var mu sync.Mutex
	stop := make(chan struct{})
	// 写线程:持续完整重写。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := b.Write(data); err != nil {
				mu.Lock()
				writeErr = err
				mu.Unlock()
				return
			}
		}
	}()
	// 删线程:持续删目标文件(模拟清除竞态窗口)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(path)
		}
	}()
	// 跑足竞态窗口:读到的任一时刻内容必须完整(原子改名保证:要么旧
	// 文件要么新文件,永无半截);文件恰被删除的瞬间读失败可容忍。
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for range 300 {
		if got, rerr := b.Read(ref); rerr == nil && !bytes.Equal(got, data) {
			t.Fatalf("读到半截内容: %d 字节 != %d", len(got), len(data))
		}
	}
	close(stop)
	<-done
	if writeErr != nil {
		t.Fatalf("并发期间 Write 失败: %v", writeErr)
	}
	// 终态:再写一次,文件在且字节一致;无 .tmp 残留。
	if _, err := b.Write(data); err != nil {
		t.Fatalf("收尾写入失败: %v", err)
	}
	got, err := b.Read(ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("终态内容不符: %v (%d 字节)", err, len(got))
	}
	_ = filepath.Walk(filepath.Join(dir, "blobs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(info.Name(), ".tmp-") {
			t.Fatalf("临时文件残留: %s", p)
		}
		return nil
	})
}

// TestBlobSweepByReference 无主清除单元:引用归零才删、有引用保留、
// 项数与字节数如实、写途临时文件不动、仓不存在零值返回。
func TestBlobSweepByReference(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blobs")
	b := NewBlobStore(root)
	kept, err := b.Write([]byte("共享内容,仍被引用"))
	if err != nil {
		t.Fatalf("写共享 blob 失败: %v", err)
	}
	orphan1, err := b.Write([]byte("无主内容一"))
	if err != nil {
		t.Fatalf("写无主 blob 失败: %v", err)
	}
	orphan2, err := b.Write([]byte("无主内容二,稍长一些"))
	if err != nil {
		t.Fatalf("写无主 blob 失败: %v", err)
	}
	// 写途临时文件:手动放一个在分片目录(Sweep 不当作 blob 删)。
	keptHash := strings.TrimPrefix(kept, BlobRefPrefix)
	tmpPath := filepath.Join(root, keptHash[:2], keptHash+".tmp-fake")
	if err := os.WriteFile(tmpPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("放假临时文件失败: %v", err)
	}

	referenced := map[string]bool{keptHash: true}
	n, removedBytes, err := b.Sweep(referenced)
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删 2 项无主 blob,得 %d", n)
	}
	wantBytes := int64(len("无主内容一") + len("无主内容二,稍长一些"))
	if removedBytes != wantBytes {
		t.Fatalf("删除字节 %d != %d", removedBytes, wantBytes)
	}
	if !b.Exists(kept) {
		t.Fatalf("被引用 blob 不得删除")
	}
	for _, ref := range []string{orphan1, orphan2} {
		if b.Exists(ref) {
			t.Fatalf("无主 blob %s 应已删除", ref)
		}
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("写途临时文件不应被 Sweep 动: %v", err)
	}
	// 引用全集为空:连共享 blob 也成无主。
	n2, _, err := b.Sweep(map[string]bool{})
	if err != nil || n2 != 1 {
		t.Fatalf("空引用再扫应删剩余 1 项: %d %v", n2, err)
	}
	// 仓不存在:零值幂等。
	n3, b3, err := NewBlobStore(filepath.Join(t.TempDir(), "不存在的仓")).Sweep(map[string]bool{})
	if err != nil || n3 != 0 || b3 != 0 {
		t.Fatalf("空仓应返回零值: %d %d %v", n3, b3, err)
	}
}
