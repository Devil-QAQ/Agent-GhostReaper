package report

import "testing"

// P2-10:修复建议查表(R17 确定性、零 LLM)与事实成果类分类的确定性
// 验证——面板详情抽屉与报告导出同源共用这两个入口。

func TestRemediation(t *testing.T) {
	cases := []struct {
		vulnclass string
		wantHit   bool
	}{
		{"弱口令", true},
		{"SQL注入", true},
		{"SQL 注入(盲注)", true}, // 词形包含:别名出现在 vulnclass 里
		{"备份文件暴露", true},     // vulnclass 是别名的更长写法
		{"某种从未见过的类型", false}, // 未命中不编造
		{"", false},          // 未声明类型
		{"  未授权访问  ", true},  // 首尾空白容忍
	}
	for _, c := range cases {
		got := Remediation(c.vulnclass)
		if c.wantHit && len(got) == 0 {
			t.Errorf("Remediation(%q) 未命中,期望命中", c.vulnclass)
		}
		if !c.wantHit && got != nil {
			t.Errorf("Remediation(%q) 命中 %d 条,期望未命中", c.vulnclass, len(got))
		}
	}
	// 确定性:同输入两次查询逐字一致(报告字节级可重现的前提)。
	a, b := Remediation("弱口令"), Remediation("弱口令")
	if len(a) != len(b) {
		t.Fatalf("同输入两次查询条数不一致:%d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("同输入两次查询第 %d 条不一致", i)
		}
	}
	// 同源前提:返回的切片不被读侧就地修改污染表(返回底层数组可被
	// 读侧追加改写——查表侧每次返回同一数组,读侧只读;此处固化当前
	// 语义,防止有人复制表后漂移)。
	if Remediation("信息暴露")[0] != "从响应、页面与公开文件中移除该敏感内容" {
		t.Error("信息暴露表项内容被改动,面板与报告将随之改变,须同步更新两侧验收")
	}
}

func TestLootClass(t *testing.T) {
	cases := []struct {
		statement string
		want      string
	}{
		{"备份文件 config.bak 内含管理后台账号与明文密码", LootCredential},
		{"用户数据接口可未授权导出,共 812 条记录", LootData}, // "未授权"不误判权限
		{"以普通用户身份可执行管理员删除操作(越权)", LootPermission},
		{"上传目录的 php 文件可被执行,已落 webshell", LootFoothold},
		{"目标 8080 端口运行 nginx 1.18.0", ""}, // 普通观察,不属战果
		{"凭据与越权同时出现时取凭据", LootCredential}, // 规则顺序:凭据先于权限
		{"普通会话可持续 12 小时", ""},             // "会话"泛用词不进凭据
	}
	for _, c := range cases {
		if got := LootClass(c.statement); got != c.want {
			t.Errorf("LootClass(%q)=%q,期望 %q", c.statement, got, c.want)
		}
	}
	// 大小写不敏感(英文关键词)。
	if LootClass("取得有效 Session 会话标识") != LootCredential {
		t.Error("英文关键词大小写未归一")
	}
}
