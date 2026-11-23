package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// P5-02 机器三查·交卷即查(v1.3.0,第三章 §5 修订):交卷事件同步触发
// 三查——纯代码、毫秒-秒级、不经收割轮;确认与复现包同批落账(构造
// 保证沿用 P4-73:确认 ⟹ 包内至少含证明交换种子);三查不过注入补考
// 反馈(原会话续跑补做,上限 1 次),补考仍不过落证据不足终态。
//
// 本文件承载:
//   - CheckClaimOnSubmit:交卷即查入口(swarm 交卷受控入口经 Executor
//     的 ClaimCheck 钩子同步调用;判定纯函数在 arbiter.ThreeChecks);
//   - retakeStepEvents:补考/重验拉起步骤(队首档,与"已批准待上位"
//     同档,同档按拉起序);
//   - checkPendingClaims:收割兜底(交卷即查失败窗口重查 + 补考未重交/
//     重验未再坐实的终态收口)——收割只做事后入账汇总,不再判定。

// retakeStepKind 补做证据/重验步骤的 kind 标记(单位起飞据此接管原
// 会话续跑;调度序据此进队首档)。
const retakeStepKind = "retake"

// retakeStepEvents 构造补考/重验拉起步骤的事件批(NodeAdded + covers
// 边):步骤绑定声明(detail.claim_id)与原交卷步骤(detail.origin_step,
// 原会话续跑的转录来源),失败原因/重开理由随 detail 注入(单位起飞的
// 续跑指令读取);head_seq 为拉起时的图版本(队首档内按拉起先后,同档
// 不抢)。reverify=true 为重验步骤(reopen 拉起,终态无重交记推翻终态);
// 缺省为补考步骤(三查不过拉起,终态无重交落证据不足)。意图分级与
// 目标归属继承原交卷步骤(covers 边继承声明的资产锚点)。
func retakeStepEvents(ctx context.Context, store *rings.Store, taskID string,
	claim rings.NodeFull, reason string, headSeq int64, reverify bool) ([]rings.Event, error) {
	originStep := ""
	derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		return nil, fmt.Errorf("查血缘: %w", err)
	}
	for _, e := range derived {
		if e.From != claim.ID {
			continue
		}
		if n, nerr := store.Node(ctx, taskID, e.To); nerr == nil && n.Type == rings.NodeStep {
			originStep = e.To
			break
		}
	}
	stepID, err := nextStepID(ctx, store, taskID)
	if err != nil {
		return nil, err
	}
	title := "补做证据: " + clipLine(claim.Title, 60)
	if reverify {
		title = "重验: " + clipLine(claim.Title, 60)
	}
	detail := map[string]any{
		"kind":         retakeStepKind,
		"claim_id":     claim.ID,
		"origin_step":  originStep,
		"reason":       reason,
		"head_seq":     headSeq,
		"action_level": string(rings.ActionReadOnly),
	}
	if reverify {
		detail["reverify"] = true
	}
	// 意图分级/危害声明/目标归属继承原交卷步骤(补做与重验重放同类
	// 动作,授权姿态与原发现一致;原步骤缺失时退只读探测,更强动作由
	// 单位按 RoE/审批门逐次过门)。
	if originStep != "" {
		if n, nerr := store.Node(ctx, taskID, originStep); nerr == nil {
			if al, _ := n.Detail["action_level"].(string); swarm.ActionLevelRank(rings.ActionLevel(al)) > 0 {
				detail["action_level"] = al
			}
			if h, _ := n.Detail["harm_class"].(string); rings.IsValidHarmClass(rings.HarmClass(h)) && h != "" {
				detail["harm_class"] = h
			}
			if g, _ := n.Detail["goal_id"].(string); g != "" {
				detail["goal_id"] = g
			}
		}
	}
	events := []rings.Event{{
		Kind: rings.EventNodeAdded, Actor: ActorOvermind,
		Payload: rings.NodeAdded{NodeID: stepID, NodeType: rings.NodeStep, Title: title, Status: "dispatched", Detail: detail},
	}}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("查资产锚点: %w", err)
	}
	for _, e := range abouts {
		if e.From != claim.ID {
			continue
		}
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: stepID, ToNode: e.To},
		})
	}
	return events, nil
}

