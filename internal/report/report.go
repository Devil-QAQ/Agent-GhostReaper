package report

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 任务级 Markdown 完整报告(P1-10 起版,P2-13 补完整反证链与复现步骤;
// 第五章定稿 v1):报告 = 确定性组装,零 agent、零 LLM(R17)——发现
// 内容在收割/裁决那一刻就已带证据落库,此处只把年轮里的数据换个格式
// 摆出来。确定性口径:一切时刻取自账本事件(occurred_at),不取导出
// 墙钟——相同账本状态两次导出字节级一致;无中生有的字段按数据现实
// 诚实省略,不造占位假数据。
//
// 章节:任务概要 → 聚合摘要(含级别分布)→ 资产覆盖统计 → Confirmed
// 列表 → 证据不足清单(含理由,R4;P4-56 改词)→ 年轮审计摘要。每条发现含完整
// 反证时间线与收割定稿的复现步骤(操作方法 + Burp 可验证原文),与
// 面板发现详情同源同一装配(claim.go,面板所见即报告所印)。

// taskStatusWords 任务状态 → 报告文案(P4-25 审核迭代:状态词 11→7,
// 与面板徽标同口径——created 并入排队中、paused/checkpointed 并入
// 冬眠中、stopped 统称已停止;已停止的结束方式括注,见 Render)。
var taskStatusWords = map[string]string{
	"created":      "排队中",
	"queued":       "排队中",
	"started":      "运行中",
	"settling":     "收尾中",
	"paused":       "冬眠中",
	"hibernated":   "冬眠中",
	"checkpointed": "冬眠中",
	"stopped":      "已停止",
	"completed":    "已完成",
}

// assetClassOrder 资产覆盖统计的固定行序(12 类,与 rings 词汇表同源)。
var assetClassOrder = []rings.AssetClass{
	rings.AssetDomain, rings.AssetSubdomain, rings.AssetHost, rings.AssetService,
	rings.AssetAPI, rings.AssetOrganization, rings.AssetMiniProgram, rings.AssetApp,
	rings.AssetWechatAccount, rings.AssetPerson, rings.AssetLeakInfo, rings.AssetOther,
}

// goalStatusWords 目标状态 → 报告文案(与面板词汇一致,裸状态词不上报告)。
var goalStatusWords = map[string]string{
	"active":   "进行中",
	"achieved": "已达成",
}

// stepStatusWordsMap 步骤状态 → 报告文案(裸状态词不上报告;exhausted
// 按术语对照表译"预算耗尽";blocked=受阻,P4-27 ② 与面板同口径;
// dispatched=排队中,P4-46 ② 与面板/状态总表同口径)。
var stepStatusWordsMap = map[string]string{
	"done": "已完成", "exhausted": "预算耗尽", "failed": "失败",
	"cancelled": "已取消", "aborted": "已中止", "running": "执行中", "dispatched": "排队中",
	"blocked":         "受阻",
	"approval_denied": "审批拒绝终止",
}

// severityOrder 级别固定序(报告与 csv 行序同源)。
var severityOrder = []string{"critical", "high", "medium", "low"}

// Render 从年轮账本组装任务级 Markdown 报告。只读账本(投影 + 事件流 +
// blob 原文回放),零 LLM、零外部输入;任务不存在返回错误。scope 为
// 发现范围(声明 ID 清单,nil/空 = 全部——格式矩阵"当前筛选/勾选项"
// 的服务端落点;筛选时任务概要注明范围,聚合计数仍按全任务如实)。
func Render(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, scope []string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("report: 任务 ID 为空")
	}
	task, err := findTask(ctx, store, taskID)
	if err != nil {
		return "", err
	}

	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return "", fmt.Errorf("report: 查事件流: %w", err)
	}
	ag, err := store.AssetGraph(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("report: 查资产图: %w", err)
	}
	goals, err := store.NodesByType(ctx, taskID, rings.NodeGoal, "")
	if err != nil {
		return "", fmt.Errorf("report: 查目标: %w", err)
	}
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return "", fmt.Errorf("report: 查步骤: %w", err)
	}
	facts, err := store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		return "", fmt.Errorf("report: 查事实: %w", err)
	}
	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
	if err != nil {
		return "", fmt.Errorf("report: 查发现: %w", err)
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return "", fmt.Errorf("report: 查资产锚点边: %w", err)
	}
	roe, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("report: 查 RoE: %w", err)
	}
	cases, err := claimCases(ctx, store, blobs, taskID)
	if err != nil {
		return "", err
	}
	inScope := func(id string) bool {
		if len(scope) == 0 {
			return true
		}
		for _, s := range scope {
			if s == id {
				return true
			}
		}
		return false
	}

	// 审计流扫描一遍:事件构成、生命周期轨迹(含起止时刻)、审批统计。
	kindCount := map[rings.EventKind]int{}
	var lifecycle []rings.RawEvent
	approval := map[string]int{}
	var lastAt time.Time
	for _, ev := range evs {
		kindCount[ev.Kind]++
		if ev.OccurredAt.After(lastAt) {
			lastAt = ev.OccurredAt
		}
		switch ev.Kind {
		case rings.EventTaskLifecycle:
			lifecycle = append(lifecycle, ev)
		case rings.EventApprovalRecord:
			var r rings.ApprovalRecord
			if err := json.Unmarshal(ev.Payload, &r); err == nil {
				approval[r.Type]++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 · %s\n\n", task.Title)
	b.WriteString("> 本报告由年轮账本直接组装,零 LLM;相同账本状态重复导出字节级一致。\n\n")

	// ---- 任务概要 ----
	// P4-27 ② 受阻单显(报告口径):活动任务(运行中/收尾中)且存在
	// 模型故障归因的 blocked 步骤时状态词单显"受阻"——报告是账本快照
	// 文档,受阻派生走账本真值(面板另走链级实时信号,见 server);确定性
	// 归因不计(程序缺陷非模型故障)。冬眠等冻结态本就单显,不叠加受阻。
	statusWord := taskStatusWords[task.Status]
	if statusWord == "" {
		statusWord = task.Status
	}
	if (task.Status == "started" || task.Status == "settling") &&
		swarm.ModelFaultBlockedCount(ctx, store, task.ID) > 0 {
		statusWord = "受阻"
	}
	startedAt, endedAt := lifecycleTimes(lifecycle)
	b.WriteString("## 任务概要\n\n")
	fmt.Fprintf(&b, "- 任务 ID:`%s`\n", task.ID)
	if task.Status == "stopped" {
		// 已停止统称手动终止与有序收尾(P4-25 审核迭代),括注结束方式
		// 与面板悬停同口径。
		way := "手动终止"
		if by := settledByOf(lifecycle); by == rings.SettleByTaskGate {
			way = "整体超时"
		} else if by == rings.SettleByClosurePrompt {
			way = "收口结束"
		}
		fmt.Fprintf(&b, "- 任务状态:%s(%s)\n", statusWord, way)
	} else {
		fmt.Fprintf(&b, "- 任务状态:%s(%s)\n", statusWord, task.Status)
	}
	fmt.Fprintf(&b, "- 起:%s", fmtTime(startedAt))
	if !endedAt.IsZero() {
		fmt.Fprintf(&b, " · 止:%s", fmtTime(endedAt))
	}
	b.WriteString("\n")
	if len(goals) > 0 {
		b.WriteString("- 目标:")
		for i, g := range goals {
			if i > 0 {
				b.WriteString(";")
			}
			fmt.Fprintf(&b, "%s %s[%s]", g.ID, g.Title, goalStatusWords[g.Status])
		}
		b.WriteString("\n")
	}
	b.WriteString("- 测试范围(RoE):")
	b.WriteString(roeScope(roe))
	b.WriteString("\n")
	fmt.Fprintf(&b, "- 数据截至 %s(按最后事件时刻,非导出时间)\n", fmtTime(lastAt))
	if scoped := scopedCount(claims, inScope); scoped != len(claims) {
		fmt.Fprintf(&b, "- 本次导出范围:%d / %d 条发现(其余发现不在本次导出内)\n", scoped, len(claims))
	}

	// ---- 聚合摘要 ----
	// fixed(已修复,P3-06)计入 Confirmed 口径——它是已确认发现之上的修复
	// 标记,不是发现消失;另计已修复条数单列呈现。过程态计数按 P4-56
	// 四页签显示词(等待反证/反证中/反证暂停,由步骤现值推导);证据
	// 不足(原"待确认")与已废弃(refuted+discarded 合并档)单列。
	// P4-30 ③:复核标记不纳入报告的判断(信息性,无直接危害)整体退出
	// 报告各计数与清单——账本不涂改,一条汇总行如实交代(判断保留可溯)。
	var confirmed, fixedN, pending, forging, ungraded, excludedN, discardedN int
	severityCount := map[string]int{}
	for _, c := range claims {
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			excludedN++
			continue
		}
		switch c.Status {
		case rings.TestClaimConfirmed, rings.TestClaimFixed:
			confirmed++
			if c.Status == rings.TestClaimFixed {
				fixedN++
			}
			cs := cases[c.ID]
			if cs != nil && cs.Repro != nil && cs.Repro.Severity != "" {
				severityCount[cs.Repro.Severity]++
			} else {
				ungraded++
			}
		case rings.TestClaimPending:
			pending++
		case rings.TestClaimRefuted, rings.TestClaimDiscarded:
			discardedN++
		case rings.TestClaimCandidate, rings.TestClaimTesting, rings.TestClaimAwaitingRound:
			forging++
		}
	}
	covered := 0
	for _, a := range ag.Assets {
		if a.Covered {
			covered++
		}
	}
	stepByStatus := map[string]int{}
	for _, s := range steps {
		stepByStatus[s.Status]++
	}
	b.WriteString("\n## 聚合摘要\n\n")
	// 证据不足为 0 时不输出指向证据不足清单的理由引用(悬挂指向)。
	if pending > 0 {
		fmt.Fprintf(&b, "- 已确认发现:**%d** 条;证据不足:%d 条(理由见证据不足清单)\n", confirmed, pending)
	} else {
		fmt.Fprintf(&b, "- 已确认发现:**%d** 条;证据不足:0 条\n", confirmed)
	}
	if fixedN > 0 {
		fmt.Fprintf(&b, "- 其中已修复(复测确认):%d 条\n", fixedN)
	}
	if discardedN > 0 {
		fmt.Fprintf(&b, "- 已废弃:%d 条(被反证推翻或目标已摘除,理由留档)\n", discardedN)
	}
	if excludedN > 0 {
		fmt.Fprintf(&b, "- 不纳入报告:%d 条判断经复核为信息性观察(无直接危害),不作为漏洞呈现;判断与证据保留在账本\n", excludedN)
	}
	if forging > 0 {
		fmt.Fprintf(&b, "- 判断在途:%s(共 %d 条)\n",
			inFlightWords(claims, steps, store.SteerKilledSteps(ctx, taskID), cases), forging)
	}
	var sevParts []string
	for _, sev := range severityOrder {
		if n := severityCount[sev]; n > 0 {
			sevParts = append(sevParts, fmt.Sprintf("%s %d", sev, n))
		}
	}
	if len(sevParts) > 0 {
		line := strings.Join(sevParts, " · ")
		if ungraded > 0 {
			line += fmt.Sprintf(" · 未定级 %d", ungraded)
		}
		fmt.Fprintf(&b, "- 级别分布(已确认,跟影响面证据走):%s\n", line)
	} else {
		fmt.Fprintf(&b, "- 级别分布(已确认,跟影响面证据走):无定稿发现\n")
	}
	if len(ag.Assets) > 0 {
		fmt.Fprintf(&b, "- 资产覆盖:%d / %d(覆盖率 %s)\n", covered, len(ag.Assets), pct(covered, len(ag.Assets)))
	} else {
		b.WriteString("- 资产覆盖:0 / 0\n")
	}
	fmt.Fprintf(&b, "- 已确认事实:%d 条;步骤 %d 条(%s)\n",
		len(facts), len(steps), stepStatusWords(stepByStatus))

	// ---- 资产覆盖统计 ----
	b.WriteString("\n## 资产覆盖统计\n\n")
	b.WriteString("| 资产类 | 总数 | 已覆盖 |\n|---|---:|---:|\n")
	classTotal := map[rings.AssetClass]int{}
	classCovered := map[rings.AssetClass]int{}
	assetTitle := map[string]string{}
	for _, a := range ag.Assets {
		classTotal[a.AssetClass]++
		if a.Covered {
			classCovered[a.AssetClass]++
		}
		assetTitle[a.ID] = a.Title
	}
	for _, cls := range assetClassOrder {
		if classTotal[cls] == 0 {
			continue // 零计数类不占行(12 类全列出归数据量增长后)
		}
		fmt.Fprintf(&b, "| %s | %d | %d |\n", assetClassWord(cls), classTotal[cls], classCovered[cls])
	}
	fmt.Fprintf(&b, "| **合计** | **%d** | **%d** |\n", len(ag.Assets), covered)

	// ---- Confirmed 发现 ----
	// 含已修复(fixed):已修复是已确认发现的修复状态标记,报告如实呈现
	// 修复结论(条目内带复测行,P3-06),不从成果清单消失。
	// 复核标记不纳入报告的(P4-30 ③,信息性)不进清单。
	b.WriteString("\n## 已确认发现\n\n")
	n := 0
	for _, c := range claims {
		if (c.Status != rings.TestClaimConfirmed && c.Status != rings.TestClaimFixed) || !inScope(c.ID) {
			continue
		}
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			continue
		}
		n++
		writeClaim(&b, n, "C", c, assetTitle, abouts, cases[c.ID])
	}
	if n == 0 {
		b.WriteString("本轮无经独立反证与机器判定确认的发现。\n")
	}

	// ---- 证据不足清单 ----
	// P4-56 ②:原"待确认清单"改词——证据不足(锤炼完仍证不实,挂起
	// 可复活),理由直陈(R4)。
	b.WriteString("\n## 证据不足清单\n\n")
	n = 0
	for _, c := range claims {
		if c.Status != rings.TestClaimPending || !inScope(c.ID) {
			continue
		}
		if cases[c.ID] != nil && cases[c.ID].ReportExcluded {
			continue
		}
		n++
		writeClaim(&b, n, "P", c, assetTitle, abouts, cases[c.ID])
	}
	if n == 0 {
		b.WriteString("无证据不足发现。\n")
	}

	// ---- 年轮审计摘要 ----
	b.WriteString("\n## 年轮审计摘要\n\n")
	fmt.Fprintf(&b, "- 事件总数:%d\n", len(evs))
	b.WriteString("- 事件构成:")
	kinds := make([]string, 0, len(kindCount))
	for k := range kindCount {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	for i, k := range kinds {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "%s ×%d", k, kindCount[rings.EventKind(k)])
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- 需批准:请求 %d · 批准 %d · 拒绝 %d",
		approval[rings.ApprovalRecRequested], approval[rings.ApprovalRecApproved],
		approval[rings.ApprovalRecDenied])
	// P4-51 ⑤:"规划后放行"废除——新任务零 forced;历史任务(存量账本)
	// 有则如实列出,无则不占行。
	if n := approval[rings.ApprovalRecForced]; n > 0 {
		fmt.Fprintf(&b, " · 规划后放行 %d", n)
	}
	b.WriteString("\n")
	if line := fuseLine(lifecycle); line != "" {
		fmt.Fprintf(&b, "- 终止记录:%s\n", line)
	} else {
		b.WriteString("- 终止记录:无\n")
	}

	b.WriteString("\n---\n导出自 Agent GhostReaper 年轮账本;每条发现的反证链与血缘可按节点 ID 在账本下钻。\n")
	return b.String(), nil
}

