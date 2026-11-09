// Package server 的建任务"从描述提取"(P2-08f ①):粘贴一段授权描述 →
// 单发 LLM 调用(小预算、跑完即弃,不建会话不落任务账本)提取结构化
// 目标 / 操作约束 / 种子资产 → 面板回填可编辑,人工确认后才随建任务
// 落库。反向规则:描述里没有的约束不编造——提示词与校验双层把守,
// 提取结果仅供参考,权威在操作者的确认。
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

	"ghostreaper/internal/rings"
)

// extractAssetIn / extractConstraintIn / extractGoalIn 提取结果的候选项
// (面板回填形态)。目标的 pred 是判定口径建议(P3-14b ④:引导生成可
// 判定完成条件,一句话说清该目标怎样算完成;描述没给判据时留空,
// 不编造——建议仅供参考,正式完成条件在 RoE 目标表单锁定)。
type extractAssetIn struct {
	Class string `json:"class"`
	Title string `json:"title"`
}

type extractGoalIn struct {
	Title string `json:"title"`
	Pred  string `json:"pred,omitempty"`
}

type extractConstraintIn struct {
	Action      string   `json:"action"`
	Disposition string   `json:"disposition"` // forbid|allow
	Quota       int      `json:"quota,omitempty"`
	Assets      []string `json:"assets,omitempty"` // 范围(资产标识;空 = 全部)
	Patterns    []string `json:"patterns,omitempty"`
	Basis       string   `json:"basis,omitempty"` // 描述原文依据(人工核对用)
}

type extractReply struct {
	Title          string                `json:"title,omitempty"`
	TaskType       string                `json:"task_type,omitempty"` // 任务类型预选(P4-44:按描述倾向,可改)
	Assets         []extractAssetIn      `json:"assets"`
	ExcludedAssets []extractAssetIn      `json:"excluded_assets"` // 初始摘除候选(P4-53 ③;仅预填,确认才落库)
	Goals          []extractGoalIn       `json:"goals"`
	CreatorNotes   []string              `json:"creator_notes"` // 注意事项候选(P4-49;仅预填,确认才落库)
	Constraints    []extractConstraintIn `json:"constraints"`
	Issues         []string              `json:"issues"` // 带问题被丢弃/修正的条目(如实呈现,不静默丢)
}

// handleTaskExtract 建任务前的描述提取(单发短调用;跑完即弃)。
// POST /api/tasks/extract {text}
func (s *Server) handleTaskExtract(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, errors.New("授权描述不能为空"))
		return
	}
	if len([]rune(text)) > 8000 {
		writeErr(w, http.StatusBadRequest, errors.New("描述过长(上限 8000 字),拆分后分次提取"))
		return
	}
	if s.Chain == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("模型未配置(LLM配置页激活模型后可提取);也可手填表单"))
		return
	}
	prov := llm.Provider(s.Chain)
	cctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	msg, _, _, err := prov.Complete(cctx, llm.CompletionRequest{
		System: []string{taskExtractSystem()},
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: llm.BlockText, Text: text}}},
		},
		MaxTokens: 4096,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("提取失败: %w", err))
		return
	}
	reply, issues := parseTaskExtract(msg.Text())
	reply.Issues = append(reply.Issues, issues...)
	writeJSON(w, http.StatusOK, reply)
}

