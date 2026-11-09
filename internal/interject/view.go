package interject

import (
	"context"
	"encoding/json"
	"strings"

	"ghostreaper/internal/rings"
)

// ---- 读视图(事件流重建,重启后零内存依赖;同 shepherd 口径) ----

// interjectMessage 一条插话发言的内部态(工作线消费)。
type interjectMessage struct {
	id, text, state string
	replied         bool
}

type convState struct {
	messages []*interjectMessage
}

func (st *convState) queuedCount() int {
	n := 0
	for _, m := range st.messages {
		if m.state == "queued" && !m.replied {
			n++
		}
	}
	return n
}

func (st *convState) oldestQueued() *interjectMessage {
	for _, m := range st.messages {
		if m.state == "queued" && !m.replied {
			return m
		}
	}
	return nil
}

// state 从事件流重建(任务 × 目标)子会话的消息态。P4-58:按 kind
// 定向直查 interject.record,不再全量扫事件流。
func (s *Service) state(ctx context.Context, taskID, key string) (*convState, error) {
	target, sessionID := splitKey(key)
	evs, err := s.Store.EventsByKinds(ctx, taskID, rings.EventInterjectRecord)
	if err != nil {
		return nil, err
	}
	st := &convState{}
	byID := map[string]*interjectMessage{}
	for _, ev := range evs {
		if ev.Kind != rings.EventInterjectRecord {
			continue
		}
		var rec rings.InterjectRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || !matchTarget(rec, target, sessionID) {
			continue
		}
		switch rec.Type {
		case rings.InterjectRecUserMessage:
			if rec.Mode != "ask" {
				continue // 改向不进应答队列(回执在舵杆/裁决线)
			}
			m := &interjectMessage{id: rec.MessageID, text: rec.Text, state: "queued"}
			st.messages = append(st.messages, m)
			byID[m.id] = m
		case rings.InterjectRecMessageState:
			if m, ok := byID[rec.MessageID]; ok {
				m.state = rec.State
			}
		case rings.InterjectRecReply:
			if m, ok := byID[rec.MessageID]; ok {
				m.replied = true
			}
		}
	}
	return st, nil
}

// splitKey 会话键 → (target, sessionID)。
func splitKey(key string) (string, string) {
	if key == TargetOvermind {
		return TargetOvermind, ""
	}
	return "unit", strings.TrimPrefix(key, "u:")
}

// matchTarget 事件是否属于该(目标 × 会话锚点)的子会话。
func matchTarget(rec rings.InterjectRecord, target, sessionID string) bool {
	if rec.Target != target {
		return false
	}
	if target == TargetOvermind {
		return true
	}
	return rec.SessionID == sessionID
}

// MessageView 一条插话发言(问/改及其回答与回执关联)。
type MessageView struct {
	ID        string `json:"id"`
	Mode      string `json:"mode"` // ask|steer
	Text      string `json:"text"`
	State     string `json:"state"` // ask: queued|delivered|replied|failed;steer: issued
	Note      string `json:"note,omitempty"`
	Reply     string `json:"reply,omitempty"`
	RefID     string `json:"ref_id,omitempty"`       // 改向关联:st-*(舵杆)|av-*(建议)
	RefKind   string `json:"ref_kind,omitempty"`     // steer|advice
	AdvState  string `json:"advice_state,omitempty"` // advice: pending|adopted|rejected
	AdvReason string `json:"advice_reason,omitempty"`
	At        string `json:"at"`
	ReplyAt   string `json:"reply_at,omitempty"`
}

// SteerView 一条改向的回执状态(汇合 steer.record 的最新态)。
type SteerView struct {
	SteerID string `json:"steer_id"`
	State   string `json:"state"` // queued|delivered|adopted|rejected|stale
	Note    string `json:"note,omitempty"`
	At      string `json:"at"`
}

// View 子会话读视图(面板插话窗的数据面;信息条的 token/模型数据由
// server 层从 token 独立表补齐)。改向的回执状态挂在发言上:对虫群查
// 舵杆状态机(steer.record 最新态),对主宰查建议裁决(interject.record
// advice_verdict)。
type View struct {
	Messages  []MessageView `json:"messages"`
	Steers    []SteerView   `json:"steers"` // 全部改向的回执状态(面板时间线;消息侧按 RefID 关联)
	Busy      bool          `json:"busy"`
	StartedAt string        `json:"started_at,omitempty"`
	Frozen    bool          `json:"frozen"` // 任务冬眠中(入口冻结)
}