// 交卷即查结果直接用 swarm.ClaimCheckResult(钩子契约类型;swarm 侧
// 据此拼交卷工具反馈)。

// claimSubmission 声明当前战役的交卷情报(事件流一次扫出的三查输入)。
type claimSubmission struct {
	submitRev   int64
	rec         *rings.TestClaimRecord // 最新交换单
	submits     int                    // 战役内交换单数(补考上限控制)
	refutedOpen bool                   // 战役内存在推翻终态裁决(未消解)
}

// readClaimSubmission 读声明当前战役的交卷情报。
func readClaimSubmission(evs []rings.RawEvent, claimID string, campaignRev int64) (*claimSubmission, error) {
	out := &claimSubmission{}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || ev.Revision <= campaignRev {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID {
			continue
		}
		switch r.Type {
		case rings.TestClaimRecSubmit:
			out.submits++
			out.rec = &r
			out.submitRev = ev.Revision
		case rings.TestClaimRecVerdict:
			if r.Decision == rings.VerdictRefuted {
				out.refutedOpen = true
			}
		}
	}
	return out, nil
}

// checkClaimFacts 解析三查事实(账本+仓库只读):引用逐条命中(镜交换
// ID 或直连 URL 回查命中可组包工具记录——与种子解析同源,范围=当前
// 战役交卷步骤流量)、proving_marker 在命中引用的响应体原文中出现
// (子串匹配)、冻结标准可解析。
func (l *Loop) checkClaimFacts(ctx context.Context, claim rings.NodeFull, sub *claimSubmission) (arbiter.CheckFacts, error) {
	f := arbiter.CheckFacts{Countercheck: sub.rec.CountercheckNote}
	// 查三:创建事件冻结的判定标准存在且可解析(标准非法=被改/缺失)。
	if raw, _ := claim.Detail["standard"].(map[string]any); raw != nil {
		if _, err := arbiter.ParseClaimStandard(raw); err == nil {
			f.StandardFrozen = true
		}
	}
	// 查一:引用逐条命中 + marker 在命中引用的响应体中。
	hits, err := l.resolveSubmitRefs(ctx, claim.ID, sub.rec)
	if err != nil {
		return f, err
	}
	f.RefsTotal = len(sub.rec.EvidenceRefs)
	marker := sub.rec.ProvingMarker
	for i := range hits {
		if !hits[i].hit {
			continue
		}
		f.RefsHit++
		if !f.MarkerInBody && marker != "" {
			f.MarkerInBody = l.markerInRefBody(&hits[i], marker)
		}
	}
	return f, nil
}

// submitRefHit 单条交卷引用的命中事实(Mirror 与 ToolRec 恰一非空)。
type submitRefHit struct {
	ref    string
	mirror *rings.MirrorRecord
	tool   *rings.SwarmRecord
	hit    bool
}

