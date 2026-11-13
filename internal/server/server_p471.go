// P4-71(问题清单 #87 ④⑤⑥,RoE 复核修正批次第二阶·末阶):审批记录
// 反向操作与退役资产复活的服务端落点。
//
// 反向操作语义(设计全图"审批记录反向操作+退役资产复活"框):每条已裁
// 决的审批记录详情可反悔——落新事件(approval.record reversed,含原记录
// 引用,不改历史 R1),卡/红点/资产图/未决面全量对账收口零僵尸:
//   - 资产审批·批准(加入资产)→ 反向"取消加入":等同摘除那套——
//     ExcludeAssets 全量联动(意图覆盖者销毁留痕、未决卡收口、互斥清理,
//     P4-69 机制原样);
//   - 资产审批·拒绝(禁止测试)→ 反向"恢复纳入":资产复活入范围
//     (retired 复活走 IncludeAssets 单源);原相关会话不恢复不续跑
//     (禁止时已销毁);唤醒主宰重新规划(新意图新单位);该 host 已重新
//     收编的如实答复"已在范围"、仅收口旧排除态零变更;
//   - 攻击卡·允许→ 反向"撤销允许":该会话攻击档撤销,该单位后续同类
//     攻击重新出卡;攻击卡·拒绝→ 反向"转为允许":授该会话攻击档,
//     该单位后续同类免卡——只调会话档不动单位(Executor 在飞登记表);
//   - 信息源访问记录(access,含分诊自动授/标记/历史第三键存量)→
//     反向"撤销信息源":RevokeInfoSource 既有(撤销粘性语义不变);
//   - 弹门批准(已执行的一次性动作)→ 无反向键(如实)。
//
// 会话处理铁则:目标类反向(取消加入/恢复纳入)走止损/复活联动;动作类
// 反向(攻击档)只调会话档不动单位;意图覆盖判定、对账收口全部复用
// P4-68/69 单源实现,禁止平行实现。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// 反向动作词表(读侧元数据与写侧端点共用;Label 为界面词,入术语清单)。
const (
	reverseUninclude  = "uninclude"   // 取消加入(资产批准反向)
	reverseReinclude  = "reinclude"   // 恢复纳入(资产拒绝反向)
	reverseUnallow    = "unallow_att" // 撤销允许(攻击卡允许反向)
	reverseAllow      = "allow_att"   // 转为允许(攻击卡拒绝反向)
	reverseRevokeSrc  = "revoke_src"  // 撤销信息源(access/triage 信息源结论反向)
	attackCardNotePre = "攻击行为卡:"      // 攻击卡族 Note 前缀(enterAttackCard 契约)
)

// reverseMeta 一条审批记录可用的反向操作(读侧元数据,详情 Sheet 据此
// 呈现反向键;无反向的记录不带本字段——弹门批准等已执行的一次性动作
// 如实无键)。
type reverseMeta struct {
	Action      string `json:"action"`
	Label       string `json:"label"`
	Target      string `json:"target,omitempty"`      // 反向针对的目标(host[:port]/资产标识)
	Unavailable string `json:"unavailable,omitempty"` // 非空=键在但不可用(人话原因,如实置灰)
}

// reverseLabelOf 动作 → 界面词。
func reverseLabelOf(action string) string {
	switch action {
	case reverseUninclude:
		return "取消加入"
	case reverseReinclude:
		return "恢复纳入"
	case reverseUnallow:
		return "撤销允许"
	case reverseAllow:
		return "转为允许"
	case reverseRevokeSrc:
		return "撤销信息源"
	}
	return ""
}

