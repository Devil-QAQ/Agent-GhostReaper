package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/capability"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// Uses the existing opt-in read-only encrypted-settings provider. No credentials
// are copied into the acceptance directory, transcript, or report.
func TestResourceLiveFormalSessions(t *testing.T) {
	p := liveActionReviewProvider(t)
	dir := os.Getenv("GHOSTREAPER_RESOURCE_EVIDENCE")
	if dir == "" {
		dir = t.TempDir()
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s := openStore(t, dir)
	c, _ := resourceFixture(t, filepath.Join(dir, "capability-library"))
	empty, e := capability.NewCatalog(filepath.Join(dir, "empty-library"))
	if e != nil {
		t.Fatal(e)
	}
	defer empty.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	indexCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.RunIndex(indexCtx)
	cases := []struct {
		id, intent string
		c          *capability.Catalog
		required   bool
	}{
		{"d-live-use", "本次是本地资料读取验收：使用资源入口查询 CVE-2099-12345，查看相关参考原文；独立浏览 odd 目录并读取 helper.sh，理解内容后通过现有 bash 完成本地核对。不要请求目标或上网。总结区分参考材料与目标事实。本场景明确验阅读，不代表每项工作都需查库。", c, true},
		{"d-live-direct", "在当前步骤工作目录用 bash 计算 17*19 并打印 DIRECT_RESULT=结果。推进线索已完整，不需要资料查询。不访问目标，不提交漏洞，完成即停。", c, false},
		{"d-live-empty", "验证空资料库回退：查询 ABSENT_RESOURCE_7391；无命中后继续用 bash 自写一行计算 23*29 并打印 FALLBACK_RESULT=结果。不访问目标，不提交漏洞。", empty, false},
		{"e-live-miss", "本地资料回退验收：查询 ABSENT_RESOURCE_7391；如未命中，自主用 bash 计算 23*29 并打印 MISS_FALLBACK=结果。不访问目标或网络，不提交漏洞。", c, false},
		{"e-live-inapplicable", "本地资料回退验收：阅读 odd/notes.md，核对它是否提供计算 23*29 的方法；资料不适用时自行通过 bash 完成计算，打印 INAPPLICABLE_FALLBACK=结果。不访问目标或网络，不提交漏洞。", c, false},
		{"e-live-failed", "本地失败回退验收：先用 bash 执行 exit 7，作为明确会失败的授权样例；失败后自主改用另一条 bash 计算 23*29，打印 FAILED_FALLBACK=结果。失败只代表本次方法失败。不访问目标或网络，不提交漏洞。", c, false},
		{"d-live-unavailable", "验证资料服务故障回退：尝试资料查询；服务不可用后继续用 bash 计算 31*37 并打印 SERVICE_FALLBACK=结果。不访问目标，不提交漏洞。", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			resourceTask(t, s, tc.id, tc.intent)
			ex := NewExecutor(s, p, filepath.Join(dir, "tasks"), Budget{MaxTurns: 24})
			ex.Capabilities = tc.c
			ex.Mirror = mirror.New(s, ex.Blobs, filepath.Join(dir, "tasks"))
			r, e := ex.RunStep(ctx, tc.id, "s1")
			if e != nil || r.Reason != "completed" {
				t.Fatalf("会话未完成: reason=%s error=%v", r.Reason, e)
			}
			counts := map[string]int{}
			bashText := ""
			for _, r := range swarmRecords(t, s, tc.id) {
				if r.Type == rings.SwarmRecToolCall && !r.IsError {
					counts[r.ToolName]++
				}
				if r.ToolName == "bash" {
					bashText += r.ResultHead + r.Note
				}
			}
			if counts["bash"] == 0 {
				t.Fatal("未实际调用 bash")
			}
			marker := map[string]string{"d-live-direct": "DIRECT_RESULT=323", "d-live-empty": "FALLBACK_RESULT=667", "d-live-unavailable": "SERVICE_FALLBACK=1147", "e-live-miss": "MISS_FALLBACK=667", "e-live-inapplicable": "INAPPLICABLE_FALLBACK=667", "e-live-failed": "FAILED_FALLBACK=667"}[tc.id]
			if marker != "" && !strings.Contains(bashText, marker) {
				t.Fatalf("缺少实际计算结果 %s", marker)
			}
			if (tc.id == "e-live-miss" || tc.id == "e-live-inapplicable") && counts["resource_read"] == 0 {
				t.Fatal("资料回退场景未实际读取资料入口", counts)
			}
			if tc.id == "e-live-failed" {
				attempted := false
				for _, rec := range swarmRecords(t, s, tc.id) {
					attempted = attempted || (rec.Type == rings.SwarmRecToolCall && rec.ToolName == "bash" && strings.Contains(string(rec.ToolArgs), "exit 7"))
				}
				if !attempted {
					t.Fatal("失败样例未实际执行")
				}
			}
			if tc.required {
				if counts["resource_read"] < 3 {
					t.Fatal("资料链未实际完成", counts)
				}
				if !strings.Contains(bashText, "RESOURCE_FILE_USED") {
					t.Fatal("未根据读取内容完成核对")
				}
			}
			if tc.id == "d-live-direct" && counts["resource_read"] != 0 {
				t.Fatal("明确线索场景仍查库", counts)
			}
			t.Logf("正式会话完成: %s unit=%s calls=%v", tc.id, r.UnitID, counts)
			_, e = s.Append(t.Context(), tc.id, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "stopped"}})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestResourceLiveReadAndRetest(t *testing.T) {
	p := liveActionReviewProvider(t)
	dir := os.Getenv("GHOSTREAPER_RESOURCE_DETAIL_EVIDENCE")
	if dir == "" {
		t.Skip("independent copy of resource files not supplied")
	}
	s := openStore(t, dir)
	c, e := capability.NewCatalog(filepath.Join(dir, "capability-library"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	intent := "本地资料验收：查询 Apache，读取命中的原文；自行理解内容并用 bash 输出完成核对的说明。只核对资料，不请求目标、不联网，不把资料或 CVE 作为目标证据。完成即停。"
	resourceTask(t, s, "d-live-details", intent)
	ex := NewExecutor(s, p, filepath.Join(dir, "tasks"), Budget{MaxTurns: 24})
	ex.Capabilities = c
	ex.Mirror = mirror.New(s, ex.Blobs, filepath.Join(dir, "tasks"))
	r, e := ex.RunStep(ctx, "d-live-details", "s1")
	if e != nil || r.Reason != "completed" {
		t.Fatalf("资料会话未完成: %v", e)
	}
	detail, bash := false, false
	for _, rec := range swarmRecords(t, s, "d-live-details") {
		if rec.Type != rings.SwarmRecToolCall || rec.IsError {
			continue
		}
		if rec.ToolName == "resource_read" && strings.Contains(string(rec.ToolArgs), `"read"`) {
			detail = true
		}
		bash = bash || rec.ToolName == "bash"
	}
	if !detail || !bash {
		t.Fatalf("原文/实际读取缺失: %v %v", detail, bash)
	}
	_, _ = s.Append(ctx, "d-live-details", rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "stopped"}})
	resourceTask(t, s, "d-live-retest", "合成判断的复测资料接入验收")
	seedRetestableClaim(t, s, "d-live-retest", "claim-resource", "合成离线判断，目标不可达时不能判断修复")
	_, e = s.Append(ctx, "d-live-retest", rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{ClaimID: "claim-resource", Type: rings.TestClaimRecRepro, Severity: "high", Steps: []rings.ReproStep{{Seq: 1, Desc: "本轮只验证本地资料接入：查询 Apache、读取原文，理解原文内容，用 bash 输出核对说明。合成目标当前不可达，不访问目标或网络；据此前置限制提交 inconclusive，不能将本地资料视为目标证据。", Op: "本地资料核对"}}}})
	if e != nil {
		t.Fatal(e)
	}
	run, e := ex.BeginRetest(ctx, "d-live-retest", "claim-resource")
	if e != nil {
		t.Fatal(e)
	}
	r, e = ex.DriveRetest(ctx, run)
	if e != nil || r.Reason != "completed" {
		t.Fatalf("复测未完成: %v", e)
	}
	calls := map[string]int{}
	for _, rec := range swarmRecords(t, s, "d-live-retest") {
		if rec.Type == rings.SwarmRecToolCall && !rec.IsError {
			calls[rec.ToolName]++
		}
	}
	for _, name := range []string{"resource_read", "bash", "submit_retest_result"} {
		if calls[name] == 0 {
			t.Fatal("复测缺少实际调用", calls)
		}
	}
	if calls["submit_claim"] != 0 || calls["submit_candidate_fact"] != 0 {
		t.Fatal("复测范围扩张", calls)
	}
	t.Logf("真实原文读取及复测会话完成，复测调用=%v", calls)
	_, _ = s.Append(ctx, "d-live-retest", rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "stopped"}})
}
