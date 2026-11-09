package failover

import (
	"sort"
	"strings"
)

// ---- 内置常见模型上下文窗口对照表 + 平台 × 格式默认端点(P4-03 ③④) ----
//
// ③ 三段式窗口填充(问题清单 #13):拉模型列表后自动填上下文窗口——
// 端点响应带 context 字段 → 直接填(最准);不带 → 查本对照表(按模型
// 名前缀匹配,长前缀优先),填完标注"自动填,请核对";都匹配不上 →
// 保守默认 200K + 提示手动核对。对照表是 UI 便利映射,不涉工具哲学。
//
// ④ 平台 × 格式默认端点(通用规则 11:造物主只给 key,端点适配是系统
// 自己的活):建模型时选平台 + 选格式,默认地址自动预填(可改);非
// 预设组合走纯手动。

// 上下文窗口保守默认(③ 第三段:未识别模型的兜底)。
const DefaultContextWindow = 200000

// builtinContextWindows 常见模型窗口对照(前缀匹配,长前缀优先)。
// 数据与官方规格核对(2026-10):GLM 系取 docs.bigmodel.cn 各模型页
// (5.3/5.3-Flash/5.2 = 1M,5/5.1/4.7/4.6 = 200K,4.5 系 = 128K;
// glm-5-turbo 无独立规格页,经 glm-5 前缀命中 200K,随"请核对"标注);
// DeepSeek 系取端点自报值(deepseek-flash / deepseek-v4-pro = 1048576,
// 旧名 deepseek-chat / deepseek-reasoner 按其历史规格 128K);claude /
// gpt 系按官方公开规格(claude 4.x 系 200K;gpt-5 系 400K;gpt-4.1
// 1M;gpt-4o 128K)。
var builtinContextWindows = []struct {
	prefix string
	window int
}{
	{"glm-5.3", 1000000},
	{"glm-5.2", 1000000},
	{"glm-5.1", 200000},
	{"glm-5", 200000},
	{"glm-4.7", 200000},
	{"glm-4.6", 200000},
	{"glm-4.5", 128000},
	{"deepseek-flash", 1048576},
	{"deepseek-v4", 1048576},
	{"deepseek-chat", 128000},
	{"deepseek-reasoner", 128000},
	{"claude-", 200000},
	{"gpt-5", 400000},
	{"gpt-4.1", 1000000},
	{"gpt-4o", 128000},
}

// LookupContextWindow 对照表查询:模型名(小写化)前缀匹配,长前缀
// 优先;未命中返回 0。
func LookupContextWindow(modelID string) int {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return 0
	}
	best, bestLen := 0, 0
	for _, e := range builtinContextWindows {
		if strings.HasPrefix(id, e.prefix) && len(e.prefix) > bestLen {
			best, bestLen = e.window, len(e.prefix)
		}
	}
	return best
}

// ---- ④ 平台 × 格式默认端点 ----

// 平台词表(面板下拉 value;空串 = 自定义,纯手动)。
const (
	PlatformGLM      = "glm"
	PlatformDeepSeek = "deepseek"
	PlatformOpenCode = "opencode"
	PlatformOpenAI   = "openai"
)

// PlatformOption 平台下拉项。
type PlatformOption struct {
	Value string `json:"value"` // "" = 自定义
	Label string `json:"label"`
}

// PlatformOptions 平台清单(面板下拉;自定义在末位)。
func PlatformOptions() []PlatformOption {
	return []PlatformOption{
		{PlatformGLM, "智谱GLM"},
		{PlatformDeepSeek, "DeepSeek"},
		{PlatformOpenCode, "OpenCode"},
		{PlatformOpenAI, "OpenAI官方"},
		{"", "自定义"},
	}
}