// reverseOpOf 按记录类型判定反向操作(读侧与写侧共用单源):
//   - sc-/ac- 范围卡族裁决:approved→取消加入 / denied→恢复纳入;
//   - ap- 弹门族裁决且 Note 为攻击卡(攻击行为卡:前缀,enterAttackCard
//     契约):approved→撤销允许 / denied→转为允许——可用性=单位在飞
//     (攻击档在 unitEnv 内存态,单位终态即失效,旧记录如实置灰);
//   - access(允许访问裁决,含 v1.2.3 历史第三键存量)→撤销信息源;
//   - triage 分诊留痕且结论为信息源(方法性访问/声明命中,含分诊自动授)
//     →撤销信息源;
//   - 其余(待决记录/弹门普通裁决/同组放行/研判后放行等已执行的一次性
//     动作)→ 无反向(如实)。
//
// unitActive 由调用方注入(攻击档可用性;nil 视为不可用)。
func reverseOpOf(row rings.ApprovalRow, unitActive func(unitID string) bool) *reverseMeta {
	isScopeCard := strings.HasPrefix(row.RequestID, rings.ScopeCardIDPrefix) ||
		strings.HasPrefix(row.RequestID, rings.AssetCardRecPrefix)
	switch {
	case isScopeCard && row.Type == rings.ApprovalRecApproved:
		return &reverseMeta{Action: reverseUninclude, Label: reverseLabelOf(reverseUninclude),
			Target: reverseScopeTarget(row.RequestID)}
	case isScopeCard && row.Type == rings.ApprovalRecDenied:
		return &reverseMeta{Action: reverseReinclude, Label: reverseLabelOf(reverseReinclude),
			Target: reverseScopeTarget(row.RequestID)}
	case row.Type == rings.ApprovalRecAccess:
		return &reverseMeta{Action: reverseRevokeSrc, Label: reverseLabelOf(reverseRevokeSrc),
			Target: reverseScopeTarget(row.RequestID)}
	case row.Type == rings.ApprovalRecTriage:
		// 分诊留痕:结论为信息源(自动授访问权的形态)可反=撤销信息源;
		// 候选/攻击/已撤销/非目标结论不设反向(撤销后重出卡由范围卡两
		// 键定音)。P4-72:合一五值 info_access 与存量旧词表值都认
		//(swarm.IsTriageInfoSource 归一)。
		verdict, _, _ := strings.Cut(row.Note, " basis=")
		verdict = strings.TrimPrefix(verdict, "verdict=")
		if swarm.IsTriageInfoSource(verdict) {
			return &reverseMeta{Action: reverseRevokeSrc, Label: reverseLabelOf(reverseRevokeSrc),
				Target: strings.TrimPrefix(row.RequestID, rings.TriageCardIDPrefix)}
		}
		return nil
	case (row.Type == rings.ApprovalRecApproved || row.Type == rings.ApprovalRecDenied) &&
		strings.HasPrefix(row.Note, attackCardNotePre):
		// 攻击行为卡裁决(批准=会话档允许/拒绝=会话档禁止,P4-70 ③):
		// 双向反只调会话档不动单位;单位已终态的档已销毁,如实置灰。
		action, label := reverseUnallow, "撤销允许"
		if row.Type == rings.ApprovalRecDenied {
			action, label = reverseAllow, "转为允许"
		}
		meta := &reverseMeta{Action: action, Label: label, Target: attackTargetOfNote(row.Note)}
		if unitActive == nil || !unitActive(row.UnitID) {
			meta.Unavailable = "单位已结束,这个攻击档已随单位销毁"
		}
		return meta
	}
	return nil
}

// reverseScopeTarget 范围卡/信息源记录的 RequestID → 目标呈现(sc-<host[:port]>
// 原样;ac-<资产ID> 呈现资产 ID,host 反解由执行侧查图完成)。
func reverseScopeTarget(rid string) string {
	if t := strings.TrimPrefix(rid, rings.ScopeCardIDPrefix); t != rid {
		return t
	}
	if t := strings.TrimPrefix(rid, rings.AssetCardRecPrefix); t != rid {
		return t
	}
	return rid
}

// attackTargetOfNote 攻击卡 Note → 目标呈现形态("攻击行为卡:<host[:port]>
// 是信息源(…)" 的 <host[:port]> 段;解析不出回原 Note 截断,防御)。
func attackTargetOfNote(note string) string {
	rest := strings.TrimPrefix(note, attackCardNotePre)
	if target, _, ok := strings.Cut(rest, " 是信息源"); ok && target != "" {
		return target
	}
	if len(rest) > 48 {
		rest = rest[:48]
	}
	return rest
}

