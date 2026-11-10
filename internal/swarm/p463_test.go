package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// ---- P4-63 完成标准(硬规则判定收窄 + 拒动作不杀单位,问题清单 #85) ----
// ① 三档判定位类级测试集(判定语义为契约,禁止按本日两实锤样本字符串
//    特判打补丁):动作位多形态拒 / 非动作位多形态放 / 模糊形态弹门;
// ② 真拦样本命令被拒且单位存活继续干活(transcript 连续可证);
// ③ 同规则累计 3 次升级杀(前两次存活记录连续);
// ④ 面板人工拒照旧杀(gate_test.go TestPanelDenyFusesUnit 回归承);
// ⑤ 规则可编辑可停用(hardrules_test.go 回归承);
// ⑥ P4-07 拦截命中统计新处置形态(server_p407_test.go 承)。

// tierCase 判定位用例:value 内找 word 首次出现处判位,matched 是规则
// 正则会给出的匹配串(数据库组判定用)。
type tierCase struct {
	name    string
	value   string
	word    string
	matched string
	want    hardTier
}

// runTierCases 跑判位用例表(sys 用 judgeCommandVerb,db 用 judgeSQLStatement)。
func runTierCases(t *testing.T, cases []tierCase, db bool) {
	t.Helper()
	for _, c := range cases {
		start := strings.Index(c.value, c.word)
		if start < 0 {
			t.Fatalf("%s: 词 %q 不在样本内", c.name, c.word)
		}
		var got hardTier
		if db {
			got = judgeSQLStatement(c.value, start, c.matched)
		} else {
			got = judgeCommandVerb(c.value, start)
		}
		if got != c.want {
			t.Fatalf("%s: %q 判位 %v,应为 %v", c.name, c.value, got, c.want)
		}
	}
}

// TestP463JudgeVerbTiers 系统破坏类判位(只认命令动词位):动作位多形态
// (行首直接命令/管道后/分号后/&&后/sudo 动词位/systemctl 动词位/命令
// 替换内/多行行首)、非动作位多形态(URL 路径/带引号字符串/参数值/注释
// 行)、模糊形态(普通命令词后——是否被执行取决于该命令语义)。
func TestP463JudgeVerbTiers(t *testing.T) {
	runTierCases(t, []tierCase{
		// 动作位:拒
		{"行首直接命令", "shutdown -h now", "shutdown", "", hardTierAction},
		{"行首直接命令 rm", "rm -rf / --no-preserve-root", "rm", "", hardTierAction},
		{"管道后命令", "cat x | reboot", "reboot", "", hardTierAction},
		{"分号后命令", "echo x; poweroff", "poweroff", "", hardTierAction},
		{"&& 后命令", "apt update && reboot", "reboot", "", hardTierAction},
		{"sudo 动词位", "sudo shutdown -h now", "shutdown", "", hardTierAction},
		{"sudo systemctl 动词位", "sudo systemctl poweroff", "poweroff", "", hardTierAction},
		{"systemctl 动词位", "systemctl reboot", "reboot", "", hardTierAction},
		{"命令替换内", "echo $(shutdown) done", "shutdown", "", hardTierAction},
		{"多行行首命令", "echo x\nmkfs.ext4 /dev/sda", "mkfs", "", hardTierAction},
		// 非动作位:放行不当动作
		{"URL 路径(本日实锤)", "curl -s https://x.example/actuator/shutdown", "shutdown", "", hardTierNonAction},
		{"URL 路径 http", "curl -s http://t/actuator/reboot", "reboot", "", hardTierNonAction},
		{"带引号字符串", `echo "shutdown label"`, "shutdown", "", hardTierNonAction},
		{"单引号字符串", `python3 -c 'print(shutdown_label)'`, "shutdown", "", hardTierNonAction},
		{"参数值", "curl -s http://t/?svc=shutdown", "shutdown", "", hardTierNonAction},
		{"参数值短参", "hosted-daemon --name=reboot", "reboot", "", hardTierNonAction},
		{"注释行 #", "# reboot the box now", "reboot", "", hardTierNonAction},
		{"注释行 //", "// halt: explained here", "halt", "", hardTierNonAction},
		{"多行注释行", "echo done\n# poweroff mentioned", "poweroff", "", hardTierNonAction},
		// 模糊形态:弹门人审
		{"普通词后不可判", "echo shutdown", "shutdown", "", hardTierAmbiguous},
		{"nohup 后不可判", "nohup reboot", "reboot", "", hardTierAmbiguous},
		{"xargs 后不可判", "xargs rm -rf /", "rm", "", hardTierAmbiguous},
		{"裸括号不可判", "(shutdown)", "shutdown", "", hardTierAmbiguous},
	}, false)
}

