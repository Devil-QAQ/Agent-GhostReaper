package swarm

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// ---- P4-68(问题清单 #86 B＋C,RoE 机制重构批次第三阶):卡片三键与
// 拒绝分族处置 ----
// ① 范围卡第三键"允许访问"——卡上手授＝分诊自动授＝牧者标记(信息源
// 清单同一状态);② 拒绝分族——判定键＝图上意图-目标覆盖关系(covers∪
// verifies→about 求交),覆盖→止损口径、顺带触碰→只拒该条命令;③b 分
// 诊等待口径——删 30 秒墙钟,慢不算失败、链真报错才按拿不准出卡。
// 环境复用 p444/p450/p467 既有铺垫(渗透任务+DNS 打桩)。

// p468ExcludedEnv 渗透任务(种子 www.example.com)+ 已摘除目标
// example-x.com + 步骤锚定(可选 covers 种子/排除资产,模拟意图覆盖
// 形态)。返回 ready 的单位环境与任务包。
func p468ExcludedEnv(t *testing.T, coverAsset string) (*rings.Store, *Executor, *unitEnv, *TaskPack) {
	t.Helper()
	store, ex, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	evs := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "example-x.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "连通性测距与常规探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
	}
	if coverAsset != "" {
		evs = append(evs, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: coverAsset},
		})
	}
	if _, err := store.Append(ctx, task, evs...); err != nil {
		t.Fatal(err)
	}
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	return store, ex, env, pack
}

// TestP468IncidentalTouchKeepsUnit 完成标准②:意图不覆盖、命令顺带触碰
// 被排除目标(v1.2.2 实锤 u727/u743 形态——意图全是范围内工作,curl
// example.com 测距被杀)→ 只拒该条命令＋换路反馈,单位同会话继续
// (deniedReason 空、留痕一笔、不出卡)。
func TestP468IncidentalTouchKeepsUnit(t *testing.T) {
	store, _, env, pack := p468ExcludedEnv(t, "") // 无 covers:意图不锚定排除目标
	d := env.denyScope(pack, "bash", json.RawMessage(
		`{"command":"curl -s --max-time 5 http://example-x.com/"}`))
	if d == "" || !strings.Contains(d, "已被排除") || !strings.Contains(d, "换连通性测试对象或跳过") {
		t.Fatalf("顺带触碰应只拒该条命令＋换路反馈: %q", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("顺带触碰不得熔断单位(意图不覆盖该目标): %q", env.deniedReason)
	}
	if got := len(p451ScopeWaitCards(t, store, env.TaskID)); got != 0 {
		t.Fatalf("已排除目标不得出卡: %d", got)
	}
	recs := p444DenyRecords(t, store, env.TaskID)
	if len(recs) != 1 || recs[0].DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("应恰一笔 roe_scope 拒绝留痕: %+v", recs)
	}
}

// TestP468IntentCoversExcludedKills 完成标准②:意图覆盖被排除目标
// (步骤 covers 边锚定排除资产——工作线就是它)→ 止损口径:拒绝留痕＋
// 单位终止(deniedReason 置位,与摘除止损/P4-45 语义收紧一致)。
func TestP468IntentCoversExcludedKills(t *testing.T) {
	store, _, env, pack := p468ExcludedEnv(t, "a-ex") // covers 锚定排除资产
	d := env.denyScope(pack, "bash", json.RawMessage(
		`{"command":"curl -s --max-time 5 http://example-x.com/"}`))
	if d == "" || !strings.Contains(d, "禁止测试") {
		t.Fatalf("意图覆盖被排除目标应按止损口径直接拒: %q", d)
	}
	if env.deniedReason == "" || !strings.Contains(env.deniedReason, "禁止测试") {
		t.Fatalf("意图覆盖者应熔断单位: %q", env.deniedReason)
	}
	if got := len(p451ScopeWaitCards(t, store, env.TaskID)); got != 0 {
		t.Fatalf("已排除目标不得出卡: %d", got)
	}
}

