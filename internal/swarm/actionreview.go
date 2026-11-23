package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// ActionReview is the semantic decision for one concrete tool invocation.
// Hard rules run before this decision. Approval remains a human decision and
// never grants a host, rule group, or entire step permission to skip review.
type ActionReview struct {
	Permission *rings.PurposePermission `json:"permission,omitempty"`
	Decision   string                   `json:"decision"`
	Reason     string                   `json:"reason"`
}

const (
	ActionAllow    = "allow"
	ActionApproval = "approval"
	ActionDeny     = "deny"
)

type ActionReviewInput struct {
	TaskType   string          `json:"task_type"`
	Goal       string          `json:"goal,omitempty"`
	Intent     string          `json:"intent,omitempty"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Boundaries ActionBoundary  `json:"boundaries"`
}

// ActionBoundary carries the current operator policy, not a model's claim of
// authorization. Asset membership and permission for this action are separate.
type ActionTargetScope struct {
	Host     string `json:"host"`
	Ports    string `json:"ports"`
	Wildcard bool   `json:"wildcard,omitempty"`
}

type ActionBoundary struct {
	TargetScope       []ActionTargetScope       `json:"target_scope,omitempty"`
	TaskDescription   string                    `json:"task_description"`
	Goals             []string                  `json:"goals"`
	AllowedTools      []string                  `json:"allowed_tools,omitempty"`
	DeniedTools       []string                  `json:"denied_tools,omitempty"`
	Relationships     []rings.AssetRelationView `json:"asset_relationships,omitempty"`
	InfoSources       []rings.RoeInfoSource     `json:"information_sources,omitempty"`
	Notes             string                    `json:"notes,omitempty"`
	AuthorizedTargets []AssetBrief              `json:"authorized_targets,omitempty"`
	Assets            []AssetBrief              `json:"assets"`
	ExcludedAssets    []AssetBrief              `json:"excluded_assets,omitempty"`
	ApprovalEpoch     int64                     `json:"approval_epoch"`
	Approvals         []rings.ApprovalRow       `json:"prior_approvals,omitempty"`
}

type ActionReviewFunc func(context.Context, ActionReviewInput) (ActionReview, error)

// Only these local categories may cross into recovery logs. Provider responses
// and tool arguments must never be included in a model-fault message.
type actionReviewError string

func (e actionReviewError) Error() string { return string(e) }

// Current rules are exactly the rules exposed by the panel. An empty list is
// valid (including deletion of every built-in rule); it must not resurrect an
// invisible fallback set. A broken rule/configuration is an execution error.
func matchHardRule(ctx context.Context, store *rings.Store, tool string, args json.RawMessage) (HardRule, string, error) {
	rules, err := ListHardRules(ctx, store)
	if err != nil {
		return HardRule{}, "", errors.New("无法读取当前硬规则")
	}
	for _, r := range rules {
		if !r.Enabled || (r.ToolScope != "" && r.ToolScope != tool) {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return HardRule{}, "", fmt.Errorf("硬规则 %s 配置无效", r.ID)
		}
		for _, value := range argStringValuesSlice(args) {
			if loc := re.FindStringIndex(value); loc != nil {
				return r, value[loc[0]:loc[1]], nil
			}
		}
	}
	return HardRule{}, "", nil
}

const actionReviewPrompt = `你负责判断任务中一次实际工具调用是否命中操作者规定的审批边界。默认allow；默认需要审批的只有：对测试对象产生实际新增、删除、修改。测试对象不包含执行环境中的临时测试产物和工作文件。不要建立允许操作分类，不因“未逐项授权”、风险词、命令名称、HTTP方法、参数变化或步骤分工要求审批，也不能自行增设边界。操作者明确的范围和限额也是黑名单边界，未满足明文限制或无法核实有条件许可时不得假定满足；不能因为未增删改就忽略操作者的明文要求。操作者明确提供的禁止项优先，实际命中时输出deny直接拒绝，不转成人工审批，也不被旧批准覆盖；明确要求审批的事项按原文处理。未命中这些黑名单项不能自行增加限制或审批条件。
任务原文task_description、当前目标goals、注意事项notes和工具限制来自操作者；authorized_targets是人工选定目标，target_scope是其主机范围，ports=all表示该主机全部端口。URL的端口、路径只是入口定位，不能缩小已经给出的目标范围；只有操作者明确写出的端口/路径限制才是限制。assets和asset_relationships是分类与证据关系，不是额外审批条件。人工加入表示纳入任务目标；自动归档不授予额外行为权限，不加入也不等于禁止资料访问。
只根据tool、arguments及实际执行内容判断直接目的和影响，不把步骤计划或文件中出现的地址当成正在执行的动作。按真实业务效果识别增删改，伴随的日志、会话等变化不作为增删改理由；不能只因为用了POST就认定修改。执行既有脚本而实际源码不可见时，你必须先调用read_context_file读取脚本及必要导入源码；它是可用的只读上下文工具，不是在执行被审查动作。读取源码不等于执行源码，不得把可自行核查的内容交给操作者确认，不根据文件名猜效果。
prior_approvals是未撤回的人工批准。已有permission时，比较实际动作与批准的purpose、targets、effects；同一目的、同目标范围、同类影响直接allow，换命令、工具、任意字段都不是重新审批的理由。只有真实目的、目标或影响超出该批准时才重新判断。旧记录没有permission时，结合原调用及批准备注理解，不假定旧脚本内容。approval_epoch只是授权编辑时间，资产更新不使批准失效；当前明确限制和撤回优先。permission只是目的与影响的摘要，不是模型追加限制的载体；原调用中具体参数值即使被写入摘要也只作示例定位，不能据此缩成一次性许可，只有操作者原文或人工批准备注明确限定的值才构成约束。
命中尚未批准的增删改时输出approval，reason说明实际增删改；同时给出permission：actions只填create/update/delete的实际类别，resource只填业务对象类型，不能填具体记录、用户名、参数值或数量；targets只填所属网站入口或系统标识，不填某个业务记录。purpose和effects概括本类目的与影响，不限定这次实例。程序以actions与resource记录本类许可，操作者另写的批准备注才是额外限制。不要把某条命令、单个参数值、临时路径或payload抄成一次性许可，也不扩大成任意目标任意行为。操作者明确免审授权或已有适用批准时沿用，不重复提问。程序已执行当前启用的硬规则，命中直接拒绝，不能由你或批准覆盖。
操作者自己写出的“仅限”“最多”“必须满足后才可”等要求是实际边界；依赖的事实缺失时不能假定已满足，应deny说明缺失依据，不另造人工审批项目。
最终只输出一个JSON对象，decision为allow、deny或approval。allow和deny只需reason；approval必须附permission：{"decision":"approval","reason":"本次实际命中审批边界的原因","permission":{"actions":["create"],"resource":"业务对象类型","purpose":"本类目的","targets":["网站入口或系统标识"],"effects":"实际增删改及影响"}}。除read_context_file外不执行工具、不规划新方向、不创建资产。`

func ActionReviewFor(provider llm.Provider, readers ...func(string) ([]byte, error)) ActionReviewFunc {
	return func(ctx context.Context, in ActionReviewInput) (ActionReview, error) {
		if provider == nil {
			return ActionReview{}, actionReviewError("动作判断无可用模型")
		}
		// A worker plan is neither an observed effect nor operator authority.
		in.Goal, in.Intent = "", ""
		in.Boundaries.Approvals = append([]rings.ApprovalRow(nil), in.Boundaries.Approvals...)
		for i := range in.Boundaries.Approvals {
			in.Boundaries.Approvals[i].Intent = ""
			if in.Boundaries.Approvals[i].Permission != nil {
				// A structured grant is purpose authority. Its original command
				// stays in the audit ledger, not as a second narrower boundary.
				in.Boundaries.Approvals[i].ToolName = ""
				in.Boundaries.Approvals[i].ToolArgs = ""
				in.Boundaries.Approvals[i].Note = ""
			}
		}
		if len(in.Boundaries.TargetScope) == 0 {
			in.Boundaries.TargetScope = actionTargetScopes(in.Boundaries.AuthorizedTargets)
		}
		body, err := json.Marshal(in)
		if err != nil {
			return ActionReview{}, actionReviewError("动作判断参数无效")
		}
		messages := []llm.Message{llm.UserText(string(body))}
		var schemas []llm.ToolSchema
		if len(readers) > 0 {
			schemas = []llm.ToolSchema{{Name: "read_context_file", Description: "只读本次命令实际引用的脚本及导入源码。路径按实际工作目录解析，不得读取无关文件。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}}}
		}
		hashes := map[string][32]byte{}
		for round := 0; round < 9; round++ {
			msg, _, _, err := provider.Complete(ctx, llm.CompletionRequest{System: []string{actionReviewPrompt}, Messages: messages, Tools: schemas, MaxTokens: 1000, Thinking: "disabled", Temperature: new(float64)})
			if err != nil {
				return ActionReview{}, actionReviewError("动作判断调用未完成")
			}
			uses := msg.ToolUses()
			if len(uses) == 0 {
				for path, h := range hashes {
					b, e := readers[0](path)
					if e != nil || sha256.Sum256(b) != h {
						return ActionReview{}, actionReviewError("判断期间脚本已变化")
					}
				}
				verdict, err := parseActionReview(msgText(msg))
				if err == actionReviewError("动作判断未给出可批准的目的范围") && round < 8 {
					messages = append(messages, msg, llm.UserText("你的approval缺少完整permission。请给出actions(create/update/delete)、resource(业务对象类型，不能含具体实例值)、targets(网站/系统)，以及purpose与effects；这是本类目的的可复用批准，不限定原调用字段值。返回完整JSON，不新增限制。"))
					continue
				}
				return verdict, err
			}
			if len(readers) == 0 {
				return ActionReview{}, actionReviewError("动作判断请求了不可用工具")
			}
			messages = append(messages, msg)
			results := llm.Message{Role: llm.RoleUser}
			for _, use := range uses {
				var args struct {
					Path string `json:"path"`
				}
				var data []byte
				var e error
				if use.Name != "read_context_file" || json.Unmarshal(use.Input, &args) != nil || len(hashes) >= 8 {
					e = errors.New("上下文读取请求无效或超过上限")
				} else {
					data, e = readers[0](args.Path)
				}
				if e != nil {
					return ActionReview{}, actionReviewError("动作判断所需源码不可读取")
				}
				text := ""
				if e == nil {
					hashes[args.Path] = sha256.Sum256(data)
					text = string(data)
				}
				results.Content = append(results.Content, llm.ToolResultText(use.ID, text, e != nil))
			}
			messages = append(messages, results)
		}
		return ActionReview{}, actionReviewError("动作判断上下文读取超过上限")
	}
}

func parseActionReview(text string) (ActionReview, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		header, body, ok := strings.Cut(text, "\n")
		if ok && (strings.TrimSpace(header) == "```" || strings.EqualFold(strings.TrimSpace(header), "```json")) {
			text = strings.TrimSpace(strings.TrimSuffix(body, "```"))
		}
	}
	var out ActionReview
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return ActionReview{}, actionReviewError("动作判断返回格式无效")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ActionReview{}, actionReviewError("动作判断返回了多个结果")
	}
	out.Reason = strings.TrimSpace(out.Reason)
	if (out.Decision != ActionAllow && out.Decision != ActionApproval && out.Decision != ActionDeny) || out.Reason == "" || utf8.RuneCountInString(out.Reason) > 800 {
		return ActionReview{}, actionReviewError("动作判断结论或依据无效")
	}
	if out.Decision == ActionApproval && out.Permission == nil {
		return ActionReview{}, actionReviewError("动作判断未给出可批准的目的范围")
	}
	if p := out.Permission; p != nil {
		if len(p.Actions) == 0 || strings.TrimSpace(p.Resource) == "" {
			return ActionReview{}, actionReviewError("动作判断未给出可批准的目的范围")
		}
		words := []string{}
		seen := map[string]bool{}
		for _, action := range p.Actions {
			word := map[string]string{"create": "新增", "update": "修改", "delete": "删除"}[action]
			if word == "" {
				return ActionReview{}, actionReviewError("批准范围无效")
			}
			if !seen[action] {
				seen[action] = true
				words = append(words, word)
			}
		}
		p.Purpose = strings.Join(words, "、") + strings.TrimSpace(p.Resource)
		p.Effects = p.Purpose
		for i, target := range p.Targets {
			if _, _, _, err := rings.AssetIdentity(rings.AssetService, target); err == nil {
				p.Targets[i] = rings.AssetDisplayTitle(rings.AssetService, target)
			}
		}
		if strings.TrimSpace(p.Purpose) == "" || strings.TrimSpace(p.Effects) == "" || len(p.Targets) == 0 || len(p.Targets) > 16 || len(p.Purpose) > 1200 || len(p.Effects) > 2400 {
			return ActionReview{}, actionReviewError("批准范围无效")
		}
		for _, target := range p.Targets {
			if strings.TrimSpace(target) == "" || len(target) > 1200 {
				return ActionReview{}, actionReviewError("批准目标无效")
			}
		}
	}
	return out, nil
}

