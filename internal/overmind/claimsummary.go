package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// P4-59 ①(问题清单 #80①③):声明一句话摘要与事实描述入定稿校对通道。
// 确认定稿时对将上面板的声明文本(存在性描述/影响面/链上事实描述)按
// 报告级文风锚生成改写:描述=一句话本质+影响对象(复杂类加"就是…从而…"
// 通俗原理句);危害=直接说能拿到什么/能干什么+数字精确实际值(禁
// "大量/部分");事实描述=同一描述口径逐条改写。与来龙去脉(P4-13)/
// 复现描述(P4-08 ④)/反证时间线人话(P4-56 ⑤)同批同规矩:
//   - 随确认裁决同批冻结入 repro 记录(Statement/Impact/Facts),重开
//     再确认随新定稿重新生成(取代语义,投影侧随 repro 刷新);
//   - 只改文风,节点原文一字不动——机器判定与审计仍读节点(R2 裁决与
//     包装分离;包装不是证据,与机器字段矛盾以原文为准);
//   - 报告导出与面板渲染零 LLM(R17 不破);
//   - 生成/校对失败回退=空字段(读侧退节点原文,即原有呈现,不阻塞);
//   - 闸门链同款:生成 → 同会话校对 → 机器语特征扫描 → 命中字段退回
//     重写一次 → 仍命中的字段不冻结(读侧退原文,无需标注——原文本就
//     在位且受生成时约束)。成本有界:至多 3 次请求。

// summaryFactCap 喂给模型的事实条数上限(链上事实 + 发现步骤产出事实;
// 组合链环节与同步骤多观察并存时取账本次序前 N 条)。
const summaryFactCap = 8

// summaryPack 声明摘要的事实包(全部读账本确定性装配,零编造)。
type summaryPack struct {
	Status    string                `json:"status"`
	Statement string                `json:"statement"`
	Impact    string                `json:"impact,omitempty"`
	Facts     []summaryFactMaterial `json:"facts,omitempty"`
}

// summaryFactMaterial 单条事实素材:节点原文陈述(模型只改文风)。
type summaryFactMaterial struct {
	FactID    string `json:"fact_id"`
	Statement string `json:"statement"`
}

// summaryReply 模型回复契约。
type summaryReply struct {
	Title     string                `json:"title"`
	Statement string                `json:"statement"`
	Impact    string                `json:"impact"`
	Facts     []rings.FactStatement `json:"facts"`
}

// genClaimSummary 定稿声明摘要生成(P4-59 ①;P4-64 ② 起与立账批次
// 共用 genClaimSummaryText 全链):确认定稿时把校对稿冻结进 repro 记录
// (Statement/Impact/Facts),重开再确认随新定稿重新生成(取代语义)。
func (l *Loop) genClaimSummary(ctx context.Context, claimID string, rec *rings.TestClaimRecord) {
	s := l.genClaimSummaryText(ctx, claimID)
	rec.Title, rec.Statement, rec.Impact, rec.Facts = s.Title, s.Statement, s.Impact, s.Facts
}

