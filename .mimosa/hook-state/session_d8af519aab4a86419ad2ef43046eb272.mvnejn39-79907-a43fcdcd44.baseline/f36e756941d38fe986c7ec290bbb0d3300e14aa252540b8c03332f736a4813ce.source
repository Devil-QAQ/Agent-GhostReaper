package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// P4-13(问题清单 #23):成果"来龙去脉"人话叙述——确认定稿时由主宰
// 模型基于链上节点+证据原文写一段自然叙述:在哪发现的(来源载体:JS
// 文件/页面/接口响应等)、谁发现的、怎么验证、怎么确认。与复现描述
// (P4-08 ④)同批同规矩:随确认裁决同批冻结入 repro 记录;重开再确认
// 随新定稿重新生成;报告导出与面板渲染零 LLM(R17 不破);生成失败回退
// 确定性拼句,不阻塞机器裁决(R2:裁决与包装分离);叙述是包装不是
// 证据,与机器字段矛盾时以原文为准。

// narrativeClip 叙述的落账上限(超长截断;面板与报告同文)。
const narrativeClip = 600

// narrativeExchangeCap 发现步骤入包的目标流量上限(判定来源载体足够;
// 取账本序最后 N 条——发现证据通常出自步骤靠后的交换)。
const narrativeExchangeCap = 6

// narrativeBodyClip 交换正文截头(载体特征对账用,全文在 blob)。
const narrativeBodyClip = 160

// narrativePack 喂模型的链上事实包(全部读账本确定性装配,零编造)。
type narrativePack struct {
	Claim struct {
		Statement string `json:"statement"`
		Impact    string `json:"impact,omitempty"`
		Combined  bool   `json:"combined,omitempty"`
	} `json:"claim"`
	// Discoveries 发现环节:普通声明单环节(声明自身的发现血缘),组合
	// 声明逐链上事实一环节。
	Discoveries []narrativeDiscovery `json:"discoveries"`
	// Falsify 当前战役的反证轮(重开前旧战役不混计,与复现步骤同口径)。
	Falsify []narrativeRound `json:"falsify"`
	// Confirm 仲裁确认的逐条件依据(词表与界面一致)。
	Confirm []string `json:"confirm"`
}

// narrativeDiscovery 一个发现环节:谁(单位)在哪个步骤(意图)发现、
// 发现证据截头、锚定资产(含发现型出生)与该步骤的目标流量(来源载体)。
type narrativeDiscovery struct {
	FactTitle    string              `json:"fact_title,omitempty"` // 组合链环节的事实陈述(组合声明)
	StepID       string              `json:"step_id,omitempty"`
	Intent       string              `json:"intent,omitempty"`
	UnitID       string              `json:"unit_id,omitempty"`
	EvidenceHead string              `json:"evidence_head,omitempty"`
	Assets       []narrativeAsset    `json:"assets,omitempty"`
	Exchanges    []narrativeExchange `json:"exchanges,omitempty"`
}

// narrativeAsset 声明锚定的资产(发现型带出生单位,链路走到对象出生)。
type narrativeAsset struct {
	Title     string `json:"title"`
	Source    string `json:"source,omitempty"` // seed|manual|discovered
	BirthUnit string `json:"birth_unit,omitempty"`
}

// narrativeExchange 发现步骤的一次 http(s) 交换(来源载体的事实依据)。
type narrativeExchange struct {
	Method    string `json:"method,omitempty"`
	URL       string `json:"url,omitempty"`
	Status    int    `json:"status,omitempty"`
	RespType  string `json:"resp_type,omitempty"`
	RespBytes int64  `json:"resp_bytes,omitempty"`
	BodyHead  string `json:"body_head,omitempty"`
}

// narrativeRound 当前战役的一轮反证结论。
type narrativeRound struct {
	Round            int            `json:"round"`
	UnitID           string         `json:"unit_id"`
	ExistenceRefuted bool           `json:"existence_refuted,omitempty"`
	ImpactSupported  bool           `json:"impact_supported,omitempty"`
	Alternatives     []narrativeAlt `json:"alternatives,omitempty"`
}

type narrativeAlt struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Refuted bool   `json:"refuted"`
}

