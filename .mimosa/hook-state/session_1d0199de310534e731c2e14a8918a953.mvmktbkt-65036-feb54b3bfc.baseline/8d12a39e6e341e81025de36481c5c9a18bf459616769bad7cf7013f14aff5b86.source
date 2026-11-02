package overmind

import (
	"strings"
	"testing"
)

// P4-24 ② 确定性单测:模式闸门特征扫描——六类特征各命中一例、正常
// 人话文本零命中(数字/路径/单位号/软件版本号等合法事实不误伤)。

func TestScanMachineTalkFeatures(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // 期望命中的特征名
	}{
		{"内部编号(tc)", "判断 tc-8f2a1b3c 成立", "内部编号"},
		{"内部编号(tc-chain)", "组合判断 tc-chain-9ab1c2d3 已确认", "内部编号"},
		{"解释编号", "竞争解释 alt2 未排除", "解释编号"},
		{"裸版本号数字", "链路回放见版本 42", "裸版本号数字"},
		{"程序报错EOF", "请求失败(EOF)", "程序报错原文"},
		{"程序报错Get", `报错原文: Get "http://x/": dial fail`, "程序报错原文"},
		{"程序报错网络", "连接被拒(connection refused)", "程序报错原文"},
		{"英文状态词", "该步骤当前 blocked", "英文状态词"},
		{"英文状态词大小写", "会话已 Completed", "英文状态词"},
		{"压缩名词堆", "本段电报体:代理旁路规则组命中放行验证", "无动词压缩名词堆"},
		{"压缩名词堆长段", "下一行:端口扫描结果指纹比对失败重试全流程记录", "无动词压缩名词堆"},
	}
	for _, c := range cases {
		hits := ScanMachineTalk(c.text)
		found := false
		for _, h := range hits {
			if h.Kind == c.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: 应命中 %q,得到 %+v", c.name, c.want, hits)
		}
	}
}

func TestScanMachineTalkClean(t *testing.T) {
	clean := []string{
		// 锚例与正常指令式描述:数字/路径/方法/凭据形态全合法。
		"向 10.0.0.5 发 GET /admin,无需凭据;返回 200 且响应含\"管理后台\"字样即复现",
		"向 k3.local 发 POST /login,请求体 user=admin&pass=admin(等 24 字节表单),无需凭据;返回 302 跳转 /home 即复现",
		// 叙述锚例:单位号/轮次/短紧凑句。
		"单位 u1 执行「拉取 127.0.0.1:4461 首页与 JS,判定服务类型」时,在 /static/app.js 的 200 响应正文里发现硬编码凭据;u2 独立反证 1 轮排除缓存污染与不可复现两条解释,仲裁者判定成立。",
		// 确定性兜底描述。
		"请求 /admin,目标返回 200",
		"提交 /login(请求体 24 字节),目标返回 200",
		// 软件版本号是事实不是账本版本号;URL 内英文词不误伤。
		"目标是 Apache 2.4.49,访问 /api/status 返回 401",
		// 正常短句无功能字但短于阈值(紧凑中文非电报体)。
		"仲裁者判定成立",
		// 币种/时间类小数与数字。
		"耗时 1.5 秒,共 3 轮",
	}
	for _, text := range clean {
		if hits := ScanMachineTalk(text); len(hits) > 0 {
			t.Fatalf("正常文本不应命中: %q → %+v", text, hits)
		}
	}
}

func TestMachineTalkURLMasking(t *testing.T) {
	// URL / 路径里的英文状态词不误伤(掩掉后再判)。
	if hits := ScanMachineTalk("访问 http://t.local/api/status?retry=1 与 /login done 页面"); len(hits) > 0 {
		t.Fatalf("URL 内状态词应被掩掉: %+v", hits)
	}
	// 正文里的裸状态词仍命中。
	if hits := ScanMachineTalk("这个步骤已经 timeout 了"); len(hits) == 0 {
		t.Fatal("正文裸状态词应命中")
	}
}

func TestTalkHitLines(t *testing.T) {
	hits := ScanMachineTalk("判断 tc-8f2a1b3c 成立")
	lines := talkHitLines(hits)
	if len(lines) == 0 || !strings.Contains(lines[0], "内部编号") || !strings.Contains(lines[0], "tc-8f2a1b3c") {
		t.Fatalf("命中行应含特征名与样例: %+v", lines)
	}
}
