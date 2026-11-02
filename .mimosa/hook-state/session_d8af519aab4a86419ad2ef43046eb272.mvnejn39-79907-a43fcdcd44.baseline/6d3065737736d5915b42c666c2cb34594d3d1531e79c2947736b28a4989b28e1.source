package server

// 拦截规则引擎的面板 API(P2-08e,第四章 §3 v2.1 判定链②③;审核迭代
// 补任务级逐条 CRUD 与自然语言录入):
//
//	GET    /api/guard/rules                全局硬规则清单(系统设置页)
//	POST   /api/guard/rules                新建 / 编辑一条硬规则(键值表)
//	DELETE /api/guard/rules/{id}           删除(内置条目记墓碑,重启不复活)
//
//	POST   /api/tasks/{id}/roe/rules       新增一条任务级规则(结构化)
//	POST   /api/tasks/{id}/roe/rules/{rid} 编辑一条
//	DELETE /api/tasks/{id}/roe/rules/{rid} 删除一条
//	POST   /api/tasks/{id}/roe/rules/extract
//	       自然语言 → 候选规则(单发 LLM 提取,不落库,确认后走新增)
//
// 任务级规则的生效语义(审核迭代定则):判定现读当前值+范围过滤(范围
// 外单位连求值都不发生);变更落 roe.updated 账并唤醒主宰;对范围相交
// 的在飞单位定向投递一条舵杆消息(下一工具调用边界注入,带回执)——
// 不相关单位零打扰,不向任何单位广播。硬规则是全局共享的(不随任务):
// 键值表存储,逐次调用现读,改完对下一次判定即时生效。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// handleGuardRulesList 硬规则清单(组序稳定)。
func (s *Server) handleGuardRulesList(w http.ResponseWriter, r *http.Request) {
	rules, err := swarm.ListHardRules(r.Context(), s.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

// guardRuleBody 硬规则提交体(id 空 = 新建;enabled 随体提交,单端点
// 覆盖"编辑"与"启停"两类操作)。
type guardRuleBody struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Name      string `json:"name"`
	Pattern   string `json:"pattern"`
	ToolScope string `json:"tool_scope"`
	Action    string `json:"action"` // 处置:""=拒绝 / gate=弹门(P4-03 ②)
	Enabled   bool   `json:"enabled"`
	Note      string `json:"note"`
}

// handleGuardRulesSave 新建 / 编辑一条硬规则。编辑内置条目 = 同 ID 覆盖
// (播种不覆盖已有键,用户改动即权威)。保存后立即对下一次判定生效。
func (s *Server) handleGuardRulesSave(w http.ResponseWriter, r *http.Request) {
	var body guardRuleBody
	if !readJSON(w, r, &body) {
		return
	}
	id, err := swarm.SaveHardRule(r.Context(), s.Store, swarm.HardRule{
		ID: strings.TrimSpace(body.ID), Group: body.Group, Name: body.Name,
		Pattern: body.Pattern, ToolScope: body.ToolScope, Action: body.Action,
		Enabled: body.Enabled, Note: strings.TrimSpace(body.Note),
	}, "creator")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handleGuardRulesDelete 删除一条硬规则(内置条目记墓碑,重启播种不
// 复活;删除后下一次判定即不再命中该条)。
func (s *Server) handleGuardRulesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := swarm.DeleteHardRule(r.Context(), s.Store, id); err != nil {
		if strings.Contains(err.Error(), "不存在") {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "deleted": "true"})
}

// ---- RoE 任务级拦截规则的提交校验与读侧计数视图 ----

// intentRuleIn RoE 提交体内的任务级规则形态。
type intentRuleIn struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`
	AssetIDs    []string `json:"asset_ids"`
	Disposition string   `json:"disposition"`
	Quota       int      `json:"quota"`
	Patterns    []string `json:"patterns"`
	Note        string   `json:"note"`
}

// toRings 转 rings 负载(结构校验在 rings 投影入口,资产存在性在
// handler 侧查图——RoE 快照只收图上真实存在的资产)。
func (b intentRuleIn) toRings() rings.IntentRule {
	return rings.IntentRule{
		ID: strings.TrimSpace(b.ID), Action: strings.TrimSpace(b.Action),
		AssetIDs: b.AssetIDs, Disposition: b.Disposition, Quota: b.Quota,
		Patterns: b.Patterns, Note: strings.TrimSpace(b.Note),
	}
}

// validateIntentRuleAssets 资产范围存在性校验:规则里的资产 ID 必须
// 已在图上(防面板陈旧选项把规则指向不存在的资产)。
func (s *Server) validateIntentRuleAssets(taskID string, rules []rings.IntentRule) error {
	existing := map[string]bool{}
	assets, err := s.Store.NodesByType(context.Background(), taskID, rings.NodeAsset, "")
	if err != nil {
		return err
	}
	for _, a := range assets {
		existing[a.ID] = true
	}
	for _, r := range rules {
		for _, id := range r.AssetIDs {
			if !existing[id] {
				return fmt.Errorf("拦截规则 %s 的资产 %s 不在图上", r.ID, id)
			}
		}
	}
	return nil
}

// validateAssetIDs 资产 ID 存在性校验(摘除清单等图上资产引用共用:
// 防面板陈旧选项把摘除指向不存在的资产)。
func (s *Server) validateAssetIDs(taskID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	existing := map[string]bool{}
	assets, err := s.Store.NodesByType(context.Background(), taskID, rings.NodeAsset, "")
	if err != nil {
		return err
	}
	for _, a := range assets {
		existing[a.ID] = true
	}
	for _, id := range ids {
		if !existing[id] {
			return fmt.Errorf("资产 %s 不在图上", id)
		}
	}
	return nil
}

// handleRoEAssetScope 授权资产范围(P4-05,问题清单 #10):提交新的摘除
// 清单(整体快照语义,空数组 = 全部纳入)。审计:roe.updated 事件 +
// 服务端自动备注本次摘除/纳入了什么(资产标识);改动下一轮生效——
// 摘除的资产主宰不再派发覆盖它的意图(派发入口守卫拒绝,快照与 RoE
// 约束块同标),在飞步骤自然收尾不做硬中断。
func (s *Server) handleRoEAssetScope(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Excluded []string `json:"excluded"`
		Note     string   `json:"note"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Excluded == nil {
		req.Excluded = []string{}
	}
	if err := s.validateAssetIDs(taskID, req.Excluded); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		// 自动备注:新旧清单差集(资产标识),审计可直读"这次动了什么"。
		oldSet := map[string]bool{}
		for _, id := range st.ExcludedAssets {
			oldSet[id] = true
		}
		newSet := map[string]bool{}
		for _, id := range req.Excluded {
			newSet[id] = true
		}
		var removed, added []string
		for _, id := range req.Excluded {
			if !oldSet[id] {
				removed = append(removed, id)
			}
		}
		for _, id := range st.ExcludedAssets {
			if !newSet[id] {
				added = append(added, id)
			}
		}
		switch {
		case len(removed) > 0 && len(added) > 0:
			note = fmt.Sprintf("摘除 %s;纳入 %s", strings.Join(removed, "、"), strings.Join(added, "、"))
		case len(removed) > 0:
			note = fmt.Sprintf("摘除 %s", strings.Join(removed, "、"))
		case len(added) > 0:
			note = fmt.Sprintf("纳入 %s", strings.Join(added, "、"))
		default:
			note = "范围未变化"
		}
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
			ExcludedAssets: req.Excluded, Note: note,
		},
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.wake(taskID)
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// intentRuleOut RoE 读侧的任务级规则视图(带资产标题与已用次数,面板
// 直观呈现"允许 ≤N 次,已用 k 次")。
type intentRuleOut struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"` // 动作描述(操作者的原话)
	AssetIDs    []string `json:"asset_ids"`
	AssetTitles []string `json:"asset_titles"`
	Disposition string   `json:"disposition"`
	Quota       int      `json:"quota"`
	Used        int      `json:"used"`               // 允许 + 次数上限规则的已计数次数
	Patterns    []string `json:"patterns,omitempty"` // 判定正则(空=按内置动作词判)
	Note        string   `json:"note,omitempty"`
}

