//go:build !windows

package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// P4-47 C:标准错误接管——崩溃现场不丢。
// 验收口径"测试注入":runtime 崩溃输出(panic 堆栈 / fatal error)的
// 写出机制就是对 fd 2 的裸写,这里以 syscall.Write(2, …) 注入同型
// 字节流,验证接管后落进运行日志、与崩溃前日志行时序连贯。

func TestStderrTakeoverCapturesCrashWrites(t *testing.T) {
	dir := t.TempDir()
	// 保留原始 fd 2,测试收尾恢复(接管只在本测试内有效)。
	savedFD, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("保留原始 fd 2: %v", err)
	}
	defer func() { _ = syscall.Dup2(savedFD, 2) }()

	rl, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("开日志: %v", err)
	}
	defer rl.Close()
	if err := TakeoverStderr(rl); err != nil {
		t.Fatalf("接管: %v", err)
	}

	// 崩溃前的最后一条常规日志行。
	Infof("崩溃前最后一行(P4-47 注入)")
	// 注入崩溃直写:与 runtime fatalpanic 对 fd 2 的裸写同型。
	crash := "panic: boom-p47\n\ngoroutine 42 [running]:\ntest.fakeCrash()\n\t/p47/inject.go:1\n"
	if n, werr := syscall.Write(2, []byte(crash)); werr != nil || n != len(crash) {
		t.Fatalf("注入直写失败: n=%d err=%v", n, werr)
	}

	body, err := os.ReadFile(filepath.Join(dir, activeName))
	if err != nil {
		t.Fatalf("读运行日志: %v", err)
	}
	for _, want := range []string{
		"标准错误已接管进运行日志",
		"崩溃前最后一行(P4-47 注入)",
		"panic: boom-p47",
		"goroutine 42 [running]:",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("运行日志缺 %q;档内容:\n%s", want, body)
		}
	}
	i := strings.Index(string(body), "崩溃前最后一行")
	j := strings.Index(string(body), "panic: boom-p47")
	if i < 0 || j < 0 || i > j {
		t.Errorf("时序不连贯:崩溃前日志行应在堆栈之前(前置行 @%d,堆栈 @%d)", i, j)
	}
	// 行完整性:注入的多行堆栈整体在档(不被日志行写穿断)。
	if !strings.Contains(string(body), "test.fakeCrash()") {
		t.Errorf("堆栈帧不完整;档内容:\n%s", body)
	}
}

// TestTakeoverRequiresActiveFile 接管前置校验:无活动日志文件时报错
// (降级路径的守门:日志未开即不接管,仅标准错误)。
func TestTakeoverRequiresActiveFile(t *testing.T) {
	if err := TakeoverStderr(nil); err == nil {
		t.Errorf("nil 日志器应报错")
	}
	rl, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("开日志: %v", err)
	}
	rl.Close()
	if err := TakeoverStderr(rl); err == nil {
		t.Errorf("已关闭日志器应报错")
	}
}
