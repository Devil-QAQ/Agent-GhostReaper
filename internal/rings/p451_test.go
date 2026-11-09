package rings

import (
	"context"
	"path/filepath"
	"testing"
)

// ---- P4-51(问题清单 #73②③④⑦):范围审批机制的数据层配套 ----
// 排除覆盖判定(防骚扰)、范围待决卡计数(红点)、scope_wait 卡的
// 记录校验豁免。

func p451OpenStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestP451ExcludedCoversTarget 排除覆盖按写法粒度:裸域名/IP 整台全端口、
// host:端口仅此端口、*.域含基域与子域;全端口意图只被全端口摘除覆盖。
func TestP451ExcludedCoversTarget(t *testing.T) {
	assets := []NodeFull{
		{NodeView: NodeView{ID: "a-bare", Title: "evil.com"}},
		{NodeView: NodeView{ID: "a-port", Title: "port9.org:9090"}},
		{NodeView: NodeView{ID: "a-wild", Title: "*.banned.org"}},
		{NodeView: NodeView{ID: "a-ip", Title: "10.9.9.9"}},
	}
	ex := map[string]bool{"a-bare": true, "a-port": true, "a-wild": true, "a-ip": true}

	cases := []struct {
		host, port string
		want       bool
	}{
		{"evil.com", "", true},       // 裸域名摘除:整台全端口
		{"evil.com", "8080", true},   // 任意端口
		{"sub.evil.com", "", false},  // 子域不在裸域名摘除内(写法即粒度)
		{"port9.org", "9090", true},  // 端口限定摘除:仅此端口
		{"port9.org", "80", false},   // 他端口不覆盖
		{"port9.org", "", false},     // 全端口意图未被端口限定摘除覆盖
		{"banned.org", "", true},     // *.域摘除:含基域
		{"a.b.banned.org", "", true}, // 含多级子域
		{"nobanned.org", "", false},  // 后缀巧合不算子域
		{"10.9.9.9", "22", true},     // 裸 IP 摘除:整台全端口
		{"10.9.9.10", "", false},     // 相邻 IP 不在内
		{"good.com", "", false},      // 无关目标
	}
	for _, c := range cases {
		if got := ExcludedCoversTarget(assets, ex, c.host, c.port); got != c.want {
			t.Errorf("ExcludedCoversTarget(%s:%s)=%v want %v", c.host, c.port, got, c.want)
		}
	}
	// 排除集为空恒不覆盖。
	if ExcludedCoversTarget(assets, nil, "evil.com", "") {
		t.Error("空排除集不得覆盖任何目标")
	}
}

// TestP451ScopePendingCount 范围待决卡计数:未裁决目标计数、裁决后消、
// 多目标各计各的;denied/approved 均视为已收口。
func TestP451ScopePendingCount(t *testing.T) {
	s := p451OpenStore(t)
	ctx := context.Background()
	const task = "p451-count"
	appendCard := func(rid string) {
		if _, err := s.Append(ctx, task, Event{
			Kind: EventApprovalRecord, Actor: "swarm:u1",
			Payload: ApprovalRecord{RequestID: rid, Type: ApprovalRecScopeWait},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCard("sc-a.com")
	appendCard("sc-b.com")
	if n, err := s.ScopePendingCount(ctx, task); err != nil || n != 2 {
		t.Fatalf("两目标应计 2: %d %v", n, err)
	}
	// 裁决 a.com(加入资产=approved)后消。
	if _, err := s.Append(ctx, task, Event{
		Kind: EventApprovalRecord, Actor: "creator",
		Payload: ApprovalRecord{RequestID: "sc-a.com", Type: ApprovalRecApproved, Reason: "加入资产"},
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ScopePendingCount(ctx, task); n != 1 {
		t.Fatalf("裁决后应计 1: %d", n)
	}
	// 裁决 b.com(禁止测试=denied)后归零。
	if _, err := s.Append(ctx, task, Event{
		Kind: EventApprovalRecord, Actor: "creator",
		Payload: ApprovalRecord{RequestID: "sc-b.com", Type: ApprovalRecDenied, Reason: "禁止测试"},
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("全部裁决后应计 0: %d", n)
	}
}

// TestP451ScopeWaitRecordValidation 范围待决卡的校验豁免:红队发现卡无
// 步骤/单位/工具/分级也可落账(锚定目标而非命令);RequestID 仍必填;
// 普通类型四字段要求不放宽。
func TestP451ScopeWaitRecordValidation(t *testing.T) {
	// 红队发现卡形态:仅 RequestID。
	if err := validateApprovalRecord(ApprovalRecord{
		RequestID: "sc-x.com", Type: ApprovalRecScopeWait,
	}); err != nil {
		t.Fatalf("红队发现卡应可落账: %v", err)
	}
	// scope_wait 缺 RequestID 仍拒。
	if err := validateApprovalRecord(ApprovalRecord{Type: ApprovalRecScopeWait}); err == nil {
		t.Fatal("scope_wait 缺 RequestID 应拒绝")
	}
	// 普通类型缺字段照旧拒绝(校验未整体放宽)。
	if err := validateApprovalRecord(ApprovalRecord{
		RequestID: "ap-1", Type: ApprovalRecRequested,
	}); err == nil {
		t.Fatal("requested 缺步骤/单位/工具应拒绝")
	}
}
