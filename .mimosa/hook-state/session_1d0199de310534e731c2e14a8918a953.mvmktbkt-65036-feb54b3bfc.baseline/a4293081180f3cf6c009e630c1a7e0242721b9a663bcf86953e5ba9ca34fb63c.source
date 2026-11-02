package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P2-04:预算三闸全套 + token 独立表 + 黑匣子 + 收尾线推导 ----

// TestTaskDeadlineDerivation 收尾线推导纯函数:未设超时零值;锚点+秒数;
// unitMaxDuration 与运行预算取小、过线收敛 1 秒。
func TestTaskDeadlineDerivation(t *testing.T) {
	if got := taskDeadlineOf(0, time.Now()); !got.IsZero() {
		t.Fatalf("未设超时应为零值,实际 %v", got)
	}
	if got := taskDeadlineOf(600, time.Time{}); !got.IsZero() {
		t.Fatalf("缺锚点应为零值,实际 %v", got)
	}
	anchor := time.Now().Add(-30 * time.Second)
	dl := taskDeadlineOf(60, anchor)
	if want := anchor.Add(60 * time.Second); !dl.Equal(want) {
		t.Fatalf("收尾线 = 锚点+超时: %v, want %v", dl, want)
	}

	// unitMaxDuration:deadline 零值原样;远线取运行预算;近线取剩余;
	// 过线收敛 1 秒(不放任无限跑)。P4-01:运行预算不限(0)时收尾线
	// 就是唯一上限——总闸不被不限预算绕过。
	if got := unitMaxDuration(10*time.Second, time.Time{}); got != 10*time.Second {
		t.Fatalf("无收尾线应原样: %v", got)
	}
	if got := unitMaxDuration(0, time.Time{}); got != 0 {
		t.Fatalf("双侧不限应为 0: %v", got)
	}
	if got := unitMaxDuration(10*time.Second, time.Now().Add(time.Hour)); got != 10*time.Second {
		t.Fatalf("远线应取运行预算: %v", got)
	}
	if got := unitMaxDuration(10*time.Second, time.Now().Add(3*time.Second)); got > 3*time.Second {
		t.Fatalf("近线应取剩余: %v", got)
	}
	if got := unitMaxDuration(10*time.Second, time.Now().Add(-time.Hour)); got != time.Second {
		t.Fatalf("过线应收敛 1 秒: %v", got)
	}
	if got := unitMaxDuration(0, time.Now().Add(3*time.Second)); got == 0 || got > 3*time.Second {
		t.Fatalf("不限预算+近线应收尾线封顶: %v", got)
	}
	if got := unitMaxDuration(0, time.Now().Add(-time.Hour)); got != time.Second {
		t.Fatalf("不限预算+过线应收敛 1 秒: %v", got)
	}
}

// TestTaskGateFromLedger TaskGate 从事件流锚点读总闸:created 事件
// occurred_at + detail.timeout_seconds;后续生命周期事件(自带 detail,
// 会覆盖投影)不影响锚点——恢复后续跑收尾线不漂移。
func TestTaskGateFromLedger(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "gate-task"

	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		OccurredAt: created,
		Payload: rings.TaskLifecycle{Action: "created", Title: "t",
			Detail: map[string]any{"timeout_seconds": 3600}},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	// 后续事件自带 detail(会整体替换 tasks 投影 detail)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "stopped", Detail: map[string]any{"reason": "x"}},
	}); err != nil {
		t.Fatalf("停任务失败: %v", err)
	}

	secs, at, err := store.TaskGate(ctx, task)
	if err != nil {
		t.Fatalf("TaskGate 失败: %v", err)
	}
	if secs != 3600 || !at.Equal(created) {
		t.Fatalf("锚点应为 created 事件: secs=%d at=%v want at=%v", secs, at, created)
	}
	// 无超时任务。
	if _, err := store.Append(ctx, "gate-task-2", rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "t2"},
	}); err != nil {
		t.Fatalf("建任务 2 失败: %v", err)
	}
	secs2, _, err := store.TaskGate(ctx, "gate-task-2")
	if err != nil || secs2 != 0 {
		t.Fatalf("无超时任务应返回 0: secs=%d err=%v", secs2, err)
	}
}