func (env *unitEnv) actionBoundary(ctx context.Context, pack *TaskPack, roe rings.RoeState) (ActionBoundary, error) {
	out := ActionBoundary{AllowedTools: roe.AllowedTools, DeniedTools: roe.DeniedTools,
		Notes: rings.PolicyNotes(roe.CreatorNotes, roe.IntentRules), InfoSources: roe.InfoSources, Assets: []AssetBrief{}, Goals: []string{}}
	detail, err := env.Executor.Store.CreatedDetail(ctx, pack.TaskID)
	if err != nil {
		return out, errors.New("无法读取任务原文")
	}
	out.TaskDescription, _ = detail["description"].(string)
	goals, err := env.Executor.Store.NodesByType(ctx, pack.TaskID, rings.NodeGoal, "")
	if err != nil {
		return out, errors.New("无法读取当前任务目标")
	}
	floor := roe.UpdatedRev
	for _, g := range goals {
		if g.UpdatedRev > floor {
			floor = g.UpdatedRev
		}
		out.Goals = append(out.Goals, g.Title)
	}
	assets, err := env.Executor.Store.NodesByType(ctx, pack.TaskID, rings.NodeAsset, "")
	if err != nil {
		return out, errors.New("无法读取当前资产边界")
	}
	graph, err := env.Executor.Store.AssetGraph(ctx, pack.TaskID)
	if err != nil {
		return out, errors.New("无法读取资产关系")
	}
	out.Relationships = graph.Relations
	excluded := rings.ExcludedCoveredAssetIDs(assets, roe.ExcludedAssetSet())
	members := rings.InventoryAssetIDs(assets, roe)
	for _, a := range assets {
		if a.Status == "retired" && !roe.ExcludedAssetSet()[a.ID] {
			continue
		}
		brief := AssetBrief{ID: a.ID, Title: a.Title, Class: string(a.AssetClass)}
		operatorTarget, _ := a.Detail["operator_target"].(bool)
		if (rings.AssetSourceOf(a.Detail) != rings.AssetSourceDiscovered || operatorTarget) && !excluded[a.ID] {
			out.AuthorizedTargets = append(out.AuthorizedTargets, brief)
		}
		if roe.ExcludedAssetSet()[a.ID] {
			out.ExcludedAssets = append(out.ExcludedAssets, brief)
		} else if members[a.ID] {
			out.Assets = append(out.Assets, brief)
		}
	}
	rows, err := env.Executor.Store.ApprovalRows(ctx, pack.TaskID)
	if err != nil {
		return out, errors.New("无法读取人工审批依据")
	}
	contexts, err := env.Executor.Store.ApprovalContexts(ctx, pack.TaskID)
	if err != nil {
		return out, errors.New("无法读取批准时的脚本依据")
	}
	latest := map[string]rings.ApprovalRow{}
	for _, r := range rows {
		latest[r.RequestID] = r
	}
	// Legacy explicit joins also define chosen task targets. Ordinary automatic
	// discoveries never enter this authoritative list merely by being present.
	authorized := map[string]bool{}
	for _, a := range out.AuthorizedTargets {
		authorized[a.ID] = true
	}
	for _, row := range latest {
		if row.Type != rings.ApprovalRecAssetAdded || row.DecidedBy != "creator" {
			continue
		}
		var choice struct {
			Class rings.AssetClass `json:"class"`
			Title string           `json:"title"`
		}
		if json.Unmarshal([]byte(row.ToolArgs), &choice) != nil {
			continue
		}
		_, _, chosenKey, e := rings.AssetIdentity(choice.Class, choice.Title)
		if e != nil {
			continue
		}
		for _, asset := range assets {
			_, _, key, e := rings.AssetIdentity(asset.AssetClass, asset.Title)
			if e == nil && key == chosenKey && members[asset.ID] && !authorized[asset.ID] {
				out.AuthorizedTargets = append(out.AuthorizedTargets, AssetBrief{ID: asset.ID, Title: asset.Title, Class: string(asset.AssetClass)})
				authorized[asset.ID] = true
			}
		}
	}
	sort.Slice(out.AuthorizedTargets, func(i, j int) bool { return out.AuthorizedTargets[i].ID < out.AuthorizedTargets[j].ID })
	out.TargetScope = actionTargetScopes(out.AuthorizedTargets)
	out.ApprovalEpoch = floor
	for _, r := range rows {
		if r.Type == rings.ApprovalRecApproved && r.DecidedBy == "creator" && latest[r.RequestID].Rev == r.Rev {
			context := contexts[r.RequestID]
			if context.Permission == nil && context.Sources != nil && !env.reviewSourcesUnchanged(context.Sources) {
				continue
			}
			r.Permission = context.Permission
			out.Approvals = append(out.Approvals, r)
		}
	}
	return out, nil
}