// summaryPack 事实包装配(只读账本):声明两维原文 + 链上事实(组合声明
// derived_from → Fact)+ 发现步骤产出的事实(普通声明血缘指向步骤,
// 其产出事实即本发现的底层事实;produces 边账本次序)。
func (l *Loop) summaryPack(ctx context.Context, claimID string) (*summaryPack, error) {
	claim, err := l.Store.Node(ctx, l.TaskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("查判断: %w", err)
	}
	derived, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDerivedFrom)
	if err != nil {
		return nil, fmt.Errorf("查血缘: %w", err)
	}
	pack := &summaryPack{Status: claim.Status, Statement: claim.Title}
	pack.Impact, _ = claim.Detail["impact"].(string)
	submissions, err := l.Store.EventsByKinds(ctx, l.TaskID, rings.EventTestClaimRecord)
	if err != nil {
		return nil, err
	}
	for _, event := range submissions {
		var sub rings.TestClaimRecord
		if json.Unmarshal(event.Payload, &sub) == nil && sub.ClaimID == claimID && sub.Type == rings.TestClaimRecSubmit {
			pack.Statement, pack.Impact = sub.Summary, sub.ImpactNote
		}
	}
	seen := map[string]bool{}
	addFact := func(id string) {
		if seen[id] {
			return
		}
		n, err := l.Store.Node(ctx, l.TaskID, id)
		if err != nil || n.Type != rings.NodeFact || n.Status != "confirmed" {
			return
		}
		seen[id] = true
		pack.Facts = append(pack.Facts, summaryFactMaterial{FactID: id, Statement: n.Title})
	}
	// Explicit attribution wins over incidental observations from the original
	// discovery step. Do not truncate later evidence behind that old context.
	sources, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeSourcedFrom)
	if err != nil {
		return nil, err
	}
	for _, edge := range sources {
		if edge.From == claimID {
			addFact(edge.To)
		}
	}
	// 血缘目标分流:Fact 直入(组合链);Step 收集后走产出边(普通声明)。
	var steps []string
	for _, e := range derived {
		if e.From != claimID {
			continue
		}
		n, err := l.Store.Node(ctx, l.TaskID, e.To)
		if err != nil {
			continue
		}
		switch n.Type {
		case rings.NodeFact:
			addFact(e.To)
		case rings.NodeStep:
			steps = append(steps, e.To)
		}
	}
	if len(steps) > 0 {
		evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("查事件流: %w", err)
		}
		inStep := map[string]bool{}
		for _, s := range steps {
			inStep[s] = true
		}
		for _, ev := range evs {
			if len(pack.Facts) >= summaryFactCap {
				break
			}
			if ev.Kind != rings.EventEdgeAdded {
				continue
			}
			var e rings.EdgeAdded
			if json.Unmarshal(ev.Payload, &e) != nil || e.EdgeType != rings.EdgeProduces || !inStep[e.FromNode] {
				continue
			}
			addFact(e.ToNode)
		}
	}
	owners := map[string]bool{claimID: true}
	for _, step := range steps {
		owners[step] = true
	}
	for _, edge := range sources {
		if len(pack.Facts) < summaryFactCap && owners[edge.From] {
			addFact(edge.To)
		}
	}
	return pack, nil
}

// summaryAskHead 生成续问:事实包 JSON + 输出契约提示。
func summaryAskHead(pack *summaryPack) string {
	payload, err := json.Marshal(pack)
	if err != nil {
		payload = []byte("{}")
	}
	return "判断原文与相关事实:\n" + string(payload)
}

// parseSummary 宽容解析模型回复:剥围栏、取首尾大括号主体;事实按素材
// ID 过滤(未知名条目丢弃),各字段截断;全空返回零值结构(调用方按
// 失败处理)。
func parseSummary(text string, pack *summaryPack) summaryReply {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	var r summaryReply
	if json.Unmarshal([]byte(body), &r) != nil {
		return summaryReply{}
	}
	known := map[string]bool{}
	for _, f := range pack.Facts {
		known[f.FactID] = true
	}
	out := summaryReply{
		Title:     strings.TrimSpace(r.Title),
		Statement: strings.TrimSpace(r.Statement),
		Impact:    strings.TrimSpace(r.Impact),
	}
	for _, f := range r.Facts {
		if !known[f.FactID] {
			continue
		}
		if s := strings.TrimSpace(f.Statement); s != "" {
			out.Facts = append(out.Facts, rings.FactStatement{FactID: f.FactID, Statement: s})
		}
	}
	return out
}

// applySummary 应用一稿(生成/校对/重写共用):非空字段覆盖,空字段保留
// 上一稿;返回是否有任何内容(全空 = 无效稿)。
func applySummary(rec *rings.TestClaimRecord, r summaryReply) bool {
	if r.Title != "" {
		rec.Title = r.Title
	}
	if r.Statement != "" {
		rec.Statement = r.Statement
	}
	if r.Impact != "" {
		rec.Impact = r.Impact
	}
	if len(r.Facts) > 0 {
		rec.Facts = r.Facts
	}
	return rec.Title != "" || rec.Statement != "" || rec.Impact != "" || len(rec.Facts) > 0
}

