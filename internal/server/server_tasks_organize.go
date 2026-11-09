// Package server 的建任务"资产一键优化整理格式"(P4-16):表单资产框的
// 乱文本(一行多条、分隔符不一、夹带说明文字、有重复)经单发 LLM 调用
// 规整为资产行——只规整(合并同义/拆行/归类/去重),不增删实体。内容
// 守恒红线由确定性把关兜底:模型输出逐条对账原文,新增的丢弃、丢失的
// 按原文保留,均如实记入 issues(不静默);权威始终在操作者的确认。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// organizeAssetIn 整理结果单行(面板回填形态,同提取通道的资产候选)。
type organizeAssetIn struct {
	Class string `json:"class"`
	Title string `json:"title"`
}

type organizeReply struct {
	Assets []organizeAssetIn `json:"assets"`
	Issues []string          `json:"issues"` // 守恒把关的丢弃/保留记录(如实呈现,不静默)
}

// handleAssetsOrganize 资产框一键整理(单发短调用;跑完即弃)。
// POST /api/tasks/assets-organize {lines:[...]}(表单当前全部资产输入:
// 框内文本按行 + 既有行标识)。
func (s *Server) handleAssetsOrganize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lines []string `json:"lines"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	lines := make([]string, 0, len(body.Lines))
	total := 0
	for _, l := range body.Lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		total += len([]rune(l))
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("先在资产框填入条目"))
		return
	}
	if len(lines) > 300 {
		writeErr(w, http.StatusBadRequest, errors.New("条目过多(上限 300 行),分批整理"))
		return
	}
	if total > 8000 {
		writeErr(w, http.StatusBadRequest, errors.New("内容过长(上限 8000 字),分批整理"))
		return
	}
	if s.Chain == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("模型未配置(LLM配置页激活模型后可整理);也可手动逐行填写"))
		return
	}
	prov := llm.Provider(s.Chain)
	cctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	msg, _, _, err := prov.Complete(cctx, llm.CompletionRequest{
		System: []string{assetsOrganizeSystem()},
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: strings.Join(lines, "\n")}}},
		},
		MaxTokens: 4096,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("整理失败: %w", err))
		return
	}
	reply := parseAssetsOrganize(msg.Text(), lines)
	writeJSON(w, http.StatusOK, reply)
}

// assetsOrganizeSystem 整理提示词:守恒纪律逐条钉死(只规整,不增删)。
func assetsOrganizeSystem() string {
	return `你是渗透测试建单表单的资产整理器。操作者输入一批资产条目(可能杂乱:一行多条、分隔符不一、夹带说明文字、有重复),你只做规整,输出且只输出一个 JSON 对象(不要围栏、不要解释):
{"assets":[{"class":"资产类","title":"资产标识"}]}
整理纪律(违反即失职):
- 内容守恒,只规整不增删:输出的每一条都必须来自输入原文(截取或同义归并);输入里的每个资产实体都必须保留——绝不新增输入里没有的资产,绝不丢弃输入里的资产。
- 允许的规整只有四种:合并同义(协议、前导 www.、末尾斜杠的差异视为同一实体,合并为一条)、拆行(一行多条拆成多条,行内说明文字丢弃)、归类(按 12 类词表)、去重(完全重复只留一条)。
- 资产类 12 词:domain 域名 / subdomain 子域名 / host 主机 / service 服务 / api 接口 / organization 组织 / miniprogram 小程序 / app 应用 / wechat_account 公众号 / person 人员 / leak_info 泄露信息 / other 其他。URL、IP:端口 → service;纯 IP → host;点分域名 → subdomain 或 domain。
- 与资产无关的整行说明文字不输出;标题保留原文里的标识写法,不改写。`
}

// parseAssetsOrganize 解析模型回复并执行守恒把关(确定性,零信任模型):
//   - 派生检查:输出条目须能在原文里找到出处(规范化相等,或为某原文
//     条目的子串)——新增实体整条丢弃并记 issue;
//   - 覆盖检查:原文每个实体须被某输出条目承接(规范化相等,或为某
//     输出条目的子串)——丢失实体按原文补回一行(启发式归类)并记
//     issue,保证零删除;
//   - 实体口径:行内可识别的标识形子串(URL / IP:端口 / 纯 IP / 点分
//     域名)逐个计实体;不含任何标识形的整行(如组织名)整行计一个
//     实体;纯说明文字不计。
func parseAssetsOrganize(reply string, lines []string) *organizeReply {
	out := &organizeReply{Assets: []organizeAssetIn{}, Issues: []string{}}
	raw := strings.TrimSpace(reply)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		out.Issues = append(out.Issues, "回复里没有 JSON 对象,整理为空(可重试或手动逐行填写)")
		return out
	}
	var parsed struct {
		Assets []organizeAssetIn `json:"assets"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		out.Issues = append(out.Issues, "JSON 解析失败,整理为空(可重试或手动逐行填写): "+err.Error())
		return out
	}

	toks := organizeTokens(lines)
	seenOut := map[string]bool{}
	for _, a := range parsed.Assets {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			continue
		}
		// 派生检查:找不到原文出处的条目 = 新增实体,丢弃。
		if !organizeDerived(title, lines, toks) {
			out.Issues = append(out.Issues, fmt.Sprintf("%q 在原文里找不到出处(疑似新增),已丢弃", title))
			continue
		}
		key := organizeNorm(title)
		if seenOut[key] {
			continue // 模型输出去重兜底
		}
		seenOut[key] = true
		class := ringsAssetClass(strings.TrimSpace(a.Class))
		if class == "" {
			class = organizeHeuristicClass(title)
		}
		out.Assets = append(out.Assets, organizeAssetIn{Class: class, Title: title})
	}

	// 覆盖检查:未被承接的原文实体按原文补回(零删除)。
	for _, tok := range toks {
		if seenOut[tok.norm] {
			continue
		}
		out.Assets = append(out.Assets, organizeAssetIn{Class: organizeHeuristicClass(tok.raw), Title: tok.raw})
		out.Issues = append(out.Issues, fmt.Sprintf("%q 未能归并,按原文保留", tok.raw))
	}
	return out
}