// resolveSubmitRefs 逐条解析交换单 evidence_refs:镜交换按 ID 命中
// (范围=当前战役交卷步骤流量,与种子解析同源);直连形态引用 URL 字
// 符串(P5-01 注意事项 3:submit 侧只收不拦)按可组包工具记录回查命中
// (P4-62 C(b) 通道)。悬空引用(查不到)如实不命中——查一据此拒。
func (l *Loop) resolveSubmitRefs(ctx context.Context, claimID string, sub *rings.TestClaimRecord) ([]submitRefHit, error) {
	stepIDs, err := claimEvidenceSteps(ctx, l.Store, l.TaskID, claimID)
	if err != nil {
		return nil, err
	}
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 三查读事件流: %w", err)
	}
	byExchange := map[string]*rings.MirrorRecord{}
	var tools []*rings.SwarmRecord
	for i := range evs {
		switch evs[i].Kind {
		case rings.EventMirrorRecord:
			var m rings.MirrorRecord
			if json.Unmarshal(evs[i].Payload, &m) != nil {
				continue
			}
			if stepIDs[m.StepID].ID == "" || (m.Type != rings.MirrorRecHTTP && m.Type != rings.MirrorRecHTTPS) {
				continue
			}
			mm := m
			byExchange[m.ExchangeID] = &mm
		case rings.EventSwarmRecord:
			var r rings.SwarmRecord
			if json.Unmarshal(evs[i].Payload, &r) != nil {
				continue
			}
			if r.Type != rings.SwarmRecToolCall || r.IsError || stepIDs[r.StepID].ID == "" {
				continue
			}
			if r.ToolName != "http_probe" && r.ToolName != "bash" {
				continue
			}
			rr := r
			tools = append(tools, &rr)
		}
	}
	hits := make([]submitRefHit, 0, len(sub.EvidenceRefs))
	for _, ref := range sub.EvidenceRefs {
		h := submitRefHit{ref: ref}
		if m, ok := byExchange[strings.TrimSpace(ref)]; ok {
			h.mirror, h.hit = m, true
		} else if t := toolRecByURL(l.TaskID, tools, strings.TrimSpace(ref)); t != nil {
			h.tool, h.hit = t, true
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// toolRecByURL 直连形态的 URL 回查:引用是 URL 字符串时,命中参数/
// 命令含该 URL 且可确定性组包的工具调用记录(P4-62 C(b) 通道,取最近
// 一条)。
func toolRecByURL(taskID string, tools []*rings.SwarmRecord, ref string) *rings.SwarmRecord {
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		return nil
	}
	for i := len(tools) - 1; i >= 0; i-- {
		if !toolReferencesURL(*tools[i], ref) {
			continue
		}
		if toolProducible(taskID, *tools[i]) {
			return tools[i]
		}
	}
	return nil
}

// markerInRefBody proving_marker 是否在命中引用的响应体原文中出现
// (镜交换=响应体;直连工具记录=结果文本——直连形态的证据原文)。工具
// 结果小输出只落截头(ResultHead,2048 字节)不落 blob:截头即证据可
// 核验面,marker 不在其中=不可核验(fail-closed:证明物核验不因截断
// 放行——关键证明物通常在响应前部,抄尾部长文里的值属异常形态)。
// blob 读不到按不匹配处理。
func (l *Loop) markerInRefBody(h *submitRefHit, marker string) bool {
	if h.mirror != nil {
		var blobs *swarm.BlobStore
		if l.Swarm != nil {
			blobs = l.Swarm.Blobs
		}
		if blobs == nil || h.mirror.BlobRef == "" {
			return false
		}
		doc, err := blobs.Read(h.mirror.BlobRef)
		if err != nil {
			return false
		}
		// 镜交换:响应体子串匹配(证明物是响应原文里的关键值)。
		var probe struct {
			Response *struct {
				Body []byte `json:"body"`
			} `json:"response"`
		}
		if json.Unmarshal(doc, &probe) != nil || probe.Response == nil {
			return false
		}
		return strings.Contains(string(probe.Response.Body), marker)
	}
	if h.tool == nil {
		return false
	}
	if h.tool.ResultRef != "" {
		var blobs *swarm.BlobStore
		if l.Swarm != nil {
			blobs = l.Swarm.Blobs
		}
		if blobs == nil {
			return false
		}
		if doc, err := blobs.Read(h.tool.ResultRef); err == nil {
			return strings.Contains(string(doc), marker)
		}
		return false
	}
	return strings.Contains(h.tool.ResultHead, marker)
}

// claimCampaignStepIDs 当前战役内该声明的交卷步骤集合(submit 记录的
// StepID;旧账本 falsify 步骤并入——存量容错)。
func claimCampaignStepIDs(ctx context.Context, store *rings.Store, taskID, claimID string, campaignRev int64) (map[string]bool, error) {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 三查查步骤: %w", err)
	}
	out := map[string]bool{}
	for _, s := range steps {
		if campaignRev > 0 && s.CreatedRev <= campaignRev {
			continue
		}
		if cid, _ := s.Detail["claim_id"].(string); cid == claimID {
			out[s.ID] = true
			continue
		}
		if cid, _ := s.Detail["testclaim_id"].(string); cid == claimID {
			out[s.ID] = true
		}
	}
	// submit 记录的 StepID(意图步骤,不带 claim 绑定 detail)也并入。
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 三查读事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || ev.Revision <= campaignRev {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID || r.Type != rings.TestClaimRecSubmit {
			continue
		}
		if r.StepID != "" {
			out[r.StepID] = true
		}
	}
	return out, nil
}

