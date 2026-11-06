//go:build !windows

package runlog

// 标准错误接管(P4-47 C,问题清单 #65:崩溃现场不丢):fd 2 在描述级
// 重定向到运行日志活动文件——panic 堆栈与 runtime 致命错误(fatal
// error: 类)由 runtime 直写落进运行日志,崩溃前日志行与堆栈同文件
// 可对时序,随诊断包导出(logs/ 全目录收档,无需另改)。
//
// 机制:dup2(活动文件, 2)。两写者(runlog 的行写与 fd 2 的崩溃直写)
// 共享同一 O_APPEND 打开文件描述,内核保证每次 write 原子追加,行内
// 完整互不覆盖;这是确定性落盘——不经管道、不靠竞态窗口里的排空
// goroutine,进程怎么死堆栈都在文件里。
//
// 控制台镜像保持:接管先 dup 保留原始 fd 2,runlog 的 stderr 镜像换
// 到保留 fd——每条日志行照常同步到原控制台(stderr 既有行为保持:
// 守护脚本与前台可见);直写标准错误的输出(panic 堆栈等)归宿改为
// 运行日志文件,这正是"接管"的语义。原始标准错误若本就指向同一日志
// 文件(罕见的手工重定向形态),镜像自动关闭防行重复。
//
// 滚动重绑:rollIfDue 换新活动文件后,接管态下 fd 2 一并重绑到新文件
// (旧档已改名但描述仍有效,重绑间隙的崩溃直写落旧改名档,诊断包收
// 全目录不丢)。
//
// 失败降级:接管任一步失败即恢复原 fd 2、返回错误,调用方降级为
// 不接管(与日志开档失败同语义,仅标准错误);守护脚本退出码契约
// (P3-12)不涉。

import (
	"fmt"
	"os"
	"syscall"
)

// TakeoverStderr 接管进程标准错误进运行日志(须在 Open 成功后调用;
// l 为本进程活动日志器)。成功后:fd 2 = 活动日志文件(共享 O_APPEND
// 描述);runlog 的 stderr 镜像 = 保留的原始 fd 2。
func TakeoverStderr(l *Logger) error {
	if l == nil {
		return fmt.Errorf("runlog: 标准错误接管需要活动日志器")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return fmt.Errorf("runlog: 标准错误接管需要活动日志文件")
	}

	// 1) 保留原始标准错误(dup 出独立描述;失败不破坏现状)。
	dupFd, err := syscall.Dup(int(os.Stderr.Fd()))
	if err != nil {
		return fmt.Errorf("runlog: 保留原始标准错误: %w", err)
	}
	saved := os.NewFile(uintptr(dupFd), "stderr-original")

	// 2) fd 2 重定向到活动日志文件(共享打开文件描述,append 原子)。
	if err := syscall.Dup2(int(l.f.Fd()), 2); err != nil {
		_ = saved.Close()
		return fmt.Errorf("runlog: 重定向标准错误: %w", err)
	}
	l.stderrTaken = true

	// 3) runlog 镜像换到保留 fd;原始标准错误与活动文件同一 inode
	//    时(手工把 stderr 重定向到日志文件的形态)关镜像防行重复。
	if sameOpenFile(saved, l.f) {
		l.stderr = nil
		_ = saved.Close()
	} else {
		l.stderr = saved
	}
	l.emitLocked(LevelInfo, "标准错误已接管进运行日志")
	return nil
}

// rebindStderrFDLocked 滚动后把 fd 2 重绑到新活动文件(须持锁且接管态)。
func (l *Logger) rebindStderrFDLocked() {
	if l.f == nil || !l.stderrTaken {
		return
	}
	if err := syscall.Dup2(int(l.f.Fd()), 2); err != nil {
		// 重绑失败:fd 2 仍指向改名旧档,崩溃直写不丢(诊断包全收),
		// 如实留痕。
		l.emitLocked(LevelWarn, "标准错误重绑失败(仍指向滚动前旧档): "+err.Error())
	}
}

// sameOpenFile 两个打开文件是否同一 inode(手工重定向检测)。
func sameOpenFile(a, b *os.File) bool {
	sa, err := a.Stat()
	if err != nil {
		return false
	}
	sb, err := b.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}
