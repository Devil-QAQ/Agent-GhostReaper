package rings

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// P3-02:流量行查询、证据版本推导与证据绑定的账本侧物理校验。

// seedMirrorRec 追加一条镜留痕事件(账本侧;无需真实 blob——校验只看
// blob_ref 非空)。
func seedMirrorRec(t *testing.T, s *Store, task, exID, typ, method, url string, status int, blobRef string, at time.Time) {
	t.Helper()
	_, err := s.Append(context.Background(), task, Event{
		Kind: EventMirrorRecord, Actor: "mirror", OccurredAt: at,
		Payload: MirrorRecord{
			ExchangeID: exID, Type: typ, StepID: "s1", UnitID: "u1",
			Method: method, URL: url, Status: status, BlobRef: blobRef,
		},
	})
	if err != nil {
		t.Fatalf("铺镜留痕 %s 失败: %v", exID, err)
	}
}

func TestTrafficRowsFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

	seedMirrorRec(t, s, "tA", "ex1", MirrorRecHTTP, "GET", "http://a.example.com/x", 200, "b1", base)
	seedMirrorRec(t, s, "tA", "ex2", MirrorRecHTTPS, "POST", "https://b.example.com/y", 403, "b2", base.Add(time.Minute))
	seedMirrorRec(t, s, "tA", "ex3", MirrorRecTunnel, "", "a.example.com:22", 0, "", base.Add(2*time.Minute))
	seedMirrorRec(t, s, "tB", "ex4", MirrorRecHTTP, "GET", "http://other.test/z", 500, "b4", base.Add(3*time.Minute))

	mustRows := func(f TrafficFilter) []TrafficRow {
		t.Helper()
		rows, _, err := s.TrafficRows(ctx, f)
		if err != nil {
			t.Fatalf("TrafficRows 失败: %v", err)
		}
		return rows
	}

	// 全量:倒序(最新在前)。
	rows := mustRows(TrafficFilter{TaskID: "tA"})
	if len(rows) != 3 {
		t.Fatalf("tA 应 3 行,得 %d", len(rows))
	}
	if rows[0].ExchangeID != "ex3" || rows[2].ExchangeID != "ex1" {
		t.Fatalf("倒序错误:%s %s %s", rows[0].ExchangeID, rows[1].ExchangeID, rows[2].ExchangeID)
	}
	// host 派生:http/https 取 URL host,tunnel 取目标 host(剥端口)。
	if rows[2].Host != "a.example.com" || rows[0].Host != "a.example.com" {
		t.Fatalf("host 派生错误:%q %q", rows[2].Host, rows[0].Host)
	}

	// host 子串(跨任务)。
	rows = mustRows(TrafficFilter{Host: "example.com"})
	if len(rows) != 3 {
		t.Fatalf("host=example.com 应 3 行,得 %d", len(rows))
	}

	// 方法 + 状态类 + 类型。
	rows = mustRows(TrafficFilter{Method: "get"})
	if len(rows) != 2 || rows[0].ExchangeID != "ex4" || rows[1].ExchangeID != "ex1" {
		t.Fatalf("method=GET 过滤错误:%d 行", len(rows))
	}
	rows = mustRows(TrafficFilter{StatusClass: 4})
	if len(rows) != 1 || rows[0].ExchangeID != "ex2" {
		t.Fatalf("status=4xx 过滤错误:%d 行", len(rows))
	}
	rows = mustRows(TrafficFilter{Type: MirrorRecTunnel})
	if len(rows) != 1 || rows[0].ExchangeID != "ex3" {
		t.Fatalf("type=tunnel 过滤错误")
	}

	// 时间窗(含边界)。
	rows = mustRows(TrafficFilter{TaskID: "tA", Since: base.Add(time.Minute), Until: base.Add(2 * time.Minute)})
	if len(rows) != 2 || rows[0].ExchangeID != "ex3" || rows[1].ExchangeID != "ex2" {
		t.Fatalf("时间窗过滤错误:%d 行", len(rows))
	}

	// limit + offset 分页。
	rows = mustRows(TrafficFilter{Limit: 1})
	if len(rows) != 1 || rows[0].ExchangeID != "ex4" {
		t.Fatalf("limit=1 应取最新 ex4")
	}
	rows = mustRows(TrafficFilter{Limit: 2, Offset: 1})
	if len(rows) != 2 || rows[0].ExchangeID != "ex3" || rows[1].ExchangeID != "ex2" {
		t.Fatalf("offset=1 分页错误")
	}
}

func TestEvidenceBindingValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "tE"
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created", Title: "绑定校验"}},
		Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
			NodeID: "web1", NodeType: NodeAsset, Title: "http://x", Status: "active", AssetClass: AssetService}},
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "tc-1", NodeType: NodeTestClaim, Title: "声明", Status: TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	seedMirrorRec(t, s, task, "ok-1", MirrorRecHTTP, "GET", "http://x/a", 200, "b1", time.Now())
	seedMirrorRec(t, s, task, "ok-2", MirrorRecHTTPS, "POST", "https://x/b", 200, "b2", time.Now())
	seedMirrorRec(t, s, task, "tun-1", MirrorRecTunnel, "", "x:22", 0, "", time.Now())
	// 另一任务的留痕(只在另一任务存在的交换,跨任务绑定应被拒)。
	seedMirrorRec(t, s, "tOther", "only-other", MirrorRecHTTP, "GET", "http://y/a", 200, "b9", time.Now())

	bind := func(exs ...string) error {
		_, err := s.Append(ctx, task, Event{
			Kind: EventTestClaimRecord, Actor: "creator",
			Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecEvidence, Exchanges: exs},
		})
		return err
	}

	// 合法绑定(http + https)。
	if err := bind("ok-1", "ok-2"); err != nil {
		t.Fatalf("合法绑定应通过: %v", err)
	}
	// 未知交换。
	if err := bind("no-such"); err == nil || !strings.Contains(err.Error(), "不在本任务留痕内") {
		t.Fatalf("未知交换应被拒,得 %v", err)
	}
	// 隧道留痕(无原文)。
	if err := bind("tun-1"); err == nil || !strings.Contains(err.Error(), "无原文的隧道记录") {
		t.Fatalf("隧道绑定应被拒,得 %v", err)
	}
	// 跨任务交换(留痕在别的任务里)。
	if err := bind("only-other"); err == nil || !strings.Contains(err.Error(), "不在本任务留痕内") {
		t.Fatalf("跨任务交换应被拒,得 %v", err)
	}
	// 同批重复。
	if err := bind("ok-1", "ok-1"); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("同批重复应被拒,得 %v", err)
	}
	// 空清单。
	if err := bind(); err == nil {
		t.Fatal("空清单应被拒")
	}
	// 非声明节点。
	_, err := s.Append(ctx, task, Event{
		Kind: EventTestClaimRecord, Actor: "creator",
		Payload: TestClaimRecord{ClaimID: "web1", Type: TestClaimRecEvidence, Exchanges: []string{"ok-1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "不是 testclaim") {
		t.Fatalf("绑到资产节点应被拒,得 %v", err)
	}

	// 补绑成功后:证据版本与绑定清单。
	if err := bind("ok-2"); err != nil {
		t.Fatalf("补绑应通过: %v", err)
	}
	perClaim, total, err := s.EvidenceVersions(ctx, task)
	if err != nil {
		t.Fatalf("EvidenceVersions 失败: %v", err)
	}
	if perClaim["tc-1"] != 2 || total != 2 {
		t.Fatalf("两次绑定后声明/任务证据版本应为 2/2,得 %d/%d", perClaim["tc-1"], total)
	}
	exs, binds, err := s.ClaimEvidence(ctx, task, "tc-1")
	if err != nil || binds != 2 {
		t.Fatalf("ClaimEvidence 失败: %v binds=%d", err, binds)
	}
	if len(exs) != 2 || exs[0] != "ok-1" || exs[1] != "ok-2" {
		t.Fatalf("绑定清单应首绑次序去重: %v", exs)
	}

	// repro 也是证据定义事件:版本推进。
	if _, err := s.Append(ctx, task, Event{
		Kind: EventTestClaimRecord, Actor: "overmind",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecRepro, Severity: "high"},
	}); err != nil {
		t.Fatalf("repro 落账失败: %v", err)
	}
	_, total, _ = s.EvidenceVersions(ctx, task)
	if total != 3 {
		t.Fatalf("repro 应推进任务证据版本到 3,得 %d", total)
	}

	// 全量重放确定性:绑定/存档类事件重放不报错。
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
}

func TestReportRecordValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "tR"
	if _, err := s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{Action: "created", Title: "存档校验"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	// 合法存档登记。
	if _, err := s.Append(ctx, task, Event{
		Kind: EventReportRecord, Actor: "creator",
		Payload: ReportRecord{Type: ReportRecArchived, Format: "md", Ref: "sha256:abc",
			EvidenceVersion: 0, LedgerRev: 1, Size: 120},
	}); err != nil {
		t.Fatalf("合法存档登记应通过: %v", err)
	}
	// 缺原文引用。
	_, err := s.Append(ctx, task, Event{
		Kind: EventReportRecord, Actor: "creator",
		Payload: ReportRecord{Type: ReportRecArchived, Format: "md", Size: 10},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("缺引用应被拒,得 %v", err)
	}
	// 未知类型。
	_, err = s.Append(ctx, task, Event{
		Kind: EventReportRecord, Actor: "creator",
		Payload: ReportRecord{Type: "deleted", Format: "md", Ref: "sha256:x", Size: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown report record type") {
		t.Fatalf("未知类型应被拒,得 %v", err)
	}
}

// TestEvidenceEventPayloadCompat 既有消费者(server 列表/仲裁者)按 Type
// 分支消费 testclaim.record——evidence 记录解码后各字段互不串扰。
func TestEvidenceEventPayloadCompat(t *testing.T) {
	raw := `{"claim_id":"tc-1","type":"evidence","exchanges":["ex-1","ex-2"],"by":"creator"}`
	var r TestClaimRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if r.Type != TestClaimRecEvidence || len(r.Exchanges) != 2 || r.Severity != "" || len(r.Steps) != 0 {
		t.Fatalf("evidence 记录字段串扰: %+v", r)
	}
}

// TestTrafficRowsPagingTotal P3-05c ③ 标准分页:过滤命中总数(total)
// 与页截取(offset+limit)独立正确——翻页可翻完全部,页内行连续不重叠。
func TestTrafficRowsPagingTotal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		seedMirrorRec(t, s, "tP", "ex"+strconv.Itoa(i), MirrorRecHTTP, "GET",
			"http://p.example.com/"+strconv.Itoa(i), 200, "b"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
	}
	// host 过滤只留 7 行中的子集,验证 total 是过滤后计数。
	seedMirrorRec(t, s, "tP", "exOther", MirrorRecHTTP, "GET", "http://other.test/q", 200, "bOther", base)

	// 页 2(每页 3):应 total=7、3 行、与页 1/页 3 不重叠。
	p1, total, err := s.TrafficRows(ctx, TrafficFilter{Host: "p.example", Limit: 3, Offset: 0})
	if err != nil || total != 7 || len(p1) != 3 {
		t.Fatalf("页 1: rows=%d total=%d err=%v", len(p1), total, err)
	}
	p2, total2, err := s.TrafficRows(ctx, TrafficFilter{Host: "p.example", Limit: 3, Offset: 3})
	if err != nil || total2 != 7 || len(p2) != 3 {
		t.Fatalf("页 2: rows=%d total=%d err=%v", len(p2), total2, err)
	}
	p3, total3, err := s.TrafficRows(ctx, TrafficFilter{Host: "p.example", Limit: 3, Offset: 6})
	if err != nil || total3 != 7 || len(p3) != 1 {
		t.Fatalf("页 3(尾页): rows=%d total=%d err=%v", len(p3), total3, err)
	}
	seen := map[string]bool{}
	for _, r := range append(append(p1, p2...), p3...) {
		if seen[r.ExchangeID] {
			t.Fatalf("分页出现重叠: %s", r.ExchangeID)
		}
		seen[r.ExchangeID] = true
	}
	if len(seen) != 7 {
		t.Fatalf("三页拼齐应为全部 7 行,得 %d", len(seen))
	}
	// 倒序连续:页 1 首行最新(ex6),页 3 尾行最旧(ex0)。
	if p1[0].ExchangeID != "ex6" || p3[0].ExchangeID != "ex0" {
		t.Fatalf("页序应最新在前、翻页到最旧: %s…%s", p1[0].ExchangeID, p3[0].ExchangeID)
	}
	// 越界页:空行 + total 仍全量。
	p9, total9, err := s.TrafficRows(ctx, TrafficFilter{Host: "p.example", Limit: 3, Offset: 30})
	if err != nil || len(p9) != 0 || total9 != 7 {
		t.Fatalf("越界页应空行: rows=%d total=%d err=%v", len(p9), total9, err)
	}
}
