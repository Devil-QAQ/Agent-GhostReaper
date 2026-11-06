package server

import (
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-49(问题清单 #70):建任务注意事项字段(面板侧) ----
// ① 建任务携带 roe.creator_notes → 落 roe.updated(仅注意事项也落账);
// ② GET/POST /roe:省略 = 保持、空串 = 清空、超长人话拒改;
// ③ 提取器解析:creator_notes 数组与裸串两形态、修剪去重、超长入 issues。

func p449Server(t *testing.T) (http.Handler, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: dir + "/tasks"})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	return srv.Handler(), store
}

// TestP449CreateCarriesNotes 建任务带注意事项:落账 + RoE 读面可见;
// 无 roe 键的留空任务回归(无注意事项)。
func TestP449CreateCarriesNotes(t *testing.T) {
	h, store := p449Server(t)
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["拿到登录口令"],"assets":[{"class":"service","title":"http://127.0.0.1:9911"}],"roe":{"creator_notes":"爆破用小字典\n不要修改任何数据"}}`, http.StatusOK, &created)
	task := created["id"]

	st, err := store.CurrentRoE(t.Context(), task)
	if err != nil {
		t.Fatalf("查 RoE 失败: %v", err)
	}
	if st.CreatorNotes != "爆破用小字典\n不要修改任何数据" {
		t.Fatalf("建任务应携带注意事项: got %q", st.CreatorNotes)
	}
	var roe map[string]any
	get(t, h, "/api/tasks/"+task+"/roe", http.StatusOK, &roe)
	if roe["creator_notes"] != "爆破用小字典\n不要修改任何数据" {
		t.Fatalf("RoE 读面应带注意事项: %v", roe["creator_notes"])
	}

	// 留空任务回归:无 roe 键 → 无注意事项,roE.updated 不因空值多落。
	var created2 map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["摸清暴露面"],"assets":[{"class":"host","title":"127.0.0.2"}]}`, http.StatusOK, &created2)
	st2, err := store.CurrentRoE(t.Context(), created2["id"])
	if err != nil {
		t.Fatalf("查 RoE(留空任务)失败: %v", err)
	}
	if st2.CreatorNotes != "" {
		t.Fatalf("留空任务不应有注意事项: got %q", st2.CreatorNotes)
	}
}

// TestP449RoEEditNotes RoE 页后改:省略保持、显式覆盖、空串清空、
// 超长拒改;账本逐笔留痕(roe.updated)。
func TestP449RoEEditNotes(t *testing.T) {
	h, store := p449Server(t)
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["拿到登录口令"],"assets":[{"class":"service","title":"http://127.0.0.1:9911"}],"roe":{"creator_notes":"爆破用小字典"}}`, http.StatusOK, &created)
	task := created["id"]

	// 省略 creator_notes 的快照提交(约束变更不带注意事项):保持。
	var v map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe", `{"denied_tools":[],"approval_threshold":"修改与持久化"}`, http.StatusOK, &v)
	if v["creator_notes"] != "爆破用小字典" {
		t.Fatalf("省略提交应保持注意事项: %v", v["creator_notes"])
	}

	// 显式覆盖(RoE 页编辑形态:总是携带)。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe", `{"creator_notes":"温柔点\r\n\r\n先只读探测","approval_threshold":"修改与持久化"}`, http.StatusOK, &v)
	if v["creator_notes"] != "温柔点\n先只读探测" {
		t.Fatalf("编辑应覆盖并归一: %v", v["creator_notes"])
	}

	// 空串清空。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe", `{"creator_notes":"","approval_threshold":"修改与持久化"}`, http.StatusOK, &v)
	if v["creator_notes"] != "" {
		t.Fatalf("空串应清空: %v", v["creator_notes"])
	}

	// 超长拒改(人话)。
	long := strings.Repeat("字", 2001)
	var errBody map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe", `{"creator_notes":"`+long+`"}`, http.StatusBadRequest, &errBody)
	if msg, _ := errBody["error"].(string); !strings.Contains(msg, "注意事项过长") {
		t.Fatalf("超长应人话拒改: %v", errBody["error"])
	}

	// 账本留痕:每笔 roe.updated 可溯。
	evs, err := store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件失败: %v", err)
	}
	count := 0
	for _, ev := range evs {
		if ev.Kind == rings.EventRoeUpdated {
			count++
		}
	}
	if count < 3 {
		t.Fatalf("RoE 变更应逐笔记账,至少 3 笔,实得 %d", count)
	}
}

// TestP449ExtractParsesNotes 提取解析:数组/裸串两形态、修剪去重、
// 超长丢弃入 issues(语义辨认本身是【模型】活体验收项,解析为确定性)。
func TestP449ExtractParsesNotes(t *testing.T) {
	reply, issues := parseTaskExtract(`{"title":"x","creator_notes":[" 用小字典 ","小字典优先","爆破用小字典","  "]}`)
	if len(issues) != 0 {
		t.Fatalf("无 issues 预期: %v", issues)
	}
	want := []string{"用小字典", "小字典优先", "爆破用小字典"}
	if len(reply.CreatorNotes) != len(want) {
		t.Fatalf("注意事项解析不符: %v", reply.CreatorNotes)
	}
	for i, w := range want {
		if reply.CreatorNotes[i] != w {
			t.Fatalf("第 %d 条应为 %q, got %q", i, w, reply.CreatorNotes[i])
		}
	}

	// 裸串形态:模型偶发单串,按一条收。
	one, _ := parseTaskExtract(`{"creator_notes":"小字典优先"}`)
	if len(one.CreatorNotes) != 1 || one.CreatorNotes[0] != "小字典优先" {
		t.Fatalf("裸串应按一条收: %v", one.CreatorNotes)
	}

	// 超长丢弃入 issues(不静默)。
	long := strings.Repeat("长", 201)
	dropped, issues := parseTaskExtract(`{"creator_notes":["` + long + `","正常的条目"]}`)
	if len(dropped.CreatorNotes) != 1 || dropped.CreatorNotes[0] != "正常的条目" {
		t.Fatalf("超长应丢弃、正常条目保留: %v", dropped.CreatorNotes)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "已丢弃") {
		t.Fatalf("超长丢弃应入 issues: %v", issues)
	}
}