// intentRuleViews 组装任务级规则视图(计数来自 guard.record 事件流)。
func (s *Server) intentRuleViews(taskID string, rules []rings.IntentRule) []intentRuleOut {
	out := make([]intentRuleOut, 0, len(rules))
	if len(rules) == 0 {
		return out
	}
	ctx := context.Background()
	titles := map[string]string{}
	if assets, err := s.Store.NodesByType(ctx, taskID, rings.NodeAsset, ""); err == nil {
		for _, a := range assets {
			titles[a.ID] = a.Title
		}
	}
	var quotaEvents []rings.GuardRecord
	if evs, err := s.Store.Events(ctx, taskID, 0, 0); err == nil {
		for _, ev := range evs {
			if ev.Kind != rings.EventGuardRecord {
				continue
			}
			var rec rings.GuardRecord
			if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.GuardRecQuotaCount {
				quotaEvents = append(quotaEvents, rec)
			}
		}
	}
	for _, r := range rules {
		v := intentRuleOut{
			ID: r.ID, Action: r.Action, AssetIDs: r.AssetIDs,
			Disposition: r.Disposition, Quota: r.Quota, Patterns: r.Patterns, Note: r.Note,
		}
		for _, id := range r.AssetIDs {
			if t, ok := titles[id]; ok {
				v.AssetTitles = append(v.AssetTitles, t)
			} else {
				v.AssetTitles = append(v.AssetTitles, id)
			}
		}
		for _, rec := range quotaEvents {
			if rec.RuleID == r.ID {
				v.Used++
			}
		}
		out = append(out, v)
	}
	return out
}

