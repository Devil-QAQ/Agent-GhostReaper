package rings

// P4-38 数据库暂存保养测试:WAL 超阈值自动回落(真实 100MB 量级,前后
// 对照留证)、journal_size_limit 生效长跑有界、写入吞吐回归无损(单写者
// 队列压测对比)、并发读期间 checkpoint 不阻塞(读超时零出现)。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// setWALKeeper 注入保养参数(空闲窗在写者循环内动态取值,注入随时生效;
// 旧实现在 Open 前注入的要求已随保养并入写者循环而取消)。
func setWALKeeper(t *testing.T, idle time.Duration, threshold int64) {
	t.Helper()
	od, ot := walKeeperIdle, walKeeperThreshold
	walKeeperIdle, walKeeperThreshold = idle, threshold
	t.Cleanup(func() { walKeeperIdle, walKeeperThreshold = od, ot })
}

// walSizeOf 暂存文件当前字节大小(无文件=0)。
func walSizeOf(t *testing.T, dbPath string) int64 {
	t.Helper()
	st, err := os.Stat(dbPath + "-wal")
	if err != nil {
		return 0
	}
	return st.Size()
}

// bulkEvents 构造一批带大 Detail 的节点事件(灌 WAL 用;每事件在
// events+nodes 两表各落一份,单事件 WAL 足迹约 2×payload)。
func bulkEvents(n, payloadKB int, prefix string) []Event {
	blob := strings.Repeat("p438", payloadKB*256) // 4 字节×256×KB = payloadKB KB
	events := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{
			NodeID: fmt.Sprintf("%s-%d", prefix, i), NodeType: NodeFact,
			Title: fmt.Sprintf("%s-%d", prefix, i), Status: "active",
			Detail: map[string]any{"blob": blob}}})
	}
	return events
}

// TestWALShrinkAfterBurstRealScale 完成标准①(真实量级):单事务巨写入把
// WAL 顶过 100MB(写入期间轮询留证)→ 写毕空闲后自动回落到 0(空闲
// TRUNCATE 全量回收)。这是 855MB 场景的同构复现。
func TestWALShrinkAfterBurstRealScale(t *testing.T) {
	setWALKeeper(t, 150*time.Millisecond, 100<<20)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rings.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	defer s.Close()
	const task = "t-p438-burst"
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "created", Title: "WAL 巨写入回落验证"}})

	// 单事务巨写入(~2500 事件×32KB×两表 ≈ 160MB WAL),期间轮询峰值。
	var peak int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if sz := walSizeOf(t, dbPath); sz > peak {
				peak = sz
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	mustAppend(t, s, task, bulkEvents(2500, 32, "burst")...)
	close(stop)
	wg.Wait()
	t.Logf("WAL 峰值 %dMB(%d 字节);阈值 %dMB", peak>>20, peak, walKeeperThreshold>>20)
	if peak < walKeeperThreshold {
		t.Fatalf("写入期间 WAL 应超阈值(观察峰值 %d < %d),数据量不足或观察失效", peak, walKeeperThreshold)
	}
	// 写毕空闲:keeper 在一个巡检周期+空闲窗内全量回收,暂存归 0。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if after := walSizeOf(t, dbPath); after < 1<<20 {
			t.Logf("WAL 回落后 %d 字节(峰值 %dMB → 0)", after, peak>>20)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("写毕空闲后 WAL 应回落: 当前 %dMB", walSizeOf(t, dbPath)>>20)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 数据完好:事件数与节点数对账。
	if n := countRows(t, s, "events"); n != 2501 {
		t.Fatalf("巨写入后事件数应 2501,实际 %d", n)
	}
	if n := countRows(t, s, "nodes"); n != 2500 {
		t.Fatalf("巨写入后节点数应 2500,实际 %d", n)
	}
}

// TestWALBoundedUnderSustainedWrites 完成标准②:journal_size_limit 生效,
// 持续写入长跑 WAL 不再无界(多事务小批连续写,commit 间 checkpoint+
// 尺寸收缩把高水位摁住)。
func TestWALBoundedUnderSustainedWrites(t *testing.T) {
	setWALKeeper(t, 10*time.Second, 100<<20) // 不空闲:批间隔远小于空闲窗,只有写后被动回收一路
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rings.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	defer s.Close()
	const task = "t-p438-sustained"
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "created", Title: "WAL 持续写入有界验证"}})
	var peak int64
	// 30 批×100 事件×32KB(单批 3MB,批间无长停)≈ 100MB 总写入。
	for b := 0; b < 30; b++ {
		mustAppend(t, s, task, bulkEvents(100, 32, fmt.Sprintf("s-%d", b))...)
		if sz := walSizeOf(t, dbPath); sz > peak {
			peak = sz
		}
	}
	t.Logf("持续写 %dMB 期间 WAL 峰值 %dMB", 30*100*32*2/(1<<20), peak>>20)
	if peak >= 64<<20 {
		t.Fatalf("持续写入长跑 WAL 应有界(<64MB),峰值 %dMB", peak>>20)
	}
	// journal_size_limit 连接级生效(Open 随 DSN 下发)。
	var jsl int64
	if err := s.db.QueryRow(`PRAGMA journal_size_limit`).Scan(&jsl); err != nil {
		t.Fatalf("查询 journal_size_limit 失败: %v", err)
	}
	if jsl != walJournalSizeLimit {
		t.Fatalf("journal_size_limit = %d,期望 %d", jsl, walJournalSizeLimit)
	}
}

