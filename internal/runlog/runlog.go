// Package runlog 是运行日志设施(P3-13b ③):logs/ 目录滚动文件
// (按大小 / 日期滚动,保留上限与天数清理),日志头自动带版本号 /
// 平台 / 启动配置,分级 DEBUG / INFO / WARN / ERROR(P4-32 起级别
// 固定全量 DEBUG——级别设置与端点已删,磁盘由滚动上限控制)。
//
// 红线:敏感信息零记录——key / 凭据 / 流量正文一律不进日志(日志会
// 随诊断包外发)。埋点侧纪律:不记任何设置值原文、工具调用明细不记
// (账本已有);本包另设一道兜底擦洗(常见 key 形态打码),双保险。
//
// 用法:进程启动早期 Open(失败不致命,降级为仅标准错误);各模块经
// 包级 Debugf/Infof/Warnf/Errorf 记录,未 Open 时为空操作(单测零依赖)。
package runlog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ghostreaper/internal/buildinfo"
)

// Level 日志级别(行内标签用;P4-32 起固定全量,不再可配)。
type Level int8

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String 级别呈现词(日志行内标签)。
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Options Open 的装配参数(零值可用,均为缺省)。
type Options struct {
	// MaxBytes 单文件大小滚动阈(按大小滚动;<=0 取缺省 10MB)。
	MaxBytes int64
	// MaxFiles 滚动文件保留个数上限(超出删最旧;<=0 取缺省 30)。
	MaxFiles int
	// MaxAge 滚动文件保留天数(按文件名日期判,超出删除;<=0 取缺省 14 天)。
	MaxAge time.Duration
	// StartupConfig 启动配置行(日志头呈现;调用方保证无敏感值)。
	StartupConfig []string
	// NoStderr 关闭标准错误同步输出(守护脚本从标准错误读启动期输出,
	// 缺省同步输出)。
	NoStderr bool
}

const (
	activeName    = "ghostreaper.log" // 活动文件名
	rolledPrefix  = "ghostreaper-"    // 滚动文件名前缀
	defaultMax    = 10 << 20          // 10MB
	defaultFiles  = 30
	defaultAge    = 14 * 24 * time.Hour
	minFreeBytes  = 1 << 30 // 磁盘剩余低于 1GB 记 WARN(每次滚动时检查)
	timeFormat    = "2006-01-02 15:04:05.000"
	rolledNameFmt = "20060102-150405"
)

// Logger 滚动文件日志器。并发安全;Write 实现 io.Writer(标准 log 库
// 重定向入口,行文按 INFO 落盘)。
type Logger struct {
	mu          sync.Mutex
	dir         string
	f           *os.File
	size        int64
	day         string // 活动文件开档日期(本地;跨日滚动)
	opts        Options
	stderr      io.Writer
	stderrTaken bool // 标准错误已接管进活动文件(P4-47 C;滚动后重绑)
	lowDisk     bool // 磁盘不足已告警(恢复静默,避免刷屏)
}

var std atomic.Pointer[Logger]

// procLevel 进程级日志级别:P4-32 起固定全量 DEBUG(级别设置已删),
// 启动即最详细,任何级别都不丢弃;保留 atomic 仅因并发读写需要。
var procLevel atomic.Int64

func init() { procLevel.Store(int64(LevelDebug)) }

// Open 打开运行日志(建目录、开活动文件、写日志头)。StartupConfig 行
// 会原样进入头部——调用方负责不含敏感值(红线)。
func Open(dir string, opts Options) (*Logger, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMax
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultFiles
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = defaultAge
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("runlog: 建日志目录: %w", err)
	}
	l := &Logger{dir: dir, opts: opts, stderr: os.Stderr}
	if opts.NoStderr {
		l.stderr = nil
	}
	if err := l.openActive(); err != nil {
		return nil, err
	}
	std.Store(l)
	return l, nil
}

// Close 关闭活动文件(此后包级调用降级为仅标准错误)。
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	if cur := std.Load(); cur == l {
		std.CompareAndSwap(l, nil)
	}
}

// Level 当前级别(固定 DEBUG,日志头呈现用)。
func (l *Logger) Level() Level { return Current() }

// Log 记一条(level 低于当前级别被丢弃;msg 经兜底擦洗)。
func (l *Logger) Log(lv Level, msg string) {
	if l == nil || lv < Current() {
		return
	}
	line := time.Now().Format(timeFormat) + " [" + lv.String() + "] " + scrub(msg)
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked(line)
}