// handleApprovalReverse 审批记录反向操作(P4-71 ②):POST
// /api/tasks/{id}/approvals/{rid}/reverse,body {"action":词表值}。
// 按原记录现值重新判定支持的动作(防前端过期载荷),全部落账本新事件
// (approval.record reversed,Note 带原记录引用,不改历史);各动作复用
// 既有机制单源——取消加入=ExcludeAssets 全量联动(P4-69)、恢复纳入=
// IncludeAssets(retired 复活单源,P4-71 ①)、攻击档双向=Executor 调档、
// 撤销信息源=RevokeInfoSource。瞬间返回(异步纪律既有)。
func (s *Server) handleApprovalReverse(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	rid := r.PathValue("rid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,审批记录只读"))
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	action := strings.TrimSpace(req.Action)
	if reverseLabelOf(action) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("未知的反向操作"))
		return
	}
	ctx := r.Context()
	// 原记录定位:该请求号族内最新一笔**已裁决**记录(approved/denied/
	// access/triage;跳过其后重新出卡的待决记录——待决卡用两键裁决,
	// 不是反向旧裁决),据此重新判定反向支持面。
	rows, err := s.Store.ApprovalByRequest(ctx, taskID, rid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var latest rings.ApprovalRow
	for i := len(rows) - 1; i >= 0; i-- {
		switch rows[i].Type {
		case rings.ApprovalRecApproved, rings.ApprovalRecDenied,
			rings.ApprovalRecAccess, rings.ApprovalRecTriage:
			latest = rows[i]
		}
		if latest.RequestID != "" {
			break
		}
	}
	if latest.RequestID == "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("审批记录 %s 不存在或尚未处理", rid))
		return
	}
	unitActive := func(unitID string) bool {
		exec := s.swarmExec()
		return exec != nil && exec.UnitActive(taskID, unitID)
	}
	meta := reverseOpOf(latest, unitActive)
	if meta == nil || meta.Action != action {
		writeErr(w, http.StatusConflict, errors.New("该记录不支持这个反向操作"))
		return
	}

	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()

	var message string
	switch action {
	case reverseUninclude:
		message, err = s.reverseUninclude(ctx, taskID, rid, latest)
	case reverseReinclude:
		message, err = s.reverseReinclude(ctx, taskID, rid, latest)
	case reverseUnallow, reverseAllow:
		message, err = s.reverseAttackRuling(ctx, taskID, rid, latest, action == reverseAllow)
	case reverseRevokeSrc:
		message, err = s.reverseRevokeSource(ctx, taskID, rid, latest)
	}
	if err != nil {
		writeErr(w, scopeReverseStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rid, "action": action, "message": message,
	})
}

// scopeReverseStatus 反向操作错误 → HTTP 状态(单位已终态等状态冲突
// 归 409——键在但不可用,与读面置灰同口径)。
func scopeReverseStatus(err error) int {
	if errors.Is(err, swarm.ErrUnitFinished) {
		return http.StatusConflict
	}
	return scopeDecisionStatus(err)
}

// reverseAssetIDOf 原记录 → 资产 ID(取消加入/恢复纳入的标的):sc- 拦截卡
// 按目标写法定位既有资产(EnsureScopeAsset 锚点等价,不重复入图;图上
// 没有的按发现形态入图——与卡裁决同口径);ac- 资产卡直接取资产 ID。
func (s *Server) reverseAssetIDOf(ctx context.Context, taskID, rid string) (string, error) {
	if aid := strings.TrimPrefix(rid, rings.AssetCardRecPrefix); aid != rid {
		return aid, nil
	}
	target := strings.TrimPrefix(rid, rings.ScopeCardIDPrefix)
	id, _, err := swarm.EnsureScopeAsset(ctx, s.Store, taskID, target, rings.AssetSourceDiscovered, "creator")
	return id, err
}

// reverseUninclude 取消加入:等同摘除那套(P4-69 机制原样)——ExcludeAssets
// 全量联动(意图覆盖者销毁留痕、未决卡收口、互斥清理)+ 对账唤醒。资产
// 已在范围外的如实归 AlreadyOut(零变更零止损)。返回人话结果。
func (s *Server) reverseUninclude(ctx context.Context, taskID, rid string, latest rings.ApprovalRow) (string, error) {
	assetID, err := s.reverseAssetIDOf(ctx, taskID, rid)
	if err != nil {
		return "", err
	}
	res, err := swarm.ExcludeAssets(ctx, s.Store, s.helmOf(), taskID, []string{assetID}, "creator")
	if err != nil {
		return "", fmt.Errorf("取消加入落账失败: %w", err)
	}
	// 对账收口(挂等卡与冻结单位全量重判,P4-69 单源)+ 唤醒主宰重规划。
	woken, terminated := s.resolveScopeWakes(taskID)
	s.wake(taskID)
	if len(res.Excluded) == 0 {
		return "该目标已在范围外,本次零变更", nil
	}
	msg := fmt.Sprintf("已按摘除处理:终止在途单位 %d 个,取消排队步骤 %d 个",
		len(res.KilledUnits), len(res.AbortedSteps))
	if terminated > 0 || woken > 0 {
		msg += fmt.Sprintf(",挂等收口 %d 个,唤醒 %d 个", terminated, woken)
	}
	s.appendReversedRecord(ctx, taskID, rid, latest, "取消加入", msg)
	return msg, nil
}

