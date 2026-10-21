package failover

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/buildinfo"
	"ghostreaper/internal/runlog"
)

// ---- opencode Go 订阅网关接入适配(P4-10 ①,问题清单 #22) ----
//
// OpenCode 的 Go 订阅流量必须走 inference/go 网关,且官方要求客户端:
// ①自报 User-Agent(不用通用 SDK 名);②每次会话带**稳定的**
// x-opencode-session 头(每会话一个——网关据此路由会话与复用提示缓存)。
// 面板不注入该头时聊天 400 MissingSessionID。
//
// 判定按端点地址:BaseURL 命中 go 网关前缀即启用注头(端点是接入方式
// 的载体——预填随"接入方式"走,用户改地址自然改行为,无需另存配置
// 字段);API 按量(zen 端点)与其他平台一律不注头。
//
// 会话头取值:优先 Norma 会话标记(agentcore 会话 Prompt 时已把转录
// 会话 ID 挂进请求 ctx,虫群/主宰/牧者会话天然覆盖)——按会话 ID 确定
// 性派生(同会话同头,进程重启亦不变,利于网关提示缓存复用);非
// agentcore 的直连调用方可用 WithLLMSession 显式标记(插话线程);两者
// 皆无(一次性工具调用)退传输层实例兜底值(同一 Provider 腿内稳定)。

// opencodeGoOrigin Go 订阅网关源(聊天与模型列表共用)。包级 var 是
// E2E 测试缝(换本地伪网关跑全链注头断言),产线零改写。
var opencodeGoOrigin = "https://opencode.ai/inference/go"

// opencodeModelListURL Go 网关模型列表(go 网关列表不在聊天路径下,
// {base}/models 实测 404;官方文档:列表在网关根 /v1/models)。
const opencodeModelListURL = "https://opencode.ai/inference/go/v1/models"

// sessionHeader opencode 会话头(官方文档 x-opencode-session)。
const sessionHeader = "x-opencode-session"

// isOpenCodeGoURL 端点是否命中 opencode Go 订阅网关(注头与列表路径
// 适配的统一判定;大小写与尾斜杠归一)。
func isOpenCodeGoURL(base string) bool {
	u := strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/"))
	return strings.HasPrefix(u, opencodeGoOrigin+"/") || u == opencodeGoOrigin
}

// opencodeUserAgent 自报客户端标识(官方要求;版本随构建注入)。
func opencodeUserAgent() string {
	v := strings.TrimSpace(buildinfo.Version)
	if v == "" {
		v = "dev"
	}
	return "ghostreaper/" + v
}

// openCodeSessionSeed 会话头的种子:Norma 会话标记 > 显式标记 > 兜底。
func opencodeSessionSeed(ctx context.Context) string {
	if sid := transcript.SessionIDFrom(ctx); sid != "" {
		return sid
	}
	if sid := llmSessionFrom(ctx); sid != "" {
		return sid
	}
	return ""
}

// opencodeSessionID 由会话种子确定性派生网关会话头值(哈希呈现,不带
// 出本地标识原文;同会话恒同值)。
func opencodeSessionID(seed string) string {
	sum := sha256.Sum256([]byte("ghostreaper-session:" + seed))
	return "gr-" + hex.EncodeToString(sum[:12])
}

// randomSessionSeed 随机会话种子(无标记时的兜底与列表拉取用)。
func randomSessionSeed() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("seed-%d", time.Now().UnixNano()) // 不可达防御
	}
	return hex.EncodeToString(b[:])
}

// llmSessionKey WithLLMSession 的 ctx 键。
type llmSessionKey struct{}

// WithLLMSession 把非 agentcore 会话的标识挂进请求 ctx(插话线程等
// 直连 Complete 的调用方;agentcore 会话自带转录标记,无需本标记)。
func WithLLMSession(ctx context.Context, session string) context.Context {
	if session == "" {
		return ctx
	}
	return context.WithValue(ctx, llmSessionKey{}, session)
}

func llmSessionFrom(ctx context.Context) string {
	if v, ok := ctx.Value(llmSessionKey{}).(string); ok {
		return v
	}
	return ""
}

// newOpenCodeGoClient Go 网关专用 HTTP client:传输层包会话头/UA 注入
// (openai 族格式用;不带整段超时——流式时长由请求 ctx 管,与 llm 包
// defaultHTTPClient 同规则),代理语义同源。
func newOpenCodeGoClient(proxy string) (*http.Client, error) {
	tr, err := proxyTransport(proxy)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: newOpenCodeHeaderTransport(tr)}, nil
}

// newOpenCodeHeaderTransport 构建注头传输层(兜底会话值按实例生成:
// 无会话标记的请求在同一传输层实例内同头)。
func newOpenCodeHeaderTransport(base http.RoundTripper) opencodeHeaderTransport {
	return opencodeHeaderTransport{base: base, fallbackSeed: randomSessionSeed()}
}

// proxyTransport 出站传输层(空代理=环境变量,http/https/socks5=定向;
// 与 llm 包 defaultHTTPClient 同规则,不带整段超时)。
func proxyTransport(proxy string) (*http.Transport, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy == "" {
		tr.Proxy = http.ProxyFromEnvironment
		return tr, nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("failover: 代理 %q 不可用: %v", proxy, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("failover: 代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
	}
	tr.Proxy = http.ProxyURL(u)
	return tr, nil
}

// opencodeHeaderTransport Go 网关注头传输层:请求出站前注入
// x-opencode-session(按会话标记稳定派生)与自报 User-Agent。
type opencodeHeaderTransport struct {
	base         http.RoundTripper
	fallbackSeed string // 无会话标记请求的兜底种子(实例内稳定)
}

// loggedSessions 已留痕的会话头值(每会话只记一次运行日志,请求留痕
// 可核——同会话同头以日志直证;进程级,不随传输层实例重置)。
var loggedSessions sync.Map

func (t opencodeHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	seed := opencodeSessionSeed(req.Context())
	if seed == "" {
		seed = t.fallbackSeed
	} else if _, dup := loggedSessions.LoadOrStore(opencodeSessionID(seed), true); !dup {
		runlog.Infof("opencode 网关会话头注入: 会话 %s → %s", seed, opencodeSessionID(seed))
	}
	r := req.Clone(req.Context())
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", opencodeUserAgent())
	}
	r.Header.Set(sessionHeader, opencodeSessionID(seed))
	return t.base.RoundTrip(r)
}