// scopedCount 范围内声明计数(导出范围行)。
func scopedCount(claims []rings.NodeFull, inScope func(string) bool) int {
	n := 0
	for _, c := range claims {
		if inScope(c.ID) {
			n++
		}
	}
	return n
}

// findTask 取任务投影行。
func findTask(ctx context.Context, store *rings.Store, taskID string) (rings.TaskRow, error) {
	rows, err := store.Tasks(ctx)
	if err != nil {
		return rings.TaskRow{}, fmt.Errorf("report: 查任务: %w", err)
	}
	for _, r := range rows {
		if r.ID == taskID {
			return r, nil
		}
	}
	return rings.TaskRow{}, fmt.Errorf("report: 任务 %s 不存在", taskID)
}

// lifecycleTimes 从生命周期事件取起止:起=首个 started,止=最末
// completed/stopped(完成/熔断终态),无终态则零值(运行中)。
func lifecycleTimes(evs []rings.RawEvent) (time.Time, time.Time) {
	var start, end time.Time
	for _, ev := range evs {
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		switch p.Action {
		case "started":
			if start.IsZero() {
				start = ev.OccurredAt
			}
		case "completed", "stopped":
			end = ev.OccurredAt
		}
	}
	return start, end
}

// fuseLine 熔断审计一句话:谁(operator)、何时、为何;无熔断返回空。
func fuseLine(evs []rings.RawEvent) string {
	for _, ev := range evs {
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Action != "stopped" {
			continue
		}
		operator, _ := p.Detail["operator"].(string)
		reason, _ := p.Detail["reason"].(string)
		line := fmt.Sprintf("%s 于 %s", operator, fmtTime(ev.OccurredAt))
		if reason != "" {
			line += " · 理由:" + reason
		}
		return line
	}
	return ""
}

