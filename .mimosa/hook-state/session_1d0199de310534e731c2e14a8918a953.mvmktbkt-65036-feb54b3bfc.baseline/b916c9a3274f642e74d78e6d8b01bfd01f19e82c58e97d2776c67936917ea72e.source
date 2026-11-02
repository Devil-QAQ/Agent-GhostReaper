package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 打开并收集全局日志器(测试隔离:结束即复位)。
func openForTest(t *testing.T, dir string, opts Options) *Logger {
	t.Helper()
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func readActive(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, activeName))
	if err != nil {
		t.Fatalf("读活动日志: %v", err)
	}
	return string(b)
}

func listRolled(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("列日志目录: %v", err)
	}
	var out []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), rolledPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestOpenWritesHeader(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{StartupConfig: []string{"监听: 127.0.0.1:28282", "账本: x.db"}, NoStderr: true})
	l.Infof("hello")

	got := readActive(t, dir)
	for _, want := range []string{
		"==== ghostreaper 运行日志 ====",
		"版本: ",
		"平台: ",
		"Go: ",
		"监听: 127.0.0.1:28282",
		"[INFO] hello",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("日志头缺 %q:\n%s", want, got)
		}
	}
}

func TestHeaderCrossPlatform(t *testing.T) {
	// 另一平台产物的日志头正确性:头部固定段按传入平台参数拼装(产物
	// 内运行时取 runtime.GOOS/GOARCH,同一函数)。
	h := Header("1.2.3", "linux", "amd64")
	for _, want := range []string{"版本: 1.2.3", "平台: linux/amd64"} {
		if !strings.Contains(h, want) {
			t.Fatalf("linux 头缺 %q:\n%s", want, h)
		}
	}
	h = Header("1.2.3", "windows", "amd64")
	if !strings.Contains(h, "平台: windows/amd64") {
		t.Fatalf("windows 头错误:\n%s", h)
	}
}

func TestLevelFilterAndRuntimeSwitch(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{NoStderr: true})
	l.Infof("info-可见")
	l.Debugf("debug-应被丢弃")
	l.SetLevel(LevelDebug)
	l.Debugf("debug-开关后可见")
	l.SetLevel(LevelError)
	l.Warnf("warn-降级后应被丢弃")
	t.Cleanup(func() { SetLevel(LevelInfo) }) // 级别是进程级状态,测完复位

	got := readActive(t, dir)
	for _, want := range []string{"info-可见", "debug-开关后可见"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"debug-应被丢弃", "warn-降级后应被丢弃"} {
		if strings.Contains(got, banned) {
			t.Fatalf("被过滤的记录出现在文件: %q", banned)
		}
	}
}

func TestLevelParse(t *testing.T) {
	cases := map[string]Level{
		"debug": LevelDebug, "INFO": LevelInfo, " warn": LevelWarn,
		"error": LevelError, "": LevelInfo, "bogus": LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRotationBySize(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{MaxBytes: 512, NoStderr: true})
	l.Infof("%s", strings.Repeat("x", 400))
	l.Infof("%s", strings.Repeat("y", 400)) // 触发大小滚动

	// 小阈值下首写即可能滚动(日志头本身占体积),只保证:至少一个
	// 旧档、旧档内容仍在、新档带日志头(每档自述)。
	if rolled := listRolled(t, dir); len(rolled) < 1 {
		t.Fatalf("按大小滚动后应有旧档,得到 %v", rolled)
	}
	if got := readActive(t, dir); !strings.Contains(got, "==== ghostreaper 运行日志 ====") {
		t.Fatalf("滚动后的新档缺日志头:\n%s", got)
	}
	if !strings.Contains(readRolledFirst(t, dir), "xxxx") {
		t.Fatal("旧档内容丢失")
	}
}

func readRolledFirst(t *testing.T, dir string) string {
	t.Helper()
	for _, n := range listRolled(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("读旧档: %v", err)
		}
		return string(b)
	}
	return ""
}

func TestRotationByDate(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{NoStderr: true})
	// 伪造跨日:直接把活动文件的开档日期改到昨天,下一次写入触发按日滚动。
	l.mu.Lock()
	l.day = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	l.mu.Unlock()
	l.Infof("跨日写入")

	if rolled := listRolled(t, dir); len(rolled) != 1 {
		t.Fatalf("按日期滚动后应有一个旧档,得到 %v", rolled)
	}
	if got := readActive(t, dir); !strings.Contains(got, "跨日写入") {
		t.Fatalf("新档缺跨日内容:\n%s", got)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{MaxBytes: 128, MaxFiles: 2, NoStderr: true})
	for i := 0; i < 6; i++ {
		l.Infof("%s", strings.Repeat("z", 120)) // 每条都触发滚动
	}
	if got := len(listRolled(t, dir)); got > 2 {
		t.Fatalf("旧档保留超限: %d 个", got)
	}
}

func TestScrubMasksKeyShapes(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{NoStderr: true})
	l.Errorf("LLM 调用失败: 401 sk-abcdef1234567890 bearer ABCDEFGHIJKLMNOP")
	l.Infof("配置 api_key=\"supersecret999\" password=hunter2value")

	got := readActive(t, dir)
	for _, banned := range []string{
		"sk-abcdef1234567890", "ABCDEFGHIJKLMNOP", "supersecret999", "hunter2value",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("敏感形态未打码: %q\n%s", banned, got)
		}
	}
	for _, want := range []string{"sk-****", "bearer ****", "api_key=****", "password=****"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺打码形态 %q\n%s", want, got)
		}
	}
}

func TestStdLogRedirect(t *testing.T) {
	dir := t.TempDir()
	l := openForTest(t, dir, Options{NoStderr: true})
	_, _ = l.Write([]byte("mirror: 交换 ex-1 入账失败: boom\n"))
	if got := readActive(t, dir); !strings.Contains(got, "[INFO] mirror: 交换 ex-1 入账失败: boom") {
		t.Fatalf("标准 log 重定向行缺失:\n%s", got)
	}
}

func TestPackageLevelNilSafe(t *testing.T) {
	// 未 Open 时包级调用零恐慌(单测环境常态)。
	if cur := std.Load(); cur != nil {
		cur.Close()
	}
	Debugf("noop")
	Infof("noop")
	Warnf("noop")
	Errorf("noop")
	// 级别是进程级状态:未 Open 也可调(面板保存即改);测完复位 info。
	SetLevel(LevelError)
	if Current() != LevelError {
		t.Fatal("SetLevel 未生效")
	}
	SetLevel(LevelInfo)
	if Dir() != "" {
		t.Fatal("未 Open 时 Dir 应为空")
	}
}