// taskExtractSystem 提取提示词:结构化目标(带判定口径建议)/注意事项/
// 操作约束/种子资产/初始摘除(P4-53 ③范围语句分流)/任务类型倾向
// (P4-44);反向规则(不编造)逐条钉死。注意事项(P4-49 ②)与范围分流
// (P4-53 ③)均凭语义辨认不靠标志词,正反例定口径。
func taskExtractSystem() string {
	return `你是渗透测试任务建单的提取器。操作者粘贴一段授权描述,你从中提取建任务表单的候选内容,输出且只输出一个 JSON 对象(不要围栏、不要解释):
{"title":"推断的任务标题(15字内)","task_type":"pentest|redteam","assets":[{"class":"资产类","title":"资产标识"}],"excluded_assets":[{"class":"资产类","title":"资产标识"}],"goals":[{"title":"目标","pred":"判定口径"}],"creator_notes":["注意事项"],"constraints":[{"action":"动作描述","disposition":"forbid|allow","quota":N,"assets":["资产标识"],"patterns":["RE2正则"],"basis":"描述里的原句"}]}
提取纪律(违反即失职):
- 只提取描述明确表达的内容,一律不编造、不推测、不补全:描述没写的约束、目标、次数上限一律不出现。
- task_type 按描述倾向二选一:只针对单个站点/网址/域名("对这个网站""测这个系统")→ pentest(渗透任务:范围外发现的相邻资产待批准才可测);全面评估/整站/内网横向/多目标/给 IP 段或一批 IP("全面""整站""这些 IP""内网")→ redteam(红队任务:发现的相邻资产直接可测)。倾向不明默认 pentest。
- 资产类 12 词:domain 域名 / subdomain 子域名 / host 主机 / service 服务 / api 接口 / organization 组织 / miniprogram 小程序 / app 应用 / wechat_account 公众号 / person 人员 / leak_info 泄露信息 / other 其他。URL、IP:端口 → service;纯 IP → host;点分域名 → subdomain 或 domain。
- 范围语句分流(assets 与 excluded_assets 的取舍,凭语义辨认):
  肯定范围("只测 X""就测 X""目标只有 X")→ X 进 assets;
  否定范围("X 不要测""X 不许动""除 X 外都测"语义里被排除的 X)→ X 进 excluded_assets;
  泛指排除转写为通配写法:"其他子域不要测"(配 aaa.x.com 类目标)→ "*.x.com" 进 excluded_assets;
  同一目标既被肯定又被否定时以肯定为准(进 assets),不进 excluded_assets;
  边界只认主机目标(域名/IP/网址),"不要改数据"这类打法要求不进 excluded_assets(归 creator_notes 或 constraints);
  范围语句不再重复提取为 constraints——范围归范围(assets/excluded_assets),动作约束归 constraints("只测 X""X 不要测"不是动作描述)。
  正例:"只测 aaa.x.com,其他子域不要测" → assets=[aaa.x.com]、excluded_assets=["*.x.com"],constraints 不收这两句;
  正例:"bbb.x.com 和 10.0.0.8 不要测" → excluded_assets=[bbb.x.com, 10.0.0.8];
  正例:"不允许上传,爆破最多 20 次" → constraints(动作约束,与范围无关);
  反例:"不要修改任何数据" → 不进 excluded_assets(进 creator_notes);
  描述没有否定范围就给空数组,不硬凑。
- goals = 可核验的最终目标(描述里的任务目的),不是步骤。每条目标尽量写成可判定的完成条件:给出明确的完成判据(拿到什么产出、达到什么数量或状态算完成,如"提取 users 表全部账号与口令哈希"),不用"评估/了解/摸清"这类说不清何时算完成的表述。
- pred 用一句话写清该目标怎样算完成(判定口径,面向证据:产出物、数量、可观察状态);判据只能来自描述里的任务目的,描述没给就留空,不编造。
- creator_notes = 注意事项(对做法的软要求:怎么测的要求与偏好),凭语义辨认,不靠"不要/禁止"这类标志词——只要句子在对做法提要求或表达偏好就提取,每条一句话、贴描述原意转写、不添义:
  正例:"用小字典就行" → 提取(如"爆破用小字典"——无标志词的做法偏好照样认);
  正例:"站很脆弱,温柔点" → 提取(转写为"目标脆弱,动作放轻");
  正例:"先只读探测,别改任何数据" → 提取(可拆"先只读探测""不要修改任何数据"两条);
  反例:"拿下管理权限"(说要拿到什么 = 目标) → 只进 goals,不进 creator_notes;
  反例:"测试 example.com 这个站点"(背景陈述,不对做法提要求) → 两者都不进;
  描述没表达做法要求就留空数组,不硬凑。
- 约束 = 授权边界(只许/不许做什么):disposition 用 forbid(禁止)或 allow(允许);quota 只在描述明确给了次数数字时填,否则不填;范围 assets 引用提取出的资产标识,描述未限定到具体资产的留空(= 全部)。
- patterns 是该动作的命令参数匹配正则(RE2 语法,无前后瞻,留意大小写加 (?i)),匹配"执行该动作的命令文本"的典型形态(bash 的 curl/sqlmap/hydra 等);生成不了就不填。
- 每条约束带 basis(描述原句截取),供操作者核对是不是真的写了。
- 描述含糊或没有可提取项的数组就留空,不要硬凑。`
}

