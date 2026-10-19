//go:build windows

package swarm

import "os/exec"

// setBashProcGroup Windows 无进程组:仅标记,回收退化为杀 shell 本体
// (管道子孙进程不随组死,受限项待 P3-12 跨平台阶段如实处理)。
func setBashProcGroup(*exec.Cmd) {}

// killBashGroup Windows 退化实现:杀 shell 进程本体。
func killBashGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
