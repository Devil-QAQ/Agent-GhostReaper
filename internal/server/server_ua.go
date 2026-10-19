package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/uapolicy"
)

// UA 策略(P3-13c 流量去特征,系统设置「UA 策略」卡):http_probe /
// web_fetch 出站请求的 User-Agent——随机浏览器池(默认)/ 固定自定义 /
// 模拟常见工具。工具每次请求现读配置,保存对下一次请求即生效。bash
// 里模型自拼命令的 UA 不受本配置影响(完全开放原则)。

// uaBody UA 策略写体:mode 三值必填;fixed 仅在 fixed 模式下要求非空。
type uaBody struct {
	Mode  string `json:"mode"`
	Fixed string `json:"fixed"`
}

// handleUAUpdate UA 策略写 API:写设置表(ua.* 键值)。UA 不是凭据,
// 原值回显(与搜索 key 的打码纪律不同)。
func (s *Server) handleUAUpdate(w http.ResponseWriter, r *http.Request) {
	var body uaBody
	if !readJSON(w, r, &body) {
		return
	}
	// 模式显式校验(不静默归一):拼错如实 400,防误存默认。
	switch strings.ToLower(strings.TrimSpace(body.Mode)) {
	case uapolicy.ModeRandom, uapolicy.ModeFixed, uapolicy.ModeTools:
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("模式须为 random/fixed/tools 之一"))
		return
	}
	fixed := strings.TrimSpace(body.Fixed)
	mode := uapolicy.NormalizeMode(body.Mode)
	if mode == uapolicy.ModeFixed && fixed == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("固定自定义模式须填写 User-Agent 原文"))
		return
	}
	// 非 fixed 模式提交的固定值照存(切回 fixed 时还在);random/tools
	// 下空串 = 清除。
	if err := uapolicy.Save(r.Context(), s.Store, uapolicy.Config{Mode: mode, Fixed: fixed}, "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.effectiveUA(r.Context()))
}

// effectiveUA 读设置表拼 UA 策略视图(handleConfig 每次重算,写路径
// 保存后同源返回)。
func (s *Server) effectiveUA(ctx context.Context) uapolicy.Config {
	return uapolicy.Load(ctx, s.Store)
}
