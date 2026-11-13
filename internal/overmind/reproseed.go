package overmind

// P4-73(问题清单 #89)确认必有复现包——证据种子解析:仲裁确认的依据
// 交换直接成为复现包必选种子(确认 ⟹ 证明物必在包,构造性保证)。
// 解析顺序固定:
//   - 引用命中:当前战役各轮 falsify_result 携带的交换 ID 引用在反证
//     步骤的镜留痕(http/https)里命中——证明物直进,不经候选池、不受
//     去同型/池裁剪/模型挑选任何影响;引用命中的交换全收(每条都是
//     单位明确指认的证明物);
//   - URL 回查兜底(引用缺失或全部未命中):按结论证据文本中出现的
//     URL 在反证步骤流量确定性回查——镜交换精确 URL → 可组包的直连
//     工具调用记录(P4-62 C(b) 通道)→ 镜交换同 host+path(查询串不敏感),
//     每层只锚最近一条命中(证据常反复引用同一 URL,逐条全收会把重复
//     交换灌成多种子;该 URL 的末次验证即证明物,重复与前置交换由模型
//     在种子之上从池里补全);回查跳过 error 交换(启发式不挑失败请求
//     当证明物;引用命中不受此限——单位点名引用的交换照收)。
//
// 直连形态(证据在工具记录而非镜交换)无镜交换可引用:URL 回查命中
// 可确定性组包的工具调用记录时,种子走 C(b) 通道组包;不可静态还原的
// 命令组不出请求包,不构成可交付证明(确认门槛校验据此不确认)。
// 确认门槛与装配共用本解析(同一函数,构造性一致),引用解析限于当前
// 战役反证步骤的流量(与 Process 清单/复现归因范围一致)。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// EvidenceSeed 确认依据解析出的证明物原料(Mirror 与 ToolRec 恰一非空)。
type EvidenceSeed struct {
	// Mirror 镜交换命中(引用或 URL 回查)。
	Mirror *rings.MirrorRecord
	// ToolRec 直连工具记录命中(URL 回查,经 C(b) 通道组包)。
	ToolRec *rings.SwarmRecord
	// Origin 账本事件序(与池候合并序用;镜=事件序,工具=事件序+事件总数,
	// 保持池的"镜先于组包"相对次序)。
	Origin int
	// Via 命中通道:"reference"(交换引用)| "url"(回查兜底)。
	Via string
}

// 引用/回查命中通道(P4-73 ②③)。
const (
	seedViaReference = "reference"
	seedViaURL       = "url"
)

