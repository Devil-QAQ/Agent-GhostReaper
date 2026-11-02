package overmind

// ---- P4-27 ③ 模式闸门新特征:句首裸端口数字(问题清单 #44)----

import (
	"strings"
	"testing"
)

// TestBarePortSubjectHits 裸端口作主语命中:句首/分句首的 2–5 位端口
// 数字,脱离上下文读不出说的是哪个目标。
func TestBarePortSubjectHits(t *testing.T) {
	for _, text := range []string{
		"9902 持续响应",
		"9902 的 /login 强制要求登录",
		"验证完成。9902 返回 200",
		"80 拒绝期间,服务不可用",
	} {
		if h, hit := barePortKind(text); !hit {
			t.Fatalf("应命中句首裸端口: %q", text)
		} else if h.Kind != "句首裸端口数字" {
			t.Fatalf("特征名不符: %+v", h)
		}
	}
	// 全链:ScanMachineTalk 也带上该特征。
	hits := ScanMachineTalk("9902 持续响应")
	found := false
	for _, h := range hits {
		if h.Kind == "句首裸端口数字" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ScanMachineTalk 应含句首裸端口命中: %+v", hits)
	}
}

// TestBarePortSubjectNonHits 误伤排除:完整标识(IP 起头/host:port/
// 域名)、计数量词、锚例句式不命中。
func TestBarePortSubjectNonHits(t *testing.T) {
	for _, text := range []string{
		"10.0.0.5 发 GET /admin,无需凭据",
		"jkysd.cn:9902 的登录页可脚本解算",
		"向 10.0.0.5:8080 发请求",
		"20 个测试全部通过",
		"3 个端口开放",
		"返回 200 且响应含管理后台字样即复现",
		"两轮验证后确认结论成立",
	} {
		if _, hit := barePortKind(text); hit {
			t.Fatalf("不应命中(误伤): %q", text)
		}
		if hits := ScanMachineTalk(text); len(hits) > 0 {
			// 允许其他特征命中,但裸端口特征不得在其中。
			for _, h := range hits {
				if h.Kind == "句首裸端口数字" {
					t.Fatalf("不应命中裸端口(误伤): %q → %+v", text, hits)
				}
			}
		}
	}
}

// TestBarePortRewriteFlow 闸门退回重写链路:命中样例进改写指令(特征
// 名 + 样例),模型改写为完整标识后过闸。
func TestBarePortRewriteFlow(t *testing.T) {
	hits := ScanMachineTalk("9902 持续响应")
	if len(hits) == 0 {
		t.Fatal("应有命中")
	}
	lines := talkHitLines(hits)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "句首裸端口数字") {
		t.Fatalf("改写指令应带特征名: %q", joined)
	}
	// 改写后(完整标识形态)零命中。
	if _, hit := barePortKind("jkysd.cn:9902 持续响应"); hit {
		t.Fatal("完整标识形态不应命中")
	}
}