// polishSummary 校对+闸门链:同会话校对改写(失败保留生成稿);逐字段
// 特征扫描,命中 → 同会话重写一次 → 复扫,仍命中的字段不冻结(置空,
// 读侧退节点原文)——原文在位且受生成时约束,无需标注行。
func (l *Loop) polishSummary(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord, pack *summaryPack) {
	dirty := summaryHits(rec)
	if len(dirty) == 0 {
		return
	}
	if err := l.rewriteSummary(ctx, gs, rec, pack, dirty); err != nil {
		runlog.Warnf("判断摘要改写失败: 任务 %s 判断 %s 原因 %s(命中字段按原文呈现)",
			l.TaskID, claimID, clipEval(err.Error()))
	}
	for field, hits := range summaryHits(rec) {
		switch field {
		case "名称":
			rec.Title = ""
		case "存在性描述":
			rec.Statement = ""
		case "影响面":
			rec.Impact = ""
		default:
			for i, f := range rec.Facts {
				if "事实 "+f.FactID == field {
					rec.Facts = append(rec.Facts[:i], rec.Facts[i+1:]...)
					break
				}
			}
		}
		runlog.Warnf("判断摘要未过文风校验,该字段按原文呈现: 任务 %s 判断 %s %s(%s)",
			l.TaskID, claimID, field, strings.Join(talkHitLines(hits), ";"))
	}
}

// summaryHits 逐字段机器语特征扫描,返回字段名 → 命中清单(字段名即
// 改写点名与日志措辞;事实按"事实 <ID>"逐条)。
func summaryHits(rec *rings.TestClaimRecord) map[string][]talkHit {
	out := map[string][]talkHit{}
	if hits := scanDisplayText(rec.Title); len(hits) > 0 {
		out["名称"] = hits
	}
	if rec.Statement != "" {
		if hits := scanDisplayText(rec.Statement); len(hits) > 0 {
			out["存在性描述"] = hits
		}
	}
	if rec.Impact != "" {
		if hits := scanDisplayText(rec.Impact); len(hits) > 0 {
			out["影响面"] = hits
		}
	}
	for _, f := range rec.Facts {
		if hits := scanDisplayText(f.Statement); len(hits) > 0 {
			out["事实 "+f.FactID] = hits
		}
	}
	return out
}

// rewriteSummary 闸门退回重写:命中字段与特征喂回同会话,重写后再复扫
// (仍命中的字段由调用方置空)。
func (l *Loop) rewriteSummary(ctx context.Context, gs *genSession, rec *rings.TestClaimRecord, pack *summaryPack, dirty map[string][]talkHit) error {
	var b strings.Builder
	b.WriteString("你写的判断文本里有机器语,逐处改写掉,事实一字不动:\n")
	if hits, ok := dirty["名称"]; ok && rec.Title != "" {
		fmt.Fprintf(&b, "名称(原文:%s):\n", rec.Title)
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	if hits, ok := dirty["存在性描述"]; ok && rec.Statement != "" {
		fmt.Fprintf(&b, "存在性描述(原文:%s):\n", clipLine(rec.Statement, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	if hits, ok := dirty["影响面"]; ok && rec.Impact != "" {
		fmt.Fprintf(&b, "影响面(原文:%s):\n", clipLine(rec.Impact, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	for _, f := range rec.Facts {
		hits, ok := dirty["事实 "+f.FactID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "事实 %s(原文:%s):\n", f.FactID, clipLine(f.Statement, 60))
		for _, line := range talkHitLines(hits) {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString(talkRewriteReferralNote + displayExplanationStandard + "\n")
	b.WriteString(`只输出 JSON:{"title":"…","statement":"…","impact":"…","facts":[{"fact_id":"…","statement":"…"},…]},只含上面点名的字段与条目。`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	applySummary(rec, parseSummary(text, pack))
	return nil
}

// summarySystem 声明摘要生成的系统段(代码固定;稳定短段利于前缀缓存)。
// 文风即报告级锚的描述/危害口径(P4-59 ①,问题清单 #80①③)。
const summarySystem = `为安全测试面板整理漏洞的名称、说明和影响。名称是一小句，供列表辨认；说明让人直接看懂实际问题；影响只写证据支持的后果与范围。资产在页面单列，正文不重复堆放完整地址、状态码和参数，详细请求与响应由证据和复现提供。
` + displayExplanationStandard + `
输入中的当前状态决定措辞：尚未确认时不得写成漏洞已成立；证据不足须保留未验证的边界。不能改变级别、确认状态或报告纳入决定，不补造影响。事实条目标识原样返回。
原交卷与后来确认的事实一起构成当前材料；新事实已证实的内容应更新原说明和影响，不再沿用“尚未验证”的旧措辞。不把补充证据另写成一个漏洞，不把其他根因的影响混进来。
只输出 JSON:{"title":"…","statement":"…","impact":"…","facts":[{"fact_id":"…","statement":"…"},…]}。`
