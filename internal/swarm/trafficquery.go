package swarm

// 流量回查的共享查询面(P5-04):镜留痕定位与原文读取的单一实现——
// 面板流量页、虫群与牧者的流量查询工具走同一份(展示与操作同源,禁
// 平行实现)。全部复用既有投影查询:rings.EventsByKinds(按事件种类
// 定向读)、rings.TrafficRows(流量列表)、BlobStore.Read(原文惰性读),
// 零新扫描路径。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// MirrorExchange 定位任务内一条镜留痕:账本摘要 + blob 原文文档
// (http/https;tunnel 无原文)。交换 ID 任务内定位(会话前缀随机,
// 跨任务理论可撞,任务内唯一)。P4-58:按 kind 定向直查 mirror.record,
// 不全量扫事件流。P4-62 C(b):镜事件流查不到时回退查组包复现步骤的
// 交换文档(工具调用记录重构,"ta-" 前缀),与镜交换同一读取通道。
func MirrorExchange(ctx context.Context, store *rings.Store, blobs *BlobStore, taskID, exID string) (*rings.MirrorRecord, *mirror.Exchange, time.Time, error) {
	evs, err := store.EventsByKinds(ctx, taskID, rings.EventMirrorRecord)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	var rec *rings.MirrorRecord
	var at time.Time
	for i := range evs {
		if evs[i].Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if json.Unmarshal(evs[i].Payload, &m) != nil || m.ExchangeID != exID {
			continue
		}
		rec, at = &m, evs[i].OccurredAt
		break
	}
	if rec == nil {
		if st, ok := ReproToolStep(ctx, store, blobs, taskID, exID); ok {
			return st.Record(), st.Exchange(blobs), st.At(), nil
		}
		return nil, nil, time.Time{}, fmt.Errorf("流量记录 %s 不在任务 %s 留痕内", exID, taskID)
	}
	var ex mirror.Exchange
	if rec.BlobRef != "" {
		if blobs == nil {
			return rec, nil, at, fmt.Errorf("读交换原文 %s: 无原文存储可用", rec.BlobRef)
		}
		doc, err := blobs.Read(rec.BlobRef)
		if err != nil {
			return rec, nil, at, fmt.Errorf("读交换原文 %s: %w", rec.BlobRef, err)
		}
		if err := json.Unmarshal(doc, &ex); err != nil {
			return rec, nil, at, fmt.Errorf("解码交换原文 %s: %w", rec.BlobRef, err)
		}
	}
	return rec, &ex, at, nil
}

// ReproToolStep 定稿步骤里的组包交换(P4-62 C(b)):扫任务的 repro
// 记录,按 ExchangeID 命中 source=tool 的步骤(P4-35 后记录 KB 级,
// 定稿记录有限,定向读不触全量事件)。读不到返回 ok=false(按不存在
// 处理,调用方如实报查无)。
func ReproToolStep(ctx context.Context, store *rings.Store, blobs *BlobStore, taskID, exID string) (*ReproToolStepRef, bool) {
	evs, err := store.EventsByKinds(ctx, taskID, rings.EventTestClaimRecord)
	if err != nil {
		return nil, false
	}
	for i := range evs {
		var r rings.TestClaimRecord
		if json.Unmarshal(evs[i].Payload, &r) != nil || r.Type != rings.TestClaimRecRepro {
			continue
		}
		for _, st := range r.Steps {
			if st.ExchangeID == exID && st.Source == "tool" {
				return &ReproToolStepRef{step: st, at: evs[i].OccurredAt}, true
			}
		}
	}
	return nil, false
}

// ReproToolStepRef 组包步骤的回放引用。
type ReproToolStepRef struct {
	step rings.ReproStep
	at   time.Time
}

// At 组包交换的留痕时刻。
func (ref *ReproToolStepRef) At() time.Time { return ref.at }

// Record 镜摘要形态(元数据条口径;Type http——组包文档本身是 http
// 请求包形态,与镜交换同渲染)。
func (ref *ReproToolStepRef) Record() *rings.MirrorRecord {
	return &rings.MirrorRecord{
		ExchangeID: ref.step.ExchangeID, Type: rings.MirrorRecHTTP,
		StepID: ref.step.StepID, UnitID: ref.step.UnitID,
		Method: ref.step.Method, URL: ref.step.URL, Status: ref.step.Status,
		BlobRef: ref.step.BlobRef,
	}
}

// Exchange blob 交换文档(读不到如实 nil:调用方按无原文处理)。
func (ref *ReproToolStepRef) Exchange(blobs *BlobStore) *mirror.Exchange {
	if blobs == nil || ref.step.BlobRef == "" {
		return nil
	}
	doc, err := blobs.Read(ref.step.BlobRef)
	if err != nil {
		return nil
	}
	var ex mirror.Exchange
	if json.Unmarshal(doc, &ex) != nil {
		return nil
	}
	return &ex
}