// parseTaskExtract 解析并校验模型回复(容忍围栏与前后杂文);非法条目
// 丢弃并记入 issues(不静默丢)。生成质量由操作者终审(面板逐条确认)。
// goals 容忍对象 {title,pred} 与裸字符串两种形态(模型遵守度不一,
// 裸串按无判定口径建议收)。
func parseTaskExtract(reply string) (*extractReply, []string) {
	var issues []string
	out := &extractReply{Assets: []extractAssetIn{}, ExcludedAssets: []extractAssetIn{}, Goals: []extractGoalIn{}, CreatorNotes: []string{}, Constraints: []extractConstraintIn{}}
	raw := strings.TrimSpace(reply)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return out, []string{"回复里没有 JSON 对象,提取为空(可重试或手填)"}
	}
	var parsed struct {
		Title          string                `json:"title"`
		TaskType       string                `json:"task_type"`
		Assets         []extractAssetIn      `json:"assets"`
		ExcludedAssets []extractAssetIn      `json:"excluded_assets"`
		Goals          []json.RawMessage     `json:"goals"`
		CreatorNotes   json.RawMessage       `json:"creator_notes"`
		Constraints    []extractConstraintIn `json:"constraints"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return out, []string{"JSON 解析失败,提取为空(可重试或手填): " + err.Error()}
	}
	if t := strings.TrimSpace(parsed.Title); t != "" && len([]rune(t)) <= 64 {
		out.Title = t
	}
	// 任务类型倾向(P4-44):词表外的值丢弃如实入 issues(仅预选,表单可改)。
	if tt := strings.TrimSpace(parsed.TaskType); tt != "" {
		if rings.IsValidTaskType(tt) {
			out.TaskType = tt
		} else {
			issues = append(issues, fmt.Sprintf("任务类型 %q 不在词表内(须 pentest|redteam),已丢弃,默认渗透", tt))
		}
	}
	seen := map[string]bool{}
	for _, a := range parsed.Assets {
		title := strings.TrimSpace(a.Title)
		class := ringsAssetClass(strings.TrimSpace(a.Class))
		if title == "" || class == "" {
			issues = append(issues, fmt.Sprintf("资产 %q(类 %q)标识为空或类不在 12 类词表,已丢弃", a.Title, a.Class))
			continue
		}
		if seen[title] {
			continue
		}
		seen[title] = true
		out.Assets = append(out.Assets, extractAssetIn{Class: class, Title: title})
	}
	// 初始摘除候选(P4-53 ③):与资产同款校验;锚点与已收资产相同的
	//(同一目标既肯定又否定)以肯定为准,丢弃入 issues;条目间锚点级
	// 去重(x.com 与 X.COM 同一对象)。
	assetAnchors := map[string]bool{}
	for _, a := range out.Assets {
		if an, ok := rings.ScopeAnchorOf(a.Title); ok {
			assetAnchors[scopeAnchorKey(an)] = true
		}
	}
	seenExAnchor := map[string]bool{}
	for _, a := range parsed.ExcludedAssets {
		title := strings.TrimSpace(a.Title)
		class := ringsAssetClass(strings.TrimSpace(a.Class))
		if title == "" || class == "" {
			issues = append(issues, fmt.Sprintf("不要测的资产 %q(类 %q)标识为空或类不在 12 类词表,已丢弃", a.Title, a.Class))
			continue
		}
		anchor, hasAnchor := rings.ScopeAnchorOf(title)
		if hasAnchor {
			key := scopeAnchorKey(anchor)
			if assetAnchors[key] {
				issues = append(issues, fmt.Sprintf("不要测的资产 %q 与要测的资产为同一目标,以资产栏为准,未预填摘除", title))
				continue
			}
			if seenExAnchor[key] {
				continue
			}
			seenExAnchor[key] = true
		}
		out.ExcludedAssets = append(out.ExcludedAssets, extractAssetIn{Class: class, Title: title})
	}
	for _, g := range parsed.Goals {
		title, pred := "", ""
		if len(g) > 0 && g[0] == '"' {
			_ = json.Unmarshal(g, &title)
		} else {
			var obj extractGoalIn
			if err := json.Unmarshal(g, &obj); err != nil {
				continue // 非法形态静默跳过(非空 title 的收集在下)
			}
			title, pred = obj.Title, obj.Pred
		}
		if title = strings.TrimSpace(title); title != "" && len([]rune(title)) <= 200 {
			pred = strings.TrimSpace(pred)
			if r := []rune(pred); len(r) > 200 {
				pred = string(r[:200])
			}
			out.Goals = append(out.Goals, extractGoalIn{Title: title, Pred: pred})
		}
	}
	// 注意事项(P4-49 ②):数组为主形态;模型偶发单串时按一条收
	//(多行串拆行)。逐条修剪、去空、去重、限 200 字(超长丢弃入
	// issues,与目标同款如实呈现)。
	if len(parsed.CreatorNotes) > 0 {
		var candidates []string
		if parsed.CreatorNotes[0] == '[' {
			var arr []string
			if err := json.Unmarshal(parsed.CreatorNotes, &arr); err == nil {
				candidates = arr
			}
		} else if parsed.CreatorNotes[0] == '"' {
			var one string
			if err := json.Unmarshal(parsed.CreatorNotes, &one); err == nil {
				candidates = strings.Split(one, "\n")
			}
		}
		seenNote := map[string]bool{}
		for _, n := range candidates {
			n = strings.TrimSpace(n)
			if n == "" || seenNote[n] {
				continue
			}
			if len([]rune(n)) > 200 {
				issues = append(issues, fmt.Sprintf("注意事项 %q 超过 200 字,已丢弃", clipExtract(n, 30)))
				continue
			}
			seenNote[n] = true
			out.CreatorNotes = append(out.CreatorNotes, n)
		}
	}
	for _, c := range parsed.Constraints {
		action := strings.TrimSpace(c.Action)
		disp := strings.TrimSpace(c.Disposition)
		if action == "" || (disp != "forbid" && disp != "allow") {
			issues = append(issues, fmt.Sprintf("拦截规则 %q 处置 %q 非法(须 forbid|allow),已丢弃", c.Action, c.Disposition))
			continue
		}
		quota := c.Quota
		if quota < 0 || (disp == "forbid" && quota > 0) {
			if quota != 0 {
				issues = append(issues, fmt.Sprintf("拦截规则 %q 的次数上限不适用于禁止,已去除", action))
			}
			quota = 0
		}
		var patterns []string
		for _, p := range c.Patterns {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if _, err := regexp.Compile(p); err != nil {
				issues = append(issues, fmt.Sprintf("拦截规则 %q 的匹配模式 %q 编译不过,已丢弃", action, p))
				continue
			}
			patterns = append(patterns, p)
		}
		var assets []string
		for _, a := range c.Assets {
			if a = strings.TrimSpace(a); a != "" {
				assets = append(assets, a)
			}
		}
		out.Constraints = append(out.Constraints, extractConstraintIn{
			Action: action, Disposition: disp, Quota: quota,
			Assets: assets, Patterns: patterns, Basis: strings.TrimSpace(c.Basis),
		})
	}
	return out, issues
}

// ringsAssetClass 面板资产类词表(12 类;引用地基层常量,不漂移)。
func ringsAssetClass(c string) string {
	for k := range assetClassWords {
		if string(k) == c {
			return c
		}
	}
	return ""
}

// clipExtract 超长条目入 issues 时的截头(人话呈现,不撑爆提示行)。
func clipExtract(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
