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

// assessPolishClip 研判单字段校对稿的落账上限(五问各一两句的量级)。
const assessPolishClip = 240

// detachCtx 脱离取消的派生 ctx(保留值、自带上限)。
func detachCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), polishAsyncTimeout)
}

// WaitPolish 等待在途异步补写收口(单测直接调 cycle 的收拢锚点;生产
// 路径由 Run 退出时的 polishWG.Wait 兜住)。
func (l *Loop) WaitPolish() { l.polishWG.Wait() }

// ---- ⑨ 研判叙述与五问行 ----

// assessPolishSystem 研判叙述校对的系统段:五问逐问结论与五问之外研判
// 的显示层改写口径(叙述锚,与四段锚/短句锚并列的第三形态——研判结论
// 是"依据图上事实的一句话判断+理由",不套报告四段)。
const assessPolishSystem = `校对改写主宰规划记录的叙述文本,写给不懂技术的读者也能读懂。输入是规划记录原文(五个问题逐问的结论与问题之外的规划结论)。任务:逐字段改写,只改文风,规划结论一字不动——事实、目标、数字、资产编号、状态、"无"这样的表态全照抄,不编造原文没有的信息,不改变任何判断。
写法:平实书面白话,短句;先说结论再说理由;数字写精确的实际值(3 台、第 2 轮),不写"多个/大量";提到目标一律用完整标识(域名或 IP 起头、host:port 形态,如 jkysd.cn:9902),不用裸端口、裸路径指代目标;无 emoji;不写工具名与实现细节。
正例:"10.0.0.5:8080 的管理接口凭 s2 拿到的口令可以登录,原判断"仅外网可达"不再成立;下一步验证该口令在 10.0.0.6 上的同一套后台是否通用。"
反例:"经过综合评估,新权限或将对既有漏洞的威胁态势产生显著影响,建议持续关注"(无结论、无数字、空话)。
禁这些机器语:内部编号解释、裸版本号数字、程序报错原文、英文状态词、无动词的压缩名词堆。
只输出 JSON:{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"},输入为空的字段输出空字符串。`

