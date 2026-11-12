package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// cryptoRandRead crypto/rand.Read 的薄别名(scopeAssetIDSuffix 用,便于
// 与既有同形态实现对照)。
func cryptoRandRead(b []byte) (int, error) { return rand.Read(b) }

// ---- 资产范围操作(P4-45,问题清单 #61 ③④) ----
//
// 摘除即止损(造物主定案语义,两种任务类型通用):RoE 摘除资产后,
// 覆盖该资产的所有在跑/排队步骤立即终止——在跑单位走舵杆 kill(与
// 熔断单位同款真中断口径:单位 ctx cancel、步骤落 aborted),排队
// 步骤整步取消(aborted,留痕交主宰重排);主宰下一轮不再派覆盖它的
// 意图(派发入口守卫 covers 判定既有,harvest 派发侧)。面板
// (/roe/assets 端点)与牧者(set_asset_scope 工具)两路同走本文件——
// 同一 roe.updated 账本状态、同一条止损链路,无旁路(R7)。
//
// 时序约定:roe.updated 先落账、止损后执行——止损进行中派发守卫已
// 生效,不会漏进覆盖已摘除资产的新意图。停止的只是"覆盖已摘除资产"
// 的工作,其余单位零打扰。

// ScopeHit 一个受影响的步骤(确认弹窗与执行结果的数据面)。
type ScopeHit struct {
	StepID string `json:"step_id"`
	Title  string `json:"title"`
}

// ScopeImpact 摘除一批资产的影响预览:确认告知"将终止 N 个在途单位"
// 的数据源(纯读,不落账)。
type ScopeImpact struct {
	Units  []ScopeHit `json:"units"`  // 在跑单位覆盖了待摘除资产(一步骤一单位)
	Queued []ScopeHit `json:"queued"` // 排队步骤覆盖了待摘除资产(含等前置)
}

// ScopeImpactOf 预览:待摘除资产命中的在跑单位与排队步骤(纯读)。
func ScopeImpactOf(ctx context.Context, store *rings.Store, taskID string, assetIDs []string) (*ScopeImpact, error) {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("swarm: 范围影响查步骤: %w", err)
	}
	hits, err := stepsCoveringAssets(ctx, store, taskID, steps, assetIDs)
	if err != nil {
		return nil, err
	}
	out := &ScopeImpact{Units: []ScopeHit{}, Queued: []ScopeHit{}}
	for _, h := range hits {
		switch h.status {
		case "running":
			out.Units = append(out.Units, h.hit)
		case "dispatched":
			out.Queued = append(out.Queued, h.hit)
		}
	}
	return out, nil
}

// scopeHitStep 预览/止损共用的中间条目(status 随读随新)。
type scopeHitStep struct {
	hit    ScopeHit
	status string
}

// stepsCoveringAssets 步骤清单里意图锚定命中资产集合的条目(按步骤 ID
// 稳定排序)。P4-68 ②(拒绝分族):意图覆盖判定＝图查询——covers 边直接
// 锚定 ∪ verifies→假设→about 锚定(验证类步骤的假设锚点＝其指向的
// 目标资产),与被摘除资产求交(直接 ID 命中,或锚定资产的写法粒度落
// 在摘除面内——ExcludedCoversTarget 同口径);零文本匹配。
func stepsCoveringAssets(ctx context.Context, store *rings.Store, taskID string, steps []rings.NodeFull, assetIDs []string) ([]scopeHitStep, error) {
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("swarm: 范围影响查资产: %w", err)
	}
	return stepsCoveringAssetsOf(ctx, store, taskID, steps, assetIDs, assets)
}

