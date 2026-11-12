// Package server 的任务范围端点(P4-44 ③,问题清单 #61):渗透任务的
// 待批资产范围裁决。写路径与 RoE 面板同源——都走 roe.updated 事件
// (放行进 scope_approved 清单、忽略进 excluded_assets 清单),面板/牧者/
// 主宰读同一账本投影,无旁路;放行后任务包闭包在下一批单位领取时纳入
// 该资产(在跑单位按领取时冻结值跑完),主宰被唤醒重规划。
// P4-51(问题清单 #73②③):范围待决卡两键裁决(加入资产/禁止测试)
// 与对账唤醒——瞬间返回,命令由单位自己异步跑(严禁同步执行命令)。
// P4-52 ⑤(审批集中 RoE):待批资产放行/忽略独立端点退役,资产以
// 同骨架范围卡进 RoE 审批区(卡 ID=资产 ID),与拦截卡共用两键裁决
// 通道;另增勾选批量两键端点。
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// assetCardIDPrefix 待批资产卡 ID 前缀(P4-52 ⑤):待批资产以资产 ID
// 本身作卡 ID(资产 ID 恒 a- 前缀,与拦截卡 sc- 前缀分流);裁决记录
// 请求号加 "ac-" 前缀区别于拦截卡(sc-)与弹门票(ap-)留档。
const assetCardIDPrefix = "a-"

// assetDecisionRequestID 待批资产裁决记录的请求号(ac- 前缀,前缀
// 常量归 rings——记录校验的同族豁免读它)。
func assetDecisionRequestID(assetID string) string {
	return rings.AssetCardRecPrefix + assetID
}

// handleScopeDecide 范围卡裁决(POST /api/tasks/{id}/approvals/{sc-*|a-*},
// 两键"加入资产/禁止测试";P4-68 第三键的决策端点保留——P4-70 ① 起
// 卡面与批量入口移除,服务端 action="allow_access" 仍可裁决):
//   - sc- 拦截卡(渗透命令拦下/红队发现可否决):见 decideScopeCard;
//   - a- 待批资产卡(P4-52 ⑤):见 decidePendingAsset;
//   - access=true:"允许访问"(action="allow_access")——host 入信息源
//     清单＋同主机资产退役＋挂等单位对账唤醒(渗透＝只授访问权;红队
//     ＝不收编照常可测);操作者日常入口在 RoE 页信息源区标记行,卡上
//     不再呈现该键。
//
// 异步纪律(教训在案):各键点击瞬间返回——只做账本写入与调度唤醒,
// 严禁同步执行命令;命令由单位自己异步跑。同卡并点互斥,重复裁决 409。
func (s *Server) handleScopeDecide(w http.ResponseWriter, r *http.Request, taskID, rid string, approved bool, reason string, access bool) {
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,审批单已冻结为只读(唤醒后恢复可审批)"))
		return
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	ctx := r.Context()

	var target string
	var woken, terminated int
	var err error
	switch {
	case access:
		target, woken, terminated, err = s.decideScopeAccess(ctx, taskID, rid)
	case strings.HasPrefix(rid, assetCardIDPrefix):
		target, woken, terminated, err = s.decidePendingAsset(ctx, taskID, rid, approved, reason)
	default:
		target, woken, terminated, err = s.decideScopeCard(ctx, taskID, rid, approved, reason)
	}
	if err != nil {
		writeErr(w, scopeDecisionStatus(err), err)
		return
	}
	verb := "禁止测试"
	switch {
	case access:
		verb = "允许访问"
	case approved:
		verb = "加入资产"
	}
	runlog.Infof("范围待决卡处理: %s %s %s(唤醒 %d 个单位,终止 %d 个)",
		taskID, verb, target, woken, terminated)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rid, "approved": approved, "access": access, "target": target,
		"woken_units": woken, "stopped_units": terminated,
	})
}

