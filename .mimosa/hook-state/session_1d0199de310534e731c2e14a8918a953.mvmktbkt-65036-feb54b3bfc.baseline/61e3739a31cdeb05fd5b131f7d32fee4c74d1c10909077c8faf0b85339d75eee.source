package main

// P3-12 二进制自保的二进制级 E2E(跨平台部分):
// 退出码契约——真实二进制 + 损坏账本(坏配置)→ 启动失败退出码 78,
// 输出带失败原因——守护脚本据此区分"起不来"与运行期崩溃。
// 守护脚本回滚全流程为 Unix 专测,见 selfguard_unix_e2e_test.go;
// Windows 冒烟为编译级,结论见 P3 进度记录。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupFailureExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过二进制级 E2E")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(db, []byte("this is not a sqlite database\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(buildBinary(t), "-db", db, "-tasks", filepath.Join(dir, "tasks"))
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("预期非零退出,得到 err=%v\n%s", err, out)
	}
	if ee.ExitCode() != exitStartupFailed {
		t.Fatalf("坏配置启动失败退出码 = %d,预期 %d\n%s", ee.ExitCode(), exitStartupFailed, out)
	}
	if !strings.Contains(string(out), "打开年轮账本") {
		t.Fatalf("输出缺启动失败原因:\n%s", out)
	}
}
