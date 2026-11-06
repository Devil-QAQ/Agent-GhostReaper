package swarm

// P4-37 后台任务失控防护测试:输出 100MB 上限(停写+省略标记+截断状态
// 回传+黑匣子)、寿命 30 分钟强杀(缩时参数主验,kill 留痕+[exit] 同款
// 尾行)、正常后台任务回归无损。前台超时语义回归由 bash_bg_test.go 既有
// 用例覆盖(TestBashTimeoutOf / TestBashSleepNotAutoBackgrounded /
// TestBashCancelKillsProcessGroup)。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setBGGuards 测试内注入缩时/缩量参数,用毕还原。
func setBGGuards(t *testing.T, capBytes int64, lifespan time.Duration) {
	t.Helper()
	oldCap, oldLife := bgOutputCapBytes, bgLifespanTimeout
	bgOutputCapBytes, bgLifespanTimeout = capBytes, lifespan
	t.Cleanup(func() { bgOutputCapBytes, bgLifespanTimeout = oldCap, oldLife })
}

// TestBashBGOutputCapTruncated 完成标准①(真实 100MB 量级):死循环刷
// 输出命令后台跑——文件停在 100MB 上限、尾部省略标记在、标记先于退出
// 尾行;bash(tail) 读回含标记;完成提醒含截断状态;黑匣子有截断记录。
func TestBashBGOutputCapTruncated(t *testing.T) {
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"yes P437-FLOOD","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	limit := int64(100 << 20)
	// 停写判定:文件出现省略标记且大小稳定在上限附近(不再增长)。
	var sizeAtMarker int64
	waitFor(t, 60*time.Second, "输出写满 100MB 触发停写(省略标记出现)", func() bool {
		b, err := os.ReadFile(outPath)
		if err != nil {
			return false
		}
		if !strings.Contains(string(b), bgTruncMarker) {
			return false
		}
		sizeAtMarker = int64(len(b))
		return sizeAtMarker >= limit
	})
	// 再等 1 秒确认文件不再增长(丢弃态生效)。
	time.Sleep(time.Second)
	after, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("读输出文件失败: %v", err)
	}
	if int64(len(after)) != sizeAtMarker {
		t.Fatalf("停写后文件不应继续增长: 标记时 %d 字节,1 秒后 %d 字节", sizeAtMarker, len(after))
	}
	if int64(len(after)) > limit+128 {
		t.Fatalf("文件应停在上限附近(100MB+标记行),实际 %d 字节", len(after))
	}
	content := string(after)
	if !strings.Contains(content, bgTruncMarker) {
		t.Fatalf("文件尾应有省略标记")
	}
	// 进程仍在跑(输出闸不收命,寿命闸负责)。
	env.Executor.bgMu.Lock()
	runs := env.Executor.bgRuns[env.UnitID]
	env.Executor.bgMu.Unlock()
	if len(runs) != 1 || runs[0].Finished() {
		t.Fatalf("输出截断不应杀死进程(寿命闸负责收命),登记 %d 条", len(runs))
	}
	// 黑匣子:截断记录在(bg_output_trunc,带文件名与上限值)。
	var truncRec *int
	for i, rec := range blackboxRecsOf(t, env.Executor.Store, env.TaskID) {
		if rec.Type == "bg_output_trunc" {
			truncRec = &i
		}
	}
	if truncRec == nil {
		t.Fatalf("截断动作应记黑匣子(bg_output_trunc)")
	}
	// 工具读回截断状态:bash(tail) 看到省略标记(模型实际读回路径)。
	tailRes, err := env.bashTool().Call(context.Background(), json.RawMessage(
		fmt.Sprintf(`{"command":"tail -c 120 %s"}`, filepath.Base(outPath))), nil)
	if err != nil || tailRes.IsError {
		t.Fatalf("tail 读回失败: %v %+v", err, tailRes)
	}
	if !strings.Contains(resultText(tailRes), bgTruncMarker) {
		t.Fatalf("tail 读回应含省略标记: %q", resultText(tailRes))
	}
	// 完成提醒含截断状态:杀掉进程(不注销登记,同单位落地前的硬停语义),
	// 退出后提醒送达,话术带截断事实与 [exit] 尾行。
	killBashGroup(runs[0].proc.cmd)
	<-runs[0].proc.exited
	waitFor(t, 5*time.Second, "退出尾行落盘", func() bool {
		b, _ := os.ReadFile(outPath)
		return strings.Contains(string(b), "[exit:")
	})
	final, _ := os.ReadFile(outPath)
	if m, e := strings.Index(string(final), bgTruncMarker), strings.Index(string(final), "[exit:"); m < 0 || e < 0 || m > e {
		t.Fatalf("省略标记应在退出尾行之前: marker@%d exit@%d", m, e)
	}
	notes := env.Executor.bgNotes(env.UnitID)
	if len(notes) != 1 || !strings.Contains(notes[0], "截断") || !strings.Contains(notes[0], "[exit:") {
		t.Fatalf("完成提醒应恰一条并含截断状态与退出尾行: %q", notes)
	}
	env.Executor.reapUnitBG(env.UnitID)
}

