package server

// P4-40 完成标准测试(确定性,无模型):
// ①~③ 草稿:暂存/列表/覆盖不新增/多份并存/删除;创建成功后对应草稿
//    删除、created 事件如常;草稿零进事件流与任务列表(口径隔离)。
// ④ 墓碑回归:不勾"同时删除相关数据和账本"语义不变。
// ⑥ 特权清除:勾选删 = 该任务各表行清零(对账)、任务目录整目录删除、
//    blobs 保留、运行日志留痕、其他任务与后续建任务不受影响。

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// p440Env 测试装配:账本 + Manager(空循环体)+ 面板后端,任务目录根
// 与服务端一致;onTerminal 可注入牧者终态回调(特权清除竞态复现用)。
func p440Env(t *testing.T, onTerminal func(taskID, status string)) (*rings.Store, *manager.Manager, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	root := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建任务目录根失败: %v", err)
	}
	mgr, err := manager.New(manager.Config{Store: store, RootDir: root, OnTerminal: onTerminal})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	srv.RootDir = root
	return store, mgr, srv.Handler(), root
}

const p440DraftBody = `{"title":"官网渗透","category":"","description":"测试 example.com",
"goals":"拿下管理权限","rows":[{"class":"domain","title":"example.com","parent":""}],
"paste":"","timeout":"600","anchor":"10",
"closure":{"on":true,"rounds":"","minutes":""},
"constraints":[{"action":"上传文件","disposition":"forbid","quota":"","scope":["example.com"],"patterns":[],"basis":"授权描述原句"}]}`

