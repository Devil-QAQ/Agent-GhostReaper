package overmind

// P4-65 ⑨⑩(问题清单 #82 A):界面层包装族——研判记录与拆解派发落账后
// 异步补写校对稿(不阻塞研判轮与派发)。循 P4-64 先例:原文一字不动
// (机器记录/任务包/审计全走原文),校对稿独立事件冻结、读侧优先链退
// 原文;生成失败/仍含机器语该字段不冻结(读侧退原文,如实不阻塞);
// 零 Provider 零外呼;存量旧账本不回填。
//
//   - ⑨ 研判叙述与五问行:overmind.record(assessment)落账后异步补写
//     overmind.record_polish(RecRev 锚定该研判记录的事件版本,研判记录
//     不可变故无取代语义);会话原文下钻走原文域。
//   - ⑩ 意图/步骤标题:拆解派发落账后异步补写 step.title(node_polish
//     投影 title 列);任务包与机器消费用节点原文,校对稿仅显示层(与
//     声明摘要同款包装分离)。
//
// 异步用脱离取消的 ctx 加超时(收尾终态后补写仍可完成,不随任务熔断
// 断链——blackbox 同哲学;读侧优先链保证半途失败只是退原文,无一致性
// 风险)。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// polishAsyncTimeout 单个异步补写批次的墙钟上限(生成+校对+至多一次
// 重写 ≈ 3 次请求的余量;到线放弃,读侧退原文)。
const polishAsyncTimeout = 5 * time.Minute

// ---- ⑨ 研判叙述与五问行 ----

// assessPolishSystem 研判叙述校对的系统段:五问逐问结论与五问之外研判
// 的显示层改写口径(叙述锚,与四段锚/短句锚并列的第三形态——研判结论
// 是"依据图上事实的一句话判断+理由",不套报告四段)。
const assessPolishSystem = `把主宰本次规划整理成操作者能直接看懂的说明。各字段先说判断，再补一条必要依据；直接写完整短句，不加“判断／依据”等标签；没有新内容就保持原本的“无新入口／无新权限”等原意，不再补一句同义解释，不补造发现。名称、目标与证据由各自记录提供，正文不逐项复述内部编号或执行参数。规划方向、决定与尚未验证的边界必须保持准确。
` + displayExplanationStandard + `
只输出 JSON:{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"}，输入为空的字段保持空。`

// polishAssessmentAsync 研判记录落账后异步补写校对稿(P4-65 ⑨):研判
// 轮与派发不等校对;失败/仍含机器语的字段不冻结(读侧退研判记录原文)。
func (l *Loop) polishAssessmentAsync(parent context.Context, recRev int64, rec rings.OvermindRecord) {
	if l.Provider == nil || recRev <= 0 {
		return
	}
	l.queueDisplay(parent, []string{displayKey("assessment", "", recRev)}, func(ctx context.Context, _ map[string]bool) { l.polishAssessment(ctx, recRev, rec) })
}

// assessPolishMaterial 研判叙述素材(仅非空字段进素材)。
type assessPolishMaterial struct {
	QAccess      string `json:"q_access,omitempty"`
	QPrivilege   string `json:"q_privilege,omitempty"`
	QCombination string `json:"q_combination,omitempty"`
	QUnlock      string `json:"q_unlock,omitempty"`
	QOverturn    string `json:"q_overturn,omitempty"`
	Note         string `json:"note,omitempty"`
}

// assessFields 研判六字段的键清单(素材装配/扫描/冻结共用,防六处字段
// 名散写漂移;取值与赋值见 assessFieldOf/assessFieldSet)。
var assessFields = []string{"q_access", "q_privilege", "q_combination", "q_unlock", "q_overturn", "note"}

// materialOf 研判记录 → 素材(只收非空字段;全空不发起——研判记录
// validateOvermindRecord 已保证至少一项非空,防御性再判)。
func materialOf(rec rings.OvermindRecord) assessPolishMaterial {
	return assessPolishMaterial{
		QAccess:      strings.TrimSpace(rec.QAccess),
		QPrivilege:   strings.TrimSpace(rec.QPrivilege),
		QCombination: strings.TrimSpace(rec.QCombination),
		QUnlock:      strings.TrimSpace(rec.QUnlock),
		QOverturn:    strings.TrimSpace(rec.QOverturn),
		Note:         strings.TrimSpace(rec.Note),
	}
}

