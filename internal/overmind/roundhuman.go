package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// P4-56 ⑤(问题清单 #80②):反证时间线人话化——确认定稿时由主宰模型
// 对当前战役每轮反证写"疑点→怎么验的→结论"三段人话:
//   - 疑点 = 该轮待排除的其他可能原因,疑问句形态("会不会是缓存造成
//     的假象?"式),替代机器标签;
//   - 怎么验的 = 验证过程人话("连续 8 次请求内容完全一致,且无任何
//     缓存标记"式);
//   - 结论 = 结论词:已排除/未排除/推翻。
// 机器原文(替代解释标签、结构化结论、证据截头)由读侧折叠呈现,默认
// 只展示人话层。与来龙去脉叙述(P4-13)同批同规矩:随确认裁决同批冻结
// 入 repro 记录(Rounds);报告导出与面板渲染零 LLM(R17 不破);生成
// 失败回退确定性拼句,不阻塞机器裁决;重开再确认随新定稿重新生成;
// 人话是包装不是证据,与机器字段矛盾时以原文为准。旧定稿无 Rounds,
// 读侧只呈现机器层(如实,不编造)。

// roundHumanClip 单段人话的落账上限(三段合计约百字量级)。
const roundHumanClip = 160

// roundMaterial 一轮反证的人话生成素材(全部读账本确定性装配,零编造)。
type roundMaterial struct {
	Round            int            `json:"round"`
	UnitID           string         `json:"unit_id,omitempty"`
	ExistenceRefuted bool           `json:"existence_refuted,omitempty"`
	ImpactSupported  bool           `json:"impact_supported,omitempty"`
	Alternatives     []narrativeAlt `json:"alternatives,omitempty"`
	EvidenceHead     string         `json:"evidence_head,omitempty"`
	Note             string         `json:"note,omitempty"`
}

// roundHumanPack 人话生成的事实包:声明两维 + 当前战役逐轮素材。
type roundHumanPack struct {
	Statement string          `json:"statement"`
	Impact    string          `json:"impact,omitempty"`
	Rounds    []roundMaterial `json:"rounds"`
}

// genRoundHumans 定稿时间线人话生成:装配逐轮素材,先落确定性拼句
// (同 P4-08 ④ 组装先落确定性描述的规矩),模型成功则改写为人话三段并
// 冻结;失败如实 WARN 回退拼句,不阻塞确认裁决。零 Provider 保留拼句
// (单测与未配模型路径,零外呼)。P4-24:生成后接定稿校对+模式闸门。
func (l *Loop) genRoundHumans(ctx context.Context, claimID string, rec *rings.TestClaimRecord) {
	pack, err := l.roundHumanPack(ctx, claimID)
	if err != nil {
		runlog.Warnf("反证时间线人话生成失败: 任务 %s 判断 %s 原因 %s(素材装配失败,机器层呈现)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	if len(pack.Rounds) == 0 {
		return // 当前战役无反证轮(重开后未跑完等),机器层如实呈现
	}
	rec.Rounds = deterministicRoundHumans(pack)
	if l.Provider == nil {
		return
	}
	gs := l.newGenSession(roundHumanSystem)
	defer gs.close()
	text, err := l.askRoundHumans(ctx, gs, pack)
	if err != nil {
		runlog.Warnf("反证时间线人话生成失败: 任务 %s 判断 %s 原因 %s(回退确定性拼句)",
			l.TaskID, claimID, clipEval(err.Error()))
		return
	}
	if rounds, perr := parseRoundHumans(text, pack.maxRound()); perr != nil {
		runlog.Warnf("反证时间线人话解析失败: 任务 %s 判断 %s 原因 %s(回退确定性拼句)",
			l.TaskID, claimID, clipEval(perr.Error()))
	} else {
		for i := range rec.Rounds {
			if r, ok := rounds[rec.Rounds[i].Round]; ok {
				if strings.TrimSpace(r.Doubt) != "" {
					rec.Rounds[i].Doubt = clipLine(r.Doubt, roundHumanClip)
				}
				if strings.TrimSpace(r.How) != "" {
					rec.Rounds[i].How = clipLine(r.How, roundHumanClip)
				}
				if strings.TrimSpace(r.Conclusion) != "" {
					rec.Rounds[i].Conclusion = clipLine(r.Conclusion, roundHumanClip)
				}
			}
		}
	}
	l.polishRoundHumans(ctx, claimID, gs, rec)
}

// roundHumanPack 逐轮素材装配(只读账本):当前战役(重开边界与复现
// 步骤同口径)的 falsify 记录,轮次升序;竞争解释标签取声明冻结清单,
// 证据截头与备注逐轮携带(模型讲"怎么验的"的事实依据)。
func (l *Loop) roundHumanPack(ctx context.Context, claimID string) (*roundHumanPack, error) {
	claim, err := l.Store.Node(ctx, l.TaskID, claimID)
	if err != nil {
		return nil, fmt.Errorf("查判断: %w", err)
	}
	campaign := map[string]bool{}
	if falsifySteps, err := ClaimFalsifySteps(ctx, l.Store, l.TaskID, claimID); err == nil {
		for _, s := range falsifySteps {
			campaign[s.ID] = true
		}
	}
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("查事件流: %w", err)
	}
	labels := altLabels(claim)
	pack := &roundHumanPack{Statement: claim.Title}
	pack.Impact, _ = claim.Detail["impact"].(string)
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil ||
			r.ClaimID != claimID || r.Type != rings.TestClaimRecFalsify || !campaign[r.StepID] {
			continue
		}
		m := roundMaterial{
			Round: r.Round, UnitID: r.UnitID,
			ExistenceRefuted: r.ExistenceRefuted, ImpactSupported: r.ImpactSupported,
			EvidenceHead: clipLine(r.EvidenceHead, 200), Note: clipLine(r.Note, 200),
		}
		for _, a := range r.Alternatives {
			m.Alternatives = append(m.Alternatives, narrativeAlt{ID: a.ID, Label: labels[a.ID], Refuted: a.Refuted})
		}
		pack.Rounds = append(pack.Rounds, m)
	}
	return pack, nil
}

