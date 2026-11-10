package overmind

// P4-64 全站文风·发现族过程态(问题清单 #82,统一规则"模型文本的冻结
// 时点＝校对时点"):发现族各对象的显示层包装文本在各自的业务时点生成
// 并独立事件冻结——
//   - ① 反证时间线逐轮生成:每轮 falsify 记录入账时同链生成该轮三段
//     人话(经 swarm 钩子 RoundHumanGen,与机器记录同批落账;定稿批次
//     不再重复生成;重开后新轮次增量补);
//   - ② 声明摘要立账即生成:收割立判断批次(候选声明派反证时)按
//     genClaimSummary 同管线生成,锤炼中声明读侧即有摘要;定稿随新
//     定稿重新生成(repro 覆盖,取代语义既有);
//   - ③ 事实描述收割确认时生成(不限于已定稿判断链上的事实);
//   - ④⑧ 假设文本评估时生成:收割评估批次对每条被评估假设改写冻结
//     (标题/依据/评估理由同批),未评估"等待验证"瞬时态保持原文;
//   - ⑥ 事实/假设标题:事实确认时、假设评估时同批按短句标准校对
//     (不套报告级四段锚);
//   - ⑦ 复测结论 note:入账时过通道(经 swarm 钩子 RetestNotePolisher)。
// 全族同规矩(P4-59 包装分离先例):机器记录与节点原文一字不动(机器
// 判定/任务包/仲裁全走原文);包装稿独立事件冻结、投影 latest-wins,
// 读侧优先链退原文;生成失败回退(拼句/不冻结)不阻塞业务;报告导出
// 与面板渲染零 LLM(R17 不破);存量旧账本不回填。

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
const titleShortStandard = `标题标准(短句,不超过 40 字):平实书面白话;主语用完整标识(域名或 IP 起头、host:port 形态,如 jkysd.cn:9902),不用裸端口、裸路径;写事实本身(是什么/拿到了什么),不写"发现""疑似""成功"这类过程词;数字写实际值;无 emoji;不写工具名与实现细节。
正例:"example.com:8080 的备份接口无需登录即可下载""10.0.0.5 的备份包内带有后台管理员明文口令"。
反例:"发现 Get /backup 200 OK 高危漏洞"(机器语堆砌);"疑似存在未授权访问问题"(过程词、不置可否)。`

// ---- ① 反证时间线逐轮生成 ----

// GenRoundHumanFor 单轮人话生成(P4-64 ①:随 falsify 记录入账同链,
// 每轮一次模型调用,反证轮有界):素材即机器记录本身(解释标签自判断
// 冻结清单反查),先落确定性拼句,模型成功覆盖三段并过校对+闸门;零
// Provider 或任何失败保留拼句——回退拼句不阻塞轮次入账。导出面供
// swarm 钩子(main.go 经注册表路由)调用。
func (l *Loop) GenRoundHumanFor(ctx context.Context, claimID string, rec *rings.TestClaimRecord) *rings.ClaimRoundHuman {
	m := l.roundMaterialOf(ctx, claimID, rec)
	h := deterministicRoundHuman(m)
	out := &rings.ClaimRoundHuman{
		ClaimID: claimID, Round: m.Round,
		Doubt: h.Doubt, How: h.How, Conclusion: h.Conclusion,
	}
	if l.Provider == nil {
		return out
	}
	gs := l.newGenSession(roundHumanSystem)
	defer gs.close()
	pack := &roundHumanPack{Rounds: []roundMaterial{m}}
	if claim, err := l.Store.Node(ctx, l.TaskID, claimID); err == nil {
		pack.Statement = claim.Title
		pack.Impact, _ = claim.Detail["impact"].(string)
	}
	payload, err := json.Marshal(pack)
	if err != nil {
		return out
	}
	text, err := gs.ask(ctx, string(payload))
	if err != nil {
		runlog.Warnf("反证时间线人话生成失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(回退确定性拼句)",
			l.TaskID, claimID, m.Round, clipEval(err.Error()))
		return out
	}
	rounds, perr := parseRoundHumans(text, m.Round)
	if perr != nil {
		runlog.Warnf("反证时间线人话解析失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(回退确定性拼句)",
			l.TaskID, claimID, m.Round, clipEval(perr.Error()))
		return out
	}
	if r, ok := rounds[m.Round]; ok {
		if s := strings.TrimSpace(r.Doubt); s != "" {
			out.Doubt = clipLine(s, roundHumanClip)
		}
		if s := strings.TrimSpace(r.How); s != "" {
			out.How = clipLine(s, roundHumanClip)
		}
		if s := strings.TrimSpace(r.Conclusion); s != "" {
			out.Conclusion = clipLine(s, roundHumanClip)
		}
	}
	l.polishRoundHumanFor(ctx, claimID, gs, out)
	return out
}