// reverseReinclude 恢复纳入:资产复活入范围(IncludeAssets 单源——retired
// 识别复活＋入批准清单＋互斥);非退役的 discovered 形态移出摘除清单后
// 仍处待批态的,并入批准清单完成契约"入范围"(seed/manual 资产天然在
// 闭包,无需并入)。原相关会话不恢复不续跑(禁止时已销毁,零挂等唤醒
// 预期;对账只收口此后重新出卡的未决卡);唤醒主宰重新规划(新意图新
// 单位)。该 host 已在此后经新发现重新收编的如实答复"已在范围"、仅
// 收口旧排除态零变更。
func (s *Server) reverseReinclude(ctx context.Context, taskID, rid string, latest rings.ApprovalRow) (string, error) {
	assetID, err := s.reverseAssetIDOf(ctx, taskID, rid)
	if err != nil {
		return "", err
	}
	res, err := swarm.IncludeAssets(ctx, s.Store, taskID, []string{assetID}, "creator")
	if err != nil {
		return "", fmt.Errorf("恢复纳入落账失败: %w", err)
	}
	title := assetTitleOfID(ctx, s.Store, taskID, assetID)
	if len(res.Revived) == 0 && len(res.Reincluded) == 0 && len(res.Approved) == 0 {
		// 已在范围(此后经新发现重新收编等):如实答复,仅收口旧排除
		// 态零变更(IncludeAssets 对 AlreadyIn 零落账;仍在排除清单的
		// 走上分支移回)。
		s.appendReversedRecord(ctx, taskID, rid, latest, "恢复纳入", "该目标已在范围,本次零变更")
		return "该目标已在测试范围内,本次零变更(仅确认,不改任何状态)", nil
	}
	// 契约"入范围":discovered 形态移回后若仍不在批准清单(回待批态),
	// 并入批准清单——反向恢复纳入一锤定音,不再回待批。
	st, err := s.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		return "", err
	}
	if !st.ScopeApprovedSet()[assetID] {
		if a, aerr := s.Store.Node(ctx, taskID, assetID); aerr == nil && a.Type == rings.NodeAsset &&
			rings.AssetSourceOf(a.Detail) == rings.AssetSourceDiscovered &&
			rings.AssetScopeOf(a.Detail) == rings.AssetScopeOut {
			next := mergeIDs(st.ScopeApproved, []string{assetID})
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
					ExcludedAssets: nextExcluded, ScopeApproved: next,
					Note: "恢复纳入 " + title,
				},
			}); err != nil {
				return "", err
			}
		}
	}
	// 对账收口(该目标此后若重新出卡,未决卡按批准语义自动收口,零僵尸)
	// + 唤醒主宰重新规划(新意图新单位)。禁止时销毁的旧会话已终态,
	// 对账不会碰它们(不复活旧会话)。
	s.resolveScopeWakes(taskID)
	s.wake(taskID)
	action := "把 " + title + " 移回测试范围"
	if len(res.Revived) > 0 {
		action = "复活 " + title + " 并重新纳入测试范围"
	}
	msg := "已" + action + ",主宰将重新规划(原相关会话不恢复)"
	s.appendReversedRecord(ctx, taskID, rid, latest, "恢复纳入", "已"+action+",主宰将重新规划")
	return msg, nil
}

// reverseAttackRuling 攻击档双向反(P4-71 ② 动作类铁则:只调会话档不动
// 单位):转为允许=授档(该单位会话内后续同类攻击免卡);撤销允许=档移除
// (后续同类攻击重新出卡——不是落禁止档)。单位已终态的档已销毁——
// 如实报错(键置灰的写侧兜底)。
func (s *Server) reverseAttackRuling(ctx context.Context, taskID, rid string, latest rings.ApprovalRow, allow bool) (string, error) {
	exec := s.swarmExec()
	if exec == nil {
		return "", errors.New("虫群执行器未装配,无法调整攻击档")
	}
	display := attackTargetOfNote(latest.Note)
	host := display
	if h, ok := swarm.ParseInfoSourceHost(display); ok {
		host = h
	}
	var allowed *bool
	if allow {
		allowed = new(bool)
		*allowed = true
	}
	if err := exec.SetUnitAttackRuling(taskID, latest.UnitID, host, allowed); err != nil {
		if errors.Is(err, swarm.ErrUnitFinished) {
			return "", fmt.Errorf("单位已结束,这个攻击档已随单位销毁,无需反向: %w", err)
		}
		return "", err
	}
	verb, msg := "撤销允许", fmt.Sprintf("已撤销该单位的攻击档,它后续对 %s 的同类攻击会重新征求批准", display)
	if allow {
		verb, msg = "转为允许", fmt.Sprintf("已授该单位攻击档,它本会话内对 %s 的后续同类攻击不再征求批准", display)
	}
	s.appendReversedRecord(ctx, taskID, rid, latest, verb, msg)
	return msg, nil
}

