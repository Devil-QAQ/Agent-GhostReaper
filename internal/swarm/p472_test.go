// ---- P4-72(问题清单 #88 B 方案,实测修正批次第一阶):目标识别归模型
// (快路径＋语法守门＋提取定性合一) ----
// 三条道顺序固定:①语法守门(裸数字/超 255 数字段/无点无字母串不是目
// 标,同源单函数挂提取出口/卡创建/资产入图三处);②快路径(全部地址
// 形态 token 被已知位置完整解释才直判,任何 token 未解释不猜测交模
// 型);③模型提取＋定性合一(一次调用出"目标清单＋逐目标判定",五值
// 词表)。静默漏拦根除:未知写法藏范围外真目标由模型读到、出卡拦
// 截;枚举旗标表封存不再扩。
package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// p472Stub 自定清单打桩:回固定目标清单,计数供调用断言。
func p472Stub(targets []TriageTarget, calls *int) TriageFunc {
	return func(_ context.Context, _ TriageInput) (TriageReply, error) {
		if calls != nil {
			*calls++
		}
		return TriageReply{Targets: targets}, nil
	}
}

// p472Cmd bash 命令载荷。
func p472Cmd(t *testing.T, cmd string) json.RawMessage {
	t.Helper()
	return json.RawMessage(mustJSON(t, cmd))
}

// p472TriageNotes 该任务全部 tg- 研判留痕 Note(合一清单断言用)。
func p472TriageNotes(t *testing.T, store *rings.Store, task string) []string {
	t.Helper()
	var out []string
	for _, r := range approvalEvents(t, store, task) {
		if r.Type == rings.ApprovalRecTriage {
			out = append(out, r.Note)
		}
	}
	return out
}

// TestP472AddressLikeHost 语法守门类级(完成标准②形态校验部分):裸数
// 字、超 255 数字段、全数字段非 IPv4、无点串、非法域名形态不是地址;
// 合法 IPv4/IPv6/含点域名是地址。
func TestP472AddressLikeHost(t *testing.T) {
	bad := []string{
		"400", "600", "8080", // 裸数字(nmap --min-rate 400 实锤形态)
		"400.0.0.1", "300.1.1.1", "999.999.999.999", // 超 255 数字段
		"1.2.3.4.5", "12.34", // 全数字段非 IPv4/段数不对
		"localhost", "intranet-portal", "x", // 无点串(无点无字母串一律不是目标)
		"my_host.com", "-bad-.com", "", // 非法域名形态
	}
	for _, s := range bad {
		if addressLikeHost(s) {
			t.Errorf("%q 不应是地址形态", s)
		}
	}
	good := []string{
		"1.2.3.4", "222.244.103.8", "10.0.0.9", // 合法 IPv4
		"::1", "2001:db8::1", "fe80::1", // 合法 IPv6
		"example.com", "ef854.iujw.cn", "a-b.co.uk", // 含点域名
		"EXAMPLE.COM", "Gitee.COM", // 大写归一
	}
	for _, s := range good {
		if !addressLikeHost(s) {
			t.Errorf("%q 应是地址形态", s)
		}
	}
}

