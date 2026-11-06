package server

// P2-06h 任务列表完整版后端契约:列表投影补全(描述/目标/分类/置顶/
// 目标进度/运行中单位数)、分类 CRUD 与指派、置顶排序、墓碑删除、
// 批量熔断/删除、投影重放确定性。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// metaSrv 常规装配:无 LLM(OnTaskRun 空)——Manager 走空转骨架,
// 生命周期与元数据 API 照常。
func metaSrv(t *testing.T) (*Server, *rings.Store, *manager.Manager) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
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
	return srv, store, mgr
}

// seedStepAndGoals 直接经账本受控入口落一个 running 步骤与两个目标
// (其一达成)——列表"运行中单位数 / 目标进度"两列的图上真值。
func seedStepAndGoals(t *testing.T, store *rings.Store, taskID string) {
	t.Helper()
	ctx := context.Background()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标一", Status: "active"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g2", NodeType: rings.NodeGoal, Title: "目标二", Status: "active"}},
		{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "g1", Status: "achieved"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测意图", Status: "dispatched"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", Payload: rings.NodeStateChanged{
			NodeID: "s1", Status: "running"}},
	}
	if _, err := store.Append(ctx, taskID, events...); err != nil {
		t.Fatalf("落步骤与目标失败: %v", err)
	}
}

func findTask(list []map[string]any, id string) map[string]any {
	for _, t := range list {
		if t["id"] == id {
			return t
		}
	}
	return nil
}

// TestTaskListFullProjection 建任务(描述 + 种子 + 分类)→ 图上真值
// (目标 1/2 达成、1 单位运行)→ 列表 API 各列对账;置顶任务排最前;
// 暂停后描述仍在(created 事件锚点,不被生命周期 detail 覆盖)。
func TestTaskListFullProjection(t *testing.T) {
	srv, store, _ := metaSrv(t)
	h := srv.Handler()

	var catCreated map[string]any
	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"内网演练"}`, http.StatusOK, &catCreated)
	catID, _ := catCreated["id"].(string)
	if catID == "" {
		t.Fatalf("建分类未返回 id: %s", mustJSON(catCreated))
	}
	post(t, h, http.MethodPost, "/api/tasks", fmt.Sprintf(`{"task_type":"redteam","goals":["种子目标"],
		"id":"full-a","title":"任务甲","description":"首轮边界测绘,两目标",
		"category":%q,"timeout_seconds":600,
		"assets":[{"class":"domain","title":"example.com"},
		          {"class":"service","title":"http://10.10.0.5:8080"}]}`, catID), http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"full-b","title":"任务乙"}`, http.StatusOK, nil)
	seedStepAndGoals(t, store, "full-a")

	// 置顶 full-b:排序应在 full-a 之前(置顶在前)。
	post(t, h, http.MethodPost, "/api/tasks/full-b/meta", `{"pinned":true}`, http.StatusOK, nil)

	var list []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &list)
	if len(list) != 2 {
		t.Fatalf("任务数 = %d, want 2(%s)", len(list), mustJSON(list))
	}
	if list[0]["id"] != "full-b" || list[0]["pinned"] != true {
		t.Fatalf("置顶任务应排最前: %s", mustJSON(list))
	}
	a := findTask(list, "full-a")
	if a == nil {
		t.Fatalf("缺 full-a: %s", mustJSON(list))
	}
	if a["description"] != "首轮边界测绘,两目标" {
		t.Fatalf("描述列 = %v", a["description"])
	}
	targets, _ := a["targets"].([]any)
	if len(targets) != 2 || targets[0] != "example.com" || targets[1] != "http://10.10.0.5:8080" {
		t.Fatalf("目标列 = %v", targets)
	}
	if a["category"] != catID {
		t.Fatalf("分类列 = %v", a["category"])
	}
	// P4-16:建任务必带目标,full-a 图上 = 建单种子目标 + seedStepAndGoals
	// 的两目标,达成 1(g1)。
	if a["goals_achieved"] != float64(1) || a["goals_total"] != float64(3) {
		t.Fatalf("目标进度 = %v/%v", a["goals_achieved"], a["goals_total"])
	}
	if a["running_units"] != float64(1) {
		t.Fatalf("运行中单位数 = %v", a["running_units"])
	}

	// 暂停携带 detail 的生命周期事件后,描述仍在(created 事件锚点)。
	if err := srv.Manager.Pause("full-a"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	if _, err := store.Append(context.Background(), "full-a", rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "paused", Detail: map[string]any{"reason": "测试覆盖 detail"}},
	}); err != nil {
		t.Fatalf("记账失败: %v", err)
	}
	var afterPause []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &afterPause)
	if a = findTask(afterPause, "full-a"); a == nil || a["description"] != "首轮边界测绘,两目标" {
		t.Fatalf("生命周期 detail 覆盖后描述丢失: %s", mustJSON(a))
	}
}