// roundMaterialOf 机器记录 → 单轮素材(解释标签自判断冻结清单反查,
// 与既有定稿批次装配同口径)。
func (l *Loop) roundMaterialOf(ctx context.Context, claimID string, rec *rings.TestClaimRecord) roundMaterial {
	labels := map[string]string{}
	if claim, err := l.Store.Node(ctx, l.TaskID, claimID); err == nil {
		labels = altLabels(claim)
	}
	m := roundMaterial{
		Round:            rec.Round,
		UnitID:           rec.UnitID,
		ExistenceRefuted: rec.ExistenceRefuted,
		ImpactSupported:  rec.ImpactSupported,
		EvidenceHead:     clipLine(rec.EvidenceHead, 200),
		Note:             clipLine(rec.Note, 200),
	}
	for _, a := range rec.Alternatives {
		m.Alternatives = append(m.Alternatives, narrativeAlt{ID: a.ID, Label: labels[a.ID], Refuted: a.Refuted})
	}
	return m
}

// deterministicRoundHuman 单轮拼句兜底(deterministicRoundHumans 的单
// 轮版,词形同源)。
func deterministicRoundHuman(r roundMaterial) rings.FalsifyRoundHuman {
	h := rings.FalsifyRoundHuman{Round: r.Round}
	var doubts []string
	for _, a := range r.Alternatives {
		if a.Label == "" {
			continue
		}
		doubts = append(doubts, "会不会是「"+a.Label+"」造成的假象?")
	}
	h.Doubt = strings.Join(doubts, "")
	h.How = "独立单位 " + r.UnitID + " 反证;证据:" + orDash(r.EvidenceHead)
	switch {
	case r.ExistenceRefuted:
		h.Conclusion = "推翻"
	case len(r.Alternatives) > 0 && allRefuted(r.Alternatives):
		h.Conclusion = "已排除"
	default:
		h.Conclusion = "未排除"
	}
	return h
}

// polishRoundHumanFor 单轮人话的校对+闸门链(polishRoundHumans 的单轮
// 版:校对续问 → 特征扫描 → 退回重写一次 → 仍命中如实标注 Unpolished)。
func (l *Loop) polishRoundHumanFor(ctx context.Context, claimID string, gs *genSession, out *rings.ClaimRoundHuman) {
	if text, err := gs.ask(ctx, roundHumanProofreadAsk); err != nil {
		runlog.Warnf("反证时间线人话校对失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(保留生成稿)",
			l.TaskID, claimID, out.Round, clipEval(err.Error()))
	} else if rounds, perr := parseRoundHumans(text, out.Round); perr == nil {
		if r, ok := rounds[out.Round]; ok {
			out.Doubt = clipLine(r.Doubt, roundHumanClip)
			out.How = clipLine(r.How, roundHumanClip)
			out.Conclusion = clipLine(r.Conclusion, roundHumanClip)
		}
	}
	rh := rings.FalsifyRoundHuman{Round: out.Round, Doubt: out.Doubt, How: out.How, Conclusion: out.Conclusion}
	hits := roundHumanHits(rh)
	if len(hits) == 0 {
		return
	}
	if err := l.rewriteRoundHuman(ctx, gs, &rh, hits); err != nil {
		runlog.Warnf("反证时间线人话改写失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(如实标注)",
			l.TaskID, claimID, out.Round, clipEval(err.Error()))
	} else {
		hits = roundHumanHits(rh)
	}
	out.Doubt, out.How, out.Conclusion = rh.Doubt, rh.How, rh.Conclusion
	if len(hits) > 0 {
		out.Unpolished = true
		runlog.Warnf("反证时间线人话未过文风校验,按原文保留: 任务 %s 判断 %s 第 %d 轮(%s)",
			l.TaskID, claimID, out.Round, strings.Join(talkHitLines(hits), ";"))
	}
}

// roundHumanHits 单轮三段的机器语特征扫描。
func roundHumanHits(r rings.FalsifyRoundHuman) []talkHit {
	var hits []talkHit
	for _, text := range []string{r.Doubt, r.How, r.Conclusion} {
		hits = append(hits, ScanMachineTalk(text)...)
	}
	return hits
}

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
	out.Statement, out.Impact, out.Facts = rec.Statement, rec.Impact, rec.Facts
	return out
}

