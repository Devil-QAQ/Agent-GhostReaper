package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-10:任务完成终态 ----
// 目标全部经仲裁者机器判定达成 → 在飞反证轮照常收尾(P1-08 时序不变式,
// 不因目标达成而遗弃)→ 真正静默后循环退出 → Manager 落 completed 终态。

// completedEventRev 取 completed 生命周期事件的 revision(不存在返回 -1)。
func completedEventRev(t *testing.T, store *rings.Store, task string) int64 {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Action != "completed" {
			continue
		}
		if p.Detail["reason"] != "全部目标达成(仲裁者判定或面板人工标记)" {
			t.Fatalf("completed 事件理由不符: %+v", p.Detail)
		}
		return ev.Revision
	}
	return -1
}

// claimFirstSubmitRev 取声明战役内首条交换单的 revision(交卷时点)。
func claimFirstSubmitRev(t *testing.T, store *rings.Store, task, claimID string) int64 {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.ClaimID == claimID && r.Type == rings.TestClaimRecSubmit {
			return ev.Revision
		}
	}
	return -1
}

// claimVerdictRev 取声明终态迁移(verdict 后的 state_changed)的 revision。
func claimVerdictRev(t *testing.T, store *rings.Store, task, claimID string) int64 {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	verdict := false
	for _, ev := range evs {
		if ev.Kind == rings.EventTestClaimRecord {
			var r rings.TestClaimRecord
			if err := json.Unmarshal(ev.Payload, &r); err == nil &&
				r.ClaimID == claimID && r.Type == rings.TestClaimRecVerdict {
				verdict = true
			}
		}
		if ev.Kind == rings.EventNodeStateChanged && verdict {
			var p rings.NodeStateChanged
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.NodeID == claimID {
				return ev.Revision
			}
		}
	}
	return -1
}

