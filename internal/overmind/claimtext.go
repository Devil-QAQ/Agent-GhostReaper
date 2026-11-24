package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// titleShortStandard 标题短句标准(P4-64 ⑥):标题是短句不是叙述,不套
// 报告级四段锚——术语门槛+完整可读资产标识(P4-27 裸引用规则)+机器语
// 特征闸门。锚例自身须过闸(改标准先跑 machinegate_test 的锚例用例)。
const titleShortStandard = `名称是一小句，供列表辨认这项观察或猜测。资产地址由页面单列，不必在名称中重复完整地址；必要的验证前提和确定性不能改变，不写成截断的执行记录。`

// ---- 声明摘要 ----

// ---- ② 声明摘要立账即生成 ----

// genClaimSummaryText 声明摘要生成全链(P4-59 定稿批次与 P4-64 立账
// 批次共用:事实包 → 模型按报告级锚改写 → 校对+闸门;字段全空 = 未
// 生成或未过闸,调用方按各自形态落账或不落)。
func (l *Loop) genClaimSummaryText(ctx context.Context, claimID string) rings.ClaimSummaryPolish {
	out := rings.ClaimSummaryPolish{ClaimID: claimID}
	if l.Provider == nil {
		return out
	}
	pack, err := l.summaryPack(ctx, claimID)
	if err != nil {
		runlog.Warnf("判断摘要生成失败: 任务 %s 判断 %s 原因 %s(读侧按原文呈现)",
			l.TaskID, claimID, clipEval(err.Error()))
		return out
	}
	gs := l.newGenSession(summarySystem)
	defer gs.close()
	text, err := gs.ask(ctx, summaryAskHead(pack))
	if err != nil {
		runlog.Warnf("判断摘要生成失败: 任务 %s 判断 %s 原因 %s(读侧按原文呈现)",
			l.TaskID, claimID, clipEval(err.Error()))
		return out
	}
	rec := &rings.TestClaimRecord{}
	if applySummary(rec, parseSummary(text, pack)) {
		l.polishSummary(ctx, claimID, gs, rec, pack)
	} else {
		runlog.Warnf("判断摘要解析失败: 任务 %s 判断 %s(读侧按原文呈现)", l.TaskID, claimID)
	}
	out.Title, out.Statement, out.Impact, out.Facts = rec.Title, rec.Statement, rec.Impact, rec.Facts
	return out
}

// ---- ③⑥ 事实描述与标题(收割确认批次) ----

// displayExplanationStandard 详情概览的阅读目标与真实性边界。
// 具体组织交给模型，响应细节留在独立证据区。
const displayExplanationStandard = `依据给定材料写完整、平实的中文，内容组织由你决定。这是操作者展开详情时先读的概览。像向同事交代结果一样，先用一两句通俗话讲清主要意思；响应码对照、协议字段和载荷清单留在证据中。必要时再另起短段交代关键条件。观察、推断、未验证和已证实的边界不能改变；没有发现问题只适用于本次验证范围。不要补造事实，也不要逐项复述响应头、载荷、工具输出或内部编号，它们保留在证据与完整原文中。`

const factTextSystem = `为安全测试面板整理事实的名称和说明。名称是方便辨认的一小句；说明让人看懂实际观察到什么，以及这个结果能说明什么。识别到的服务名、版本号等若就是本条结果，应直接写出材料中的实际值，不能只说“已取得服务名与版本”而让人去证据里找答案；协议明细与结果本身要分清。普通公开信息如实称为公开信息，不因为取到了它就写成泄露或攻击成功。资产地址在界面单列，不必在名称与正文反复写完整地址。事实已确认只表示这项观察有记录，不等于漏洞成立、凭据有效或攻击成功；不要替漏洞裁决定级。通常一两句就能讲清结果，不要另加泛泛的提醒。
` + displayExplanationStandard + `
只输出 JSON:{"facts":[{"fact_id":"…","title":"…","statement":"…"},…]}，条目标识与输入一致。`

