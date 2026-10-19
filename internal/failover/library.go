package failover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// warnOnce 同文案告警只记一次(高频读路径的配置读失败告警去重,
// P3-13b ③)。
var warnOnceSeen sync.Map

func warnOnce(msg string) {
	if _, dup := warnOnceSeen.LoadOrStore(msg, true); !dup {
		runlog.Warnf("%s", msg)
	}
}

// ---- 模型库(P2-06e,第七章 §5「LLM 模型库与主备」) ----
//
// 设置页存多个模型配置(名称/端点/模型/key/参数),从库中激活主备两个
// 为全局默认,任务级可覆盖。存储复用 P2-06c 通用配置键值表(schema v6,
// 单写者排队 + by/updated_at 轻量审计),键空间点分命名:
//
//	model.library.<name>            一条模型配置(JSON)
//	model.active.primary / .backup  激活的主用/备库名(缺省=未激活)
//	model.task.<taskID>.primary     任务级覆盖(缺省=随全局)
//
// Key 安全:api_key 只进键值表(服务端 SQLite);一切对外读路径经
// Masked 打码(如 sk-…abc4),不写日志/黑匣子/transcript——本包的错误
// 文案只携带状态码与错误类别,不携带配置内容。

// 模型库配置键(rings v6 通用配置键值表)。
const (
	keyLibraryPrefix = "model.library."
	keyActivePrimary = "model.active.primary"
	keyActiveBackup  = "model.active.backup"
	keyTaskPrefix    = "model.task."
)

// ModelConfig 模型库的一条配置。Format 取 llm 包 wire 格式词
// (openai|anthropic|openai-responses)。v2(P2-08c,第七章 §5 模型库
// v2):代理(仅 LLM 出站)、限速、轮询(备链优先级/不参与)、输出与
// 思考、重试覆盖五层(0=全局默认 / -1=关闭 / >0=值)。
type ModelConfig struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	// ContextWindow 模型上下文窗口(token,P2-07 会话信息条的消耗比例
	// 分母;牧者会话的自动压缩线)。0 = 未配置(比例不呈现,压缩走
	// 引擎默认窗口);备链路由以它做"上下文装不下跳过"判定。
	ContextWindow int `json:"context_window,omitempty"`
	// Proxy 出站代理(http://|https://|socks5://,可带 user:pass@)。
	// 只作用于本配置构建的 LLM Provider——模型出站走代理,工具流量
	// (镜/探测)不经它。空 = 不走代理。
	Proxy string `json:"proxy,omitempty"`
	// RatePerSecond / RatePerMinute 限速(每秒/每分钟请求数,0=不限);
	// 同一配置的全部调用方共享一个限速器(引擎内重试不计数)。
	RatePerSecond float64 `json:"rate_per_second,omitempty"`
	RatePerMinute float64 `json:"rate_per_minute,omitempty"`
	// PollPriority 备链轮询优先级(≥1,小者先;0 视同 1)。仅对备链
	// 成员生效——激活主用恒第 1 顺位,不按此排序。
	PollPriority int `json:"poll_priority,omitempty"`
	// PollDisabled 不参与备链轮询(显式退出;默认参与)。
	PollDisabled bool `json:"poll_disabled,omitempty"`
	// MaxTokens 输出上限(token)。>0 时作为该模型的输出上限:请求未
	// 设或超于此值时压到本值。MaxTokensField 选请求里携带上限的字段名
	// (""=max_tokens;"max_completion_tokens"=o 系列只认的新字段)。
	MaxTokens      int    `json:"max_tokens,omitempty"`
	MaxTokensField string `json:"max_tokens_field,omitempty"`
	// StreamDisabled 关流式:请求以非流式(整段一次)发出,面板与会话
	// 逻辑无感知。默认流式。
	StreamDisabled bool `json:"stream_disabled,omitempty"`
	// Thinking 思考开关:""(随端点默认)|enabled|disabled;ThinkingEffort
	// 思考强度:""|low|medium|high|max。
	Thinking       string `json:"thinking,omitempty"`
	ThinkingEffort string `json:"thinking_effort,omitempty"`
	// Retry* 重试覆盖五层(第七章「重试分层」①~④ + 意图级重跑⑤),
	// 三态:0=全局默认 / -1=关闭 / >0=值。
	RetryConnect int `json:"retry_connect,omitempty"` // ①建连重试
	RetryEmpty   int `json:"retry_empty,omitempty"`   // ②空响应重试
	RetryReplay  int `json:"retry_replay,omitempty"`  // ③零交付重放
	RetryBreaker int `json:"retry_breaker,omitempty"` // ④熔断阈值(连续失败开闸次数)
	RetryRerun   int `json:"retry_rerun,omitempty"`   // ⑤意图级重跑
}

