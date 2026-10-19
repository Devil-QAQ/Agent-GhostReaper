//go:build !windows

package main

// P3-12 二进制自保的二进制级 E2E(Unix 部分,守护脚本是 sh):
//   ① 连续启动失败自动回滚:好版(真实二进制)落位 → upgrade 换上模拟
//      坏新版(对当前配置起不来的替身,恒 78)→ 守护脚本重试至阈值 →
//      自动回滚 ghostreaper.old → 旧版拉起成功、面板可访问。
//   ② 信号停止不拉起:对运行中的旧版发 SIGTERM(退出码 143)→ 守护
//      结束、不再拉起,端口不再服务。

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForPanel 轮询面板公开探活端点,返回是否在期限内就绪。
func waitForPanel(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/auth/status")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func TestDaemonRollbackE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过二进制级 E2E")
	}
	bin := buildBinary(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "ghostreaperd.sh")

	deploy := t.TempDir()
	cur := filepath.Join(deploy, "ghostreaper")
	old := cur + ".old"

	// 旧版 = 真实好二进制。
	good, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, good, 0o755); err != nil {
		t.Fatal(err)
	}
	// 坏新版 = 对当前配置起不来的替身(恒 78)。真实二进制的 78 契约由
	// TestStartupFailureExitCode 证明,这里只替"新版起不来"这一前提。
	badScript := "#!/bin/sh\necho '打开年轮账本: file is not a database (模拟坏新版)'\nexit 78\n"
	bad := filepath.Join(deploy, "bad-new")
	if err := os.WriteFile(bad, []byte(badScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// upgrade:旧版应备份到 .old,新版落位。
	up := exec.Command("/bin/sh", script, "upgrade", bad)
	up.Env = append(os.Environ(), "GR_BIN="+cur)
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("upgrade 失败: %v\n%s", err, out)
	}
	oldB, err := os.ReadFile(old)
	if err != nil || !bytes.Equal(oldB, good) {
		t.Fatalf(".old 应为旧版备份: err=%v", err)
	}
	curB, _ := os.ReadFile(cur)
	if !bytes.Equal(curB, []byte(badScript)) {
		t.Fatalf("upgrade 后应为新版: %s", cur)
	}

	// 守护启动:重试至阈值 → 回滚 → 好版拉起。
	addr := freeAddr(t)
	logPath := filepath.Join(deploy, "daemon.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	d := exec.Command("/bin/sh", script, "start", "--",
		"-addr", addr, "-db", filepath.Join(deploy, "gr.db"), "-tasks", filepath.Join(deploy, "tasks"))
	d.Env = append(os.Environ(), "GR_BIN="+cur)
	d.Stdout = logf
	d.Stderr = logf
	d.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组,收尾整组终止防孤儿
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	killGroup := func() {
		_ = syscall.Kill(-d.Process.Pid, syscall.SIGKILL)
	}
	defer killGroup()

	if !waitForPanel(addr, 30*time.Second) {
		logBytes, _ := os.ReadFile(logPath)
		t.Fatalf("回滚后旧版未在期限内就绪\n守护日志:\n%s", logBytes)
	}
	logBytes, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logBytes), "连续 3 次启动失败,回滚旧版") {
		t.Fatalf("守护日志缺回滚记录:\n%s", logBytes)
	}
	curB, _ = os.ReadFile(cur)
	if !bytes.Equal(curB, good) {
		t.Fatal("回滚后二进制未恢复为旧版")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal(".old 应已回滚消费")
	}

	// 信号停止语义:终止守护正在跑的旧版(SIGTERM → 143)→ 守护结束、
	// 不再拉起。
	childOut, err := exec.Command("pgrep", "-P", strconv.Itoa(d.Process.Pid)).Output()
	if err != nil {
		t.Fatalf("找守护的子进程失败: %v", err)
	}
	for _, pidStr := range strings.Fields(string(childOut)) {
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		killGroup()
		t.Fatal("信号停止后守护未结束(仍在拉起?)")
	}
	if st := d.ProcessState; st == nil || st.ExitCode() != 0 {
		t.Fatalf("守护应干净收场,退出码 = %v", st)
	}
	logBytes, _ = os.ReadFile(logPath)
	if !strings.Contains(string(logBytes), "退出码 143(主动退出),守护结束") {
		t.Fatalf("守护日志缺信号停止记录:\n%s", logBytes)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/api/auth/status"); err != nil {
			break // 连接拒绝 = 无复活实例
		} else {
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	if resp, err := http.Get("http://" + addr + "/api/auth/status"); err == nil {
		_ = resp.Body.Close()
		killGroup()
		t.Fatal("守护结束后端口仍在服务(存在被拉起的复活实例)")
	}
}