// narrativeCondWords 仲裁条件种类 → 界面词(术语对照表·分域扩充同词,
// 面板 COND_LABELS 同源不另造)。
var narrativeCondWords = map[string]string{
	"falsify_completed":     "反证完成",
	"existence_not_refuted": "存在性未推翻",
	"alternatives_killed":   "竞争解释全排除",
	"impact_supported":      "影响面有证据",
	"verifier_independent":  "验证者独立",
	"fact_confirmed":        "事实已确认",
	"tool_observed":         "工具已观测",
}

// genClaimNarrative 定稿来龙去脉叙述生成:组装链上事实包,先落确定性
// 拼句(同 P4-08 ④ 组装时先落确定性描述的规矩),模型成功则改写为人话
// 叙述并冻结;失败如实 WARN 回退拼句,不阻塞确认裁决。零 Provider 保留
// 拼句(单测与未配模型路径,零外呼)。
// P4-24:生成后接定稿校对+模式闸门(同会话,见 proofread.go)。
func (l *Loop) genClaimNarrative(ctx context.Context, claimID string, j *arbiter.ClaimJudgement, rec *rings.TestClaimRecord) {
	pack, err := l.narrativePack(ctx, claimID, j)
	if err != nil {
		runlog.Warnf("来龙去脉生成失败: 任务 %s 判断 %s 原因 %s(链上事实包装配失败,无叙述)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	rec.Narrative = deterministicNarrative(pack)
	if l.Provider == nil {
		return
	}
	gs := l.newGenSession(narrativeSystem)
	defer gs.close()
	text, err := l.askClaimNarrative(ctx, gs, pack)
	if err != nil {
		runlog.Warnf("来龙去脉生成失败: 任务 %s 判断 %s 原因 %s(回退确定性拼句)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	if t := strings.TrimSpace(text); t != "" {
		rec.Narrative = clipLine(t, narrativeClip)
	}
	l.polishNarrative(ctx, claimID, gs, rec)
}

// narrativePack 链上事实包装配(只读账本):声明两维与组合标记、发现
// 环节(普通 = 声明自身血缘;组合 = 链上各事实及其发现步骤)、当前战役
// 反证轮与竞争解释排除结论、仲裁确认条件。
func (l *Loop) narrativePack(ctx context.Context, claimID string, j *arbiter.ClaimJudgement) (*narrativePack, error) {
	claim, err := l.Store.Node(ctx, l.TaskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("查判断: %w", err)
	}
	derived, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeDerivedFrom)
	if err != nil {
		return nil, fmt.Errorf("查血缘: %w", err)
	}
	abouts, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("查资产锚点: %w", err)
	}
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("查事件流: %w", err)
	}
	assets := l.narrativeAssets(ctx, claimID, abouts)
	unit, _ := claim.Detail["unit_id"].(string)
	pack := &narrativePack{}
	pack.Claim.Statement = claim.Title
	pack.Claim.Impact, _ = claim.Detail["impact"].(string)
	pack.Claim.Combined, _ = claim.Detail["combined"].(bool)

	if pack.Claim.Combined {
		// 组合声明:derived_from → Fact 即链上环节,各事实的发现步骤经
		// produces 边(对象出生同源,P4-12)。
		for _, e := range derived {
			if e.From != claimID {
				continue
			}
			fact, err := l.Store.Node(ctx, l.TaskID, e.To)
			if err != nil || fact.Type != rings.NodeFact {
				continue
			}
			d := narrativeDiscovery{FactTitle: fact.Title}
			d.UnitID, _ = fact.Detail["unit_id"].(string)
			d.EvidenceHead = l.claimEvidenceHead(fact.Detail)
			if stepID := factDiscoverStep(evs, fact.ID); stepID != "" {
				d.StepID = stepID
				if n, err := l.Store.Node(ctx, l.TaskID, stepID); err == nil {
					d.Intent = n.Title
				}
			}
			d.Assets = assets // 链环节共享声明的资产锚点
			d.Exchanges = l.stepExchanges(evs, d.StepID)
			pack.Discoveries = append(pack.Discoveries, d)
		}
	} else {
		// 普通声明:derived_from → Step 即发现步骤(血缘即"这个发现从哪
		// 来"),发现单位在声明 detail。
		d := narrativeDiscovery{UnitID: unit, EvidenceHead: l.claimEvidenceHead(claim.Detail)}
		for _, e := range derived {
			if e.From != claimID {
				continue
			}
			if n, err := l.Store.Node(ctx, l.TaskID, e.To); err == nil && n.Type == rings.NodeStep {
				d.StepID = e.To
				d.Intent = n.Title
				break
			}
		}
		d.Assets = assets
		d.Exchanges = l.stepExchanges(evs, d.StepID)
		pack.Discoveries = append(pack.Discoveries, d)
	}

	// 反证轮:当前战役(重开边界与复现步骤同口径),轮次升序;竞争解释
	// 标签取声明冻结清单。
	labels := altLabels(claim)
	campaign := map[string]bool{}
	if falsifySteps, err := ClaimFalsifySteps(ctx, l.Store, l.TaskID, claimID); err == nil {
		for _, s := range falsifySteps {
			campaign[s.ID] = true
		}
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil ||
			r.ClaimID != claimID || r.Type != rings.TestClaimRecFalsify || !campaign[r.StepID] {
			continue
		}
		round := narrativeRound{
			Round: r.Round, UnitID: r.UnitID,
			ExistenceRefuted: r.ExistenceRefuted, ImpactSupported: r.ImpactSupported,
		}
		for _, a := range r.Alternatives {
			round.Alternatives = append(round.Alternatives,
				narrativeAlt{ID: a.ID, Label: labels[a.ID], Refuted: a.Refuted})
		}
		pack.Falsify = append(pack.Falsify, round)
	}

	if j != nil {
		for _, c := range j.Verdict.Conditions {
			pack.Confirm = append(pack.Confirm, narrativeCondWords[c.Kind])
		}
	}
	return pack, nil
}