// TestLoopCompletesTaskAfterClaimsSettle 完成语义 E2E:mock 靶站上带谓词
// 目标(fact_confirmed + tool_observed port_probe)与两条可证伪声明同场——
// 目标在反证轮在飞时即可达成(事实与机器观察先行落地),但任务不立即
// 完成:反证 Step 继续执行、声明全部到终态、循环真静默后才退出并落
// completed。反证收尾被遗弃即本测试失败。
func TestLoopCompletesTaskAfterClaimsSettle(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "complete-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 脚本模型(P5-01 单会话连打):拆解状态感知(审计意图已派过即空);
	// 发现包 port_probe → robots/备份/版本三连 curl → 两条 submit_claim
	// 交卷(六字段,evidence_refs 引 URL) → 候选事实 → 收口(无反证包)。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "审计主站敏感路径") {
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[{"intent":"审计主站敏感路径暴露(robots/备份目录/版本接口)","expected_observation":"敏感路径清单与各自内容证据","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
		}
		u, err := url.Parse(mainURL)
		if err != nil || u.Port() == "" {
			return scriptedReply{Text: "资产地址无法解析。", Finish: "stop"}
		}
		switch {
		case !seenToolResult(rr, "port_probe", "state="):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "pp", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%s}`, u.Hostname(), u.Port()),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d1", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/robots.txt"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "mock backup archive listing"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d2", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/old-backup/"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", `"version":"2.4.1"`):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d3", Name: "bash", Args: fmt.Sprintf(`{"command":"curl -sS -m 5 %s/api/version"}`, mainURL),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_claim", "备份目录可公开匿名列举"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d4", Name: "submit_claim",
				Args: fmt.Sprintf(`{"summary":"/old-backup/ 备份目录可公开匿名列举","severity":"high","evidence_refs":[%q],"proving_marker":"mock backup archive listing","countercheck_note":"直接请求与绕缓存重放各一次,返回一致,排除缓存假象","impact_note":"备份文件可被未授权下载","asset_id":"web1","vulnclass":"信息暴露"}`, mainURL+"/old-backup/"),
			}}, Finish: "tool_calls"}
		case !hasToolCallWithArg(rr, "submit_claim", "公开暴露服务版本号"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d5", Name: "submit_claim",
				Args: fmt.Sprintf(`{"summary":"/api/version 公开暴露服务版本号","severity":"none","evidence_refs":[%q],"proving_marker":"2.4.1","countercheck_note":"重放一次确认稳定返回,非一次性状态","asset_id":"web1","vulnclass":"信息暴露"}`, mainURL+"/api/version"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "d6", Name: "submit_candidate_fact",
				Args: `{"statement":"主站服务在线，版本号观察为2.4.1","evidence":"port_probe state=open；本次/api/version原文{\"version\":\"2.4.1\"}","asset_id":"web1"}`,
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "审计完成:两条交卷单与一条候选事实已提交。", Finish: "stop"}
		}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := dir
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}
	m, err := manager.New(manager.Config{
		Store: store, RootDir: root,
		OnTaskRun: func(tctx context.Context, _ string, wake <-chan struct{}) { loop.Run(tctx, wake) },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(task)
		m.Close()
	})
	if _, err := m.CreateTask(ctx, task, "完成终态 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "审计主站暴露面并确认存活",
		map[string]any{"predicate": map[string]any{
			"op": "all_of",
			"conditions": []any{
				map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
				map[string]any{"kind": "tool_observed", "asset_id": "web1", "tool": "port_probe", "result_contains": "state=open", "min": 1},
			},
		}}); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1"}, map[string]string{"web1": mainURL})
	tk := m.Task(task)
	tk.Wake()

	// ---- 任务进入完成终态(Manager 状态 + 账本 completed 事件) ----
	waitFor(t, 90*time.Second, "任务进入完成终态", func() bool {
		return m.Task(task).Status() == manager.StatusCompleted && completedEventRev(t, store, task) > 0
	})
	revCompleted := completedEventRev(t, store, task)

	// 普通信息按事实回写，只有具有实际影响的声明进入漏洞确认。
	backup := claimByTitle(t, store, task, "备份目录")
	if claimByTitleSoft(store, task, "版本号") != nil {
		t.Fatal("信息性结果被立为漏洞")
	}
	facts, _ := store.NodesByType(ctx, task, rings.NodeFact, "confirmed")
	versionObserved := false
	for _, f := range facts {
		versionObserved = versionObserved || strings.Contains(f.Title, "版本号观察")
	}
	if !versionObserved {
		t.Fatal("拒收后实际版本观察未按事实回写")
	}
	if backup.Status != rings.TestClaimConfirmed {
		for _, r := range testclaimRecords(t, store, task, rings.TestClaimRecVerdict) {
			t.Logf("verdict %s: %s %q", r.ClaimID, r.Decision, r.PendingReason)
		}
		for _, r := range testclaimRecords(t, store, task, rings.TestClaimRecSubmit) {
			t.Logf("submit %s: step=%s refs=%v", r.ClaimID, r.StepID, r.EvidenceRefs)
		}
		allEvs, _ := store.Events(ctx, task, 0, 0)
		for _, ev := range allEvs {
			if ev.Kind != rings.EventSwarmRecord {
				continue
			}
			var sr rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &sr) == nil && sr.Type == rings.SwarmRecToolCall && sr.ToolName == "bash" {
				t.Logf("toolrec step=%s args=%.120s ref=%.40s", sr.StepID, sr.ToolArgs, sr.ResultRef)
			}
		}
		t.Fatalf("P5-02:证据成立的交卷应经三查确认: %s 原因=%q", backup.Status, backup.Status)
	}
	// 交卷时点取首单(补考重交允许发生在收尾宽限内——原会话原地补做)。
	for _, c := range []string{backup.ID} {
		if rv := claimFirstSubmitRev(t, store, task, c); rv <= 0 || rv >= revCompleted {
			t.Fatalf("声明 %s 首交(rev %d)必须先于 completed(rev %d)", c, rv, revCompleted)
		}
	}
	// Goal 经仲裁者判定达成。
	if got := goalStatus(t, store, task, "g1"); got != GoalStatusAchieved {
		t.Fatalf("目标应 achieved: %s", got)
	}
	if actor := goalAchievedBy(t, store, task, "g1"); actor != "arbiter" {
		t.Fatalf("目标达成应归 arbiter: %q", actor)
	}

	// ---- 循环已退出:完成后唤醒不再产生任何拆解 ----
	before := len(model.decomposeRequests())
	tk.Wake()
	time.Sleep(500 * time.Millisecond)
	if after := len(model.decomposeRequests()); after != before {
		t.Fatalf("完成退出后循环不得再拆解: before=%d after=%d", before, after)
	}
	if m.Task(task).Status() != manager.StatusCompleted {
		t.Fatalf("完成后状态保持 completed: %s", m.Task(task).Status())
	}
	if err := loopErr(loop); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}
}
