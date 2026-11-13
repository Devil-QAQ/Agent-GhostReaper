package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P4-67(问题清单 #86 A②＋E＋F,RoE 机制重构批次第二层):研判分诊
// 与信息源清单 ----
// 范围外命中(经 P4-66 豁免后仍命中)在出卡前先研判——单次轻量模型
// 调用(打桩注入保确定性),三类判定:方法性访问→自动授访问权零出卡
// 入清单;信息源上的攻击行为→攻击行为卡(批准=放行该次动作、拒绝=只
// 杀动作);相关资产候选/拿不准→现行范围卡(偏向出卡)。任务级缓存
// (tg- 研判留痕+信息源清单)同任务内零重复调用;撤销恢复拦截。
// 环境复用 p450Env(渗透任务,种子 ef854.iujw.cn,DNS 打桩)。

// p467Triage 打桩分诊(P4-72 合一契约形态):verdicts 按 host 前缀匹配
// (合一调用不带 Host 时按 Candidates 逐个对;无匹配返回候选——拿不
// 准方向),calls 计数供缓存断言;回复按锚点 host 具名单目标。
func p467Triage(verdicts map[string]string, calls *int) TriageFunc {
	match := func(s string) (string, string, bool) {
		for prefix, v := range verdicts {
			if strings.HasPrefix(s, prefix) {
				return v, "打桩依据:" + prefix + " 属" + v, true
			}
		}
		return "", "", false
	}
	return func(_ context.Context, in TriageInput) (TriageReply, error) {
		if calls != nil {
			*calls++
		}
		if v, basis, ok := match(in.Host); ok {
			return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
		}
		// 合一调用(命令路):候选逐个对,首个命中即回该判定。
		for _, c := range in.Candidates {
			if v, basis, ok := match(c); ok {
				return TriageReply{Targets: []TriageTarget{{Host: c, Verdict: v, Basis: basis}}}, nil
			}
		}
		host := in.Host
		if len(in.Candidates) > 0 {
			host = in.Candidates[0]
		}
		return TriageReply{Targets: []TriageTarget{
			{Host: host, Verdict: TriageCandidate, Basis: "打桩依据:归属不明确,偏向出卡"},
		}}, nil
	}
}

// p467Target 范围外目标的探测命令(bash)。
func p467Target(t *testing.T, host string) json.RawMessage {
	t.Helper()
	return json.RawMessage(mustJSON(t, "curl -s --max-time 5 http://"+host+"/"))
}

// TestP467MethodicalAccessAutoGrant 完成标准①:方法性访问多形态(代码
// 托管类/搜索引擎类/公共文档类各一)自动放行零出卡、host 入信息源
// 清单、零资产入图。
func TestP467MethodicalAccessAutoGrant(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(map[string]string{
		"gitee.com":       TriageMethodical, // 代码托管类
		"www.bing.com":    TriageMethodical, // 搜索引擎类
		"docs.python.org": TriageMethodical, // 公共文档类
	}, &called)
	for _, host := range []string{"gitee.com", "www.bing.com", "docs.python.org"} {
		env.scopeHold = nil
		if reason := env.denyScope(pack, "bash", p467Target(t, host)); reason != "" {
			t.Errorf("%s: 方法性访问应自动放行,得拦截: %s", host, reason)
		}
		if env.scopeHold != nil {
			t.Errorf("%s: 方法性访问不得置位冻结", host)
		}
	}
	if called != 3 {
		t.Fatalf("三个新 host 应各分诊一次,得 %d 次", called)
	}
	// 信息源清单:三 host 在册,带依据摘要。
	roe, err := store.CurrentRoE(context.Background(), env.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roe.InfoSources) != 3 {
		t.Fatalf("信息源清单应有 3 条,得 %+v", roe.InfoSources)
	}
	for _, src := range roe.InfoSources {
		if src.Basis == "" {
			t.Errorf("清单条目 %s 缺研判依据摘要", src.Host)
		}
	}
	// 零资产入图:资产树仍只有种子。
	assets, _ := store.NodesByType(context.Background(), env.TaskID, rings.NodeAsset, "")
	if len(assets) != 1 || assets[0].Title != "ef854.iujw.cn" {
		t.Fatalf("方法性访问零资产入图,得 %d 个资产: %+v", len(assets), assets)
	}
	// 零范围卡零拒绝。
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("方法性访问应零范围卡,得 %d 张", len(cards))
	}
	if recs := p444DenyRecords(t, store, env.TaskID); len(recs) != 0 {
		t.Fatalf("方法性访问应零拒绝,得 %d 笔", len(recs))
	}
	// 分诊留痕:三条 triage 记录(结论与依据入账可审计)。
	if n := p467TriageRecords(t, store, env.TaskID); n != 3 {
		t.Fatalf("应恰 3 笔研判留痕,得 %d", n)
	}
}

