package report

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P3-02 报告存档:乐观锁(旧证据版本 409 语义)、原文入 blob、登记入账、
// 补绑后旧存档自动"待更新"。

func openArchStore(t *testing.T) (*rings.Store, *swarm.BlobStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, swarm.NewBlobStore(filepath.Join(dir, "tasks", "blobs"))
}

func seedArchiveTask(t *testing.T, store *rings.Store, blobs *swarm.BlobStore, task string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "存档验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "备份目录匿名可读", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: "web1"}},
		// 一条可绑定的镜留痕(真 blob 文档)。
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: rings.MirrorRecord{
			ExchangeID: "ex-1", Type: rings.MirrorRecHTTP, StepID: "s1", UnitID: "u1",
			Method: "GET", URL: "http://mock/old-backup/", Status: 200,
			BlobRef: mustMirrorBlob(t, blobs, "ex-1")},
		},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
}

// mustMirrorBlob 写一份真实交换文档入 blob,返回引用。
func mustMirrorBlob(t *testing.T, blobs *swarm.BlobStore, exID string) string {
	t.Helper()
	doc := []byte(`{"id":"` + exID + `","type":"http","task_id":"tA","step_id":"s1","unit_id":"u1",` +
		`"request":{"method":"GET","url":"http://mock/old-backup/","proto":"HTTP/1.1","host":"mock",` +
		`"headers":[{"name":"User-Agent","value":"probe/1"}]},"started_at":"2026-10-10T09:00:00Z",` +
		`"response":{"proto":"HTTP/1.1","status":200,"status_text":"OK","headers":[],"body":"b2FjayBsaXN0"}}`)
	ref, err := blobs.Write(doc)
	if err != nil {
		t.Fatalf("交换文档入 blob 失败: %v", err)
	}
	return ref
}

func TestArchiveOptimisticLockAndStale(t *testing.T) {
	store, blobs := openArchStore(t)
	ctx := context.Background()
	const task = "arch-1"
	seedArchiveTask(t, store, blobs, task)

	// 证据版本 0(尚无绑定/定稿):存档通过。
	mdBefore, err := Render(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	view, err := ArchiveTask(ctx, store, blobs, task, 0, "creator")
	if err != nil {
		t.Fatalf("首次存档应通过: %v", err)
	}
	if view.EvidenceVersion != 0 || view.Stale {
		t.Fatalf("存档视图锚点错误: %+v", view)
	}
	if view.Size == 0 || view.Ref == "" {
		t.Fatalf("存档应有原文与引用: %+v", view)
	}

	// 原文回放 = 存档时刻的渲染全文(字节级)。存档之后账本多了登记
	// 事件,随后的渲染自然不同(审计摘要如实计入)——快照语义锚定
	// 存档时刻,不追账本。
	body, err := ArchiveBody(blobs, view.Ref)
	if err != nil {
		t.Fatalf("存档原文回放失败: %v", err)
	}
	if string(body) != mdBefore {
		t.Fatalf("存档原文须与存档时刻的渲染全文逐字一致(%d vs %d 字节)", len(body), len(mdBefore))
	}

	// 证据更新(补绑一条):任务证据版本 0 → 1。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "creator",
		Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecEvidence,
			Exchanges: []string{"ex-1"}, By: "creator"},
	}); err != nil {
		t.Fatalf("补绑失败: %v", err)
	}

	// 旧证据版本(0)写入被拒——乐观锁。
	_, err = ArchiveTask(ctx, store, blobs, task, 0, "creator")
	var stale *ErrEvidenceStale
	if !errors.As(err, &stale) || stale.Current != 1 || stale.Submitted != 0 {
		t.Fatalf("旧版本存档应被拒(ErrEvidenceStale),得 %v", err)
	}

	// 新版本(1)存档通过;清单里旧存档自动"待更新"。
	v2, err := ArchiveTask(ctx, store, blobs, task, 1, "creator")
	if err != nil {
		t.Fatalf("新版本存档应通过: %v", err)
	}
	list, current, err := Archives(ctx, store, task)
	if err != nil {
		t.Fatalf("存档清单失败: %v", err)
	}
	if current != 1 {
		t.Fatalf("当前证据版本应为 1,得 %d", current)
	}
	if len(list) != 2 {
		t.Fatalf("应 2 份存档,得 %d", len(list))
	}
	// 倒序:最新在前。
	if list[0].Rev != v2.Rev || list[0].Stale {
		t.Fatalf("最新存档(证据版本 1 = 当前)不应待更新: %+v", list[0])
	}
	if list[1].Rev != view.Rev || !list[1].Stale {
		t.Fatalf("旧存档(证据版本 0)应自动待更新: %+v", list[1])
	}

	// ArchiveByRev 定位 + 未知版本。
	rec, err := ArchiveByRev(ctx, store, task, view.Rev)
	if err != nil || rec.Ref != view.Ref {
		t.Fatalf("按版本号定位存档失败: %v %+v", err, rec)
	}
	if _, err := ArchiveByRev(ctx, store, task, 9999); err == nil || !strings.Contains(err.Error(), "无报告存档") {
		t.Fatalf("未知版本应报无存档,得 %v", err)
	}
}

// TestArchiveEmptyTask 空任务(无发现)也可存档:证据版本 0,渲染如实。
func TestArchiveEmptyTask(t *testing.T) {
	store, blobs := openArchStore(t)
	ctx := context.Background()
	const task = "arch-empty"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "空"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	view, err := ArchiveTask(ctx, store, blobs, task, 0, "creator")
	if err != nil {
		t.Fatalf("空任务存档应通过: %v", err)
	}
	if view.Size == 0 {
		t.Fatal("空任务报告仍有正文,尺寸非零")
	}
	if _, _, err := Archives(ctx, store, task); err != nil {
		t.Fatalf("空任务存档清单失败: %v", err)
	}
	// 时间锚点:登记事件的 OccurredAt 供面板呈现。
	if view.At.IsZero() || view.At.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("存档时刻异常: %v", view.At)
	}
}

// TestClaimCaseReproWithoutStandard 无冻结标准的声明,卷宗装配不得丢
// Repro 定稿(审核迭代 2 修:旧实现 FrozenAlternatives 出错即 continue,
// 连定稿复现步骤一起跳过——解释解析失败只降级解释区)。
func TestClaimCaseReproWithoutStandard(t *testing.T) {
	store, blobs := openArchStore(t)
	ctx := context.Background()
	const task = "t-no-std"
	ref := mustMirrorBlob(t, blobs, "ex-9")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "无标准"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-x", NodeType: rings.NodeTestClaim, Title: "声明(无冻结标准)",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露"}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind",
			Payload: rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
				Steps: []rings.ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2",
					Method: "GET", URL: "http://mock/x", Status: 200, BlobRef: ref}}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	cs, err := ClaimCaseOf(ctx, store, blobs, task, "tc-x")
	if err != nil {
		t.Fatalf("卷宗装配失败: %v", err)
	}
	if cs.Repro == nil || len(cs.Repro.Steps) != 1 || cs.Repro.Steps[0].Raw == "" {
		t.Fatalf("无冻结标准的声明不得丢定稿: %+v", cs.Repro)
	}
	if cs.Repro.Severity != "high" {
		t.Fatalf("定稿级别错误: %s", cs.Repro.Severity)
	}
	// 解释区如实为空(降级不造数据)。
	if len(cs.Alternatives) != 0 {
		t.Fatalf("解释区应为空: %+v", cs.Alternatives)
	}
}