// TestRunSecondsBudgetRenewsInPlace 墙钟预算(run_seconds)到线(P4-01 ②):
// 引擎就地打断进收尾(Settlement 写回候选)→ 不落地,**原会话续跑**(每段
// 预算逐段重算)→ 续跑段完成剩余工作收口 done。转录单文件连续(到线
// 收尾与续跑指令同在一条会话轨迹里,换人税为零)。
func TestRunSecondsBudgetRenewsInPlace(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "runsec-task"

	host, port := seedTarget(t, "r\r\n")
	seedTaskGate(t, store, task, 0) // 无总闸(created 恒为任务首条事件)
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			if !seenToolResult(rr, "submit_candidate_fact", "已提交待确认事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "w1", Name: "submit_candidate_fact",
					Args: `{"statement":"墙钟预算内实测端口开放","evidence":"port_probe 实测 state=open","asset_id":"target"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "墙钟耗尽,发现已写回。", Finish: "stop"}
		}
		if strings.Contains(rr.LastUser, "预算 · 续跑") {
			return scriptedReply{Text: "续跑段确认端口结论,本步骤完成。", Finish: "stop"}
		}
		// 常规轮:发起挂死工具(sleep 30s 远超 1s 墙钟)。
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "bash",
			Args: `{"command":"sleep 30 && echo done"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 20, RunSeconds: 1, WrapupMaxTurns: 2})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "completed" || res.StepStatus != "done" {
		t.Fatalf("到线续跑后应完成而非切段: %+v", res)
	}
	pg, _ := store.ProgressGraph(ctx, task)
	if len(pg.Facts) != 1 || pg.Steps[0].Status != "done" {
		t.Fatalf("收尾应写回候选且 step 落 done: facts=%+v steps=%+v", pg.Facts, pg.Steps)
	}
	// 黑匣子:预算到线续跑留痕(unit_budget_renew),无 exhausted 落地。
	var renew, exhausted int
	for _, rec := range blackboxRecsOf(t, store, task) {
		switch rec.Type {
		case rings.BlackboxBudgetRenewal:
			renew++
			if rec.StepID != "s1" {
				t.Fatalf("续跑留痕应锚定 s1: %+v", rec)
			}
		case rings.BlackboxUnitExhausted:
			exhausted++
		}
	}
	if renew < 1 || exhausted != 0 {
		t.Fatalf("应留痕预算续跑且零 exhausted 落地: renew=%d exhausted=%d", renew, exhausted)
	}
	// 转录连续可证:同一文件先含收尾指令再含续跑指令(单会话)。
	raw := readTranscript(t, filepath.Join(dir, "tasks"), task, res.TranscriptID)
	if !strings.Contains(raw, "运行预算即将耗尽") || !strings.Contains(raw, "预算 · 续跑") {
		t.Fatalf("转录应同文件含收尾与续跑指令(原会话连续)")
	}
	// 时间账:跨过原 1 秒线仍在跑(billable 超线,续跑段照计)。
	var finished rings.SwarmRecord
	for _, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionEnd {
			finished = rec
		}
	}
	if !strings.Contains(finished.Note, "billable=") {
		t.Fatalf("session_finished 应带时间账: %s", finished.Note)
	}
}

// seedTaskGate 给任务铺 created 事件(带 timeout_seconds;0 = 不设)。
func seedTaskGate(t *testing.T, store *rings.Store, task string, timeoutSeconds int) {
	t.Helper()
	detail := map[string]any{}
	if timeoutSeconds > 0 {
		detail["timeout_seconds"] = timeoutSeconds
	}
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: task, Detail: detail},
	}); err != nil {
		t.Fatalf("铺 created 失败: %v", err)
	}
}

// blackboxRecsOf 取任务内全部黑匣子记录。
func blackboxRecsOf(t *testing.T, store *rings.Store, task string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码黑匣子记录: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// readTranscript 读会话转录 JSONL 原文(连续性断言用)。
func readTranscript(t *testing.T, root, task, tid string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, task, "transcripts", tid+".jsonl"))
	if err != nil {
		t.Fatalf("读转录 %s: %v", tid, err)
	}
	return string(b)
}

