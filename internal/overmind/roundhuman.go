package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

// P4-56 ⑤(问题清单 #80②)+P4-64 ①(问题清单 #82):反证时间线人话三段
// "疑点→怎么验的→结论"。P4-64 起逐轮生成——每轮 falsify 记录入账时
// 同链生成并冻结(claim.round_human 事件,与机器记录同批;见 claimtext.go
// 的 GenRoundHumanFor 与 swarm 钩子),定稿批次不再重复生成;重开后新
// 轮次增量补(轮号全局续号,投影按轮 upsert)。本文件承载逐轮生成
// 复用的素材形态与解析;读侧:锤炼中即有(详情 round_humans),存量
// 旧定稿的 repro.Rounds 由读侧合读(同轮号新事件优先),旧定稿无
// Rounds 读侧只呈现机器层(如实,不编造)。

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

// roundHumanPack 人话生成的事实包:声明两维 + 单轮素材(逐轮会话每次
// 只带一轮;字段形态与 P4-56 定稿批次兼容,读侧零改动)。
type roundHumanPack struct {
	Statement string          `json:"statement"`
	Impact    string          `json:"impact,omitempty"`
	Rounds    []roundMaterial `json:"rounds"`
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

// rewriteRoundHuman 单轮三段的闸门退回重写(经指针回填调用方元素;
// 逐轮生成管线复用)。
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