// stepIntentCoversAssets 单个步骤的意图锚定资产与资产集合求交(非空＝
// 覆盖)——P4-68 ② 拒绝分族在命令路的判定键(已排除目标的触碰分族:
// 覆盖→止损口径、不覆盖→只拒该条命令);图查询零文本匹配。读不出或
// 步骤不在图上返回 false(防御:不猜)。
func stepIntentCoversAssets(ctx context.Context, store *rings.Store, taskID, stepID string, assetIDs []string, assets []rings.NodeFull) bool {
	if stepID == "" || len(assetIDs) == 0 {
		return false
	}
	step, err := store.Node(ctx, taskID, stepID)
	if err != nil || step.Type != rings.NodeStep {
		return false
	}
	hits, err := stepsCoveringAssetsOf(ctx, store, taskID, []rings.NodeFull{*step}, assetIDs, assets)
	return err == nil && len(hits) > 0
}

// StepIntentCoversAssets stepIntentCoversAssets 的导出形态(P4-69 ①:
// 挂等冻结单位被排除时的收口分族由 overmind 对账复用——同一判定单源,
// 禁止另写)。assets 由调用方传现值清单(对账已查,免重复)。
func StepIntentCoversAssets(ctx context.Context, store *rings.Store, taskID, stepID string, assetIDs []string, assets []rings.NodeFull) bool {
	return stepIntentCoversAssets(ctx, store, taskID, stepID, assetIDs, assets)
}

// stepsCoveringAssetsOf stepsCoveringAssets 的核心(资产清单由调用方
// 传入,止损/预览/命令路判定共用一份现值资产,免重复查询)。
func stepsCoveringAssetsOf(ctx context.Context, store *rings.Store, taskID string, steps []rings.NodeFull, assetIDs []string, assets []rings.NodeFull) ([]scopeHitStep, error) {
	want := map[string]bool{}
	for _, id := range assetIDs {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	// 意图锚定资产集:covers 直接锚定 + verifies→假设→about 锚定。
	anchored := map[string]map[string]bool{} // stepID -> 锚定资产 ID 集
	addAnchors := func(from, to string) {
		if anchored[from] == nil {
			anchored[from] = map[string]bool{}
		}
		anchored[from][to] = true
	}
	if edges, err := store.Edges(ctx, taskID, rings.EdgeCovers); err == nil {
		for _, e := range edges {
			addAnchors(e.From, e.To)
		}
	} else {
		return nil, fmt.Errorf("swarm: 范围影响查 covers 边: %w", err)
	}
	// 验证类步骤(Step→verifies→Hypothesis→about→Asset):假设的 about
	// 锚点＝提议时指向的目标资产,同为该步骤的意图锚定。
	if verifies, verr := store.Edges(ctx, taskID, rings.EdgeVerifies); verr == nil && len(verifies) > 0 {
		hypAssets := map[string][]string{}
		if abouts, aerr := store.Edges(ctx, taskID, rings.EdgeAbout); aerr == nil {
			for _, e := range abouts {
				hypAssets[e.From] = append(hypAssets[e.From], e.To)
			}
		}
		for _, e := range verifies {
			for _, a := range hypAssets[e.To] {
				addAnchors(e.From, a)
			}
		}
	}
	// 锚定命中:直接 ID ∈ 摘除集,或锚定资产的写法粒度落在摘除面内
	//(同 host 不同端口的兄弟资产同样命中——与围栏/摘除的写法粒度
	// 同口径)。
	covered := func(assetID string) bool {
		if want[assetID] {
			return true
		}
		for _, a := range assets {
			if a.ID != assetID {
				continue
			}
			if anchor, ok := rings.ScopeAnchorOf(strings.TrimSpace(a.Title)); ok {
				return rings.ExcludedCoversTarget(assets, want, anchor.Host, anchor.Port)
			}
			return false
		}
		return false
	}
	var out []scopeHitStep
	for _, s := range steps {
		hit := false
		for id := range anchored[s.ID] {
			if covered(id) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		out = append(out, scopeHitStep{hit: ScopeHit{StepID: s.ID, Title: s.Title}, status: s.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].hit.StepID < out[j].hit.StepID })
	return out, nil
}

// idIn 清单包含判定(shepherd 侧 containsStr 的本地同款;包内既有词
// 不复用外部命名)。
func idIn(list []string, id string) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// EnsureScopeAsset 资产标识解析入图(P4-54 牧者操作面/server 范围卡
// 裁决共用):标识可以是图上资产 ID、标题精确匹配、或同主机锚点的
// 既有资产(P4-52 锚点等价——URL 形态与 host:端口 写法对上同一资产,
// 不重复入图);都不命中时按标识写法入图(类按锚点推断,IP→host、
// 通配/域名→domain;来源与范围归属由调用方声明:范围卡=发现+范围外,
// 牧者纳入=手动+范围内,通配摘除=发现+范围外——与建任务初始摘除
// 同形态)。返回(资产 ID, 是否新建)。
func EnsureScopeAsset(ctx context.Context, store *rings.Store, taskID, ident string, source, actor string) (string, bool, error) {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return "", false, errors.New("swarm: 资产标识为空")
	}
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return "", false, fmt.Errorf("swarm: 查图上资产: %w", err)
	}
	for _, a := range assets {
		if a.ID == ident || a.Title == ident {
			return a.ID, false, nil
		}
	}
	if anchor, ok := rings.ScopeAnchorOf(ident); ok {
		for _, a := range assets {
			if a2, ok2 := rings.ScopeAnchorOf(strings.TrimSpace(a.Title)); ok2 &&
				a2.Host == anchor.Host && a2.Port == anchor.Port &&
				a2.Wildcard == anchor.Wildcard {
				return a.ID, false, nil
			}
		}
	}
	class := rings.AssetDomain
	if anchor, ok := rings.ScopeAnchorOf(ident); ok && anchor.IsIP {
		class = rings.AssetHost
	}
	scope := rings.AssetScopeIn
	if source == rings.AssetSourceDiscovered {
		// 发现形态入图默认范围外(与范围卡目标入图同口径,归属随裁决
		// 或放行转范围);手动形态天然范围内。
		scope = rings.AssetScopeOut
	}
	id := "a-" + scopeAssetIDSuffix()
	detail := map[string]any{"source": source, "scope": scope}
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: actor,
		Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeAsset, Title: ident,
			Status: "active", AssetClass: class, Detail: detail,
		},
	}); err != nil {
		return "", false, fmt.Errorf("swarm: 资产入图: %w", err)
	}
	return id, true, nil
}