// deterministicRoundHumans 拼句兜底(生成失败/未配模型时冻结的确定性
// 三段;只述轮上可数事实,疑问句与结论词按定稿词形拼装)。
func deterministicRoundHumans(p *roundHumanPack) []rings.FalsifyRoundHuman {
	out := make([]rings.FalsifyRoundHuman, 0, len(p.Rounds))
	for _, r := range p.Rounds {
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
		out = append(out, h)
	}
	return out
}

func allRefuted(alts []narrativeAlt) bool {
	for _, a := range alts {
		if !a.Refuted {
			return false
		}
	}
	return len(alts) > 0
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// askRoundHumans 一次无工具单轮会话:事实包 → 三段人话 JSON。
func (l *Loop) askRoundHumans(ctx context.Context, gs *genSession, pack *roundHumanPack) (string, error) {
	payload, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	return gs.ask(ctx, string(payload))
}

// maxRound 素材里的最大轮次号——轮次号全局续号(reopen 后战役内首轮
// 也是 7 这样的号),解析上界须取实际轮号而非战役内序数(活体实锤:
// 重开定稿时按 len(轮数) 过滤把合法回复整体丢弃,回退了拼句)。
func (p *roundHumanPack) maxRound() int {
	max := 0
	for _, r := range p.Rounds {
		if r.Round > max {
			max = r.Round
		}
	}
	return max
}

// roundsMaxOf 已冻结轮次集合的最大轮号。
func roundsMaxOf(rs []rings.FalsifyRoundHuman) int {
	max := 0
	for _, r := range rs {
		if r.Round > max {
			max = r.Round
		}
	}
	return max
}

// parseRoundHumans 宽容解析模型回复:剥围栏、取首尾大括号主体;按轮次
// 归位(模型漏答的轮次保留拼句,round 字段与素材对不上即弃该条)。
func parseRoundHumans(text string, want int) (map[int]rings.FalsifyRoundHuman, error) {
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
		return nil, fmt.Errorf("回复为空")
	}
	var r struct {
		Rounds []rings.FalsifyRoundHuman `json:"rounds"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, fmt.Errorf("不是合法 JSON: %w", err)
	}
	out := map[int]rings.FalsifyRoundHuman{}
	for _, x := range r.Rounds {
		if x.Round >= 1 && x.Round <= want {
			out[x.Round] = x
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("rounds 为空")
	}
	return out, nil
}

// roundHumanSystem 三段人话生成的系统段(代码固定;文风按 #80 报告级
// 锚的验证过程口径:逻辑链步骤、数字精确、结论词受限)。
const roundHumanSystem = `为渗透测试的反证轮写人话三段。对给出的每一轮反证,写三段中文:

- doubt(疑点):这一轮要排除的其他可能原因,写成疑问句,如"会不会是缓存造成的假象?会不会是当时的请求根本没到达目标?"——每条一个疑问句,不加编号。
- how(怎么验的):验证过程人话,按"做了什么→看到什么"写,如"连续 8 次请求内容完全一致,且响应头无任何缓存标记";只依据给出的证据与结论素材,数字精确。
- conclusion(结论):只能用三个词之一——全部排除写"已排除",还有未排除的写"未排除"(可加一句还剩什么),推翻了存在性判断写"推翻"。

规则:
1. 只用给出的事实,不编造;素材里没有的不写。
2. 平实书面白话;短句;无 emoji;不写工具名与实现细节。
3. 每段不超过两行。

只输出 JSON:{"rounds":[{"round":1,"doubt":"…","how":"…","conclusion":"…"},…]},round 与素材一致,覆盖全部轮次。`

// polishRoundHumans 三段人话的校对+闸门链(同 polishNarrative 的逐条
// 版:三段任一命中机器语特征 → 同会话退回重写一次 → 复扫仍命中如实
// 标注 Unpolished,不阻塞定稿)。
func (l *Loop) polishRoundHumans(ctx context.Context, claimID string, gs *genSession, rec *rings.TestClaimRecord) {
	if text, err := gs.ask(ctx, roundHumanProofreadAsk); err != nil {
		runlog.Warnf("反证时间线人话校对失败: 任务 %s 判断 %s 原因 %s(保留生成稿)",
			l.TaskID, claimID, clipEval(err.Error()))
	} else if rounds, perr := parseRoundHumans(text, roundsMaxOf(rec.Rounds)); perr == nil {
		for i := range rec.Rounds {
			if r, ok := rounds[rec.Rounds[i].Round]; ok {
				rec.Rounds[i].Doubt = clipLine(r.Doubt, roundHumanClip)
				rec.Rounds[i].How = clipLine(r.How, roundHumanClip)
				rec.Rounds[i].Conclusion = clipLine(r.Conclusion, roundHumanClip)
			}
		}
	}
	for i := range rec.Rounds {
		var hits []talkHit
		for _, text := range []string{rec.Rounds[i].Doubt, rec.Rounds[i].How, rec.Rounds[i].Conclusion} {
			hits = append(hits, ScanMachineTalk(text)...)
		}
		if len(hits) == 0 {
			continue
		}
		if err := l.rewriteRoundHuman(ctx, gs, &rec.Rounds[i], hits); err != nil {
			runlog.Warnf("反证时间线人话改写失败: 任务 %s 判断 %s 第 %d 轮 原因 %s(如实标注)",
				l.TaskID, claimID, rec.Rounds[i].Round, clipEval(err.Error()))
		} else {
			hits = nil
			for _, text := range []string{rec.Rounds[i].Doubt, rec.Rounds[i].How, rec.Rounds[i].Conclusion} {
				hits = append(hits, ScanMachineTalk(text)...)
			}
		}
		if len(hits) > 0 {
			rec.Rounds[i].Unpolished = true
			runlog.Warnf("反证时间线人话未过文风校验,按原文保留: 任务 %s 判断 %s 第 %d 轮(%s)",
				l.TaskID, claimID, rec.Rounds[i].Round, strings.Join(talkHitLines(hits), ";"))
		}
	}
}

// rewriteRoundHuman 单轮三段的闸门退回重写(经指针回填调用方切片元素)。
func (l *Loop) rewriteRoundHuman(ctx context.Context, gs *genSession, r *rings.FalsifyRoundHuman, hits []talkHit) error {
	var b strings.Builder
	fmt.Fprintf(&b, "你写的第 %d 轮人话里有机器语,改写掉,事实一字不动:\n", r.Round)
	fmt.Fprintf(&b, "原文:%s / %s / %s\n", r.Doubt, r.How, r.Conclusion)
	for _, line := range talkHitLines(hits) {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString(talkRewriteReferralNote + proofreadHumanStandard + "\n")
	b.WriteString(`只输出 JSON:{"rounds":[{"round":` + fmt.Sprint(r.Round) + `,"doubt":"…","how":"…","conclusion":"…"}]}`)
	text, err := gs.ask(ctx, b.String())
	if err != nil {
		return err
	}
	rounds, err := parseRoundHumans(text, 1<<20)
	if err != nil {
		return err
	}
	nr, ok := rounds[r.Round]
	if !ok {
		return fmt.Errorf("重写回复缺第 %d 轮", r.Round)
	}
	r.Doubt, r.How, r.Conclusion = nr.Doubt, nr.How, nr.Conclusion
	return nil
}

// roundHumanProofreadAsk 三段人话的校对续问。
const roundHumanProofreadAsk = `校对改写你刚写的全部反证轮人话:只改文风,事实一字不动(轮次、解释内容、数字、结论全照抄);疑问句仍是疑问句,结论词仍只用 已排除/未排除/推翻。
` + proofreadHumanStandard + `
只输出 JSON:{"rounds":[{"round":1,"doubt":"…","how":"…","conclusion":"…"},…]},round 与上一轮一致,覆盖全部轮次。`
