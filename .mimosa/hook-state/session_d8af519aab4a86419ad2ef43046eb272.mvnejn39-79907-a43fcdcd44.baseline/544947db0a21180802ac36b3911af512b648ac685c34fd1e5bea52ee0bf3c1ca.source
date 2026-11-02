package overmind

import (
	"context"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- Agent 栏运行参数的全局覆盖(P2-08d):并发上限与心跳间隔 ----

// TestUnitLimitGlobalOverride 并发上限三级优先:装配显式值 > 全局键 >
// 代码默认;脏值 fail-safe 退默认。
func TestUnitLimitGlobalOverride(t *testing.T) {
	store := openLoopStore(t)
	ctx := context.Background()

	// 无全局键:代码默认。
	l := &Loop{Store: store}
	if got := l.unitLimit(); got != DefaultMaxConcurrentUnits {
		t.Fatalf("无覆盖应为默认 %d,实际 %d", DefaultMaxConcurrentUnits, got)
	}

	// 全局键生效:下一轮派发即按新值。
	if err := store.SetSetting(ctx, SettingKeyUnitsLimit, "5", "creator"); err != nil {
		t.Fatalf("写全局并发失败: %v", err)
	}
	if got := l.unitLimit(); got != 5 {
		t.Fatalf("全局并发应为 5,实际 %d", got)
	}

	// 装配显式值优先(演示/测试确定性不受全局值干扰)。
	explicit := &Loop{Store: store, MaxConcurrentUnits: 1}
	if got := explicit.unitLimit(); got != 1 {
		t.Fatalf("装配显式值 1 应优先,实际 %d", got)
	}

	// 脏值:静默退代码默认。
	for _, bad := range []string{"abc", "-2", "0", "  "} {
		if err := store.SetSetting(ctx, SettingKeyUnitsLimit, bad, "creator"); err != nil {
			t.Fatalf("写脏值 %q 失败: %v", bad, err)
		}
	}
	if got := l.unitLimit(); got != DefaultMaxConcurrentUnits {
		t.Fatalf("脏值应退默认 %d,实际 %d", DefaultMaxConcurrentUnits, got)
	}
}

// TestHeartbeatIntervalGlobalOverride 心跳间隔三级优先与 fail-safe(同上)。
func TestHeartbeatIntervalGlobalOverride(t *testing.T) {
	store := openLoopStore(t)
	ctx := context.Background()

	l := &Loop{Store: store}
	if got := l.hbInterval(); got != DefaultHeartbeatInterval {
		t.Fatalf("无覆盖应为默认 %v,实际 %v", DefaultHeartbeatInterval, got)
	}
	if err := store.SetSetting(ctx, SettingKeyHeartbeatSeconds, "30", "creator"); err != nil {
		t.Fatalf("写全局心跳失败: %v", err)
	}
	if got := l.hbInterval(); got != 30*time.Second {
		t.Fatalf("全局心跳应为 30s,实际 %v", got)
	}
	explicit := &Loop{Store: store, HeartbeatInterval: 300 * time.Millisecond}
	if got := explicit.hbInterval(); got != 300*time.Millisecond {
		t.Fatalf("装配显式值应优先,实际 %v", got)
	}
	if err := store.SetSetting(ctx, SettingKeyHeartbeatSeconds, "bad", "creator"); err != nil {
		t.Fatalf("写脏值失败: %v", err)
	}
	if got := l.hbInterval(); got != DefaultHeartbeatInterval {
		t.Fatalf("脏值应退默认 %v,实际 %v", DefaultHeartbeatInterval, got)
	}
}

// TestHeartbeatLoopRereadsInterval 心跳定时器逐拍重布:全局键改值后,
// 下一拍起按新间隔走(存留至多一拍已布防的旧间隔)。1 秒是键的最小
// 粒度,用宽裕的时间窗断言拍数,不卡精确时刻。
func TestHeartbeatLoopRereadsInterval(t *testing.T) {
	store := openLoopStore(t)
	ctx := context.Background()
	const task = "hb-reread"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "心跳重读任务"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	// 键值 1 秒:无装配值,心跳走全局键。
	if err := store.SetSetting(ctx, SettingKeyHeartbeatSeconds, "1", "creator"); err != nil {
		t.Fatalf("写全局心跳失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}
	l.runDone = make(chan struct{})
	defer close(l.runDone)
	loopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.heartbeatLoop(loopCtx)

	// 2.6 秒内至少 2 拍(1 秒一拍)。
	waitFor(t, 5*time.Second, "全局键 1 秒一拍", func() bool {
		return heartbeatCount(blackboxRecords(t, store, task)) >= 2
	})

	// 改键 30 秒:已布防的至多再响一拍,其后 2.5 秒无新拍。
	if err := store.SetSetting(ctx, SettingKeyHeartbeatSeconds, "30", "creator"); err != nil {
		t.Fatalf("改全局心跳失败: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	grew := heartbeatCount(blackboxRecords(t, store, task))
	time.Sleep(1200 * time.Millisecond)
	if n := heartbeatCount(blackboxRecords(t, store, task)); n > grew+0 {
		t.Fatalf("改 30 秒后不应再有新拍(改前 %d,之后 %d)", grew, n)
	}
	// 生效值与最后一拍的间隔口径一致(下一拍将按 30 秒布防)。
	if got := l.hbInterval(); got != 30*time.Second {
		t.Fatalf("改后生效间隔应为 30s,实际 %v", got)
	}
}

// heartbeatCount 黑匣子记录里的巡检拍数。
func heartbeatCount(recs []rings.BlackboxRecord) int {
	n := 0
	for _, r := range recs {
		if r.Type == rings.BlackboxHeartbeat {
			n++
		}
	}
	return n
}
