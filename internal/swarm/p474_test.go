// ---- P4-74(问题清单 #90):审批卡面与记录可读性 ----
// ① 卡面以命令为准:危害说明与徽标按命令实际内容拼装,步骤分级降为
// 背景行;③ 卡文案只陈述事实:"X 是信息源"句式仅信息源判定时出现,
// 未判定不宣称"主动攻击"。② 空参数与 ④ 折叠在 server/web 侧测试。
package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP474ClassifyCommandAction 命令实际内容分级类级(完成标准①的判级
// 基座,规则 15 多形态):写入族(删除/重定向/POST 提交/sed -i/建目录)、
// 数据接入族、主动测试族、只读族(本地查看/网络读取/混合只读)、只读
// 工具按名判 L1;认不出(未知动词/无命令/非 bash 未知工具)回退。
func TestP474ClassifyCommandAction(t *testing.T) {
	cases := []struct {
		tool string
		args string
		want rings.ActionLevel
		ok   bool
		note string
	}{
		// 写入族(L4)多形态。
		{"bash", `{"command":"rm -rf /tmp/xx"}`, rings.ActionModifyPersist, true, "删除"},
		{"bash", `{"command":"mkdir -p /var/www/uploads"}`, rings.ActionModifyPersist, true, "建目录"},
		{"bash", `{"command":"echo x > /var/www/html/shell.php"}`, rings.ActionModifyPersist, true, "重定向写"},
		{"bash", `{"command":"curl -X POST -d 'a=b' http://t.local:9902/api"}`, rings.ActionModifyPersist, true, "POST 提交"},
		{"bash", `{"command":"wget --post-data 'a=b' http://t.local:9902/api"}`, rings.ActionModifyPersist, true, "wget post-data"},
		{"bash", `{"command":"sed -i 's/a/b/' app.conf"}`, rings.ActionModifyPersist, true, "sed 原地改"},
		{"bash", `{"command":"chmod 777 /tmp/x"}`, rings.ActionModifyPersist, true, "改权限"},
		{"bash", `{"command":"useradd gr01"}`, rings.ActionModifyPersist, true, "建账号"},
		// 数据接入族(L3)。
		{"bash", `{"command":"mysql -h t.local -u root -e 'select 1'"}`, rings.ActionExploit, true, "mysql 接入"},
		{"bash", `{"command":"ssh root@t.local 'id'"}`, rings.ActionExploit, true, "ssh 接入"},
		{"bash", `{"command":"redis-cli -h t.local keys '*'"}`, rings.ActionExploit, true, "redis 接入"},
		// 主动测试族(L2)。
		{"bash", `{"command":"nmap -p 1-100 t.local"}`, rings.ActionActiveTest, true, "端口扫描"},
		{"bash", `{"command":"hydra -l admin -P pass.txt t.local ssh"}`, rings.ActionActiveTest, true, "爆破"},
		{"bash", `{"command":"gobuster dir -u http://t.local/ -w wordlist"}`, rings.ActionActiveTest, true, "目录枚举"},
		// 只读族(L1)多形态:#90 实锤 pwd; ls -la、本地查看、网络读取、混合。
		{"bash", `{"command":"pwd; ls -la"}`, rings.ActionReadOnly, true, "#90 实锤形态"},
		{"bash", `{"command":"cat /etc/hosts && whoami"}`, rings.ActionReadOnly, true, "本地查看"},
		{"bash", `{"command":"curl -s http://t.local:9902/page.html"}`, rings.ActionReadOnly, true, "GET 拉取"},
		{"bash", `{"command":"ping -c 2 t.local"}`, rings.ActionReadOnly, true, "连通测距"},
		{"bash", `{"command":"dig +short t.local"}`, rings.ActionReadOnly, true, "DNS 查询"},
		{"bash", `{"command":"curl -s http://t.local/ -o /dev/null && cat resp.txt"}`, rings.ActionReadOnly, true, "混合只读"},
		{"bash", `{"command":"curl -s http://t.local/ 2>/dev/null"}`, rings.ActionReadOnly, true, "stderr 丢弃非写入"},
		{"bash", `{"command":"sed 's/a/b/' old.conf"}`, rings.ActionReadOnly, true, "sed 流变换非原地"},
		// 混段按最高危段:pwd 夹 rm 按写入说。
		{"bash", `{"command":"pwd; ls; rm x.tmp"}`, rings.ActionModifyPersist, true, "混段最高危"},
		// 只读工具按名判 L1。
		{"port_probe", `{"host":"t.local","port":443}`, rings.ActionReadOnly, true, "端口探测工具"},
		{"http_probe", `{"url":"http://t.local/"}`, rings.ActionReadOnly, true, "HTTP 探测工具"},
		{"web_fetch", `{"url":"https://docs.example.org/x"}`, rings.ActionReadOnly, true, "网页拉取工具"},
		// 认不出:未知动词/无命令/未知非 bash 工具——回退调用方。
		{"bash", `{"command":"mycustomtool --x 1"}`, "", false, "未知动词"},
		{"bash", `{}`, "", false, "无命令"},
		{"bash", `{"command":""}`, "", false, "空命令"},
		{"mytool", `{"a":1}`, "", false, "未知非 bash 工具"},
	}
	for _, c := range cases {
		got, ok := ClassifyCommandAction(c.tool, json.RawMessage(c.args))
		if ok != c.ok || got != c.want {
			t.Errorf("%s(%s · %s): 得 (%q,%v), 要 (%q,%v)",
				c.note, c.tool, c.args, got, ok, c.want, c.ok)
		}
	}
}

// TestP474CardFaceByCommand 卡面以命令为准(完成标准①):只读命令在
// L4(修改与持久化)步骤中出卡——危害说明为命令实际危害(查看类锚例
// 逐字),分级降为背景行;写入命令卡面为写入、无背景行;认不出的命令
// 回退步骤分级、无背景行。
func TestP474CardFaceByCommand(t *testing.T) {
	// #90 实锤形态:pwd; ls -la 顶着"修改与持久化"的脸——现在按命令
	// 说查看(锚例句式),步骤分级降为背景行。
	roArgs := json.RawMessage(`{"command":"pwd; ls -la"}`)
	got := HarmNote(rings.ActionModifyPersist, "bash", roArgs)
	if want := "将执行 pwd 与 ls 查看目录和文件列表。目标不会发生任何变化。"; got != want {
		t.Fatalf("只读命令在 L4 步骤应为查看类锚例:\n得 %q\n要 %q", got, want)
	}
	ground := StepGroundNote(rings.ActionModifyPersist, "bash", roArgs)
	if ground != "所属步骤含修改类动作，按步骤门槛过门" {
		t.Fatalf("步骤分级背景行不符: %q", ground)
	}

	// 网络读取命令在 L4 步骤:拉取响应、目标无变化,背景行同款。
	netArgs := json.RawMessage(`{"command":"curl -s http://t.local:9902/api/v1/user"}`)
	got = HarmNote(rings.ActionModifyPersist, "bash", netArgs)
	if !strings.Contains(got, "目标不会发生任何变化") || !strings.Contains(got, "curl") {
		t.Fatalf("网络读取命令应为只读卡面: %q", got)
	}
	if ground = StepGroundNote(rings.ActionModifyPersist, "bash", netArgs); ground == "" {
		t.Fatalf("L4 步骤中的只读命令应有背景行")
	}

	// 写入命令在 L4 步骤:卡面为写入(注册锚例),无背景行(命令与步骤
	// 同级,无落差)。
	wArgs := json.RawMessage(`{"command":"curl -X POST -d 'u=a&p=b' http://jkysd.cn:9902/register"}`)
	got = HarmNote(rings.ActionModifyPersist, "bash", wArgs)
	if want := "将向 jkysd.cn:9902 提交注册表单创建账号。目标会新增一个可登录账号。账号会留存于目标系统，管理员可能察觉异常注册。"; got != want {
		t.Fatalf("写入命令卡面应为注册锚例:\n得 %q\n要 %q", got, want)
	}
	if ground = StepGroundNote(rings.ActionModifyPersist, "bash", wArgs); ground != "" {
		t.Fatalf("命令与步骤同级无背景行: %q", ground)
	}

	// 主动测试命令在 L4 步骤:卡面为主动测试,背景行说明按步骤门槛。
	tArgs := json.RawMessage(`{"command":"nmap -p 1-100 t.local"}`)
	got = HarmNote(rings.ActionModifyPersist, "bash", tArgs)
	if !strings.Contains(got, "发送测试请求") || !strings.Contains(got, "触发目标的安全告警") {
		t.Fatalf("主动测试命令卡面应为测试句式: %q", got)
	}
	if ground = StepGroundNote(rings.ActionModifyPersist, "bash", tArgs); ground != "所属步骤含修改类动作，按步骤门槛过门" {
		t.Fatalf("L4 步骤中的测试命令背景行不符: %q", ground)
	}

	// L3/L2 步骤的只读命令:背景行按步骤级别措辞。
	if ground = StepGroundNote(rings.ActionExploit, "bash", roArgs); ground != "所属步骤含利用类动作，按步骤门槛过门" {
		t.Fatalf("L3 背景行不符: %q", ground)
	}
	if ground = StepGroundNote(rings.ActionActiveTest, "bash", roArgs); ground != "所属步骤含测试类动作，按步骤门槛过门" {
		t.Fatalf("L2 背景行不符: %q", ground)
	}

	// 认不出的命令:回退步骤分级(既有审计字段),无背景行。
	unkArgs := json.RawMessage(`{"command":"mycustomtool --x 1"}`)
	got = HarmNote(rings.ActionModifyPersist, "bash", unkArgs)
	if !strings.Contains(got, "修改目标的数据或配置") {
		t.Fatalf("认不出的命令回退步骤分级 L4 句式: %q", got)
	}
	if ground = StepGroundNote(rings.ActionModifyPersist, "bash", unkArgs); ground != "" {
		t.Fatalf("认不出无背景行: %q", ground)
	}

	// 只读工具的卡面:port_probe/http_probe 按只读说,背景行随步骤。
	if got = HarmNote(rings.ActionModifyPersist, "port_probe", json.RawMessage(`{"host":"t.local","port":443}`)); !strings.Contains(got, "目标不会发生任何变化") {
		t.Fatalf("只读探测工具应为只读卡面: %q", got)
	}
	if ground = StepGroundNote(rings.ActionModifyPersist, "port_probe", json.RawMessage(`{"host":"t.local","port":443}`)); ground == "" {
		t.Fatalf("L4 步骤中的只读工具命令应有背景行")
	}
}

// p474AttackNotes 该任务全部审批记录的 Note(句式断言用)。
func p474AttackNotes(t *testing.T, store *rings.Store, task string) []string {
	t.Helper()
	var out []string
	for _, r := range approvalEvents(t, store, task) {
		out = append(out, r.Note)
	}
	return out
}

// TestP474AttackCardWordingJudged 攻击卡句式·已判定形态(完成标准③):
// 模型判 info_attack——"X 是信息源"句式出现(判定在先),动作句带依据。
func TestP474AttackCardWordingJudged(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "gitee.com", Verdict: TriageInfoAttack, Basis: "对公开服务发起端口扫描"},
	}, &called)
	d := env.denyScope(pack, "bash", p467Target(t, "gitee.com"))
	if d == "" || !strings.Contains(d, "已提交面板待决") {
		t.Fatalf("info_attack 应出攻击行为卡: %q", d)
	}
	if env.park == nil || env.park.Req.AttackTarget == "" {
		t.Fatalf("应挂在攻击行为卡票上")
	}
	note := env.park.Req.Note
	if !strings.Contains(note, "是信息源(查资料用的公开服务,不是测试目标)") {
		t.Fatalf("已判定信息源攻击应含\"是信息源\"句式: %q", note)
	}
	if !strings.Contains(note, "本次动作是主动攻击(分诊依据:对公开服务发起端口扫描)") {
		t.Fatalf("动作句应带判定依据: %q", note)
	}
	_ = store
}