// TestTaskCategoryCRUD 分类增删改查:重名拒、改名、指派校验(不存在拒)、
// 删除分类自动移出全部任务(逐任务 task.meta 入账)。
func TestTaskCategoryCRUD(t *testing.T) {
	srv, store, _ := metaSrv(t)
	h := srv.Handler()

	var created map[string]any
	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"分组一"}`, http.StatusOK, &created)
	catID, _ := created["id"].(string)
	if catID == "" {
		t.Fatalf("建分类未返回 id: %s", mustJSON(created))
	}
	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"分组一"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"  "}`, http.StatusBadRequest, nil)

	var catList struct {
		Categories []map[string]any `json:"categories"`
	}
	get(t, h, "/api/task-categories", http.StatusOK, &catList)
	if len(catList.Categories) != 1 || catList.Categories[0]["name"] != "分组一" {
		t.Fatalf("分类清单 = %s", mustJSON(catList))
	}

	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"cat-t1","title":"分类验证"}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"cat-t2","title":"分类验证二"}`, http.StatusOK, nil)
	// 指派:合法 ID 通过;不存在拒;空串移出。
	post(t, h, http.MethodPost, "/api/tasks/cat-t1/meta", fmt.Sprintf(`{"category":%q}`, catID), http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks/cat-t2/meta", `{"category":"cat-nope"}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/cat-t2/meta", `{}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, fmt.Sprintf("/api/task-categories/%s", catID), `{"name":"分组二"}`, http.StatusOK, nil)

	get(t, h, "/api/task-categories", http.StatusOK, &catList)
	if catList.Categories[0]["name"] != "分组二" {
		t.Fatalf("改名未生效: %s", mustJSON(catList))
	}

	// 指派过 catID 的任务改名后指派仍在;删除分类应移出指派。
	//(每次 GET 用全新切片:json.Unmarshal 对既有 map 按 key 合并,
	// 复用变量会让被 omitempty 省略的字段残留旧值,读到假数据。)
	var list []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &list)
	if t1 := findTask(list, "cat-t1"); t1["category"] != catID {
		t.Fatalf("指派未生效: %s", mustJSON(t1))
	}
	post(t, h, http.MethodDelete, fmt.Sprintf("/api/task-categories/%s", catID), `{}`, http.StatusOK, nil)
	var after []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &after)
	if t1 := findTask(after, "cat-t1"); t1["category"] != nil && t1["category"] != "" {
		t.Fatalf("删除分类后指派残留: %s", mustJSON(t1))
	}
	// 移出入账可查:事件流里有该任务的 task.meta(category 置空)。
	evs, err := store.Events(context.Background(), "cat-t1", 0, 0)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	metaSeen := false
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskMeta {
			continue
		}
		var p rings.TaskMeta
		if json.Unmarshal(ev.Payload, &p) == nil && p.Category != nil && *p.Category == "" {
			metaSeen = true
		}
	}
	if !metaSeen {
		t.Fatalf("删除分类的移出未入账")
	}
}