// TestP468VerifiesHypothesisAnchor 意图覆盖判定含验证类步骤
// (Step→verifies→Hypothesis→about→Asset):假设的 about 锚点＝其指向
// 的目标资产,验证步骤锚定排除目标同样按止损口径(图查询零文本匹配)。
func TestP468VerifiesHypothesisAnchor(t *testing.T) {
	store, ex, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "example-x.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "h1", NodeType: rings.NodeHypothesis, Title: "example-x 存在影子后台", Status: "active"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "h1", ToNode: "a-ex"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "验证影子后台假设", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeVerifies, FromNode: "s1", ToNode: "h1"}},
	); err != nil {
		t.Fatal(err)
	}
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	// 命令文本不含目标词(纯连通性形态),覆盖关系只在图上——意图覆盖
	// 由 verifies→about 边判定,零文本匹配。
	d := env.denyScope(pack, "bash", json.RawMessage(
		`{"command":"curl -s --max-time 5 http://example-x.com/"}`))
	if d == "" || !strings.Contains(d, "禁止测试") || env.deniedReason == "" {
		t.Fatalf("验证步骤锚定排除目标应按止损口径(图判定): %q denied=%q", d, env.deniedReason)
	}
}

// TestP468MixedCommandPartialDeny 完成标准②:混合命令(范围内真目标＋
// 被排除 host)、意图覆盖范围内目标 → 只拒排除 host 相关执行、单位存活
// (命令含范围内目标不拦它,只对排除 host 分族;deniedReason 空)。
func TestP468MixedCommandPartialDeny(t *testing.T) {
	store, _, env, pack := p468ExcludedEnv(t, "a-seed") // 意图覆盖种子(范围内)
	d := env.denyScope(pack, "bash", json.RawMessage(
		`{"command":"curl -s http://www.example.com/app http://example-x.com/"}`))
	if d == "" || !strings.Contains(d, "example-x.com:80 已被排除") {
		t.Fatalf("混合命令应只拒排除 host 相关执行: %q", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("意图覆盖的是范围内目标,排除 host 顺带触碰只拒命令: %q", env.deniedReason)
	}
	// 顺带触碰留痕恰一笔,范围内目标零留痕零卡。
	if recs := p444DenyRecords(t, store, env.TaskID); len(recs) != 1 {
		t.Fatalf("应恰一笔拒绝留痕: %+v", recs)
	}
	if got := len(p451ScopeWaitCards(t, store, env.TaskID)); got != 0 {
		t.Fatalf("混合命令不得出卡: %d", got)
	}
}

// TestP468TouchEscalateAfterThree 顺带触碰的升级保险:同目标累计
// HardDenyEscalateAfter 次升级杀单位(与硬规则升级保险同机制、单位
// 生命周期累计不重置);前两次单位存活。
func TestP468TouchEscalateAfterThree(t *testing.T) {
	_, _, env, pack := p468ExcludedEnv(t, "")
	cmd := json.RawMessage(`{"command":"curl -s --max-time 5 http://example-x.com/"}`)
	for i := 1; i <= 2; i++ {
		d := env.denyScope(pack, "bash", cmd)
		if d == "" || env.deniedReason != "" {
			t.Fatalf("第 %d 次顺带触碰应只拒命令: d=%q denied=%q", i, d, env.deniedReason)
		}
	}
	d := env.denyScope(pack, "bash", cmd)
	if d == "" || !strings.Contains(d, "同目标累计命中 3 次") {
		t.Fatalf("第三次应升级杀: %q", d)
	}
	if env.deniedReason == "" || !strings.Contains(env.deniedReason, "同目标累计命中 3 次") {
		t.Fatalf("升级后单位应熔断: %q", env.deniedReason)
	}
}

// TestP468StopLossIntentCover 止损面收紧(P4-45 语义收紧到意图覆盖者):
// StopLossForAssets 只杀意图覆盖被摘除资产的步骤(covers 或
// verifies→about),顺带触碰形态的排队步骤不被误杀(v1.2.2 u727/u743
// 误杀的止损面对照)。
func TestP468StopLossIntentCover(t *testing.T) {
	store, _, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "example-x.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// s-cover:covers 锚定 a-ex(意图覆盖者,应被取消)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测试排除目标", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-ex"}},
		// s-touch:锚定种子(意图不覆盖,顺带触碰形态,不应被杀)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-touch", NodeType: rings.NodeStep, Title: "范围内常规工作", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-touch", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}}},
	); err != nil {
		t.Fatal(err)
	}
	_ = env
	killed, aborted, err := StopLossForAssets(ctx, store, nil, task, []string{"a-ex"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 || len(aborted) != 1 || aborted[0].StepID != "s-cover" {
		t.Fatalf("止损应只取消意图覆盖者: killed=%v aborted=%v", killed, aborted)
	}
	touch, terr := store.Node(ctx, task, "s-touch")
	if terr != nil || touch.Status != "dispatched" {
		t.Fatalf("顺带触碰步骤不应被误杀: %+v err=%v", touch, terr)
	}
}

// ---- ① 允许访问:同一状态多种入口(卡手授＝分诊自动授＝牧者标记) ----

// TestP468AllowAccessGrantsAndRetires 完成标准①:AllowScopeAccess 统一
// 入口——host 入信息源清单、图上同主机资产退役(资产图/待批清单读面
// 过滤)、后续访问零出卡(清单命中获取使用放行);撤销后同 host 访问
// 重新出卡(access 记录＝已裁,卡状态机按 open 重出)。
func TestP468AllowAccessGrantsAndRetires(t *testing.T) {
	store, _, env, pack := p450Env(t) // 种子 ef854.iujw.cn
	ctx := context.Background()
	// 图上同主机资产(待批形态)+ 待决卡(候选出卡)。
	if _, err := store.Append(ctx, env.TaskID,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-out", NodeType: rings.NodeAsset, Title: "gitee.com:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	env.triage = p467Triage(map[string]string{"gitee.com": TriageCandidate}, nil)
	if d := env.denyScope(pack, "bash", p467Target(t, "gitee.com:443")); d == "" {
		t.Fatal("候选目标应先出卡拦下")
	}
	if got := len(p451ScopeWaitCards(t, store, env.TaskID)); got != 1 {
		t.Fatalf("应恰 1 张待决卡: %d", got)
	}
	// 第三键核心(卡上手授)。
	if err := AllowScopeAccess(ctx, store, env.TaskID, "gitee.com:443", "操作者手授(允许访问)", "creator"); err != nil {
		t.Fatal(err)
	}
	// 清单在册;同主机资产退役。
	roe, _ := store.CurrentRoE(ctx, env.TaskID)
	if !roe.InfoSourceHit("gitee.com") {
		t.Fatalf("host 应入信息源清单: %+v", roe.InfoSources)
	}
	a, err := store.Node(ctx, env.TaskID, "a-out")
	if err != nil || a.Status != rings.AssetStatusRetired {
		t.Fatalf("同主机资产应退役: %+v err=%v", a, err)
	}
	// 资产图/待批清单读面过滤(等同不入资产图)。
	ag, _ := store.AssetGraph(ctx, env.TaskID)
	for _, x := range ag.Assets {
		if x.ID == "a-out" {
			t.Fatalf("退役资产不应出现在资产图: %+v", x)
		}
	}
	if pend, _ := store.PendingScopeAssets(ctx, env.TaskID); len(pend) != 0 {
		t.Fatalf("退役资产不在待批清单: %+v", pend)
	}
	// 后续访问零出卡(获取使用放行;动作复查打桩为非攻击)。
	env.triage = p467Triage(map[string]string{}, nil) // 无匹配→候选(动作复查中候选=获取使用放行)
	env.scopeHold = nil
	if d := env.denyScope(pack, "bash", p467Target(t, "gitee.com:443")); d != "" {
		t.Fatalf("允许访问后获取使用应零出卡: %q", d)
	}
	if got := len(p451ScopeWaitCards(t, store, env.TaskID)); got != 1 {
		t.Fatalf("不得新增卡(仍为原 1 张待裁决): %d", got)
	}
	// 撤销:清单移除,同 host 访问恢复出卡(access 后卡状态机按 open)。
	if err := RevokeInfoSource(ctx, store, env.TaskID, "gitee.com", "creator"); err != nil {
		t.Fatal(err)
	}
	env.scopeHold = nil
	env.triage = p467Triage(map[string]string{"gitee.com": TriageCandidate}, nil)
	if d := env.denyScope(pack, "bash", p467Target(t, "gitee.com:443")); d == "" {
		t.Fatal("撤销后同 host 访问应恢复出卡")
	}
}

// TestP468ListedSourceAttackStillCards P4-67 注意事项 3 收口:清单命中
// host 上的主动攻击动作仍走攻击行为卡(访问权与攻击行为正交——动作
// 复查单次分诊只判动作性质,零规则判攻击)。
func TestP468ListedSourceAttackStillCards(t *testing.T) {
	store, _, env, pack := p450Env(t)
	ctx := context.Background()
	if err := GrantInfoSource(ctx, store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	// 动作复查打桩:该 host 上判攻击。
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		v, basis := TriageCandidate, "归类"
		if in.ListedInfoSource {
			v, basis = TriageAttack, "对信息源主动攻击"
		}
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
	}
	scan := json.RawMessage(mustJSON(t, "nmap -p 1-1000 gitee.com"))
	d := env.denyScope(pack, "bash", scan)
	if d == "" || !strings.Contains(d, "信息源") || !strings.Contains(d, "已提交面板待决") {
		t.Fatalf("清单命中的攻击动作应出攻击行为卡: %q", d)
	}
	if env.park == nil || env.park.Req.AttackTarget == "" {
		t.Fatalf("应挂在攻击行为卡票上: %+v", env.park)
	}
	if env.scopeHold != nil {
		t.Fatalf("攻击行为卡不得置位范围冻结")
	}
	// 获取使用照常放行(动作复查非攻击)。
	env.park = nil
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		v, basis := TriageCandidate, "归类"
		if in.ListedInfoSource {
			v, basis = TriageMethodical, "获取使用"
		}
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
	}
	if d := env.denyScope(pack, "bash", p467Target(t, "gitee.com")); d != "" {
		t.Fatalf("清单命中的获取使用应放行: %q", d)
	}
}

// ---- ③b 分诊等待口径:删 30 秒墙钟(慢≠失败) ----

// p468ProbeProvider 记录分诊调用 ctx 的 provider 包装(墙钟删除的直接
// 证明:无分诊自身 deadline)＋可编排延迟/报错。
type p468ProbeProvider struct {
	deadline bool
	delay    time.Duration
	fail     bool
}

func (p *p468ProbeProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if _, ok := ctx.Deadline(); ok {
		p.deadline = true
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		}
	}
	if p.fail {
		return llm.Message{}, "", llm.Usage{}, context.DeadlineExceeded
	}
	return llm.Message{Content: []llm.ContentBlock{{Text: `{"verdict":"methodical_access","basis":"公开文档站"}`}}},
		"", llm.Usage{}, nil
}

func (p *p468ProbeProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg, _, _, err := p.Complete(ctx, req)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for _, b := range msg.Content {
			if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: b.Text}, nil) {
				return
			}
		}
	}
}

