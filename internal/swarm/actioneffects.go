package swarm

import (
	"encoding/json"
	"io"
	"strings"

	"ghostreaper/internal/rings"
)

// The model describes effects and references grants. It does not emit an
// independent approval switch which can contradict those same effects.
type actionEffects struct {
	Reason             string           `json:"reason"`
	Prohibition        string           `json:"prohibition,omitempty"`
	OperatorPermission string           `json:"operator_permission,omitempty"`
	Changes            []businessChange `json:"changes"`
}

type businessChange struct {
	Permission rings.PurposePermission `json:"permission"`
	ApprovedBy string                  `json:"approved_by,omitempty"`
}

func parseActionEffects(text string, boundary ActionBoundary) (ActionReview, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		_, body, ok := strings.Cut(text, "\n")
		if ok {
			text = strings.TrimSpace(strings.TrimSuffix(body, "```"))
		}
	}
	var raw struct {
		Reason             string            `json:"reason"`
		Prohibition        string            `json:"prohibition,omitempty"`
		OperatorPermission string            `json:"operator_permission,omitempty"`
		Changes            []json.RawMessage `json:"changes"`
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if d.Decode(&raw) != nil || d.Decode(new(any)) != io.EOF || (raw.Changes == nil && strings.TrimSpace(raw.Prohibition) == "") || len(raw.Changes) > 16 {
		return ActionReview{}, actionReviewError("动作判断业务影响格式无效")
	}
	effects := actionEffects{Reason: raw.Reason, Prohibition: raw.Prohibition, OperatorPermission: raw.OperatorPermission}
	// A prohibition has no approval path, so do not interpret grant claims
	// attached to a prohibited action.
	if strings.TrimSpace(raw.Prohibition) == "" {
		for _, body := range raw.Changes {
			var change businessChange
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&change) != nil {
				return ActionReview{}, actionReviewError("动作判断业务影响格式无效")
			}
			effects.Changes = append(effects.Changes, change)
		}
	}
	operatorPermission := strings.TrimSpace(effects.OperatorPermission)
	if operatorPermission != "" && !strings.Contains(strings.Join(append([]string{boundary.TaskDescription, boundary.Notes}, boundary.Goals...), "\n"), operatorPermission) {
		return ActionReview{}, actionReviewError("动作判断未引用操作者授权原文")
	}
	out := ActionReview{Decision: ActionAllow, Reason: effects.Reason}
	if strings.TrimSpace(effects.Prohibition) != "" {
		out.Decision, out.Reason = ActionDeny, effects.Prohibition
	} else {
		for _, change := range effects.Changes {
			p := change.Permission
			// Validate every effect, including reused ones, using the shared
			// permission representation persisted by the approval ledger.
			b, _ := json.Marshal(ActionReview{Decision: ActionApproval, Reason: effects.Reason, Permission: &p})
			validated, err := parseActionReview(string(b))
			if err != nil {
				return ActionReview{}, err
			}
			p = *validated.Permission
			reused := false
			for _, grant := range boundary.Approvals {
				if grant.Type != rings.ApprovalRecApproved || grant.DecidedBy != "creator" {
					continue
				}
				if change.ApprovedBy == grant.RequestID && (grant.Permission == nil || permissionCovers(grant.Permission, &p, boundary)) || (grant.Reason == "" || grant.Reason == purposeApprovalMeaning) && sameBusinessPermission(grant.Permission, &p, boundary) {
					reused = true
					break
				}
			}
			if change.ApprovedBy != "" && !reused {
				return ActionReview{}, actionReviewError("动作判断引用的批准已失效")
			}
			if reused || operatorPermission != "" {
				continue
			}
			out.Decision = ActionApproval
			if out.Permission == nil {
				out.Permission = &p
			} else {
				out.Permission.Actions = append(out.Permission.Actions, p.Actions...)
				out.Permission.Resource += "、" + p.Resource
				out.Permission.Purpose += "；" + p.Purpose
				out.Permission.Effects += "；" + p.Effects
				out.Permission.Targets = append(out.Permission.Targets, p.Targets...)
			}
		}
	}
	b, _ := json.Marshal(out)
	return parseActionReview(string(b))
}

func sameBusinessPermission(grant, effect *rings.PurposePermission, boundary ActionBoundary) bool {
	if grant == nil || effect == nil || !strings.EqualFold(strings.TrimSpace(grant.Resource), strings.TrimSpace(effect.Resource)) {
		return false
	}
	return permissionCovers(grant, effect, boundary)
}

func permissionCovers(grant, effect *rings.PurposePermission, boundary ActionBoundary) bool {
	if grant == nil || effect == nil {
		return false
	}
	contains := func(all, wanted []string) bool {
		for _, w := range wanted {
			found := false
			for _, a := range all {
				if a == w {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return contains(grant.Actions, effect.Actions) && contains(permissionSystemTargets(grant.Targets, boundary), permissionSystemTargets(effect.Targets, boundary))
}

// Target aliases come only from the shared evidence-backed system projection.
// Host sharing is not a grant to another system.
func permissionSystemTargets(targets []string, boundary ActionBoundary) []string {
	out := []string{}
	for _, target := range targets {
		found := false
		for _, asset := range boundary.Assets {
			_, _, a, ea := rings.AssetIdentity(rings.AssetClass(asset.Class), asset.Title)
			_, _, t, et := rings.AssetIdentity(rings.AssetClass(asset.Class), target)
			if asset.ID != target && (ea != nil || et != nil || a != t) {
				continue
			}
			if subjects := boundary.Subjects[asset.ID]; len(subjects) > 0 {
				out = append(out, subjects...)
				found = true
				break
			}
		}
		if !found {
			out = append(out, target)
		}
	}
	return out
}
