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
	Subjects          map[string][]string       `json:"asset_subjects,omitempty"`
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

const actionReviewPrompt = `你负责判断任务中一次实际工具调用的直接目的与业务影响。默认放行；需要审批的只有对测试对象业务对象的实际新增、删除、修改，或操作者原文明示需要审批的事项。测试对象不包括执行环境的临时测试产物和工作文件。用黑名单边界判断，不建立允许清单，不自行增设条件。
changes只记录本次动作直接要求的业务变化。系统处理请求时伴随产生的运行日志、认证记录和会话状态，不是独立的业务增删改；不能把这些伴随结果、风险词或“可能发生”的影响当成申请理由。HTTP方法、命令名称、字段、账号、数量或步骤分工本身不决定审批。
操作者的task_description、goals、notes及工具限制是授权依据。实际命中明文禁止项返回prohibition，不转成人工审批，不被旧批准覆盖；操作者明确给出的条件和限额仍须遵守，不能假定缺失的条件依据已满足。未命中明文限制或业务增删改时，changes为空。
authorized_targets是人工目标，target_scope是其主机范围，ports=all表示全部端口；入口URL的端口和路径不自动缩小授权。assets、asset_relationships只是事实分类与关系，不增加条件。资产加入由独立流程处理，changes不处理资产加入，行为批准也不扩大资产范围。
只看tool、arguments和实际源码，不把步骤计划或文件提及的地址当作正在执行的动作。执行脚本而源码不可见时，先用read_context_file读取脚本及必要导入；读取不是执行，不能把可自行核查的内容交操作者审批，也不能按文件名猜影响。
prior_approvals是未撤回的人工批准。同目的、同目标范围、同类业务影响沿用原许可，优先填写approved_by引用其request_id，不重命名类别或缩小许可。原命令、参数、账号、路径和数量是实例，除操作者原文或人工批备注明确限制外，不形成新的条件。approval_epoch和资产新增不撤销批准。旧批准没有permission时，索引保留编号、工具及批备注；需要沿用时先用read_approval按request_id读取原调用及批备注，不凭索引猜许可，不猜旧脚本内容。无业务变化不必读取批准。
每项changes只填permission（actions=create/update/delete；resource=业务影响类别，保留实际权限层级差别，不含具体记录值；targets=所属系统入口；purpose=直接目的；effects=直接业务影响）和可选approved_by。已批准与未批准的变化分别填写，程序只对未授权变化申请审批。只有真实目的、目标范围或影响不同才重新判断，不能只因实例变化要求重批。
只输出JSON：{"reason":"直接目的和实际影响依据","changes":[]}。实际业务变化写入changes：[{"permission":{"actions":["create"],"resource":"业务类别","targets":["系统入口"],"purpose":"直接目的","effects":"直接影响"},"approved_by":"适用批准编号，无则省略"}]。命中明文禁止时返回顶层prohibition及changes:[]；prohibition逐字引用禁止原文并说明实际命中。操作者明文授权覆盖本次变化时可附顶层operator_permission逐字引用该原文，仍如实记录changes，不扩大授权。不要输出decision，由程序统一生成唯一裁决。工具只读源码或批准原文，不执行动作、不规划方向、不创建资产。`

