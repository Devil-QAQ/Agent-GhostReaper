package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	// Submit 交卷单要素(当前战役最近一次交换单;确认时点的新素材源,
	// P5-03③ 改挂——原逐轮反证素材已随三段流水线退役)。
	Submit *narrativeSubmit `json:"submit,omitempty"`
	// Confirm 机器核验的逐条件依据(词表与界面一致)。
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

// narrativeSubmit 交卷单要素(来龙去脉"怎么验证的"素材:自查记录与
// 证明物,全部照抄交换单原文)。
type narrativeSubmit struct {
	UnitID        string   `json:"unit_id,omitempty"`
	ProvingMarker string   `json:"proving_marker,omitempty"`
	Countercheck  string   `json:"countercheck,omitempty"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
}

// narrativeCondWords 机器核验条件种类 → 界面词(P5-02 三查词表;术语
// 对照表同词,面板 COND_LABELS 同源不另造——"证据可追溯/自查有记录/
// 标准未变"入术语清单)。
var narrativeCondWords = map[string]string{
	"evidence_traceable": "证据可追溯",
	"countercheck_done":  "自查有记录",
	"standard_frozen":    "标准未变",
	"fact_confirmed":     "事实已确认",
	"tool_observed":      "工具已观测",
}

// genClaimNarrative 定稿来龙去脉叙述生成:组装链上事实包,先落确定性
// 拼句(同 P4-08 ④ 组装时先落确定性描述的规矩),模型成功则改写为人话
// 叙述并冻结;失败如实 WARN 回退拼句,不阻塞确认裁决。零 Provider 保留
// 拼句(单测与未配模型路径,零外呼)。
// P4-24:生成后接定稿校对+模式闸门(同会话,见 proofread.go)。
func (l *Loop) genClaimNarrative(ctx context.Context, claimID string, conds []rings.ArbiterConditionResult, rec *rings.TestClaimRecord) {
	pack, err := l.narrativePack(ctx, claimID, conds)
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
		rec.Narrative = strings.TrimSpace(t)
	}
	l.polishNarrative(ctx, claimID, gs, rec)
}

// narrativePack 链上事实包装配(只读账本):声明两维与组合标记、发现
// 环节(普通 = 声明自身血缘;组合 = 链上各事实及其发现步骤)、当前战役
// 反证轮(存量)、机器核验条件。
func (l *Loop) narrativePack(ctx context.Context, claimID string, conds []rings.ArbiterConditionResult) (*narrativePack, error) {
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

	// 交卷单要素:当前战役(重开边界)最近一次交换单(P5-03③ 改挂交卷
	// 确认时点的新素材源——自查记录与证明物;旧账本无 submit 记录时
	// 留空,叙述按缺项如实呈现)。
	campaignRev := int64(0)
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID {
			continue
		}
		switch r.Type {
		case rings.TestClaimRecReopen:
			campaignRev = ev.Revision
		case rings.TestClaimRecSubmit:
			if ev.Revision > campaignRev {
				pack.Submit = &narrativeSubmit{
					UnitID:        r.UnitID,
					ProvingMarker: clipLine(r.ProvingMarker, 120),
					Countercheck:  clipLine(r.CountercheckNote, narrativeBodyClip),
					EvidenceRefs:  r.EvidenceRefs,
				}
			}
		}
	}

	if conds != nil {
		for _, c := range conds {
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
			continue // 隧道无原文,来源载体缺失
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

// altLabels 声明冻结竞争解释的解析已随 v1.3.0 砍除清单删除(P5-02⑥)。

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
	b.WriteString(";单位会话内自查后交卷;机器核验确认")
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
const narrativeSystem = `为渗透测试发现写"来龙去脉"叙述。基于给出的链上事实与证据,用简短中文讲清发现和验证的关键经过，按材料决定如何组织。不要复述每个步骤和报文，人员与编号由来源记录提供；必要的访问前提、验证结果与范围必须准确。

规则:
1. 只用给出的事实,不编造;链上没有的信息不写(如无来源载体线索就不猜载体)。
2. 数字精确(状态码、字节数);无 emoji;不写工具名与实现细节;陈述事实不解说。
3. 组合判断按链上各事实顺序叙述各环节的发现。

只输出 JSON:{"narrative":"…"}。`
