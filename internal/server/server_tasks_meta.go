// Package server 的任务元数据面板 API(P2-06h 任务列表完整版):任务
// 分类(清单存全局键值表、指派走 task.meta 事件入账)、置顶(同事件)、
// 墓碑删除(先停后埋,账本历史保留)、批量熔断/删除。全部操作经账本
// (R7 无旁路),面板可见即可操作。
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// ---- 任务分类(清单 = 全局键值表;指派 = task.meta 事件) ----

// 分类键前缀与取值约束。
const (
	taskCategoryKeyPrefix = "task.category."
	taskCategoryNameMax   = 30 // 名称长度上限(字符)
)

// categoryView 分类条目(id + 名称 + 轻量审计)。
type categoryView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// listCategories 全部分类(键序稳定 = 创建序近似;面板侧自行与任务
// 清单合并计数)。
func (s *Server) listCategories(ctx context.Context) ([]categoryView, error) {
	entries, err := s.Store.SettingsWithPrefix(ctx, taskCategoryKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]categoryView, 0, len(entries))
	for _, e := range entries {
		v := categoryView{
			ID:        strings.TrimPrefix(e.Key, taskCategoryKeyPrefix),
			Name:      e.Value,
			UpdatedBy: e.UpdatedBy,
		}
		if !e.UpdatedAt.IsZero() {
			v.UpdatedAt = e.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		}
		out = append(out, v)
	}
	return out, nil
}

// categoryNameTaken 名称是否已被占用(分类间不重名)。
func (s *Server) categoryNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	cats, err := s.listCategories(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range cats {
		if c.ID != exceptID && c.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// categoryExists 分类 ID 是否存在。
func (s *Server) categoryExists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	_, ok, err := s.Store.Setting(ctx, taskCategoryKeyPrefix+id)
	return ok, err
}

func (s *Server) handleTaskCategoriesList(w http.ResponseWriter, r *http.Request) {
	cats, err := s.listCategories(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// handleTaskCategoryCreate 新建分类(键值表,带 by/updated_at 审计)。
func (s *Server) handleTaskCategoryCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("分类名称不能为空"))
		return
	}
	if utf8.RuneCountInString(name) > taskCategoryNameMax {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("分类名称不能超过 %d 字", taskCategoryNameMax))
		return
	}
	if taken, err := s.categoryNameTaken(r.Context(), name, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	} else if taken {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("分类 %q 已存在", name))
		return
	}
	id := shortID("cat")
	if err := s.Store.SetSetting(r.Context(), taskCategoryKeyPrefix+id, name, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": name})
}

// handleTaskCategoryRename 改名(键值表覆盖写,审计随动)。
func (s *Server) handleTaskCategoryRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := ""
	{
		var req struct {
			Name string `json:"name"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		name = strings.TrimSpace(req.Name)
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("分类名称不能为空"))
		return
	}
	if utf8.RuneCountInString(name) > taskCategoryNameMax {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("分类名称不能超过 %d 字", taskCategoryNameMax))
		return
	}
	if _, ok, err := s.Store.Setting(r.Context(), taskCategoryKeyPrefix+id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	} else if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("分类 %s 不存在", id))
		return
	}
	if taken, err := s.categoryNameTaken(r.Context(), name, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	} else if taken {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("分类 %q 已存在", name))
		return
	}
	if err := s.Store.SetSetting(r.Context(), taskCategoryKeyPrefix+id, name, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": name})
}

// handleTaskCategoryDelete 删除分类:先经 task.meta 事件把该分类下的
// 未删任务移出分类(逐任务入账,可审计),再删键值表条目。
func (s *Server) handleTaskCategoryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	ok, err := s.categoryExists(ctx, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("分类 %s 不存在", id))
		return
	}
	rows, err := s.Store.Tasks(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	empty := ""
	cleared := 0
	for _, t := range rows {
		if t.Deleted || t.Category != id {
			continue
		}
		meta := rings.TaskMeta{Category: &empty, Reason: "分类删除,移出分类"}
		if _, err := s.Store.Append(ctx, t.ID, rings.Event{
			Kind: rings.EventTaskMeta, Actor: "creator", Payload: meta,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("移出任务 %s 的分类: %w", t.ID, err))
			return
		}
		cleared++
	}
	if err := s.Store.DeleteSetting(ctx, taskCategoryKeyPrefix+id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id, "cleared_tasks": cleared})
}

// ---- 任务元数据(指派分类 / 置顶) ----

// handleTaskMeta POST /api/tasks/{id}/meta:分类指派与置顶切换,task.meta
// 事件入账(投影即时生效)。已删除(墓碑)的任务不可再改。
func (s *Server) handleTaskMeta(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	row, err := s.Store.Task(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Category *string `json:"category"` // 空串 = 移出分类
		Pinned   *bool   `json:"pinned"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Category == nil && req.Pinned == nil {
		writeErr(w, http.StatusBadRequest, errors.New("未提交任何修改项(分类 / 置顶至少一项)"))
		return
	}
	if row.Deleted {
		writeErr(w, http.StatusBadRequest, errors.New("任务已删除,不可再改分类或置顶"))
		return
	}
	meta := rings.TaskMeta{Category: req.Category, Pinned: req.Pinned}
	if req.Category != nil {
		cat := strings.TrimSpace(*req.Category)
		if cat != "" {
			ok, err := s.categoryExists(r.Context(), cat)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			if !ok {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("分类 %s 不存在", cat))
				return
			}
		}
		meta.Category = &cat
	}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventTaskMeta, Actor: "creator", Payload: meta,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": taskID})
}

