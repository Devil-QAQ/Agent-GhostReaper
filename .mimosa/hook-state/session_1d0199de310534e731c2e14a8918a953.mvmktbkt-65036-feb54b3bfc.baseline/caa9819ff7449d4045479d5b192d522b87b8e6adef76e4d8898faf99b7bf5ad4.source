//go:build !windows

package swarm

import (
	"os/exec"
	"syscall"
)

// setBashProcGroup bash 子进程归独立进程组:命令的管道子孙同组,
// 回收(超时即杀/单位落地整组回收)时一击杀全,不漏孙子进程。
func setBashProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killBashGroup 杀整个进程组(进程已退出时 Kill 对负 pid 返回 ESRCH,
// 忽略即可)。
func killBashGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