// polishClaimLaunchSummaries 收割立判断批次(P4-64 ②):本轮刚派反证
// 的候选声明逐条生成摘要并落 claim.summary 事件——锤炼中(未定稿)声明
// 读侧即有摘要;确认定稿由 genClaimSummary 随新定稿重新生成覆盖(投影
// 同列 latest-wins,取代语义既有)。
func (l *Loop) polishClaimLaunchSummaries(ctx context.Context, claimIDs []string) {
	for _, id := range claimIDs {
		s := l.genClaimSummaryText(ctx, id)
		if s.Statement == "" && s.Impact == "" && len(s.Facts) == 0 {
			continue
		}
		if _, err := l.Store.Append(ctx, l.TaskID, rings.Event{
			Kind: rings.EventClaimSummaryPol, Actor: ActorOvermind, Payload: s,
		}); err != nil {
			runlog.Warnf("判断摘要写入失败: 任务 %s 判断 %s 原因 %s(读侧按原文呈现)",
				l.TaskID, id, clipEval(err.Error()))
		}
	}
}

// ---- ③⑥ 事实描述与标题(收割确认批次) ----

// factTextSystem 事实文本生成的系统段:标题走短句标准、描述走报告级
// 描述口径(一句话本质+影响对象)。
const factTextSystem = `为渗透测试确认的事实改写两段文本,写给不懂技术的读者也能读懂。输入是每条事实的陈述原文与证据摘要。任务:

1. title(标题):按标题标准写一句短句——陈述事实本身,主语用完整标识。
2. statement(描述):一句话本质+影响对象;事实不好懂时补一句通俗原理,用"就是…从而…"句式。正例:"10.0.0.5 的备份文件无需登录即可下载,包内带有后台管理员的明文口令"。

规则:
1. 只改文风,事实一字不动:目标、路径、数字、凭据有无全照抄;不编造原文没有的信息;证据摘要只是改写依据,不写进正文。
2. 平实书面白话;短句;无 emoji;不写工具名与实现细节。
3. fact_id 原样返回。

只输出 JSON:{"facts":[{"fact_id":"…","title":"…","statement":"…"},…]},facts 只含输入里给出的条目。`

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
		events = append(events, rings.Event{
			Kind: rings.EventFactText, Actor: ActorOvermind, Payload: p,
		})
	}
	if len(events) == 0 {
		return
	}
	if _, err := l.Store.Append(ctx, l.TaskID, events...); err != nil {
		runlog.Warnf("事实文本写入失败: 任务 %s 原因 %s(读侧按原文呈现)",
			l.TaskID, clipEval(err.Error()))
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
	// 校对续问(同会话,模型可见自己刚写的全部条目)。
	if text, err := gs.ask(ctx, factTextProofreadAsk); err == nil {
		if reparsed := parseFactTexts(text, known); len(reparsed) > 0 {
			out = reparsed
		}
	} else {
		runlog.Warnf("事实文本校对失败: 任务 %s 原因 %s(保留生成稿)", l.TaskID, clipEval(err.Error()))
	}
	// 闸门:标题命中 → 同会话重写一次 → 仍命中如实标注;描述命中 →
	// 重写一次 → 仍命中该条描述不冻结(读侧退节点标题,原文在位)。
	dirty := map[string][]talkHit{}
	for id, p := range out {
		if hits := ScanMachineTalk(p.Title); len(hits) > 0 {
			dirty[id] = append(dirty[id], hits...)
		}
		if hits := ScanMachineTalk(p.Statement); len(hits) > 0 {
			dirty[id] = append(dirty[id], hits...)
		}
	}
	if len(dirty) > 0 {
		if err := l.rewriteFactTexts(ctx, gs, out, dirty); err != nil {
			runlog.Warnf("事实文本改写失败: 任务 %s 原因 %s(如实标注)", l.TaskID, clipEval(err.Error()))
		}
	}
	for id, p := range out {
		titleHits := ScanMachineTalk(p.Title)
		if len(titleHits) > 0 {
			p.Unpolished = true
			runlog.Warnf("事实标题未过文风校验,按原文保留: 任务 %s 事实 %s(%s)",
				l.TaskID, id, strings.Join(talkHitLines(titleHits), ";"))
		}
		if hits := ScanMachineTalk(p.Statement); len(hits) > 0 {
			runlog.Warnf("事实描述未过文风校验,该字段按原文呈现: 任务 %s 事实 %s(%s)",
				l.TaskID, id, strings.Join(talkHitLines(hits), ";"))
			p.Statement = ""
		}
		out[id] = p
	}
	return out
}