// TestP467CandidateCards 完成标准①:相关资产候选(同根域兄弟子域/同
// IP 邻居各一)与归属模糊样本出卡(现行范围卡机制,拿不准偏向出卡)。
func TestP467CandidateCards(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(map[string]string{
		"mail.ef854.iujw.cn":  TriageCandidate, // 同根域兄弟子域(归属线索)
		"220.167.100.200":     TriageCandidate, // 同 IP 段邻居(归属线索)
		"unknown-service.net": TriageCandidate, // 归属模糊(拿不准偏向)
	}, &called)
	for _, host := range []string{"mail.ef854.iujw.cn", "220.167.100.200", "unknown-service.net"} {
		env.scopeHold = nil
		reason := env.denyScope(pack, "bash", p467Target(t, host))
		if reason == "" {
			t.Errorf("%s: 候选目标应出卡拦下,得放行", host)
			continue
		}
		if env.scopeHold == nil || env.scopeHold.host != host {
			t.Errorf("%s: 应置位范围冻结(等待批准),得 %+v", host, env.scopeHold)
		}
	}
	if called != 3 {
		t.Fatalf("三个候选应各分诊一次,得 %d 次", called)
	}
	cards := p451ScopeWaitCards(t, store, env.TaskID)
	if len(cards) != 3 {
		t.Fatalf("候选目标应各出一张范围卡,得 %d 张: %+v", len(cards), cards)
	}
	// 候选不入信息源清单(概念正交:候选≠信息源)。
	roe, _ := store.CurrentRoE(context.Background(), env.TaskID)
	if len(roe.InfoSources) != 0 {
		t.Fatalf("候选目标不得入信息源清单,得 %+v", roe.InfoSources)
	}
}

// TestP467AttackCard 完成标准②(判定层):对信息源的扫描/攻击动作出
// 攻击行为卡(attack_wait),不出范围卡、不冻结收场(挂起等裁决)。
func TestP467AttackCard(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(map[string]string{
		"gitee.com": TriageAttack, // 同一信息源,这次是主动攻击
	}, &called)
	scan := json.RawMessage(mustJSON(t, "nmap -p 1-1000 gitee.com"))
	reason := env.denyScope(pack, "bash", scan)
	if reason == "" || !strings.Contains(reason, "信息源") {
		t.Fatalf("攻击动作应出攻击行为卡提示,得: %q", reason)
	}
	if !strings.Contains(reason, "已提交面板待决") {
		t.Fatalf("应是挂起待决(不是冻结收场): %q", reason)
	}
	if env.scopeHold != nil {
		t.Fatalf("攻击行为卡是挂起等裁决,不得置位范围冻结(冻结收场是范围卡机制)")
	}
	if env.park == nil || env.park.Req.AttackTarget != "gitee.com:1" {
		t.Fatalf("应挂在攻击行为卡票上(AttackTarget=gitee.com),得 %+v", env.park)
	}
	if called != 1 {
		t.Fatalf("应恰一次分诊调用,得 %d", called)
	}
	// 账本:attack_wait 待决记录 + triage 留痕;零范围卡。
	recs := approvalEvents(t, store, env.TaskID)
	var attack, triage int
	for _, r := range recs {
		switch r.Type {
		case rings.ApprovalRecAttackWait:
			attack++
		case rings.ApprovalRecTriage:
			triage++
		}
	}
	if attack != 1 || triage != 1 {
		t.Fatalf("应恰 1 笔攻击待决 + 1 笔研判留痕,得 attack=%d triage=%d: %+v", attack, triage, recs)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("攻击行为卡不是范围卡,得 %d 张", len(cards))
	}
}