// settledByOf 终态为有序收尾时的发起方(最后一条 stopped 的 detail.
// settled_by 在词表内才算;P2-04 整体超时 task_gate / P4-18 收口结束
// closure_prompt)。操作者熔断不带此字段,返回空。
func settledByOf(evs []rings.RawEvent) string {
	by := ""
	for _, ev := range evs {
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Action != "stopped" {
			continue
		}
		if v, _ := p.Detail["settled_by"].(string); v == rings.SettleByTaskGate || v == rings.SettleByClosurePrompt {
			by = v
		}
	}
	return by
}

// writeClaim 写一条发现小节:prefix C-(已确认)或 P-(证据不足)。
// 证据不足的理由直陈(R4);发现者/验证者并排(R12 可审计)。
// P2-13:小节含完整反证时间线与收割定稿复现步骤(卷宗装配与面板
// 发现详情同源,claim.go)。
func writeClaim(b *strings.Builder, n int, prefix string, c rings.NodeFull,
	assetTitle map[string]string, abouts []rings.EdgeView, cs *ClaimCase) {
	vulnclass, _ := c.Detail["vulnclass"].(string)
	discoverer, _ := c.Detail["unit_id"].(string)
	// 声明文本优先定稿校对稿(P4-59 ①:报告级文风——描述=一句话本质+
	// 影响对象,危害=直接说结果+数字精确);未生成/未过闸退节点原文
	//(机器判定与审计仍读节点,包装不是证据)。
	statement, impact := c.Title, ""
	if impactRaw, ok := c.Detail["impact"].(string); ok {
		impact = impactRaw
	}
	if cs != nil && cs.Summary != nil {
		if cs.Summary.Statement != "" {
			statement = cs.Summary.Statement
		}
		if cs.Summary.Impact != "" {
			impact = cs.Summary.Impact
		}
	}
	// 标题是一句话人话(判断原文),tc-ID 进条目内详情行,不作标题。
	head := fmt.Sprintf("### %s-%d %s", prefix, n, statement)
	if vulnclass != "" {
		head += " · " + vulnclass
	}
	if cs != nil && cs.Repro != nil && cs.Repro.Severity != "" {
		head += " · 级别 " + cs.Repro.Severity
	}
	fmt.Fprintf(b, "%s\n\n", head)
	fmt.Fprintf(b, "- 判断标识:`%s`\n", c.ID)
	fmt.Fprintf(b, "- 存在性判断:%s\n", statement)
	if impact != "" {
		fmt.Fprintf(b, "- 影响面判断:%s\n", impact)
	}
	if cs != nil && cs.Retest != nil {
		fmt.Fprintf(b, "- 复测:%s(第 %d 轮)\n", retestVerdictWord(cs.Retest.Verdict), cs.Retest.Round)
	}
	var anchors []string
	for _, e := range abouts {
		if e.From != c.ID {
			continue
		}
		if t := assetTitle[e.To]; t != "" {
			anchors = append(anchors, fmt.Sprintf("`%s`(%s)", e.To, t))
		} else {
			anchors = append(anchors, "`"+e.To+"`")
		}
	}
	if len(anchors) > 0 {
		fmt.Fprintf(b, "- 资产锚点:%s\n", strings.Join(anchors, "、"))
	}
	if discoverer != "" {
		fmt.Fprintf(b, "- 发现单位 `%s`", discoverer)
		if step, _ := c.Detail["step_id"].(string); step != "" {
			fmt.Fprintf(b, " · 发现步骤 `%s`", step)
		}
		if verifier := cs.Verifier(); verifier != "" {
			fmt.Fprintf(b, ";验证单位 `%s`(验证者 ≠ 发现者)", verifier)
		} else if step := cs.FalsifyStep(); step != "" {
			fmt.Fprintf(b, ";反证步骤 `%s`", step)
		}
		b.WriteString("\n")
	}
	if prefix == "P" {
		if reason := cs.PendingReason(); reason != "" {
			fmt.Fprintf(b, "- 证据不足:%s\n", reason)
		}
	}
	if prefix == "C" {
		b.WriteString("- 判定:仲裁者机器判定已确认(条件求值全部通过)\n")
	}
	// 来龙去脉(P4-13):定稿冻结的模型叙述,读定稿零 LLM(R17);旧定稿
	// 无叙述如实标注,不编造。P4-24:文风校验未过的叙述带说明行。
	if cs != nil && cs.Repro != nil {
		switch {
		case cs.Repro.Narrative != "":
			fmt.Fprintf(b, "- 来龙去脉:%s\n", cs.Repro.Narrative)
			if cs.Repro.NarrativeUnpolished {
				b.WriteString("  文字未过文风校验,按原文保留\n")
			}
		default:
			b.WriteString("- 来龙去脉:定稿时未生成叙述\n")
		}
	}
	// 反证记录:完整时间线(面板发现详情同源同一装配)。P4-56 ⑤:
	// 定稿冻结的人话三段(疑点→怎么验的→结论)先于人话对应轮次的
	// 机器行——报告与面板同口径(人话层默认可见,机器原文随后)。
	if cs != nil && len(cs.Timeline) > 0 {
		b.WriteString("- 反证记录:\n")
		humanOf := map[int]rings.FalsifyRoundHuman{}
		if cs.Repro != nil {
			for _, rh := range cs.Repro.Rounds {
				humanOf[rh.Round] = rh
			}
		}
		for _, e := range cs.Timeline {
			if e.Kind == "falsify" && e.Round > 0 {
				if rh, ok := humanOf[e.Round]; ok && (rh.Doubt != "" || rh.How != "" || rh.Conclusion != "") {
					fmt.Fprintf(b, "  - 第 %d 轮人话\n", e.Round)
					if rh.Doubt != "" {
						fmt.Fprintf(b, "    疑点:%s\n", rh.Doubt)
					}
					if rh.How != "" {
						fmt.Fprintf(b, "    怎么验的:%s\n", rh.How)
					}
					if rh.Conclusion != "" {
						fmt.Fprintf(b, "    结论:%s\n", rh.Conclusion)
					}
					if rh.Unpolished {
						b.WriteString("    (文字未过文风校验,按原文保留)\n")
					}
				}
			}
			if line := caseEntryLine(e); line != "" {
				fmt.Fprintf(b, "  - %s\n", line)
			}
		}
	}
	// 竞争性替代解释(创建时冻结 · 逐条排除状态)。
	if cs != nil && len(cs.Alternatives) > 0 {
		var alts []string
		for _, a := range cs.Alternatives {
			killed := "未排除"
			if a.Killed {
				killed = "已排除"
			}
			label := a.Label
			if label == "" {
				label = a.ID
			}
			alts = append(alts, fmt.Sprintf("%s(%s)", label, killed))
		}
		fmt.Fprintf(b, "- 其他可能原因(创建时冻结):%s\n", strings.Join(alts, ";"))
	}
	// 复现步骤(收割定稿,P4-35 最小集口径):每步三层(短名/做什么/
	// 作用)+ Burp 可验证原文(与面板同口径)。P4-08 ⑤:操作方法(op
	// 行)撤下报告正文,收进证据包;P4-35:过程流量计数如实标注
	// (未进最小集的交换全量在证据包)。
	if cs != nil && cs.Repro != nil {
		fmt.Fprintf(b, "- 复现步骤(检查归纳定稿")
		if cs.Repro.Severity != "" {
			fmt.Fprintf(b, " · 级别 %s", cs.Repro.Severity)
		}
		if n := len(cs.Repro.Steps); n > 0 {
			fmt.Fprintf(b, " · 最小集 %d 步", n)
		}
		if m := cs.Repro.ProcessUnlisted(); m > 0 {
			fmt.Fprintf(b, " · 过程流量 %d 条见证据包", m)
		}
		fmt.Fprintf(b, "):\n")
		if len(cs.Repro.Steps) == 0 {
			b.WriteString("  - (反证轮未产生可复现的流量记录)\n")
		}
		for i, st := range cs.Repro.Steps {
			fmt.Fprintf(b, "  - %s\n", reproStepHead(i+1, st))
			if st.Desc != "" {
				fmt.Fprintf(b, "    做什么:%s\n", st.Desc)
			} else if st.Method != "" || st.URL != "" {
				fmt.Fprintf(b, "    做什么:%s %s → %d\n", st.Method, st.URL, st.Status)
			}
			if st.Purpose != "" {
				fmt.Fprintf(b, "    作用:%s\n", st.Purpose)
			}
			if st.Unpolished { // P4-24:文风校验未过的如实标注(与面板同句)
				b.WriteString("    (文字未过文风校验,按原文保留)\n")
			}
			if st.TLSWord != "" {
				fmt.Fprintf(b, "    目标证书:%s(包外信息行,校验事实在原始数据)\n", st.TLSWord)
			}
			if st.Raw != "" {
				fmt.Fprintf(b, "    HTTP 原文(Burp 可验证%s):\n", map[bool]string{true: ",面板同口径截断,全文在证据包", false: ""}[st.RawClip])
				fmt.Fprintf(b, "    %s\n", fence(st.Raw, "    "))
				fmt.Fprintf(b, "    原始数据 `%s`\n", st.BlobRef)
			}
		}
	}
	// 修复建议(第五章 §6:确定性知识库查表,零 LLM):与面板详情抽屉
	// 同源同一张表(report.Remediation),面板所见即报告所印;未命中
	// 不编造,如实注明。
	if items := Remediation(vulnclass); len(items) > 0 {
		for i, item := range items {
			fmt.Fprintf(b, "- 修复建议 %d/%d:%s\n", i+1, len(items), item)
		}
	} else if vulnclass != "" {
		b.WriteString("- 修复建议:该类型暂无预置条目\n")
	} else {
		b.WriteString("- 修复建议:未申报类型,无预置条目\n")
	}
	b.WriteString("\n")
}