// TestWrapupMaxTurnsBounds 收尾轮数预算(wrapup_max_turns)生效:收尾轮
// 里的模型还想继续调工具,收尾自身轮数到顶即收场——引擎按收尾预算截停;
// P4-01 ② 后到线收住转入原会话续跑(续跑段收口 done),收尾轮数不因
// 续跑失守(每段各自的收尾都受同一上限约束)。
func TestWrapupMaxTurnsBounds(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "wrapup-task"

	host, port := seedTarget(t, "w\r\n")
	seedTaskGate(t, store, task, 0)
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		// 收尾轮:永远还想再调一次工具(模拟收尾跑飞)。
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "w" + strconv.Itoa(rr.Index), Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		if strings.Contains(rr.LastUser, "预算 · 续跑") {
			return scriptedReply{Text: "续跑段收口,本步骤完成。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 2, RunSeconds: 120, WrapupMaxTurns: 1})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "completed" || res.StepStatus != "done" {
		t.Fatalf("收尾被截停后应原会话续跑收口: %+v", res)
	}
	// 主轮 2 + 收尾 1 + 续跑 1 = 至多 5 次模型调用;收尾轮数不失控。
	if model.requestCount() > 5 {
		t.Fatalf("收尾轮数失控: 模型调用 %d 次", model.requestCount())
	}
	var renew int
	for _, rec := range blackboxRecsOf(t, store, task) {
		if rec.Type == rings.BlackboxBudgetRenewal {
			renew++
		}
	}
	if renew < 1 {
		t.Fatalf("到线应留痕预算续跑,实际 %d", renew)
	}
}

// TestUnitTokenUsageAndBlackbox 四类记录分工落位(单位侧):逐次用量入
// token 独立表(一行=一次模型调用,增量与脚本 usage 对账)、预算过半与
// 到线续跑入黑匣子(blackbox.record 事件流)——P4-01 ② 后到线不落地,
// 留痕形态是 unit_budget_renew(原会话续跑),token 行连续归因同一转录。
func TestUnitTokenUsageAndBlackbox(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "token-task"

	host, port := seedTarget(t, "t\r\n")
	seedTaskGate(t, store, task, 0)
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			return scriptedReply{Text: "预算耗尽,无未写回发现。", Finish: "stop"}
		}
		if strings.Contains(rr.LastUser, "预算 · 续跑") {
			return scriptedReply{Text: "续跑段收口,本步骤完成。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 4, RunSeconds: 120, WrapupMaxTurns: 1})
	ex.Model = "scripted-swarm-model"

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "completed" || res.StepStatus != "done" {
		t.Fatalf("到线续跑后应完成: %+v", res)
	}

	// token 独立表:每轮一行,总量对账(脚本按请求体规模回报
	// prompt_tokens=BodyChars/4、completion≈文本/4+8;行值须与对应
	// 请求的上报逐行相符,不再是无信息的常数)。
	rows, err := store.TokenUsageRows(ctx, task)
	if err != nil {
		t.Fatalf("查 token 表失败: %v", err)
	}
	reqs := model.requests()
	if len(rows) == 0 || len(rows) != len(reqs) {
		t.Fatalf("token 行数应等于模型调用次数: rows=%d calls=%d", len(rows), len(reqs))
	}
	var inSum, outSum int
	for i, r := range rows {
		if r.Turn != i+1 || r.Agent != "swarm:u1" || r.Model != "scripted-swarm-model" {
			t.Fatalf("第 %d 行归属/轮次不符: %+v", i, r)
		}
		if r.InputTokens != reqs[i].BodyChars/4 {
			t.Fatalf("第 %d 行输入用量与脚本上报不符: row=%+v 请求体 %d 字符", i, r, reqs[i].BodyChars)
		}
		if r.TranscriptID != res.TranscriptID {
			t.Fatalf("第 %d 行应归因原会话 %s: %+v", i, res.TranscriptID, r)
		}
		inSum += r.InputTokens
		outSum += r.OutputTokens
	}
	expectIn := 0
	for _, rr := range reqs {
		expectIn += rr.BodyChars / 4
	}
	if inSum != expectIn || outSum == 0 {
		t.Fatalf("用量对账失败: in=%d(期望 %d) out=%d rows=%d", inSum, expectIn, outSum, len(rows))
	}

	// 黑匣子:过半里程碑恰一次 + 到线续跑留痕恰一次,零 exhausted 落地。
	evs, _ := store.Events(ctx, task, 0, 0)
	half, renew, exhausted := 0, 0, 0
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("解码黑匣子记录: %v", err)
		}
		switch rec.Type {
		case rings.BlackboxBudgetHalf:
			half++
			if rec.StepID != "s1" || rec.UnitID != "u1" {
				t.Fatalf("里程碑应锚定 s1/u1: %+v", rec)
			}
		case rings.BlackboxBudgetRenewal:
			renew++
		case rings.BlackboxUnitExhausted:
			exhausted++
		default:
			t.Fatalf("本测试不应出现其他黑匣子类型: %+v", rec)
		}
	}
	if half != 1 {
		t.Fatalf("预算过半里程碑应恰一次,实际 %d", half)
	}
	if renew != 1 {
		t.Fatalf("到线续跑应留痕一次,实际 %d", renew)
	}
	if exhausted != 0 {
		t.Fatalf("原会话续跑下不应有 exhausted 落地,实际 %d", exhausted)
	}
}