// claimEvidenceHead 节点 detail 的发现证据截头(inline evidence 兜底,
// 同入账口径)。
func (l *Loop) claimEvidenceHead(detail map[string]any) string {
	if head, _ := detail["evidence_head"].(string); head != "" {
		return head
	}
	if ev, _ := detail["evidence"].(string); ev != "" {
		return clipLine(ev, narrativeBodyClip)
	}
	return ""
}

// factDiscoverStep 事实的发现步骤(produces 边来源;事件流一次扫,
// 组合声明多环节共用)。
func factDiscoverStep(evs []rings.RawEvent, factID string) string {
	for _, ev := range evs {
		if ev.Kind != rings.EventEdgeAdded {
			continue
		}
		var e rings.EdgeAdded
		if json.Unmarshal(ev.Payload, &e) != nil || e.EdgeType != rings.EdgeProduces || e.ToNode != factID {
			continue
		}
		return e.FromNode
	}
	return ""
}

// narrativeAssets 声明锚定资产(标题 + 来源 + 发现型出生单位)。
func (l *Loop) narrativeAssets(ctx context.Context, claimID string, abouts []rings.EdgeView) []narrativeAsset {
	var out []narrativeAsset
	for _, e := range abouts {
		if e.From != claimID {
			continue
		}
		n, err := l.Store.Node(ctx, l.TaskID, e.To)
		if err != nil || n.Type != rings.NodeAsset {
			continue
		}
		a := narrativeAsset{Title: n.Title}
		a.Source = rings.AssetSourceOf(n.Detail)
		if a.Source == string(rings.AssetSourceDiscovered) {
			a.BirthUnit, _ = n.Detail["unit_id"].(string)
		}
		out = append(out, a)
	}
	return out
}

// stepExchanges 步骤的 http(s) 交换(来源载体事实;账本序最后 N 条,
// 正文截头自 blob 回放,读不到只留摘要字段)。
func (l *Loop) stepExchanges(evs []rings.RawEvent, stepID string) []narrativeExchange {
	if stepID == "" {
		return nil
	}
	var all []narrativeExchange
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if json.Unmarshal(ev.Payload, &m) != nil || m.StepID != stepID {
			continue
		}
		if m.Type != rings.MirrorRecHTTP && m.Type != rings.MirrorRecHTTPS {
			continue // 隧道无原文,来源载体不成立
		}
		x := narrativeExchange{
			Method: m.Method, URL: m.URL, Status: m.Status, RespBytes: m.RespBytes,
		}
		if l.Swarm != nil && l.Swarm.Blobs != nil && m.BlobRef != "" {
			if doc, err := l.Swarm.Blobs.Read(m.BlobRef); err == nil {
				var ex mirror.Exchange
				if json.Unmarshal(doc, &ex) == nil && ex.Response != nil {
					x.RespType = headerValue(ex.Response.Headers, "Content-Type")
					x.BodyHead = clipLine(string(ex.Response.Body), narrativeBodyClip)
				}
			}
		}
		all = append(all, x)
	}
	if len(all) > narrativeExchangeCap {
		all = all[len(all)-narrativeExchangeCap:]
	}
	return all
}