// CheckClaimOnSubmit 交卷即查(P5-02②:交卷事件同步触发,毫秒-秒级,
// 不经收割轮):解析账本事实 → arbiter.ThreeChecks 纯函数求值 →
// 确认(复现包定稿同批落账,确认 ⟙ 必有包)/补考反馈(声明留候选,
// 派补考步骤拉起原会话)/证据不足终态(补考上限 1 次用尽)。幂等:
// 非候选态零落账直接返回(并发重入与收割兜底重查的安全网)。
func (l *Loop) CheckClaimOnSubmit(ctx context.Context, taskID, claimID string) (*swarm.ClaimCheckResult, error) {
	for attempt := 0; attempt < 12; attempt++ {
		out, err := l.checkClaimSubmission(ctx, taskID, claimID)
		if errors.Is(err, rings.ErrClaimCheckApplied) {
			return l.recordedClaimCheck(ctx, taskID, claimID)
		}
		if !errors.Is(err, rings.ErrStaleRevision) {
			return out, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, rings.ErrStaleRevision
}

func (l *Loop) recordedClaimCheck(ctx context.Context, task, id string) (*swarm.ClaimCheckResult, error) {
	n, err := l.Store.Node(ctx, task, id)
	if err != nil {
		return nil, err
	}
	row, ok, err := l.Store.ClaimCaseOf(ctx, task, id)
	if err != nil {
		return nil, err
	}
	out := &swarm.ClaimCheckResult{Status: n.Status}
	if ok {
		out.Reason = row.PendingReason
	}
	return out, nil
}

func (l *Loop) checkClaimSubmission(ctx context.Context, taskID, claimID string) (*swarm.ClaimCheckResult, error) {
	if taskID != l.TaskID {
		return nil, fmt.Errorf("overmind: 交卷即查任务不符(%s != %s)", taskID, l.TaskID)
	}
	claim, err := l.Store.Node(ctx, taskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("overmind: 交卷即查查判断 %s: %w", claimID, err)
	}
	if claim.Type != rings.NodeTestClaim {
		return nil, fmt.Errorf("overmind: 节点 %s 是 %s 不是 testclaim", claimID, claim.Type)
	}
	if claim.Status != rings.TestClaimCandidate {
		return l.recordedClaimCheck(ctx, taskID, claimID)
	}
	evs, err := l.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 交卷即查读事件流: %w", err)
	}
	campaignRev, err := claimCampaignRev(ctx, l.Store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	sub, err := readClaimSubmission(evs, claimID, campaignRev)
	if err != nil {
		return nil, err
	}
	if sub.rec == nil {
		return nil, fmt.Errorf("overmind: 判断 %s 战役内无交换单", claimID)
	}
	if l.verdictAfter(evs, claimID, sub.rec) {
		return l.recordedClaimCheck(ctx, taskID, claimID)
	}
	version := rings.ClaimSubmissionVersion{ClaimID: claimID, SubmitRev: sub.submitRev}

	facts, err := l.checkClaimFacts(ctx, *claim, sub)
	if err != nil {
		return nil, err
	}
	check := arbiter.ThreeChecks(facts)
	out := &swarm.ClaimCheckResult{Status: check.Status, Reason: check.Verdict.PendingReason}
	rev, err := l.Store.CurrentRevision(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("overmind: 交卷即查查图版本: %w", err)
	}
	check.Verdict.ClaimID, check.Verdict.EvaluatedRev = claimID, rev

	switch check.Status {
	case rings.TestClaimConfirmed:
		// 确认 ⟹ 复现包同批落账(P4-73 构造性保证):种子(交卷引用,
		// 不经候选池)→装配→模型挑选(失败仍出种子包+如实标注)→
		// 叙述/摘要(失败回退拼句)→verdict+repro+状态迁移一批事务。
		var blobs *swarm.BlobStore
		if l.Swarm != nil {
			blobs = l.Swarm.Blobs
		}
		seeds, serr := ResolveEvidenceSeeds(ctx, l.Store, blobs, taskID, claimID)
		if serr != nil {
			return nil, fmt.Errorf("overmind: 确认判断 %s 证据种子: %w", claimID, serr)
		}
		if len(seeds) == 0 {
			// 构造性兜底:三查过而种子解析空(解析口径与三查同源,理论
			// 不可达)——如实改判证据不足,不出无包确认。
			check.Status = rings.TestClaimInsufficient
			check.Verdict.Decision = rings.VerdictInsufficient
			check.Verdict.Satisfied = false
			check.Verdict.PendingReason = "证据不可追溯(种子解析为空)"
			out.Status, out.Reason = check.Status, check.Verdict.PendingReason
			_, err := l.Store.AppendForClaimSubmission(ctx, taskID, version, nil,
				rings.Event{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: check.Verdict},
				rings.Event{Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter, Payload: rings.NodeStateChanged{NodeID: claimID, Status: check.Status}})
			return out, err
		}
		// repro 的级别词表四级(critical|high|medium|low):交换单自报
		// none(信息性不立)映射为空——定稿未定级,如实呈现不造数据。
		severity := sub.rec.Severity
		if severity == "none" {
			severity = ""
		}
		repro, _, seedSeqs, rerr := AssembleRepro(ctx, l.Store, blobs, rings.DefaultScopeResolver, taskID, claimID, severity, seeds)
		if rerr != nil {
			return nil, fmt.Errorf("overmind: 定稿判断 %s: %w", claimID, rerr)
		}
		repro.ProofSeqs = append([]int(nil), seedSeqs...)
		if l.Provider == nil {
			l.pickReproSteps(ctx, claimID, repro, ReproPoolDiag{}, seedSeqs)
			l.genClaimNarrative(ctx, claimID, check.Verdict.Conditions, repro)
		} else {
			repro.StepsIncomplete = "流程步骤整理中"
			if pack, err := l.narrativePack(ctx, claimID, check.Verdict.Conditions); err == nil {
				repro.Narrative = deterministicNarrative(pack)
			}
		}
		events := []rings.Event{
			{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: check.Verdict},
			{Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: *repro},
		}
		if done, derr := l.cancelQueuedRetakes(ctx, claimID); derr == nil && len(done) > 0 {
			// 补考/重验步骤已无意义(会话内原地重交已确认),排队步骤收场。
			events = append(events, done...)
		}
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
			Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimConfirmed}})
		written, err := l.Store.AppendForClaimSubmission(ctx, taskID, version, nil, events...)
		if err != nil {
			return nil, fmt.Errorf("overmind: 确认判断 %s 落账: %w", claimID, err)
		}
		l.polishClaimPackageAsync(ctx, claimID, written[len(written)-1], *repro, check.Verdict.Conditions, seedSeqs)
		runlog.Infof("交卷即查:判断 %s 三查全过,已确认(复现包 %d 步)", claimID, len(repro.Steps))
		return out, nil
	case rings.TestClaimCandidate:
		// 三查不过:首败派补考(反馈注入原会话,上限 1 次);补考再败
		// 落证据不足终态。
		if sub.submits >= 2 {
			check.Verdict.Decision = rings.VerdictInsufficient
			out.Status, out.Reason = rings.TestClaimInsufficient, check.Verdict.PendingReason
			events := []rings.Event{
				{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: check.Verdict},
			}
			if done, derr := l.cancelQueuedRetakes(ctx, claimID); derr == nil && len(done) > 0 {
				events = append(events, done...)
			}
			events = append(events, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
				Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimInsufficient}})
			if _, err := l.Store.AppendForClaimSubmission(ctx, taskID, version, nil, events...); err != nil {
				return nil, fmt.Errorf("overmind: 判断 %s 证据不足落账: %w", claimID, err)
			}
			l.polishClaimTextAsync(ctx, claimID)
			runlog.Infof("交卷即查:判断 %s 补考仍未过,落证据不足(%s)", claimID, out.Reason)
			return out, nil
		}
		// 首败:verdict(retake) + 补考步骤(已有未终态补考步骤不重复派)。
		events := []rings.Event{
			{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: check.Verdict},
		}
		if !l.hasOpenRetakeStep(evs, claimID, campaignRev) {
			stepEvents, serr := retakeStepEvents(ctx, l.Store, taskID, *claim, check.Verdict.PendingReason, rev, false)
			if serr != nil {
				return nil, fmt.Errorf("overmind: 判断 %s 派补考步骤: %w", claimID, serr)
			}
			events = append(events, stepEvents...)
		}
		if _, err := l.Store.AppendForClaimSubmission(ctx, taskID, version, &rev, events...); err != nil {
			return nil, fmt.Errorf("overmind: 判断 %s 补考落账: %w", claimID, err)
		}
		l.polishClaimTextAsync(ctx, claimID)
		runlog.Infof("交卷即查:判断 %s 三查未过,派补考(%s)", claimID, out.Reason)
		return out, nil
	}
	return out, nil
}

