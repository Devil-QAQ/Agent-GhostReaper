package websearch

import (
	"context"
	"ghostreaper/internal/rings"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Published request limits, checked 2026-11-28. Credits, data rows and money are
// deliberately not treated as requests. Unknown public limits add no local cap.
type LimitProfile struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	QPS       float64 `json:"qps,omitempty"`
	PerMinute int     `json:"per_minute,omitempty"`
	PerDay    int     `json:"per_day,omitempty"`
	Source    string  `json:"source"`
	Note      string  `json:"note"`
}

func LimitProfiles() map[string][]LimitProfile {
	return map[string][]LimitProfile{
		"fofa": {
			{"personal", "个人版 / 专业版", 1, 0, 0, "https://fofa.info/vip", "官方每秒 1 次；数据条数与 F 点另按账号权益"},
			{"business", "商业版", 2, 0, 0, "https://fofa.info/vip", "官方每秒 2 次"},
		},
		"zoomeye": {
			{"free", "免费 / 点数账号", 0.5, 0, 0, "https://www.zoomeye.ai/pricing", "官方每秒 0.5 次；API 权限另按账号权益"},
			{"personal", "个人 / 专业 / 商业版", 1, 0, 0, "https://www.zoomeye.ai/pricing", "官方每秒 1 次"},
			{"corporate", "Corporate", 2, 0, 0, "https://www.zoomeye.ai/pricing", "官方每秒 2 次"},
		},
		"brave": {{"search", "Search", 50, 0, 0, "https://api-dashboard.search.brave.com/app/plans", "当前 Search 档每秒 50 次；旧套餐以响应额度为准"}},
		"tavily": {
			{"free", "Researcher（免费）", 0, 100, 0, "https://docs.tavily.com/documentation/rate-limits", "官方每分钟 100 次；积分另计"},
			{"paid", "PAYGO / Project / Bootstrap / Startup", 0, 1000, 0, "https://docs.tavily.com/documentation/rate-limits", "官方每分钟 1000 次"},
			{"growth", "Growth / Scale", 0, 1500, 0, "https://docs.tavily.com/documentation/rate-limits", "官方每分钟 1500 次"},
		},
		"deepseek": {{"search", "Search API", 100, 0, 0, "https://api-docs.deepseek.com/guides/search/", "官方每秒 100 次，账号内共用"}},
		"zerozone": {{"standard", "标准账号", 0, 0, 250, "https://0.zone/applyParticulars?type=api_use", "标准每天 250 次，邀请奖励及模块权益另计；本地按连续 24 小时计数"}},
	}
}
func selectedLimit(cfg Config) LimitProfile {
	rows := LimitProfiles()[cfg.Kind]
	for _, p := range rows {
		if p.ID == cfg.LimitPlan {
			return p
		}
	}
	if len(rows) > 0 {
		return rows[0]
	}
	return LimitProfile{}
}

// A key's actual policy can be lower than the public plan. Apply its response
// limits before another caller reserves a request, including monthly windows.
func recordSitePolicy(store *rings.Store, cfg Config, h http.Header) error {
	if cfg.Kind != "brave" || h.Get("X-RateLimit-Policy") == "" {
		return nil
	}
	policies := strings.Split(h.Get("X-RateLimit-Policy"), ",")
	remaining := strings.Split(h.Get("X-RateLimit-Remaining"), ",")
	resets := strings.Split(h.Get("X-RateLimit-Reset"), ",")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assetMu.Lock()
	defer assetMu.Unlock()
	m, err := readAssetUsage(ctx, store)
	if err != nil {
		return err
	}
	key := assetAccount(cfg)
	u := m[key]
	now := time.Now()
	for i, raw := range policies {
		parts := strings.Split(strings.TrimSpace(raw), ";w=")
		if len(parts) != 2 {
			continue
		}
		limit, e1 := strconv.ParseFloat(parts[0], 64)
		window, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 != nil || e2 != nil || limit <= 0 || window <= 0 {
			continue
		}
		if window == 1 {
			u.OfficialQPS = limit * 0.9
		}
		if i >= len(remaining) || i >= len(resets) {
			continue
		}
		left, e1 := strconv.ParseFloat(strings.TrimSpace(remaining[i]), 64)
		seconds, e2 := strconv.ParseFloat(strings.TrimSpace(resets[i]), 64)
		if e1 == nil && e2 == nil && seconds > 0 && (left <= 0 || (window > 1 && limit-left >= math.Floor(limit*0.9))) {
			u.Blocked = maxTime(u.Blocked, now.Add(time.Duration(seconds*float64(time.Second))))
		}
	}
	m[key] = u
	return saveAssetUsage(ctx, store, m)
}
