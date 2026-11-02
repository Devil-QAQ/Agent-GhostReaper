// Package uapolicy 出站请求的 User-Agent 可配置策略(P3-13c 流量去
// 特征)。三种模式:随机浏览器池(默认)/ 固定自定义 / 模拟常见工具。
// 作用面是自家工具(http_probe / web_fetch)对目标与外网的请求;bash
// 里模型自拼命令的 UA 不干预(完全开放原则,哲学红线);web_search
// 对搜索后端的 UA 是功能必需(非浏览器 UA 被 DuckDuckGo 拦),不走
// 本策略。
package uapolicy

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"ghostreaper/internal/rings"
)

// 模式标识(设置值,面板与工具同一份词汇)。
const (
	ModeRandom = "random" // 随机浏览器池(默认)
	ModeFixed  = "fixed"  // 固定自定义
	ModeTools  = "tools"  // 模拟常见工具
)

// 设置键(rings 通用配置键值表,命名空间 ua.*;与 search.* 同款形态)。
const (
	SettingKeyMode  = "ua.mode"
	SettingKeyFixed = "ua.fixed"
)

// browserPool 随机浏览器池:现代四家(Chrome/Firefox/Safari/Edge)×
// 三平台。版本取写定时点的稳定形态,不求追新——池的目的是去掉固定
// 指纹,不是精确伪装。
var browserPool = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
}

// toolPool 常见工具池:命令行取数工具的默认 UA 形态(低调,与 bash
// curl 的流量形态一致,不显眼)。
var toolPool = []string{
	"curl/8.7.1",
	"Wget/1.24.5 (linux-gnu)",
	"python-requests/2.32.3",
	"Go-http-client/2.0",
	"axios/1.7.7",
	"httpie/3.2.3",
}

// Config 一次请求的生效策略(Load 从设置表拼出;零值 = 默认随机池)。
// json tag 供面板配置视图共用(/api/config 的 ua 段原样序列化)。
type Config struct {
	Mode  string `json:"mode"`  // random(默认)|fixed|tools
	Fixed string `json:"fixed"` // 固定自定义 UA(fixed 模式用)
}

// NormalizeMode 归一模式:已知三值原样,其余(含空)归 random——默认
// 池是设计决策(子计划 P3-13c),未知值不报错按默认跑(fail-safe,
// 配置问题由设置页读路径呈现,不挡工具)。
func NormalizeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ModeFixed:
		return ModeFixed
	case ModeTools:
		return ModeTools
	default:
		return ModeRandom
	}
}

// ModeLabel 模式的展示名(面板共用,操作者语言)。
func ModeLabel(m string) string {
	switch NormalizeMode(m) {
	case ModeFixed:
		return "固定自定义"
	case ModeTools:
		return "模拟常见工具"
	default:
		return "随机浏览器"
	}
}

// Load 从设置表拼出生效策略。读失败/缺键按默认(random、无固定值)。
func Load(ctx context.Context, store *rings.Store) Config {
	cfg := Config{Mode: ModeRandom}
	if store == nil {
		return cfg
	}
	get := func(key string) string {
		e, ok, err := store.Setting(ctx, key)
		if err != nil || !ok {
			return ""
		}
		return strings.TrimSpace(e.Value)
	}
	cfg.Mode = NormalizeMode(get(SettingKeyMode))
	cfg.Fixed = get(SettingKeyFixed)
	return cfg
}

// Save 整份写入设置表:空值 = 删键(键值表值非空约束下,删键即"恢复
// 默认"的既定语义,与 search.* 同款)。by 为写者标记(面板路径恒
// creator)。
func Save(ctx context.Context, store *rings.Store, cfg Config, by string) error {
	kv := map[string]string{
		SettingKeyMode:  NormalizeMode(cfg.Mode),
		SettingKeyFixed: cfg.Fixed,
	}
	for k, v := range kv {
		var err error
		if v == "" {
			err = store.DeleteSetting(ctx, k)
		} else {
			err = store.SetSetting(ctx, k, v, by)
		}
		if err != nil {
			return fmt.Errorf("uapolicy: 写配置 %s: %w", k, err)
		}
	}
	return nil
}

// Pick 按策略取一个 User-Agent。粒度:每次调用现抽——配置对下一次
// 请求即生效(与搜索配置同语义);fixed 模式固定值为空时回退随机池
// (fail-safe:面板侧拦空值,这里兜底防配置被直写库绕成空 UA)。
func (c Config) Pick() string {
	switch NormalizeMode(c.Mode) {
	case ModeFixed:
		if c.Fixed != "" {
			return c.Fixed
		}
		return pick(browserPool)
	case ModeTools:
		return pick(toolPool)
	default:
		return pick(browserPool)
	}
}

// pick 池内均匀随机取一(池非空是包内常量保证)。
func pick(pool []string) string {
	if len(pool) == 0 {
		return ""
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
	if err != nil {
		// 加密随机源失效(理论上不可能):退化取末位,不因熵源挡请求。
		return pool[len(pool)-1]
	}
	return pool[n.Int64()]
}
