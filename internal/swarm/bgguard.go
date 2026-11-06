package swarm

// bgguard.go P4-37 后台任务失控防护(问题清单 #57 ③,防"死循环刷屏
// 60GB"重演):后台 bash 命令两道闸——输出 100MB 停写 + 挂钟 30 分钟
// 强杀。两闸参数为定值不做配置项;var 形态仅供测试注入缩时/缩量参数
// (运行期恒为定值,不上配置不上面板)。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 两闸定值(P4-37 拍板:100MB / 30 分钟)。
var (
	bgOutputCapBytes  int64         = 100 << 20        // ① 输出上限:写满即停写,尾部追加省略标记
	bgLifespanTimeout time.Duration = 30 * time.Minute // ② 寿命上限:挂钟到点强杀
)

// bgTruncMarker 输出截断标记(文件尾一行,人话)。
const bgTruncMarker = "输出超过上限，后面省略"

// capWriter 带上限的输出写入器(P4-37 ①):写满 limit 字节即停止续写,
// 尾部一次性追加省略标记,其后写入全部丢弃但返回成功(子进程不因管道
// 断裂误死——收命由寿命闸负责,输出闸只管文件不再长)。stdout/stderr
// 两路拷贝可能并发调用,内部加锁。
type capWriter struct {
	mu       sync.Mutex
	f        *os.File
	limit    int64
	written  int64
	capped   bool
	onCapped func() // 装配于转后台交接(纯前台路径不记);至多触发一次
	fired    bool
}

func newCapWriter(f *os.File, limit int64) *capWriter {
	return &capWriter{f: f, limit: limit}
}

// Write 达限前如实落盘;达限那次写入补齐到 limit、追加省略标记并转入
// 丢弃态;其后返回成功但不写文件。
func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.capped {
		w.mu.Unlock()
		return len(p), nil
	}
	if int64(len(p)) <= w.limit-w.written {
		n, err := w.f.Write(p)
		w.written += int64(n)
		w.mu.Unlock()
		return n, err
	}
	if room := w.limit - w.written; room > 0 {
		_, _ = w.f.Write(p[:room])
	}
	_, _ = fmt.Fprintf(w.f, "\n%s\n", bgTruncMarker)
	w.capped = true
	fire := w.onCapped != nil && !w.fired
	w.fired = true
	w.mu.Unlock()
	if fire {
		w.onCapped()
	}
	return len(p), nil
}

// armCapped 装配截断回记回调,并补发装配前已发生的截断(转后台时输出
// 可能已在超时等待的前台段被截断)。幂等,至多回记一次。
func (w *capWriter) armCapped(fn func()) {
	w.mu.Lock()
	w.onCapped = fn
	fire := w.capped && !w.fired
	if fire {
		w.fired = true
	}
	w.mu.Unlock()
	if fire {
		fn()
	}
}

// Truncated 是否已达上限截断(exited 通道关闭后读取,写入已停止)。
func (w *capWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.capped
}

// recordBGGuard 后台闸动作记黑匣子(截断/强杀各一类;记录用独立 ctx,
// 不随任务熔断断链——黑匣子侧约定)。
func (env *unitEnv) recordBGGuard(kind string, p *bashProc) {
	detail := map[string]any{"file": filepath.Base(p.outPath)}
	switch kind {
	case rings.BlackboxBGOutputTrunc:
		detail["limit_bytes"] = bgOutputCapBytes
	case rings.BlackboxBGLifespanKill:
		detail["lifespan"] = bgLifespanTimeout.String()
	}
	if err := blackbox.Record(context.Background(), env.Executor.Store, env.TaskID, "swarm:"+env.UnitID,
		rings.BlackboxRecord{Type: kind, StepID: env.StepID, UnitID: env.UnitID, Detail: detail}); err != nil {
		runlog.Errorf("黑匣子写入失败: 后台闸 %s %s/%s: %v", kind, env.TaskID, env.UnitID, err)
	}
}

// bgLifespanGuard P4-37 ② 寿命闸看门狗:挂钟(进程启动起算)到点强杀
// 整组,退出尾行与既有 [exit] 口径同款;kill 记黑匣子。自然退出则静默。
func (env *unitEnv) bgLifespanGuard(p *bashProc) {
	d := time.Until(p.started.Add(bgLifespanTimeout))
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.exited:
	case <-timer.C:
		select {
		case <-p.exited: // 与到点同刻完成:按完成计
		default:
			killBashGroup(p.cmd)
			env.recordBGGuard(rings.BlackboxBGLifespanKill, p)
			<-p.exited
		}
	}
}