// TestP474AttackCardWordingUnjudged 攻击卡句式·拿不准形态(完成标准③):
// 红队清单命中、动作复查失败——目标在清单(判定在先,"是信息源"如实),
// 但动作性质未判定:不写"本次动作是主动攻击"。
func TestP474AttackCardWordingUnjudged(t *testing.T) {
	store, _, env, pack := p450Env(t)
	rpack := p470RedTeamPack(pack)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	env.triage = func(_ context.Context, _ TriageInput) (TriageReply, error) {
		return TriageReply{}, context.DeadlineExceeded
	}
	d := env.denyScope(rpack, "bash", p470ScanCmd(t, "nmap -p 443 gitee.com"))
	if d == "" || !strings.Contains(d, "已提交面板待决") {
		t.Fatalf("拿不准应按攻击卡提交人审: %q", d)
	}
	if env.park == nil || env.park.Req.AttackTarget == "" {
		t.Fatalf("应挂在攻击行为卡票上")
	}
	note := env.park.Req.Note
	if !strings.Contains(note, "本次动作的性质未判定") {
		t.Fatalf("未判定形态应如实说未判定: %q", note)
	}
	if strings.Contains(note, "本次动作是主动攻击") {
		t.Fatalf("未判定不得宣称\"本次动作是主动攻击\": %q", note)
	}
	// 目标在信息源清单(判定在先):"是信息源"句式保留(有判定支撑)。
	if !strings.Contains(note, "是信息源") {
		t.Fatalf("清单命中的目标如实呈现信息源身份: %q", note)
	}
}

