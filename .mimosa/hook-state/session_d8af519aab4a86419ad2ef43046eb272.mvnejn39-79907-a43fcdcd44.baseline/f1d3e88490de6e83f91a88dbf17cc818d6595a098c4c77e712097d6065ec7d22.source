package server

// P3-13c UA 策略配置 API:读段默认 random、写后同源返回、模式与
// 固定值校验(拼错 400、fixed 空 400)。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestUAConfigAPI UA 策略读写:GET /api/config 带 ua 段(缺省
// random);POST /api/config/ua 三态写与校验。
func TestUAConfigAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	h := srv.Handler()

	type uaView struct {
		Mode  string `json:"mode"`
		Fixed string `json:"fixed"`
	}
	readUA := func() uaView {
		t.Helper()
		var cfg struct {
			UA uaView `json:"ua"`
		}
		get(t, h, "/api/config", http.StatusOK, &cfg)
		return cfg.UA
	}

	// 读:缺省 random、无固定值。
	if ua := readUA(); ua.Mode != "random" || ua.Fixed != "" {
		t.Fatalf("缺省 UA 策略应为 random/无固定值: %+v", ua)
	}

	post := func(payload string) uaView {
		t.Helper()
		rec := doReq(t, h, http.MethodPost, "/api/config/ua", payload)
		if rec.Code != http.StatusOK {
			t.Fatalf("写 UA 策略失败(%s): %d %s", payload, rec.Code, rec.Body.String())
		}
		var v uaView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("解码写响应失败: %v (%s)", err, rec.Body.String())
		}
		return v
	}
	postWant := func(payload string, wantCode int, wantErrPart string) {
		t.Helper()
		rec := doReq(t, h, http.MethodPost, "/api/config/ua", payload)
		if rec.Code != wantCode || (wantErrPart != "" && !strings.Contains(rec.Body.String(), wantErrPart)) {
			t.Fatalf("POST %s = %d %s, want %d 含 %q", payload, rec.Code, rec.Body.String(), wantCode, wantErrPart)
		}
	}

	// 写 fixed:同源返回原值;回读一致(UA 原值回显,非凭据)。
	if v := post(`{"mode":"fixed","fixed":"MyAgent/2.0 (x86)"}`); v.Mode != "fixed" || v.Fixed != "MyAgent/2.0 (x86)" {
		t.Fatalf("写后应同源返回: %+v", v)
	}
	if ua := readUA(); ua.Mode != "fixed" || ua.Fixed != "MyAgent/2.0 (x86)" {
		t.Fatalf("回读不符: %+v", ua)
	}

	// 切 tools:固定值清空(删键恢复默认语义)。
	if v := post(`{"mode":"tools","fixed":""}`); v.Mode != "tools" || v.Fixed != "" {
		t.Fatalf("切 tools 后同源返回不符: %+v", v)
	}
	if ua := readUA(); ua.Mode != "tools" || ua.Fixed != "" {
		t.Fatalf("切 tools 后回读不符: %+v", ua)
	}

	// 拼错模式:400。
	postWant(`{"mode":"brave"}`, http.StatusBadRequest, "模式")
	// fixed 模式空固定值:400。
	postWant(`{"mode":"fixed","fixed":"  "}`, http.StatusBadRequest, "User-Agent")
}