// TestP468TriageNoWallClock 完成标准④:分诊调用不带自身墙钟(ctx 无
// deadline,失败判定交给链既有语义)——慢端点(远超原 30 秒墙钟的相对
// 形态,按比例缩放)等到结果按结论处置零出卡。
func TestP468TriageNoWallClock(t *testing.T) {
	store, _, env, pack := p450Env(t)
	prov := &p468ProbeProvider{delay: 1200 * time.Millisecond} // 原墙钟 30s 的 1/25;若按比例缩短仍属"慢"
	env.triage = TriageFor(prov)
	start := time.Now()
	d := env.denyScope(pack, "bash", p467Target(t, "docs.python.org"))
	if time.Since(start) < 1200*time.Millisecond {
		t.Fatalf("分诊应等结果本身(慢不算失败): 仅 %v", time.Since(start))
	}
	if d != "" || env.scopeHold != nil {
		t.Fatalf("慢端点等到结论应按结论处置(方法性访问零出卡): d=%q", d)
	}
	if prov.deadline {
		t.Fatalf("分诊 ctx 不应带自身墙钟(30 秒墙钟已删)")
	}
	// 入清单可证(结论生效)。
	roe, _ := store.CurrentRoE(context.Background(), env.TaskID)
	if !roe.InfoSourceHit("docs.python.org") {
		t.Fatalf("方法性访问结论应入清单: %+v", roe.InfoSources)
	}
}