// altLabels 声明冻结的竞争解释 id → 标签。
func altLabels(claim *rings.NodeFull) map[string]string {
	labels := map[string]string{}
	raw, _ := claim.Detail["alternatives"].([]any)
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		label, _ := m["label"].(string)
		if id != "" {
			labels[id] = label
		}
	}
	return labels
}

// deterministicNarrative 拼句兜底(生成失败/未配模型时冻结的确定性
// 陈述;只述链上可数事实,不猜来源载体)。
func deterministicNarrative(p *narrativePack) string {
	var b strings.Builder
	switch {
	case p.Claim.Combined:
		fmt.Fprintf(&b, "主宰组合链上 %d 条事实提出判断", len(p.Discoveries))
	case len(p.Discoveries) > 0 && p.Discoveries[0].StepID != "":
		d := p.Discoveries[0]
		b.WriteString("单位 " + d.UnitID)
		if d.Intent != "" {
			fmt.Fprintf(&b, " 执行「%s」时发现", clipLine(d.Intent, 40))
		} else {
			b.WriteString(" 在步骤 " + d.StepID + " 发现")
		}
	default:
		b.WriteString("链上无发现血缘")
	}
	if n := len(p.Falsify); n > 0 {
		killed := 0
		for _, r := range p.Falsify {
			for _, a := range r.Alternatives {
				if a.Refuted {
					killed++
				}
			}
		}
		fmt.Fprintf(&b, ";经 %d 轮反证", n)
		if killed > 0 {
			fmt.Fprintf(&b, "(排除竞争解释 %d 条)", killed)
		}
	} else {
		b.WriteString(";无反证轮")
	}
	b.WriteString(";仲裁者判定成立")
	return b.String()
}

// askClaimNarrative 一次无工具单轮会话:链上事实包 → 叙述 JSON(引擎
// 链路与转录/用量记账同拆解会话,与描述生成共用 genSession)。
func (l *Loop) askClaimNarrative(ctx context.Context, gs *genSession, pack *narrativePack) (string, error) {
	payload, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	text, err := gs.ask(ctx, string(payload))
	if err != nil {
		return "", err
	}
	return parseClaimNarrative(text)
}

// parseClaimNarrative 宽容解析模型回复:剥围栏、取首尾大括号主体、
// 空 narrative 视为失败(调用方保留拼句)。
func parseClaimNarrative(text string) (string, error) {
	body := strings.TrimSpace(text)
	if m := jsonFenceRe.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
	}
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	if body == "" {
		return "", fmt.Errorf("回复为空")
	}
	var r struct {
		Narrative string `json:"narrative"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return "", fmt.Errorf("不是合法 JSON: %w", err)
	}
	if strings.TrimSpace(r.Narrative) == "" {
		return "", fmt.Errorf("narrative 为空")
	}
	return r.Narrative, nil
}

// narrativeSystem 叙述生成的系统段(代码固定;稳定短段利于前缀缓存)。
// 文风即面板文风(8.5):陈述事实、数字精确、无 emoji、无服务腔。
const narrativeSystem = `为渗透测试发现写"来龙去脉"叙述。基于给出的链上事实与证据,写一段中文自然叙述(一段,不超过 150 字),依次说清四件事:在哪发现的(来源载体:从哪个页面 / JS 文件 / 接口响应发现,照抄证据或交换里的 URL 与载体特征)、谁发现的(虫群单位与步骤意图)、怎么验证的(反证轮次与竞争解释排除)、怎么确认的(仲裁者判定)。

规则:
1. 只用给出的事实,不编造;链上没有的信息不写(如无来源载体线索就不猜载体)。
2. 数字精确(轮次、状态码、字节数);无 emoji;不写工具名与实现细节;陈述事实不解说。
3. 组合判断按链上各事实顺序叙述各环节的发现。

只输出 JSON:{"narrative":"…"}。`
