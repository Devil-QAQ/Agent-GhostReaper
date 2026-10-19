package mocktarget

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestMockTargetServeAndCount(t *testing.T) {
	s, err := Start()
	if err != nil {
		t.Fatalf("起靶站失败: %v", err)
	}
	t.Cleanup(s.Close)

	// 主站:首页指纹 / robots 线索 / 版本接口 / 404。
	code, body := get(t, s.MainURL()+"/")
	if code != 200 || !strings.Contains(body, "gr-mock-main") {
		t.Fatalf("主站首页不符: %d %s", code, body)
	}
	if code, body = get(t, s.MainURL()+"/robots.txt"); code != 200 || !strings.Contains(body, "Disallow: /old-backup") {
		t.Fatalf("robots.txt 不符: %d %s", code, body)
	}
	if code, body = get(t, s.MainURL()+"/api/version"); code != 200 || !strings.Contains(body, `"version":"2.4.1"`) {
		t.Fatalf("版本接口不符: %d %s", code, body)
	}
	if code, _ = get(t, s.MainURL()+"/nope"); code != 404 {
		t.Fatalf("未知路径应 404,得到 %d", code)
	}

	// 管理后台:一律 401 带质询与指纹。
	if code, body = get(t, s.AdminURL()+"/"); code != 401 || !strings.Contains(body, "mock admin console") {
		t.Fatalf("管理后台应 401 带指纹: %d %s", code, body)
	}

	// 命中计数器:测试侧观测"靶站确实被真实探测"。
	if n := s.Hits("main", "/"); n != 1 {
		t.Fatalf("主站首页应命中 1 次,得到 %d", n)
	}
	if n := s.Hits("main", "/api/version"); n != 1 {
		t.Fatalf("版本接口应命中 1 次,得到 %d", n)
	}
	if n := s.Hits("admin", "/"); n != 1 {
		t.Fatalf("管理后台应命中 1 次,得到 %d", n)
	}
	if n := s.Hits("main", "/never"); n != 0 {
		t.Fatalf("未请求路径应 0 次,得到 %d", n)
	}
	get(t, s.MainURL()+"/")
	if n := s.Hits("main", "/"); n != 2 {
		t.Fatalf("二次请求应计数 2,得到 %d", n)
	}

	// 两个服务是独立端口(两个可种子化的资产)。
	if s.MainURL() == s.AdminURL() {
		t.Fatalf("主站与管理后台应在不同端口")
	}
}
