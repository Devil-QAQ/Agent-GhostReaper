package swarm

// P3-05 bash 限时放开测试:模型自调 timeout(默认 120 秒封顶 600 秒)、
// >60 秒命令不掐断、超时自动转后台、显式后台、sleep 不转后台、取消即杀、
// 单位落地整组回收、完成提醒、单发路径不转后台、后台流量经镜留痕。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
)

// bgEnv 直调 bash 工具的单位环境。
func bgEnv(t *testing.T) *unitEnv {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "bg-t", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	return env
}

// waitFor / resultText 沿用包内既有助手(gate_test.go / toolrules_test.go)。

// TestBashTimeoutOf 限时解析:缺省/非法取默认,超上限压到 600 秒。
func TestBashTimeoutOf(t *testing.T) {
	cases := []struct {
		ms   int
		want time.Duration
	}{
		{0, 120 * time.Second},
		{-5, 120 * time.Second},
		{1, time.Millisecond},
		{120000, 120 * time.Second},
		{300000, 300 * time.Second},
		{600000, 600 * time.Second},
		{600001, 600 * time.Second},
		{999999999, 600 * time.Second},
	}
	for _, c := range cases {
		if got := bashTimeoutOf(c.ms); got != c.want {
			t.Fatalf("bashTimeoutOf(%d) = %s, want %s", c.ms, got, c.want)
		}
	}
}