// Verifier 卷宗时间线取最新验证单位(verdict 优先,次 falsify)。
// 详情 API 与报告共用(面板所见即报告所印)。
func (cs *ClaimCase) Verifier() string {
	if cs == nil {
		return ""
	}
	for i := len(cs.Timeline) - 1; i >= 0; i-- {
		e := cs.Timeline[i]
		if (e.Kind == "verdict" || e.Kind == "falsify") && e.UnitID != "" {
			return e.UnitID
		}
	}
	return ""
}

// FalsifyStep 最新反证 Step。
func (cs *ClaimCase) FalsifyStep() string {
	if cs == nil {
		return ""
	}
	for i := len(cs.Timeline) - 1; i >= 0; i-- {
		if cs.Timeline[i].Kind == "falsify" && cs.Timeline[i].StepID != "" {
			return cs.Timeline[i].StepID
		}
	}
	return ""
}

// PendingReason 最新非 confirm 裁决理由(R4)。
func (cs *ClaimCase) PendingReason() string {
	if cs == nil {
		return ""
	}
	for i := len(cs.Timeline) - 1; i >= 0; i-- {
		if cs.Timeline[i].Kind == "verdict" && cs.Timeline[i].PendingReason != "" {
			return cs.Timeline[i].PendingReason
		}
	}
	return ""
}

