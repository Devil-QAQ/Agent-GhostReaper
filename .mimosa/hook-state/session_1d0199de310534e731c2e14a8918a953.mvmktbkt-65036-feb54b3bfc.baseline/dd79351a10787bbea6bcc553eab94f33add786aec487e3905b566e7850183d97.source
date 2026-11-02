package swarm

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P4-02 ④ 活动时钟:登记/冻结/注销语义 + 脚本跑全程活体可读 ----

func TestRunClockRegistry(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})

	// 未登记:读不到。
	if _, ok := ex.ClockBillable("t", "swarm-u1"); ok {
		t.Fatal("未登记的单位不应有读数")
	}

	// 活动:读数随时间增长。
	ex.clockSet("t", "swarm-u1", 0, true)
	d1, ok := ex.ClockBillable("t", "swarm-u1")
	if !ok || d1 < 0 {
		t.Fatalf("登记后应有读数: ok=%v d=%v", ok, d1)
	}
	time.Sleep(60 * time.Millisecond)
	d2, _ := ex.ClockBillable("t", "swarm-u1")
	if d2 <= d1 {
		t.Fatalf("活动中读数应增长: %v → %v", d1, d2)
	}

	// 冻结:读数停在已收拢值,不再增长。
	ex.clockSet("t", "swarm-u1", d2, false)
	f1, _ := ex.ClockBillable("t", "swarm-u1")
	time.Sleep(60 * time.Millisecond)
	f2, _ := ex.ClockBillable("t", "swarm-u1")
	if f1 != f2 || f1 < d2 {
		t.Fatalf("冻结后读数应停在已收拢值: f1=%v f2=%v acc=%v", f1, f2, d2)
	}

	// 解冻续走:活动起点重置,读数从冻结值继续增长。
	ex.clockSet("t", "swarm-u1", f2, true)
	time.Sleep(30 * time.Millisecond)
	r1, _ := ex.ClockBillable("t", "swarm-u1")
	if r1 <= f2 {
		t.Fatalf("解冻后应续走: %v → %v", f2, r1)
	}

	// 注销:落地即消失。
	ex.clockDrop("t", "swarm-u1")
	if _, ok := ex.ClockBillable("t", "swarm-u1"); ok {
		t.Fatal("落地后不应再有读数")
	}
}

// TestRunClockDuringRun 脚本跑全程:执行中活体可读且增长,落地即注销;
// 落账 note 的 billable 与活体读数同源(时间账对账)。
func TestRunClockDuringRun(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "clock-task"

	host, port := seedTarget(t, "r\r\n")
	seedTaskGate(t, store, task, 0)
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	var ex *Executor
	var first, second time.Duration
	var hasFirst, hasSecond bool
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		d, ok := ex.ClockBillable(task, "swarm-u1")
		if rr.Index == 0 {
			first, hasFirst = d, ok
			time.Sleep(80 * time.Millisecond) // 活动段内制造可测增长
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "e1", Name: "bash", Args: `{"command":"echo hi"}`,
			}}, Finish: "tool_calls"}
		}
		second, hasSecond = d, ok
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex = NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	res, err := ex.RunStep(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("应完成: %+v", res)
	}
	if res.TranscriptID != "swarm-u1" {
		t.Fatalf("转录锚应为 swarm-u1: %s", res.TranscriptID)
	}
	if !hasFirst || !hasSecond {
		t.Fatalf("执行中应可读到活动时钟: first=%v second=%v", hasFirst, hasSecond)
	}
	if second <= first {
		t.Fatalf("活动段内读数应增长: %v → %v", first, second)
	}
	if _, ok := ex.ClockBillable(task, "swarm-u1"); ok {
		t.Fatal("落地后时钟应注销")
	}
	// 落账对账:note 的 billable ≥ 活体末次读数(最后一段照计入账)。
	var note string
	for _, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionEnd {
			note = rec.Note
		}
	}
	if !strings.Contains(note, "billable=") {
		t.Fatalf("note 应含时间账: %s", note)
	}
	i := strings.Index(note, "billable=")
	v := strings.TrimSuffix(strings.SplitN(note[i+len("billable="):], " ", 2)[0], "s")
	billable, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("解析落账 billable 失败: %q (%v)", v, err)
	}
	// note 落账为 %.1f(既有格式),比较留一位小数的容差。
	if billable+0.05 < second.Seconds() {
		t.Fatalf("落账 billable(%v) 不应小于活体末次读数(%v)", billable, second.Seconds())
	}
}
