// P4-67(问题清单 #86 A②,RoE 机制重构批次第二层):研判分诊与信息源
// 清单的服务端读面与写口——RoE 面板信息源区(host＋研判依据摘要＋撤销
// 按钮)、撤销端点(与牧者标记/分诊自动授同一账本动作)。攻击行为卡
// 的待决呈现与裁决走既有弹门通道(Kind="attack" 仅是读面区分)。
package server

import (
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/swarm"
)

// triageVerdictWords 分诊结论的人话词表(研判留痕与信息源区共用)。
var triageVerdictWords = map[string]string{
	swarm.TriageMethodical: "方法性访问",
	swarm.TriageDeclared:   "命中任务写明的外部服务",
	swarm.TriageAttack:     "信息源上的攻击行为",
	swarm.TriageCandidate:  "疑似目标相关资产",
	"revoked":              "已撤销",
}

// triageNoteText 研判留痕 Note 的人话化("verdict=X basis=Y" →
// "研判分诊:结论;依据:Y");解析不出原样返回(防御)。
func triageNoteText(note string) string {
	verdict, basis, ok := strings.Cut(note, " basis=")
	if !ok {
		return note
	}
	verdict = strings.TrimPrefix(verdict, "verdict=")
	word, known := triageVerdictWords[verdict]
	if !known {
		return note
	}
	return fmt.Sprintf("性质判定:%s;依据:%s", word, basis)
}

// handleInfoSourceRevoke 信息源撤销(P4-67 ④:撤销=恢复拦截,后续访问
// 重新走分诊;与牧者"别当信息源了"同一动作同一账本)。POST 体
// {"host":"host[:port]"}——面板信息源区撤销按钮的落点。
func (s *Server) handleInfoSourceRevoke(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, fmt.Errorf("任务冬眠中,信息源清单只读"))
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.TrimSpace(body.Host)
	if host == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("信息源地址不能为空"))
		return
	}
	if err := swarm.RevokeInfoSource(r.Context(), s.Store, taskID, host, "creator"); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"host": host, "status": "revoked"})
}

// handleInfoSourceMark 信息源标记(P4-68 ① 允许访问的 RoE 面板入口;
// P4-70 ① 卡面入口移除后为手授唯一日常入口:与牧者标记同一账本动作
// swarm.AllowScopeAccess——host 入清单＋同主机资产退役＋挂等单位对账
// 唤醒,瞬间返回)。POST 体 {"host":"host[:port]"}——面板信息源区标记
// 行的落点。
func (s *Server) handleInfoSourceMark(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, fmt.Errorf("任务冬眠中,信息源清单只读"))
		return
	}
	var body struct {
		Host string `json:"host"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	host := strings.TrimSpace(body.Host)
	anchor, ok := swarm.ParseInfoSourceHost(host)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s 不是可识别的主机地址(给域名或 IP,可带端口)", host))
		return
	}
	basis := "操作者标记(信息源)"
	if err := swarm.AllowScopeAccess(r.Context(), s.Store, taskID, anchor, basis, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 挂等单位即时对账回队(免等下一轮)。
	woken, _ := s.resolveScopeWakes(taskID)
	writeJSON(w, http.StatusOK, map[string]any{"host": anchor, "status": "granted", "woken_units": woken})
}