// Debugf / Infof / Warnf / Errorf 实例级便捷入口。
func (l *Logger) Debugf(format string, args ...any) { l.Log(LevelDebug, fmt.Sprintf(format, args...)) }
func (l *Logger) Infof(format string, args ...any)  { l.Log(LevelInfo, fmt.Sprintf(format, args...)) }
func (l *Logger) Warnf(format string, args ...any)  { l.Log(LevelWarn, fmt.Sprintf(format, args...)) }
func (l *Logger) Errorf(format string, args ...any) { l.Log(LevelError, fmt.Sprintf(format, args...)) }

// writeLocked 落一行(须持锁):滚动检查 → 文件写 → 标准错误同步。
func (l *Logger) writeLocked(line string) {
	if l.f == nil {
		if l.stderr != nil {
			_, _ = io.WriteString(l.stderr, line)
		}
		return
	}
	l.rollIfDue(int64(len(line)))
	n, err := l.f.WriteString(line)
	l.size += int64(n)
	if err != nil {
		// 写失败不致命:转标准错误保底,文件句柄保留(下次滚动可能恢复)。
		if l.stderr != nil {
			fmt.Fprintf(l.stderr, "runlog: 写日志文件失败: %v\n", err)
		}
		return
	}
	if l.stderr != nil {
		_, _ = io.WriteString(l.stderr, line)
	}
}

// rollIfDue 滚动判定(须持锁):大小过阈或跨日 → 关当前、按时刻改名、
// 清理超限旧档、开新档写日志头。
func (l *Logger) rollIfDue(incoming int64) {
	now := time.Now()
	if l.size+incoming <= l.opts.MaxBytes && now.Format("2006-01-02") == l.day {
		return
	}
	name := l.f.Name()
	_ = l.f.Close()
	l.f = nil
	rolled := filepath.Join(l.dir, rolledPrefix+now.Format(rolledNameFmt)+".log")
	// 同秒二次滚动的极端情形:加序号防覆盖。
	if _, err := os.Stat(rolled); err == nil {
		for i := 1; ; i++ {
			alt := filepath.Join(l.dir,
				fmt.Sprintf("%s%s-%d.log", rolledPrefix, now.Format(rolledNameFmt), i))
			if _, err := os.Stat(alt); err != nil {
				rolled = alt
				break
			}
		}
	}
	if err := os.Rename(name, rolled); err != nil {
		// 改名失败(极端):活动文件续写,大小阈不再触发死循环(以重开告终)。
		l.emitLocked(LevelWarn, "日志滚动改名失败: "+err.Error())
	}
	l.pruneRolled(now)
	l.checkDiskLocked()
	if err := l.openActive(); err != nil {
		if l.stderr != nil {
			fmt.Fprintf(l.stderr, "runlog: 重开日志文件失败: %v\n", err)
		}
	}
	// 接管态下 fd 2 重绑到新活动文件(标准错误跟随滚动,P4-47 C)。
	l.rebindStderrFDLocked()
}

// pruneRolled 清理超限旧档(须持锁):文件名序即时间序;超出保留个数删
// 最旧,文件名日期早于保留窗口的删除。诊断包读 logs/ 全量目录,保留
// 上限即包内日志上限。
func (l *Logger) pruneRolled(now time.Time) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	type rolledFile struct {
		path string
		day  string
	}
	var files []rolledFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, rolledPrefix) || !strings.HasSuffix(n, ".log") || n == activeName {
			continue
		}
		files = append(files, rolledFile{
			path: filepath.Join(l.dir, n),
			day:  strings.TrimSuffix(strings.TrimPrefix(n, rolledPrefix), ".log"),
		})
	}
	cutoff := now.Add(-l.opts.MaxAge).Format("20060102")
	keep := l.opts.MaxFiles
	for i := len(files) - 1; i >= 0; i-- { // 自新向旧:超龄删除,超额删最旧
		f := files[i]
		if len(f.day) >= 8 && f.day[:8] < cutoff {
			_ = os.Remove(f.path)
			continue
		}
		if keep <= 0 {
			_ = os.Remove(f.path)
			continue
		}
		keep--
	}
}

// checkDiskLocked 磁盘剩余检查(须持锁,滚动时一并在场):剩余空间低于
// 阈值记 WARN 一次,恢复后静默重置(不刷屏)。
func (l *Logger) checkDiskLocked() {
	free, ok := diskFree(l.dir)
	if !ok {
		return
	}
	if free < minFreeBytes {
		if !l.lowDisk {
			l.lowDisk = true
			l.emitLocked(LevelWarn, fmt.Sprintf("磁盘空间不足:日志目录所在卷剩余 %d 字节(低于 %d 字节)", free, minFreeBytes))
		}
		return
	}
	l.lowDisk = false
}