// TestP463JudgeSQLTiers 数据库破坏类判位(只认 SQL 语句语境:关键词组合
// 且处语句位)。
func TestP463JudgeSQLTiers(t *testing.T) {
	runTierCases(t, []tierCase{
		// 动作位:真语句,拒
		{"值首真 TRUNCATE TABLE", "TRUNCATE TABLE users;", "TRUNCATE", "TRUNCATE TABLE users", hardTierAction},
		{"值首真 DROP TABLE", "DROP TABLE sessions;", "DROP", "DROP TABLE sessions", hardTierAction},
		{"引号内容首语句", `mysql -u root -e "TRUNCATE TABLE users"`, "TRUNCATE", "TRUNCATE TABLE users", hardTierAction},
		{"单引号内容首语句", `mysql -e 'DROP TABLE logs'`, "DROP", "DROP TABLE logs", hardTierAction},
		{"分号后第二条语句", "http://t/?q=1;DROP TABLE users", "DROP", "DROP TABLE users", hardTierAction},
		{"多行语句位", "SELECT 1;\nDELETE FROM tokens;", "DELETE", "DELETE FROM tokens;", hardTierAction},
		{"HTTP 包体语句", `{"query":"DROP TABLE users"}`, "DROP", "DROP TABLE users", hardTierAction},
		{"自建规则组合 DROP INDEX", "DROP INDEX idx_sessions;", "DROP", "DROP INDEX idx_sessions", hardTierAction},
		// 非动作位:组合缺位或不在语句位,放行
		{"print 标签(本日实锤)", "print(\"truncate -2\")", "truncate", "truncate -2", hardTierNonAction},
		{"切片标签", "TRUNCATED OUTPUT [-2:]", "TRUNCATED", "TRUNCATED OUTPUT", hardTierNonAction},
		{"裸词带标识符(文件截断)", "truncate -s 0 /tmp/out.log", "truncate", "truncate -s", hardTierNonAction},
		{"字面量中段提及", `x = "note DROP TABLE later"`, "DROP", "DROP TABLE later", hardTierNonAction},
		{"参数值 query=", "curl -d 'q=DROP TABLE t' http://t", "DROP", "DROP TABLE t", hardTierNonAction},
		{"URL 路径", "curl -s http://t/api/drop-table", "drop", "drop-table", hardTierNonAction},
		{"注释行", "# TRUNCATE TABLE users", "TRUNCATE", "TRUNCATE TABLE users", hardTierNonAction},
		// 模糊形态:弹门人审(喂给解释器与否静态不可判)
		{"echo 喂 mysql 管道", "echo DROP TABLE users | mysql -u root t", "DROP", "DROP TABLE users", hardTierAmbiguous},
		{"裸括号", "(DROP TABLE", "DROP", "DROP TABLE", hardTierAmbiguous},
	}, true)
}