// TestP472SyntaxGateFiltersNumericFlags 误拦根除类(完成标准②):数值
// 旗标多形态——语法守门滤除零卡零模型调用。--min-rate 400(#88 实锤)
// 、--top-ports 1000、--retry 999、curl --max-time 30 等数值参数不是
// 目标;范围内全解命令零调用直判(完成标准③范围内部)。
func TestP472SyntaxGateFiltersNumericFlags(t *testing.T) {
	store, _, env, pack := p450Env(t)
	_ = store
	called := 0
	env.triage = p472Stub(nil, &called)
	cmds := []string{
		// #88 实锤形态:--min-rate 400(表外旗标的数值现在由守门根除,
		// 不再依赖旗标表)。
		"nmap -Pn -n --top-ports 1000 --min-rate 400 ef854.iujw.cn",
		// 表外未知旗标 + 数值。
		"nmap --unknown-flag 400 --another 1.2.3.4.5 ef854.iujw.cn",
		"curl --max-time 30 --retry 999 http://ef854.iujw.cn/",
		"masscan --rate 400 -p 80 ef854.iujw.cn",
	}
	for _, c := range cmds {
		if d := env.denyScope(pack, "bash", p472Cmd(t, c)); d != "" {
			t.Fatalf("%q: 数值旗标应零卡放行,得: %q", c, d)
		}
	}
	if called != 0 {
		t.Fatalf("范围内全解命令应零模型调用,得 %d 次", called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("零范围卡,得 %d 张", len(cards))
	}
	if n := p467TriageRecords(t, store, env.TaskID); n != 0 {
		t.Fatalf("零判定留痕,得 %d 笔", n)
	}
	// 旧引擎对照:--min-rate 400 形态在 P4-50 提取面下 400 被误当位置
	// 参数、语法守门后彻底不是目标。
	if ts := scopeCallTargets("bash", p472Cmd(t, "nmap --min-rate 400 ef854.iujw.cn")); len(ts) != 1 || ts[0].host != "ef854.iujw.cn" {
		t.Fatalf("提取面应只剩种子目标,得 %+v", ts)
	}
}

// TestP472UAAndHeaderValuesCovered UA 版本号/请求头值(#73 形态回归):
// 值位置整体入覆盖集——零卡零模型调用。
func TestP472UAAndHeaderValuesCovered(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub(nil, &called)
	cmds := []string{
		`curl -A "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" http://ef854.iujw.cn/`,
		`curl -H "X-Forwarded-For: 203.0.113.7" -H "X-Real-IP: 198.51.100.9" http://ef854.iujw.cn/`,
		`curl -H "Referer: https://servicewechat.com/x" http://ef854.iujw.cn/`,
		`curl --resolve ef854.iujw.cn:443:127.0.0.1 https://ef854.iujw.cn/`,
	}
	for _, c := range cmds {
		if d := env.denyScope(pack, "bash", p472Cmd(t, c)); d != "" {
			t.Fatalf("UA/头值/对照侧应零卡放行: %q 得: %q", c, d)
		}
	}
	if called != 0 {
		t.Fatalf("值位置覆盖后应零模型调用,得 %d 次", called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("零范围卡,得 %d 张", len(cards))
	}
}

// TestP472FastPathKnownPositions 快路径类(完成标准③):curl 标准 URL/
// nmap 标准位置/Host 头/dev/tcp/openssl——已知位置完整解释,范围内零
// 模型调用直判;范围外已知目标(无缓存)走合一调用(一次)后入缓存,
// 第二次同 host 零调用。
func TestP472FastPathKnownPositions(t *testing.T) {
	_, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "ef854.iujw.cn", Verdict: TriageInScope, Basis: "种子范围内"},
	}, &called)
	inScope := []string{
		"curl -s http://ef854.iujw.cn/api/v1/user",
		"nmap -p 1-100 ef854.iujw.cn",
		`curl -H "Host: ef854.iujw.cn" http://220.167.100.103/`,
		"timeout 5 bash -c 'cat < /dev/tcp/ef854.iujw.cn/80'",
		"openssl s_client -connect ef854.iujw.cn:443 -servername ef854.iujw.cn",
		"nc -zv ef854.iujw.cn 80",
	}
	for _, c := range inScope {
		if d := env.denyScope(pack, "bash", p472Cmd(t, c)); d != "" {
			t.Fatalf("范围内已知位置应直判放行: %q 得: %q", c, d)
		}
	}
	if called != 0 {
		t.Fatalf("范围内全解命令零模型调用,得 %d 次", called)
	}
}

// TestP472SilentLeakUnknownFlagHost 静默漏拦类(完成标准①形态A·核心增
// 量):未知工具未知旗标藏范围外真目标——快路径未解释 → 模型读到 →
// 出卡拦截;旧引擎形态对照(枚举提取面零发现、静默放行)。
func TestP472SilentLeakUnknownFlagHost(t *testing.T) {
	store, _, env, pack := p450Env(t)
	const hidden = "evil.attacker.net"
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: hidden, Verdict: TriageCandidate, Basis: "命令实际连接该地址"},
	}, &called)
	cmd := "mycustomtool --mode fast --endpoint " + hidden + " --debug"
	// 旧引擎对照:枚举提取面零发现(不认识的工具静默跳过——这正是
	// 静默漏拦的病根)。
	if ts := bashAddrScanOf(cmd).targets; len(ts) != 0 {
		t.Fatalf("未知工具零目标位提取(对照),得 %+v", ts)
	}
	d := env.denyScope(pack, "bash", p472Cmd(t, cmd))
	if d == "" || !strings.Contains(d, hidden) {
		t.Fatalf("未知写法藏真目标应出卡拦截,得: %q", d)
	}
	if called != 1 {
		t.Fatalf("应恰一次合一调用,得 %d 次", called)
	}
	cards := p451ScopeWaitCards(t, store, env.TaskID)
	if len(cards) != 1 || !strings.Contains(cards[0].RequestID, hidden) {
		t.Fatalf("应恰 1 张 %s 范围卡,得 %+v", hidden, cards)
	}
}

