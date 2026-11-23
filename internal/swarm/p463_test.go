package swarm

import (
	"context"

	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P4-63 完成标准(硬规则判定收窄 + 拒动作不杀单位,问题清单 #85) ----
// ① 三档判定位类级测试集(判定语义为契约,禁止按本日两实锤样本字符串
//    特判打补丁):动作位多形态拒 / 非动作位多形态放 / 模糊形态弹门;
// ② 真拦样本命令被拒且单位存活继续干活(transcript 连续可证);
// ③ P5-03③ 起无连拒计数升级杀——拒绝仅是反馈,单位存活(类级:多
//    次连拒不熔断);
// ④ 面板人工拒照旧杀(gate_test.go TestPanelDenyFusesUnit 回归承);
// ⑤ 规则可编辑可停用(hardrules_test.go 回归承);
// ⑥ P4-07 拦截命中统计新处置形态(server_p407_test.go 承)。

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
	recs := []rings.ApprovalRecord{}
	for _, r := range approvalEvents(t, store, task) {
		if r.Type == rings.ApprovalRecDenied {
			recs = append(recs, r)
		}
	}
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecDenied ||
		recs[0].DenyLine != rings.DenyLineHardRule || !containsAll(recs[0].Reason, "关机重启") {
		t.Fatalf("拒绝留痕应恰一笔(规则身份可解析): %+v", recs)
	}
}

// TestP463RepeatDenyNeverKills 完成标准③(P5-03③ 口径):同单位同规则
// 连续多次命中确定性拒绝——每笔只拒动作,单位持续存活(模型每被再次
// 调用即证),拒绝留痕连续且零升级说明。
func TestP463RepeatDenyNeverKills(t *testing.T) {
	store, model, ex, task := p463RunStepEnv(t, func(rr *recordedRequest) scriptedReply {
		if rr.Index >= 5 {
			return scriptedReply{Text: "放弃该方向,收口。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "b1", Name: "bash", Args: `{"command":"shutdown -h now"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	ctx := context.Background()

	res, _ := ex.RunStep(ctx, task, "s1")
	if res.StepStatus != "exhausted" && res.StepStatus != "done" && res.StepStatus != "failed" {
		t.Fatalf("多次拒绝后单位不得被升级终止(终态 %q 应为自然收场): %+v", res.StepStatus, res)
	}
	if strings.Contains(res.Reason, "升级终止单位") {
		t.Fatalf("终因不得含升级说明: %q", res.Reason)
	}
	// 单位存活:模型在多次拒绝后仍被再次调用(同会话连续)。
	if n := model.requestCount(); n < 6 {
		t.Fatalf("模型应被调用至少 6 次(拒绝后单位仍活着): %d", n)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) < 5 || calls[0].UnitID != calls[len(calls)-1].UnitID {
		t.Fatalf("应同单位多次被拒(记录连续): %d 笔", len(calls))
	}
	recs := approvalEvents(t, store, task)
	if len(recs) < 5 {
		t.Fatalf("应至少五笔拒绝留痕: %+v", recs)
	}
	for i, r := range recs {
		if r.Type != rings.ApprovalRecDenied || r.DenyLine != rings.DenyLineHardRule {
			t.Fatalf("第 %d 笔应为硬规则拒绝: %+v", i, r)
		}
		if strings.Contains(r.Reason, "升级终止单位") {
			t.Fatalf("任何一笔都不得带升级说明: %+v", r)
		}
	}
}

// TestP463ReadOnlyChannelDenyNoKill 只读通道(http_probe)同款:确定性
// 拒绝不熔断(P5-03③:连续多次也不升级)——与审批门 ② 同一
// denyHardAction 路径。
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
	// 不熔断;连续三次均不升级(P5-03③ 无连拒计数升级杀)。
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
		if env.deniedReason != "" {
			t.Fatalf("第 %d 次不得熔断单位(P5-03③ 无升级杀): %q", i+1, env.deniedReason)
		}
	}
}