// TestP440TaskDraftAPI 草稿暂存/列表/覆盖/并存/删除 + 零事件流痕迹 +
// 任务口径隔离。
func TestP440TaskDraftAPI(t *testing.T) {
	store, _, h, _ := p440Env(t, nil)
	ctx := context.Background()

	// 对照任务:草稿操作前后事件数与任务列表完全不变(账本只记真任务)。
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-keep","title":"对照","goals":["目标"],
		"assets":[{"class":"domain","title":"example.com"}]}`, http.StatusOK, nil)
	var keepEvents int
	if evs, err := store.Events(ctx, "t-keep", 0, 0); err != nil {
		t.Fatalf("读对照任务事件失败: %v", err)
	} else {
		keepEvents = len(evs)
	}
	var tasksBefore []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasksBefore)

	// 暂存新草稿。
	var saved map[string]any
	post(t, h, http.MethodPost, "/api/task-drafts", `{"id":"","draft":`+p440DraftBody+`}`,
		http.StatusOK, &saved)
	did, _ := saved["id"].(string)
	if did == "" {
		t.Fatalf("暂存未返回草稿 ID: %+v", saved)
	}

	var list struct {
		Drafts []struct {
			ID        string         `json:"id"`
			CreatedAt string         `json:"created_at"`
			Draft     map[string]any `json:"draft"`
		} `json:"drafts"`
	}
	get(t, h, "/api/task-drafts", http.StatusOK, &list)
	if len(list.Drafts) != 1 {
		t.Fatalf("草稿列表应有 1 份,得 %d", len(list.Drafts))
	}
	d := list.Drafts[0]
	if d.ID != did || d.CreatedAt == "" {
		t.Fatalf("草稿条目不符: %+v", d)
	}
	if d.Draft["title"] != "官网渗透" || d.Draft["goals"] != "拿下管理权限" {
		t.Fatalf("草稿字段回读不符: %+v", d.Draft)
	}
	rows, _ := d.Draft["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("草稿资产行应 1 条: %+v", d.Draft["rows"])
	}

	// 覆盖暂存:同 ID 不新增,创建时间保持首存。
	post(t, h, http.MethodPost, "/api/task-drafts",
		`{"id":"`+did+`","draft":{"title":"改名后","goals":"x","rows":[],"paste":"","closure":{"on":true,"rounds":"","minutes":""},"constraints":[]}}`,
		http.StatusOK, &saved)
	if saved["id"] != did {
		t.Fatalf("覆盖暂存应保持原 ID: %+v", saved)
	}
	firstCreatedAt := d.CreatedAt
	get(t, h, "/api/task-drafts", http.StatusOK, &list)
	if len(list.Drafts) != 1 || list.Drafts[0].CreatedAt != firstCreatedAt {
		t.Fatalf("覆盖后应仍 1 份且创建时间不变: %d %+v", len(list.Drafts), list.Drafts[0])
	}

	// 多份并存 + 行内删除。
	var second map[string]any
	post(t, h, http.MethodPost, "/api/task-drafts", `{"id":"","draft":{"title":"","goals":"","rows":[],"paste":"","closure":{"on":true,"rounds":"","minutes":""},"constraints":[]}}`,
		http.StatusOK, &second)
	get(t, h, "/api/task-drafts", http.StatusOK, &list)
	if len(list.Drafts) != 2 {
		t.Fatalf("两份草稿应并存,得 %d", len(list.Drafts))
	}
	// 未命名草稿(空标题)如实保存。
	if list.Drafts[0].Draft["title"] != "" && list.Drafts[1].Draft["title"] != "" {
		t.Fatalf("第二份草稿应为空标题: %+v", list.Drafts)
	}
	post(t, h, http.MethodDelete, "/api/task-drafts/"+second["id"].(string), "", http.StatusOK, nil)
	get(t, h, "/api/task-drafts", http.StatusOK, &list)
	if len(list.Drafts) != 1 || list.Drafts[0].ID != did {
		t.Fatalf("删除后应剩原草稿 1 份: %+v", list.Drafts)
	}

	// 口径隔离:事件流零草稿痕迹、任务列表不变。
	if evs, _ := store.Events(ctx, "t-keep", 0, 0); len(evs) != keepEvents {
		t.Fatalf("草稿操作不得触碰事件流: %d != %d", len(evs), keepEvents)
	}
	if entries, err := store.SettingsWithPrefix(ctx, "task.draft."); err != nil || len(entries) != 1 {
		t.Fatalf("草稿应只存键值表 1 条: %v %d", err, len(entries))
	}
	var tasksAfter []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasksAfter)
	if len(tasksAfter) != len(tasksBefore) {
		t.Fatalf("草稿不得计入任务列表: %d != %d", len(tasksAfter), len(tasksBefore))
	}
}

// TestP440DraftPromotionOnCreate 从草稿创建任务:成功后对应草稿删除,
// created 事件如常,任务入列。
func TestP440DraftPromotionOnCreate(t *testing.T) {
	store, _, h, _ := p440Env(t, nil)

	var saved map[string]any
	post(t, h, http.MethodPost, "/api/task-drafts", `{"id":"","draft":`+p440DraftBody+`}`,
		http.StatusOK, &saved)
	did := saved["id"].(string)

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-from-draft","title":"官网渗透",
		"goals":["拿下管理权限"],"assets":[{"class":"domain","title":"example.com"}],
		"timeout_seconds":600,"anchor_every_steps":10,"draft_id":"`+did+`"}`,
		http.StatusOK, &created)
	if created["id"] != "t-from-draft" {
		t.Fatalf("建任务响应不符: %+v", created)
	}

	// 草稿已随创建删除;任务 created 事件在账本。
	var list struct {
		Drafts []map[string]any `json:"drafts"`
	}
	get(t, h, "/api/task-drafts", http.StatusOK, &list)
	if len(list.Drafts) != 0 {
		t.Fatalf("创建成功后草稿应删除: %+v", list.Drafts)
	}
	evs, err := store.Events(context.Background(), "t-from-draft", 0, 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("任务事件应如常入账: %v %d", err, len(evs))
	}
	// 任务正常入列。
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	found := false
	for _, tk := range tasks {
		if tk["id"] == "t-from-draft" {
			found = true
		}
	}
	if !found {
		t.Fatalf("从草稿创建的任务应入列: %+v", tasks)
	}
}