func msgText(m llm.Message) string {
	var parts []string
	for _, b := range m.Content {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Source reads are confined to task work files and temporary source files.
func (env *unitEnv) readReviewSource(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(env.Executor.stepDir(env.TaskID, env.StepID), path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	roots := []string{filepath.Join(env.Executor.RootDir, env.TaskID), os.TempDir(), "/tmp"}
	allowed := false
	for _, root := range roots {
		root, _ = filepath.Abs(root)
		root, _ = filepath.EvalSymlinks(root)
		rel, e := filepath.Rel(root, real)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("不属于可读取的工作源码")
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if len(b) > 65536 {
		return nil, errors.New("源码过长")
	}
	if err == nil {
		if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			return nil, errors.New("工作内容不是文本")
		}
		if env.reviewSources == nil {
			env.reviewSources = map[string]string{}
		}
		h := sha256.Sum256(b)
		env.reviewSources[real] = hex.EncodeToString(h[:])
	}
	return b, err
}
func (env *unitEnv) reviewSourcesUnchanged(sources map[string]string) bool {
	if sources == nil {
		return false
	}
	for path, want := range sources {
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		b, err := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if err != nil || len(b) > 65536 {
			return false
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != want {
			return false
		}
	}
	return true
}

func boundaryFingerprint(b ActionBoundary) string {
	b.Approvals = nil
	b.Assets = nil
	b.Relationships = nil
	b.ApprovalEpoch = 0
	raw, _ := json.Marshal(b)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func approvalsStillCurrent(before, after ActionBoundary) bool {
	current := map[string]int64{}
	for _, row := range after.Approvals {
		current[row.RequestID] = row.Rev
	}
	for _, row := range before.Approvals {
		if current[row.RequestID] != row.Rev {
			return false
		}
	}
	return true
}

func boundaryStillCurrent(before, after ActionBoundary) bool {
	oldTargets, newTargets := before.AuthorizedTargets, after.AuthorizedTargets
	before.AuthorizedTargets, after.AuthorizedTargets = nil, nil
	before.TargetScope, after.TargetScope = nil, nil
	if boundaryFingerprint(before) != boundaryFingerprint(after) || !approvalsStillCurrent(before, after) {
		return false
	}
	current := map[AssetBrief]bool{}
	for _, target := range newTargets {
		current[target] = true
	}
	for _, target := range oldTargets {
		if !current[target] {
			return false
		}
	}
	return true
}
func (env *unitEnv) approvalStillActive(ctx context.Context, request string) bool {
	if request == "" {
		return false
	}
	rows, err := env.Executor.Store.ApprovalByRequest(ctx, env.TaskID, request)
	return err == nil && len(rows) > 0 && rows[len(rows)-1].Type == rings.ApprovalRecApproved
}

func approvalScopeNote(review ActionReview) string {
	if review.Permission == nil {
		return review.Reason
	}
	p := review.Permission
	note := review.Reason + "\n目的：" + p.Purpose + "\n目标：" + strings.Join(p.Targets, "、")
	if p.Effects != p.Purpose {
		note += "\n影响：" + p.Effects
	}
	return note + "\n后续同类操作沿用批准。"

}

func actionTargetScopes(targets []AssetBrief) []ActionTargetScope {
	scopes := []ActionTargetScope{}
	seen := map[ActionTargetScope]bool{}
	for _, target := range targets {
		address := rings.AssetEntryAddress(target.Title)
		a, ok := rings.ScopeAnchorOf(address)
		if !ok {
			continue
		}
		// Asset entry syntax identifies a service, not an implicit prohibition of
		// other ports. Explicit operator constraints remain in notes and goals.
		scope := ActionTargetScope{Host: a.Host, Ports: "all", Wildcard: a.Wildcard}
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

const purposeApprovalMeaning = "批准本类目的和同类影响；原调用的命令与参数是实例，不将后续同目的操作限定为相同字段值。"