func ActionReviewFor(provider llm.Provider, readers ...func(string) ([]byte, error)) ActionReviewFunc {
	return func(ctx context.Context, in ActionReviewInput) (ActionReview, error) {
		if provider == nil {
			return ActionReview{}, errors.New("动作判断无可用模型")
		}
		// A worker plan is neither an observed effect nor operator authority.
		in.Goal, in.Intent = "", ""
		if len(in.Boundaries.TargetScope) == 0 {
			in.Boundaries.TargetScope = actionTargetScopes(in.Boundaries.AuthorizedTargets)
		}
		body, legacy, err := actionReviewBody(in)
		if err != nil {
			return ActionReview{}, actionReviewError("动作判断参数无效")
		}
		messages := []llm.Message{llm.UserText(string(body))}
		var schemas []llm.ToolSchema
		if len(readers) > 0 {
			schemas = []llm.ToolSchema{{Name: "read_context_file", Description: "只读本次命令实际引用的脚本及导入源码。路径按实际工作目录解析，不得读取无关文件。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}}}
		}
		if len(legacy) > 0 {
			schemas = append(schemas, llm.ToolSchema{Name: "read_approval", Description: "按索引编号读取一条有效旧批准的完整原调用及人工批备注。仅在需要沿用该批准时读取，不是执行或重新申请审批。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"request_id": map[string]any{"type": "string"}}, "required": []string{"request_id"}, "additionalProperties": false}})
		}
		hashes := map[string][32]byte{}
		readGrants := map[string]bool{}
		// Approval lookups must not consume the existing source-read allowance.
		// The finite input grant set bounds extra lookup turns, without adding
		// another approval stage or truncating an otherwise valid script review.
		for round := 0; round < 9+len(legacy); round++ {
			msg, _, _, err := provider.Complete(ctx, llm.CompletionRequest{System: []string{actionReviewPrompt}, Messages: messages, Tools: schemas, MaxTokens: 1000, Thinking: "disabled", Temperature: new(float64)})
			if err != nil {
				return ActionReview{}, errors.New("动作判断调用未完成")
			}
			uses := msg.ToolUses()
			if len(uses) == 0 {
				for path, h := range hashes {
					b, e := readers[0](path)
					if e != nil || sha256.Sum256(b) != h {
						return ActionReview{}, actionReviewError("判断期间脚本已变化")
					}
				}
				verdict, err := parseActionEffects(msgText(msg), in.Boundaries)
				if err == nil && unreadLegacyGrant(msgText(msg), legacy, readGrants) {
					err = actionReviewError("动作判断未读取引用批准的原文")
				}
				if err != nil && round < 2 {
					messages = append(messages, msg, llm.UserText(`返回格式或批准依据不符。沿用无permission的旧批准须先read_approval读取原文。禁止调用请返回顶层对象 {"reason":"实际动作","prohibition":"操作者禁止原文与命中原因","changes":[]}，prohibition不放在changes里面。其他调用返回reason和changes，实际业务变化写changes的permission与适用approved_by；操作者明文免审时可附顶层operator_permission逐字引用覆盖本次动作的授权原文。不输出decision。`))
					continue
				}
				if err != nil {
					return ActionReview{}, err
				}
				return verdict, nil
			}
			messages = append(messages, msg)
			results := llm.Message{Role: llm.RoleUser}
			for _, use := range uses {
				if use.Name == "read_approval" {
					var args struct {
						RequestID string `json:"request_id"`
					}
					if json.Unmarshal(use.Input, &args) != nil {
						results.Content = append(results.Content, llm.ToolResultText(use.ID, "批准原文读取请求无效，请使用当前批准索引中的 request_id", true))
						continue
					}
					grant, ok := legacy[args.RequestID]
					if !ok {
						results.Content = append(results.Content, llm.ToolResultText(use.ID, "批准编号不在当前可读取的旧批准索引中，请核对当前索引；已有 permission 的批准无需读取原文", true))
						continue
					}
					data, _ := json.Marshal(struct {
						RequestID string `json:"request_id"`
						Tool      string `json:"tool"`
						Arguments string `json:"arguments"`
						Note      string `json:"note"`
						Reason    string `json:"reason"`
					}{grant.RequestID, grant.ToolName, grant.ToolArgs, grant.Note, grant.Reason})
					readGrants[args.RequestID] = true
					results.Content = append(results.Content, llm.ToolResultText(use.ID, string(data), false))
					continue
				}
				var args struct {
					Path string `json:"path"`
				}
				var data []byte
				var e error
				if use.Name != "read_context_file" || len(readers) == 0 || json.Unmarshal(use.Input, &args) != nil || len(hashes) >= 8 {
					e = errors.New("上下文读取请求无效或超过上限")
				} else {
					data, e = readers[0](args.Path)
				}
				if e != nil {
					results.Content = append(results.Content, llm.ToolResultText(use.ID, e.Error(), true))
					continue
				}
				hashes[args.Path] = sha256.Sum256(data)
				results.Content = append(results.Content, llm.ToolResultText(use.ID, string(data), false))
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
		// Concrete values belong to ToolArgs and the review reason. Persist a
		// class permission, so an example record cannot become a new limit.
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
	out.Subjects = map[string][]string{}
	for _, a := range graph.Assets {
		out.Subjects[a.ID] = a.SubjectIDs
	}
	excluded := rings.ExcludedCoveredAssetIDs(assets, roe.ExcludedAssetSet(), roe.ScopeApprovedSet())
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
		if excluded[a.ID] {
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
	b.Subjects = nil
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