// TestP463MatchIntegration matchHardRule 集成(播种库,三档到处置的映射
// 由消费点承):动作位命中、非动作位不命中(两实锤回归)、模糊命中。
func TestP463MatchIntegration(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	ctx := context.Background()

	denied := []struct{ cmd, ruleID string }{
		{"shutdown -h now", "hr-shutdown"},                    // 行首直接命令
		{`mysql -u root -e "TRUNCATE TABLE users"`, "hr-truncate"}, // 引号内真语句
	}
	for _, c := range denied {
		r, _, tier, hit := matchHardRule(ctx, store, "bash", json.RawMessage(`{"command":`+mustJSONString(c.cmd)+`}`))
		if !hit || r.ID != c.ruleID || tier != hardTierAction {
			t.Fatalf("动作位应命中拒绝 %s: hit=%v rule=%s tier=%v", c.cmd, hit, r.ID, tier)
		}
	}
	allowed := []string{
		"curl -s https://x.example/actuator/shutdown",     // 实锤①:URL 路径探测
		`python3 -c "print('truncate -2')"`,               // 实锤②:print 标签
		"curl -s http://t/?svc=shutdown",                  // 参数值
		"echo done\n# reboot the box now",                 // 注释行
	}
	for _, cmd := range allowed {
		if _, _, _, hit := matchHardRule(ctx, store, "bash", json.RawMessage(`{"command":`+mustJSONString(cmd)+`}`)); hit {
			t.Fatalf("非动作位不应命中: %q", cmd)
		}
	}
	r, _, tier, hit := matchHardRule(ctx, store, "bash", json.RawMessage(`{"command":"echo shutdown"}`))
	if !hit || r.ID != "hr-shutdown" || tier != hardTierAmbiguous {
		t.Fatalf("模糊形态应命中弹门: hit=%v rule=%s tier=%v", hit, r.ID, tier)
	}
}

// TestP463RejectTextAnchor 拒绝反馈锚例逐字锁定(造物主 2026-11-09
// 拍板文案,防文案漂移)。
func TestP463RejectTextAnchor(t *testing.T) {
	r := findHardRule(t, builtinHardRules(), "hr-truncate")
	want := "命令已被拦截:数据库破坏·清空表——删除目标数据的操作禁止执行。请改用只读方式验证;确需破坏性验证时由操作者在拦截规则面板处理。"
	if got := hardRuleRejectText(r); got != want {
		t.Fatalf("锚例文案不符:\n got  %q\n want %q", got, want)
	}
}

// p463RunStepEnv 活体测试共用铺垫:本地靶标 + 派发只读探测步骤(硬规则
// 判定在分级门槛之前,只读级即可命中)。
func p463RunStepEnv(t *testing.T, script func(rr *recordedRequest) scriptedReply) (*rings.Store, *scriptedModel, *Executor, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, "p463-live", host, port)
	dispatchStepLikeOvermind(t, store, "p463-live", "s1", "探测关机接口暴露面", "g1", "target")
	model := newScriptedModel(script)
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	return store, model, ex, "p463-live"
}