// seedHardRulesOnce 启动播种(server.New 调用,幂等):内置清单写缺失键,
// 不覆盖用户改动(第四章 §3 v2.1"规则是数据不是代码")。播种失败不
// 阻断面板启动——判定路径有代码内置兜底(宁可多拦),规则端点读写
// 键值表实况,错误如实返回给面板。
func seedHardRulesOnce(store *rings.Store) error {
	return swarm.SeedHardRules(context.Background(), store)
}

// ---- 任务级规则的逐条增删改(审核迭代:与系统级拦截规则同款操作形态) ----
//
//	POST   /api/tasks/{id}/roe/rules           新增一条(结构化)
//	POST   /api/tasks/{id}/roe/rules/{rid}     编辑一条
//	DELETE /api/tasks/{id}/roe/rules/{rid}     删除一条
//	POST   /api/tasks/{id}/roe/rules/extract   自然语言提取候选(不落库,
//	                                            操作者确认后走上面的新增)
//
// 生效语义(第四章 §4,操作者确认的机制):变更只走 roe.updated 账本事件
// 并 wake 该任务的主宰循环——绝不向虫群单位注入任何消息(防上下文污染:
// 单位 a 在飞 xxx.com、单位 b 在飞 aaa.com,规则变更对二者零感知)。
// 主宰下一轮研判读到新边界后按新规则派发;在飞单位按领取时冻结的
// 任务包跑完,收割后由主宰重判。R7 无旁路:每次增删改都是一笔
// roe.updated 快照事件(全量规则随快照落账,审计走事件流)。

// validateRulePatterns 规则正则编译校验(保存入口;地基层只管结构):
// 每条须非空且可编译(RE2),否则拒绝——入账的规则必须能参与判定。
func validateRulePatterns(rules []rings.IntentRule) error {
	for _, r := range rules {
		for i, p := range r.Patterns {
			p = strings.TrimSpace(p)
			if p == "" {
				return fmt.Errorf("规则 %s 第 %d 条匹配模式为空", r.ID, i+1)
			}
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("规则 %s 第 %d 条匹配模式编译失败: %w", r.ID, i+1, err)
			}
		}
	}
	return nil
}

// intentRuleID 生成规则短 ID(ir-<rand>)。
func intentRuleID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ir-%d", time.Now().UnixNano())
	}
	return "ir-" + hex.EncodeToString(b[:])
}

// roeRuleBody 逐条端点的提交体(id 由路径或服务端给,新增可省)。
// Action 为动作描述自由文本;Patterns 每行一条正则(空 = 描述按内置
// 动作词分类判)。
type roeRuleBody struct {
	Action      string   `json:"action"`
	AssetIDs    []string `json:"asset_ids"`
	Disposition string   `json:"disposition"`
	Quota       int      `json:"quota"`
	Patterns    []string `json:"patterns"`
	Note        string   `json:"note"`
}

// toRings 转负载(结构校验在 rings 投影入口,资产存在性与正则编译在
// handler)。Action 是动作描述自由文本;Patterns 是判定正则(可由
// 描述经模型推导,也可手写)。
func (b roeRuleBody) toRings(id string) rings.IntentRule {
	return rings.IntentRule{
		ID: strings.TrimSpace(id), Action: strings.TrimSpace(b.Action),
		AssetIDs: b.AssetIDs, Disposition: b.Disposition, Quota: b.Quota,
		Patterns: b.Patterns, Note: strings.TrimSpace(b.Note),
	}
}