// TestP467CacheNoRepeatCall 完成标准③(P4-68 ① 复核后口径):host 归属
// 结论任务级缓存——tg- 研判留痕恰一笔(第二次访问零归类调用、零重复
// 留痕);P4-68 起 info_source 命中改为动作复查(访问权与攻击行为正交,
// 逐命令判动作性质——攻击照走攻击行为卡),故第二次访问有一次动作
// 分诊调用但不落 tg- 留痕;候选结论走 tg- 留痕缓存(同 host 第二次
// 不再调用,卡状态机接管)。
func TestP467CacheNoRepeatCall(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(map[string]string{
		"gitee.com":        TriageMethodical,
		"unknown-cand.net": TriageCandidate,
	}, &called)
	// 信息源缓存:第一次分诊入清单,第二次动作复查(零归类留痕)。
	env.denyScope(pack, "bash", p467Target(t, "gitee.com"))
	if called != 1 {
		t.Fatalf("首次 gitee.com 应分诊一次,得 %d 次", called)
	}
	env.scopeHold = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com:443")); reason != "" {
		t.Fatalf("同 host 不同端口应按信息源放行(清单 host 级),得: %q", reason)
	}
	if called != 2 {
		t.Fatalf("P4-68 动作复查逐命令一次(归类不重复):第二次访问应 2 次调用,得 %d 次", called)
	}
	if n := p467TriageRecords(t, store, env.TaskID); n != 1 {
		t.Fatalf("tg- 归类留痕应恰 1 笔(动作复查不落痕),得 %d 笔", n)
	}
	// 候选缓存:第一次分诊出卡,第二次读 tg- 留痕零调用(并入等待)。
	env.denyScope(pack, "bash", p467Target(t, "unknown-cand.net"))
	if called != 3 {
		t.Fatalf("首次 unknown-cand.net 应分诊,得 %d 次", called)
	}
	env.scopeHold = nil
	env.park = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "unknown-cand.net")); reason == "" {
		t.Fatalf("候选目标第二次访问应仍拦下(卡状态机),得放行")
	}
	if called != 3 {
		t.Fatalf("候选结论走任务级缓存,第二次应零分诊调用,得 %d 次", called)
	}
}

// TestP467RevokeRestoresInterception 完成标准④:撤销信息源后恢复拦截
// ——同 host 访问重新走分诊(revoked 留痕作废缓存)。
func TestP467RevokeRestoresInterception(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(map[string]string{"gitee.com": TriageMethodical}, &called)
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); reason != "" {
		t.Fatalf("首次应放行: %q", reason)
	}
	// 撤销(面板撤销端点的同一账本动作)。
	if err := RevokeInfoSource(context.Background(), store, env.TaskID, "gitee.com", "creator"); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	roe, _ := store.CurrentRoE(context.Background(), env.TaskID)
	if len(roe.InfoSources) != 0 {
		t.Fatalf("撤销后清单应空,得 %+v", roe.InfoSources)
	}
	// 撤销后同 host 访问恢复拦截:撤销是操作者推翻分类,粘性——不再重
	// 新分诊(零调用),按候选出卡,由范围卡两键一锤定音。
	env.scopeHold = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); reason == "" {
		t.Fatalf("撤销后应恢复拦截出卡,得放行")
	}
	if env.scopeHold == nil || env.scopeHold.host != "gitee.com" {
		t.Fatalf("撤销后应出范围卡(候选),得 %+v", env.scopeHold)
	}
	if called != 1 {
		t.Fatalf("撤销粘性:不得重新分诊,得 %d 次", called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 1 {
		t.Fatalf("撤销后应恰 1 张范围卡,得 %d 张", len(cards))
	}
	// 牧者/面板重新标记优先于撤销标记:入清单后访问恢复放行。
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "操作者重新标记", "creator"); err != nil {
		t.Fatal(err)
	}
	env.scopeHold = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); reason != "" {
		t.Fatalf("重新标记后应放行,得: %q", reason)
	}
}