// parseFactTexts 宽容解析事实文本回复(未知名条目丢弃,字段按字符截断)。
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
			Title:  clipRunes(f.Title, 80), Statement: clipRunes(f.Statement, summaryFactClip),
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
	b.WriteString(talkRewriteReferralNote + titleShortStandard + "\n")
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

// factTextProofreadAsk 事实文本的校对续问。
const factTextProofreadAsk = `校对改写你刚写的全部事实文本:只改文风,事实一字不动(目标、路径、数字、凭据有无全照抄);标题仍是短句,描述仍是一句话本质+影响对象,fact_id 与上一轮一致。
` + titleShortStandard + `
只输出 JSON:{"facts":[{"fact_id":"…","title":"…","statement":"…"},…]},覆盖全部条目。`

// ---- ④⑥⑧ 假设文本(收割评估批次) ----

// hypoTextSystem 假设文本生成的系统段:标题短句、依据与评估理由报告
// 级口径。
const hypoTextSystem = `为渗透测试的待验证猜测改写三段文本,写给不懂技术的读者也能读懂。输入是每条猜测的陈述原文、依据原文与主宰本次评估的理由原文。任务:

1. title(标题):按标题标准写一句短句。
2. basis(依据):把依据改写为通顺的话,事实一字不动;依据为空则留空。
3. reason(评估理由):把评估理由改写为通顺的话,裁决含义不变(转验证/挂账/废弃各是各),事实一字不动;理由为空则留空。

规则:
1. 只改文风,不编造原文没有的信息;目标、数字、结论全照抄。
2. 平实书面白话;短句;无 emoji;不写工具名与实现细节。
3. hypothesis_id 原样返回。

只输出 JSON:{"hypotheses":[{"hypothesis_id":"…","title":"…","basis":"…","reason":"…"},…]},hypotheses 只含输入里给出的条目。`