// TestP440DeleteTombstoneRegression 不勾"同时删除相关数据和账本" =
// 墓碑语义一字不变:列表移除、账本历史保留、牧者终态总结照常触发。
func TestP440DeleteTombstoneRegression(t *testing.T) {
	var fired atomic.Bool
	store, _, h, _ := p440Env(t, func(taskID, status string) {
		if taskID == "t-tomb" {
			fired.Store(true)
		}
	})
	ctx := context.Background()
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-tomb","title":"墓碑","goals":["目标"],
		"assets":[{"class":"domain","title":"example.com"}]}`, http.StatusOK, nil)

	post(t, h, http.MethodDelete, "/api/tasks/t-tomb", `{"reason":"面板删除"}`,
		http.StatusOK, nil)
	deadline := time.Now().Add(time.Second)
	for !fired.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fired.Load() {
		t.Fatalf("墓碑删除的停止应照常触发终态总结回调(语义不变)")
	}

	row, err := store.Task(ctx, "t-tomb")
	if err != nil || !row.Deleted {
		t.Fatalf("墓碑任务应保留任务行且带删除标记: %v %+v", err, row)
	}
	if evs, _ := store.Events(ctx, "t-tomb", 0, 0); len(evs) == 0 {
		t.Fatalf("墓碑语义应保留全部事件")
	}
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	for _, t2 := range tasks {
		if t2["id"] == "t-tomb" {
			t.Fatalf("墓碑任务不应出现在列表")
		}
	}
}

// TestP440DeletePurge 勾选"同时删除相关数据和账本" = 特权清除:各表行
// 清零、任务目录整目录删除、blobs 保留、运行日志一行留痕、其他任务
// 与后续建任务不受影响。注入延迟写账的牧者终态回调复现清除竞态——
// 勾选删应静默停止(回调不触发),迟到写入被账本拒收,数据不复活。
func TestP440DeletePurge(t *testing.T) {
	var fired atomic.Bool
	var ref struct {
		mu    sync.Mutex
		store *rings.Store
		root  string
	}
	onTerminal := func(taskID, status string) {
		if taskID != "t-purge" {
			return
		}
		// 模拟牧者终态总结的异步链:清除返回后才落笔。
		time.Sleep(30 * time.Millisecond)
		fired.Store(true)
		ref.mu.Lock()
		s, root := ref.store, ref.root
		ref.mu.Unlock()
		if s == nil {
			return
		}
		_, _ = s.Append(context.Background(), taskID, rings.Event{
			Kind: rings.EventShepherdRecord, Actor: "system",
			Payload: rings.ShepherdRecord{MessageID: "sn-x", Type: rings.ShepherdRecSystemNote, Text: "迟到总结"},
		})
		_ = os.MkdirAll(filepath.Join(root, taskID, "transcripts"), 0o755)
		_ = os.WriteFile(filepath.Join(root, taskID, "transcripts", "shepherd.jsonl"), []byte("{}"), 0o644)
	}
	store, _, h, root := p440Env(t, onTerminal)
	ref.mu.Lock()
	ref.store, ref.root = store, root
	ref.mu.Unlock()
	ctx := context.Background()

	// 运行日志捕获(特权清除留痕行断言用)。
	logDir := filepath.Join(t.TempDir(), "logs")
	rl, err := runlog.Open(logDir, runlog.Options{NoStderr: true})
	if err != nil {
		t.Fatalf("开运行日志失败: %v", err)
	}
	defer rl.Close()

	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-purge","title":"清除对象","goals":["目标"],
		"assets":[{"class":"domain","title":"example.com"}]}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-stay","title":"保留对照","goals":["目标"],
		"assets":[{"class":"domain","title":"keep.example.com"}]}`, http.StatusOK, nil)

	// 任务工作目录转录 + 共享 blobs 各造假文件:整目录删、blobs 不动。
	sub := filepath.Join(root, "t-purge", "transcripts")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("建转录目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "swarm-u1.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("写转录文件失败: %v", err)
	}
	blobPath := filepath.Join(root, "blobs", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		t.Fatalf("建 blobs 目录失败: %v", err)
	}
	if err := os.WriteFile(blobPath, []byte("shared"), 0o644); err != nil {
		t.Fatalf("写 blob 文件失败: %v", err)
	}

	post(t, h, http.MethodDelete, "/api/tasks/t-purge", `{"reason":"面板删除","purge":true}`,
		http.StatusOK, nil)
	// 等过延迟写者窗口再对账(竞态复现口径)。
	time.Sleep(120 * time.Millisecond)

	// 勾选删路径静默停止:终态回调不触发,无迟到写入。
	if fired.Load() {
		t.Fatalf("特权清除的停止不应触发终态总结回调")
	}
	// 各表行清零对账(读侧)。
	if _, err := store.Task(ctx, "t-purge"); err == nil {
		t.Fatalf("特权清除后任务行应不可读")
	}
	if evs, _ := store.Events(ctx, "t-purge", 0, 0); len(evs) != 0 {
		t.Fatalf("事件应清零,剩 %d", len(evs))
	}
	if nodes, _ := store.NodesByType(ctx, "t-purge", rings.NodeGoal, ""); len(nodes) != 0 {
		t.Fatalf("目标节点应清零")
	}
	// 目录删除、blobs 保留。
	if _, err := os.Stat(filepath.Join(root, "t-purge")); !os.IsNotExist(err) {
		t.Fatalf("任务目录应整目录删除: %v", err)
	}
	if b, err := os.ReadFile(blobPath); err != nil || string(b) != "shared" {
		t.Fatalf("blobs 应保留不动: %v %q", err, string(b))
	}
	// 对照任务不受影响;运行日志留痕。
	if row, err := store.Task(ctx, "t-stay"); err != nil || row.ID != "t-stay" {
		t.Fatalf("对照任务应完好: %v", err)
	}
	logText := p440ReadLogs(t, logDir)
	if !strings.Contains(logText, "特权清除: 任务 t-purge") || !strings.Contains(logText, "账本 ") {
		t.Fatalf("运行日志应留特权清除行(任务/行数/目录): %s", logText)
	}
	// 删除后新任务照常创建。
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"t-after","title":"清除后再建","goals":["目标"],
		"assets":[{"class":"domain","title":"after.example.com"}]}`, http.StatusOK, nil)
	if row, err := store.Task(ctx, "t-after"); err != nil || row.ID != "t-after" {
		t.Fatalf("清除后建任务应正常: %v", err)
	}
}

// TestP440DraftSaveQuietInRunlog 草稿写入键值表不落"配置修改"日志行
// (草稿保存不是配置修改);常规配置写照常留痕。
func TestP440DraftSaveQuietInRunlog(t *testing.T) {
	_, _, h, _ := p440Env(t, nil)
	logDir := filepath.Join(t.TempDir(), "logs")
	rl, err := runlog.Open(logDir, runlog.Options{NoStderr: true})
	if err != nil {
		t.Fatalf("开运行日志失败: %v", err)
	}
	defer rl.Close()

	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"渗透"}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/task-drafts", `{"id":"","draft":{"title":"x","goals":"","rows":[],"paste":"","closure":{"on":true,"rounds":"","minutes":""},"constraints":[]}}`,
		http.StatusOK, nil)

	logText := p440ReadLogs(t, logDir)
	if strings.Contains(logText, "task.draft") {
		t.Fatalf("草稿保存不应落配置修改日志行: %s", logText)
	}
	if !strings.Contains(logText, "配置修改: task.category.") {
		t.Fatalf("常规配置写应照常留痕: %s", logText)
	}
}

// p440ReadLogs 拼读日志目录全部文件(留痕断言用)。
func p440ReadLogs(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		b.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("读日志目录失败: %v", err)
	}
	return b.String()
}

// TestP440bPurgeSweepsOrphanBlobs 特权清除清无主 blob(P4-40 续做 ①):
// 两任务共享同一 blob → 勾选删其一 → 共享 blob 保留、另一任务存档
// (证据导出通道)照常;仅本任务引用的 blob 与无任何引用的 blob 删除;
// 墓碑删除 blobs 零变化;留痕行带无主数据项数与字节数。
func TestP440bPurgeSweepsOrphanBlobs(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	rl, err := runlog.Open(logDir, runlog.Options{NoStderr: true})
	if err != nil {
		t.Fatalf("开运行日志失败: %v", err)
	}
	defer rl.Close()

	store, _, h, root := p440Env(t, nil)
	ctx := context.Background()
	blobs := swarm.NewBlobStore(filepath.Join(root, "blobs"))

	sharedRef, err := blobs.Write([]byte("shared-evidence"))
	if err != nil {
		t.Fatalf("写共享 blob 失败: %v", err)
	}
	onlyARef, err := blobs.Write([]byte("only-a"))
	if err != nil {
		t.Fatalf("写 A 独占 blob 失败: %v", err)
	}
	orphanRef, err := blobs.Write([]byte("nobody"))
	if err != nil {
		t.Fatalf("写无主 blob 失败: %v", err)
	}
	onlyCRef, err := blobs.Write([]byte("only-c"))
	if err != nil {
		t.Fatalf("写 C 独占 blob 失败: %v", err)
	}

	// 任务 A 引用共享 + A 独占;任务 B 引用共享;任务 C 引用 C 独占
	//(墓碑对照)。
	for _, task := range []struct {
		id    string
		ref   string
		extra string
	}{
		{"t-a", sharedRef, " 与独占 " + onlyARef},
		{"t-b", sharedRef, ""},
		{"t-c", onlyCRef, ""},
	} {
		post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","id":"`+task.id+`","title":"`+task.id+`","goals":["目标"],
			"assets":[{"class":"domain","title":"`+task.id+`.example.com"}]}`, http.StatusOK, nil)
		if _, err := store.Append(ctx, task.id, rings.Event{
			Kind: rings.EventShepherdRecord, Actor: "swarm:u1",
			Payload: rings.ShepherdRecord{MessageID: "sn-" + task.id, Type: rings.ShepherdRecSystemNote,
				Text: "证据原文 " + task.ref + task.extra},
		}); err != nil {
			t.Fatalf("写引用事件失败: %v", err)
		}
	}

	// 勾选删 A:共享保留、A 独占与无主 blob 删除。
	post(t, h, http.MethodDelete, "/api/tasks/t-a", `{"reason":"活体验收","purge":true}`, http.StatusOK, nil)
	if !blobs.Exists(sharedRef) {
		t.Fatalf("共享 blob(仍被 t-b 引用)不得删除")
	}
	if got, rerr := blobs.Read(sharedRef); rerr != nil || string(got) != "shared-evidence" {
		t.Fatalf("共享 blob 内容不符: %v %q", rerr, string(got))
	}
	if blobs.Exists(onlyARef) {
		t.Fatalf("仅被 A 引用的 blob 应随特权清除删除")
	}
	if blobs.Exists(orphanRef) {
		t.Fatalf("无任何任务引用的 blob 应删除")
	}
	if !blobs.Exists(onlyCRef) {
		t.Fatalf("墓碑对照 blob 不应受影响")
	}
	// 留痕行:项数与字节数(only-a 6 字节 + nobody 6 字节)。
	logText := p440ReadLogs(t, logDir)
	if !strings.Contains(logText, "特权清除: 任务 t-a") || !strings.Contains(logText, "无主数据 2 项 12 字节") {
		t.Fatalf("留痕行应含无主数据项数与字节数: %s", logText)
	}
	// 另一任务证据导出通道照常:存档一次(报告原文入 blob + 登记)。
	_, evVer, err := store.EvidenceVersions(ctx, "t-b")
	if err != nil {
		t.Fatalf("读证据版本失败: %v", err)
	}
	av, err := report.ArchiveTask(ctx, store, blobs, "t-b", evVer, "creator")
	if err != nil {
		t.Fatalf("清除后他任务存档(证据导出通道)失败: %v", err)
	}
	if body, rerr := report.ArchiveBody(blobs, av.Ref); rerr != nil || len(body) == 0 {
		t.Fatalf("存档原文读回失败: %v", rerr)
	}

	// 墓碑删除(不勾):blobs 零变化。
	post(t, h, http.MethodDelete, "/api/tasks/t-c", `{"reason":"面板删除"}`, http.StatusOK, nil)
	for _, ref := range []string{sharedRef, onlyCRef} {
		if !blobs.Exists(ref) {
			t.Fatalf("墓碑删除不得动 blobs: %s", ref)
		}
	}
}