// scopeAssetIDSuffix 资产 ID 的随机后缀(同秒多条不撞 ID;与收割入图
// 的 a-<hex> 形态一致)。
func scopeAssetIDSuffix() string {
	var b [4]byte
	if _, err := cryptoRandRead(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ScopeChange 一次范围变更(纳入/摘除两向)的结果:面板 toast、牧者
// 人话答复与账本对账的共用数据面。
type ScopeChange struct {
	Excluded     []string   `json:"excluded"`      // 本次实际摘除的资产 ID(止损针对它们)
	Reincluded   []string   `json:"reincluded"`    // 本次移回范围的资产 ID(原在摘除清单)
	Approved     []string   `json:"approved"`      // 本次放行的待批资产 ID(渗透任务)
	Revived      []string   `json:"revived"`       // 本次复活的退役资产 ID(P4-71 ①:纳入遇 retired 一律复活)
	AlreadyIn    []string   `json:"already_in"`    // 纳入时本就在范围内的资产 ID(零变更)
	AlreadyOut   []string   `json:"already_out"`   // 摘除时本就范围外的资产 ID(零变更)
	KilledUnits  []ScopeHit `json:"killed_units"`  // 摘除终止的在途单位
	AbortedSteps []ScopeHit `json:"aborted_steps"` // 摘除取消的排队步骤
}

// assetTitleOf 资产 ID → 标题(账本外词不上答复;查不到回 ID 本身)。
func assetTitleOf(ctx context.Context, store *rings.Store, taskID string, id string) string {
	if a, err := store.Node(ctx, taskID, id); err == nil && a.Type == rings.NodeAsset {
		return a.Title
	}
	return id
}

// resolveAssetIDs 校验资产 ID 全部在图上,返回存在性错误(防陈旧标识
// 指向不存在的资产;错误里带现存清单供牧者自纠)。
func resolveAssetIDs(ctx context.Context, store *rings.Store, taskID string, assetIDs []string) error {
	if len(assetIDs) == 0 {
		return errors.New("swarm: 资产清单为空")
	}
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return fmt.Errorf("swarm: 查图上资产: %w", err)
	}
	existing := map[string]bool{}
	for _, a := range assets {
		existing[a.ID] = true
	}
	var unknown []string
	for _, id := range assetIDs {
		if !existing[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("swarm: 资产 %s 不在本任务图上(可用资产: %s)",
			strings.Join(unknown, "、"), assetIDList(assets))
	}
	return nil
}

// assetIDList 资产清单的 ID 摘要串(错误提示用,≤12 条防刷屏)。
func assetIDList(assets []rings.NodeFull) string {
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ID)
	}
	if len(ids) > 12 {
		ids = append(ids[:12], fmt.Sprintf("…共 %d 个", len(assets)))
	}
	return strings.Join(ids, "、")
}

