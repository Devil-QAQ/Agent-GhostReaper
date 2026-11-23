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

type extractReply struct {
	Title          string           `json:"title,omitempty"`
	TaskType       string           `json:"task_type,omitempty"` // 任务类型预选(P4-44:按描述倾向,可改)
	Assets         []extractAssetIn `json:"assets"`
	ExcludedAssets []extractAssetIn `json:"excluded_assets"` // 初始摘除候选(P4-53 ③;仅预填,确认才落库)
	Goals          []extractGoalIn  `json:"goals"`
	CreatorNotes   []string         `json:"creator_notes"` // 注意事项候选(P4-49;仅预填,确认才落库)
	Issues         []string         `json:"issues"`        // 带问题被丢弃/修正的条目(如实呈现,不静默丢)
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
	return `从操作者的任务描述提取可编辑的建任务内容，只输出JSON对象：
{"title":"任务标题","task_type":"pentest|redteam","assets":[{"class":"domain|host|service|app|miniprogram","title":"对象标识"}],"excluded_assets":[{"class":"domain|host|service","title":"不要测试的地址"}],"goals":[{"title":"目标","pred":"原文明确的完成判据"}],"creator_notes":["操作者的要求"]}
只提取原文明确表达的目标、对象和要求，不补造授权、动作、目标、次数或完成条件。没有内容的数组留空。
域名和子域名均为domain，IP为host，完整URL或地址:端口为service，API路径归所属服务。组织、人员、登记资料、文件及泄露内容是背景，不自动加入资产；明确作为任务对象的独立应用或小程序保留其标识。
原文明确不要测试的域名、IP或网址放excluded_assets；同一对象既肯定又否定时保留在assets，并在注意事项中如实指出原文冲突，不代操作者扩权。
做法偏好、允许或禁止的动作、次数要求全部保留为creator_notes中的普通文字，注明原文中的对象和数字；不生成正则、程序规则或额外审批门槛。
目标描述最终要取得的产出，pred只记录原文明示的可核验完成条件，没有则留空，不把执行步骤当目标。
根据原文预选task_type：单个明确站点或对象为pentest，明确要求全面或多个目标为redteam，含糊时pentest。两类都走相同的动作判断和新资产加入流程。`
}

// parseTaskExtract 解析并校验模型回复(容忍围栏与前后杂文);非法条目
// 丢弃并记入 issues(不静默丢)。生成质量由操作者终审(面板逐条确认)。
// goals 容忍对象 {title,pred} 与裸字符串两种形态(模型遵守度不一,
// 裸串按无判定口径建议收)。
func parseTaskExtract(reply string) (*extractReply, []string) {
	var issues []string
	out := &extractReply{Assets: []extractAssetIn{}, ExcludedAssets: []extractAssetIn{}, Goals: []extractGoalIn{}, CreatorNotes: []string{}}
	raw := strings.TrimSpace(reply)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return out, []string{"回复里没有 JSON 对象,提取为空(可重试或手填)"}
	}
	var parsed struct {
		Title          string            `json:"title"`
		TaskType       string            `json:"task_type"`
		Assets         []extractAssetIn  `json:"assets"`
		ExcludedAssets []extractAssetIn  `json:"excluded_assets"`
		Goals          []json.RawMessage `json:"goals"`
		CreatorNotes   json.RawMessage   `json:"creator_notes"`
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