// TestP463DenyKeepsUnitAlive 完成标准②:真拦样本命令被拒且单位存活
// 继续干活——拒绝结果是换路指引(锚例文案),单位读到反馈换只读命令,
// 同一会话收尾正常完成(transcript 连续)。
func TestP463DenyKeepsUnitAlive(t *testing.T) {
	store, model, ex, task := p463RunStepEnv(t, func(rr *recordedRequest) scriptedReply {
		switch rr.Index {
		case 0:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash", Args: `{"command":"shutdown -h now"}`,
			}}, Finish: "tool_calls"}
		case 1:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b2", Name: "bash", Args: `{"command":"echo rerouted-readonly"}`,
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "已改用只读方式核验", Finish: "stop"}
	})
	defer model.Close()
	ctx := context.Background()

	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil || res.StepStatus != "done" {
		t.Fatalf("单位应存活并正常完成: %+v err=%v", res, err)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 2 {
		t.Fatalf("应有两次 bash 调用(被拒一次 + 换路一次): %d", len(calls))
	}
	if !calls[0].IsError || !containsAll(calls[0].ResultHead, "命令已被拦截", "系统破坏命令", "关机重启") {
		t.Fatalf("拒绝结果应为换路指引(锚例文案): %+v", calls[0])
	}
	if calls[1].IsError {
		t.Fatalf("换路命令应执行成功: %+v", calls[1])
	}
	if calls[0].UnitID != calls[1].UnitID {
		t.Fatalf("两次调用应同单位(transcript 连续): %s vs %s", calls[0].UnitID, calls[1].UnitID)
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecDenied ||
		recs[0].DenyLine != rings.DenyLineHardRule || !containsAll(recs[0].Reason, "关机重启") {
		t.Fatalf("拒绝留痕应恰一笔(规则身份可解析): %+v", recs)
	}
}

// TestP463EscalateAfterThree 完成标准③:同单位同规则累计命中 3 次升级
// 杀单位;前两次单位存活(模型每被再次调用即证),三笔拒绝留痕连续,
// 第三笔带升级说明。
func TestP463EscalateAfterThree(t *testing.T) {
	store, model, ex, task := p463RunStepEnv(t, func(rr *recordedRequest) scriptedReply {
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "b1", Name: "bash", Args: `{"command":"shutdown -h now"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	ctx := context.Background()

	res, _ := ex.RunStep(ctx, task, "s1")
	if res.StepStatus != StepApprovalDenied {
		t.Fatalf("第 3 次命中应升级杀单位: %+v", res)
	}
	if !strings.Contains(res.Reason, "同规则累计命中 3 次") {
		t.Fatalf("终因应含升级说明: %q", res.Reason)
	}
	// 前两次存活:模型在第 1、2 次拒绝后仍被再次调用(同会话连续)。
	if n := model.requestCount(); n < 3 {
		t.Fatalf("模型应被调用至少 3 次(前两次拒绝后单位仍活着): %d", n)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 3 || calls[0].UnitID != calls[2].UnitID {
		t.Fatalf("应同单位三次被拒(记录连续): %d 笔", len(calls))
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 3 {
		t.Fatalf("应恰三笔拒绝留痕: %+v", recs)
	}
	for i, r := range recs {
		if r.Type != rings.ApprovalRecDenied || r.DenyLine != rings.DenyLineHardRule {
			t.Fatalf("第 %d 笔应为硬规则拒绝: %+v", i, r)
		}
		if i < 2 && strings.Contains(r.Reason, "升级终止") {
			t.Fatalf("前两笔不应带升级说明: %+v", r)
		}
	}
	if !strings.Contains(recs[2].Reason, "同规则累计命中 3 次,升级终止单位") {
		t.Fatalf("第三笔应带升级说明: %+v", recs[2])
	}
}

// TestP463AmbiguousGatesForHuman 模糊形态弹门人审:待决卡带判位模糊说明
// 与命令原文;批准后重发同调用按放行配额通过(只放这一条);无门
// fail-safe 拒本次动作不熔断单位。
func TestP463AmbiguousGatesForHuman(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	const task = "p463-ambig"
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ctx := context.Background()
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "意图", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
	args := json.RawMessage(`{"command":"echo shutdown"}`)

	d := env.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Deny || !strings.Contains(d.Message, approvalPendingNote) {
		t.Fatalf("模糊形态应弹门: %+v", d)
	}
	if env.park == nil || env.deniedReason != "" {
		t.Fatalf("弹门应挂起且不熔断: park=%v denied=%q", env.park != nil, env.deniedReason)
	}
	pending := gate.Pending(task)
	if len(pending) != 1 || !containsAll(pending[0].Note, "判位模糊", "系统破坏命令") {
		t.Fatalf("待决卡应带判位模糊说明: %+v", pending)
	}
	if !strings.Contains(pending[0].ToolArgs, "echo shutdown") {
		t.Fatalf("待决卡应带命令原文: %s", pending[0].ToolArgs)
	}
	// 批准:armGateApproval 只武装单次放行配额(非代理旁路组、非门槛票),
	// 重发同调用通过,不同命令重新过门。
	if err := gate.Decide(ctx, task, pending[0].ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	env.armGateApproval(pending[0])
	if d := env.approvalGate(pack)(ctx, "bash", args, permission.Context{}); d.Behavior != permission.Allow {
		t.Fatalf("批准后重发同调用应放行: %+v", d)
	}
	args2 := json.RawMessage(`{"command":"echo shutdown again"}`)
	if d := env.approvalGate(pack)(ctx, "bash", args2, permission.Context{}); d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("不同命令应重新弹门: %+v", d)
	}
	// 无门 fail-safe:拒本次动作、不熔断单位。
	ex.Gate = nil
	env2 := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}
	d = env2.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Deny || env2.deniedReason != "" || !strings.Contains(d.Message, "命令已被拦截") {
		t.Fatalf("无门应拒动作不熔断: %+v denied=%q", d, env2.deniedReason)
	}
}

// TestP463IntentRuleEscalate 意图拦截同款升级保险:同规则累计 3 次拒绝
// 升级杀单位,前两次只拒调用(单位存活由 env.deniedReason 为空证)。
func TestP463IntentRuleEscalate(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	ctx := context.Background()
	rules := []rings.IntentRule{{
		ID: "ir-1", Action: string(rings.IntentDataRead), Disposition: rings.IntentForbid,
	}}
	pack := intentPack(rules, AssetBrief{ID: "a1", Title: "http://target-a:8080"})
	env := &unitEnv{Executor: ex, TaskID: "intent-task", StepID: "s1", UnitID: "u9", TranscriptID: "swarm-u9"}

	for i := 1; i <= 3; i++ {
		d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://target-a:8080/x"}`), permission.Context{})
		if d.Behavior != permission.Deny {
			t.Fatalf("第 %d 次应拒绝: %+v", i, d)
		}
		if i < 3 && env.deniedReason != "" {
			t.Fatalf("第 %d 次不得熔断单位: %q", i, env.deniedReason)
		}
		if i == 3 && (env.deniedReason == "" || !strings.Contains(env.deniedReason, "同规则累计命中 3 次")) {
			t.Fatalf("第 3 次应升级杀: %q", env.deniedReason)
		}
	}
	recs := approvalEvents(t, ex.Store, "intent-task")
	if len(recs) != 3 || recs[2].DenyLine != rings.DenyLineIntentRule ||
		!strings.Contains(recs[2].Reason, "同规则累计命中 3 次") {
		t.Fatalf("意图拦截升级留痕不符: %+v", recs)
	}
}