// ExcludeAssets 摘除资产(牧者 set_asset_scope 摘除向;面板快照端点
// 复用 StopLossForAssets 同一止损本体):新摘除并入清单 → roe.updated
// 落账 → 覆盖新摘除资产的在途止损。已摘除条目如实归 AlreadyOut(零
// 变更零落账)。唤醒主宰由调用方执行。
func ExcludeAssets(ctx context.Context, store *rings.Store, helm *Helm, taskID string, assetIDs []string, actor string) (*ScopeChange, error) {
	if err := resolveAssetIDs(ctx, store, taskID, assetIDs); err != nil {
		return nil, err
	}
	st, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 摘除读当前范围: %w", err)
	}
	res := &ScopeChange{}
	old := st.ExcludedAssetSet()
	next := append([]string{}, st.ExcludedAssets...)
	inSet := map[string]bool{}
	for _, id := range next {
		inSet[id] = true
	}
	for _, id := range assetIDs {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if old[id] {
			res.AlreadyOut = append(res.AlreadyOut, id)
			continue
		}
		res.Excluded = append(res.Excluded, id)
		if !inSet[id] {
			next = append(next, id)
			inSet[id] = true
		}
	}
	if len(res.Excluded) > 0 {
		var titles []string
		for _, id := range res.Excluded {
			titles = append(titles, assetTitleOf(ctx, store, taskID, id))
		}
		// P4-69 ②(状态互斥):摘除同步从批准清单剔除——排除与批准不
		// 并存(实锤 a-72dab0e5 同列两清单),摘除快照不再原样透传
		// scope_approved。
		nextApproved := make([]string, 0, len(st.ScopeApproved))
		for _, id := range st.ScopeApproved {
			if !idIn(res.Excluded, id) {
				nextApproved = append(nextApproved, id)
			}
		}
		if _, err := store.Append(ctx, taskID, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: actor,
			Payload: rings.RoeUpdated{
				AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
				ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
				ExcludedAssets: next, ScopeApproved: nextApproved,
				Note: "摘除 " + strings.Join(titles, "、"),
			},
		}); err != nil {
			return nil, fmt.Errorf("swarm: 摘除落账: %w", err)
		}
		var serr error
		if res.KilledUnits, res.AbortedSteps, serr = StopLossForAssets(ctx, store, helm, taskID, res.Excluded, actor); serr != nil {
			return res, serr
		}
		// 摘除连坐废弃(P4-55 审核迭代,造物主 2026-11-08 定案:动"对象"
		// 摘资产→声明/假设程序确定性全废弃):摘除覆盖资产上锚定的非终态
		// 声明与假设批量转已废弃(理由"目标已摘除"),不问主宰、不挂起;
		// 幂等,零命中零落笔;失败如实跳过可重入(勾回不自动复活)。
		if dClaims, dHypos, derr := rings.DiscardAnchoredFindings(ctx, store, taskID, rings.DiscardReasonExcluded, actor); derr != nil {
			runlog.Warnf("摘除连坐废弃失败(如实跳过,可重入): %s %v", taskID, derr)
		} else if len(dClaims) > 0 || len(dHypos) > 0 {
			runlog.Infof("摘除连坐废弃: %s 判断 %d 个,假设 %d 个转已废弃(目标已摘除)", taskID, len(dClaims), len(dHypos))
		}
	}
	return res, nil
}

