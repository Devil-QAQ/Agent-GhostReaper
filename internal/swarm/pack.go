package swarm

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/vault"
)

// Claim 领取任务包:从年轮账本构建七要素并绑定当前图版本。
// 纯读操作——不改变图、不占坑(一 Step 一单位的派发调度归主宰/P1-05),
// 领取动作本身由调用方记账。绑定的 GraphVersion 在执行前复检(R14)。
// P2-06c:领取时应用全局预算覆盖(设置面板可写,键值表存储)——生效
// 语义即"对新领取的单位即时生效":在飞单位早已把领取时的 Budget 冻结
// 进任务包,不受后续覆盖影响(与并发调整同纪律,不硬中断)。
func (e *Executor) Claim(ctx context.Context, taskID, stepID string) (*TaskPack, error) {
	step, err := e.Store.Node(ctx, taskID, stepID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取步骤 %s: %w", stepID, err)
	}
	if step.Type != rings.NodeStep {
		return nil, fmt.Errorf("swarm: 节点 %s 是 %s 不是 step", stepID, step.Type)
	}

	pack := &TaskPack{
		TaskID: taskID,
		StepID: stepID,
		Budget: e.Budget,
	}
	// 全局预算覆盖(P2-06c):装配默认 + 设置面板写入的全局值,只覆盖
	// max_turns / run_seconds 两项(wrapup 维持只读默认)。读失败或值
	// 非法退装配默认(fail-safe:配置不可用不挡领取)。
	applyBudgetOverrides(ctx, e.Store, &pack.Budget)

	// 任务级总闸(P2-04,第四章 §6):领取时从事件流锚点推导收尾线
	//——单位墙钟预算 = min(运行预算, 距收尾线剩余);零值=不限。
	secs, createdAt, terr := e.Store.TaskGate(ctx, taskID)
	if terr != nil {
		return nil, fmt.Errorf("swarm: 领取: 查任务总闸: %w", terr)
	}
	pack.TaskDeadline = taskDeadlineOf(secs, createdAt)

	// 循环内重锚定间隔(P2-05,第八章第 8 节防线③):同样钉在事件流
	// created 锚点上(不受后续生命周期事件覆盖投影 detail 的影响),
	// 面板建任务表单可调;任务未显式设定时取全局默认(P2-08d Agent 栏
	// 可写),读失败/非法值保持零值由门卫归一到代码默认(fail-safe)。
	createdDetail, cerr := e.Store.CreatedDetail(ctx, taskID)
	if cerr != nil {
		return nil, fmt.Errorf("swarm: 领取: 查任务创建选项: %w", cerr)
	}
	if n, _ := createdDetail["anchor_every_steps"].(float64); n > 0 {
		pack.AnchorEverySteps = int(n)
	} else if n, ok := settingPositiveInt(ctx, e.Store, SettingKeyReanchorSteps); ok {
		pack.AnchorEverySteps = n
	}

	// 任务类型与范围闭包(P4-44):类型从 created 定格(建后不可改);
	// 渗透任务冻结种子闭包(权限管线命令级硬拦的判定面)与待批清单
	// (RoE 约束块渲染)。红队/未标任务两 nil——现状行为,不拦。
	pack.TaskType = rings.TaskTypeOf(createdDetail)
	if pack.TaskType == rings.TaskTypePentest {
		if roe, err := e.Store.CurrentRoE(ctx, taskID); err != nil {
			return nil, fmt.Errorf("swarm: 领取: 查 RoE 范围: %w", err)
		} else {
			approved := roe.ScopeApprovedSet()
			if approved == nil {
				approved = map[string]bool{}
			}
			// 摘除即去权(P4-54 修复):排除清单内的资产(含手动/种子)
			// 不贡献闭包——摘除后新领的任务包不再含其主机,命令级范围
			// 检查不会被"闭包内"短路。
			excluded := roe.ExcludedAssetSet()
			if assets, err := e.Store.NodesByType(ctx, taskID, rings.NodeAsset, ""); err != nil {
				return nil, fmt.Errorf("swarm: 领取: 查资产(范围闭包): %w", err)
			} else {
				pack.ScopeClosure = rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), approved, excluded)
			}
		}
		if pending, err := e.Store.PendingScopeAssets(ctx, taskID); err == nil {
			pack.PendingScope = pending
		}
	}

	// ① 目标:step.detail.goal_id 指定,缺省取图上最新目标。
	goalID, _ := step.Detail["goal_id"].(string)
	if goalID == "" {
		goals, err := e.Store.NodesByType(ctx, taskID, rings.NodeGoal, "")
		if err != nil {
			return nil, fmt.Errorf("swarm: 领取: 查目标: %w", err)
		}
		if len(goals) > 0 {
			goalID = goals[len(goals)-1].ID
		}
	}
	if goalID != "" {
		if g, err := e.Store.Node(ctx, taskID, goalID); err == nil && g.Type == rings.NodeGoal {
			pack.GoalID, pack.GoalTitle = g.ID, g.Title
		}
	}

	// ② 意图与预期观察 + 意图分级 + 危害声明(第四章 §3:分级与危害
	// 声明随任务包交给虫群,审批门据此定处置)。
	pack.StepIntent = step.Title
	if eo, ok := step.Detail["expected_observation"].(string); ok {
		pack.ExpectedObservation = eo
	}
	if al, ok := step.Detail["action_level"].(string); ok {
		pack.ActionLevel = rings.ActionLevel(al)
	}
	if hc, ok := step.Detail["harm_class"].(string); ok {
		pack.HarmClass = rings.HarmClass(hc)
	}

	// ④ 资产:本步骤 covers 的资产。
	covers, err := e.Store.Edges(ctx, taskID, rings.EdgeCovers)
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查 covers 边: %w", err)
	}
	var coveredAssets []string
	for _, e := range covers {
		if e.From == stepID {
			coveredAssets = append(coveredAssets, e.To)
		}
	}
	for _, aid := range coveredAssets {
		if a, err := e.Store.Node(ctx, taskID, aid); err == nil && a.Type == rings.NodeAsset {
			class := string(a.AssetClass)
			pack.Assets = append(pack.Assets, AssetBrief{ID: a.ID, Title: a.Title, Class: class})
		}
	}

	// ③ 相关可信事实:挂在 covered 资产上的 confirmed 事实(about 边);
	// 步骤未绑定资产时退化为全部 confirmed 事实(P1 任务规模小,可接受)。
	facts, err := e.Store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查可信事实: %w", err)
	}
	abouts, err := e.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查 about 边: %w", err)
	}
	factAbout := map[string][]string{} // factID -> assets
	for _, e := range abouts {
		factAbout[e.From] = append(factAbout[e.From], e.To)
	}
	covered := map[string]bool{}
	for _, aid := range coveredAssets {
		covered[aid] = true
	}
	for _, f := range facts {
		relevant := len(coveredAssets) == 0
		for _, a := range factAbout[f.ID] {
			if covered[a] {
				relevant = true
			}
		}
		if !relevant {
			continue
		}
		brief := FactBrief{ID: f.ID, Title: f.Title}
		if ev, ok := f.Detail["evidence_head"].(string); ok {
			brief.Evidence = ev
		}
		if ref, ok := f.Detail["evidence_ref"].(string); ok {
			brief.Evidence += " [" + ref + "]"
		}
		pack.Facts = append(pack.Facts, brief)
	}

	// ⑤ 前序尝试:同目标下的其他步骤(排除自己)。
	steps, err := e.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查前序步骤: %w", err)
	}
	for _, s := range steps {
		if s.ID == stepID {
			continue
		}
		sameGoal := goalID != ""
		if gid, ok := s.Detail["goal_id"].(string); ok && gid != goalID {
			sameGoal = false
		}
		if !sameGoal {
			continue
		}
		att := AttemptBrief{StepID: s.ID, Intent: s.Title, Status: s.Status}
		if r, ok := s.Detail["fail_reason"].(string); ok {
			att.Reason = r
		}
		pack.PriorAttempts = append(pack.PriorAttempts, att)
	}

	// ⑥ 授权边界(RoE 当前值:工具清单 + 审批门槛 + 任务级拦截规则 +
	// 注意事项)。注意事项(P4-49)随授权冻结进任务包——约束块行/授权节/
	// 重锚定重申三处同读这份副本;面板改注意事项对下一批领取生效(与
	// 操作约束同款次序,不硬中断在飞单位)。
	roe, err := e.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查 RoE: %w", err)
	}
	pack.Authorization = RoE{
		AllowedTools:      roe.AllowedTools,
		DeniedTools:       roe.DeniedTools,
		ApprovalThreshold: roe.ApprovalThreshold,
		IntentRules:       roe.IntentRules,
		CreatorNotes:      roe.CreatorNotes,
	}

	// ⑦ 图版本。
	pack.GraphVersion, err = e.Store.CurrentRevision(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("swarm: 领取: 查图版本: %w", err)
	}

	// 可用目标凭据(P3-09):全局凭据库的名称/用户名/调用环境变量名
	//(无秘密)。读失败退空清单(fail-safe:凭据不可读不挡领取,与预算
	// 覆盖读失败同纪律)。
	if creds, verr := vault.Briefs(ctx, e.Store); verr == nil {
		pack.Credentials = creds
	}

	// 反证任务上下文(P1-08 + P2-02):本 Step 绑定了声明(detail.testclaim_id)
	// 即为验证类意图,把声明冻结要素交给反证单位(R12:领到者必然是
	// 与发现者不同的新单位,一 Step 一单位;入口另有同单位拒收兜底)。
	// P2-02:轮次与本轮待检的存活竞争解释从 step.detail 读取(收割派发
	// 时从账本算出冻结;缺省退化为全量冻结清单,兼容 P1-08 旧步骤)。
	if tcID, _ := step.Detail["testclaim_id"].(string); tcID != "" {
		claim, err := e.Store.Node(ctx, taskID, tcID)
		if err != nil {
			return nil, fmt.Errorf("swarm: 领取反证判断 %s: %w", tcID, err)
		}
		if claim.Type != rings.NodeTestClaim {
			return nil, fmt.Errorf("swarm: 反证绑定节点 %s 是 %s 不是 testclaim", tcID, claim.Type)
		}
		brief := &FalsifyBrief{ClaimID: claim.ID, Statement: claim.Title}
		brief.Impact, _ = claim.Detail["impact"].(string)
		brief.Vulnclass, _ = claim.Detail["vulnclass"].(string)
		brief.DiscovererUnit, _ = claim.Detail["unit_id"].(string)
		if ev, ok := claim.Detail["evidence_head"].(string); ok {
			brief.Evidence = ev
		}
		if ref, ok := claim.Detail["evidence_ref"].(string); ok {
			brief.Evidence += " [" + ref + "]"
		}
		if r, ok := step.Detail["round"].(float64); ok {
			brief.Round = int(r)
		}
		brief.Alternatives = altBriefsOf(step.Detail["alternatives"])
		if brief.Alternatives == nil {
			brief.Alternatives = altBriefsOf(claim.Detail["alternatives"])
		}
		// 攻击链环节(P2-06 组合发现):链式声明的血缘链条进任务包,
		// 反证单位据此独立重放整条链的组合效果。
		if combined, _ := claim.Detail["combined"].(bool); combined {
			brief.Chain = chainBriefsOf(ctx, e.Store, taskID, claim.Detail["chain"])
		}
		pack.Falsify = brief
	}
	// 假设验证任务上下文(P2-03,第四章 §5):本 Step 为主宰收割评估
	// "值得跟"派发的验证意图(detail.kind=verify)时,把假设冻结要素交给
	// 验证单位(R12:领到者必然是与提出者不同的新单位,一 Step 一单位;
	// 写回入口另有同单位拒收兜底)。
	if kind, _ := step.Detail["kind"].(string); kind == "verify" {
		hypID, _ := step.Detail["hypothesis_id"].(string)
		if hypID == "" {
			return nil, fmt.Errorf("swarm: 领取验证步骤 %s: 缺 hypothesis_id 绑定", stepID)
		}
		hyp, err := e.Store.Node(ctx, taskID, hypID)
		if err != nil {
			return nil, fmt.Errorf("swarm: 领取验证假设 %s: %w", hypID, err)
		}
		if hyp.Type != rings.NodeHypothesis {
			return nil, fmt.Errorf("swarm: 验证绑定节点 %s 是 %s 不是 hypothesis", hypID, hyp.Type)
		}
		brief := &HypothesisBrief{HypothesisID: hyp.ID, Statement: hyp.Title}
		brief.Basis, _ = hyp.Detail["basis"].(string)
		brief.ProposerUnit, _ = hyp.Detail["unit_id"].(string)
		pack.Verify = brief
	}
	return pack, nil
}