// TestBashAutoBackgroundOnTimeout 完成标准核心:>限时命令不掐断——超时
// 自动转后台,进程继续跑完,输出落文件并以 [exit: N] 尾行收尾;完成提醒
// 恰好送达一次。
func TestBashAutoBackgroundOnTimeout(t *testing.T) {
	env := bgEnv(t)
	ctx := context.Background()
	start := time.Now()
	res, err := env.bashTool().Call(ctx, json.RawMessage(
		`{"command":"echo bg-start; sleep 2; echo bg-tail","timeout_ms":700}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "已转后台") || !strings.Contains(text, "bg-1.output") {
		t.Fatalf("超时应转后台(返回文本含文件名): isErr=%v text=%q", res.IsError, text)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("转后台应在前台限期内返回,实际耗时 %s", elapsed)
	}
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	outPath := filepath.Join(dir, "bg-1.output")
	waitFor(t, 8*time.Second, "后台命令跑完(输出文件出现尾行)", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), "bg-tail") && strings.Contains(string(b), "[exit: 0]")
	})
	out, _ := os.ReadFile(outPath)
	if !strings.Contains(string(out), "bg-start") {
		t.Fatalf("后台输出文件应含超时前已产出的内容: %q", string(out))
	}
	// 完成提醒:恰好一次。
	notes := env.Executor.bgNotes(env.UnitID)
	if len(notes) != 1 || !strings.Contains(notes[0], "bg-1.output") || !strings.Contains(notes[0], "[exit: 0]") {
		t.Fatalf("完成提醒应恰一条并带退出尾行: %q", notes)
	}
	if again := env.Executor.bgNotes(env.UnitID); len(again) != 0 {
		t.Fatalf("完成提醒应恰好一次,第二次应为空: %q", again)
	}
}

// TestBashOver60SecondsNotKilled 完成标准核心:>60 秒命令默认限时内
// 跑完不掐断(旧版 60 秒硬超时已解开;默认 120 秒)。
func TestBashOver60SecondsNotKilled(t *testing.T) {
	env := bgEnv(t)
	start := time.Now()
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 65; echo survived-p305"}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "survived-p305") {
		t.Fatalf("65 秒命令应完整跑完不被掐断: isErr=%v text=%q", res.IsError, text)
	}
	if elapsed := time.Since(start); elapsed < 65*time.Second {
		t.Fatalf("命令确需运行 65 秒,实际 %s 即返回(测试失效)", elapsed)
	}
	// 前台完成不留 bg 文件。
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if ents, err := os.ReadDir(dir); err == nil && len(ents) != 0 {
		t.Fatalf("前台完成不应残留输出文件: %v", ents)
	}
}

// TestBashExplicitBackground 显式后台:立即返回,输出与尾行落文件。
func TestBashExplicitBackground(t *testing.T) {
	env := bgEnv(t)
	start := time.Now()
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 1; echo explicit-bg-data","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	if res.IsError || !strings.Contains(resultText(res), "bg-1.output") {
		t.Fatalf("显式后台应立即返回并给出输出文件: %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("显式后台应立即返回,实际 %s", elapsed)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 8*time.Second, "显式后台命令跑完", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), "explicit-bg-data") && strings.Contains(string(b), "[exit: 0]")
	})
}

// TestBashSleepNotAutoBackgrounded sleep 类等待命令不转后台(与引擎
// 同款):超时即杀、如实报错,不产出后台文件。
func TestBashSleepNotAutoBackgrounded(t *testing.T) {
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 3","timeout_ms":300}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "命令超时") || !strings.Contains(text, "已终止") {
		t.Fatalf("sleep 超时应即杀并如实报错: isErr=%v text=%q", res.IsError, text)
	}
	if !strings.Contains(text, "timeout_ms") {
		t.Fatalf("超时报错应指路 timeout_ms: %q", text)
	}
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if ents, err := os.ReadDir(dir); err == nil && len(ents) != 0 {
		t.Fatalf("超时即杀不应残留输出文件: %v", ents)
	}
	if notes := env.Executor.bgNotes(env.UnitID); len(notes) != 0 {
		t.Fatalf("未转后台不应有完成提醒: %q", notes)
	}
}

// TestBashCancelKillsProcessGroup ctx 取消(段收拢/单位硬停)杀整组
// 进程:管道子孙同组同死,调用即回,输出文件清理。
func TestBashCancelKillsProcessGroup(t *testing.T) {
	env := bgEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan tool.Result, 1)
	go func() {
		res, err := env.bashTool().Call(ctx, json.RawMessage(
			`{"command":"sleep 30 | cat; echo unreachable"}`), nil)
		if err != nil {
			t.Errorf("调用异常: %v", err)
		}
		done <- res
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		text := resultText(res)
		if !strings.Contains(text, "[exit:") && !res.IsError {
			t.Fatalf("被中断的命令应带退出注记或错误: %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ctx 取消后应即回(杀进程组),5 秒未归")
	}
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if ents, err := os.ReadDir(dir); err == nil && len(ents) != 0 {
		t.Fatalf("取消路径不应残留输出文件: %v", ents)
	}
}

// TestBashReapUnitBG 单位落地整组回收:在跑的后台进程被杀,登记注销,
// 输出文件保留(审计与读取)。
func TestBashReapUnitBG(t *testing.T) {
	env := bgEnv(t)
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 30","run_in_background":true}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	env.Executor.reapUnitBG(env.UnitID)
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 5*time.Second, "回收杀死后台进程(登记表已注销,输出文件保留)", func() bool {
		if _, err := os.Stat(outPath); err != nil {
			return false
		}
		env.Executor.bgMu.Lock()
		_, registered := env.Executor.bgRuns[env.UnitID]
		env.Executor.bgMu.Unlock()
		return !registered
	})
	// 进程确实死了:登记表空 + 文件存在(保留)。
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("回收后输出文件应保留: %v", err)
	}
}

// TestBashOneShotNoBackground 单发路径(研判后放行):run_in_background
// 不生效,超时即杀不转后台。
func TestBashOneShotNoBackground(t *testing.T) {
	env := bgEnv(t)
	env.oneShot = true
	// 显式后台被忽略 → 前台执行完成。
	res, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"echo oneshot-fg","run_in_background":true}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	if res.IsError || !strings.Contains(resultText(res), "oneshot-fg") {
		t.Fatalf("单发路径 run_in_background 应被忽略、照常前台执行: %+v", res)
	}
	// 超时不转后台。
	res2, err := env.bashTool().Call(context.Background(), json.RawMessage(
		`{"command":"sleep 3","timeout_ms":300}`), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	if !res2.IsError || !strings.Contains(resultText(res2), "已终止") {
		t.Fatalf("单发路径超时应即杀: %+v", res2)
	}
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if ents, err := os.ReadDir(dir); err == nil && len(ents) != 0 {
		t.Fatalf("单发路径不应留下后台文件: %v", ents)
	}
}

// TestBashBackgroundThroughMirror 后台子进程与前台同一构造:镜代理
// 环境注入照旧——后台 curl 流量经镜留痕可回放。
func TestBashBackgroundThroughMirror(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "bg-mirror-t", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	env.Executor.Mirror = mirror.New(store, env.Executor.Blobs, t.TempDir())
	// 单位镜会话(与 drive 装配同款):bash 子进程的代理环境来自会话。
	sess, err := env.Executor.Mirror.Session(mirror.Meta{
		TaskID: env.TaskID, StepID: env.StepID,
		UnitID: env.UnitID, TranscriptID: env.TranscriptID,
	})
	if err != nil {
		t.Fatalf("起镜会话失败: %v", err)
	}
	defer sess.Close()
	env.mirror = sess
	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()

	res, err := env.bashTool().Call(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"command":"sleep 1; curl -s %s/robots.txt","run_in_background":true}`, target.MainURL())), nil)
	if err != nil || res.IsError {
		t.Fatalf("显式后台启动失败: %v %+v", err, res)
	}
	outPath := filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), "bg-1.output")
	waitFor(t, 10*time.Second, "后台 curl 跑完", func() bool {
		b, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(b), "[exit: 0]")
	})
	// 镜留痕:后台 curl 恰 1 条,可回放。
	recs := mirrorRecordsOf(t, store, env.TaskID)
	if len(recs) != 1 {
		t.Fatalf("后台 curl 应恰 1 条镜留痕,得到 %d", len(recs))
	}
	if recs[0].Type != rings.MirrorRecHTTP || recs[0].BlobRef == "" {
		t.Fatalf("后台流量应全量留痕(带 blob 引用): %+v", recs[0])
	}
}