// polishAssessmentAsync 研判记录落账后异步补写校对稿(P4-65 ⑨):研判
// 轮与派发不等校对;失败/仍含机器语的字段不冻结(读侧退研判记录原文)。
func (l *Loop) polishAssessmentAsync(parent context.Context, recRev int64, rec rings.OvermindRecord) {
	if l.Provider == nil || recRev <= 0 {
		return
	}
	ctx, cancel := detachCtx(parent)
	l.polishWG.Add(1)
	go func() {
		defer l.polishWG.Done()
		defer cancel()
		l.polishAssessment(ctx, recRev, rec)
	}()
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
	// 校对续问(同会话,模型可见自己刚写的全部字段)。
	if text, err := gs.ask(ctx, assessPolishProofreadAsk); err == nil {
		if re := parseAssessPolish(text); re != nil {
			out = re
		}
	} else {
		runlog.Warnf("规划叙述校对失败: 任务 %s 记录版本 %d 原因 %s(保留生成稿)",
			l.TaskID, recRev, clipEval(err.Error()))
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
		if hits := ScanMachineTalk(v); len(hits) > 0 {
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
	if _, err := l.Store.Append(ctx, l.TaskID, rings.Event{
		Kind: rings.EventOvermindPolish, Actor: ActorOvermind, Payload: pol,
	}); err != nil {
		runlog.Warnf("规划叙述校对稿写入失败: 任务 %s 记录版本 %d 原因 %s(读侧按原文呈现)",
			l.TaskID, recRev, clipEval(err.Error()))
	}
}

// assessPolishProofreadAsk 校对续问。
const assessPolishProofreadAsk = `校对改写你刚写的全部规划叙述:只改文风,规划结论一字不动(事实、目标、数字、资产编号、状态、"无"全照抄);仍是先结论后理由的平实短句,空字段保持空。
只输出 JSON:{"q_access":"…","q_privilege":"…","q_combination":"…","q_unlock":"…","q_overturn":"…","note":"…"},覆盖全部字段。`

// assessPolishHits 逐字段机器语扫描(键 → 命中)。
func assessPolishHits(r *assessPolishReply) map[string][]talkHit {
	dirty := map[string][]talkHit{}
	for _, key := range assessFields {
		if v := assessFieldOf(r, key); v != "" {
			if hits := ScanMachineTalk(v); len(hits) > 0 {
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

// assessFieldOf 回复字段按键取值(统一截断 assessPolishClip)。
func assessFieldOf(r *assessPolishReply, key string) string {
	switch key {
	case "q_access":
		return clipRunes(strings.TrimSpace(r.QAccess), assessPolishClip)
	case "q_privilege":
		return clipRunes(strings.TrimSpace(r.QPrivilege), assessPolishClip)
	case "q_combination":
		return clipRunes(strings.TrimSpace(r.QCombination), assessPolishClip)
	case "q_unlock":
		return clipRunes(strings.TrimSpace(r.QUnlock), assessPolishClip)
	case "q_overturn":
		return clipRunes(strings.TrimSpace(r.QOverturn), assessPolishClip)
	case "note":
		return clipRunes(strings.TrimSpace(r.Note), assessPolishClip)
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

// intentTitleStandard 意图标题校对标准(P4-65 ⑩):意图是动作短句(资产
// +手法+目标),与事实/假设标题的陈述短句(P4-64 ⑥ titleShortStandard)
// 同为短句族、句式各异——不套报告级四段锚。与拆解契约 StructuredFieldStyle
// (生成时收口)同口径:≤40 字、数字精确、完整标识。
const intentTitleStandard = `意图标题标准(动作短句,不超过 40 字):资产+手法+目标一句说清;主语用完整标识(域名或 IP 起头、host:port 形态,如 jkysd.cn:9902),不用裸端口、裸路径;数字写实际值(20 个、第 2 轮);平实书面白话;无 emoji;不写工具名与实现细节;命令与参数留给执行,不进标题。
正例:"对 10.0.0.5:8080 做目录枚举,找未授权入口""用 s2 拿到的口令登录 10.0.0.6 后台,验证是否通用"。
反例:"针对目标 Web 应用开展全面深入的安全测试"(无定位、空话);"Get /backup 200 OK 高危漏洞探测"(机器语堆砌)。`

// stepTitleSystem 意图标题校对的系统段。
const stepTitleSystem = `校对改写渗透测试刚派发的意图标题,写给不懂技术的读者也能读懂。输入是每条意图的原文。任务:按意图标题的标准逐条改写——只改文风,意图内容一字不动(目标、路径、数字、动作全照抄),不编造原文没有的信息。
` + intentTitleStandard + `
只输出 JSON:{"steps":[{"step_id":"…","title":"…"},…]},steps 只含输入里给出的条目。`

// stepTitleMaterial 单条意图标题素材。
type stepTitleMaterial struct {
	StepID string `json:"step_id"`
	Intent string `json:"intent"`
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
	ctx, cancel := detachCtx(parent)
	l.polishWG.Add(1)
	go func() {
		defer l.polishWG.Done()
		defer cancel()
		l.polishStepTitles(ctx, plans)
	}()
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
	// 校对续问(同会话,模型可见自己刚写的全部条目)。
	if text, err := gs.ask(ctx, stepTitleProofreadAsk); err == nil {
		if re := parseStepTitles(text, known); len(re) > 0 {
			out = re
		}
	} else {
		runlog.Warnf("意图标题校对失败: 任务 %s 原因 %s(保留生成稿)", l.TaskID, clipEval(err.Error()))
	}
	// 闸门:命中 → 同会话重写一次 → 仍命中的条目不冻结。
	dirty := map[string][]talkHit{}
	for id, t := range out {
		if hits := ScanMachineTalk(t); len(hits) > 0 {
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
		if !ok || t == "" {
			continue // 未生成或未过闸:读侧退节点标题
		}
		if hits := ScanMachineTalk(t); len(hits) > 0 {
			runlog.Warnf("意图标题未过文风校验,按原文呈现: 任务 %s 步骤 %s(%s)",
				l.TaskID, p.StepID, strings.Join(talkHitLines(hits), ";"))
			continue
		}
		events = append(events, rings.Event{
			Kind: rings.EventStepTitle, Actor: ActorOvermind,
			Payload: rings.StepTitlePolish{StepID: p.StepID, Title: t},
		})
	}
	if len(events) == 0 {
		return
	}
	if _, err := l.Store.Append(ctx, l.TaskID, events...); err != nil {
		runlog.Warnf("意图标题校对稿写入失败: 任务 %s %d 条 原因 %s(读侧按原文呈现)",
			l.TaskID, len(events), clipEval(err.Error()))
	}
}

// stepTitleProofreadAsk 意图标题的校对续问。
const stepTitleProofreadAsk = `校对改写你刚写的全部意图标题:只改文风,意图内容一字不动(目标、路径、数字、动作全照抄);仍是动作短句,step_id 与上一轮一致。
` + intentTitleStandard + `
只输出 JSON:{"steps":[{"step_id":"…","title":"…"},…]},覆盖全部条目。`

// parseStepTitles 宽容解析意图标题回复(未知条目丢弃,标题按字符截断)。
func parseStepTitles(text string, known map[string]bool) map[string]string {
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
	out := map[string]string{}
	for _, s := range r.Steps {
		if !known[s.StepID] {
			continue
		}
		if t := clipRunes(strings.TrimSpace(s.Title), 80); t != "" {
			out[s.StepID] = t
		}
	}
	return out
}

// rewriteStepTitles 闸门退回重写(命中条目与特征喂回同会话)。
func (l *Loop) rewriteStepTitles(ctx context.Context, gs *genSession, out map[string]string, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你改写的意图标题里有机器语,逐处改写掉,事实一字不动:\n")
	for id, hits := range dirty {
		fmt.Fprintf(&b, "步骤 %s(标题:%s):\n", id, clipLine(out[id], 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + intentTitleStandard + "\n")
	b.WriteString(`只输出 JSON:{"steps":[{"step_id":"…","title":"…"},…]},只含上面点名的条目。`)
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
