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
		{"", "自定义"},
	}
}

// platformPresets 平台 × 格式 → 默认端点。glm / deepseek 双格式经真实
// 端点验证(2026-10);opencode 按公开文档预设(无 key 未实测,地址可改)。
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
		"anthropic": "https://opencode.ai/zen",
		"openai":    "https://opencode.ai/zen/v1",
	},
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