// emitLocked 直接落一行(须持锁;绕过级别判定的内部告警用)。
func (l *Logger) emitLocked(lv Level, msg string) {
	line := time.Now().Format(timeFormat) + " [" + lv.String() + "] " + scrub(msg) + "\n"
	if l.f != nil {
		if n, err := l.f.WriteString(line); err == nil {
			l.size += int64(n)
		}
	}
	if l.stderr != nil {
		_, _ = io.WriteString(l.stderr, line)
	}
}

// openActive 开活动文件并写日志头(版本 / 平台 / 启动配置)。每次滚动
// 后的新档同头自述(任一日志文件单独拿出都可对出处)。
func (l *Logger) openActive() error {
	f, err := os.OpenFile(filepath.Join(l.dir, activeName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("runlog: 打开日志文件: %w", err)
	}
	l.f = f
	l.day = time.Now().Format("2006-01-02")
	st, err := f.Stat()
	if err == nil {
		l.size = st.Size()
	}
	var head strings.Builder
	head.WriteString(Header(buildinfo.Version, runtime.GOOS, runtime.GOARCH))
	for _, line := range l.opts.StartupConfig {
		head.WriteString(scrub(line) + "\n")
	}
	head.WriteString(fmt.Sprintf("级别: %s\n", l.Level().String()))
	n, _ := f.WriteString(head.String())
	l.size += int64(n)
	return nil
}

// Header 日志头的固定段(版本 / 平台 / Go 版本 / 时间)。纯函数抽出,
// 供跨平台头部正确性的直测。
func Header(version, goos, goarch string) string {
	return fmt.Sprintf("==== ghostreaper 运行日志 ====\n版本: %s\n平台: %s/%s\nGo: %s\n时间: %s\n",
		version, goos, goarch, runtime.Version(), time.Now().Format(timeFormat))
}

// Write io.Writer 入口(标准 log 库重定向;行文按 INFO 落)。
func (l *Logger) Write(p []byte) (int, error) {
	l.Log(LevelInfo, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// ---- 包级便捷入口(未 Open 时空操作;Open 后全进程共用) ----

func stdLogf(lv Level, format string, args ...any) {
	if cur := std.Load(); cur != nil && lv >= cur.Level() {
		cur.Log(lv, fmt.Sprintf(format, args...))
	}
}

// Debugf 调试级(P4-32 起级别固定全量,恒落盘)。
func Debugf(format string, args ...any) { stdLogf(LevelDebug, format, args...) }

// Infof 信息级(任务生命周期 / 单位起落 / 登录登出与配置修改等)。
func Infof(format string, args ...any) { stdLogf(LevelInfo, format, args...) }

// Warnf 警告级(模型重试与熔断 / 心跳巡检标记 / 配置读失败退默认 / 磁盘不足)。
func Warnf(format string, args ...any) { stdLogf(LevelWarn, format, args...) }

// Errorf 错误级(崩溃堆栈 / LLM 调用失败 / 工具执行异常 / 账本写入失败 /
// 面板 API 服务端错误 / 启动装配失败)。
func Errorf(format string, args ...any) { stdLogf(LevelError, format, args...) }

// Current 进程级当前级别(固定 DEBUG)。
func Current() Level { return Level(procLevel.Load()) }

// Dir 全局日志目录(未 Open 返回空串;诊断包据此收集)。
func Dir() string {
	if cur := std.Load(); cur != nil {
		cur.mu.Lock()
		defer cur.mu.Unlock()
		return cur.dir
	}
	return ""
}

// ---- 敏感信息兜底擦洗(红线双保险:埋点纪律之外的最后防线) ----

// scrubPatterns 常见敏感形态的擦洗表:sk- 开头的 key 形态、Bearer
// 凭据、api_key/password/token 类键值对取值。只做形态打码,不改变
// 其余内容。
var scrubPatterns = []struct {
	re   *regexp.Regexp
	mask string
}{
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`), "sk-****"},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`), "bearer ****"},
	{regexp.MustCompile(`(?i)(api[_-]?key|password|passwd|token|secret)["' ]*[:=]["' ]*[^\s"'&,]{4,}`), "$1=****"},
}

// scrub 应用擦洗表(逐条正则替换)。
func scrub(s string) string {
	if s == "" {
		return s
	}
	for _, p := range scrubPatterns {
		s = p.re.ReplaceAllString(s, p.mask)
	}
	return s
}