// VerdictRev 最新裁决事件的账本版本号(无裁决为零)。
func (cs *ClaimCase) VerdictRev() int64 {
	if cs == nil {
		return 0
	}
	for i := len(cs.Timeline) - 1; i >= 0; i-- {
		if cs.Timeline[i].Kind == "verdict" {
			return cs.Timeline[i].Rev
		}
	}
	return 0
}

// Severity 定稿严重级别(=末轮反证影响面定级;未定稿为空)。
func (cs *ClaimCase) Severity() string {
	if cs == nil || cs.Repro == nil {
		return ""
	}
	return cs.Repro.Severity
}

// ReproSteps 定稿复现步骤数(列表轻量字段)。
func (cs *ClaimCase) ReproSteps() int {
	if cs == nil || cs.Repro == nil {
		return 0
	}
	return len(cs.Repro.Steps)
}

// caseEntryLine 时间线条目 → 报告一句话(与面板 TimelineEntry 同义,
// 报告侧直陈事实)。
// caseEntryLine 时间线不带版本号(报告是给操作者的陈述,版本刻度
// 属开发口径;面板侧版本号在悬停)。
func caseEntryLine(e CaseEntry) string {
	switch e.Kind {
	case "created":
		return fmt.Sprintf("判断已记录，等待安排反证(发现单位 `%s`)", orDash(e.UnitID))
	case "state":
		return fmt.Sprintf("状态 → %s", rings.ClaimStatusWord(e.Status))
	case "falsify":
		exist := "存在性未推翻"
		if e.ExistenceRefuted {
			exist = "存在性被推翻"
		}
		round := ""
		if e.Round > 0 {
			round = fmt.Sprintf("第 %d 轮", e.Round)
		}
		line := fmt.Sprintf("%s反证(单位 `%s`):%s", round, e.UnitID, exist)
		if e.ImpactSupported {
			level := e.ImpactLevel
			if level == "" {
				level = "未定级"
			}
			line += " · 影响面:证据充分(" + level + ")"
		} else {
			line += " · 影响面:证据不足"
		}
		for _, a := range e.AltResults {
			word := "未排除"
			if a.Refuted {
				word = "已排除"
			}
			line += fmt.Sprintf(" · 其他可能原因 %s %s", altWord(a), word)
		}
		if e.EvidenceHead != "" {
			line += " · 证据:" + e.EvidenceHead
		}
		return line
	case "verdict":
		switch e.Decision {
		case rings.VerdictConfirm:
			return "机器判定:已确认"
		case rings.VerdictAdvanceRound:
			return fmt.Sprintf("轮末评估:待下一轮反证(%s)", e.PendingReason)
		default:
			return fmt.Sprintf("机器判定:证据不足(%s)", e.PendingReason)
		}
	case "reopen":
		return fmt.Sprintf("重开(触发:%s · 理由:%s)", reopenWord(e.Trigger), e.Reason)
	case "repro":
		return fmt.Sprintf("检查归纳定稿(级别 %s)", orDash(e.Severity))
	}
	return ""
}