// ---- 守恒对账的规范化与实体抽取(确定性) ----

var (
	organizeURLRe    = regexp.MustCompile(`https?://[^\s、，,;；` + "`" + `"‘’"“”()（）\[\]【】]+`)
	organizeIPPortRe = regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}:\d+`)
	organizeIPRe     = regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}`)
	organizeDomainRe = regexp.MustCompile(`[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9-]+)+`)
	organizeSchemeRe = regexp.MustCompile(`^https?://`)
	// P4-50 ③新写法(无锚,行内子串提取——同 DomainRe 口径):*.域 通
	// 配与 域名:端口,整体成实体(拆开即授权粒度漂移)。
	organizeWildRe   = regexp.MustCompile(`\*\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9-]+)+`)
	organizeHostPort = regexp.MustCompile(`[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9-]+)+:\d+`)
)

// organizeNorm 标识规范化:小写、去协议、去前导 www.、去末尾斜杠——
// 同义实体(协议/前导 www./末尾斜杠差异)规范化后相等。
func organizeNorm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = organizeSchemeRe.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "www.")
	return strings.TrimRight(s, "/")
}

type organizeToken struct {
	raw  string // 原文写法(补回时逐字保留)
	norm string
}

// organizeTokens 从输入行抽实体:按 URL → IP:端口 → 域名:端口 →
// 纯 IP → *.域 → 点分域名的优先级提取标识形子串,已消费的跨度用占位
// 符遮蔽(URL 内的域名、IP:端口内的裸 IP、*.域 内的裸域名不重复计
// 实体;*.域 与 域名:端口 整体成实体——拆开即粒度漂移,P4-50 ③);
// 一行内一个标识形都没有时,整行(去空白后仍有内容)计一个实体(组织
// 名/人名等非标识形资产)。
func organizeTokens(lines []string) []organizeToken {
	var toks []organizeToken
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		n := organizeNorm(raw)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		toks = append(toks, organizeToken{raw: raw, norm: n})
	}
	for _, line := range lines {
		masked := line
		found := false
		for _, re := range []*regexp.Regexp{organizeURLRe, organizeIPPortRe, organizeHostPort, organizeIPRe, organizeWildRe, organizeDomainRe} {
			for _, m := range re.FindAllString(masked, -1) {
				add(m)
				found = true
			}
			masked = re.ReplaceAllString(masked, "\x00")
		}
		if !found {
			// 非标识行:整行计一个实体;行内分隔符切出的碎片不拆
			//(组织名等资产不按分隔符断开)。
			add(line)
		}
	}
	return toks
}

// organizeDerived 输出条目能否在原文找到出处:与某输入行规范化相等,
// 或是某输入行(或行内标识)的子串(拆行/合并同义的证据)。
func organizeDerived(title string, lines []string, toks []organizeToken) bool {
	n := organizeNorm(title)
	if n == "" {
		return false
	}
	for _, line := range lines {
		l := organizeNorm(line)
		if l == n || strings.Contains(l, n) {
			return true
		}
	}
	// 拆行证据也认行内标识形:输出条目是某标识的规范化子串。
	for _, tok := range toks {
		if tok.norm == n || strings.Contains(tok.norm, n) {
			return true
		}
	}
	return false
}

// organizeHeuristicClass 标识形启发式归类(与面板 recognizeLines 同口径:
// URL/带端口 IP/域名:端口 → service;纯 IP → host;*.域/点分域名 →
// subdomain;其余 → other),用于覆盖检查补回行与模型漏给类时的兜底。
func organizeHeuristicClass(title string) string {
	switch {
	case organizeSchemeRe.MatchString(title) || organizeIPPortRe.MatchString(title) ||
		organizeHostPort.MatchString(title):
		return "service"
	case organizeIPRe.MatchString(title):
		return "host"
	case organizeWildRe.MatchString(title) || organizeDomainRe.MatchString(title):
		return "subdomain"
	default:
		return "other"
	}
}
