package rings

// P5-04 TrafficRows host 预筛(instr(lower(payload)))的类级测试:多形态
// 样本集断言预筛不改变语义(大小写/tunnel/通配字面量/非 ASCII/无 host),
// 以及大流量账本下带 host 过滤的查询秒回(防慢闸门:裸搜不许上线)。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestP504TrafficHostPrefilterSemantics 预筛语义不变:各形态输入下,
// 带 host 过滤的结果与权威判定(Go 侧 host 派生匹配)一致。
func TestP504TrafficHostPrefilterSemantics(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

	seedMirrorRec(t, s, "tA", "ex1", MirrorRecHTTP, "GET", "http://Mixed.Case.Example.com/x", 200, "b1", base)
	seedMirrorRec(t, s, "tA", "ex2", MirrorRecTunnel, "", "upper.host.example.com:22", 0, "", base.Add(time.Minute))
	seedMirrorRec(t, s, "tA", "ex3", MirrorRecHTTP, "GET", "http://plain.example.com/a%2fb?x=1", 200, "b3", base.Add(2*time.Minute))
	seedMirrorRec(t, s, "tA", "ex4", MirrorRecHTTP, "GET", "http://中文.示例.com/路径", 200, "b4", base.Add(3*time.Minute))
	seedMirrorRec(t, s, "tA", "ex5", MirrorRecHTTP, "GET", "http://other.net/z", 200, "b5", base.Add(4*time.Minute))

	rows := func(f TrafficFilter) map[string]bool {
		t.Helper()
		out, _, err := s.TrafficRows(ctx, f)
		if err != nil {
			t.Fatalf("TrafficRows 失败: %v", err)
		}
		set := map[string]bool{}
		for _, r := range out {
			set[r.ExchangeID] = true
		}
		return set
	}

	// 小写查询命中大写 URL(预筛 lower() 与 Go 侧 ToLower 同口径)。
	if got := rows(TrafficFilter{Host: "case.example.com"}); len(got) != 1 || !got["ex1"] {
		t.Fatalf("小写查询应命中大写 URL 的 ex1: %v", got)
	}
	// 大写查询同样命中(SQL lower 折叠)。
	if got := rows(TrafficFilter{Host: "CASE.Example"}); len(got) != 1 || !got["ex1"] {
		t.Fatalf("大写查询应命中 ex1: %v", got)
	}
	// tunnel 的 CONNECT 目标 host(无 URL 结构,预筛子串在 payload 内)。
	if got := rows(TrafficFilter{Host: "upper.host"}); len(got) != 1 || !got["ex2"] {
		t.Fatalf("tunnel host 应命中 ex2: %v", got)
	}
	// % 与 _ 按字面量匹配(instr 非 LIKE 通配;Go 侧 Contains 同语义:
	// 任何 host 都不含 % 与 _,LIKE 语义下则 % 会通配命中全部行)。
	if got := rows(TrafficFilter{Host: "%"}); len(got) != 0 {
		t.Fatalf("%% 字面量不应命中任何 host(LIKE 语义会误命中全部): %v", got)
	}
	if got := rows(TrafficFilter{Host: "_"}); len(got) != 0 {
		t.Fatalf("_ 字面量不应命中(全部 host 无下划线): %v", got)
	}
	if got := rows(TrafficFilter{Host: "plain.example.com"}); len(got) != 1 || !got["ex3"] {
		t.Fatalf("普通 host 应命中 ex3: %v", got)
	}
	// 非 ASCII host 输入:预筛不生效(退全扫),命中照样正确。
	if got := rows(TrafficFilter{Host: "示例.com"}); len(got) != 1 || !got["ex4"] {
		t.Fatalf("非 ASCII 查询应命中 ex4: %v", got)
	}
	// 子串不命中的主域不误伤。
	if got := rows(TrafficFilter{Host: "other.net"}); len(got) != 1 || !got["ex5"] {
		t.Fatalf("other.net 应恰命中 ex5: %v", got)
	}
	// 无 host:无预筛,全量照常。
	if got := rows(TrafficFilter{}); len(got) != 5 {
		t.Fatalf("无 host 全量应 5 行: %v", got)
	}
}

// TestP504TrafficRowsFastOnLargeLedger 防慢闸门:大流量账本(2 万条留痕)
// 下,host 过滤查询必须秒回(预筛跳过无关行的 JSON 解码)。
func TestP504TrafficRowsFastOnLargeLedger(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-big"
	const n = 20000

	// 铺两台主机的交替流量(单批事务,快)。
	evs := make([]Event, 0, n)
	base := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		host := "bulk-a.example.com"
		if i%2 == 1 {
			host = "bulk-b.example.com"
		}
		evs = append(evs, Event{
			Kind: EventMirrorRecord, Actor: "mirror", OccurredAt: base.Add(time.Duration(i) * time.Millisecond),
			Payload: MirrorRecord{
				ExchangeID: fmt.Sprintf("ms-bulk.%d", i), Type: MirrorRecHTTP,
				StepID: "s1", UnitID: "u1", Method: "GET",
				URL: "http://" + host + "/p/" + fmt.Sprint(i%97), Status: 200, BlobRef: "b",
			},
		})
	}
	if _, err := s.Append(ctx, task, evs...); err != nil {
		t.Fatalf("铺大账本失败: %v", err)
	}

	start := time.Now()
	rows, total, err := s.TrafficRows(ctx, TrafficFilter{TaskID: task, Host: "bulk-b.example.com", Limit: 10})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("大账本查询失败: %v", err)
	}
	if total != n/2 || len(rows) != 10 {
		t.Fatalf("过滤命中不符: rows=%d total=%d", len(rows), total)
	}
	if elapsed > time.Second {
		t.Fatalf("大流量账本(2 万条)host 过滤应秒回,实际 %s(慢即补索引,裸搜不许上线)", elapsed)
	}
	t.Logf("2 万条留痕 host 过滤耗时 %s(total=%d)", elapsed, total)
}