// TestP472SilentLeakVariableConcat 静默漏拦类(完成标准①形态B):变量拼
// 接含完整域名——目标 URL 是 $ 变量(提取不出),赋值处的完整域名由
// 未解释集交给模型,判候选出卡。
func TestP472SilentLeakVariableConcat(t *testing.T) {
	_, _, env, pack := p450Env(t)
	const hidden = "db.internal.corp.cn"
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: hidden, Verdict: TriageCandidate, Basis: "变量赋值指向该地址,实际连接"},
	}, &called)
	cmd := "TARGET_DB=" + hidden + ":8080; curl -s http://$TARGET_DB/api"
	d := env.denyScope(pack, "bash", p472Cmd(t, cmd))
	if d == "" || !strings.Contains(d, hidden) {
		t.Fatalf("变量拼接藏真目标应出卡拦截,得: %q", d)
	}
	if called != 1 {
		t.Fatalf("应恰一次合一调用,得 %d 次", called)
	}
	if env.scopeHold == nil || env.scopeHold.host != hidden {
		t.Fatalf("应冻结在真目标上: %+v", env.scopeHold)
	}
}

// TestP472UnifiedCallSinglePerCommand 合一调用类(完成标准④):模糊命
// 令一次调用出"目标清单＋逐目标判定"——留痕可证(targets= 清单携带
// 全部判定目标);example.com 连通性判信息源获取零卡;同根域"连通测
// 试"判候选(校准断言——提示词正反例逐字在)。
func TestP472UnifiedCallSinglePerCommand(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "ef854.iujw.cn", Verdict: TriageInScope, Basis: "种子范围内"},
		{Host: "unknown-peer.net", Verdict: TriageCandidate, Basis: "归属不明确"},
	}, &called)
	// 模糊命令:已知目标(范围内)+ 未知写法藏的第二个目标。
	cmd := "curl -s http://ef854.iujw.cn/ && mytool --peer unknown-peer.net"
	d := env.denyScope(pack, "bash", p472Cmd(t, cmd))
	if d == "" || !strings.Contains(d, "unknown-peer.net") {
		t.Fatalf("候选目标应出卡,得: %q", d)
	}
	if called != 1 {
		t.Fatalf("整条命令应恰一次合一调用,得 %d 次", called)
	}
	// 留痕可证:目标清单(targets=)＋逐目标判定与依据。
	notes := p472TriageNotes(t, store, env.TaskID)
	if len(notes) == 0 {
		t.Fatalf("合一判定应留痕")
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "targets=") && strings.Contains(n, "unknown-peer.net") {
			found = true
		}
	}
	if !found {
		t.Fatalf("留痕应含目标清单摘要,得 %v", notes)
	}

	// example.com 连通性判信息源获取:零卡入清单(校准正例)。
	env2 := &unitEnv{Executor: env.Executor, TaskID: env.TaskID, StepID: env.StepID, UnitID: "u2", TranscriptID: "swarm-u2", scopeResolve: env.scopeResolve}
	called2 := 0
	env2.triage = p472Stub([]TriageTarget{
		{Host: "example.com", Verdict: TriageInfoAccess, Basis: "示例保留域文档对照站,与目标无归属"},
	}, &called2)
	if d := env2.denyScope(pack, "bash", p467Target(t, "example.com")); d != "" {
		t.Fatalf("example.com 连通性判信息源获取应零卡,得: %q", d)
	}
	roe, err := store.CurrentRoE(context.Background(), env.TaskID)
	if err != nil || !roe.InfoSourceHit("example.com") {
		t.Fatalf("example.com 应入信息源清单: %+v err=%v", roe.InfoSources, err)
	}

	// 同根域"连通测试"判候选(校准反例——归属优先于动作表述):提示词
	// 正反例逐字断言。
	for _, anchor := range []string{"同根域", "连通性测距", "example.com"} {
		if !strings.Contains(triagePrompt, anchor) {
			t.Fatalf("提示词判据校准应含 %q", anchor)
		}
	}
	env3 := &unitEnv{Executor: env.Executor, TaskID: env.TaskID, StepID: env.StepID, UnitID: "u3", TranscriptID: "swarm-u3", scopeResolve: env.scopeResolve}
	called3 := 0
	env3.triage = p472Stub([]TriageTarget{
		{Host: "mail.ef854.iujw.cn", Verdict: TriageCandidate, Basis: "与目标同根域,归属优先于连通测试表述"},
	}, &called3)
	d3 := env3.denyScope(pack, "bash", p467Target(t, "mail.ef854.iujw.cn"))
	if d3 == "" || !strings.Contains(d3, "mail.ef854.iujw.cn") {
		t.Fatalf("同根域连通测试应判候选出卡,得: %q", d3)
	}
}