// hypoTextMaterial 单条假设素材。
type hypoTextMaterial struct {
	HypothesisID string `json:"hypothesis_id"`
	Statement    string `json:"statement"`
	Basis        string `json:"basis,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// hypoTextReply 模型回复契约。
type hypoTextReply struct {
	Hypotheses []rings.HypoTextPolish `json:"hypotheses"`
}

// polishHypoTexts 假设文本生成(P4-64 ④⑥⑧:收割评估批次同批,只覆盖
// 本次被评估的假设):标题/依据/评估理由一次会话生成→校对→闸门;未
// 评估(等待验证瞬时态)不生成,读侧保持原文。返回 hypothesis_id →
// 包装(随评估记录同批落账)。
func (l *Loop) polishHypoTexts(ctx context.Context, plans []hypoEvalPlan) map[string]rings.HypoTextPolish {
	out := map[string]rings.HypoTextPolish{}
	if l.Provider == nil || len(plans) == 0 {
		return out
	}
	materials := make([]hypoTextMaterial, 0, len(plans))
	for _, p := range plans {
		h, err := l.Store.Node(ctx, l.TaskID, p.HypothesisID)
		if err != nil || h.Type != rings.NodeHypothesis {
			continue
		}
		m := hypoTextMaterial{HypothesisID: p.HypothesisID, Statement: h.Title, Reason: strings.TrimSpace(p.Reason)}
		m.Basis, _ = h.Detail["basis"].(string)
		materials = append(materials, m)
	}
	if len(materials) == 0 {
		return out
	}
	gs := l.newGenSession(hypoTextSystem)
	defer gs.close()
	payload, err := json.Marshal(materials)
	if err != nil {
		return out
	}
	text, err := gs.ask(ctx, "猜测原文与评估理由:\n"+string(payload))
	if err != nil {
		runlog.Warnf("假设文本生成失败: 任务 %s 原因 %s(读侧按原文呈现)", l.TaskID, clipEval(err.Error()))
		return out
	}
	known := map[string]bool{}
	for _, m := range materials {
		known[m.HypothesisID] = true
	}
	parsed := parseHypoTexts(text, known)
	if len(parsed) == 0 {
		runlog.Warnf("假设文本解析失败: 任务 %s(读侧按原文呈现)", l.TaskID)
		return out
	}
	out = parsed
	if text, err := gs.ask(ctx, hypoTextProofreadAsk); err == nil {
		if reparsed := parseHypoTexts(text, known); len(reparsed) > 0 {
			out = reparsed
		}
	} else {
		runlog.Warnf("假设文本校对失败: 任务 %s 原因 %s(保留生成稿)", l.TaskID, clipEval(err.Error()))
	}
	// 闸门:标题命中 → 重写一次 → 仍命中如实标注;依据/理由命中 →
	// 重写一次 → 仍命中该字段不冻结(读侧退原文)。
	dirty := map[string][]talkHit{}
	for id, p := range out {
		for _, text := range []string{p.Title, p.Basis, p.Reason} {
			dirty[id] = append(dirty[id], ScanMachineTalk(text)...)
		}
	}
	if len(dirty) > 0 {
		if err := l.rewriteHypoTexts(ctx, gs, out, dirty); err != nil {
			runlog.Warnf("假设文本改写失败: 任务 %s 原因 %s(如实标注)", l.TaskID, clipEval(err.Error()))
		}
	}
	for id, p := range out {
		if hits := ScanMachineTalk(p.Title); len(hits) > 0 {
			p.Unpolished = true
			runlog.Warnf("假设标题未过文风校验,按原文保留: 任务 %s 假设 %s(%s)",
				l.TaskID, id, strings.Join(talkHitLines(hits), ";"))
		}
		if hits := ScanMachineTalk(p.Basis); len(hits) > 0 {
			p.Basis = ""
		}
		if hits := ScanMachineTalk(p.Reason); len(hits) > 0 {
			p.Reason = ""
		}
		out[id] = p
	}
	return out
}

// parseHypoTexts 宽容解析假设文本回复。
func parseHypoTexts(text string, known map[string]bool) map[string]rings.HypoTextPolish {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r hypoTextReply
	if json.Unmarshal([]byte(body), &r) != nil {
		return nil
	}
	out := map[string]rings.HypoTextPolish{}
	for _, h := range r.Hypotheses {
		if !known[h.HypothesisID] {
			continue
		}
		p := rings.HypoTextPolish{
			HypothesisID: h.HypothesisID,
			Title:    clipRunes(h.Title, 80),
			Basis:    clipRunes(h.Basis, summaryFactClip),
			Reason:   clipRunes(h.Reason, summaryFactClip),
		}
		if p.Title != "" || p.Basis != "" || p.Reason != "" {
			out[h.HypothesisID] = p
		}
	}
	return out
}

// rewriteHypoTexts 闸门退回重写(命中条目与特征喂回同会话)。
func (l *Loop) rewriteHypoTexts(ctx context.Context, gs *genSession, out map[string]rings.HypoTextPolish, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你写的猜测文本里有机器语,逐处改写掉,事实一字不动:\n")
	for id, hits := range dirty {
		p := out[id]
		fmt.Fprintf(&b, "猜测 %s(标题:%s / 依据:%s / 理由:%s):\n",
			id, clipLine(p.Title, 40), clipLine(p.Basis, 60), clipLine(p.Reason, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + titleShortStandard + "\n")
	b.WriteString(`只输出 JSON:{"hypotheses":[{"hypothesis_id":"…","title":"…","basis":"…","reason":"…"},…]},只含上面点名的条目。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for id := range dirty {
		known[id] = true
	}
	for id, p := range parseHypoTexts(text, known) {
		out[id] = p
	}
	return nil
}

// hypoTextProofreadAsk 假设文本的校对续问。
const hypoTextProofreadAsk = `校对改写你刚写的全部猜测文本:只改文风,事实一字不动(目标、数字、结论全照抄);标题仍是短句,裁决含义不变,hypothesis_id 与上一轮一致。
` + titleShortStandard + `
只输出 JSON:{"hypotheses":[{"hypothesis_id":"…","title":"…","basis":"…","reason":"…"},…]},覆盖全部条目。`

// ---- ⑦ 复测结论 note ----

// retestNoteSystem 复测备注校对的系统段。
const retestNoteSystem = `校对改写渗透测试复测的备注:只改文风,事实一字不动(目标、路径、数字、状态码全照抄);写成一句或几句通顺的话,说清这轮复测看到了什么;平实书面白话,无 emoji,不写工具名与实现细节;提到目标一律用完整标识(域名或 IP 起头,host:port 形态)。
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
	if hits := ScanMachineTalk(out); len(hits) > 0 {
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
		if hits := ScanMachineTalk(out); len(hits) > 0 {
			runlog.Warnf("复测备注未过文风校验,按原文保留: 任务 %s 判断 %s 第 %d 轮(%s)",
				l.TaskID, claimID, round, strings.Join(talkHitLines(hits), ";"))
			return nil
		}
	}
	if out == "" {
		return nil
	}
	return &rings.RetestNotePolish{ClaimID: claimID, Round: round, Note: clipRunes(out, summaryFactClip)}
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
