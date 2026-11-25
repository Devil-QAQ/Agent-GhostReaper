package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// 真实二进制从不同工作目录启动,确认文件落点及原库/密钥续用。
func TestPortableDataDirectoryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过二进制级 E2E")
	}
	binary, err := os.ReadFile(buildBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"default", "relative", "absolute"} {
		t.Run(mode, func(t *testing.T) {
			deploy := filepath.Join(t.TempDir(), "程序 with spaces")
			if err := os.Mkdir(deploy, 0o755); err != nil {
				t.Fatal(err)
			}
			name := "ghostreaper"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			executable := filepath.Join(deploy, name)
			if err := os.WriteFile(executable, binary, 0o755); err != nil {
				t.Fatal(err)
			}
			db, tasks := filepath.Join(deploy, "ghostreaper.db"), filepath.Join(deploy, "tasks")
			var flags []string
			switch mode {
			case "relative":
				flags = []string{"-db", "data/custom.db", "-tasks", "work"}
				db, tasks = filepath.Join(deploy, "data", "custom.db"), filepath.Join(deploy, "work")
			case "absolute":
				root := t.TempDir()
				db, tasks = filepath.Join(root, "custom.db"), filepath.Join(root, "work")
				flags = []string{"-db", db, "-tasks", tasks}
			}
			var key []byte
			for generation := 0; generation < 2; generation++ {
				cwd, envDir := t.TempDir(), t.TempDir()
				addr := freeAddr(t)
				cmd := exec.Command(executable, append([]string{"-addr", addr}, flags...)...)
				cmd.Dir = cwd
				cmd.Env = append(os.Environ(), "HOME="+envDir, "USERPROFILE="+envDir,
					"TMPDIR="+envDir, "TEMP="+envDir, "TMP="+envDir,
					"GR_BASE_URL=", "GR_API_KEY=", "GR_MODEL=")
				cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
				defer stop()
				client := &http.Client{Timeout: time.Second}
				ready := false
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					resp, err := client.Get("http://" + addr + "/api/auth/status")
					if err == nil {
						_ = resp.Body.Close()
						ready = resp.StatusCode == http.StatusOK
						if ready {
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
				if !ready {
					t.Fatal("面板未就绪")
				}
				authInit(t, addr) // 第二次必须从同一账本登录。
				for _, path := range []string{db, db + ".key", db + "-wal", db + "-shm",
					filepath.Join(filepath.Dir(db), "logs", "ghostreaper.log"),
					filepath.Join(tasks, "skills", "humanizer", "SKILL.md")} {
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("产物缺失 %s: %v", path, err)
					}
				}
				currentKey, err := os.ReadFile(db + ".key")
				if err != nil {
					t.Fatal(err)
				}
				if generation > 0 && !bytes.Equal(key, currentKey) {
					t.Fatal("更换启动目录后密钥改变")
				}
				key = currentKey
				code, body := apiJSON(t, http.MethodGet, addr, "/api/diagnostics?content=db", nil)
				if code != http.StatusOK {
					t.Fatalf("导出诊断包: %d %s", code, body)
				}
				zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
				if err != nil {
					t.Fatal(err)
				}
				foundDB := false
				for _, file := range zr.File {
					foundDB = foundDB || file.Name == "db/"+filepath.Base(db)
				}
				if !foundDB {
					t.Fatal("诊断包缺数据库快照")
				}
				stop()
				for _, dir := range []string{cwd, envDir} {
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Fatalf("文件泄漏到 %s: %v (%v)", dir, entries, err)
					}
				}
				if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(db), "gr-diag-db-*")); err != nil || len(leftovers) != 0 {
					t.Fatalf("诊断快照未清理: %v (%v)", leftovers, err)
				}
			}
		})
	}
}
