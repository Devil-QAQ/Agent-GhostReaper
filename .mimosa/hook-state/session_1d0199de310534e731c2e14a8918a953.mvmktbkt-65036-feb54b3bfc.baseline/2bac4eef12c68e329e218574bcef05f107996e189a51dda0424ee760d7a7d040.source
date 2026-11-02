package swarm

// ---- P4-27 ⑨ 审批卡危害说明三段式(锚例逐字,造物主定稿 2026-10-24)----

import (
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// TestHarmNoteAnchors 锚例逐字核验(注册类/查看类各一,三段式齐全):
// 注册类——"将向 {host} 提交注册表单创建账号。目标会新增一个可登录
// 账号。账号会留存于目标系统，管理员可能察觉异常注册。"(目标以完整
// 标识 host:port 呈现,锚例中"9902"即目标站点的 host:port 位);
// 查看类——"将执行 pwd 与 ls 查看目录和文件列表。目标不会发生任何
// 变化。"
func TestHarmNoteAnchors(t *testing.T) {
	// 查看类锚例:pwd 与 ls(L1 只读)。
	got := HarmNote(rings.ActionReadOnly, "bash", json.RawMessage(`{"command":"pwd && ls"}`))
	want := "将执行 pwd 与 ls 查看目录和文件列表。目标不会发生任何变化。"
	if got != want {
		t.Fatalf("查看类锚例逐字不符:\n得 %q\n要 %q", got, want)
	}

	// 注册类锚例(L4 + 带载荷提交注册端点):后两句逐字,首句目标为
	// 完整标识(锚例"9902"位)。
	got = HarmNote(rings.ActionModifyPersist, "bash", json.RawMessage(
		`{"command":"curl -X POST -d 'username=gr&password=xx' http://jkysd.cn:9902/register"}`))
	want = "将向 jkysd.cn:9902 提交注册表单创建账号。目标会新增一个可登录账号。账号会留存于目标系统，管理员可能察觉异常注册。"
	if got != want {
		t.Fatalf("注册类锚例逐字不符:\n得 %q\n要 %q", got, want)
	}
}

// TestHarmNoteClasses 各分级三段式齐全:动作/目标变化/最坏后果三句;
// 只读类按锚例句式为两段(动作 + "目标不会发生任何变化"合并后两段)。
func TestHarmNoteClasses(t *testing.T) {
	cases := []struct {
		level     rings.ActionLevel
		args      string
		sentences int
	}{
		{rings.ActionModifyPersist, `{"command":"echo x > /var/www/html/shell.php"}`, 3},
		{rings.ActionModifyPersist, `{"command":"rm -rf /tmp/xx"}`, 3},
		{rings.ActionExploit, `{"command":"cat /etc/shadow"}`, 3},
		{rings.ActionActiveTest, `{"command":"sqlmap -u http://t.local:8080/?id=1 --batch"}`, 3},
		{rings.ActionReadOnly, `{"command":"cat /etc/hosts"}`, 2},
	}
	for _, c := range cases {
		got := HarmNote(c.level, "bash", json.RawMessage(c.args))
		if got == "" {
			t.Fatalf("分级 %s 应有危害说明", c.level)
		}
		if n := countSentences(got); n != c.sentences {
			t.Fatalf("分级 %s 句数不符(要 %d 实 %d): %q", c.level, c.sentences, n, got)
		}
	}
}

// countSentences 句号计数(中文句号结尾计一句)。
func countSentences(s string) int {
	n := 0
	for _, r := range s {
		if r == '。' {
			n++
		}
	}
	return n
}

// TestHarmNoteRegisterDetection 注册类判定的边界:无载荷提交不算注册
// 提交;注册词无 URL 时退通用写入句式。
func TestHarmNoteRegisterDetection(t *testing.T) {
	// GET 拉注册页(无提交载荷):不是注册提交,走通用写入(L4)。
	got := HarmNote(rings.ActionModifyPersist, "bash", json.RawMessage(`{"command":"curl http://t.local:9902/register"}`))
	if want := "将向 t.local:9902 提交注册表单创建账号"; got == want {
		t.Fatalf("无载荷提交不应判注册提交: %q", got)
	}
	// 提交载荷但目标无注册词:通用写入句式。
	got = HarmNote(rings.ActionModifyPersist, "bash", json.RawMessage(`{"command":"curl -X POST -d 'a=b' http://t.local:9902/api/x"}`))
	if strings.Contains(got, "提交注册表单创建账号") {
		t.Fatalf("无注册词不应判注册提交: %q", got)
	}
}
