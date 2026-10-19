package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

// ---- 纳谏:牧者建议的主宰裁决(P2-07,第四章 §7"对主宰 = 建议进
// 正式规划,裁决后改") ----
//
// 牧者经 advise_overmind 提交的建议以 pending 入账(shepherd.record
// advice);主宰每轮拆解时在态势快照读到全部待裁决建议,回复的
// advices 字段逐条裁决(采纳/不采纳 + 理由);裁决写回
// advice_verdict 事件,面板建议卡的状态随之改变。裁决语义:采纳的
// 建议须反映到本轮或后续意图(在理由中说明何时体现),不采纳必须
// 给出基于图上事实的理由——牧者不代决,主宰不受命,一切可审计。

// adviceVerdictPlan 拆解回复里的建议裁决草稿。
type adviceVerdictPlan struct {
	ID     string `json:"id"`
	Adopt  bool   `json:"adopt"`
	Reason string `json:"reason"`
}

// pendingAdvice 待裁决建议(事件流重建,最新状态读法)。From 记录建议
// 来源事件(shepherd.record=牧者对话提交 / interject.record=插话子会话
// 提交,P2-08)——裁决写回同源,两路建议同一裁决语义。
type pendingAdvice struct {
	ID   string
	Text string
	From rings.EventKind
}

// pendingAdvices 图上全部待裁决建议(快照的"待裁决建议"区数据源):
// 牧者建议(shepherd.record advice)与插话建议(interject.record advice,
// P2-08"改"对主宰)都算——对主宰 = 建议进正式规划,裁决后改,不分
// 提交入口。读失败退空(快照如实呈现"读取失败"由调用方处理;fail-safe
// 不挡拆解)。
func (l *Loop) pendingAdvices(ctx context.Context) ([]pendingAdvice, error) {
	evs, err := l.Store.Events(ctx, l.TaskID, 0, 0)
	if err != nil {
		return nil, err
	}
	var out []pendingAdvice
	idx := map[string]int{}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventShepherdRecord:
			var rec rings.ShepherdRecord
			if json.Unmarshal(ev.Payload, &rec) != nil {
				continue
			}
			switch rec.Type {
			case rings.ShepherdRecAdvice:
				idx[rec.AdviceID] = len(out)
				out = append(out, pendingAdvice{ID: rec.AdviceID, Text: rec.Text, From: ev.Kind})
			case rings.ShepherdRecAdviceVerdict:
				l.dropPendingAdvice(idx, &out, rec.AdviceID)
			}
		case rings.EventInterjectRecord:
			var rec rings.InterjectRecord
			if json.Unmarshal(ev.Payload, &rec) != nil {
				continue
			}
			switch rec.Type {
			case rings.InterjectRecAdvice:
				idx[rec.AdviceID] = len(out)
				out = append(out, pendingAdvice{ID: rec.AdviceID, Text: rec.Text, From: ev.Kind})
			case rings.InterjectRecAdviceVerdict:
				l.dropPendingAdvice(idx, &out, rec.AdviceID)
			}
		}
	}
	return out, nil
}

// dropPendingAdvice 裁决已落:从待决列表移除并修正索引。
func (l *Loop) dropPendingAdvice(idx map[string]int, out *[]pendingAdvice, adviceID string) {
	i, ok := idx[adviceID]
	if !ok {
		return
	}
	*out = append((*out)[:i], (*out)[i+1:]...)
	for id, j := range idx {
		if j > i {
			idx[id] = j - 1
		}
	}
	delete(idx, adviceID)
}

// writeAdviceVerdicts 落一批建议裁决(每轮拆解后调用):只对真实存在
// 且仍待裁决的建议落账,幂等跳过其余(重复裁决/幽灵 ID 不入账);
// 单条失败不拖垮其余,如实留 LastErr。裁决写回建议的同源事件
// (shepherd 建议落 shepherd.record、插话建议落 interject.record)——
// 两路提交同一裁决语义,面板各自读同源状态。
func (l *Loop) writeAdviceVerdicts(ctx context.Context, plans []adviceVerdictPlan) {
	if len(plans) == 0 {
		return
	}
	pending, err := l.pendingAdvices(ctx)
	if err != nil {
		l.setErr(fmt.Errorf("overmind: 建议裁决查待决: %w", err))
		return
	}
	byID := map[string]pendingAdvice{}
	for _, a := range pending {
		byID[a.ID] = a
	}
	for _, p := range plans {
		id, reason := strings.TrimSpace(p.ID), strings.TrimSpace(p.Reason)
		if id == "" || reason == "" {
			continue
		}
		a, exists := byID[id]
		if !exists {
			continue
		}
		state := rings.ShepherdAdviceRejected
		if p.Adopt {
			state = rings.ShepherdAdviceAdopted
		}
		var ev rings.Event
		if a.From == rings.EventInterjectRecord {
			ev = rings.Event{
				Kind: rings.EventInterjectRecord, Actor: ActorOvermind,
				Payload: rings.InterjectRecord{
					Target: "overmind", Type: rings.InterjectRecAdviceVerdict,
					AdviceID: id, State: state, Reason: reason,
				},
			}
		} else {
			ev = rings.Event{
				Kind: rings.EventShepherdRecord, Actor: ActorOvermind,
				Payload: rings.ShepherdRecord{
					Type: rings.ShepherdRecAdviceVerdict, AdviceID: id, State: state, Reason: reason,
				},
			}
		}
		if _, err := l.Store.Append(ctx, l.TaskID, ev); err != nil {
			l.setErr(fmt.Errorf("overmind: 建议裁决入账 %s: %w", id, err))
		}
	}
}
