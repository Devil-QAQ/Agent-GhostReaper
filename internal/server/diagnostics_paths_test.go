package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticsSnapshotLocalDirectory(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	root := t.TempDir()
	srv.Config.System.DBPath = filepath.Join(root, "rings.db")
	// 无效临时目录确保成功快照确实不依赖系统临时目录。
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		t.Setenv(key, filepath.Join(root, "nonexistent"))
	}
	path, err := srv.diagnosticsDBSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(path))
	if filepath.Dir(filepath.Dir(path)) != root {
		t.Fatalf("快照未留在账本目录: %s", path)
	}
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	// 正式导出入口成功后和快照创建失败后都不留下临时目录。
	files, _, err := getZipWithHeader(t, mux, "/api/diagnostics?content=db")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["db/rings.db"]; !ok {
		t.Fatalf("导出缺数据库: %v", keysOf(files))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := srv.diagnosticsDBSnapshot(ctx); err == nil {
		t.Fatal("已取消请求未返回错误")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("遗留诊断临时文件: %v (%v)", entries, err)
	}
}