// hasOpenRetakeStep 声明在当前战役是否已有未终态的补考/重验步骤
// (防重复派发)。
func (l *Loop) hasOpenRetakeStep(evs []rings.RawEvent, claimID string, campaignRev int64) bool {
	terminal := map[string]bool{}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) == nil {
			terminal[p.NodeID] = p.Status != "dispatched"
		}
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded {
			continue
		}
		var p rings.NodeAdded
		if json.Unmarshal(ev.Payload, &p) != nil || p.NodeType != rings.NodeStep {
			continue
		}
		if k, _ := p.Detail["kind"].(string); k != retakeStepKind {
			continue
		}
		if cid, _ := p.Detail["claim_id"].(string); cid != claimID {
			continue
		}
		if campaignRev > 0 && ev.Revision <= campaignRev {
			continue
		}
		if !terminal[p.NodeID] {
			return true
		}
	}
	return false
}

// cancelQueuedRetakes 收场声明的排队补考/重验步骤(确认/终态后已无
// 意义;在跑单位不杀——重交会被受控入口拒收,单位如实收尾)。
func (l *Loop) cancelQueuedRetakes(ctx context.Context, claimID string) ([]rings.Event, error) {
	steps, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeStep, "dispatched")
	if err != nil {
		return nil, err
	}
	var out []rings.Event
	for _, s := range steps {
		if k, _ := s.Detail["kind"].(string); k != retakeStepKind {
			continue
		}
		if cid, _ := s.Detail["claim_id"].(string); cid != claimID {
			continue
		}
		out = append(out, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
			Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "cancelled", Note: "交卷已出结论,补做步骤收场"},
		})
	}
	return out, nil
}