// IncludeAssets 纳入资产(牧者 set_asset_scope 纳入向/反向键"恢复纳入"
// 共用):渗透任务的待批资产放行(scope_approved)+ 已摘除资产移回范围
// (摘除清单去除)+ 退役资产复活(P4-71 ①:retired 一律复活——转信息源
// 退役的资产经纳入路径收回测试范围,不再误答"本就范围内");红队/未标
// 任务的常规资产无待批语义(本就可测,如实归 AlreadyIn 零变更)。纳入
// 无在途可终止,零止损;唤醒主宰由调用方执行。
func IncludeAssets(ctx context.Context, store *rings.Store, taskID string, assetIDs []string, actor string) (*ScopeChange, error) {
	if err := resolveAssetIDs(ctx, store, taskID, assetIDs); err != nil {
		return nil, err
	}
	st, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 纳入读当前范围: %w", err)
	}
	pendingSet := map[string]bool{}
	if pending, perr := store.PendingScopeAssets(ctx, taskID); perr == nil {
		for _, p := range pending {
			pendingSet[p.ID] = true
		}
	}
	excluded := st.ExcludedAssetSet()
	// P4-71 ①:图上资产的退役态——纳入遇 retired 一律复活(写法粒度,
	// 同 host 其余退役节点不连带),retired 不再落 AlreadyIn 误答。
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("swarm: 纳入查图上资产: %w", err)
	}
	retiredSet := map[string]bool{}
	for _, a := range assets {
		if a.Status == rings.AssetStatusRetired {
			retiredSet[a.ID] = true
		}
	}
	res := &ScopeChange{}
	for _, id := range assetIDs {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		switch {
		case retiredSet[id]:
			// P4-71 ①:退役识别优先——retired 资产即使因历史脏数据并存于
			// 摘除清单,也按"复活＋入批准清单＋互斥移回"整体处理(与
			// AllowScopeAccess 不动排除清单的写侧对称),不落 Reincluded
			// 分支漏复活。
			res.Revived = append(res.Revived, id)
		case pendingSet[id]:
			res.Approved = append(res.Approved, id)
		case excluded[id]:
			res.Reincluded = append(res.Reincluded, id)
		default:
			res.AlreadyIn = append(res.AlreadyIn, id)
		}
	}
	if len(res.Approved) == 0 && len(res.Reincluded) == 0 && len(res.Revived) == 0 {
		return res, nil // 全部本就范围内:零变更零落账,如实答复
	}
	// P4-71 ①:退役资产复活(节点级留痕,Note"纳入复活")。
	revived, rerr := ReviveRetiredAssets(ctx, store, taskID, res.Revived, actor)
	if rerr != nil {
		return nil, rerr
	}
	_ = revived
	// 复活资产与摘除互斥:仍在摘除清单的(历史脏数据并存)一并移回。
	nextExcluded := make([]string, 0, len(st.ExcludedAssets))
	for _, id := range st.ExcludedAssets {
		if !idIn(res.Reincluded, id) && !idIn(res.Revived, id) {
			nextExcluded = append(nextExcluded, id)
		}
	}
	nextApproved := append([]string{}, st.ScopeApproved...)
	for _, id := range res.Approved {
		if !idIn(nextApproved, id) {
			nextApproved = append(nextApproved, id)
		}
	}
	// 复活即入批准清单(条目契约:复活＋入批准清单＋互斥检查)——退役
	// 资产复活后须立即可测,不再回待批。
	for _, id := range res.Revived {
		if !idIn(nextApproved, id) {
			nextApproved = append(nextApproved, id)
		}
	}
	var parts []string
	for _, id := range res.Revived {
		parts = append(parts, "复活 "+assetTitleOf(ctx, store, taskID, id))
	}
	for _, id := range res.Reincluded {
		parts = append(parts, "纳入 "+assetTitleOf(ctx, store, taskID, id))
	}
	for _, id := range res.Approved {
		parts = append(parts, "放行 "+assetTitleOf(ctx, store, taskID, id))
	}
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: actor,
		Payload: rings.RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
			ExcludedAssets: nextExcluded, ScopeApproved: nextApproved,
			Note: strings.Join(parts, ";"),
		},
	}); err != nil {
		return nil, fmt.Errorf("swarm: 纳入落账: %w", err)
	}
	return res, nil
}