// TestP467DeclaredSourceExempt 完成标准⑥:意图带外部服务声明,执行
// 命中声明的服务形态→分诊判 declared_source 免出卡直接放行(处置同
// 方法性访问);清单外新址照走分诊。
func TestP467DeclaredSourceExempt(t *testing.T) {
	store, _, env, pack := p450Env(t)
	pack.ExternalServices = "代码托管平台的公开仓库"
	called := 0
	env.triage = p467Triage(map[string]string{
		"gitee.com":        TriageDeclared,  // 命中声明(代码托管形态)
		"unknown-cand.net": TriageCandidate, // 清单外新址
	}, &called)
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); reason != "" {
		t.Fatalf("命中声明的服务形态应免分诊出卡放行,得: %q", reason)
	}
	roe, _ := store.CurrentRoE(context.Background(), env.TaskID)
	if len(roe.InfoSources) != 1 || roe.InfoSources[0].Host != "gitee.com" {
		t.Fatalf("声明命中应入信息源清单,得 %+v", roe.InfoSources)
	}
	// 清单外新址照走分诊(候选→出卡)。
	env.scopeHold = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "unknown-cand.net")); reason == "" {
		t.Fatalf("清单外新址应照走分诊(候选出卡),得放行")
	}
	if called != 2 {
		t.Fatalf("声明命中与清单外各一次分诊,得 %d 次", called)
	}
}

// TestP467RedTeamCommandPathNoTriage 完成标准⑦(命令路):红队任务
// 命令路无拦截、不调分诊——直接放行留痕照镜。
func TestP467RedTeamCommandPathNoTriage(t *testing.T) {
	store, _, env, pack := p450Env(t)
	called := 0
	env.triage = p467Triage(nil, &called)
	// 红队任务:闭包 nil → denyScope 首行即返回,轮不到分诊。
	pack.ScopeClosure = nil
	if reason := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); reason != "" {
		t.Fatalf("红队命令路应直接放行,得: %q", reason)
	}
	if called != 0 {
		t.Fatalf("红队命令路不得调分诊,得 %d 次", called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("红队命令路零卡,得 %d 张", len(cards))
	}
}

// TestP467TriagParse 分诊输出解析(P4-72 合一契约):targets 清单逐项
// 词表校验、围栏包裹、词表外拒绝、空依据兜底;旧单值形态兼容读(解
// 析失败按拿不准由调用方处理——错误路径断言)。
func TestP467TriagParse(t *testing.T) {
	cases := []struct {
		in    string
		v     string
		b     string
		fails bool
	}{
		{`{"targets":[{"host":"docs.python.org","verdict":"info_access","basis":"公共文档站,与目标无归属"},{"host":"400","verdict":"not_target","basis":"速率参数值"}]}`,
			TriageInfoAccess, "公共文档站,与目标无归属", false},
		{"```json\n{\"targets\":[{\"host\":\"gitee.com\",\"verdict\":\"info_attack\",\"basis\":\"扫描动作\"}]}\n```",
			TriageInfoAttack, "扫描动作", false},
		{`{"targets":[{"host":"mail.x.com","verdict":"related_candidate","basis":""}]}`,
			TriageCandidate, "未给出依据", false},
		// 旧单值形态兼容(过渡期打桩/罐头)。
		{`{"verdict":"methodical_access","basis":"公开文档站,与目标无归属"}`,
			TriageMethodical, "公开文档站,与目标无归属", false},
		{`{"targets":[{"host":"x.com","verdict":"nope","basis":"x"}]}`, "", "", true},
		{`不是 JSON`, "", "", true},
	}
	for _, c := range cases {
		reply, err := parseTriageReply(c.in)
		if c.fails {
			if err == nil {
				t.Errorf("%q: 应解析失败,得 %+v", c.in, reply)
			}
			continue
		}
		if err != nil || len(reply.Targets) == 0 ||
			reply.Targets[0].Verdict != c.v || reply.Targets[0].Basis != c.b {
			t.Errorf("%q: 得 %+v err=%v", c.in, reply, err)
		}
	}
}