// evidenceURLRe 证据文本中的 URL token(http/https 起头,止于空白与
// 常见包裹符;尾部标点收敛——确定性,零语义猜测)。
var evidenceURLRe = regexp.MustCompile(`https?://[^\s"'<>\` + "`" + `]+`)

// evidenceURLTrim URL token 尾部的句读标点(中文顿号/逗号/句号与 ASCII
// 标点,证据行文里的收尾)。
const evidenceURLTrim = ".,;:!?、，。；：）)】》」』]"

// extractEvidenceURLs 从证据文本抽取 URL(按出现次序去重;blob 全文优先,
// 读不到退截头)。
func extractEvidenceURLs(texts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, m := range evidenceURLRe.FindAllString(t, -1) {
			u := strings.TrimRight(m, evidenceURLTrim)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// evidenceURLHostPath URL 的 host+path 归一口径(查询串不敏感;host
// 小写,解析失败返回原串)。
func evidenceURLHostPath(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		return strings.ToLower(u.Host) + path
	}
	return raw
}

// toolProducible 直连工具调用记录能否确定性组包(P4-62 C(b):组不出
// 请求包的记录不构成可交付证明——种子命中与装配同一判定,防门槛与
// 装配漂移)。非流量命令不构成命中。
func toolProducible(taskID string, r rings.SwarmRecord) bool {
	if r.ToolName == "http_probe" {
		return assembleProbePacket(taskID, r.StepID, r.UnitID, r.ToolArgs, "").Ex != nil
	}
	if r.ToolName != "bash" {
		return false
	}
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(r.ToolArgs, &a) != nil || !looksLikeTrafficCommand(a.Command) {
		return false
	}
	return assembleBashPacket(taskID, r.StepID, r.UnitID, r.ToolArgs, "").Ex != nil
}

// ResolveEvidenceSeeds 解析声明确认依据的种子集(P4-73 ②③;纯读账本
// 与 blob,零 LLM,零写)。返回种子的账本次序与命中通道;空集 = 证据
// 不可追溯(确认门槛校验据此不确认)。blobs 为 nil 时证据全文退截头。
func ResolveEvidenceSeeds(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore,
	taskID, claimID string) ([]EvidenceSeed, error) {
	campaignRev, err := claimCampaignRev(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	falsifySteps, err := ClaimFalsifySteps(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	stepIDs := map[string]bool{}
	for _, s := range falsifySteps {
		stepIDs[s.ID] = true
	}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 种子解析读事件流: %w", err)
	}

	// 战役内反证结论(引用清单 + 证据文本)。
	var refs []string
	var evidenceTexts []string
	seenRef := map[string]bool{}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || ev.Revision <= campaignRev {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil {
			continue
		}
		if r.Type != rings.TestClaimRecFalsify || r.ClaimID != claimID {
			continue
		}
		text := r.EvidenceHead
		if r.EvidenceRef != "" && blobs != nil {
			if doc, rerr := blobs.Read(r.EvidenceRef); rerr == nil {
				text = string(doc)
			}
		}
		if strings.TrimSpace(text) != "" {
			evidenceTexts = append(evidenceTexts, text)
		}
		for _, id := range r.EvidenceExchanges {
			if id = strings.TrimSpace(id); id != "" && !seenRef[id] {
				seenRef[id] = true
				refs = append(refs, id)
			}
		}
	}

	// 反证步骤的镜留痕(http/https,账次序)与可组包直连工具调用记录。
	type mirrorHit struct {
		rec    rings.MirrorRecord
		origin int
	}
	mirrors := []mirrorHit{}
	byExchange := map[string]int{}
	toolOffset := len(evs) // 工具组包排在全部镜交换之后(与池构造同次序)
	type toolHit struct {
		rec    rings.SwarmRecord
		origin int
	}
	tools := []toolHit{}
	for i, ev := range evs {
		switch ev.Kind {
		case rings.EventMirrorRecord:
			var m rings.MirrorRecord
			if json.Unmarshal(ev.Payload, &m) != nil {
				continue
			}
			if !stepIDs[m.StepID] || (m.Type != rings.MirrorRecHTTP && m.Type != rings.MirrorRecHTTPS) {
				continue
			}
			byExchange[m.ExchangeID] = len(mirrors)
			mirrors = append(mirrors, mirrorHit{rec: m, origin: i})
		case rings.EventSwarmRecord:
			var r rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &r) != nil {
				continue
			}
			if r.Type != rings.SwarmRecToolCall || r.IsError || !stepIDs[r.StepID] {
				continue
			}
			if (r.ToolName != "http_probe" && r.ToolName != "bash") || !toolProducible(taskID, r) {
				continue
			}
			tools = append(tools, toolHit{rec: r, origin: toolOffset + i})
		}
	}

	var seeds []EvidenceSeed
	addMirror := func(idx int, via string) {
		seeds = append(seeds, EvidenceSeed{Mirror: &mirrors[idx].rec, Origin: mirrors[idx].origin, Via: via})
	}

	// 引用命中:单位点名的交换照收(error 过滤不适用于种子——证明物直进);
	// 同一 URL 的重复引用按地址去重保最近(活体实锤:单位常把同一证明的
	// 多次重放逐条引用,同一证明不重复进包;不同地址的引用各自保留——
	// 多步链的各环节证明不受影响)。这不是池过滤,是同一证明物的归并。
	urlLatest := map[string]int{} // 引用命中的交换 URL → mirrors 下标(最近)
	for _, id := range refs {
		idx, ok := byExchange[id]
		if !ok {
			continue
		}
		urlLatest[mirrors[idx].rec.URL] = idx
	}
	seenURL := map[string]bool{}
	var refOrder []string
	for _, id := range refs { // 按引用次序输出去重后的种子(保序可读)
		idx, ok := byExchange[id]
		if !ok {
			continue
		}
		u := mirrors[idx].rec.URL
		if seenURL[u] {
			continue
		}
		seenURL[u] = true
		refOrder = append(refOrder, u)
	}
	for _, u := range refOrder {
		addMirror(urlLatest[u], seedViaReference)
	}
	if len(seeds) > 0 {
		return seeds, nil
	}

	// URL 回查兜底:结论证据文本中出现的 URL 在反证步骤流量确定性匹配。
	// 活体实锤修正(P4-73 活体一轮):证据文本常反复引用同一 URL(多次
	// 重放、对照验证),逐条全收会把重复交换灌成"多种子"——回查是启发
	// 式锚定,每层只取最近一条命中(该 URL 的末次验证即证明物;重复与
	// 前置交换由模型在种子之上从池里补全)。单位点名的引用不受此限
	//(引用命中全收——每条都是单位明确指认的证明物)。
	urls := extractEvidenceURLs(evidenceTexts)
	if len(urls) == 0 {
		return nil, nil
	}
	urlSet := map[string]bool{}
	for _, u := range urls {
		urlSet[u] = true
	}
	// 层 1:镜交换精确 URL(跳过 error 交换),取最近一条。
	lastMirror := -1
	for idx, m := range mirrors {
		if m.rec.Error != "" || !urlSet[m.rec.URL] {
			continue
		}
		lastMirror = idx
	}
	if lastMirror >= 0 {
		addMirror(lastMirror, seedViaURL)
		return seeds, nil
	}
	// 层 2:可组包的直连工具调用记录(参数/命令含证据 URL;直连形态),
	// 取最近一条。
	for i := len(tools) - 1; i >= 0; i-- {
		if containsAnyURL(string(tools[i].rec.ToolArgs), urls) {
			th := tools[i]
			seeds = append(seeds, EvidenceSeed{ToolRec: &th.rec, Origin: th.origin, Via: seedViaURL})
			return seeds, nil
		}
	}
	// 层 3:镜交换同 host+path(查询串不敏感;证据引 URL 摘路径形态时
	// 仍可回查,跳过 error 交换),取最近一条。
	hpSet := map[string]bool{}
	for _, u := range urls {
		hpSet[evidenceURLHostPath(u)] = true
	}
	for idx := len(mirrors) - 1; idx >= 0; idx-- {
		if mirrors[idx].rec.Error != "" || !hpSet[evidenceURLHostPath(mirrors[idx].rec.URL)] {
			continue
		}
		addMirror(idx, seedViaURL)
		return seeds, nil
	}
	return seeds, nil
}

// containsAnyURL 文本是否包含任一候选 URL(子串口径:工具参数/命令里
// 的字面 URL;变量拼接形态匹配不上,如实不构成命中)。
func containsAnyURL(text string, urls []string) bool {
	for _, u := range urls {
		if strings.Contains(text, u) {
			return true
		}
	}
	return false
}