func (m assessPolishMaterial) empty() bool {
	return m.QAccess == "" && m.QPrivilege == "" && m.QCombination == "" &&
		m.QUnlock == "" && m.QOverturn == "" && m.Note == ""
}

// assessPolishReply 模型回复契约。
type assessPolishReply struct {
	QAccess      string `json:"q_access"`
	QPrivilege   string `json:"q_privilege"`
	QCombination string `json:"q_combination"`
	QUnlock      string `json:"q_unlock"`
	QOverturn    string `json:"q_overturn"`
	Note         string `json:"note"`
}

// polishAssessment 同步全链:生成 → 校对续问 → 逐字段闸门 → 退回重写
// 一次 → 仍命中的字段置空不冻结 → 非空即落 overmind.record_polish。
func (l *Loop) polishAssessment(ctx context.Context, recRev int64, rec rings.OvermindRecord) {
	m := materialOf(rec)
	if m.empty() {
		return
	}
	gs := l.newGenSession(assessPolishSystem)
	defer gs.close()
	payload, err := json.Marshal(m)
	if err != nil {
		return
	}
	text, err := gs.ask(ctx, "规划记录原文:\n"+string(payload))
	if err != nil {
		runlog.Warnf("规划叙述校对生成失败: 任务 %s 记录版本 %d 原因 %s(读侧按原文呈现)",
			l.TaskID, recRev, clipEval(err.Error()))
		return
	}
	out := parseAssessPolish(text)
	if out == nil {
		runlog.Warnf("规划叙述校对解析失败: 任务 %s 记录版本 %d(读侧按原文呈现)", l.TaskID, recRev)
		return
	}
	// 闸门:逐字段扫;命中 → 同会话重写一次 → 仍命中的字段置空不冻结。
	if dirty := assessPolishHits(out); len(dirty) > 0 {
		if err := l.rewriteAssessPolish(ctx, gs, out, dirty); err != nil {
			runlog.Warnf("规划叙述改写失败: 任务 %s 记录版本 %d 原因 %s(命中的字段按原文呈现)",
				l.TaskID, recRev, clipEval(err.Error()))
		}
	}
	pol := rings.OvermindPolish{RecRev: recRev}
	for _, key := range assessFields {
		v := assessFieldOf(out, key)
		if v == "" {
			continue
		}
		if hits := scanDisplayText(v); len(hits) > 0 {
			runlog.Warnf("规划叙述未过文风校验,该字段按原文呈现: 任务 %s 记录版本 %d 字段 %s(%s)",
				l.TaskID, recRev, key, strings.Join(talkHitLines(hits), ";"))
			continue
		}
		setPolishField(&pol, key, v)
	}
	if pol.QAccess == "" && pol.QPrivilege == "" && pol.QCombination == "" &&
		pol.QUnlock == "" && pol.QOverturn == "" && pol.Note == "" {
		return // 全部字段未过闸:不落空事件(物理校验拒空的生成侧保证)
	}
	if _, err := l.Store.AppendDisplay(ctx, l.TaskID, rings.Event{
		Kind: rings.EventOvermindPolish, Actor: ActorOvermind, Payload: pol,
	}); err != nil {
		runlog.Warnf("规划叙述校对稿写入失败: 任务 %s 记录版本 %d 原因 %s(读侧按原文呈现)",
			l.TaskID, recRev, clipEval(err.Error()))
	}
}

// assessPolishHits 逐字段机器语扫描(键 → 命中)。
func assessPolishHits(r *assessPolishReply) map[string][]talkHit {
	dirty := map[string][]talkHit{}
	for _, key := range assessFields {
		if v := assessFieldOf(r, key); v != "" {
			if hits := scanDisplayText(v); len(hits) > 0 {
				dirty[key] = hits
			}
		}
	}
	return dirty
}