// TestP467InfoSourceProjection 信息源清单投影与命中:指针语义(nil 不
// 动/非空覆盖/空数组清空)、host 级命中(端口不参与)、撤销落 revoked
// 留痕作废缓存。
func TestP467InfoSourceProjection(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p467-proj"
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test",
		Payload: rings.TaskLifecycle{Action: "created"}}); err != nil {
		t.Fatal(err)
	}
	// 入清单。
	if err := GrantInfoSource(ctx, store, task, "gitee.com", "代码托管,与目标无归属", "swarm:u1"); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "www.bing.com", "搜索引擎", "swarm:u1"); err != nil {
		t.Fatal(err)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 2 {
		t.Fatalf("清单应 2 条,得 %+v", roe.InfoSources)
	}
	if !roe.InfoSourceHit("gitee.com") || !roe.InfoSourceHit("www.bing.com") {
		t.Fatalf("清单 host 应命中")
	}
	if roe.InfoSourceHit("gitee.com.evil.net") || roe.InfoSourceHit("notgitee.com") {
		t.Fatalf("命中必须是 host 级精确,前缀/后缀不得命中")
	}
	if roe.InfoSourceHit("bing.com") {
		t.Fatalf("子串不得命中(www.bing.com ≠ bing.com)")
	}
	// 重复入清单幂等。
	if err := GrantInfoSource(ctx, store, task, "gitee.com", "再次", "swarm:u1"); err != nil {
		t.Fatal(err)
	}
	roe, _ = store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 2 {
		t.Fatalf("重复入清单应幂等,得 %d 条", len(roe.InfoSources))
	}
	// 快照透传(其余 roe.updated 不带 InfoSources 不误清空)。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{Note: "改门槛"}}); err != nil {
		t.Fatal(err)
	}
	roe, _ = store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 2 {
		t.Fatalf("透传快照不得误清空信息源清单,得 %d 条", len(roe.InfoSources))
	}
	// 撤销:清单移除 + tg- 缓存作废。
	if err := RevokeInfoSource(ctx, store, task, "www.bing.com:443", "creator"); err != nil {
		t.Fatal(err)
	}
	roe, _ = store.CurrentRoE(ctx, task)
	if roe.InfoSourceHit("www.bing.com") {
		t.Fatalf("撤销后不得命中")
	}
	if v, _ := TriageVerdictOf(ctx, store, task, "www.bing.com"); v != TriageRevoked {
		t.Fatalf("撤销应落粘性标记,得 %q", v)
	}
	// 撤销不在册:如实报错。
	if err := RevokeInfoSource(ctx, store, task, "nope.net", "creator"); err == nil {
		t.Fatalf("撤销不在册地址应报错")
	}
}

// p467TriageRecords 研判留痕计数(approval.record triage)。
func p467TriageRecords(t *testing.T, store *rings.Store, task string) int {
	t.Helper()
	n := 0
	for _, r := range approvalEvents(t, store, task) {
		if r.Type == rings.ApprovalRecTriage {
			n++
		}
	}
	return n
}

// ---- 攻击行为卡处置(drive 级活体:批准执行留痕/拒绝只杀动作单位存活) ----

// p467AttackDriveEnv 渗透任务 + 范围外攻击目标(1.2.3.4 经 --resolve 落
// 本机短命服务,P4-66 后环回不可直接当靶标)+ 预置 tg- 攻击判定缓存
// (任务级,单位领到即见——不依赖 drive 内的分诊注入)。
func p467AttackDriveEnv(t *testing.T) (*rings.Store, *scriptedModel, *Executor, string, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	var hits int32
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("p467-ok"))
	}))
	t.Cleanup(out.Close)
	outURL, _ := url.Parse(out.URL)
	outPort := outURL.Port()
	const task = "p467-drive"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "对信息源发起扫描",
			Status: "dispatched", Detail: map[string]any{"action_level": "主动测试"}}},
		// 预置攻击判定缓存:1.2.3.4 判过"信息源上的攻击行为"。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "overmind", Payload: BuildTriageRecord(
			"1.2.3.4", TriageAttack, "对信息源的扫描动作", rings.ApprovalRecord{})},
	); err != nil {
		t.Fatal(err)
	}
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply { return scriptedReply{} })
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	scanCmd := fmt.Sprintf(
		`{"command":"curl -s --max-time 5 --resolve 1.2.3.4:%s:127.0.0.1 http://1.2.3.4:%s/scan"}`, outPort, outPort)
	t.Cleanup(func() { model.Close() })
	return store, model, ex, task, scanCmd
}