// TestBashBGScriptedE2E 引擎全链路(脚本模型):新参数(timeout_ms/
// run_in_background)过真实工具管线,后台输出经 read 读回,完成提醒在
// 下一工具调用边界注入,账本 tool_call 留痕齐。
func TestBashBGScriptedE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "bg-e2e"
	seedGraph(t, store, task, "长命令后台执行与读回验证")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", "已在后台启动"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "bash",
				Args: `{"command":"sleep 1; echo e2e-bg-payload","run_in_background":true}`,
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "read", "e2e-bg-payload"):
			// 模拟模型"思考耗时",让后台命令先跑完(去竞态)再读回。
			time.Sleep(1500 * time.Millisecond)
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "read",
				Args: `{"path":"bg-1.output"}`,
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图执行完成:后台命令输出已读回。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := NewExecutor(store, prov, root, Budget{})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取任务包失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("执行结果不符: %+v", res)
	}
	recs := swarmRecords(t, store, task)
	var bashArgs, readHead string
	for _, r := range recs {
		if r.Type != rings.SwarmRecToolCall {
			continue
		}
		switch r.ToolName {
		case "bash":
			bashArgs = string(r.ToolArgs)
		case "read":
			readHead = r.ResultHead
		}
	}
	if !strings.Contains(bashArgs, "run_in_background") {
		t.Fatalf("bash 调用参数应含 run_in_background: %q", bashArgs)
	}
	if !strings.Contains(readHead, "e2e-bg-payload") {
		t.Fatalf("read 读回应含后台输出内容: %q", readHead)
	}
	// 完成提醒进了会话(transcript 可查):脚本模型第三轮看到即收口,
	// 这里以提醒已送达登记表为证(notified 已置位、第二次为空)。
	if notes := ex.bgNotes(res.UnitID); len(notes) != 0 {
		t.Fatalf("单位已落地,提醒应在会话期内送达完毕(补取应为空): %q", notes)
	}
}
