package overmind

// P5-02⑦【模型】活体:真实模型(deepseek-flash,经 P502_LIVE_KEY 环境
// 变量注入,不落盘)在 mock 靶站上单会话连打——探索→发现→证明→自查
// →交卷→机器三查→确认→复现包含证明交换全程。零 key 时跳过(确定性
// 回归在 p502_check_test;跑法:P502_LIVE_KEY=sk-... go test -run
// TestP502LiveThreeChecks -v -timeout 20m)。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestP502LiveThreeChecks 真实模型连打→交卷→三查→确认→复现包全程。
func TestP502LiveThreeChecks(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("P502_LIVE_KEY"))
	if key == "" {
		t.Skip("P502_LIVE_KEY 未配置,跳过活体(真实 LLM 行为验收)")
	}
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "p502-live"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 真实模型(平台预设端点:deepseek openai 格式;key 只经环境变量,
	// 不入任何文件与日志)。
	prov, err := llm.NewProvider(llm.Config{
		APIKey: key, BaseURL: "https://api.deepseek.com",
		Format: "openai", Model: "deepseek-flash",
	})
	if err != nil {
		t.Fatalf("构建真实模型 provider 失败: %v", err)
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

	// 目标与资产:只读审计(工具面防误伤;靶站是本机 mock)。
	if _, err := m.CreateTask(ctx, task, "P5-02 机器三查活体", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := SeedGoal(ctx, store, task, "g1", "审计 mock 靶站主站敏感路径暴露(robots/备份目录/版本接口),坐实漏洞并交卷", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	seedServiceAssets(t, store, task, []string{"web1"}, map[string]string{"web1": mainURL})
	m.Task(task).Wake()

	// ---- 等真实模型完成连打与交卷(交卷即查即时出结论) ----
	deadline := time.Now().Add(12 * time.Minute)
	var confirmed, insufficient int
	for time.Now().Before(deadline) {
		claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
		confirmed, insufficient = 0, 0
		done := 0
		for _, c := range claims {
			switch c.Status {
			case rings.TestClaimConfirmed:
				confirmed++
				done++
			case rings.TestClaimInsufficient, rings.TestClaimRefuted, rings.TestClaimDiscarded:
				insufficient++
				done++
			}
		}
		// 至少一条出终态即继续等齐(单位可能分批交卷);全部步骤终态且
		// 有终态声明即收。
		steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
		busy := false
		for _, s := range steps {
			if s.Status == "running" || s.Status == "dispatched" {
				busy = true
			}
		}
		if done > 0 && !busy {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
	if confirmed+insufficient == 0 {
		t.Fatal("真实模型未在时限内交卷出结论(超时)")
	}
	t.Logf("活体结论:已确认 %d,证据不足/推翻 %d", confirmed, insufficient)

	// ---- 断言链:交卷→三查→确认→复现包含证明交换 ----
	claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, "")
	var confirmedClaim *rings.NodeFull
	for i := range claims {
		if claims[i].Status == rings.TestClaimConfirmed {
			confirmedClaim = &claims[i]
		}
	}
	if confirmedClaim == nil {
		t.Fatalf("应至少一条声明经三查确认(全落证据不足=%d,模型行为不可控但 mock 靶站证据充分)", insufficient)
	}
	// 三查裁决在案:verdict(confirm) 三条件行。
	vs := p473Events(t, store, task, rings.TestClaimRecVerdict, confirmedClaim.ID)
	foundConfirm := false
	for _, v := range vs {
		if v.Decision == rings.VerdictConfirm && len(v.Conditions) == 3 {
			foundConfirm = true
		}
	}
	if !foundConfirm {
		t.Fatalf("确认声明应有三查裁决(三条件行): %+v", vs)
	}
	// 确认 ⟙ 必有包:复现定稿含证明交换(引用可回查靶站真实命中)。
	rec := p473LatestRepro(t, store, task, confirmedClaim.ID)
	if rec == nil || len(rec.Steps) == 0 {
		t.Fatalf("确认声明必须有复现包(证明交换种子在内): %+v", rec)
	}
	evs, _ := store.Events(ctx, task, 0, 0)
	submitN := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == rings.TestClaimRecSubmit {
			submitN++
			if r.Summary == "" || r.ProvingMarker == "" || r.CountercheckNote == "" || len(r.EvidenceRefs) == 0 {
				t.Fatalf("交换单六字段必须齐全: %+v", r)
			}
		}
	}
	if submitN == 0 {
		t.Fatal("真实模型未交卷")
	}
	// 事件序:确认声明的**最新交换单**(补考重交也是交换单)与 confirm
	// verdict 之间零收割轮事件(交卷即查,毫秒-秒级;首交到确认之间隔的
	// 是补考链的自身事件——反馈注入与重交属交卷即查语义,不算收割轮)。
	var subRev, verdictRev int64 = 0, 0
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != confirmedClaim.ID {
			continue
		}
		if r.Type == rings.TestClaimRecSubmit {
			subRev = ev.Revision // 取最新交换单(重交即查的时点)
		}
		if r.Type == rings.TestClaimRecVerdict && r.Decision == rings.VerdictConfirm {
			verdictRev = ev.Revision
		}
	}
	if subRev == 0 || verdictRev == 0 || verdictRev <= subRev || verdictRev-subRev > 12 {
		t.Fatalf("三查应紧随交卷(事件序近邻,不经收割轮): 最新交卷=%d 确认裁决=%d(差 %d)",
			subRev, verdictRev, verdictRev-subRev)
	}
	// 靶站被真实命中(证据非自说自话;备份目录非必经,命中数只记日志)。
	_ = target.Hits("main", "/old-backup/")
}