// reverseRevokeSource 撤销信息源:RevokeInfoSource 既有(撤销粘性语义
// 不变——后续访问按候选出卡,由范围卡两键一锤定音)。host 不在清单的
// (已撤销过/存量)如实答复零变更。
func (s *Server) reverseRevokeSource(ctx context.Context, taskID, rid string, latest rings.ApprovalRow) (string, error) {
	host := reverseHostOfRecord(ctx, s.Store, taskID, latest)
	if host == "" {
		return "", errors.New("该记录无主机标识,不适用撤销信息源")
	}
	if err := swarm.RevokeInfoSource(ctx, s.Store, taskID, host, "creator"); err != nil {
		// 不在清单(此前已撤销/经其他通道移除):如实答复零变更。
		s.appendReversedRecord(ctx, taskID, rid, latest, "撤销信息源", "该地址不在信息源清单,本次零变更")
		return fmt.Sprintf("%s 不在信息源清单(可能已撤销过),本次零变更", host), nil
	}
	msg := fmt.Sprintf("已撤销 %s 的信息源授权,后续访问恢复拦截", host)
	s.appendReversedRecord(ctx, taskID, rid, latest, "撤销信息源", msg)
	runlog.Infof("审批记录反向: %s 撤销信息源 %s", taskID, host)
	return msg, nil
}

// reverseHostOfRecord 信息源类记录(access/triage)→ 信息源 host:
// sc-<host[:port]> 直接反解;ac-<资产ID> 查资产标题取锚点;tg-<host>
// 直接反解。
func reverseHostOfRecord(ctx context.Context, store *rings.Store, taskID string, row rings.ApprovalRow) string {
	if t := strings.TrimPrefix(row.RequestID, rings.ScopeCardIDPrefix); t != row.RequestID {
		if h, ok := swarm.ParseInfoSourceHost(t); ok {
			return h
		}
		return ""
	}
	if t := strings.TrimPrefix(row.RequestID, rings.TriageCardIDPrefix); t != row.RequestID {
		if h, ok := swarm.ParseInfoSourceHost(t); ok {
			return h
		}
		return ""
	}
	if aid := strings.TrimPrefix(row.RequestID, rings.AssetCardRecPrefix); aid != row.RequestID {
		return assetAnchorHost(ctx, store, taskID, aid)
	}
	return ""
}

// assetAnchorHost 资产 ID → 主机锚点 host(查不到返回空串)。
func assetAnchorHost(ctx context.Context, store *rings.Store, taskID, assetID string) string {
	a, err := store.Node(ctx, taskID, assetID)
	if err != nil || a.Type != rings.NodeAsset {
		return ""
	}
	if anchor, ok := rings.ScopeAnchorOf(strings.TrimSpace(a.Title)); ok {
		return anchor.Host
	}
	return ""
}

// assetTitleOfID 资产 ID → 标题(查不到回 ID 本身;答复人话用)。
func assetTitleOfID(ctx context.Context, store *rings.Store, taskID, assetID string) string {
	if a, err := store.Node(ctx, taskID, assetID); err == nil && a.Type == rings.NodeAsset {
		return a.Title
	}
	return assetID
}

// appendReversedRecord 反向裁决留痕(approval.record reversed):落新事件
// 不改历史,Note 带原记录引用。Reason 为人话结果陈述(上面板详情)。
func (s *Server) appendReversedRecord(ctx context.Context, taskID, rid string, latest rings.ApprovalRow, verb, result string) {
	rec := rings.ApprovalRecord{
		RequestID: reverseRequestID(), StepID: latest.StepID, UnitID: latest.UnitID,
		Type: rings.ApprovalRecReversed, DecidedBy: "creator",
		Note:   "反向 " + rid + ": " + verb,
		Reason: result,
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rec,
	}); err != nil {
		runlog.Errorf("审批记录反向留痕失败: %s %s: %v", taskID, rid, err)
	}
	runlog.Infof("审批记录反向: %s %s %s(%s)", taskID, rid, verb, result)
}

// reverseRequestID 反向裁决记录的请求号(rv-<rand>,与原记录族不共号——
// 不污染卡状态机与待决收口判定的 RequestID 族语义)。
func reverseRequestID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("rv-%d", time.Now().UnixNano())
	}
	return "rv-" + hex.EncodeToString(b[:])
}