// altWord 时间线疑点内容(装配已按冻结清单回填 label;旧数据无
// label 时退显 ID,如实不编造)。
func altWord(a rings.AltResult) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

func reopenWord(trigger string) string {
	switch trigger {
	case rings.ReopenNewEvidence:
		return "新证据"
	case rings.ReopenManual:
		return "人工反驳"
	case rings.ReopenExpired:
		return "结论过期"
	}
	return trigger
}

// inFlightWords 判断在途计数行(P4-56 四页签显示词,与面板同拍):按
// 机器状态+反证步骤现值推导(等待反证/反证中/反证暂停),零计数段不
// 输出;无在途判断返回空串(调用方不输出本行)。
func inFlightWords(claims, steps []rings.NodeFull, steerKills map[string]bool, cases map[string]*ClaimCase) string {
	falsifyOf := map[string][]rings.NodeFull{}
	for _, s := range steps {
		if kind, _ := s.Detail["kind"].(string); kind != "falsify" {
			continue
		}
		if cid, _ := s.Detail["testclaim_id"].(string); cid != "" {
			falsifyOf[cid] = append(falsifyOf[cid], s)
		}
	}
	counts := map[string]int{}
	for _, c := range claims {
		switch c.Status {
		case rings.TestClaimCandidate, rings.TestClaimTesting, rings.TestClaimAwaitingRound:
		default:
			continue
		}
		var reason string
		if cs := cases[c.ID]; cs != nil {
			reason = cs.PendingReason()
		}
		key, _ := rings.ClaimDisplay(c.Status, rings.ClaimFlightOfSteps(falsifyOf[c.ID], steerKills), reason)
		counts[key]++
	}
	var parts []string
	for _, key := range []string{
		rings.ClaimDisplayFalsifying, rings.ClaimDisplayWaitFalsify,
		rings.ClaimDisplayFalsifyPaused,
	} {
		if n := counts[key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", rings.ClaimDisplayWord(key), n))
		}
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// fence 原文块包裹:缩进式代码块(4 空格)不受内部反引号/栅栏影响,
// md 渲染保真且不破版。
func fence(raw, indent string) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	for i, l := range lines {
		lines[i] = indent + "    " + l
	}
	return strings.Join(lines, "\n")
}

// roeScope RoE 测试范围一句话(空值语义:边界从宽起步)。审批门槛
// 归一展示(P2-06b:空=默认宽,仅增删改弹门)。
func roeScope(roe rings.RoeState) string {
	var parts []string
	threshold := roe.ApprovalThreshold
	if threshold == "" {
		threshold = "修改与持久化"
	}
	parts = append(parts, "审批门槛 "+string(threshold)+" 及以上需批准")
	if len(roe.DeniedTools) > 0 {
		parts = append(parts, "禁止工具 "+strings.Join(roe.DeniedTools, "、"))
	}
	if len(roe.AllowedTools) > 0 {
		parts = append(parts, "仅允许工具 "+strings.Join(roe.AllowedTools, "、"))
	}
	if roe.Note != "" {
		parts = append(parts, "说明:"+roe.Note)
	}
	return strings.Join(parts, ";")
}

// retestVerdictWord 复测结论词的报告文案(裸状态词不上报告,P3-06)。
func retestVerdictWord(v string) string {
	switch v {
	case rings.RetestVerdictFixed:
		return "已修复"
	case rings.RetestVerdictUnfixed:
		return "未修复"
	case rings.RetestVerdictInconclusive:
		return "未能定论"
	}
	return v
}

// stepStatusWords 步骤状态计数的报告文案(固定顺序,零计数省略)。
// 顺序清单必须覆盖 stepStatusWordsMap 全部键(P4-46 ②:approval_denied
// 曾漏列——计数入总数而词被吞,"7 条(已中止 5 · 已取消 1)"对不上账;
// 测试逐键对账防再漏)。
func stepStatusWords(byStatus map[string]int) string {
	var words []string
	for _, st := range []string{
		"done", "exhausted", "failed", "blocked", "approval_denied",
		"aborted", "cancelled", "running", "dispatched",
	} {
		if n := byStatus[st]; n > 0 {
			words = append(words, fmt.Sprintf("%s %d", stepStatusWordsMap[st], n))
		}
	}
	if len(words) == 0 {
		return "无"
	}
	return strings.Join(words, " · ")
}

// assetClassWord 资产类 → 中文(与面板词汇同源,报告侧自持映射)。
func assetClassWord(cls rings.AssetClass) string {
	words := map[rings.AssetClass]string{
		rings.AssetDomain: "域名", rings.AssetSubdomain: "子域名", rings.AssetHost: "主机",
		rings.AssetService: "服务", rings.AssetAPI: "API 接口", rings.AssetOrganization: "组织",
		rings.AssetMiniProgram: "小程序", rings.AssetApp: "应用", rings.AssetWechatAccount: "微信公众号",
		rings.AssetPerson: "人员", rings.AssetLeakInfo: "泄露信息", rings.AssetOther: "其他",
	}
	if w := words[cls]; w != "" {
		return w
	}
	return string(cls)
}

func pct(a, b int) string {
	if b <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(a)/float64(b)*100)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "(账本无时刻)"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}
