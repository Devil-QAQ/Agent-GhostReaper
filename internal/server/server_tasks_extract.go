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
	Title       string                `json:"title,omitempty"`
	Assets      []extractAssetIn      `json:"assets"`
	Goals       []extractGoalIn       `json:"goals"`
	Constraints []extractConstraintIn `json:"constraints"`
	Issues      []string              `json:"issues"` // 带问题被丢弃/修正的条目(如实呈现,不静默丢)
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

// taskExtractSystem 提取提示词:结构化目标(带判定口径建议)/操作约束/
// 种子资产;反向规则(不编造)逐条钉死。
func taskExtractSystem() string {
	return `你是渗透测试任务建单的提取器。操作者粘贴一段授权描述,你从中提取建任务表单的候选内容,输出且只输出一个 JSON 对象(不要围栏、不要解释):
{"title":"推断的任务标题(15字内)","assets":[{"class":"资产类","title":"资产标识"}],"goals":[{"title":"目标","pred":"判定口径"}],"constraints":[{"action":"动作描述","disposition":"forbid|allow","quota":N,"assets":["资产标识"],"patterns":["RE2正则"],"basis":"描述里的原句"}]}
提取纪律(违反即失职):
- 只提取描述明确表达的内容,一律不编造、不推测、不补全:描述没写的约束、目标、次数上限一律不出现。
- 资产类 12 词:domain 域名 / subdomain 子域名 / host 主机 / service 服务 / api 接口 / organization 组织 / miniprogram 小程序 / app 应用 / wechat_account 公众号 / person 人员 / leak_info 泄露信息 / other 其他。URL、IP:端口 → service;纯 IP → host;点分域名 → subdomain 或 domain。
- goals = 可核验的最终目标(描述里的任务目的),不是步骤。每条目标尽量写成可判定的完成条件:给出明确的完成判据(拿到什么产出、达到什么数量或状态算完成,如"提取 users 表全部账号与口令哈希"),不用"评估/了解/摸清"这类说不清何时算完成的表述。
- pred 用一句话写清该目标怎样算完成(判定口径,面向证据:产出物、数量、可观察状态);判据只能来自描述里的任务目的,描述没给就留空,不编造。
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
	out := &extractReply{Assets: []extractAssetIn{}, Goals: []extractGoalIn{}, Constraints: []extractConstraintIn{}}
	raw := strings.TrimSpace(reply)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return out, []string{"回复里没有 JSON 对象,提取为空(可重试或手填)"}
	}
	var parsed struct {
		Title       string                `json:"title"`
		Assets      []extractAssetIn      `json:"assets"`
		Goals       []json.RawMessage     `json:"goals"`
		Constraints []extractConstraintIn `json:"constraints"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return out, []string{"JSON 解析失败,提取为空(可重试或手填): " + err.Error()}
	}
	if t := strings.TrimSpace(parsed.Title); t != "" && len([]rune(t)) <= 64 {
		out.Title = t
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
	for _, c := range parsed.Constraints {
		action := strings.TrimSpace(c.Action)
		disp := strings.TrimSpace(c.Disposition)
		if action == "" || (disp != "forbid" && disp != "allow") {
			issues = append(issues, fmt.Sprintf("约束 %q 处置 %q 非法(须 forbid|allow),已丢弃", c.Action, c.Disposition))
			continue
		}
		quota := c.Quota
		if quota < 0 || (disp == "forbid" && quota > 0) {
			if quota != 0 {
				issues = append(issues, fmt.Sprintf("约束 %q 的次数上限不适用于禁止,已去除", action))
			}
			quota = 0
		}
		var patterns []string
		for _, p := range c.Patterns {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if _, err := regexp.Compile(p); err != nil {
				issues = append(issues, fmt.Sprintf("约束 %q 的匹配模式 %q 编译不过,已丢弃", action, p))
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