// View 子会话视图:插话发言(问/改)+ 改向回执(舵杆状态机汇合)+
// 建议裁决轨迹 + 冻结态。P4-58:按 kind 定向直查 interject.record +
// steer.record,不再全量扫事件流。
func (s *Service) View(ctx context.Context, taskID, target, sessionID string) (*View, error) {
	evs, err := s.Store.EventsByKinds(ctx, taskID, rings.EventInterjectRecord, rings.EventSteerRecord)
	if err != nil {
		return nil, err
	}
	key := sessionKey(target, sessionID)
	v := &View{Messages: []MessageView{}, Steers: []SteerView{},
		Frozen: s.hibernating(taskID)}
	msgIdx := map[string]int{}
	advMsg := map[string]int{} // adviceID → 消息位(裁决回写)
	steerLatest := map[string]int{}
	linkRef := func(rec rings.InterjectRecord, refID, refKind string) {
		if i, ok := msgIdx[rec.MessageID]; ok {
			v.Messages[i].RefID, v.Messages[i].RefKind = refID, refKind
			return
		}
		// 无锚(理论不发生:入队先落 user_message)——独立成条,不丢。
		v.Messages = append(v.Messages, MessageView{
			ID: rec.MessageID, Mode: "steer", Text: rec.Text, State: "issued",
			RefID: refID, RefKind: refKind,
		})
		msgIdx[rec.MessageID] = len(v.Messages) - 1
	}
	for _, ev := range evs {
		at := ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
		switch ev.Kind {
		case rings.EventInterjectRecord:
			var rec rings.InterjectRecord
			if json.Unmarshal(ev.Payload, &rec) != nil || !matchTarget(rec, target, sessionID) {
				continue
			}
			if v.StartedAt == "" {
				v.StartedAt = at
			}
			switch rec.Type {
			case rings.InterjectRecUserMessage:
				state := "queued"
				if rec.Mode == "steer" {
					state = "issued" // 改向不进应答队列,回执走舵杆/裁决线
				}
				v.Messages = append(v.Messages, MessageView{
					ID: rec.MessageID, Mode: rec.Mode, Text: rec.Text, State: state, At: at,
				})
				msgIdx[rec.MessageID] = len(v.Messages) - 1
			case rings.InterjectRecMessageState:
				if i, ok := msgIdx[rec.MessageID]; ok && v.Messages[i].State != "replied" {
					v.Messages[i].State, v.Messages[i].Note = rec.State, rec.Note
				}
			case rings.InterjectRecReply:
				if i, ok := msgIdx[rec.MessageID]; ok {
					v.Messages[i].Reply, v.Messages[i].ReplyAt, v.Messages[i].State = rec.Text, at, "replied"
				}
			case rings.InterjectRecSteer:
				linkRef(rec, rec.SteerID, "steer")
			case rings.InterjectRecAdvice:
				linkRef(rec, rec.AdviceID, "advice")
				if i, ok := msgIdx[rec.MessageID]; ok {
					v.Messages[i].AdvState = "pending"
					advMsg[rec.AdviceID] = i
				}
			case rings.InterjectRecAdviceVerdict:
				if i, ok := advMsg[rec.AdviceID]; ok {
					v.Messages[i].AdvState, v.Messages[i].AdvReason = rec.State, rec.Reason
				}
			}
		case rings.EventSteerRecord:
			// 改向回执状态机:同一 SteerID 的最新事件即当前态。
			var rec rings.SteerRecord
			if json.Unmarshal(ev.Payload, &rec) != nil {
				continue
			}
			if i, ok := steerLatest[rec.SteerID]; ok {
				v.Steers[i].State, v.Steers[i].Note, v.Steers[i].At = rec.State, rec.Note, at
			} else {
				steerLatest[rec.SteerID] = len(v.Steers)
				v.Steers = append(v.Steers, SteerView{SteerID: rec.SteerID, State: rec.State, Note: rec.Note, At: at})
			}
		}
	}
	s.mu.Lock()
	if c, ok := s.convs[taskID+"|"+key]; ok && c.busy {
		v.Busy = true
	}
	s.mu.Unlock()
	return v, nil
}