// ReviveRetiredAssets 退役资产复活(P4-71 ①,问题清单 #87 ⑤):纳入路径
// 识别 retired 资产→复活(NodeStateChanged retired→active 留痕,Note
// "纳入复活"),资产图/待批清单/资产树等读面自然复现(status 过滤按
// 现值走,零读面改动)。复活粒度＝按纳入的资产节点(写法粒度)——同
// host 其余退役节点不连带复活(要收编逐个纳入、各自复活留痕)。幂等
// (非 retired 跳过零落笔)。返回本次复活的资产 ID(与输入等价,留作
// 对账)。批准清单并入与互斥剔除由调用方随 roe.updated 落账(本函数
// 只管节点状态,清单语义归范围操作单源)。
func ReviveRetiredAssets(ctx context.Context, store *rings.Store, taskID string, assetIDs []string, actor string) ([]string, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	var evs []rings.Event
	revived := make([]string, 0, len(assetIDs))
	for _, id := range assetIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		a, err := store.Node(ctx, taskID, id)
		if err != nil || a.Type != rings.NodeAsset {
			continue // 陈旧引用:如实跳过(resolveAssetIDs 已挡主流)
		}
		if a.Status != rings.AssetStatusRetired {
			continue // 幂等:非退役零落笔
		}
		revived = append(revived, id)
		evs = append(evs, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: actor,
			Payload: rings.NodeStateChanged{
				NodeID: a.ID, Status: "active", Note: "纳入复活",
			},
		})
	}
	if len(evs) == 0 {
		return nil, nil
	}
	if _, err := store.Append(ctx, taskID, evs...); err != nil {
		return nil, fmt.Errorf("swarm: 退役资产复活落账: %w", err)
	}
	runlog.Infof("退役资产复活: %s %d 个(纳入复活)", taskID, len(evs))
	return revived, nil
}

// ---- 范围卡第三键"允许访问"(P4-68 ①,问题清单 #86 B) ----

// ParseInfoSourceHost 标记入口的地址归一:host[:port] → 裸 host(小写;
// 访问权与信息源清单同为 host 级);非主机形态返回 false。
func ParseInfoSourceHost(s string) (string, bool) {
	if a, ok := rings.ScopeAnchorOf(strings.TrimSpace(s)); ok {
		return a.Host, true
	}
	return "", false
}