// commitRoERules 以新规则清单落一笔 roe.updated 快照(工具清单/门槛/
// 说明/授权范围摘除清单随当前值透传,规则区变更语义只动规则),唤醒
// 主宰,并对范围相交的在飞单位定向投递舵杆告知(见 notifyRoERuleChange)。
// affected: 本次变更涉及的规则(新增=新则,编辑=旧则+新则,删除=旧则),
// 空范围规则恒影响全部在飞单位。
func (s *Server) commitRoERules(ctx context.Context, taskID string, rules []rings.IntentRule, note string, verb string, affected []rings.IntentRule) error {
	st, err := s.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		return err
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: rules,
			ExcludedAssets: st.ExcludedAssets, Note: note,
		},
	}); err != nil {
		return err
	}
	s.wake(taskID)
	s.notifyRoERuleChange(ctx, taskID, verb, affected)
	return nil
}

// notifyRoERuleChange 定向告知(审核迭代机制,操作者定则):任务级规则
// 变更只投给"范围与步骤相交"的在飞单位——经舵杆在下一工具调用边界
// 注入一条告知消息(带回执状态机),范围外单位零打扰(不污染上下文)。
// 单位已落地时 SteerWork 如实报错,忽略即可(无需告知已结束的单位)。
func (s *Server) notifyRoERuleChange(ctx context.Context, taskID, verb string, affected []rings.IntentRule) {
	if s.swarmExec() == nil || s.helmOf() == nil || len(affected) == 0 {
		return
	}
	running := s.swarmExec().RunningUnitSteps(taskID)
	if len(running) == 0 {
		return
	}
	// 步骤 covers 资产(图上现值):规则范围 ∩ covers = 该单位受影响。
	covers := map[string]map[string]bool{}
	if edges, err := s.Store.Edges(ctx, taskID, rings.EdgeCovers); err == nil {
		for _, e := range edges {
			if covers[e.From] == nil {
				covers[e.From] = map[string]bool{}
			}
			covers[e.From][e.To] = true
		}
	}
	for _, rs := range running {
		stepAssets := covers[rs.StepID]
		hit := false
		for _, rule := range affected {
			if len(rule.AssetIDs) == 0 {
				hit = true // 空范围 = 全部资产
				break
			}
			for _, id := range rule.AssetIDs {
				if stepAssets[id] {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			continue
		}
		msg := ruleNotifyMessage(verb, affected, covers[rs.StepID])
		if _, err := s.helmOf().SteerWork(ctx, taskID, rs.StepID, msg, "creator"); err != nil {
			continue // 单位恰好落地等:已结束的单位无需告知
		}
	}
}

// ruleNotifyMessage 告知消息(注入受影响单位会话的舵杆正文;外层由
// 舵杆机制带来源/回执格式,文风 8.5:陈述事实、给动作指引)。
func ruleNotifyMessage(verb string, affected []rings.IntentRule, stepAssets map[string]bool) string {
	var b strings.Builder
	b.WriteString("任务级拦截规则已" + verb + ",即刻生效:\n")
	for _, r := range affected {
		scope := "全部资产"
		if names := affectedScopeNames(r, stepAssets); names != "" {
			scope = names
		}
		switch {
		case r.Disposition == rings.IntentForbid:
			fmt.Fprintf(&b, "- 禁止对 %s 做%s", scope, r.Action)
		case r.Quota > 0:
			fmt.Fprintf(&b, "- 允许对 %s 做%s,全任务合计不超过 %d 次", scope, r.Action, r.Quota)
		default:
			fmt.Fprintf(&b, "- 允许对 %s 做%s", scope, r.Action)
		}
		b.WriteString("\n")
	}
	b.WriteString("违反禁止或超出次数的调用会被直接拒绝(只拒该次调用,不影响你的其他工作);与上述范围无关的工作照常进行,不要因此停下。")
	return b.String()
}

// affectedScopeNames 告知消息里的范围名(只列与本单位相交的资产,其余
// 不提——避免把别的单位的范围带进它的上下文)。
func affectedScopeNames(r rings.IntentRule, stepAssets map[string]bool) string {
	var parts []string
	for _, id := range r.AssetIDs {
		if len(stepAssets) == 0 || stepAssets[id] {
			parts = append(parts, id)
		}
	}
	return strings.Join(parts, "、")
}

// handleRoERuleAdd 新增一条任务级规则。
func (s *Server) handleRoERuleAdd(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var body roeRuleBody
	if !readJSON(w, r, &body) {
		return
	}
	rule := body.toRings(intentRuleID())
	st, err := s.Store.CurrentRoE(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	next := append(append([]rings.IntentRule{}, st.IntentRules...), rule)
	if err := s.validateIntentRuleAssets(taskID, next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := validateRulePatterns(next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.commitRoERules(r.Context(), taskID, next,
		fmt.Sprintf("任务级拦截规则新增 %s(%s·%s)", rule.ID, rule.Action, rule.Disposition),
		"新增", []rings.IntentRule{rule}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRoERuleEdit 编辑一条任务级规则(同 ID 覆盖)。
func (s *Server) handleRoERuleEdit(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	rid := r.PathValue("rid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var body roeRuleBody
	if !readJSON(w, r, &body) {
		return
	}
	st, err := s.Store.CurrentRoE(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	idx := -1
	for i := range st.IntentRules {
		if st.IntentRules[i].ID == rid {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("规则 %s 不存在", rid))
		return
	}
	old := st.IntentRules[idx]
	next := append([]rings.IntentRule{}, st.IntentRules...)
	next[idx] = body.toRings(rid)
	if err := s.validateIntentRuleAssets(taskID, next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := validateRulePatterns(next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.commitRoERules(r.Context(), taskID, next,
		fmt.Sprintf("任务级拦截规则编辑 %s(%s·%s)", rid, next[idx].Action, next[idx].Disposition),
		"变更", []rings.IntentRule{old, next[idx]}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRoERuleDelete 删除一条任务级规则。
func (s *Server) handleRoERuleDelete(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	rid := r.PathValue("rid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	st, err := s.Store.CurrentRoE(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	next := make([]rings.IntentRule, 0, len(st.IntentRules))
	var removed []rings.IntentRule
	for _, x := range st.IntentRules {
		if x.ID == rid {
			removed = append(removed, x)
			continue
		}
		next = append(next, x)
	}
	if len(removed) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("规则 %s 不存在", rid))
		return
	}
	if err := s.commitRoERules(r.Context(), taskID, next,
		fmt.Sprintf("任务级拦截规则删除 %s", rid), "解除", removed); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- 描述 → 判定正则的生成(录入辅助,判定保持确定性) ----
//
// 合并弹窗(审核迭代三轮)的唯一辅助:操作者写动作描述(自由文本),
// 模型把描述转成命令参数匹配正则(RE2),面板回填可编辑——保存前
// 操作者看得见、改得了(生成错不静默入账)。判定纯正则,零 LLM。
// 反向规则:只覆盖描述明确提到的行为,生成不了就返回空清单(操作者
// 手写或改用内置动作词)。单发短调用(60 秒上限,跑完即弃)。

// handleRoERulePatterns 动作描述 → 匹配正则清单(不落库)。
func (s *Server) handleRoERulePatterns(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, errors.New("动作描述不能为空"))
		return
	}
	if s.Chain == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("模型未配置(LLM配置页激活模型后可生成);也可手写匹配模式,或描述直接用内置动作词(上传/爆破等)"))
		return
	}
	prov := llm.Provider(s.Chain)
	cctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	msg, _, _, err := prov.Complete(cctx, llm.CompletionRequest{
		System: []string{patternsExtractSystem()},
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: text}}},
		},
		MaxTokens: 2048,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("生成失败: %w", err))
		return
	}
	patterns := parsePatternList(msg.Text())
	// 编译不过的条目丢弃(面板回填的是可用正则;生成质量由操作者终审)。
	okPatterns := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if _, err := regexp.Compile(p); err != nil {
			continue
		}
		okPatterns = append(okPatterns, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": okPatterns})
}

// patternsExtractSystem 生成提示词:描述 → 正则清单。
func patternsExtractSystem() string {
	return `你是渗透测试任务的动作规则编译器。把操作者对动作的自然语言描述转成命令参数匹配正则,输出且只输出一个 JSON 字符串数组(可为空 []),不要输出其他文字。
要求:
- 每个正则匹配"执行该动作的命令文本"的典型形态(动作可能经 bash 的 curl/wget/sqlmap/hydra/nmap 等工具执行)。
- 只覆盖描述明确提到的行为,不扩展、不推测;同一动作的多种命令形态给多条。
- 正则用 RE2 语法(无前后瞻),留意大小写时加 (?i)。
- 描述含糊或生成不了就输出 []。
示例:描述"上传文件" -> ["(?i)\bcurl\b[^|;&]*(\s-T\b|--upload-file|\s-F\b|--form\b)","(?i)\bwget\b[^|;&]*(--post-file|--post-data)"]`
}

// parsePatternList 从模型回复解析正则清单(容忍围栏与前后杂文;也接受
// 逐行纯文本形态)。
func parsePatternList(reply string) []string {
	raw := strings.TrimSpace(reply)
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start >= 0 && end > start {
		var list []string
		if err := json.Unmarshal([]byte(raw[start:end+1]), &list); err == nil {
			return list
		}
	}
	// 退化为逐行:非空行即候选正则。
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