// TestDefaultBudgetUnlimited 默认预算 0=不限(P4-01 ①):零值装配(无
// 全局覆盖)下,复合意图跑满超过原默认 30 轮(40 轮)不切段——单一
// 会话完成,零预算黑匣子事件(无过半/续跑/落地);墙钟维度同机制直证
// (引擎收到 MaxDuration=0=off,不设线)。
func TestDefaultBudgetUnlimited(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "unlimited-task"

	host, port := seedTarget(t, "u\r\n")
	seedTaskGate(t, store, task, 0)
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	const turns = 40 // 超过旧默认 30 轮
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if rr.Index >= turns {
			return scriptedReply{Text: "40 轮探测完成,本步骤收口。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Budget.MaxTurns != 0 || pack.Budget.RunSeconds != 0 {
		t.Fatalf("默认领取应为 0=不限: %+v", pack.Budget)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "completed" || res.StepStatus != "done" {
		t.Fatalf("默认不限应跑满 40 轮完成不切段: %+v", res)
	}
	if n := model.requestCount(); n < turns {
		t.Fatalf("应至少 %d 轮(超过旧默认 30 轮),实际 %d", turns, n)
	}
	var budgetEvents int
	for _, rec := range blackboxRecsOf(t, store, task) {
		switch rec.Type {
		case rings.BlackboxBudgetHalf, rings.BlackboxBudgetRenewal, rings.BlackboxUnitExhausted:
			budgetEvents++
		}
	}
	if budgetEvents != 0 {
		t.Fatalf("不限预算下不应有任何预算事件,实际 %d", budgetEvents)
	}
}

// TestTaskGateStopsDespiteUnlimitedBudget 任务总闸到点仍硬收(P4-01 ③):
// 预算不限(0)时收尾线照样封顶单位墙钟——到点 Settlement 收尾写回、
// 落 exhausted(不续跑),黑匣子零续跑留痕。
func TestTaskGateStopsDespiteUnlimitedBudget(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "gate-unlimited-task"

	host, port := seedTarget(t, "g\r\n")
	seedTaskGate(t, store, task, 2) // 总闸 2 秒
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			if !seenToolResult(rr, "submit_candidate_fact", "已提交待确认事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "w1", Name: "submit_candidate_fact",
					Args: `{"statement":"总闸收尾前实测端口开放","evidence":"port_probe 实测 state=open","asset_id":"target"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "总闸收尾,发现已写回。", Finish: "stop"}
		}
		// 不识别续跑指令:硬收路径根本不该发出它。
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "bash",
			Args: `{"command":"sleep 30"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{}) // 不限

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.TaskDeadline.IsZero() {
		t.Fatalf("总闸 2 秒应推导出收尾线")
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "timeout" || res.StepStatus != "exhausted" {
		t.Fatalf("总闸到点应硬收 exhausted: %+v", res)
	}
	pg, _ := store.ProgressGraph(ctx, task)
	if len(pg.Facts) != 1 || pg.Steps[0].Status != "exhausted" {
		t.Fatalf("硬收应写回候选且 step 落 exhausted: facts=%+v steps=%+v", pg.Facts, pg.Steps)
	}
	for _, rec := range blackboxRecsOf(t, store, task) {
		if rec.Type == rings.BlackboxBudgetRenewal {
			t.Fatalf("总闸到点不得续跑: %+v", rec)
		}
	}
}

// TestCancelRunningUnitsHardStops 宽限硬停的机制件:在飞单位挂死
// (工具 sleep 30s),CancelRunningUnits 后真中断(aborted,非 exhausted),
// 终态照常入账(审计闭环)。
func TestCancelRunningUnitsHardStops(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hardstop-task"

	host, port := seedTarget(t, "h\r\n")
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "p" + strconv.Itoa(rr.Index), Name: "bash",
			Args: `{"command":"sleep 30"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 20, RunSeconds: 120, WrapupMaxTurns: 1})

	done := make(chan struct{})
	var res *RunResult
	go func() {
		defer close(done)
		pack, err := ex.Claim(ctx, task, "s1")
		if err != nil {
			t.Errorf("领取失败: %v", err)
			return
		}
		r, err := ex.Run(ctx, pack)
		if err != nil {
			t.Errorf("执行失败: %v", err)
			return
		}
		res = r
	}()

	// 等单位起飞(工具挂死中),然后硬停。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n := countRunningTools(t, store, task); n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ids := ex.CancelRunningUnits()
	if len(ids) != 1 {
		t.Fatalf("应硬停恰 1 只单位: %v", ids)
	}
	select {
	case <-done:
	case <-time.After(35 * time.Second):
		t.Fatalf("硬停后单位应在数秒内落地,而非等满 sleep 30")
	}
	if res == nil || res.StepStatus != "aborted" {
		t.Fatalf("硬停应落 aborted: %+v", res)
	}
	// 终态照常入账(审计闭环)。
	pg, _ := store.ProgressGraph(ctx, task)
	if len(pg.Steps) != 1 || pg.Steps[0].Status != "aborted" {
		t.Fatalf("step 应落 aborted: %+v", pg.Steps)
	}
}

func countRunningTools(t *testing.T, store *rings.Store, task string) int {
	t.Helper()
	nodes, err := store.NodesByType(context.Background(), task, rings.NodeStep, "running")
	if err != nil {
		t.Fatalf("查 running 步骤失败: %v", err)
	}
	return len(nodes)
}