// TestP474NoInfoSourcePhraseForNonSource 非信息源目标无"是信息源"句式
// (完成标准③断言):候选/非目标/范围内结论走范围卡或静默过滤——账本
// 全部记录的 Note 零"是信息源"句式(该句式仅信息源判定时出现)。
func TestP474NoInfoSourcePhraseForNonSource(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "peer.example.net", Verdict: TriageCandidate, Basis: "归属不明确"},
	}, &called)
	// 候选:出范围卡(sc-),非攻击卡。
	if d := env.denyScope(pack, "bash", p467Target(t, "peer.example.net")); d == "" {
		t.Fatalf("候选目标应出范围卡")
	}
	// 非目标:静默过滤。
	env2 := &unitEnv{Executor: env.Executor, TaskID: env.TaskID, StepID: env.StepID, UnitID: "u2", TranscriptID: "swarm-u2", scopeResolve: env.scopeResolve}
	called2 := 0
	env2.triage = p472Stub([]TriageTarget{
		{Host: "rate.example.net", Verdict: TriageNotTarget, Basis: "参数值"},
	}, &called2)
	if d := env2.denyScope(pack, "bash", json.RawMessage(mustJSON(t, "nmap --limit rate.example.net ef854.iujw.cn"))); d != "" {
		t.Fatalf("非目标应静默过滤: %q", d)
	}
	for _, note := range p474AttackNotes(t, store, env.TaskID) {
		if strings.Contains(note, "是信息源") {
			t.Fatalf("非信息源目标的留痕不得出现\"是信息源\"句式: %q", note)
		}
	}
}
