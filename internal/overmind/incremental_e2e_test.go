package overmind

// P4-04 增量唤醒链路(问题清单 #9)的确定性验证:两个意图并发,
// 慢线单位被按请求挂起(模拟长任务),快线单位先落地——主宰必须在
// 慢线仍执行中就拿到研判轮,基于快线增量产出新方向(s3 派发先于慢线
// 落地,账本版本可证),不等批内其他单位。旧实现(schedule wg.Wait
// 等批)在本场景下不会产生任何落地间的拆解轮——反证即本测试对旧
// 代码必然失败。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestLandingWakesIncrementalRoundE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "incremental-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()
	packURLRe := regexp.MustCompile(`-\s+web1 \[service\] (http://\S+)`)

	held := make(chan struct{})     // 慢线:全程挂起,测试末放行
	heldFast := make(chan struct{}) // 快线:探测后的提交调用挂起,派发后轮过后放行
	var sawIncrement atomic.Bool
	// 任务包意图节锚点:前序尝试节含同目标其他步骤(快线的包里也有
	// 慢线字样),判别单位身份须锚定"本步骤意图"节。
	const intentSection = "二、本步骤意图(只做这一条)\n  "

	// 脚本分支:
	//   拆解:无步骤 → 派快线+慢线两条意图;快线事实确认且图上有步骤
	//   在跑(慢线)且增量方向未派过 → 基于增量派 s3(带研判结论);
	//   其余空列表。
	//   虫群包:快线 探测→提交指纹→收口;慢线 提交枚举→收口(请求被
	//   holdReq 挂起);增量方向 提交版本接口→收口。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			switch {
			case strings.Contains(rr.LastUser, "步骤:\n  (无)"):
				return scriptedReply{Text: `{"new_intents":[` +
					`{"intent":"快线:确认靶站首页指纹","expected_observation":"首页内容与响应头证据","action_level":"只读探测","covers":["web1"]},` +
					`{"intent":"慢线:枚举靶站目录结构","expected_observation":"目录清单","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			case strings.Contains(rr.LastUser, "首页指纹已确认") &&
				strings.Contains(rr.LastUser, "[running]") &&
				!strings.Contains(rr.LastUser, "增量新方向"):
				sawIncrement.Store(true)
				sawIncrement.Store(true)
				return scriptedReply{Text: `{"assessment":{"q_unlock":"快线单位已落地并确认首页指纹,据此解锁指纹驱动的接口探测;慢线在途,不阻塞对新结果的响应","note":"增量研判:基于快线产出新方向"},"new_intents":[` +
					`{"intent":"基于指纹的增量新方向:探测版本接口","expected_observation":"版本接口响应","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			default:
				return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
			}
		}
		base := mainURL
		if u := packURLRe.FindStringSubmatch(rr.LastUser); u != nil {
			base = u[1]
		}
		switch {
		case strings.Contains(rr.LastUser, intentSection+"慢线"):
			if !seenToolResult(rr, "submit_candidate_fact", "已入候选事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "m1", Name: "submit_candidate_fact",
					Args: `{"statement":"目录结构已枚举","evidence":"http_probe 逐路径实测","asset_id":"web1"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "慢线完成:目录已枚举。", Finish: "stop"}
		case strings.Contains(rr.LastUser, intentSection+"基于指纹的增量新方向"):
			if !seenToolResult(rr, "submit_candidate_fact", "已入候选事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "i1", Name: "submit_candidate_fact",
					Args: `{"statement":"版本接口已确认","evidence":"http_probe 实测版本端点","asset_id":"web1"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "增量方向完成:版本接口已确认。", Finish: "stop"}
		default: // 快线
			if !seenToolResult(rr, "http_probe", "status=") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "f1", Name: "http_probe",
					Args: fmt.Sprintf(`{"url":%q}`, base+"/"),
				}}, Finish: "tool_calls"}
			}
			if !seenToolResult(rr, "submit_candidate_fact", "已入候选事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "f2", Name: "submit_candidate_fact",
					Args: `{"statement":"首页指纹已确认:服务在线","evidence":"http_probe 实测响应","asset_id":"web1"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "快线完成:指纹已提交。", Finish: "stop"}
		}
	})
	t.Cleanup(model.Close)
	model.release() // 闸门常开;节奏由 holdReq 控制
	model.holdReq = func(rr *recordedRequest) <-chan struct{} {
		switch {
		case strings.Contains(rr.LastUser, intentSection+"慢线"):
			return held
		case strings.Contains(rr.LastUser, intentSection+"快线") &&
			seenToolResult(rr, "http_probe", "status="):
			// 快线的探测后调用(提交指纹/收口):挂住直至派发后轮已过,
			// 保证落地必然发生在第二轮研判之后——增量只能由落地唤醒
			// 撞出,不会被派发后轮抢先撞见(确定性)。
			return heldFast
		}
		return nil
	}
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	ex.Model = "scripted-overmind-model"

	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov,
		RootDir: root, Model: "scripted-overmind-model"}
	m := newManagerAsm(t, store, root, loop)
	if _, err := m.CreateTask(ctx, task, "增量研判 E2E", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 目标:两条已确认事实即达成(快线+增量;慢线的事实是收尾前的
	// 第三条——达成后循环仍等在飞慢线收场,P1-08 时序不变式)。
	mustSeedGoal(t, store, task, "g1", "确认靶站暴露面", map[string]any{
		"op":         "all_of",
		"conditions": []any{map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 2}},
	})
	mustSeedAsset(t, store, task, "web1", mainURL)
	m.Task(task).Wake()

	// ---- 轮次定序:派发轮与派发后轮都先于任何落地(双单位挂起) ----
	waitFor(t, 60*time.Second, "派发轮+派发后轮到达(均无事实)", func() bool {
		return len(model.decomposeRequests()) >= 2
	})
	for i, rr := range model.decomposeRequests() {
		if strings.Contains(rr.LastUser, "首页指纹已确认") {
			t.Fatalf("落地前第 %d 轮不应看到已确认事实(定序被破坏)", i)
		}
	}
	if s1 := mustNode(t, store, task, "s1"); s1.Status != "running" {
		t.Fatalf("快线应挂起在执行中,实际 %s", s1.Status)
	}
	close(heldFast) // 放行快线:其落地只能撞出增量轮(第二轮已过)

	// ---- 增量轮:慢线仍挂起(从未落地)时,基于快线增量的研判已发生 ----
	waitFor(t, 60*time.Second, "快线落地即触发增量研判轮(不等慢线)", func() bool {
		return sawIncrement.Load()
	})
	if !sawIncrement.Load() {
		for i, rr := range model.decomposeRequests() {
			t.Logf("拆解#%d 态势尾段: %s", i, clipTailStr(rr.LastUser, 600))
		}
		t.Fatalf("增量研判轮未发生(见上:主宰看到的态势)")
	}
	if s3 := mustNode(t, store, task, "s3"); !strings.Contains(s3.Title, "增量新方向") {
		t.Fatalf("增量轮应已派发新方向步骤: %s", s3.Title)
	}
	if s2 := mustNode(t, store, task, "s2"); s2.Status != "running" {
		t.Fatalf("慢线应仍在执行(增量轮发生在其落地前),实际 %s", s2.Status)
	}
	// 拆解请求直证:存在一轮既看到快线已确认事实、又看到在跑步骤的
	// 增量研判(旧实现等批,且派发后轮已过——此轮只能是落地唤醒的)。
	var incrementalReq bool
	for _, rr := range model.decomposeRequests() {
		if strings.Contains(rr.LastUser, "首页指纹已确认") && strings.Contains(rr.LastUser, "[running]") {
			incrementalReq = true
		}
	}
	if !incrementalReq {
		t.Fatalf("应存在基于增量(事实确认+在跑步骤)的拆解请求")
	}

	// ---- 放行慢线:在飞工作收尾,任务经机器判定完成 ----
	waitFor(t, 60*time.Second, "快线与增量方向单位均完成", func() bool {
		return mustNode(t, store, task, "s1").Status == "done" && mustNode(t, store, task, "s3").Status == "done"
	})
	close(held)
	waitFor(t, 60*time.Second, "慢线收尾后任务完成", func() bool {
		return m.Task(task).Status() == "completed"
	})
	if s2 := mustNode(t, store, task, "s2"); s2.Status != "done" {
		t.Fatalf("慢线应正常收尾 done,实际 %s", s2.Status)
	}
	if facts := countNodes(t, store, task, rings.NodeFact, "confirmed"); facts != 3 {
		t.Fatalf("应确认 3 条事实(快线/慢线/增量),实际 %d", facts)
	}
	if err := loop.LastErr(); err != nil {
		t.Fatalf("干净运行不应残留错误: %v", err)
	}

	// ---- 账本版本序(事后可证,版本不可变):s3 派发与研判记录均先于
	// 慢线落地——"不等后完成者"的账本证据 ----
	evs, err := store.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	s3Added, assRev, s2Finished := int64(0), int64(0), int64(0)
	for _, ev := range evs {
		switch {
		case ev.Kind == rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == "s3" && s3Added == 0 {
				s3Added = ev.Revision
			}
		case ev.Kind == rings.EventOvermindRecord:
			var p rings.OvermindRecord
			if json.Unmarshal(ev.Payload, &p) == nil && p.Type == rings.OvermindRecAssessment &&
				strings.Contains(p.QUnlock, "快线") {
				assRev = ev.Revision
			}
		case ev.Kind == rings.EventSwarmRecord:
			var p rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &p) == nil && p.StepID == "s2" && p.Type == rings.SwarmRecSessionEnd {
				s2Finished = ev.Revision
			}
		}
	}
	if s2Finished == 0 {
		t.Fatalf("慢线落地事件缺失")
	}
	if s3Added == 0 || s3Added >= s2Finished {
		t.Fatalf("增量新方向 s3 派发(rev %d)必须先于慢线落地(rev %d)", s3Added, s2Finished)
	}
	if assRev == 0 || assRev >= s2Finished {
		t.Fatalf("增量研判记录(rev %d)必须先于慢线落地(rev %d)", assRev, s2Finished)
	}
}

// clipTailStr 取尾段(调试输出用)。
func clipTailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