// checkPendingClaims 收割兜底(P5-02②:收割只做事后入账汇总,不再
// 判定)。三类收口:
//   - 候选声明"最新交换单后无裁决"(交卷即查失败/进程崩溃窗口)→
//     重跑三查(CheckClaimOnSubmit 同入口);
//   - 补考步骤终态而战役内无第二次交换单(补考机会已给未用)→
//     证据不足终态("补考未重交");
//   - 重验步骤终态而战役内无新交换单(reopen 重验未再坐实)→
//     推翻终态(账本留痕,显示层移出漏洞页)。
//
// 存量旧账本的 testing/awaiting_round/pending 声明不再处理(停原状态,
// 升级删除路线 P5-03④)。返回本轮落账数(静默判定依据)。
func (l *Loop) checkPendingClaims(ctx context.Context) (int, error) {
	claims, err := l.Store.NodesByType(ctx, l.TaskID, rings.NodeTestClaim, rings.TestClaimCandidate)
	if err != nil {
		return 0, fmt.Errorf("overmind: 兜底查待验交卷单: %w", err)
	}
	judged := 0
	for _, c := range claims {
		evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
		if err != nil {
			return judged, fmt.Errorf("overmind: 兜底读事件流: %w", err)
		}
		campaignRev, err := claimCampaignRev(ctx, l.Store, l.TaskID, c.ID)
		if err != nil {
			return judged, err
		}
		sub, err := readClaimSubmission(evs, c.ID, campaignRev)
		if err != nil {
			return judged, err
		}
		if sub.rec == nil {
			// 战役内无交换单:reopen 后重验未重交形态(retake 步骤终态
			// 收口);无 retake 步骤的异常形态跳过(终局清点兜底)。
			if retake, reverify, retakeDone := l.latestRetakeStep(evs, c.ID, campaignRev); retake != "" && retakeDone {
				if err := l.closeoutRetakeClaim(ctx, c, reverify, evs); err != nil {
					return judged, err
				}
				judged++
			}
			continue
		}
		// 最新交换单之后已有裁决 → 已查过,进入补考/重验收口判定。
		if !l.verdictAfter(evs, c.ID, sub.rec) {
			// 交卷即查窗口遗漏:重查(幂等)。
			if _, err := l.CheckClaimOnSubmit(ctx, l.TaskID, c.ID); err != nil {
				l.setErr(fmt.Errorf("overmind: 兜底重查判断 %s: %w", c.ID, err))
				continue
			}
			judged++
			continue
		}
		// 补考/重验步骤终态后的收口:终态时点之后无第二次交换单。
		retake, reverify, retakeDone := l.latestRetakeStep(evs, c.ID, campaignRev)
		if retake == "" || !retakeDone {
			continue
		}
		if sub.submits >= 2 || (sub.submits >= 1 && l.submitAfter(evs, c.ID, retake)) {
			continue // 已重交,三查自会判定
		}
		if err := l.closeoutRetakeClaim(ctx, c, reverify, evs); err != nil {
			return judged, err
		}
		judged++
	}
	return judged, nil
}