// TestWALWriteThroughputUnharmed 完成标准③:单写者队列写入吞吐压测对比
// ——保养关(巡检拉满不开)与保养开(高频巡检+空闲回收)各写同样批量,
// 吞吐不应显著劣化(≥60% 宽松下限防环境抖动,实测数字进测试日志)。
func TestWALWriteThroughputUnharmed(t *testing.T) {
	const batches, perBatch, payloadKB = 40, 100, 8 // ≈64MB/轮
	run := func(idle time.Duration) float64 {
		setWALKeeper(t, idle, 100<<20)
		s, err := Open(filepath.Join(t.TempDir(), "rings.db"))
		if err != nil {
			t.Fatalf("打开年轮存储失败: %v", err)
		}
		defer s.Close()
		const task = "t-p438-throughput"
		if _, err := s.Append(context.Background(), task, Event{Kind: EventTaskLifecycle, Actor: "creator",
			Payload: TaskLifecycle{Action: "created", Title: "吞吐对比"}}); err != nil {
			t.Fatalf("种子失败: %v", err)
		}
		start := time.Now()
		for b := 0; b < batches; b++ {
			if _, err := s.Append(context.Background(), task, bulkEvents(perBatch, payloadKB, fmt.Sprintf("tp-%d", b))...); err != nil {
				t.Fatalf("第 %d 批写入失败: %v", b, err)
			}
		}
		return float64(batches*perBatch) / time.Since(start).Seconds()
	}
	baseline := run(time.Hour) // 保养关(空闲窗拉满,测试期内不保养)
	enabled := run(100 * time.Millisecond)
	t.Logf("写入吞吐: 保养关 %.0f 事件/秒,保养开 %.0f 事件/秒(比值 %.2f)", baseline, enabled, enabled/baseline)
	if enabled < baseline*0.6 {
		t.Fatalf("保养开时吞吐不应显著劣化: 关 %.0f/s 开 %.0f/s", baseline, enabled)
	}
}

// TestWALKeeperBranches 保养两分支(经生产路径触发,保养只在写者循环内
// 跑,测试不直调避免与写后保养竞态):写后超阈值走 PASSIVE(尽力回收,
// 不报错不挡后续写);写静默过空闲窗走 TRUNCATE 归零。
func TestWALKeeperBranches(t *testing.T) {
	setWALKeeper(t, 80*time.Millisecond, 1<<20) // 空闲窗短:两分支自然触发
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rings.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	defer s.Close()
	const task = "t-p438-branches"
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "created", Title: "保养分支直测"}})
	// 写后路径(超阈值):批写完成即触发 PASSIVE(此刻空闲计时刚重置,
	// TRUNCATE 不竞入);紧接着持续写不中断——回收不挡写。
	for b := 0; b < 5; b++ {
		mustAppend(t, s, task, bulkEvents(20, 16, fmt.Sprintf("br-%d", b))...) // 每批 ~0.6MB,累计超 1MB 阈值
	}
	if _, err := s.Append(context.Background(), task, bulkEvents(1, 1, "after-passive")...); err != nil {
		t.Fatalf("超阈值回收期间写入应照常: %v", err)
	}
	// 空闲路径:写静默 80ms 后写者自查 TRUNCATE——暂存归零。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if after := walSizeOf(t, dbPath); after < 1<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("空闲 TRUNCATE 后暂存应归零,当前 %d 字节", walSizeOf(t, dbPath))
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 数据完好(共 created+100+1=102 事件)。
	if n := countRows(t, s, "events"); n != 102 {
		t.Fatalf("事件数应 102,实际 %d", n)
	}
}

// TestWALConcurrentReadsDuringCheckpoint 完成标准④:高频空闲回收
// (TRUNCATE checkpoint 密集触发)期间面板并发读——零错误零超时,写路径
// 同步在跑,三方并发互不打架。
func TestWALConcurrentReadsDuringCheckpoint(t *testing.T) {
	setWALKeeper(t, 50*time.Millisecond, 4<<20)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rings.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	defer s.Close()
	const task = "t-p438-concurrent"
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "creator",
		Payload: TaskLifecycle{Action: "created", Title: "并发读期间回收验证"}})
	mustAppend(t, s, task, bulkEvents(200, 4, "seed")...)

	var readErrs, writeErrs int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// 8 路并发读(面板读形态:事件流+配置,每次 2 秒超时,超时即计失败)。
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if _, err := s.Events(ctx, task, 0, 100); err != nil {
					atomic.AddInt32(&readErrs, 1)
					t.Errorf("并发读失败: %v", err)
				}
				cancel()
			}
		}()
	}
	// 1 路背景写(每 150ms 一小批,持续制造新帧与回收对象)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.Append(context.Background(), task, bulkEvents(5, 2, fmt.Sprintf("cw-%d", i))...); err != nil {
				atomic.AddInt32(&writeErrs, 1)
				t.Errorf("背景写失败: %v", err)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()
	// 跑 3 秒(空闲窗 50ms,批写间隔 150ms,期间多轮 TRUNCATE 回收)。
	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
	if c := atomic.LoadInt32(&readErrs); c != 0 {
		t.Fatalf("checkpoint 期间并发读应零失败,实际 %d 次", c)
	}
	if c := atomic.LoadInt32(&writeErrs); c != 0 {
		t.Fatalf("checkpoint 期间背景写应零失败,实际 %d 次", c)
	}
}