// platformPresets 平台 × 格式 → 默认端点。glm / deepseek 双格式经真实
// 端点验证(2026-10);opencode 为接入方式的默认形态(Go 订阅,实测全链
// 2026-10,问题清单 #22),另一形态见 opencodeModePresets;openai 官方
// 仅 openai 格式(无 anthropic 端点,留空走纯手动)。注意 anthropic
// 格式的 BaseURL 不带 /v1(Norma 请求时追加 /v1/messages;go 网关实测
// .../anthropic/v1/messages 200,双 /v1 404)。
var platformPresets = map[string]map[string]string{
	PlatformGLM: {
		"anthropic": "https://open.bigmodel.cn/api/anthropic",
		"openai":    "https://open.bigmodel.cn/api/paas/v4",
	},
	PlatformDeepSeek: {
		"anthropic": "https://api.deepseek.com/anthropic",
		"openai":    "https://api.deepseek.com",
	},
	PlatformOpenCode: {
		"anthropic": "https://opencode.ai/inference/go/anthropic",
		"openai":    "https://opencode.ai/inference/go/openai/v1",
	},
	PlatformOpenAI: {
		"openai": "https://api.openai.com/v1",
	},
}

// ---- 思考回传规则预设(P4-57 ①,问题清单 #76) ----
//
// 各家端点对历史思考内容(reasoning_content / thinking 块)的要求相反:
// 出站组装按当前服务腿的规则确定性处理,一处收口(见 thinking.go)。
// 规则三值:传回(历史思考原样带上)/ 剔除(剥掉)/ 不干预(系统不动,
// 引擎行为原样)。预设映射经真实端点实测(2026-11-08,glm-5.3-flash /
// deepseek-flash):glm 两格式传回可行;deepseek openai 按造物主定案取
// 剔除;deepseek anthropic 实测传回与剔除均被接受,按 anthropic 协议
// 原生语义取传回;openai 官方不干预;自定义/未知端点默认剔除(剔除
// 恒安全:最坏多耗一轮思考,传错规则则确定性 400)。

// 思考回传规则词表(配置值与面板下拉共用)。
const (
	ThinkingPassback = "passback" // 传回
	ThinkingStrip    = "strip"    // 剔除
	ThinkingNone     = "none"     // 不干预
)

// ThinkingPassbackOption 规则下拉项(label 为界面词)。
type ThinkingPassbackOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

// ThinkingPassbackOptions 规则清单(建模型弹窗下拉;顺序即呈现序)。
func ThinkingPassbackOptions() []ThinkingPassbackOption {
	return []ThinkingPassbackOption{
		{ThinkingPassback, "传回", "历史思考内容原样随请求带上"},
		{ThinkingStrip, "剔除", "历史思考内容不随请求发送(默认)"},
		{ThinkingNone, "不干预", "系统不做处理,按引擎默认发送"},
	}
}

// platformThinkingPassback 平台 × 格式 → 默认思考回传规则(建模型时
// 预填;未列组合无预设,走剔除默认)。
var platformThinkingPassback = map[string]map[string]string{
	PlatformGLM: {
		"openai":    ThinkingPassback,
		"anthropic": ThinkingPassback,
	},
	PlatformDeepSeek: {
		"openai":    ThinkingStrip,
		"anthropic": ThinkingPassback,
	},
	PlatformOpenAI: {
		"openai": ThinkingNone,
	},
}

// ThinkingPassbackPreset 平台 × 格式的预设规则;无预设组合返回空串
// (调用方落剔除默认)。
func ThinkingPassbackPreset(platform, format string) string {
	m, ok := platformThinkingPassback[strings.TrimSpace(strings.ToLower(platform))]
	if !ok {
		return ""
	}
	return m[NormalizeFormat(format)]
}

// ThinkingPassbackPresets 全部预设快照(随 /api/models 下发,建模型
// 弹窗按平台+格式联动预填)。
func ThinkingPassbackPresets() map[string]map[string]string {
	out := make(map[string]map[string]string, len(platformThinkingPassback))
	for p, m := range platformThinkingPassback {
		cm := make(map[string]string, len(m))
		for f, v := range m {
			cm[f] = v
		}
		out[p] = cm
	}
	return out
}

// hostPlatforms 端点主机 → 平台(存量配置/环境变量腿未带规则字段时按
// 端点归一匹配的运行时兜底;与 platformPresets 同源主机)。
var hostPlatforms = []struct {
	hostSuffix string
	platform   string
}{
	{"bigmodel.cn", PlatformGLM},
	{"deepseek.com", PlatformDeepSeek},
	{"api.openai.com", PlatformOpenAI},
}

