package mirror

// 交换原文渲染(P5-04 起独立于报告包,供面板流量回放 / 报告证据包 /
// 虫群与牧者的流量查询工具三方同源复用——同一份装配,零平行实现)。
// 渲染口径与原实现逐字一致(Burp 风格:请求行/头/体 + 状态行/头/体;
// 正文超长按 clipAt 截断并如实标记)。

import (
	"fmt"
	"net/url"
	"strings"
)

// RenderExchange 交换文档 → Burp 风格原文:clipAt>0 时请求/响应正文截断
// 并标记,≤0 全文。请求行 origin-form;行尾与消息边界用线上格式(\r\n,
// 请求头后带空行终止——渲染出的请求包本身即完整可发的 HTTP 报文,
// 复制即贴 Burp Repeater 可发);目标证书结论不进包文(包外信息,审计
// 事实在本文档内)。返回(原文, 是否截断)。
func RenderExchange(ex *Exchange, clipAt int) (string, bool) {
	var b strings.Builder
	t1 := writeRequestBlock(&b, ex, clipAt)
	var rb strings.Builder
	t2 := writeResponseBlock(&rb, ex, clipAt)
	resp := rb.String()
	if ex.Response != nil {
		b.WriteString("\r\n") // 请求与响应块间的分隔行(与原始报文形态一致)
		b.WriteString(resp)
	} else if resp != "" {
		// 无响应仅异常注记:注记自带前导空行,原样衔接。
		b.WriteString(resp)
	}
	return b.String(), t1 || t2
}

// RenderExchangeSide 交换文档单侧原文(side=req 请求侧〔Burp 就绪〕 /
// side=resp 响应侧〔含镜异常注记〕),不截断、字节级保真;与全量装配
// 同一分块。
func RenderExchangeSide(ex *Exchange, side string) string {
	var b strings.Builder
	switch side {
	case "req":
		writeRequestBlock(&b, ex, 0)
	case "resp":
		writeResponseBlock(&b, ex, 0)
		return strings.TrimPrefix(b.String(), "\r\n") // 独立视图不带前导空行
	}
	return b.String()
}

// writeRequestBlock 请求侧原文块:请求行/头/体(origin-form 请求行 +
// Host 头承载 + \r\n 行尾 + 头后空行终止)。返回正文是否被截断。
func writeRequestBlock(b *strings.Builder, ex *Exchange, clipAt int) bool {
	trunc := false
	if ex.Request != nil {
		fmt.Fprintf(b, "%s %s %s\r\n", ex.Request.Method, requestTarget(ex.Request.URL), protoOf(ex))
		if host := requestHost(ex); host != "" {
			fmt.Fprintf(b, "Host: %s\r\n", host)
		}
		for _, h := range ex.Request.Headers {
			fmt.Fprintf(b, "%s: %s\r\n", h.Name, h.Value)
		}
		b.WriteString("\r\n") // 头终止空行(无体请求至此即完整可发)
		if len(ex.Request.Body) > 0 {
			s, t := clipBody(ex.Request.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\r\n")
		}
	}
	return trunc
}

// requestTarget 请求行目标:绝对 URI → origin-form(路径+查询串,根路径
// 兜底 "/");解析失败或本就无 host 的目标原样保留(如实不改写)。
func requestTarget(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// requestHost Host 头取值:留痕的 Host 优先(代理形态下独立携带),
// 缺省从请求 URL 推(host[:port] 原样)。
func requestHost(ex *Exchange) string {
	if ex.Request == nil {
		return ""
	}
	if ex.Request.Host != "" {
		return ex.Request.Host
	}
	if u, err := url.Parse(ex.Request.URL); err == nil {
		return u.Host
	}
	return ""
}

// protoOf 请求协议版本(留痕缺省退 HTTP/1.1——请求行必须有协议段)。
func protoOf(ex *Exchange) string {
	if ex.Request != nil && ex.Request.Proto != "" {
		return ex.Request.Proto
	}
	return "HTTP/1.1"
}

// writeResponseBlock 响应侧原文块:状态行/头/体 + 镜异常注记(无响应时
// 仅注记,自带前导空行)。返回正文是否被截断。
func writeResponseBlock(b *strings.Builder, ex *Exchange, clipAt int) bool {
	trunc := false
	if ex.Response != nil {
		if ex.Response.Proto != "" && ex.Response.StatusText != "" {
			fmt.Fprintf(b, "%s %d %s\r\n", ex.Response.Proto, ex.Response.Status, ex.Response.StatusText)
		} else {
			fmt.Fprintf(b, "HTTP %d\r\n", ex.Response.Status)
		}
		for _, h := range ex.Response.Headers {
			fmt.Fprintf(b, "%s: %s\r\n", h.Name, h.Value)
		}
		if len(ex.Response.Body) > 0 {
			b.WriteString("\r\n")
			s, t := clipBody(ex.Response.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\r\n")
		}
	}
	if ex.Error != "" {
		fmt.Fprintf(b, "\r\n[镜留痕异常: %s]\r\n", ex.Error)
	}
	return trunc
}

// clipBody 正文截断(clipAt≤0 全文)。
func clipBody(body []byte, clipAt int) (string, bool) {
	if clipAt > 0 && len(body) > clipAt {
		return string(body[:clipAt]), true
	}
	return string(body), false
}