// TestBashBGOutputCapSmallScale 机制缩量验:上限缩到 64KB,语义同上
// (停写、标记、黑匣子),快速回归闸门行为。
func TestBashBGOutputCapSmallScale(t *testing.T) {
	setBGGuards(t, 64<<10, 30*time.Minute)
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"yes SMALL-FLOOD","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 10*time.Second, "缩量上限触发停写", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), bgTruncMarker)
	})
	b, _ := os.ReadFile(outPath)
	if int64(len(b)) > 64<<10+128 {
		t.Fatalf("缩量闸应停在 64KB 附近,实际 %d 字节", len(b))
	}
	if !strings.Contains(string(b), bgTruncMarker) {
		t.Fatalf("缩量闸同样应有省略标记")
	}
	found := false
	for _, rec := range blackboxRecsOf(t, env.Executor.Store, env.TaskID) {
		if rec.Type == "bg_output_trunc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缩量闸截断同样记黑匣子")
	}
	env.Executor.reapUnitBG(env.UnitID)
}

// TestBashBGLifespanKill 完成标准②(缩时参数主验):空转后台任务寿命到
// 点被强杀——[exit] 同款尾行(非 0 退出)、kill 记黑匣子、无产出落盘。
func TestBashBGLifespanKill(t *testing.T) {
	setBGGuards(t, 100<<20, 1500*time.Millisecond)
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 300; echo never-reached","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 10*time.Second, "寿命到点强杀(退出尾行落盘)", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), "[exit:")
	})
	b, _ := os.ReadFile(outPath)
	text := string(b)
	if strings.Contains(text, "[exit: 0]") || strings.Contains(text, "never-reached") {
		t.Fatalf("寿命强杀不应是正常退出: %q", text)
	}
	if !strings.Contains(text, "[exit: -1]") {
		t.Fatalf("强杀尾行应为信号终止形态(-1): %q", text)
	}
	// kill 留痕:黑匣子 bg_lifespan_kill(带文件名与寿命值)。
	found := false
	for _, rec := range blackboxRecsOf(t, env.Executor.Store, env.TaskID) {
		if rec.Type == "bg_lifespan_kill" {
			found = true
			if rec.Detail["file"] != "bg-1.output" {
				t.Fatalf("强杀记录应带输出文件名: %+v", rec.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("寿命强杀应记黑匣子(bg_lifespan_kill)")
	}
	// 完成提醒照常送达(留痕可查的会话内通道)。
	notes := env.Executor.bgNotes(env.UnitID)
	if len(notes) != 1 || !strings.Contains(notes[0], "[exit: -1]") {
		t.Fatalf("强杀后完成提醒应送达并带终止尾行: %q", notes)
	}
}

// TestBashBGNormalRunUnharmed 完成标准③回归:正常后台任务(MB 级输出、
// 秒级完成)在两闸下无损——输出完整、正常 [exit: 0]、无省略标记、
// 无任何黑匣子闸记录。
func TestBashBGNormalRunUnharmed(t *testing.T) {
	setBGGuards(t, 2<<20, 5*time.Second) // 闸收紧到比产物更小一档仍须无损
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"seq 1 200000","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 15*time.Second, "正常后台命令跑完", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), "[exit: 0]")
	})
	b, _ := os.ReadFile(outPath)
	text := string(b)
	if strings.Contains(text, bgTruncMarker) {
		t.Fatalf("未达上限不应有省略标记")
	}
	if !strings.Contains(text, "200000") {
		t.Fatalf("输出应完整到末行(200000)")
	}
	for _, rec := range blackboxRecsOf(t, env.Executor.Store, env.TaskID) {
		if rec.Type == "bg_output_trunc" || rec.Type == "bg_lifespan_kill" {
			t.Fatalf("正常后台任务不应触发闸记录: %+v", rec)
		}
	}
	notes := env.Executor.bgNotes(env.UnitID)
	if len(notes) != 1 || strings.Contains(notes[0], "截断") {
		t.Fatalf("正常完成提醒不应带截断话术: %q", notes)
	}
}