// closeoutRetakeClaim 补考/重验步骤终态而无重交的收口落账:补考 →
// 证据不足终态;重验 → 推翻终态(账本留痕,显示层移出漏洞页)。
func (l *Loop) closeoutRetakeClaim(ctx context.Context, c rings.NodeFull, reverify bool, inspected []rings.RawEvent) error {
	reason := "补考未重交"
	status := rings.TestClaimInsufficient
	decision := rings.VerdictInsufficient
	if reverify {
		reason = "重验未再坐实,原确认撤销"
		status = rings.TestClaimRefuted
		decision = rings.VerdictRefuted
	}
	rev, err := l.Store.CurrentRevision(ctx, l.TaskID)
	if err != nil {
		return fmt.Errorf("overmind: 兜底查图版本: %w", err)
	}
	conds := []rings.ArbiterConditionResult{{
		Kind: arbiter.CondCountercheckDone, Required: 1, Actual: 0, Satisfied: false}}
	events := []rings.Event{
		{Kind: rings.EventTestClaimRecord, Actor: arbiter.ActorArbiter, Payload: rings.TestClaimRecord{
			ClaimID: c.ID, Type: rings.TestClaimRecVerdict, Decision: decision,
			EvaluatedRev: rev, PendingReason: reason, Conditions: conds}},
		{Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: status}},
	}
	version := rings.ClaimSubmissionVersion{ClaimID: c.ID}
	for _, ev := range inspected {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var record rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &record) == nil && record.ClaimID == c.ID && (record.Type == rings.TestClaimRecSubmit || record.Type == rings.TestClaimRecVerdict || record.Type == rings.TestClaimRecReopen) {
			version.SubmitRev, version.RecordType = ev.Revision, record.Type
		}
	}
	if _, err := l.Store.AppendForClaimSubmission(ctx, l.TaskID, version, nil, events...); err != nil {
		if errors.Is(err, rings.ErrClaimCheckApplied) || errors.Is(err, rings.ErrStaleRevision) {
			return nil
		}
		return fmt.Errorf("overmind: 判断 %s 兜底收口落账: %w", c.ID, err)
	}
	runlog.Infof("机器核验兜底:交卷单 %s %s → %s", c.ID, reason, status)
	return nil
}