// TestP467AttackApproveExecutes 完成标准②(批准路径):对信息源的扫描
// 动作出攻击行为卡——批准后该次动作执行留痕(hits==1)、单位原会话
// 续跑完成、host 仍非目标(零资产入图、零信息源入清单)。
func TestP467AttackApproveExecutes(t *testing.T) {
	store, model, ex, task, scanCmd := p467AttackDriveEnv(t)
	model.mu.Lock()
	model.script = func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "bash", "p467-ok") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash", Args: scanCmd,
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "动作已执行,收尾。", Finish: "stop"}
	}
	model.mu.Unlock()
	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	rid := p467WaitAttackTicket(t, ex, task)
	if err := ex.Gate.Decide(context.Background(), task, rid, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("批准后单位应完成: %+v", res)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 2 || !calls[0].IsError || calls[1].IsError ||
		!strings.Contains(calls[1].ResultHead, "p467-ok") {
		t.Fatalf("应 1 笔待决提示 + 1 笔真实执行(恰一次): %+v", calls)
	}
	// 账本:attack_wait 待决 + 批准记录;零范围卡、零资产入图、零信息源。
	recs := approvalEvents(t, store, task)
	var wait, approved int
	for _, r := range recs {
		if r.Type == rings.ApprovalRecAttackWait {
			wait++
		}
		if r.Type == rings.ApprovalRecApproved && strings.HasPrefix(r.RequestID, "ap-") {
			approved++
		}
	}
	if wait != 1 || approved != 1 {
		t.Fatalf("应恰 1 待决 + 1 批准,得 wait=%d approved=%d: %+v", wait, approved, recs)
	}
	if cards := p451ScopeWaitCards(t, store, task); len(cards) != 0 {
		t.Fatalf("攻击行为卡不是范围卡: %d 张", len(cards))
	}
	assets, _ := store.NodesByType(context.Background(), task, rings.NodeAsset, "")
	if len(assets) != 1 {
		t.Fatalf("host 仍非目标:零资产入图,得 %d 个", len(assets))
	}
	roe, _ := store.CurrentRoE(context.Background(), task)
	if len(roe.InfoSources) != 0 {
		t.Fatalf("攻击行为批准不放信息源入清单,得 %+v", roe.InfoSources)
	}
}

// TestP467AttackDenyKillsActionOnly 完成标准②(拒绝路径):拒绝只杀该
// 动作——单位同会话存活换路续跑(换条无目标的命令正常执行完成),
// 被拒动作零执行。
func TestP467AttackDenyKillsActionOnly(t *testing.T) {
	store, model, ex, task, scanCmd := p467AttackDriveEnv(t)
	model.mu.Lock()
	model.script = func(rr *recordedRequest) scriptedReply {
		switch rr.Index {
		case 0:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash", Args: scanCmd,
			}}, Finish: "tool_calls"}
		case 1: // 拒绝反馈后换路(无目标的命令)
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b2", Name: "bash", Args: mustJSON(t, "echo rerouted-after-deny"),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "已放弃对信息源的攻击,换方向。", Finish: "stop"}
	}
	model.mu.Unlock()
	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	rid := p467WaitAttackTicket(t, ex, task)
	if err := ex.Gate.Decide(context.Background(), task, rid, GateDecision{Approved: false, By: "creator", Reason: "不许扫它"}); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("拒绝只杀动作,单位应存活完成: %+v", res)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 2 || !calls[0].IsError || calls[1].IsError {
		t.Fatalf("应 1 笔待决提示 + 1 笔换路执行(被拒动作零执行): %+v", calls)
	}
	if !strings.Contains(calls[1].ResultHead, "rerouted-after-deny") {
		t.Fatalf("执行的应是换路命令: %+v", calls[1])
	}
	recs := approvalEvents(t, store, task)
	denied := 0
	for _, r := range recs {
		if r.Type == rings.ApprovalRecDenied && strings.HasPrefix(r.RequestID, "ap-") {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("应恰 1 笔拒绝留痕,得 %d: %+v", denied, recs)
	}
}

// p467WaitAttackTicket 等攻击行为卡票出现(轮询待决表;测试用)。
func p467WaitAttackTicket(t *testing.T, ex *Executor, task string) string {
	t.Helper()
	for i := 0; i < 400; i++ {
		for _, p := range ex.Gate.Pending(task) {
			if p.AttackTarget != "" {
				return p.ID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("攻击行为卡票未出现")
	return ""
}
