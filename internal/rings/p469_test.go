package rings

// P4-69(问题清单 #86 D,摘除联动收口与状态一致性)rings 层:
// ExcludedAssetsCovering 写法粒度类级、PendingScopeCards 未决卡查询
// 口径、ReconcileRoeExclusivity 排除与批准互斥的存量清理本体。

import (
	"context"
	"path/filepath"
	"testing"
)

// p469Store 开临时库并落一个任务与 given 资产(来源发现范围外,标题
// 逐条对应)。
func p469Store(t *testing.T, task string, assetTitles ...string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": TaskTypePentest}}},
	); err != nil {
		t.Fatal(err)
	}
	var evs []Event
	for i, title := range assetTitles {
		evs = append(evs, Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "a-" + string(rune('a'+i)), NodeType: NodeAsset, Title: title,
			Status: "active", AssetClass: AssetDomain,
			Detail: map[string]any{"source": AssetSourceDiscovered, "scope": AssetScopeOut}}})
	}
	if len(evs) > 0 {
		if _, err := s.Append(ctx, task, evs...); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestP469ExcludedAssetsCoveringGranularity 类级:被排除资产的写法粒度
// ——裸域名/IP=整台全端口;host:端口=仅此端口;*.域=基域＋子域;端口
// 不匹配/不同主机不算(与 ExcludedCoversTarget 同源对称,两者口径须
// 一致——逐形态双向断言)。
func TestP469ExcludedAssetsCoveringGranularity(t *testing.T) {
	ctx := context.Background()
	const task = "t-p469-cover"
	s := p469Store(t, task, "bare.com", "port.com:8443", "10.1.2.3", "*.wild.com", "other.com")
	assets, err := s.NodesByType(ctx, task, NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	excluded := map[string]bool{"a-a": true, "a-b": true, "a-c": true, "a-d": true}
	cases := []struct {
		host, port string
		want       []string // 命中的资产 ID 集
	}{
		{"bare.com", "", []string{"a-a"}},          // 裸域名:全端口意图命中
		{"bare.com", "9999", []string{"a-a"}},      // 裸域名:任意端口命中
		{"port.com", "8443", []string{"a-b"}},      // 端口限定:仅此端口
		{"port.com", "", nil},                      // 端口限定×全端口意图:不算
		{"port.com", "9090", nil},                  // 端口限定×他端口:不算
		{"10.1.2.3", "", []string{"a-c"}},          // 裸 IP:整台
		{"10.1.2.3", "22", []string{"a-c"}},        // 裸 IP:任意端口
		{"wild.com", "", []string{"a-d"}},          // 通配:基域
		{"sub.wild.com", "443", []string{"a-d"}},   // 通配:任意子域任意端口
		{"deep.sub.wild.com", "", []string{"a-d"}}, // 通配:深层子域
		{"notwild.com", "", nil},                   // 后缀巧合不算子域
		{"other.com", "443", nil},                  // 未排除资产:零命中
		{"bare.com.evil.net", "", nil},             // 后缀巧合不算子域
	}
	idSet := func(ids []string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	for _, c := range cases {
		got := idSet(ExcludedAssetsCovering(assets, excluded, c.host, c.port))
		want := idSet(c.want)
		for id := range want {
			if !got[id] {
				t.Fatalf("%s:%s 应命中 %s: %v", c.host, c.port, id, got)
			}
		}
		for id := range got {
			if !want[id] {
				t.Fatalf("%s:%s 不应命中 %s: %v", c.host, c.port, id, got)
			}
		}
		// 与 ExcludedCoversTarget 双向一致(命中集非空 ⇔ 命中为真)。
		if cover := ExcludedCoversTarget(assets, excluded, c.host, c.port); cover != (len(got) > 0) {
			t.Fatalf("%s:%s 清单形态与布尔形态不一致: cover=%v got=%v", c.host, c.port, cover, got)
		}
	}
}

// TestP469PendingScopeCards 未决卡查询口径:每个请求号的最新记录恰为
// scope_wait 才在列——待决卡在、显式裁决(approved/denied/access)后不在、
// 多卡并存全量可见、其他类型(triage/弹门)不混入。
func TestP469PendingScopeCards(t *testing.T) {
	ctx := context.Background()
	const task = "t-p469-cards"
	s := p469Store(t, task)
	if _, err := s.Append(ctx, task,
		// 待决卡 1(渗透命令拦截形态)。
		Event{Kind: EventApprovalRecord, Actor: "swarm:u1", Payload: ApprovalRecord{
			RequestID: ScopeCardID("wait1.com", "80"), StepID: "s1",
			Type: ApprovalRecScopeWait, ToolName: "bash"}},
		// 卡 2:待决后被显式拒绝(禁止测试)——不在未决列。
		Event{Kind: EventApprovalRecord, Actor: "swarm:u2", Payload: ApprovalRecord{
			RequestID: ScopeCardID("denied.com", "443"), StepID: "s2",
			Type: ApprovalRecScopeWait, ToolName: "bash"}},
		Event{Kind: EventApprovalRecord, Actor: "creator", Payload: ApprovalRecord{
			RequestID: ScopeCardID("denied.com", "443"), Type: ApprovalRecDenied}},
		// 卡 3:红队可否决形态(Note 不同,同为 scope_wait)。
		Event{Kind: EventApprovalRecord, Actor: "overmind", Payload: ApprovalRecord{
			RequestID: ScopeCardID("redtarget.com", ""), Type: ApprovalRecScopeWait,
			Note: "红队任务:发现范围外资产"}},
		// 卡 4:待决后经第三键允许访问——最新记录为 access,不在未决列。
		Event{Kind: EventApprovalRecord, Actor: "swarm:u3", Payload: ApprovalRecord{
			RequestID: ScopeCardID("access.com", ""), StepID: "s3",
			Type: ApprovalRecScopeWait, ToolName: "bash"}},
		Event{Kind: EventApprovalRecord, Actor: "creator", Payload: ApprovalRecord{
			RequestID: ScopeCardID("access.com", ""), Type: ApprovalRecAccess}},
		// 非范围卡记录(分诊留痕)不混入。
		Event{Kind: EventApprovalRecord, Actor: "overmind", Payload: ApprovalRecord{
			RequestID: TriageCardID("info.org"), Type: ApprovalRecTriage}},
	); err != nil {
		t.Fatal(err)
	}
	cards, err := s.PendingScopeCards(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range cards {
		got[c.RequestID] = true
	}
	if !got[ScopeCardID("wait1.com", "80")] || !got[ScopeCardID("redtarget.com", "")] {
		t.Fatalf("待决卡应全量在列: %v", got)
	}
	if got[ScopeCardID("denied.com", "443")] || got[ScopeCardID("access.com", "")] {
		t.Fatalf("已裁决卡不应在未决列: %v", got)
	}
	if got[TriageCardID("info.org")] {
		t.Fatalf("分诊留痕不应混入未决卡: %v", got)
	}
	if len(cards) != 2 {
		t.Fatalf("未决卡应恰 2 张: %d", len(cards))
	}
}

// TestP469ReconcileExclusivity 互斥清理本体:摘除清单成员一律从批准
// 清单剔除(存量脏快照归位);两清单无交集时零落笔(幂等);清空方向
// (批准清单全为交集)同样成立。
func TestP469ReconcileExclusivity(t *testing.T) {
	ctx := context.Background()
	const task = "t-p469-reconcile"
	s := p469Store(t, task, "x.com", "y.com", "z.com")
	// 旧机制脏快照:a-x 摘除却又在批准列(实锤 a-72dab0e5 形态),
	// a-z 只在批准列(不相交,保留)。
	if _, err := s.Append(ctx, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ExcludedAssets: []string{"a-a"}, ScopeApproved: []string{"a-a", "a-c"}}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.ReconcileRoeExclusivity(ctx, task, "manager")
	if err != nil || removed != 1 {
		t.Fatalf("应剔除 1 条: %d %v", removed, err)
	}
	st, _ := s.CurrentRoE(ctx, task)
	if len(st.ScopeApproved) != 1 || st.ScopeApproved[0] != "a-c" {
		t.Fatalf("批准清单应只剩不相交成员: %+v", st.ScopeApproved)
	}
	if len(st.ExcludedAssets) != 1 {
		t.Fatalf("摘除清单不动: %+v", st.ExcludedAssets)
	}
	// 幂等:干净后再清零落笔。
	revBefore := st.UpdatedRev
	if removed, err := s.ReconcileRoeExclusivity(ctx, task, "manager"); err != nil || removed != 0 {
		t.Fatalf("干净任务应零剔除: %d %v", removed, err)
	}
	if st2, _ := s.CurrentRoE(ctx, task); st2.UpdatedRev != revBefore {
		t.Fatalf("干净任务应零落笔: %d → %d", revBefore, st2.UpdatedRev)
	}
}