// scopeDecisionStatus 裁决错误 → HTTP 状态(单卡与批量同口径)。
func scopeDecisionStatus(err error) int {
	if errors.Is(err, swarm.ErrGateRequestNotFound) {
		return http.StatusNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "已处理过") || strings.Contains(msg, "不是待批资产") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// handleApprovalBatch POST /api/tasks/{id}/approvals/batch
// {ids:[卡ID...], approved} 范围卡勾选批量两键(P4-52 ⑤;P4-70 ① 批量
// 第三键 allow_access 入口移除——单卡决策端点保留):ids 全部须为范围卡
// (sc- 拦截卡 / a- 待批资产卡),逐卡走单卡裁决通道、账本逐笔留痕;单项
// 失败不影响其余,failed 逐条回传人话原因。瞬间返回。
func (s *Server) handleApprovalBatch(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,审批单已冻结为只读(唤醒后恢复可审批)"))
		return
	}
	var req struct {
		IDs      []string `json:"ids"`
		Approved *bool    `json:"approved"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Approved == nil {
		writeErr(w, http.StatusBadRequest, errors.New("审批结果必须是布尔值"))
		return
	}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("勾选清单为空(至少勾选一张范围卡)"))
		return
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	ctx := r.Context()

	type itemFailure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	handled, woken, stopped := 0, 0, 0
	failed := []itemFailure{}
	for _, rid := range ids {
		var target string
		var wn, st int
		var err error
		switch {
		case strings.HasPrefix(rid, assetCardIDPrefix):
			target, wn, st, err = s.decidePendingAsset(ctx, taskID, rid, *req.Approved, "")
		case strings.HasPrefix(rid, rings.ScopeCardIDPrefix):
			target, wn, st, err = s.decideScopeCard(ctx, taskID, rid, *req.Approved, "")
		default:
			err = fmt.Errorf("不是范围卡(批量只处理范围待决卡)")
		}
		if err != nil {
			failed = append(failed, itemFailure{ID: rid, Error: err.Error()})
			continue
		}
		handled++
		woken += wn
		stopped += st
		_ = target
	}
	verb := "禁止测试"
	if *req.Approved {
		verb = "加入资产"
	}
	if handled > 0 {
		s.wake(taskID)
		runlog.Infof("范围待决卡批量处理: %s %s %d 张(唤醒 %d 个单位,终止 %d 个,失败 %d 张)",
			taskID, verb, handled, woken, stopped, len(failed))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"handled": handled, "failed": failed,
		"woken_units": woken, "stopped_units": stopped,
	})
}

// decideScopeAccess "允许访问"裁决核心(P4-68 ① 卡第三键引入;P4-70 ①
// 卡面入口移除后由单卡决策端点保留调用;调用方持 scopeMu):host 入信
// 息源清单(swarm.AllowScopeAccess 统一入口——与分诊自动授/牧者标记/
// 面板标记行同一机制)＋图上同主机资产退役(渗透命令卡目标未入图
// 即零入图;待批资产卡/红队可否决卡的资产随退役从资产图消失)→挂等
// 单位对账唤醒(原会话续跑,重发访问命令经清单命中放行)。不触发止损
// (允许访问不是拒绝);该 host 后续访问零出卡,对它的攻击动作仍走
// 攻击行为卡。返回(目标, 唤醒单位数, 终止单位数)。
func (s *Server) decideScopeAccess(ctx context.Context, taskID, rid string) (string, int, int, error) {
	var target string
	switch {
	case strings.HasPrefix(rid, rings.ScopeCardIDPrefix):
		// sc- 拦截卡:卡面定位(该目标的卡须在待决中;已裁过 409)。
		target = strings.TrimPrefix(rid, rings.ScopeCardIDPrefix)
		rows, err := s.Store.ApprovalByRequest(ctx, taskID, rid)
		if err != nil {
			return "", 0, 0, err
		}
		var card *rings.ApprovalRecord
		for i := range rows {
			rec := approvalRowToRecord(rows[i])
			card = &rec
		}
		if card == nil {
			return "", 0, 0, swarm.ErrGateRequestNotFound
		}
		if card.Type != rings.ApprovalRecScopeWait {
			return "", 0, 0, errors.New("该范围卡已处理过(同一目标不再出卡)")
		}
	case strings.HasPrefix(rid, assetCardIDPrefix):
		// a- 待批资产卡:目标＝待批资产标题(锚定主机)。
		pending, err := s.Store.PendingScopeAssets(ctx, taskID)
		if err != nil {
			return "", 0, 0, err
		}
		found := false
		for _, p := range pending {
			if p.ID == rid {
				target, found = p.Title, true
				break
			}
		}
		if !found {
			return "", 0, 0, fmt.Errorf("资产 %s 不是待批资产(可能已加入或已禁止)", rid)
		}
	default:
		return "", 0, 0, errors.New("不是范围卡(允许访问只处理范围待决卡)")
	}
	anchor, ok := rings.ScopeAnchorOf(strings.TrimSpace(target))
	if !ok {
		return "", 0, 0, errors.New("该目标无主机标识,不适用允许访问(用加入资产或禁止测试)")
	}
	// 授访问权统一入口:入清单＋同主机资产退役(一个机制多种入口,
	// P4-67 信息源清单同一状态)。
	basis := "操作者手授(允许访问)"
	if err := swarm.AllowScopeAccess(ctx, s.Store, taskID, anchor.Host, basis, "creator"); err != nil {
		return "", 0, 0, fmt.Errorf("允许访问落账失败: %w", err)
	}
	// 裁决记录(账本先于副作用,R7;同请求号族收口,卡消)——sc- 卡
	// 用卡请求号,a- 资产卡用 ac- 请求号(与加入资产/禁止测试同族)。
	rec := rings.ApprovalRecord{
		Type: rings.ApprovalRecAccess,
		Note: "范围待批资产 " + target, DecidedBy: "creator",
		Reason: "允许访问: " + target + "(只授访问权,不是测试目标)",
	}
	if strings.HasPrefix(rid, assetCardIDPrefix) {
		rec.RequestID = assetDecisionRequestID(rid)
	} else {
		rec.RequestID = rid
	}
	if card, err := s.latestCardRecord(ctx, taskID, rid); err == nil && card != nil {
		rec.StepID, rec.UnitID = card.StepID, card.UnitID
		rec.Intent, rec.ActionLevel, rec.HarmClass = card.Intent, card.ActionLevel, card.HarmClass
		rec.ToolName, rec.ToolArgs = card.ToolName, card.ToolArgs
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rec,
	}); err != nil {
		return "", 0, 0, err
	}
	// 对账唤醒:挂等该目标的冻结单位即时回队(有空位执行中、无空位
	// 排队中插队队首,原会话续跑);主宰随后读到清单变化。
	woken, terminated := s.resolveScopeWakes(taskID)
	return target, woken, terminated, nil
}

// latestCardRecord 该请求号最新的审批记录(裁决回写带卡面上下文;
// 读不出返回 nil)。
func (s *Server) latestCardRecord(ctx context.Context, taskID, rid string) (*rings.ApprovalRecord, error) {
	rows, err := s.Store.ApprovalByRequest(ctx, taskID, rid)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	rec := approvalRowToRecord(rows[len(rows)-1])
	return &rec, nil
}

// decideScopeCard sc- 拦截卡裁决核心(P4-51 ②③;调用方持 scopeMu):
//   - 加入资产:目标入图(未入图的按目标提取 host 入图,来源发现)转
//     范围内(scope_approved 并入)→ 挂等单位对账唤醒(有空位执行中、
//     无空位排队中插队队首)→ 原会话续跑执行原命令;
//   - 禁止测试:目标转排除态(与 RoE 摘除同一排除机制:在途止损 + 后续
//     不派)→ 挂等单位终止,后续同类命令零出卡。
//
// 返回(目标, 唤醒单位数, 终止单位数)。
func (s *Server) decideScopeCard(ctx context.Context, taskID, rid string, approved bool, reason string) (string, int, int, error) {
	// 卡面定位:该目标的卡须在待决中(最新记录为 scope_wait;已裁过
	// 的 409——同一目标不再出卡)。
	rows, err := s.Store.ApprovalByRequest(ctx, taskID, rid)
	if err != nil {
		return "", 0, 0, err
	}
	var card *rings.ApprovalRecord
	for i := range rows {
		rec := approvalRowToRecord(rows[i])
		card = &rec // rev 升序,留在最后的即最新
	}
	if card == nil {
		return "", 0, 0, swarm.ErrGateRequestNotFound
	}
	if card.Type != rings.ApprovalRecScopeWait {
		return "", 0, 0, errors.New("该范围卡已处理过(同一目标不再出卡)")
	}
	target := strings.TrimPrefix(rid, rings.ScopeCardIDPrefix)
	verb := "禁止测试"
	if approved {
		verb = "加入资产"
	}

	// 目标入图(卡锚定目标,未入图的按目标提取 host 入图,来源发现——
	// 与拦截记录的目标写法一致,写法即授权粒度)。
	assetID, err := s.ensureScopeAsset(ctx, taskID, target)
	if err != nil {
		return "", 0, 0, err
	}

	if approved {
		// 加入资产:scope_approved 并入(渗透/红队同源——红队确认后资产
		// 出"发现的其他资产"分组转入范围);目标此前被排除过的(卡挂
		// 期间又经 RoE 摘除的窗口)一并移回,两态不得并存。
		// P4-71 ①(卡上加入资产入口):目标此前经"允许访问"退役过的
		// (信息源撤销后重出卡等形态)一并复活——NodeStateChanged
		// retired→active 留痕"纳入复活",资产图复现(幂等,非退役零落笔)。
		if _, rerr := swarm.ReviveRetiredAssets(ctx, s.Store, taskID, []string{assetID}, "creator"); rerr != nil {
			return "", 0, 0, fmt.Errorf("加入资产复活落账失败: %w", rerr)
		}
		st, err := s.Store.CurrentRoE(ctx, taskID)
		if err != nil {
			return "", 0, 0, err
		}
		nextExcluded := make([]string, 0, len(st.ExcludedAssets))
		for _, id := range st.ExcludedAssets {
			if id != assetID {
				nextExcluded = append(nextExcluded, id)
			}
		}
		if _, err := s.Store.Append(ctx, taskID, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{
				AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
				ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
				ExcludedAssets: nextExcluded,
				ScopeApproved:  mergeIDs(st.ScopeApproved, []string{assetID}),
				Note:           verb + " " + target,
			},
		}); err != nil {
			return "", 0, 0, err
		}
	} else {
		// 禁止测试:排除态(与 RoE 摘除同一机制——roe.updated + 在途止损;
		// 资产刚入图必不在摘除清单,ExcludeAssets 走新摘除分支)。
		if _, err := swarm.ExcludeAssets(ctx, s.Store, s.helmOf(), taskID, []string{assetID}, "creator"); err != nil {
			return "", 0, 0, fmt.Errorf("禁止测试落账失败: %w", err)
		}
	}

	// 裁决记录(账本先于副作用,R7;同 RequestID 收口,卡消)。
	decided := *card
	if approved {
		decided.Type = rings.ApprovalRecApproved
		decided.DenyLine = ""
	} else {
		decided.Type = rings.ApprovalRecDenied
		decided.DenyLine = rings.DenyLineRoeScope
	}
	decided.DecidedBy = "creator"
	if reason != "" {
		decided.Reason = verb + ": " + reason
	} else {
		decided.Reason = verb
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "creator", Payload: decided,
	}); err != nil {
		return "", 0, 0, err
	}

	// 对账唤醒:挂等该目标的冻结单位即时收口(加入资产→回队队首,禁
	// 止测试→终止);主宰随后重规划(新资产入图/方向收窄)。
	woken, terminated := s.resolveScopeWakes(taskID)
	return target, woken, terminated, nil
}

// decidePendingAsset a- 待批资产卡裁决核心(P4-52 ⑤;调用方持
// scopeMu):待批资产(渗透任务范围外发现入图,锁定未测)的两键——
// 加入资产=并入 scope_approved(与拦截卡批准同一 RoE 写法)+ 对账唤
// 醒;禁止测试=ExcludeAssets 排除止损(与 RoE 摘除同机制)。裁决落
// approval.record(ac- 请求号,卡随待批态消失)。返回(目标, 唤醒, 终止)。
func (s *Server) decidePendingAsset(ctx context.Context, taskID, assetID string, approved bool, reason string) (string, int, int, error) {
	pending, err := s.Store.PendingScopeAssets(ctx, taskID)
	if err != nil {
		return "", 0, 0, err
	}
	var cur *rings.ExcludedAssetView
	for i := range pending {
		if pending[i].ID == assetID {
			cur = &pending[i]
			break
		}
	}
	if cur == nil {
		return "", 0, 0, fmt.Errorf("资产 %s 不是待批资产(可能已加入或已禁止)", assetID)
	}
	target := cur.Title
	verb := "禁止测试"
	if approved {
		verb = "加入资产"
	}

	var woken, stopped int
	if approved {
		// P4-71 ①(卡上加入资产入口):待批资产经"允许访问"退役过的同
		// 形态防御(现行 PendingScopeAssets 过滤 retired,理论上到不了
		// 这里;单源调用幂等零落笔,语义钉死"加入资产必复活")。
		if _, rerr := swarm.ReviveRetiredAssets(ctx, s.Store, taskID, []string{assetID}, "creator"); rerr != nil {
			return "", 0, 0, fmt.Errorf("加入资产复活落账失败: %w", rerr)
		}
		st, err := s.Store.CurrentRoE(ctx, taskID)
		if err != nil {
			return "", 0, 0, err
		}
		nextExcluded := make([]string, 0, len(st.ExcludedAssets))
		for _, id := range st.ExcludedAssets {
			if id != assetID {
				nextExcluded = append(nextExcluded, id)
			}
		}
		if _, err := s.Store.Append(ctx, taskID, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{
				AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
				ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
				ExcludedAssets: nextExcluded,
				ScopeApproved:  mergeIDs(st.ScopeApproved, []string{assetID}),
				Note:           verb + " " + target,
			},
		}); err != nil {
			return "", 0, 0, err
		}
	} else {
		if _, err := swarm.ExcludeAssets(ctx, s.Store, s.helmOf(), taskID, []string{assetID}, "creator"); err != nil {
			return "", 0, 0, fmt.Errorf("禁止测试落账失败: %w", err)
		}
	}

	// 裁决记录(同拦截卡口径:账本先于副作用,ac- 请求号留档)。
	rec := rings.ApprovalRecord{
		RequestID: assetDecisionRequestID(assetID),
		StepID:    cur.StepID, UnitID: cur.UnitID,
		Note: "范围待批资产 " + target, DecidedBy: "creator", Reason: verb,
	}
	if approved {
		rec.Type = rings.ApprovalRecApproved
	} else {
		rec.Type = rings.ApprovalRecDenied
		rec.DenyLine = rings.DenyLineRoeScope
	}
	if reason != "" {
		rec.Reason = verb + ": " + reason
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rec,
	}); err != nil {
		return "", 0, 0, err
	}

	woken, stopped = s.resolveScopeWakes(taskID)
	return target, woken, stopped, nil
}

// mergeIDs 清单并集(去重保序;快照语义下的"并入"辅助)。
func mergeIDs(base, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(add))
	for _, id := range base {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range add {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// approvalRowToRecord 投影行 → 审批记录(裁决回写复用卡面上下文)。
func approvalRowToRecord(row rings.ApprovalRow) rings.ApprovalRecord {
	return rings.ApprovalRecord{
		RequestID: row.RequestID, StepID: row.StepID, UnitID: row.UnitID,
		Type: row.Type, ActionLevel: rings.ActionLevel(row.ActionLevel),
		HarmClass: rings.HarmClass(row.HarmClass), Intent: row.Intent,
		ToolName: row.ToolName, ToolArgs: []byte(row.ToolArgs),
		Note: row.Note, DenyLine: row.DenyLine, DecidedBy: row.DecidedBy,
		Reason: row.Reason, WaitMillis: row.WaitMillis,
	}
}

// ensureScopeAsset 目标入图(P4-54 起与牧者操作面共用 swarm.EnsureScopeAsset
// 同一实现):标题精确等于目标写法、或同主机锚点的不同写法(如 URL 形态
// 资产 vs host:端口 卡)直接复用既有资产;未入图的按目标提取 host 入图
// (来源发现、范围外冻结——归属判定随 roe 快照走,入图后按裁决转范围/
// 排除)。返回资产 ID。
func (s *Server) ensureScopeAsset(ctx context.Context, taskID, target string) (string, error) {
	id, _, err := swarm.EnsureScopeAsset(ctx, s.Store, taskID, target, rings.AssetSourceDiscovered, "creator")
	return id, err
}

// resolveScopeWakes 范围对账唤醒(免等下一轮研判):在跑主宰循环直达
// (有空位立即拉起单位,无空位排队中插队队首);循环不在跑(冬眠收场/
// 终态/未装配)时用裸循环执行对账本体(纯账本驱动,无即时拉起——
// 下一次拉起轮的调度自然消费回队步骤)。
func (s *Server) resolveScopeWakes(taskID string) (woken, terminated int) {
	var l *overmind.Loop
	if s.Loops != nil {
		l = s.Loops.Get(taskID)
	}
	if l == nil {
		l = &overmind.Loop{TaskID: taskID, Store: s.Store}
	}
	return l.ResolveScopeWaits(context.Background())
}