// verdictAfter 事件流中该声明在 revSub(交换单事件序)之后是否已有
// 裁决记录。
func (l *Loop) verdictAfter(evs []rings.RawEvent, claimID string, sub *rings.TestClaimRecord) bool {
	// 重新定位交换单事件序:比对负载(同 claim 的最新 submit)。
	subRev := int64(0)
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID || r.Type != rings.TestClaimRecSubmit {
			continue
		}
		subRev = ev.Revision
	}
	if subRev == 0 {
		return false
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || ev.Revision <= subRev {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID {
			continue
		}
		if r.Type == rings.TestClaimRecVerdict {
			return true
		}
	}
	return false
}

// submitAfter 声明在指定事件序之后是否有交换单(补考重交判定)。
func (l *Loop) submitAfter(evs []rings.RawEvent, claimID string, afterStep string) bool {
	afterRev := int64(0)
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded {
			continue
		}
		var p rings.NodeAdded
		if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == afterStep {
			afterRev = ev.Revision
		}
	}
	if afterRev == 0 {
		return false
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || ev.Revision <= afterRev {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID {
			continue
		}
		if r.Type == rings.TestClaimRecSubmit {
			return true
		}
	}
	return false
}

// latestRetakeStep 声明当前战役最新的补考/重验步骤与其终态情况
// (retakeDone=步骤已到终态)。
func (l *Loop) latestRetakeStep(evs []rings.RawEvent, claimID string, campaignRev int64) (stepID string, reverify, done bool) {
	var latestRev int64
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded {
			continue
		}
		var p rings.NodeAdded
		if json.Unmarshal(ev.Payload, &p) != nil || p.NodeType != rings.NodeStep {
			continue
		}
		if k, _ := p.Detail["kind"].(string); k != retakeStepKind {
			continue
		}
		if cid, _ := p.Detail["claim_id"].(string); cid != claimID {
			continue
		}
		if campaignRev > 0 && ev.Revision <= campaignRev {
			continue
		}
		if ev.Revision >= latestRev {
			latestRev = ev.Revision
			stepID = p.NodeID
			reverify, _ = p.Detail["reverify"].(bool)
		}
	}
	if stepID == "" {
		return "", false, false
	}
	status := "dispatched"
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) != nil || p.NodeID != stepID {
			continue
		}
		status = p.Status
	}
	done = status != "dispatched" && status != "running"
	return stepID, reverify, done
}