// TestTaskTombstoneDelete 墓碑删除:运行中任务先停后埋(账本两事件
// 可查)、列表隐藏、幂等、已删任务拒改元数据;事件流历史完整保留。
func TestTaskTombstoneDelete(t *testing.T) {
	srv, store, _ := metaSrv(t)
	h := srv.Handler()

	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"tomb-t","title":"删除验证"}`, http.StatusOK, nil)
	// 无理由拒删。
	post(t, h, http.MethodDelete, "/api/tasks/tomb-t", `{}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodDelete, "/api/tasks/tomb-t", `{"reason":"演练清理"}`, http.StatusOK, nil)

	row, err := store.Task(context.Background(), "tomb-t")
	if err != nil || !row.Deleted {
		t.Fatalf("墓碑未落: %v %+v", err, row)
	}
	if row.Status != "stopped" {
		t.Fatalf("删除前未停止,状态 = %s", row.Status)
	}
	var list []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &list)
	if findTask(list, "tomb-t") != nil {
		t.Fatalf("墓碑任务仍出现在列表")
	}
	// 幂等:再删一次 200;已删任务拒改分类/置顶。
	post(t, h, http.MethodDelete, "/api/tasks/tomb-t", `{"reason":"再删一次"}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks/tomb-t/meta", `{"pinned":true}`, http.StatusBadRequest, nil)

	// 账本可查:stopped(删除前停止)+ task.meta(deleted+理由)。
	evs, err := store.Events(context.Background(), "tomb-t", 0, 0)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	stopReason, tombReason := "", ""
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventTaskLifecycle:
			var p rings.TaskLifecycle
			if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "stopped" {
				if r, ok := p.Detail["reason"].(string); ok {
					stopReason = r
				}
			}
		case rings.EventTaskMeta:
			var p rings.TaskMeta
			if json.Unmarshal(ev.Payload, &p) == nil && p.Deleted {
				tombReason = p.Reason
			}
		}
	}
	if stopReason != "删除前停止: 演练清理" || tombReason != "演练清理" {
		t.Fatalf("删除留痕不全: stop=%q tomb=%q", stopReason, tombReason)
	}
}

// TestTaskBatchOps 批量熔断/删除:逐任务走单项权威路径,单任务失败
// (不存在)不拖垮其余,结果逐项回报。
func TestTaskBatchOps(t *testing.T) {
	srv, _, _ := metaSrv(t)
	h := srv.Handler()

	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"bat-1","title":"批量一"}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"bat-2","title":"批量二"}`, http.StatusOK, nil)

	var res struct {
		Done     int `json:"done"`
		Failures []struct {
			ID string `json:"id"`
		} `json:"failures"`
	}
	post(t, h, http.MethodPost, "/api/tasks/batch",
		`{"ids":["bat-1","bat-2","ghost"],"action":"stop","reason":"批量演练"}`,
		http.StatusOK, &res)
	if res.Done != 2 || len(res.Failures) != 1 || res.Failures[0].ID != "ghost" {
		t.Fatalf("批量熔断结果 = %s", mustJSON(res))
	}
	for _, id := range []string{"bat-1", "bat-2"} {
		if st := srv.Manager.Task(id).Status(); st != "stopped" {
			t.Fatalf("任务 %s 状态 = %s, want stopped", id, st)
		}
	}

	post(t, h, http.MethodPost, "/api/tasks/batch",
		`{"ids":["bat-1","bat-2"],"action":"delete","reason":"批量清理"}`,
		http.StatusOK, &res)
	if res.Done != 2 || len(res.Failures) != 0 {
		t.Fatalf("批量删除结果 = %s", mustJSON(res))
	}
	var list []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("批量删除后列表应为空: %s", mustJSON(list))
	}
	// 未知动作拒(hibernate 是 P2-08 起的合法批量动作,用真未知词)。
	post(t, h, http.MethodPost, "/api/tasks/batch", `{"ids":["x"],"action":"teleport","reason":"r"}`, http.StatusBadRequest, nil)
}

// TestTaskMetaReplayDeterminism 元数据投影的重放确定性:分类/置顶/
// 墓碑经全量重放后逐列复原(增量维护与全量重放同一份 applyEvent)。
func TestTaskMetaReplayDeterminism(t *testing.T) {
	srv, store, _ := metaSrv(t)
	h := srv.Handler()

	var catCreated map[string]any
	post(t, h, http.MethodPost, "/api/task-categories", `{"name":"重放验证"}`, http.StatusOK, &catCreated)
	catID, _ := catCreated["id"].(string)
	post(t, h, http.MethodPost, "/api/tasks",
		fmt.Sprintf(`{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"replay-t","title":"重放","category":%q}`, catID), http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks/replay-t/meta", `{"pinned":true}`, http.StatusOK, nil)
	post(t, h, http.MethodDelete, "/api/tasks/replay-t", `{"reason":"重放后删"}`, http.StatusOK, nil)

	before, err := store.Task(context.Background(), "replay-t")
	if err != nil {
		t.Fatalf("读任务失败: %v", err)
	}
	if err := store.RebuildProjections(context.Background()); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	after, err := store.Task(context.Background(), "replay-t")
	if err != nil {
		t.Fatalf("重放后读任务失败: %v", err)
	}
	if after.Category != before.Category || after.Pinned != before.Pinned ||
		after.Deleted != before.Deleted || after.Status != before.Status {
		t.Fatalf("重放前后不一致: before=%+v after=%+v", before, after)
	}
}

// mustJSON 测试内紧凑呈现。
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