// ---- 墓碑删除(单删 + 批量)与批量熔断;特权清除(P4-40 ⑥) ----

// purgeWaitCap 特权清除前的收场等待上限(删除前停止只令收场,运行件
// 可能在写末笔账;对齐任务收尾宽限 90 秒惯例)。超时如实报错、数据不动,
// 由操作者稍后再删。
const purgeWaitCap = 90 * time.Second

// deleteTask 删除任务的单一权威路径。purge=false 为墓碑删除:非终态
// 任务先熔断(先记控制事件再 cancel,次序由 Manager 保证),再落
// task.meta 墓碑标记,历史全部保留(R1);面板列表过滤墓碑,账本可查
// "谁·何时·为何删除"。purge=true 为特权清除(第七章 §3"真正的数据
// 销毁是造物主主动执行的特权清除操作"):先停次序照旧 → 等运行件收场
// (末笔账不与清行竞态)→ 单事务删除该任务账本各表行 → 任务工作目录
// 整目录删除 → 无主 blob 清除(P4-40 续做:扫剩余全部任务事件负载取
// 引用全集,引用归零才删,有其他任务引用的一律保留)→ 运行日志一行
// 留痕。
func (s *Server) deleteTask(ctx context.Context, taskID, reason string, purge bool) error {
	row, err := s.Store.Task(ctx, taskID)
	if err != nil {
		return fmt.Errorf("任务 %s 不存在", taskID)
	}
	if row.Deleted {
		return nil // 幂等:已墓碑再删为无操作
	}
	if reason == "" {
		reason = "面板删除"
	}
	if row.Status != "stopped" && row.Status != "completed" {
		detail := map[string]any{"operator": "creator", "reason": "删除前停止: " + reason}
		// purge 走静默停止(不触发牧者终态总结:数据即将销毁,总结的
		// 异步落账还会与清行竞态);墓碑路径停止语义一字不变。
		stop := s.Manager.StopBy
		if purge {
			stop = s.Manager.StopForPurge
		}
		if err := stop(taskID, detail); err != nil {
			return fmt.Errorf("删除前停止失败: %w", err)
		}
	}
	if purge {
		if err := s.Manager.WaitSettled(taskID, purgeWaitCap); err != nil {
			return fmt.Errorf("%v,数据未动,稍后再删", err)
		}
		rows, err := s.Store.PurgeTask(ctx, taskID)
		if err != nil {
			return err
		}
		dir := filepath.Join(s.RootDir, taskID)
		dirNote := "无"
		if _, serr := os.Stat(dir); serr == nil {
			if rerr := os.RemoveAll(dir); rerr != nil {
				return fmt.Errorf("删除任务目录 %s 失败: %w", dir, rerr)
			}
			dirNote = dir
		}
		// 无主原始数据清除(P4-40 续做 ①):删完账本行与任务目录后,
		// 扫剩余全部任务的事件负载取 blob 引用全集,引用归零的 blob
		// 删除——有其他任务引用的一律保留;扫描失败不回滚已清数据,
		// 如实入留痕行(引用扫描可重试,Sweep 幂等)。
		orphanNote := "未扫"
		if referenced, rerr := s.Store.ReferencedBlobHashes(ctx); rerr != nil {
			runlog.Warnf("特权清除: 任务 %s 无主原始数据扫描失败: %v", taskID, rerr)
		} else {
			blobs := swarm.NewBlobStore(filepath.Join(s.RootDir, "blobs"))
			n, bytes, serr2 := blobs.Sweep(referenced)
			if serr2 != nil {
				runlog.Warnf("特权清除: 任务 %s 无主原始数据清除失败: %v", taskID, serr2)
			} else {
				orphanNote = fmt.Sprintf("%d 项 %d 字节", n, bytes)
			}
		}
		runlog.Infof("特权清除: 任务 %s「%s」,账本 %d 行,目录 %s,无主数据 %s", taskID, row.Title, rows, dirNote, orphanNote)
		return nil
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventTaskMeta, Actor: "creator",
		Payload: rings.TaskMeta{Deleted: true, Reason: reason},
	}); err != nil {
		return err
	}
	return nil
}

