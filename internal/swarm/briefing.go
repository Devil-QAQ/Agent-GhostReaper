package swarm

// 任务简报(P5-04 虫群知情化):虫群开工的第一条消息里给一份轻量
// 概况——目标、已确认事实(最新约 20 条摘要＋总数)、已确认漏洞清单、
// 在跑/排队意图摘要;不给规划类信息(未测资产清单等 coverage 信号
// ——那是主宰的事),纪律只有开头一句话、无禁令清单。装配失败静默
// 降级:任一读失败即不给简报,虫群照常开工,不阻塞不报错。
// 数据全部走既有投影查询(NodesByType),零新扫描路径。

import (
	"context"
	"encoding/json"

	"ghostreaper/internal/rings"
)

// briefingLead 简报开头一句话(锚例逐字,P5-04 定稿;纪律只此一句)。
const briefingLead = "下面是任务全局，帮你别重复别人的活；发散想可以，动手只做你领的这条，别的线索写回交主宰。"

// briefingFactLatest 简报携带的已确认事实最新条数上限(轻量概况,
// 防长任务事实池撑爆首条消息;全量走 search_facts 按需查)。
const briefingFactLatest = 20

// briefingIntentLatest 在跑/排队意图摘要的条数上限(同上,防刷屏)。
const briefingIntentLatest = 20

// briefingFactClip / briefingIntentClip 简报内单条摘要的截断长度。
const (
	briefingFactClip   = 120
	briefingIntentClip = 100
)

// briefingJSON 概况 JSON 的形状(键名稳定,模型解析与测试断言依据)。
type briefingJSON struct {
	Goal string `json:"goal,omitempty"`
	// ConfirmedFacts 已确认事实:总数 + 最新摘要(不含证据原文——按需
	// 经查事实工具取)。
	ConfirmedFacts struct {
		Total  int            `json:"total"`
		Latest []briefingItem `json:"latest"`
	} `json:"confirmed_facts"`
	// ConfirmedClaims 已确认漏洞清单(报漏洞前先核对,防重复上报)。
	ConfirmedClaims []briefingClaim `json:"confirmed_claims"`
	// ActiveIntents 在跑/排队意图摘要(在跑=有单位执行中;排队中=已派
	// 待领)。只给意图摘要,不给规划信号。
	ActiveIntents []briefingIntent `json:"active_intents"`
}

type briefingItem struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	ClaimID string `json:"claim_id,omitempty"`
}

type briefingClaim struct {
	ID       string `json:"id"`
	Severity string `json:"severity,omitempty"`
	Summary  string `json:"summary"`
}

type briefingIntent struct {
	Step   string `json:"step"`
	State  string `json:"state"` // 在跑 | 排队中
	Intent string `json:"intent"`
}

// assembleBriefing 装配任务简报(锚例句 + 概况 JSON)。任一读失败返回
// 空串(静默降级:不给简报,虫群照常开工)。
func assembleBriefing(ctx context.Context, store *rings.Store, taskID string) string {
	if store == nil {
		return ""
	}
	var b briefingJSON

	goals, err := store.NodesByType(ctx, taskID, rings.NodeGoal, "")
	if err != nil {
		return ""
	}
	if len(goals) > 0 {
		b.Goal = goals[len(goals)-1].Title
	}

	facts, err := store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		return ""
	}
	b.ConfirmedFacts.Total = len(facts)
	// NodesByType 按创建序返回,最新 = 尾部。
	for i := len(facts) - 1; i >= 0 && len(b.ConfirmedFacts.Latest) < briefingFactLatest; i-- {
		claimID, _ := facts[i].Detail["claim_id"].(string)
		b.ConfirmedFacts.Latest = append(b.ConfirmedFacts.Latest, briefingItem{
			ID: facts[i].ID, Summary: clipLine(facts[i].Title, briefingFactClip), ClaimID: claimID,
		})
	}

	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, "confirmed")
	if err != nil {
		return ""
	}
	for _, c := range claims {
		bc := briefingClaim{ID: c.ID, Summary: clipLine(c.Title, briefingFactClip)}
		if sev, ok := c.Detail["severity"].(string); ok {
			bc.Severity = sev
		}
		b.ConfirmedClaims = append(b.ConfirmedClaims, bc)
	}

	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return ""
	}
	for i := len(steps) - 1; i >= 0 && len(b.ActiveIntents) < briefingIntentLatest; i-- {
		var state string
		switch steps[i].Status {
		case "running":
			state = "在跑"
		case "dispatched":
			state = "排队中"
		default:
			continue
		}
		b.ActiveIntents = append(b.ActiveIntents, briefingIntent{
			Step: steps[i].ID, State: state, Intent: clipLine(steps[i].Title, briefingIntentClip),
		})
	}

	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return briefingLead + "\n" + string(raw)
}

// FirstMessage 开工第一条消息:任务简报(装配失败为空串,静默降级)+
// 七要素任务包。简报只进首条消息,重锚定重申的是任务包核心,不重复
// 注入简报。
func (p *TaskPack) FirstMessage() string {
	if p.Briefing == "" {
		return p.Render()
	}
	return p.Briefing + "\n\n" + p.Render()
}