// factTextAsk 事实文本的生成续问。
func factTextAsk(facts []factTextMaterial) string {
	payload, err := json.Marshal(facts)
	if err != nil {
		payload = []byte("[]")
	}
	return "事实原文与证据摘要:\n" + string(payload)
}

// factTextMaterial 单条事实素材。
type factTextMaterial struct {
	FactID       string `json:"fact_id"`
	Statement    string `json:"statement"`
	EvidenceHead string `json:"evidence_head,omitempty"`
}

// factTextReply 模型回复契约与单条包装。
type factTextReply struct {
	Facts []rings.FactTextPolish `json:"facts"`
}

// polishConfirmedFacts 事实文本生成(P4-64 ③⑥:收割确认批次,不限于
// 已定稿判断链上的事实):标题短句校对+描述报告级口径,一批事实一个
// 会话(生成→校对→闸门重写一次→标题仍脏如实标注、描述仍脏不冻结);
// 零 Provider 零外呼。
func (l *Loop) polishConfirmedFacts(ctx context.Context, factIDs []string) {
	sources, err := l.Store.NodeTextSourceRevs(ctx, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	l.polishFactsAt(ctx, factIDs, sources)
}

func (l *Loop) polishFactsAt(ctx context.Context, factIDs []string, sources map[string]int64) {
	if len(factIDs) == 0 || l.Provider == nil {
		return
	}
	materials := make([]factTextMaterial, 0, len(factIDs))
	for _, id := range factIDs {
		n, err := l.Store.Node(ctx, l.TaskID, id)
		if err != nil || n.Type != rings.NodeFact {
			continue
		}
		m := factTextMaterial{FactID: id, Statement: n.Title}
		if eh, _ := n.Detail["evidence_head"].(string); eh != "" {
			m.EvidenceHead = clipLine(eh, 200)
		} else if eh, _ := n.Detail["evidence"].(string); eh != "" {
			m.EvidenceHead = clipLine(eh, 200)
		}
		materials = append(materials, m)
	}
	if len(materials) == 0 {
		return
	}
	reply := l.genFactTexts(ctx, materials)
	events := make([]rings.Event, 0, len(reply))
	for _, m := range materials {
		p, ok := reply[m.FactID]
		if !ok {
			continue // 该条未生成或描述未过闸:读侧退节点原文
		}
		p.SourceRev = sources[m.FactID]
		events = append(events, rings.Event{
			Kind: rings.EventFactText, Actor: ActorOvermind, Payload: p,
		})
	}
	if len(events) == 0 {
		return
	}
	for _, event := range events {
		if _, err := l.Store.AppendDisplay(ctx, l.TaskID, event); err != nil {
			l.setErr(err)
		}
	}
}

// genFactTexts 事实文本生成全链:返回 fact_id → 包装(只含有内容的条目)。
func (l *Loop) genFactTexts(ctx context.Context, materials []factTextMaterial) map[string]rings.FactTextPolish {
	out := map[string]rings.FactTextPolish{}
	gs := l.newGenSession(factTextSystem)
	defer gs.close()
	text, err := gs.ask(ctx, factTextAsk(materials))
	if err != nil {
		runlog.Warnf("事实文本生成失败: 任务 %s 原因 %s(读侧按原文呈现)", l.TaskID, clipEval(err.Error()))
		return out
	}
	known := map[string]bool{}
	for _, m := range materials {
		known[m.FactID] = true
	}
	parsed := parseFactTexts(text, known)
	if len(parsed) == 0 {
		runlog.Warnf("事实文本解析失败: 任务 %s(读侧按原文呈现)", l.TaskID)
		return out
	}
	for id, p := range parsed {
		out[id] = p
	}
	// 闸门:标题命中 → 同会话重写一次 → 仍命中如实标注;描述命中 →
	// 重写一次 → 仍命中该条描述不冻结(读侧退节点标题,原文在位)。
	dirty := map[string][]talkHit{}
	for id, p := range out {
		if hits := scanDisplayText(p.Title); len(hits) > 0 {
			dirty[id] = append(dirty[id], hits...)
		}
		if hits := scanDisplayText(p.Statement); len(hits) > 0 {
			dirty[id] = append(dirty[id], hits...)
		}
	}
	if len(dirty) > 0 {
		if err := l.rewriteFactTexts(ctx, gs, out, dirty); err != nil {
			runlog.Warnf("事实文本改写失败: 任务 %s 原因 %s(如实标注)", l.TaskID, clipEval(err.Error()))
		}
	}
	for id, p := range out {
		titleHits := scanDisplayText(p.Title)
		if len(titleHits) > 0 {
			p.Unpolished = true
			runlog.Warnf("事实标题未过文风校验,按原文保留: 任务 %s 事实 %s(%s)",
				l.TaskID, id, strings.Join(talkHitLines(titleHits), ";"))
		}
		if hits := scanDisplayText(p.Statement); len(hits) > 0 {
			runlog.Warnf("事实描述未过文风校验,该字段按原文呈现: 任务 %s 事实 %s(%s)",
				l.TaskID, id, strings.Join(talkHitLines(hits), ";"))
			p.Statement = ""
		}
		out[id] = p
	}
	return out
}

// parseFactTexts 丢弃未知条目，保留完整句子与结论边界，不裁断显示稿。
func parseFactTexts(text string, known map[string]bool) map[string]rings.FactTextPolish {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r factTextReply
	if json.Unmarshal([]byte(body), &r) != nil {
		return nil
	}
	out := map[string]rings.FactTextPolish{}
	for _, f := range r.Facts {
		if !known[f.FactID] {
			continue
		}
		p := rings.FactTextPolish{
			FactID: f.FactID,
			Title:  strings.TrimSpace(f.Title), Statement: strings.TrimSpace(f.Statement),
		}
		if p.Title != "" || p.Statement != "" {
			out[f.FactID] = p
		}
	}
	return out
}

// rewriteFactTexts 闸门退回重写(命中条目与特征喂回同会话)。
func (l *Loop) rewriteFactTexts(ctx context.Context, gs *genSession, out map[string]rings.FactTextPolish, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你写的事实文本里有机器语,逐处改写掉,事实一字不动:\n")
	for id, hits := range dirty {
		p := out[id]
		fmt.Fprintf(&b, "事实 %s(标题:%s / 描述:%s):\n", id, clipLine(p.Title, 60), clipLine(p.Statement, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + displayExplanationStandard + "\n")
	b.WriteString(`只输出 JSON:{"facts":[{"fact_id":"…","title":"…","statement":"…"},…]},只含上面点名的条目。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for id := range dirty {
		known[id] = true
	}
	reparsed := parseFactTexts(text, known)
	for id, p := range reparsed {
		out[id] = p
	}
	return nil
}

const retestNoteSystem = `把本轮复测结果写成简短说明，先说实际结果，再说明影响结论的前提或限制。保留真实结论，不把未复现直接说成已修复，不逐项复述报文与参数。
` + displayExplanationStandard + `
只输出 JSON:{"note":"…"}`

// PolishRetestNote 复测结论 note 校对(P4-64 ⑦:入账时过通道;随
// retest 记录同批落 claim.retest_note 事件)。零 Provider/生成失败/仍
// 含机器语 → 返回 nil(不落事件,读侧退机器记录原文,不阻塞入账)。
// 导出面供 swarm 钩子(main.go 经注册表路由)调用。
func (l *Loop) PolishRetestNote(ctx context.Context, claimID string, round int, verdict, note string) *rings.RetestNotePolish {
	note = strings.TrimSpace(note)
	if l.Provider == nil || note == "" {
		return nil
	}
	gs := l.newGenSession(retestNoteSystem)
	defer gs.close()
	ask := fmt.Sprintf("复测结论:%s\n备注原文:\n%s\n请校对改写。", verdict, note)
	text, err := gs.ask(ctx, ask)
	if err != nil {
		runlog.Warnf("复测备注校对失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(按原文呈现)",
			l.TaskID, claimID, round, clipEval(err.Error()))
		return nil
	}
	out := parseSingleText(text, "note")
	if out == "" {
		runlog.Warnf("复测备注解析失败: 任务 %s 判断 %s 第 %d 轮(按原文呈现)", l.TaskID, claimID, round)
		return nil
	}
	if hits := scanDisplayText(out); len(hits) > 0 {
		// 退回重写一次;仍命中如实标注(原文保留)。
		var b strings.Builder
		b.WriteString("你改写的备注里有机器语,逐处改写掉,事实一字不动:\n")
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
		b.WriteString(talkRewriteReferralNote + `只输出 JSON:{"note":"…"}`)
		if text, err := gs.ask(ctx, b.String()); err == nil {
			out = parseSingleText(text, "note")
		}
		if hits := scanDisplayText(out); len(hits) > 0 {
			runlog.Warnf("复测备注未过文风校验,按原文保留: 任务 %s 判断 %s 第 %d 轮(%s)",
				l.TaskID, claimID, round, strings.Join(talkHitLines(hits), ";"))
			return nil
		}
	}
	if out == "" {
		return nil
	}
	return &rings.RetestNotePolish{ClaimID: claimID, Round: round, Note: strings.TrimSpace(out)}
}

// parseSingleText 宽容解析 {"<key>":"…"} 形态的单文本回复。
func parseSingleText(text, key string) string {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r map[string]string
	if json.Unmarshal([]byte(body), &r) != nil {
		return ""
	}
	return strings.TrimSpace(r[key])
}

// polishClaimTextAsync 为待核验/证据不足的漏洞补显示稿，独立于机器判定。
// 已确认分支的名称与说明由同批定稿摘要生成；此处亦供历史副本的正式
// 显示生成重跑使用，生成结果直接记账，不经人工替换。
func (l *Loop) polishClaimTextAsync(parent context.Context, claimID string) {
	if l.Provider == nil {
		return
	}
	source, err := l.Store.ClaimTextSourceRev(parent, l.TaskID, claimID)
	if err != nil {
		l.setErr(err)
		return
	}
	l.queueClaimTextAt(parent, claimID, source)
}

func (l *Loop) polishClaimText(ctx context.Context, claimID string) {
	source, err := l.Store.ClaimTextSourceRev(ctx, l.TaskID, claimID)
	if err != nil {
		return
	}
	l.polishClaimTextAt(ctx, claimID, source)
}

func (l *Loop) polishClaimTextAt(ctx context.Context, claimID string, source int64) {
	if !l.claimDisplayCurrent(ctx, claimID, source) {
		return
	}
	done, err := l.Store.HasClaimText(ctx, l.TaskID, claimID, source)
	if err != nil {
		l.setErr(err)
		return
	}
	if done {
		return
	}
	row, found, err := l.Store.ClaimCaseOf(ctx, l.TaskID, claimID)
	if err != nil {
		l.setErr(err)
		return
	}
	claim, err := l.Store.Node(ctx, l.TaskID, claimID)
	if err != nil {
		l.setErr(err)
		return
	}
	if found && (claim.Status == rings.TestClaimConfirmed || claim.Status == rings.TestClaimFixed) && row.Repro != nil && row.Repro.StepsIncomplete == "流程步骤整理中" && len(row.Repro.ProofSeqs) > 0 {
		l.polishClaimPackageAt(ctx, claimID, source, row)
		return
	}
	p := l.genClaimSummaryText(ctx, claimID)
	if p.Title == "" || p.Statement == "" || !l.claimDisplayCurrent(ctx, claimID, source) {
		return
	}
	if _, err := l.Store.AppendDisplay(ctx, l.TaskID, rings.Event{Kind: rings.EventClaimText, Actor: ActorOvermind, Payload: rings.ClaimTextPolish{ClaimID: claimID, SourceRev: source, Title: p.Title, Statement: p.Statement, Impact: p.Impact, Facts: p.Facts}}); err != nil {
		runlog.Warnf("漏洞显示稿未保存: 任务 %s 判断 %s 原因 %s", l.TaskID, claimID, clipEval(err.Error()))
	}
}

func clipEval(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
