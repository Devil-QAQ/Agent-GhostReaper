package failover

// ---- 思考回传出站处理(P4-57 ②,问题清单 #76) ----
//
// 各家端点对历史思考内容的要求相反(GLM 思考＝必须传回、DeepSeek＝
// 剔除、anthropic 格式＝thinking 块带签名原样传回、openai 官方＝不
// 干预)。本层在 Chain 路由解析后、发往当前服务腿前,按该腿配置的
// 规则(P4-57 ① 协议声明层)对历史 assistant 消息的思考内容做确定性
// 处理——一处收口,虫群/主宰/牧者/插话快照/复测全部调用方自动受益;
// 与逐请求路由(P4-41)协同:会话跨模型切换时按当前腿规则处理整个
// 历史;规则确定性＝前缀缓存友好。
//
// 规则语义(词表见 modeltable.go):
//   - passback 传回:请求不动——thinking 块原样出站(openai 族序列化为
//     reasoning_content,anthropic 族带签名原样;anthropic 适配器在
//     思考关闭时按协议自身要求剔除,本层不重复干预);
//   - strip 剔除:剥掉历史 assistant 消息的思考块;剥后无任何内容的
//     消息(纯思考轮)整条剔除(空消息出站即 4xx);
//   - none 不干预:请求一字不动,引擎行为原样(openai 官方)。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/runlog"
)

// stripHistoryThinking 剔除历史 assistant 消息的思考块;返回处理后的
// 请求与剥掉的块数(未发生剔除时请求原样返回)。
func stripHistoryThinking(req llm.CompletionRequest) (llm.CompletionRequest, int) {
	removed := 0
	out := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role != llm.RoleAssistant {
			out = append(out, m)
			continue
		}
		hasThink := false
		for _, b := range m.Content {
			if b.Type == llm.BlockThinking {
				hasThink = true
				break
			}
		}
		if !hasThink {
			out = append(out, m)
			continue
		}
		nb := make([]llm.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == llm.BlockThinking {
				removed++
				continue
			}
			nb = append(nb, b)
		}
		if len(nb) == 0 {
			continue // 纯思考轮:剔除后无内容,整条丢(空消息出站即 4xx)
		}
		nm := m
		nm.Content = nb
		out = append(out, nm)
	}
	if removed == 0 {
		return req, 0
	}
	req.Messages = out
	return req, removed
}

// passbackWordOf 规则的界面词(日志与人话呈现共用)。
func passbackWordOf(rule string) string {
	switch rule {
	case ThinkingPassback:
		return "传回"
	case ThinkingNone:
		return "不干预"
	case ThinkingStrip:
		return "剔除"
	}
	return rule
}

// legThinkingReq 按腿的思考回传规则处理请求(Stream/Complete 每次尝试
// 解析出当前腿后调用;重放换腿时各自按新腿规则)。跨腿规则切换记一条
// 运行日志(会话跨模型切换按当前腿处理整个历史的可见性,规则未变不刷)。
func (c *Chain) legThinkingReq(ctx context.Context, l *leg, req llm.CompletionRequest) llm.CompletionRequest {
	rule := l.cfg.EffectiveThinkingPassback()
	key := sessionKeyOf(ctx)
	c.mu.Lock()
	prev := ""
	if key != "" && c.lastRule != nil {
		prev = c.lastRule[key]
	}
	if key != "" {
		if c.lastRule == nil {
			c.lastRule = map[string]string{}
		}
		c.lastRule[key] = rule
	}
	c.mu.Unlock()
	if key != "" && prev != rule {
		runlog.Infof("思考回传规则: 会话 %s 当前服务配置 %s 规则 %s(会话跨模型切换按当前腿规则处理整个历史)",
			shortKey(key), l.name, passbackWordOf(rule))
	}
	if rule != ThinkingStrip {
		return req
	}
	out, _ := stripHistoryThinking(req)
	return out
}

// shortKey 会话键的短呈现(转录 ID 类长键截尾)。
func shortKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return "…" + key[len(key)-10:]
}

// failSigOf 同类连环检测的错误签名:类别 + 报错前 160 字符(同字节
// 报错即同类;v1.2.0 实锤形态为同 400 原文连环)。
func failSigOf(cls ErrClass, err error) string {
	msg := ""
	if err != nil {
		msg = strings.TrimSpace(err.Error())
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return fmt.Sprintf("%s|%s", cls, msg)
}
