// 任务草稿(P4-40,问题清单 #59):新建任务表单的暂存与草稿管理。
// 存储走服务端配置区键值表(键 task.draft.<id>,值 JSON),不进年轮
// 事件流——草稿不是业务事件,账本只记真任务;任务口径隔离(草稿不计
// 入 KPI/并发准入/状态统计)由"只存键值表、不建任务行"天然成立。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// taskDraftKeyPrefix 草稿键前缀(键值表)。
const taskDraftKeyPrefix = "task.draft."

// taskDraftMaxBytes 单份草稿序列化后的大小上限(防御性;表单字段量级
// 远低于此)。
const taskDraftMaxBytes = 256 * 1024

// taskDraftRow 草稿里的资产行(提取/整理的回填结果即资产行)。
type taskDraftRow struct {
	Class  string `json:"class"`
	Title  string `json:"title"`
	Parent string `json:"parent,omitempty"`
}

// taskDraftClosure 收口提醒任务级初始配置(开关 + 两数字,留空 = 用
// 全局默认,与表单同语义)。
type taskDraftClosure struct {
	On      bool   `json:"on"`
	Rounds  string `json:"rounds"`
	Minutes string `json:"minutes"`
}

// taskDraftRule 拦截规则行(从描述提取或手填)。
type taskDraftRule struct {
	Action      string   `json:"action"`
	Disposition string   `json:"disposition"`
	Quota       string   `json:"quota"`
	Scope       []string `json:"scope"`
	Patterns    []string `json:"patterns"`
	Basis       string   `json:"basis"`
}

// taskDraftBody 草稿表单字段全量(P4-40 ②):任务名/分类/描述/目标/
// 资产(行 + 批量粘贴框残余)/整体超时/拦截规则/纠偏步数/收口提醒;
// TaskType 任务类型(P4-44:必填字段的草稿形态,空 = 表单按默认渗透);
// Notes 注意事项(P4-49:多行,空 = 无)。
type taskDraftBody struct {
	Title       string            `json:"title"`
	Category    string            `json:"category"`
	Description string            `json:"description"`
	TaskType    string            `json:"task_type"`
	Goals       string            `json:"goals"`
	Notes       string            `json:"notes"`
	Rows        []taskDraftRow    `json:"rows"`
	Paste       string            `json:"paste"`
	Timeout     string            `json:"timeout"`
	Anchor      string            `json:"anchor"`
	Closure     *taskDraftClosure `json:"closure"`
	Constraints []taskDraftRule   `json:"constraints"`
}

// taskDraftRecord 键值表值:创建时间 + 表单字段(覆盖暂存不改创建时间)。
type taskDraftRecord struct {
	CreatedAt string        `json:"created_at"`
	Form      taskDraftBody `json:"form"`
}

// taskDraftView 草稿列表条目(id + 创建时间 + 全量字段,列表行展示与
// 继续编辑共用)。
type taskDraftView struct {
	ID        string        `json:"id"`
	CreatedAt string        `json:"created_at"`
	Draft     taskDraftBody `json:"draft"`
}

// listTaskDrafts 全部草稿(创建时间新→旧;键序不稳定,按创建时间排)。
func (s *Server) listTaskDrafts(ctx context.Context) ([]taskDraftView, error) {
	entries, err := s.Store.SettingsWithPrefix(ctx, taskDraftKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]taskDraftView, 0, len(entries))
	for _, e := range entries {
		var rec taskDraftRecord
		if err := json.Unmarshal([]byte(e.Value), &rec); err != nil {
			// 坏值如实跳过:单份坏草稿不拖垮列表(键值表无 schema 约束)。
			continue
		}
		out = append(out, taskDraftView{
			ID:        strings.TrimPrefix(e.Key, taskDraftKeyPrefix),
			CreatedAt: rec.CreatedAt,
			Draft:     rec.Form,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (s *Server) handleTaskDraftsList(w http.ResponseWriter, r *http.Request) {
	drafts, err := s.listTaskDrafts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if drafts == nil {
		drafts = []taskDraftView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": drafts})
}

// handleTaskDraftSave POST /api/task-drafts:暂存草稿。带 id 且该草稿
// 存在 = 覆盖原草稿(创建时间保持首存时刻);否则新建。草稿不做表单
// 校验(半成品就是草稿的价值),仅限单份大小上限。
func (s *Server) handleTaskDraftSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string        `json:"id"`
		Draft taskDraftBody `json:"draft"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := strings.TrimSpace(req.ID)
	createdAt := ""
	if id != "" {
		if e, ok, err := s.Store.Setting(ctx, taskDraftKeyPrefix+id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		} else if ok {
			var prev taskDraftRecord
			if err := json.Unmarshal([]byte(e.Value), &prev); err == nil {
				createdAt = prev.CreatedAt
			}
		}
	}
	if createdAt == "" {
		createdAt = time.Now().Format("2006-01-02 15:04:05")
		id = shortID("draft")
	}
	rec := taskDraftRecord{CreatedAt: createdAt, Form: req.Draft}
	rec.Form.Title = strings.TrimSpace(rec.Form.Title)
	if rec.Form.Category != "" {
		rec.Form.Category = strings.TrimSpace(rec.Form.Category)
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(blob) > taskDraftMaxBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("草稿太大(上限 %d KB),精简后再暂存", taskDraftMaxBytes/1024))
		return
	}
	if err := s.Store.SetSettingQuiet(ctx, taskDraftKeyPrefix+id, string(blob), "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handleTaskDraftDelete DELETE /api/task-drafts/{id}:删除草稿(二次确认
// 由面板承担);键不存在幂等成功。建任务成功后的对应草稿删除(同一
// 键,见 handleTaskCreate)与本端点同一路径。
func (s *Server) handleTaskDraftDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("草稿 ID 不能为空"))
		return
	}
	if err := s.Store.DeleteSetting(r.Context(), taskDraftKeyPrefix+id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}
