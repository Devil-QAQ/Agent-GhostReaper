package server

// P4-34 任务并发入系统设置:/api/config system 段生效视图(缺省 2 +
// 覆盖审计)、POST /api/config/system 写校验(1–16 越界人话拒改、清空
// 回默认、未提交拒改)、调大保存即触发队列扫描拉起排队任务。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
)

// readTaskConcurrency 读 /api/config 的 system.task_concurrency 段。
func readTaskConcurrency(t *testing.T, h http.Handler) agentIntParam {
	t.Helper()
	var cfg struct {
		System struct {
			TaskConcurrency agentIntParam `json:"task_concurrency"`
		} `json:"system"`
	}
	get(t, h, "/api/config", http.StatusOK, &cfg)
	return cfg.System.TaskConcurrency
}

// TestTaskConcurrencyConfigAPI 任务并发读写:缺省 2、写后同源返回与
// 回读一致、越界/非整数 400 不落库、清空删键回默认、空体 400。
func TestTaskConcurrencyConfigAPI(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	ctx := context.Background()

	// 缺省:值 2 未覆盖(默认安装行为与现状一致)。
	if p := readTaskConcurrency(t, h); p.Value != 2 || p.Default != 2 || p.Overridden {
		t.Fatalf("缺省任务并发应为 2/未覆盖: %+v", p)
	}

	// 写 3:同源返回覆盖视图,键落库。
	var sv ConfigView
	post(t, h, "POST", "/api/config/system", `{"task_concurrency":"3"}`, http.StatusOK, &sv)
	if p := sv.System.TaskConcurrency; p.Value != 3 || !p.Overridden {
		t.Fatalf("写后同源返回不符: %+v", p)
	}
	if p := readTaskConcurrency(t, h); p.Value != 3 || !p.Overridden || p.By != "creator" {
		t.Fatalf("回读不符: %+v", p)
	}
	if e, ok, _ := store.Setting(ctx, manager.SettingKeyMaxConcurrentTasks); !ok || e.Value != "3" {
		t.Fatalf("并发键未落库: %v %v", ok, e)
	}

	// 越界与非整数:400 人话提示,键不改动。
	for _, bad := range []string{`{"task_concurrency":"0"}`, `{"task_concurrency":"17"}`, `{"task_concurrency":"abc"}`, `{"task_concurrency":" 8x"}`} {
		rec := doReq(t, h, http.MethodPost, "/api/config/system", bad)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "任务并发须为 1–16 的整数") {
			t.Fatalf("POST %s = %d %s,应 400 含人话提示", bad, rec.Code, rec.Body.String())
		}
	}
	if e, ok, _ := store.Setting(ctx, manager.SettingKeyMaxConcurrentTasks); !ok || e.Value != "3" {
		t.Fatalf("拒改后键不应变动: %v %v", ok, e)
	}

	// 空体/缺字段:400。
	rec := doReq(t, h, http.MethodPost, "/api/config/system", `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "未提交") {
		t.Fatalf("空体应 400 未提交,实际 %d %s", rec.Code, rec.Body.String())
	}

	// 清空(空串):删键回默认 2。
	post(t, h, "POST", "/api/config/system", `{"task_concurrency":"  "}`, http.StatusOK, &sv)
	if p := sv.System.TaskConcurrency; p.Value != 2 || p.Overridden {
		t.Fatalf("清空后应回默认 2: %+v", p)
	}
	if _, ok, _ := store.Setting(ctx, manager.SettingKeyMaxConcurrentTasks); ok {
		t.Fatal("清空应删键")
	}
}

// TestTaskConcurrencyScanOnSave 调大保存即触发队列扫描:并发占满 2 且
// 第三任务排队中 → 改 3 → 排队任务立即自动拉起(started 落账,无重启)。
func TestTaskConcurrencyScanOnSave(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()
	mgr := srv.Manager
	ctx := context.Background()

	for _, id := range []string{"tc-s1", "tc-s2", "tc-s3"} {
		if _, err := mgr.CreateTask(ctx, id, id, rings.RoeUpdated{}, nil); err != nil {
			t.Fatalf("创建 %s 失败: %v", id, err)
		}
	}
	if s1, s2, s3 := mgr.Task("tc-s1").Status(), mgr.Task("tc-s2").Status(), mgr.Task("tc-s3").Status(); s1 != manager.StatusStarted || s2 != manager.StatusStarted || s3 != manager.StatusQueued {
		t.Fatalf("缺省 2 应前两个起跑第三个排队: %s %s %s", s1, s2, s3)
	}

	// 面板改并发为 3:保存即拉起排队中的第三个(同步完成,响应返回后
	// 状态即已落定)。
	var sv ConfigView
	post(t, h, "POST", "/api/config/system", `{"task_concurrency":"3"}`, http.StatusOK, &sv)
	if sv.System.TaskConcurrency.Value != 3 {
		t.Fatalf("写响应不符: %+v", sv.System.TaskConcurrency)
	}
	if st := mgr.Task("tc-s3").Status(); st != manager.StatusStarted {
		t.Fatalf("调大保存后 tc-s3 应自动拉起,实际 %s", st)
	}

	// 改回 2:在跑 3 个不硬杀(照常运行)。
	post(t, h, "POST", "/api/config/system", `{"task_concurrency":"2"}`, http.StatusOK, &sv)
	for _, id := range []string{"tc-s1", "tc-s2", "tc-s3"} {
		if st := mgr.Task(id).Status(); st != manager.StatusStarted {
			t.Fatalf("调小后 %s 不应被硬杀,实际 %s", id, st)
		}
	}
}