// rewriteAssessPolish 闸门退回重写(命中字段与特征喂回同会话)。
func (l *Loop) rewriteAssessPolish(ctx context.Context, gs *genSession, out *assessPolishReply, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你改写的规划叙述里有机器语,逐处改写掉,事实一字不动:\n")
	for _, key := range assessFields {
		hits, ok := dirty[key]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "字段 %s(原文:%s):\n", key, clipLine(assessFieldOf(out, key), 80))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + `只输出 JSON:{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"},未点名字段原样返回。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	if re := parseAssessPolish(text); re != nil {
		for _, key := range assessFields {
			if _, ok := dirty[key]; ok {
				setReplyField(out, key, assessFieldOf(re, key))
			}
		}
	}
	return nil
}

// assessFieldOf 按键取完整显示说明，不裁断句子。
func assessFieldOf(r *assessPolishReply, key string) string {
	switch key {
	case "q_access":
		return strings.TrimSpace(r.QAccess)
	case "q_privilege":
		return strings.TrimSpace(r.QPrivilege)
	case "q_combination":
		return strings.TrimSpace(r.QCombination)
	case "q_unlock":
		return strings.TrimSpace(r.QUnlock)
	case "q_overturn":
		return strings.TrimSpace(r.QOverturn)
	case "note":
		return strings.TrimSpace(r.Note)
	}
	return ""
}

// setReplyField / setPolishField 六字段按键赋值(回复与冻结负载各一)。
func setReplyField(r *assessPolishReply, key, val string) {
	switch key {
	case "q_access":
		r.QAccess = val
	case "q_privilege":
		r.QPrivilege = val
	case "q_combination":
		r.QCombination = val
	case "q_unlock":
		r.QUnlock = val
	case "q_overturn":
		r.QOverturn = val
	case "note":
		r.Note = val
	}
}

func setPolishField(p *rings.OvermindPolish, key, val string) {
	switch key {
	case "q_access":
		p.QAccess = val
	case "q_privilege":
		p.QPrivilege = val
	case "q_combination":
		p.QCombination = val
	case "q_unlock":
		p.QUnlock = val
	case "q_overturn":
		p.QOverturn = val
	case "note":
		p.Note = val
	}
}

// parseAssessPolish 宽容解析研判校对回复。
func parseAssessPolish(text string) *assessPolishReply {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r assessPolishReply
	if json.Unmarshal([]byte(body), &r) != nil {
		return nil
	}
	return &r
}

// ---- ⑩ 意图/步骤标题 ----

const intentTitleStandard = `名称只概括这条意图要做的事，适合在列表中一眼辨认。说明讲清要检查什么、为什么检查；操作参数与命令留在完整意图，不重复罗列。资产地址由界面单列，名称不必塞入完整地址。意图只说明要做什么，不逐项列出完整的验证判据，也不预先讨论测试结果。详细方案留给完整意图。`

// 只约束真实性与名称/说明分工，让模型组织面向人的内容。
const stepTitleSystem = `为安全测试面板整理意图的名称和说明。读者要能迅速看懂这次准备做什么，不需要先读执行记录。
` + intentTitleStandard + `
` + displayExplanationStandard + `
只输出 JSON:{"steps":[{"step_id":"…","title":"…","statement":"…"},…]}，条目标识与输入一致。`

// stepTitleMaterial 单条意图标题素材。
type stepTitleMaterial struct {
	StepID    string `json:"step_id"`
	Intent    string `json:"intent"`
	SourceRev int64  `json:"-"`
}

// stepTitleReply 模型回复契约与单条包装。
type stepTitleReply struct {
	Steps []rings.StepTitlePolish `json:"steps"`
}

// polishStepTitlesAsync 拆解派发落账后异步补写意图标题(P4-65 ⑩):派发
// 不等校对;一批一会话(生成→校对→闸门→重写一次→仍脏该条不冻结)。
func (l *Loop) polishStepTitlesAsync(parent context.Context, plans []stepTitleMaterial) {
	if l.Provider == nil || len(plans) == 0 {
		return
	}
	sources, err := l.Store.NodeTextSourceRevs(parent, l.TaskID)
	if err != nil {
		l.setErr(err)
		return
	}
	l.queueStepTitles(parent, plans, sources)
}

func (l *Loop) queueStepTitles(parent context.Context, plans []stepTitleMaterial, sources map[string]int64) {
	var keys []string
	plans = append([]stepTitleMaterial(nil), plans...)
	for i := range plans {
		plans[i].SourceRev = sources[plans[i].StepID]
		if n, err := l.Store.Node(parent, l.TaskID, plans[i].StepID); err == nil {
			plans[i].Intent = n.Title
		}
		keys = append(keys, displayKey("step", plans[i].StepID, plans[i].SourceRev))
	}
	l.queueDisplay(parent, keys, func(ctx context.Context, accepted map[string]bool) {
		var selected []stepTitleMaterial
		for _, plan := range plans {
			if accepted[displayKey("step", plan.StepID, plan.SourceRev)] {
				selected = append(selected, plan)
			}
		}
		l.polishStepTitles(ctx, selected)
	})
}

// polishStepTitles 同步全链:生成 → 校对续问 → 闸门 → 重写一次 → 仍
// 命中的条目不冻结 → 通过的条目同批落 step.title 事件。
func (l *Loop) polishStepTitles(ctx context.Context, plans []stepTitleMaterial) {
	gs := l.newGenSession(stepTitleSystem)
	defer gs.close()
	payload, err := json.Marshal(plans)
	if err != nil {
		return
	}
	text, err := gs.ask(ctx, "意图原文:\n"+string(payload))
	if err != nil {
		runlog.Warnf("意图标题校对生成失败: 任务 %s %d 条 原因 %s(读侧按原文呈现)",
			l.TaskID, len(plans), clipEval(err.Error()))
		return
	}
	known := map[string]bool{}
	for _, p := range plans {
		known[p.StepID] = true
	}
	out := parseStepTitles(text, known)
	if len(out) == 0 {
		runlog.Warnf("意图标题校对解析失败: 任务 %s %d 条(读侧按原文呈现)", l.TaskID, len(plans))
		return
	}
	// 闸门:命中 → 同会话重写一次 → 仍命中的条目不冻结。
	dirty := map[string][]talkHit{}
	for id, t := range out {
		if hits := scanDisplayText(t.Title + "\n" + t.Statement); len(hits) > 0 {
			dirty[id] = hits
		}
	}
	if len(dirty) > 0 {
		if err := l.rewriteStepTitles(ctx, gs, out, dirty); err != nil {
			runlog.Warnf("意图标题改写失败: 任务 %s 原因 %s(命中条目按原文呈现)", l.TaskID, clipEval(err.Error()))
		}
	}
	events := make([]rings.Event, 0, len(out))
	for _, p := range plans {
		t, ok := out[p.StepID]
		if !ok || t.Title == "" {
			continue // 未生成或未过闸:读侧退节点标题
		}
		if hits := scanDisplayText(t.Title + "\n" + t.Statement); len(hits) > 0 {
			runlog.Warnf("意图标题未过文风校验,按原文呈现: 任务 %s 步骤 %s(%s)",
				l.TaskID, p.StepID, strings.Join(talkHitLines(hits), ";"))
			continue
		}
		t.SourceRev = p.SourceRev
		events = append(events, rings.Event{
			Kind: rings.EventStepTitle, Actor: ActorOvermind,
			Payload: t,
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

// parseStepTitles 丢弃未知条目，保留完整显示稿；旧版仅标题回复兼容。
func parseStepTitles(text string, known map[string]bool) map[string]rings.StepTitlePolish {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r stepTitleReply
	if json.Unmarshal([]byte(body), &r) != nil {
		return nil
	}
	out := map[string]rings.StepTitlePolish{}
	for _, item := range r.Steps {
		if !known[item.StepID] {
			continue
		}
		item.Title = strings.TrimSpace(item.Title)
		item.Statement = strings.TrimSpace(item.Statement)
		if item.Title != "" {
			out[item.StepID] = item
		}
	}
	return out
}

// rewriteStepTitles 闸门退回重写(命中条目与特征喂回同会话)。
func (l *Loop) rewriteStepTitles(ctx context.Context, gs *genSession, out map[string]rings.StepTitlePolish, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你改写的意图标题里有机器语,逐处改写掉,事实一字不动:\n")
	for id, hits := range dirty {
		fmt.Fprintf(&b, "步骤 %s(标题:%s):\n", id, clipLine(out[id].Title+" / "+out[id].Statement, 120))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + intentTitleStandard + "\n" + displayExplanationStandard + "\n")
	b.WriteString(`只输出 JSON:{"steps":[{"step_id":"…","title":"…","statement":"…"},…]},只含上面点名的条目。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for id := range dirty {
		known[id] = true
	}
	for id, t := range parseStepTitles(text, known) {
		out[id] = t
	}
	return nil
}