// AllowScopeAccess 授访问权(host 入信息源清单)的统一入口——卡上手授
// ＝分诊自动授＝牧者标记＝RoE 面板标记,一个机制多种入口(P4-67 信息源
// 清单同一状态):
//   - GrantInfoSource:host 入任务信息源清单(后续访问零出卡;撤销恢复
//     拦截——RevokeInfoSource 既有);
//   - 图上同主机资产退役(rings.AssetStatusRetired,状态迁移留痕不删
//     节点):资产图/待批清单/主宰快照/报告等读面一律过滤——不入资产
//     图、不进范围闭包、不算覆盖、不被规划测试、不进资产树分组;
//   - 不触发止损、不动排除清单(允许访问不是拒绝:红队照常可测、渗透
//     访问放行;对它的攻击行为仍走攻击行为卡——denyScope 清单命中
//     分支按动作复查)。
//
// 挂等该目标的冻结单位由调用方对账唤醒(overmind.ResolveScopeWaits
// 信息源分支:回可领取插队队首,原会话续跑)。
func AllowScopeAccess(ctx context.Context, store *rings.Store, taskID, host, basis, actor string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if a, ok := rings.ScopeAnchorOf(host); ok {
		host = a.Host // 端口无关:访问权与信息源清单同为 host 级
	}
	if err := GrantInfoSource(ctx, store, taskID, host, basis, actor); err != nil {
		return err
	}
	retireAssetsOfHost(ctx, store, taskID, host, actor)
	return nil
}

// retireAssetsOfHost 图上同主机资产的退役(host 级:同 host 不同端口的
// 资产一并退役——访问权 host 级,资产图呈现同步消失)。幂等(已退役
// 跳过);退役留痕失败如实记档不回滚(访问权已授,账本只追加)。
func retireAssetsOfHost(ctx context.Context, store *rings.Store, taskID, host, actor string) {
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		runlog.Warnf("允许访问:查资产失败(退役跳过,如实记档): %s %s: %v", taskID, host, err)
		return
	}
	var evs []rings.Event
	for _, a := range assets {
		if a.Status == rings.AssetStatusRetired {
			continue
		}
		anchor, ok := rings.ScopeAnchorOf(strings.TrimSpace(a.Title))
		if !ok || anchor.Host != host {
			continue
		}
		evs = append(evs, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: actor,
			Payload: rings.NodeStateChanged{
				NodeID: a.ID, Status: rings.AssetStatusRetired,
				Note: "允许访问:转信息源,不收编",
			},
		})
	}
	if len(evs) == 0 {
		return
	}
	if _, err := store.Append(ctx, taskID, evs...); err != nil {
		runlog.Warnf("允许访问:资产退役落账失败(如实记档): %s %s: %v", taskID, host, err)
		return
	}
	runlog.Infof("允许访问: %s %s 资产退役 %d 个(转信息源,不收编)", taskID, host, len(evs))
}