// altBriefsOf detail 里的解释清单(派发冻结的存活清单或声明全量)转
// 任务包条目;格式不符或为空返回 nil。
func altBriefsOf(raw any) []AltBrief {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []AltBrief
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		label, _ := m["label"].(string)
		if id == "" {
			continue
		}
		out = append(out, AltBrief{ID: id, Label: label})
	}
	return out
}

// ---- 全局预算覆盖(P2-06c) ----

// 全局预算配置键(rings v6 通用配置键值表;键空间为点分命名空间,
// 后续设置项直接加新键、per-model 覆盖预留 model.<name>.budget.* 形态)。
const (
	SettingKeyMaxTurns   = "swarm.budget.max_turns"
	SettingKeyRunSeconds = "swarm.budget.run_seconds"

	// SettingKeyReanchorSteps 重锚定默认间隔的全局键(P2-08d,Agent 栏
	// 可写):任务创建未显式设定间隔时,领取按此值注入任务锚。
	SettingKeyReanchorSteps = "swarm.reanchor.every_steps"
)

// applyBudgetOverrides 把全局配置的预算覆盖应用到任务包预算上(只覆盖
// max_turns / run_seconds)。fail-safe 语义:键不存在、读失败、值非正
// 整数——一律保持装配默认,不报错不挡领取;配置问题另由设置面板读路径
// 呈现。独立函数便于单测直证读失败退默认。
func applyBudgetOverrides(ctx context.Context, store *rings.Store, b *Budget) {
	if n, ok := settingPositiveInt(ctx, store, SettingKeyMaxTurns); ok {
		b.MaxTurns = n
	}
	if n, ok := settingPositiveInt(ctx, store, SettingKeyRunSeconds); ok {
		b.RunSeconds = n
	}
}

// settingPositiveInt 读一条应为正整数的配置;读失败/缺省/非法值返回
// ok=false(fail-safe 退默认)。
func settingPositiveInt(ctx context.Context, store *rings.Store, key string) (int, bool) {
	e, ok, err := store.Setting(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(e.Value))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// chainBriefsOf detail 里的攻击链环节转任务包条目(P2-06 组合发现):
// 每环节补事实陈述标题(环节引用的 fact 节点读侧下钻;查不到的环节
// 如实带空标题,不猜)。
func chainBriefsOf(ctx context.Context, store *rings.Store, taskID string, raw any) []ChainBrief {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ChainBrief
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["fact_id"].(string)
		if id == "" {
			continue
		}
		b := ChainBrief{FactID: id}
		if role, _ := m["role"].(string); role != "" {
			b.Role = role
		}
		if f, err := store.Node(ctx, taskID, id); err == nil && f.Type == rings.NodeFact {
			b.Title = f.Title
		}
		out = append(out, b)
	}
	return out
}
