package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-67(问题清单 #86 A②,RoE 机制重构批次第二层):发现路分诊 ----
// 范围外新资产入图前归类(两模式同款):信息源不入资产图、只记清单
//(渗透不出待批卡、红队不出可否决卡、不进"发现的其他资产"分组);
// 候选/拿不准照走现行入图(渗透待批锁定、红队直接测＋可否决卡)。
// 任务级缓存:信息源清单/tg- 留痕命中零分诊调用。环境复用 p444 种子
// 助手(渗透/红队任务 + 带 new_asset 声明的候选事实)。

// p467TriageStub 打桩分诊(发现路注入)。
func p467TriageStub(verdict string, calls *int) swarm.TriageFunc {
	return func(_ context.Context, in swarm.TriageInput) (string, string, error) {
		if calls != nil {
			*calls++
		}
		return verdict, "打桩依据:" + in.Host, nil
	}
}

// TestP467DiscoveryInfoSourceNotInGraph 完成标准①(发现路):信息源多
// 形态(代码托管/搜索引擎/公共文档各一)不入资产图、只记清单、零待
// 批卡零红队卡——渗透任务。
func TestP467DiscoveryInfoSourceNotInGraph(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p467-pen-disc"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	for i, host := range []string{
		"gitee.com",       // 代码托管类
		"www.bing.com",    // 搜索引擎类
		"docs.python.org", // 公共文档类
	} {
		p444FactWithAsset(t, store, task, "f-"+host, host)
		_ = i
	}
	called := 0
	if _, err := Harvest(ctx, store, task, p467TriageStub(swarm.TriageMethodical, &called)); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	if called != 3 {
		t.Fatalf("三个新 host 应各分诊一次,得 %d 次", called)
	}
	// 零资产入图(只有种子)。
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 1 || assets[0].Title != "www.example.com" {
		t.Fatalf("信息源零资产入图,得 %d 个: %+v", len(assets), assets)
	}
	// 信息源清单:三条,带依据摘要。
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 3 {
		t.Fatalf("信息源清单应 3 条,得 %+v", roe.InfoSources)
	}
	// 零范围卡(渗透不出发现卡,信息源更不出)。
	if cards := p451CardsOf(t, store, task); len(cards) != 0 {
		t.Fatalf("信息源发现零卡,得 %d 张: %+v", len(cards), cards)
	}
	// 事实照常转正(观察保留,审计完整)。
	for _, host := range []string{"gitee.com", "www.bing.com", "docs.python.org"} {
		n, err := store.Node(ctx, task, "f-"+host)
		if err != nil || n.Status != "confirmed" {
			t.Fatalf("发现事实 %s 应照常转正: %+v err=%v", "f-"+host, n, err)
		}
	}
	// 研判留痕:三笔 triage。
	if n := p467TriageCount(t, store, task); n != 3 {
		t.Fatalf("应恰 3 笔研判留痕,得 %d", n)
	}
}

// TestP467DiscoveryCandidateEntersGraphPentest 完成标准①(对照):相关
// 资产候选照走现行入图——渗透任务范围外发现入图挂待批(scope=out),
// 零信息源入清单。
func TestP467DiscoveryCandidateEntersGraphPentest(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p467-pen-cand"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, task, "f-cand", "mail.example.com")
	called := 0
	if _, err := Harvest(ctx, store, task, p467TriageStub(swarm.TriageCandidate, &called)); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 2 {
		t.Fatalf("候选应入图(种子+新资产),得 %d 个", len(assets))
	}
	for _, a := range assets {
		if a.Title == "mail.example.com" && rings.AssetScopeOf(a.Detail) != rings.AssetScopeOut {
			t.Fatalf("渗透任务候选入图应挂待批(scope=out): %+v", a)
		}
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 0 {
		t.Fatalf("候选不得入信息源清单,得 %+v", roe.InfoSources)
	}
}

// TestP467DiscoveryRedteam 完成标准⑦(红队对照):信息源零卡零分组
// (不入图即无"发现的其他资产"分组、不出可否决卡);候选入图＋可
// 否决卡(现行机制)。
func TestP467DiscoveryRedteam(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p467-red-disc"
	p444Task(t, store, task, rings.TaskTypeRedteam, "www.example.com")
	// 信息源发现:不入图、不出卡。
	p444FactWithAsset(t, store, task, "f-info", "gitee.com")
	// 候选发现:入图＋可否决卡。
	p444FactWithAsset(t, store, task, "f-cand", "mail.example.com")
	called := 0
	triage := func(_ context.Context, in swarm.TriageInput) (string, string, error) {
		called++
		if in.Host == "gitee.com" {
			return swarm.TriageMethodical, "公开代码托管服务", nil
		}
		return swarm.TriageCandidate, "同根域子域,归属线索存在", nil
	}
	if _, err := Harvest(ctx, store, task, triage); err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	// 信息源不入图:资产树只有种子+候选。
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 2 {
		t.Fatalf("红队信息源零入图(种子+候选共 2),得 %d 个: %+v", len(assets), assets)
	}
	// 卡:候选恰一张可否决卡;信息源零卡。
	cards := p451CardsOf(t, store, task)
	if len(cards) != 1 || !strings.Contains(cards[0].RequestID, "mail.example.com") {
		t.Fatalf("应恰 1 张候选可否决卡(信息源零卡),得 %+v", cards)
	}
	// 信息源清单在册。
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.InfoSources) != 1 || roe.InfoSources[0].Host != "gitee.com" {
		t.Fatalf("信息源应入清单,得 %+v", roe.InfoSources)
	}
	if called != 2 {
		t.Fatalf("两个新 host 应各分诊一次,得 %d 次", called)
	}
}

// TestP467DiscoveryCacheNoRepeat 任务级缓存:同 host 第二次发现零分诊
// 调用(信息源清单命中)。
func TestP467DiscoveryCacheNoRepeat(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p467-pen-cache"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, task, "f-1", "gitee.com")
	called := 0
	if _, err := Harvest(ctx, store, task, p467TriageStub(swarm.TriageMethodical, &called)); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("首次发现应分诊一次,得 %d", called)
	}
	// 同 host 再次发现(另一事实):清单命中零调用,不入图。
	p444FactWithAsset(t, store, task, "f-2", "gitee.com")
	if _, err := Harvest(ctx, store, task, p467TriageStub(swarm.TriageMethodical, &called)); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("同 host 第二次发现应零分诊调用,得 %d", called)
	}
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets) != 1 {
		t.Fatalf("重复发现仍零入图,得 %d 个", len(assets))
	}
}

// p451CardsOf 任务范围卡(scope_wait 记录,全量行过滤)。
func p451CardsOf(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	var out []rings.ApprovalRecord
	for _, r := range p467AllApprovals(t, store, task) {
		if r.Type == rings.ApprovalRecScopeWait {
			out = append(out, r)
		}
	}
	return out
}

// p467TriageCount 研判留痕计数。
func p467TriageCount(t *testing.T, store *rings.Store, task string) int {
	t.Helper()
	n := 0
	for _, r := range p467AllApprovals(t, store, task) {
		if r.Type == rings.ApprovalRecTriage {
			n++
		}
	}
	return n
}

func p467AllApprovals(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	rows, err := store.ApprovalRows(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]rings.ApprovalRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, rings.ApprovalRecord{RequestID: r.RequestID, Type: r.Type, Note: r.Note})
	}
	return out
}