// TestP468TriageChainErrorFailsClosed 完成标准④对照:链真报错按拿不准
// 出卡(失败方向钉死——宁可多问不误放);慢不算失败、错才算。
func TestP468TriageChainErrorFailsClosed(t *testing.T) {
	_, _, env, pack := p450Env(t)
	prov := &p468ProbeProvider{fail: true}
	env.triage = TriageFor(prov)
	d := env.denyScope(pack, "bash", p467Target(t, "unknown-fail.net"))
	if d == "" || env.scopeHold == nil {
		t.Fatalf("链真报错应按拿不准出卡(冻结等待): d=%q", d)
	}
	if got := len(p451ScopeWaitCards(t, env.Executor.Store, env.TaskID)); got != 1 {
		t.Fatalf("应恰一张范围卡: %d", got)
	}
}

// TestP468ScopeResumePromptSelection 续跑指令按现值区分:目标在信息源
// 清单(允许访问唤醒)→"允许访问 · 续跑"(只授访问权措辞);不在清单
// (加入资产唤醒)→"范围批准 · 续跑"(可测试措辞)。原会话续跑链路
// (pendingSession resumeScope→drive 首段)为 P4-51 既有,e2e 由
// TestP451FreezeApproveResumeE2E 承载。
func TestP468ScopeResumePromptSelection(t *testing.T) {
	store, ex, _, pack := p450Env(t)
	ctx := context.Background()
	// 步骤带 scope_wait 目标标记(冻结时投影合并的 detail 形态)。
	if _, err := store.Append(ctx, pack.TaskID,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: pack.StepID, Status: rings.StepScopeWait,
				Note: "scope_wait gitee.com 443"}},
	); err != nil {
		t.Fatal(err)
	}
	if got := scopeResumePrompt(ex, pack); !strings.Contains(got, "范围批准 · 续跑") {
		t.Fatalf("清单未命中应走加入资产措辞: %q", got)
	}
	if err := GrantInfoSource(ctx, store, pack.TaskID, "gitee.com", "依据", "creator"); err != nil {
		t.Fatal(err)
	}
	got := scopeResumePrompt(ex, pack)
	if !strings.Contains(got, "允许访问 · 续跑") || !strings.Contains(got, "gitee.com") {
		t.Fatalf("清单命中应走允许访问措辞(只授访问权,不是测试目标): %q", got)
	}
}

