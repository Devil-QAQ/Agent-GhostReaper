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

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
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
// 服务端自动备注本次摘除/纳入了什么(资产标识)。摘除即止损(P4-45,
// 造物主定案语义):落账后立即终止覆盖新摘除资产的在跑单位(熔断同款
// 真中断)并取消覆盖它的排队步骤(留痕交主宰重排),主宰下一轮不再派
// 覆盖它的意图(派发入口守卫既有);终止笔数随响应如实返回。面板确认
// 弹窗的预计数走 /roe/assets/impact。
func (s *Server) handleRoEAssetScope(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Excluded []string `json:"excluded"`
		AssetID  string   `json:"asset_id"`
		Include  *bool    `json:"include"`
		Note     string   `json:"note"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if (req.AssetID != "") != (req.Include != nil) {
		writeErr(w, 400, errors.New("须同时指定资产与加入状态"))
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
	assets, err := s.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if req.Include != nil {
		var selected *rings.NodeFull
		for _, a := range assets {
			if a.ID == req.AssetID {
				x := a
				selected = &x
				break
			}
		}
		if selected == nil {
			writeErr(w, 400, errors.New("资产不存在"))
			return
		}
		_, _, key, _ := rings.AssetIdentity(selected.AssetClass, selected.Title)
		req.Excluded = []string{}
		for _, id := range st.ExcludedAssets {
			same := id == selected.ID
			for _, a := range assets {
				if a.ID == id {
					_, _, other, e := rings.AssetIdentity(a.AssetClass, a.Title)
					same = same || e == nil && key != "" && key == other
					break
				}
			}
			if !same {
				req.Excluded = append(req.Excluded, id)
			}
		}
		if !*req.Include {
			req.Excluded = append(req.Excluded, selected.ID)
		}
	}
	note := strings.TrimSpace(req.Note)
	var removed []string // 本次新摘除(止损对象)
	var added []string   // 本次纳入(勾回;P4-71 复活判定的输入)
	{
		oldSet := map[string]bool{}
		for _, id := range st.ExcludedAssets {
			oldSet[id] = true
		}
		newSet := map[string]bool{}
		for _, id := range req.Excluded {
			newSet[id] = true
		}
		for _, id := range req.Excluded {
			if !oldSet[id] {
				removed = append(removed, id)
			}
		}
		// 本次纳入 = 旧摘除清单 − 新摘除清单(勾回)。
		for _, id := range st.ExcludedAssets {
			if !newSet[id] {
				added = append(added, id)
			}
		}
		if note == "" {
			// 自动备注:新旧清单差集(资产标识),审计可直读"这次动了什么"。
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
	}
	if req.Include != nil && *req.Include && !idInList(added, req.AssetID) {
		added = append(added, req.AssetID)
	}
	if strings.TrimSpace(req.Note) == "" && req.Include != nil {
		verb := "摘除"
		if *req.Include {
			verb = "纳入"
		}
		note = verb + " " + req.AssetID
	}
	// P4-69 ②(状态互斥):排除清单成员一律从批准清单剔除——快照提交
	// 不再原样透传 scope_approved(排除与批准不并存,实锤 a-72dab0e5
	// 同列两清单)。
	newSet := map[string]bool{}
	for _, id := range req.Excluded {
		newSet[id] = true
	}
	removedSet := map[string]bool{}
	for _, id := range removed {
		removedSet[id] = true
	}
	removedCoverage := rings.ExcludedCoveredAssetIDs(assets, removedSet)
	nextApproved := make([]string, 0, len(st.ScopeApproved))
	for _, id := range st.ScopeApproved {
		if !newSet[id] && !removedCoverage[id] {
			nextApproved = append(nextApproved, id)
		}
	}
	// P4-71 ①(RoE 勾回入口):本次纳入(勾回)的资产中退役(retired)
	// 的一律复活——NodeStateChanged 留痕"纳入复活"＋并入批准清单
	//(复活即入范围,不回待批);非退役资产勾回语义维持现状。同 host
	// 其余退役节点不连带(写法粒度)。
	revived, rerr := swarm.ReviveRetiredAssets(ctx, s.Store, taskID, added, "creator")
	if rerr != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("勾回资产复活落账失败: %w", rerr))
		return
	}
	for _, id := range revived {
		if !idInList(nextApproved, id) {
			nextApproved = append(nextApproved, id)
		}
	}
	// Removing an explicit exclusion is an explicit inventory rejoin, including
	// legacy discoveries whose original frozen scope was outside the seed.
	for _, id := range added {
		if !idInList(nextApproved, id) {
			nextApproved = append(nextApproved, id)
		}
	}
	if len(revived) > 0 {
		note = fmt.Sprintf("%s;复活 %s(退役转回测试范围)", note, strings.Join(revived, "、"))
	}
	for {
		rev, e := s.Store.CurrentRevision(ctx, taskID)
		if e != nil {
			writeErr(w, 500, e)
			return
		}
		latest, e := s.Store.CurrentRoE(ctx, taskID)
		if e != nil {
			writeErr(w, 500, e)
			return
		}
		if latest.UpdatedRev != st.UpdatedRev {
			writeErr(w, 409, errors.New("资产范围已变化，请重新操作"))
			return
		}
		_, err = s.Store.AppendAtRevision(ctx, taskID, rev, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{
				AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
				ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
				ExcludedAssets: req.Excluded, ScopeApproved: nextApproved, Note: note,
			}})
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		break
	}
	// 在途止损(P4-45 ①:roe.updated 先落账,守卫生效后再终止在途)。
	var killed, aborted []swarm.ScopeHit
	if len(removed) > 0 {
		killed, aborted, err = swarm.StopLossForAssets(ctx, s.Store, s.helmOf(), taskID, removed, "creator")
		if err != nil {
			// 终态已落账,止损失败如实报错不回滚(终止笔数以账本为准)
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("摘除已落账,在途止损失败: %w", err))
			return
		}
		if _, derr := rings.DiscardAnchoredFindings(ctx, s.Store, taskID, rings.DiscardReasonExcluded, "creator"); derr != nil {
			runlog.Warnf("摘除连坐废弃失败(如实跳过,可重入): %s %v", taskID, derr)
		}
	}
	// P4-51 ⑥ 通道联动:RoE 面板纳入(勾回/取消摘除)与卡"加入资产"
	// 等效——挂等该目标的冻结单位对账唤醒(排除的对账收口由止损与下
	// 方 resolveScopeWakes 一并覆盖)。
	s.wake(taskID)
	v, err := s.currentRoE(taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 响应 = RoE 视图原字段(既有消费面零破坏)+ 止损笔数(前端 toast
	// 如实呈现终止了多少在途单位)。
	out := map[string]any{"stopped_units": len(killed), "stopped_steps": len(aborted)}
	if raw, merr := json.Marshal(v); merr == nil {
		_ = json.Unmarshal(raw, &out)
	}
	writeJSON(w, http.StatusOK, out)
}

// idInList 清单包含判定(server 包内小助手,P4-71 勾回复活并入用)。
func idInList(list []string, id string) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// handleRoEAssetImpact GET /api/tasks/{id}/roe/assets/impact?assets=a,b
// 摘除影响预览(P4-45 确认弹窗"将终止 N 个在途单位"的数据面;纯读
// 不落账,执行时按当下状态止损——竞态下以执行结果为准)。
func (s *Server) handleRoEAssetImpact(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	assets := strings.Split(r.URL.Query().Get("assets"), ",")
	var ids []string
	for _, id := range assets {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("assets 参数为空(至少一个资产 ID)"))
		return
	}
	impact, err := swarm.ScopeImpactOf(r.Context(), s.Store, taskID, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, impact)
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
	Patterns    []string `json:"patterns,omitempty"` // 判定正则(空=按内置动作词判)
	Note        string   `json:"note,omitempty"`
}

// intentRuleViews 组装任务级规则视图(计数来自 guard.record 事件流;
// P4-58:按 kind 定向直查,不再全量扫事件流)。
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