// MaskedConfig 对外视图:Key 打码、代理去凭据,其余原样(API 永不回显
// 明文)。
type MaskedConfig struct {
	Name       string `json:"name"`
	Format     string `json:"format"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKeyMask string `json:"api_key_mask"`
	HasKey     bool   `json:"has_key"`
	// ContextWindow 同 ModelConfig(非敏感,原样呈现)。
	ContextWindow int `json:"context_window,omitempty"`
	// ProxyMask 代理去凭据视图(如 http://***@127.0.0.1:7890);空=未配。
	ProxyMask string `json:"proxy_mask,omitempty"`
	// v2 非敏感字段原样。
	RatePerSecond  float64 `json:"rate_per_second,omitempty"`
	RatePerMinute  float64 `json:"rate_per_minute,omitempty"`
	PollPriority   int     `json:"poll_priority,omitempty"`
	PollDisabled   bool    `json:"poll_disabled,omitempty"`
	MaxTokens      int     `json:"max_tokens,omitempty"`
	MaxTokensField string  `json:"max_tokens_field,omitempty"`
	StreamDisabled bool    `json:"stream_disabled,omitempty"`
	Thinking       string  `json:"thinking,omitempty"`
	ThinkingEffort string  `json:"thinking_effort,omitempty"`
	RetryConnect   int     `json:"retry_connect,omitempty"`
	RetryEmpty     int     `json:"retry_empty,omitempty"`
	RetryReplay    int     `json:"retry_replay,omitempty"`
	RetryBreaker   int     `json:"retry_breaker,omitempty"`
	RetryRerun     int     `json:"retry_rerun,omitempty"`
	UpdatedBy      string  `json:"updated_by"`
	UpdatedAt      string  `json:"updated_at"`
}

// Masked 打码视图转换。
func (c ModelConfig) Masked() MaskedConfig {
	return MaskedConfig{
		Name: c.Name, Format: c.Format, BaseURL: c.BaseURL, Model: c.Model,
		APIKeyMask: MaskKey(c.APIKey), HasKey: c.APIKey != "",
		ContextWindow: c.ContextWindow,
		ProxyMask:     MaskProxy(c.Proxy),
		RatePerSecond: c.RatePerSecond, RatePerMinute: c.RatePerMinute,
		PollPriority: c.PollPriority, PollDisabled: c.PollDisabled,
		MaxTokens: c.MaxTokens, MaxTokensField: c.MaxTokensField,
		StreamDisabled: c.StreamDisabled,
		Thinking:       c.Thinking, ThinkingEffort: c.ThinkingEffort,
		RetryConnect: c.RetryConnect, RetryEmpty: c.RetryEmpty,
		RetryReplay: c.RetryReplay, RetryBreaker: c.RetryBreaker,
		RetryRerun: c.RetryRerun,
		UpdatedBy:  "", UpdatedAt: "", // 由调用方补审计字段(键值表携带)
	}
}

// MaskProxy 代理 URL 去凭据呈现:保留协议/主机/端口,user:pass 折叠为
// ***(凭据同 key 待遇,不上面板);空串原样。解析失败整体折叠。
func MaskProxy(proxy string) string {
	if proxy == "" {
		return ""
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return "***"
	}
	out := u.Scheme + "://"
	if u.User != nil {
		out += "***@"
	}
	out += u.Host
	return out
}

// ContextWindowFor 按模型名解析上下文窗口(P2-07 会话信息条/压缩线):
// 优先库内 Model 字段匹配(主备切换后同会话按服务模型取窗口),命中
// 多条取首个非零;未命中退主用激活配置的窗口;再退 0(未配置)。
// 配置读失败退默认记 WARN(P3-13b ③)——按错误去重,防轮询刷屏。
func ContextWindowFor(ctx context.Context, store *rings.Store, model string) int {
	if store == nil {
		return 0
	}
	cfgs, err := ListModelConfigs(ctx, store)
	if err != nil {
		warnOnce(fmt.Sprintf("读模型库失败,窗口按未配置处理: %v", err))
		return 0
	}
	fallback := 0
	activePrimary := ""
	if p, ok, _ := ActiveName(ctx, store, RolePrimary); ok {
		activePrimary = p
	}
	for _, c := range cfgs {
		if model != "" && c.Model == model && c.ContextWindow > 0 {
			return c.ContextWindow
		}
		if c.Name == activePrimary && c.ContextWindow > 0 && fallback == 0 {
			fallback = c.ContextWindow
		}
	}
	return fallback
}

// MaskKey 打码规则(第七章 §5:如 sk-…abc4):保留前 3 后 4,中段折叠;
// 过短的 key 只露尾 2 位(≤2 位全折叠);空 key 返回空串。
func MaskKey(key string) string {
	switch {
	case key == "":
		return ""
	case len(key) <= 2:
		return "…"
	case len(key) <= 8:
		return "…" + key[len(key)-2:]
	default:
		return key[:3] + "…" + key[len(key)-4:]
	}
}

// ValidFormat 格式词受控校验(与 llm.NewProvider 的接受集一致)。
func ValidFormat(f string) bool {
	switch strings.TrimSpace(strings.ToLower(f)) {
	case "openai", "anthropic", "openai-responses":
		return true
	}
	return false
}

// NormalizeFormat 归一小写;非法原样返回(由调用方校验)。
func NormalizeFormat(f string) string { return strings.TrimSpace(strings.ToLower(f)) }

// validateV2 校验 P2-08c 新增字段(词表受控/数值范围;代理 scheme 与
// llm 包 defaultHTTPClient 的接受集一致)。
func validateV2(cfg ModelConfig) error {
	if cfg.Proxy != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil || u.Host == "" {
			return fmt.Errorf("failover: 代理 %q 不是可用 URL", cfg.Proxy)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return fmt.Errorf("failover: 代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
		}
	}
	if cfg.RatePerSecond < 0 || cfg.RatePerMinute < 0 {
		return fmt.Errorf("failover: 限速不能为负数(0 = 不限)")
	}
	if cfg.PollPriority < 0 || cfg.PollPriority > 100 {
		return fmt.Errorf("failover: 轮询优先级须为 0~100(0 视同 1,小者先)")
	}
	switch cfg.MaxTokensField {
	case "", "max_tokens", llm.MaxTokensFieldCompletion:
	default:
		return fmt.Errorf("failover: 上限字段名 %q 不在词汇表内(max_tokens|max_completion_tokens)", cfg.MaxTokensField)
	}
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("failover: 输出上限不能为负数(0 = 不设)")
	}
	switch cfg.Thinking {
	case "", "enabled", "disabled":
	default:
		return fmt.Errorf("failover: 思考开关 %q 不在词汇表内(enabled|disabled|空=随端点默认)", cfg.Thinking)
	}
	switch cfg.ThinkingEffort {
	case "", "low", "medium", "high", "max":
	default:
		return fmt.Errorf("failover: 思考强度 %q 不在词汇表内(low|medium|high|max|空=不设)", cfg.ThinkingEffort)
	}
	for name, v := range map[string]int{
		"建连重试": cfg.RetryConnect, "空响应重试": cfg.RetryEmpty,
		"零交付重放": cfg.RetryReplay, "熔断阈值": cfg.RetryBreaker, "意图级重跑": cfg.RetryRerun,
	} {
		if v < -1 {
			return fmt.Errorf("failover: %s覆盖值 %d 非法(0=默认 / -1=关闭 / >0=值)", name, v)
		}
	}
	return nil
}

// ValidateEndpoint 端点级最小校验(测试连接/从 API 拉列表的草稿用):
// 不要求名称受控与模型名(拉列表正是为了选模型),只要求能发请求。
func ValidateEndpoint(cfg ModelConfig) error {
	if !ValidFormat(cfg.Format) {
		return fmt.Errorf("failover: 端点格式 %q 不在词汇表内(openai|anthropic|openai-responses)", cfg.Format)
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("failover: 端点不能为空")
	}
	if cfg.Proxy != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil || u.Host == "" {
			return fmt.Errorf("failover: 代理 %q 不是可用 URL", cfg.Proxy)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return fmt.Errorf("failover: 代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
		}
	}
	return nil
}

// SaveModelConfig 写入(或覆盖)一条模型配置。名称放开自定义(P3-05c
// ②:不限字符格式——点号/中文/空格皆可),仅保留两条技术约束:非空
// 且 1~64 字符(TrimSpace 后),以及不含 "/"(REST 路径按名称分段,
// 斜杠会破坏 /api/models/{name} 族端点)。重名拦截在调用方(新增意图
// 与编辑自身区分);key 为空且已有同名校时保留旧 key(编辑不重填场景)。
func SaveModelConfig(ctx context.Context, store *rings.Store, cfg ModelConfig, by string) error {
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.Name == "" || len(cfg.Name) > 64 {
		return fmt.Errorf("failover: 模型名须为 1~64 个字符")
	}
	if strings.Contains(cfg.Name, "/") {
		return fmt.Errorf("failover: 模型名不能含 /(会破坏按名称寻址的端点)")
	}
	if !ValidFormat(cfg.Format) {
		return fmt.Errorf("failover: 端点格式 %q 不在词汇表内(openai|anthropic|openai-responses)", cfg.Format)
	}
	cfg.Format = NormalizeFormat(cfg.Format)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.BaseURL == "" || cfg.Model == "" {
		return fmt.Errorf("failover: 端点与模型名不能为空")
	}
	if cfg.ContextWindow < 0 {
		return fmt.Errorf("failover: 上下文窗口不能为负数(0 = 未配置)")
	}
	if err := validateV2(cfg); err != nil {
		return err
	}
	if cfg.APIKey == "" {
		if old, ok, err := ModelConfigByName(ctx, store, cfg.Name); err == nil && ok {
			cfg.APIKey = old.APIKey // 编辑未重填 key:保留旧值
		}
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("failover: api key 不能为空(新配置必填)")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failover: 序列化模型配置: %w", err)
	}
	return store.SetSetting(ctx, keyLibraryPrefix+cfg.Name, string(raw), by)
}

// DeleteModelConfig 删除一条模型配置(幂等);被激活引用时一并清除
// 激活项(主/备指向不存在的配置无意义,回退到剩余可用项)。
func DeleteModelConfig(ctx context.Context, store *rings.Store, name, by string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("failover: 模型名不能为空")
	}
	if err := store.DeleteSetting(ctx, keyLibraryPrefix+name); err != nil {
		return err
	}
	if p, ok, _ := ActiveName(ctx, store, RolePrimary); ok && p == name {
		_ = store.DeleteSetting(ctx, keyActivePrimary)
	}
	if b, ok, _ := ActiveName(ctx, store, RoleBackup); ok && b == name {
		_ = store.DeleteSetting(ctx, keyActiveBackup)
	}
	return nil
}

// ListModelConfigs 全库清单(含明文 key,仅供服务端构建 Provider;
// 一切对外呈现必须先转 MaskedConfig)。按名称排序稳定输出。
func ListModelConfigs(ctx context.Context, store *rings.Store) ([]ModelConfig, error) {
	entries, err := store.SettingsWithPrefix(ctx, keyLibraryPrefix)
	if err != nil {
		return nil, fmt.Errorf("failover: 读模型库: %w", err)
	}
	out := make([]ModelConfig, 0, len(entries))
	for _, e := range entries {
		var cfg ModelConfig
		if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
			continue // 脏值跳过(fail-safe:单条坏配置不拖垮整库读取)
		}
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ModelConfigByName 按名取一条;ok=false 表示不存在。
func ModelConfigByName(ctx context.Context, store *rings.Store, name string) (ModelConfig, bool, error) {
	e, ok, err := store.Setting(ctx, keyLibraryPrefix+strings.TrimSpace(name))
	if err != nil || !ok {
		return ModelConfig{}, false, err
	}
	var cfg ModelConfig
	if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
		return ModelConfig{}, false, fmt.Errorf("failover: 模型配置 %s 损坏: %w", name, err)
	}
	return cfg, true, nil
}

// 角色词:主/备。
const (
	RolePrimary = "primary"
	RoleBackup  = "backup"
)

// SetActive 写激活项(name 为空 = 清除该角色,回退装配默认)。
func SetActive(ctx context.Context, store *rings.Store, role, name, by string) error {
	key := keyActivePrimary
	if role == RoleBackup {
		key = keyActiveBackup
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return store.DeleteSetting(ctx, key)
	}
	if _, ok, err := ModelConfigByName(ctx, store, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("failover: 模型 %q 不在库内", name)
	}
	return store.SetSetting(ctx, key, name, by)
}

// ActiveName 读激活项;ok=false 表示未激活(随装配默认)。
func ActiveName(ctx context.Context, store *rings.Store, role string) (string, bool, error) {
	key := keyActivePrimary
	if role == RoleBackup {
		key = keyActiveBackup
	}
	e, ok, err := store.Setting(ctx, key)
	if err != nil || !ok {
		return "", false, err
	}
	name := strings.TrimSpace(e.Value)
	if name == "" {
		return "", false, nil
	}
	if _, exists, _ := ModelConfigByName(ctx, store, name); !exists {
		return "", false, nil // 指向已删配置:视同未激活(fail-safe)
	}
	return name, true, nil
}

// SetTaskOverride 任务级主备覆盖(第七章 §5「任务级可覆盖」);空名 =
// 清除该角色回全局。
func SetTaskOverride(ctx context.Context, store *rings.Store, taskID, role, name, by string) error {
	if taskID == "" {
		return fmt.Errorf("failover: 任务 ID 不能为空")
	}
	key := keyTaskPrefix + taskID + "." + RolePrimary
	if role == RoleBackup {
		key = keyTaskPrefix + taskID + "." + RoleBackup
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return store.DeleteSetting(ctx, key)
	}
	if _, ok, err := ModelConfigByName(ctx, store, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("failover: 模型 %q 不在库内", name)
	}
	return store.SetSetting(ctx, key, name, by)
}

// taskOverrideName 任务级覆盖读取(缺省回全局激活名)。
func taskOverrideName(ctx context.Context, store *rings.Store, taskID, role string) (string, bool) {
	if taskID == "" {
		return "", false
	}
	key := keyTaskPrefix + taskID + "." + role
	e, ok, err := store.Setting(ctx, key)
	if err != nil || !ok {
		return "", false
	}
	name := strings.TrimSpace(e.Value)
	if name == "" {
		return "", false
	}
	if _, exists, _ := ModelConfigByName(ctx, store, name); !exists {
		return "", false
	}
	return name, true
}