// TestP468CardStateAccessReopens 卡状态机:access 记录(允许访问已裁)
// 后按 open 处理——撤销清单再访问时重出卡,由范围卡键一锤定音。
func TestP468CardStateAccessReopens(t *testing.T) {
	_, _, env, pack := p450Env(t)
	rid := scopeCardID("gitee.com", "443")
	// 待决 → access(第三键裁决形态)。
	env.recordApproval(rings.ApprovalRecord{RequestID: rid, Type: rings.ApprovalRecScopeWait, ToolName: "bash"})
	if got := env.scopeCardState(pack, rid); got != scopeCardPending {
		t.Fatalf("待决中: %d", got)
	}
	env.recordApproval(rings.ApprovalRecord{RequestID: rid, Type: rings.ApprovalRecAccess})
	if got := env.scopeCardState(pack, rid); got != scopeCardOpen {
		t.Fatalf("access 后应按 open(撤销后可重出卡): %d", got)
	}
	// denied 终局性不受 access 影响(禁止测试仍直接拒)。
	env.recordApproval(rings.ApprovalRecord{RequestID: rid, Type: rings.ApprovalRecDenied})
	if got := env.scopeCardState(pack, rid); got != scopeCardDenied {
		t.Fatalf("denied 应为终局: %d", got)
	}
}