// PassbackByBaseURL 按端点地址与格式推断规则(URL 主机后缀匹配 → 平台
// 预设表);匹配不上返回剔除(自定义/未知端点默认,可经配置字段显式改)。
func PassbackByBaseURL(baseURL, format string) string {
	u := strings.TrimSpace(baseURL)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	for _, e := range hostPlatforms {
		if strings.HasSuffix(u, e.hostSuffix) {
			if r := ThinkingPassbackPreset(e.platform, format); r != "" {
				return r
			}
			break
		}
	}
	return ThinkingStrip
}

// ---- opencode 接入方式双形态(P4-10 ①,问题清单 #22) ----
//
// Go 订阅(默认):走 inference/go 网关,请求自动注入稳定会话头与自报
// User-Agent(见 opencode.go),拉模型列表走网关根 /v1/models;API 按
// 量:走 zen 端点,不注入头。面板"接入方式"下拉切换预填,端点地址是
// 行为载体(见 isOpenCodeGoURL)。

// 接入方式词表(opencode)。
const (
	OpenCodeModeGo  = "go"  // Go 订阅(默认)
	OpenCodeModeAPI = "api" // API 按量
)

// opencodeModePresets opencode 接入方式 × 格式 → 默认端点(Go 订阅与
// platformPresets 主表现同;zen 按量实测 402 属未充 API 额度,非端点
// 错误)。anthropic 两形态均不带 /v1(Norma anthropic 追加 /v1/messages;
// go 网关实测 .../anthropic/v1/messages 200——子计划预填串 .../anthropic/v1
// 会拼出双 /v1 404,按 #22 实测 URL 勘误为 .../anthropic)。
var opencodeModePresets = map[string]map[string]string{
	OpenCodeModeGo: {
		"anthropic": "https://opencode.ai/inference/go/anthropic",
		"openai":    "https://opencode.ai/inference/go/openai/v1",
	},
	OpenCodeModeAPI: {
		"anthropic": "https://opencode.ai/zen",
		"openai":    "https://opencode.ai/zen/v1",
	},
}

// OpenCodeModeURL opencode 接入方式 × 格式的默认端点;非法方式或格式
// 返回空串。
func OpenCodeModeURL(mode, format string) string {
	m, ok := opencodeModePresets[strings.TrimSpace(strings.ToLower(mode))]
	if !ok {
		return ""
	}
	return m[NormalizeFormat(format)]
}

// OpenCodeModePresetURLs 接入方式双形态快照(随 /api/models 下发,建
// 模型弹窗"接入方式"预填数据源)。
func OpenCodeModePresetURLs() map[string]map[string]string {
	out := make(map[string]map[string]string, len(opencodeModePresets))
	for mode, m := range opencodeModePresets {
		cm := make(map[string]string, len(m))
		for f, u := range m {
			cm[f] = u
		}
		out[mode] = cm
	}
	return out
}

// PlatformDefaultURL 平台 × 格式的默认端点;非预设组合(含自定义平台、
// openai-responses)返回空串走纯手动。
func PlatformDefaultURL(platform, format string) string {
	m, ok := platformPresets[strings.TrimSpace(strings.ToLower(platform))]
	if !ok {
		return ""
	}
	return m[NormalizeFormat(format)]
}

// PlatformPresetURLs 全部预设端点(面板预填判定"当前地址是否仍是预设
// 值"用;也随 /api/models 下发)。
func PlatformPresetURLs() map[string]map[string]string {
	out := make(map[string]map[string]string, len(platformPresets))
	for p, m := range platformPresets {
		cm := make(map[string]string, len(m))
		for f, u := range m {
			cm[f] = u
		}
		out[p] = cm
	}
	return out
}

// IsKnownPlatform 平台词是否在预设清单内(自定义返回 false)。
func IsKnownPlatform(platform string) bool {
	_, ok := platformPresets[strings.TrimSpace(strings.ToLower(platform))]
	return ok
}

// sortedPlatformNames 测试辅助:稳定序的平台名清单。
func sortedPlatformNames() []string {
	out := make([]string, 0, len(platformPresets))
	for p := range platformPresets {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
