// Package server 的发现页两层未读提醒 API(P5-08,接替 P4-47 页签级
// 单层已读线):数字与条目点两层不联动——数字管"知道有新的"(进页即消),
// 条目点管"还没细看"(点开详情才消)。
//
// ① 页面级数字:意图/事实/漏洞三页签各自独立已读线(每任务×页签一键,
// 值=上次进页时刻,服务端盖戳跨浏览器一致);口径 = 已读线之后新落页
// 条目数(意图新提出/漏洞新确认/事实新确认,与页签展示同口径,见
// rings.UnreadFindings)。
// ② 条目级已看集合:每任务×页签一键,值=已看条目节点 ID 的 JSON 数组;
// 判定与展示留在前端(列表行 ID ∉ 集合即亮淡红点),服务端只存集合。
//
//	GET  /api/tasks/{id}/unread → {"intents":n,"claims":n,"facts":n,
//	                               "seen":{"intents":[id…],"claims":[…],"facts":[…]}}
//	POST /api/tasks/{id}/seen   {"kind":"intents"|"claims"|"facts"}
//	  → 进页盖"现在"戳(页面级数字清零);条目级集合不动。
//	POST /api/tasks/{id}/seen   {"kind":…,"item":"<节点ID>"}
//	  → 条目记已看(集合追加);已读线不动。
//	两形态响应同 GET(最新全量,供前端即时灭点)。

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ghostreaper/internal/runlog"
)

// 已读存储键(键值表,沿 P4-47 存法):已读线键值 = 上次进页时刻
//(RFC3339Nano UTC,与 nodes 时刻列同格式);已看集合键值 = 条目节点
// ID 的 JSON 字符串数组。kind = intents|claims|facts(claims 即漏洞页,
// 沿用历史键面,换浏览器读同一份)。
const (
	readLineKeyIntents = "read.intents."
	readLineKeyClaims  = "read.claims."
	readLineKeyFacts   = "read.facts."
	readHitKeyPrefix   = "readhit." // + kind + "." + taskID
)

// unreadKinds 三页签已读 kind(与前端页签↔kind 映射同表)。
var unreadKinds = map[string]string{
	"intents": readLineKeyIntents,
	"claims":  readLineKeyClaims,
	"facts":   readLineKeyFacts,
}

// unreadState GET /unread 响应:三页签新落页计数 + 条目级已看集合
//(三键恒在,空为空数组——载荷形状稳定,前端免判空)。
type unreadState struct {
	Intents int                `json:"intents"`
	Claims  int                `json:"claims"`
	Facts   int                `json:"facts"`
	Seen    map[string][]string `json:"seen"`
}

// parseReadLine 已读线值 → 时刻;缺失 / 坏值退零值(从未看过,全部
// 在页条目计为未读——如实语义)。
func parseReadLine(v string, ok bool) time.Time {
	if !ok || v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseReadHit 已看集合值 → 集合;缺失 / 坏值退空集合。
func parseReadHit(v string, ok bool) []string {
	if !ok || v == "" {
		return []string{}
	}
	var ids []string
	if err := json.Unmarshal([]byte(v), &ids); err != nil {
		return []string{}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// unreadStateOf 任务两层未读全量(读三键已读线 + 三键已看集合 + 查台账
// 节点;任何一步失败按零值如实返回,红点不因读失败点亮)。
func (s *Server) unreadStateOf(ctx context.Context, taskID string) unreadState {
	state := unreadState{Seen: map[string][]string{
		"intents": {}, "claims": {}, "facts": {},
	}}
	lines := map[string]time.Time{}
	for kind := range unreadKinds {
		e, ok, _ := s.Store.Setting(ctx, readLineKeyOf(kind)+taskID)
		lines[kind] = parseReadLine(e.Value, ok)
		h, hok, _ := s.Store.Setting(ctx, readHitKeyPrefix+kind+"."+taskID)
		if hok {
			state.Seen[kind] = parseReadHit(h.Value, hok)
		}
	}
	var err error
	state.Intents, state.Claims, state.Facts, err = s.Store.UnreadFindings(ctx, taskID,
		lines["intents"], lines["claims"], lines["facts"])
	if err != nil {
		runlog.Warnf("未读计数读取失败(任务 %s): %v", taskID, err)
		return unreadState{Seen: map[string][]string{"intents": {}, "claims": {}, "facts": {}}}
	}
	return state
}

// readLineKeyOf kind → 已读线键前缀(kind 已在 unreadKinds 校验)。
func readLineKeyOf(kind string) string {
	return unreadKinds[kind]
}

// handleUnread 两层未读全量(GET /api/tasks/{id}/unread):三页签数字
// 红点与条目淡红点的数据源;发现 / 任务两主题实时推送驱动刷新
//(确认落账即变)。
func (s *Server) handleUnread(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	writeJSON(w, http.StatusOK, s.unreadStateOf(r.Context(), taskID))
}

// handleSeen 记已读(POST /api/tasks/{id}/seen):kind=intents|claims|
// facts。带 item = 条目级(该条详情已点开,集合追加,幂等);不带 item
// = 页面级(进该子页签,盖"现在"戳)。键值表写入走 SetSettingQuiet
//(已读不是配置修改,不进"配置修改"日志行防刷屏,与任务草稿同款)。
func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Kind string `json:"kind"`
		Item string `json:"item"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, ok := unreadKinds[req.Kind]; !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("kind 须为 intents、claims 或 facts"))
		return
	}
	if req.Item != "" {
		// 条目级:已看集合追加(幂等);节点 ID 轻量限长,防坏值撑爆键值。
		if len(req.Item) > 128 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("item 过长"))
			return
		}
		key := readHitKeyPrefix + req.Kind + "." + taskID
		e, ok, err := s.Store.Setting(r.Context(), key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		ids := parseReadHit(e.Value, ok)
		dup := false
		for _, id := range ids {
			if id == req.Item {
				dup = true
				break
			}
		}
		if !dup {
			ids = append(ids, req.Item)
			raw, merr := json.Marshal(ids)
			if merr != nil {
				writeErr(w, http.StatusInternalServerError, merr)
				return
			}
			if err := s.Store.SetSettingQuiet(r.Context(), key, string(raw), "creator"); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		// 条目级不动已读线、不影响任务行汇总,不发脏标记——条目点本就
		// 由前端点开动作本地即灭,跨浏览器靠既有轮询拍对齐。
	} else {
		if err := s.Store.SetSettingQuiet(r.Context(), readLineKeyOf(req.Kind)+taskID,
			time.Now().UTC().Format(time.RFC3339Nano), "creator"); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// 已读线不经账本,live 推送无事件可发——主动标脏 tasks 主题,
		// 任务列表/任务空间标签的未读汇总订阅方即时重拉(轮询连接的
		// 客户端下一拍自然对齐)。
		if s.live != nil {
			s.live.markDirty(liveTopicTasks)
		}
	}
	writeJSON(w, http.StatusOK, s.unreadStateOf(r.Context(), taskID))
}