// StopLossForAssets 在途止损本体(P4-45 ①b;roe.updated 已落账后调用,
// 面板与牧者两路共用):
//   - 意图覆盖任一已摘除资产的在跑步骤 → 舵杆 kill(与熔断单位同款真
//     中断口径,单位终止留痕、步骤落 aborted);
//   - 意图覆盖任一已摘除资产的排队步骤(dispatched,含等前置)→ 整步
//     aborted,事件 Note"范围摘除"留痕,交主宰重排(摘除后派发守卫
//     已拒新覆盖意图,重排只会换方向)。
//     P4-68 ②:"覆盖"收紧为意图覆盖(stepsCoveringAssets 单源:图查询
//     covers∪verifies→about＋写法粒度,零文本匹配)——意图不覆盖、仅命
//     令顺带触碰被摘除目标的单位不在止损面(命令路只拒该条命令,见
//     scopeguard denyExcludedTouch)。
//
// 竞态防护:排队步骤取消走 AppendAtRevision(领取把步骤推走时整批
// 重读——重读后它已成在跑,转走 kill 分支);在跑单位 kill 由 KillWork
// 内部现读状态校验(窗口内自然落地的单位如实不计入)。批量取消后带
// 一次收敛复查(领取竞态残留在跑条目补杀;≤3 轮,轮间隔让引擎把
// 已停单位的 aborted 终态写完)。
func StopLossForAssets(ctx context.Context, store *rings.Store, helm *Helm, taskID string, removedIDs []string, actor string) (killed, aborted []ScopeHit, err error) {
	killed, aborted = []ScopeHit{}, []ScopeHit{}
	want := map[string]bool{}
	var titles []string
	for _, id := range removedIDs {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
			titles = append(titles, assetTitleOf(ctx, store, taskID, id))
		}
	}
	if len(want) == 0 {
		return killed, aborted, nil
	}
	reason := fmt.Sprintf("范围摘除止损:资产 %s 已从授权范围摘除", strings.Join(titles, "、"))
	killedSet := map[string]bool{}
	for pass := 0; pass < 3; pass++ {
		if pass > 0 {
			select {
			case <-ctx.Done():
				return killed, aborted, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		rev, err := store.CurrentRevision(ctx, taskID)
		if err != nil {
			return killed, aborted, fmt.Errorf("swarm: 止损查图版本: %w", err)
		}
		steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
		if err != nil {
			return killed, aborted, fmt.Errorf("swarm: 止损查步骤: %w", err)
		}
		hits, err := stepsCoveringAssets(ctx, store, taskID, steps, removedIDs)
		if err != nil {
			return killed, aborted, err
		}
		var abortEvs []rings.Event
		var abortHits []ScopeHit
		var running []ScopeHit
		for _, h := range hits {
			switch h.status {
			case "dispatched":
				abortEvs = append(abortEvs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: actor,
					Payload: rings.NodeStateChanged{NodeID: h.hit.StepID, Status: "aborted", Note: "范围摘除"},
				})
				abortHits = append(abortHits, h.hit)
			case "running":
				if !killedSet[h.hit.StepID] {
					running = append(running, h.hit)
				}
			}
		}
		if len(abortEvs) > 0 {
			if _, err := store.AppendAtRevision(ctx, taskID, rev, abortEvs...); err != nil {
				if errors.Is(err, rings.ErrStaleRevision) {
					continue // 领取竞态:整批重读(该步骤已成在跑,转 kill 分支)
				}
				return killed, aborted, fmt.Errorf("swarm: 止损取消排队步骤: %w", err)
			}
			aborted = append(aborted, abortHits...)
		}
		if helm != nil {
			for _, h := range running {
				if _, kerr := helm.KillWork(ctx, taskID, h.StepID, reason, actor); kerr != nil {
					// 单位恰在窗口内自然落地(已不在执行中)——如实不计入
					runlog.Infof("范围止损: 步骤 %s 无需终止(%v)", h.StepID, kerr)
					continue
				}
				killed = append(killed, h)
				killedSet[h.StepID] = true
			}
		}
		// 收敛复查:无遗留(在跑未杀/排队未取消)即完成。
		hits2, err := stepsCoveringAssets(ctx, store, taskID,
			mustSteps(ctx, store, taskID), removedIDs)
		if err != nil {
			return killed, aborted, err
		}
		left := false
		for _, h := range hits2 {
			if (h.status == "dispatched") || (h.status == "running" && !killedSet[h.hit.StepID]) {
				left = true
				break
			}
		}
		if !left {
			break
		}
	}
	if len(killed) > 0 || len(aborted) > 0 {
		runlog.Infof("范围摘除止损: %s 终止在途单位 %d 个,取消排队步骤 %d 个(资产: %s)",
			taskID, len(killed), len(aborted), strings.Join(titles, "、"))
	}
	return killed, aborted, nil
}

// mustSteps 步骤全量重读(收敛复查用;读失败给空清单让复查走重试)。
func mustSteps(ctx context.Context, store *rings.Store, taskID string) []rings.NodeFull {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil
	}
	return steps
}