// TestP472NotTargetSilentFilter 非目标静默过滤(完成标准④/P4-07 口径):
// 模型判 not_target 的候选目标——不出卡、不冻结、留痕入账不算拦截
// (tg- triage 记录,非 denied)。
func TestP472NotTargetSilentFilter(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "rate-limit.example.net", Verdict: TriageNotTarget, Basis: "参数值,不是连接目标"},
	}, &called)
	cmd := "nmap --custom-limit rate-limit.example.net ef854.iujw.cn"
	if d := env.denyScope(pack, "bash", p472Cmd(t, cmd)); d != "" {
		t.Fatalf("非目标应静默过滤零卡,得: %q", d)
	}
	if env.park != nil || env.scopeHold != nil {
		t.Fatalf("非目标不得出卡或冻结")
	}
	notes := p472TriageNotes(t, store, env.TaskID)
	if len(notes) != 1 || !strings.Contains(notes[0], "not_target") {
		t.Fatalf("非目标过滤应恰一笔留痕可观测,得 %v", notes)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("非目标零范围卡,得 %d 张", len(cards))
	}
	// 第二次同命令:tg- 缓存命中,零调用零卡。
	before := *(&called)
	if d := env.denyScope(pack, "bash", p472Cmd(t, cmd)); d != "" {
		t.Fatalf("缓存命中应放行,得: %q", d)
	}
	if called != before {
		t.Fatalf("缓存命中零重复调用,得 %d 次", called)
	}
}

// TestP472CacheAfterUnified 合一结论入缓存:判过候选出卡后的同 host 第
// 二次访问零模型调用直接出卡(任务级缓存,同 P4-67 口径)。
func TestP472CacheAfterUnified(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p472Stub([]TriageTarget{
		{Host: "sibling.ef854.iujw.cn", Verdict: TriageCandidate, Basis: "同根域"},
	}, &called)
	cmd := p467Target(t, "sibling.ef854.iujw.cn")
	if d := env.denyScope(pack, "bash", cmd); d == "" {
		t.Fatalf("候选应出卡")
	}
	if d := env.denyScope(pack, "bash", cmd); d == "" || called != 1 {
		t.Fatalf("第二次同 host 应零调用按缓存出卡: %q calls=%d", d, called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 1 {
		t.Fatalf("同一目标一张卡,得 %d 张", len(cards))
	}
}

// TestP472UnsureFailsClosedCard 合一失败按拿不准出卡(失败方向钉死):
// 链报错时确定性待判目标按候选出卡,不放行。
func TestP472UnsureFailsClosedCard(t *testing.T) {
	store, _, env, pack := p450Env(t)
	env.triage = func(_ context.Context, _ TriageInput) (TriageReply, error) {
		return TriageReply{}, context.DeadlineExceeded
	}
	if d := env.denyScope(pack, "bash", p467Target(t, "strange-out.net")); d == "" {
		t.Fatalf("链报错应按拿不准出卡")
	}
	if env.scopeHold == nil || env.scopeHold.host != "strange-out.net" {
		t.Fatalf("应冻结在待判目标: %+v", env.scopeHold)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 1 {
		t.Fatalf("应恰 1 张范围卡,得 %d 张", len(cards))
	}
}

// TestP472ScopeClosureSummary 授权范围写法清单(合一 in_scope 对照输入):
// 域名/通配/IP/端口限定逐条列出、排序稳定;空闭包空串。
func TestP472ScopeClosureSummary(t *testing.T) {
	c := rings.BuildScopeClosure([]rings.ScopeSeed{
		{ID: "1", Title: "b.example.cn", Source: rings.AssetSourceSeed},
		{ID: "2", Title: "*.wild.cn", Source: rings.AssetSourceSeed},
		{ID: "3", Title: "10.0.0.9", Source: rings.AssetSourceSeed},
		{ID: "4", Title: "c.example.cn:8443", Source: rings.AssetSourceSeed},
	}, nil, nil)
	got := c.Summary()
	for _, want := range []string{"b.example.cn", "*.wild.cn", "10.0.0.9", "c.example.cn:8443"} {
		if !strings.Contains(got, want) {
			t.Fatalf("范围清单应含 %q,得 %q", want, got)
		}
	}
	if rings.BuildScopeClosure(nil, nil, nil).Summary() != "" {
		t.Fatalf("空闭包清单应为空串")
	}
}