// TestP463ReadOnlyChannelDenyNoKill 只读通道(http_probe)同款:确定性
// 拒绝不熔断、同规则累计升级——与审批门 ② 同一 denyHardAction 路径。
func TestP463ReadOnlyChannelDenyNoKill(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	env := &unitEnv{Executor: ex, TaskID: "p463-ro", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: "p463-ro", StepID: "s1", StepIntent: "意图", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
	g := newToolGuard(env, pack, nil, 100, time.Time{}, 0)
	wt := &wrappedTool{inner: env.httpProbeTool(), guard: g}

	// SQL 语句位(注入参数里的分号后语句,数据库组不限工具)→ 拒动作
	// 不熔断;同规则累计第 3 次升级。
	probes := []string{
		`{"url":"http://t/api?q=1;DROP TABLE users"}`,
		`{"url":"http://t/api?q=2;DROP TABLE logs"}`,
		`{"url":"http://t/api?q=3;DROP TABLE tokens"}`,
	}
	for i, p := range probes {
		reason := callToolText(t, wt, p)
		if !containsAll(reason, "命令已被拦截", "数据库破坏") {
			t.Fatalf("第 %d 次应拒绝动作: %q", i+1, reason)
		}
		if i < 2 && env.deniedReason != "" {
			t.Fatalf("第 %d 次不得熔断单位: %q", i+1, env.deniedReason)
		}
		if i == 2 && (env.deniedReason == "" || !strings.Contains(env.deniedReason, "同规则累计命中 3 次")) {
			t.Fatalf("第 3 次应升级杀: %q", env.deniedReason)
		}
	}
}