// handleTaskDelete DELETE /api/tasks/{id}:purge 缺省 false = 墓碑标记
// (第七章 §3,与 R1 历史不删一致);purge=true = 特权清除(勾选"同时
// 删除相关数据和账本")。运行中的任务先停再删(次序不变)。
func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
		Purge  bool   `json:"purge"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("删除理由不能为空(随账本留痕)"))
		return
	}
	if err := s.deleteTask(r.Context(), taskID, strings.TrimSpace(req.Reason), req.Purge); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": taskID, "deleted": "true"})
}

// handleTaskBatch POST /api/tasks/batch:批量熔断 / 批量删除 / 批量冬眠
// (P2-08)。逐任务走与单项操作完全相同的权威路径(StopBy / deleteTask /
// Hibernate),单任务失败不拖垮其余,结果逐项回报。二次确认由面板承担
// (本端点不做确认)。
func (s *Server) handleTaskBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"` // stop|delete|hibernate
		Reason string   `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("未选择任何任务"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeErr(w, http.StatusBadRequest, errors.New("理由不能为空(随账本留痕)"))
		return
	}
	type failure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	var failures []failure
	done := 0
	for _, id := range req.IDs {
		var err error
		switch req.Action {
		case "stop":
			err = s.Manager.StopBy(id, map[string]any{
				"operator": "creator", "reason": "批量终止: " + reason,
			})
		case "delete":
			err = s.deleteTask(r.Context(), id, "批量删除: "+reason, false)
		case "hibernate":
			err = s.Manager.Hibernate(id, map[string]any{
				"by": "creator", "reason": "批量冬眠: " + reason,
			})
		default:
			writeErr(w, http.StatusBadRequest, fmt.Errorf("未知批量动作 %q(仅 stop / delete / hibernate)", req.Action))
			return
		}
		if err != nil {
			failures = append(failures, failure{ID: id, Error: err.Error()})
			continue
		}
		done++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action": req.Action, "done": done, "failures": failures,
	})
}

// ---- 任务列表投影补全(P2-06h 各列数据源) ----

// taskDigest 任务行的派生数据:描述(created 事件 detail,事件流锚点
// 不受后续生命周期事件覆盖 detail 的影响)、目标(种子资产标识清单 =
// creator 落账的资产节点)、目标进度(达成 n / 总 m)、运行中虫群单位数
// (图上 running 步骤计数——单位领取即动工,与账本同源,重启幽灵清理
// 后自然归零)。
type taskDigest struct {
	Description   string
	Targets       []string
	GoalsTotal    int
	GoalsAchieved int
	RunningUnits  int
	TaskType      string // pentest|redteam|""(存量未标,P4-44)
}

// digestOf 单任务派生数据(P4-36 起读 task_extras 投影——描述与种子
// 目标域由 created/creator 资产事件写侧同事务维护,不再扫描事件流;
// 目标进度与运行中单位数仍读节点投影;任务类型(P4-44)随 created
// 定格进 task_extras——建后不可改,读侧零事件流)。
func (s *Server) digestOf(ctx context.Context, taskID string) taskDigest {
	var d taskDigest
	d.Targets = []string{}
	if extras, ok, err := s.Store.TaskExtras(ctx, taskID); err == nil && ok {
		d.Description, d.Targets, d.TaskType = extras.Description, extras.Targets, extras.TaskType
	}
	if goals, err := s.Store.NodesByType(ctx, taskID, rings.NodeGoal, ""); err == nil {
		d.GoalsTotal = len(goals)
		for _, g := range goals {
			if g.Status == "achieved" {
				d.GoalsAchieved++
			}
		}
	}
	if steps, err := s.Store.NodesByType(ctx, taskID, rings.NodeStep, "running"); err == nil {
		d.RunningUnits = len(steps)
	}
	return d
}
