package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 报告存档(P3-02,第五章 × 流量证据绑定三机制):报告 md 仍是确定性
// 渲染(R17,零 LLM),存档 = 把某一账本状态的渲染原文固化——原文入
// 内容寻址存储(R18),登记入账本(report.record 事件,审计流)。三
// 机制在此落点:
//   - evidence_version 乐观锁:存档写入必须携带客户端所见的任务证据
//     版本,与当前不符即拒(ErrEvidenceStale,写入口以 409 呈现)——
//     证据已更新后的旧版本写入不落库;
//   - 补绑自动标记待更新:存档记录携带写入时的证据版本,证据版本由
//     事件流实时推导,存档版本落后当前即"待更新"(Stale),无需改档。

// ErrEvidenceStale 证据版本冲突(乐观锁拒绝):提交版本 ≠ 当前版本。
// Current 为当前任务证据版本,Submitted 为提交携带的版本,409 呈现用。
type ErrEvidenceStale struct {
	Current   int64
	Submitted int64
}

func (e *ErrEvidenceStale) Error() string {
	return fmt.Sprintf("证据已更新:当前证据版本 %d,提交 %d,刷新后再存档", e.Current, e.Submitted)
}

// ArchiveView 存档的读模型(面板存档清单与单条存档共用)。
type ArchiveView struct {
	Rev             int64     `json:"rev"`              // 登记事件的账本版本号(存档的稳定标识)
	At              time.Time `json:"at"`               // 存档时刻(事件 occurred_at)
	Format          string    `json:"format"`           // 存档格式(md)
	Ref             string    `json:"ref"`              // 原文的内容寻址引用(R18)
	EvidenceVersion int64     `json:"evidence_version"` // 写入时的任务证据版本
	LedgerRev       int64     `json:"ledger_rev"`       // 渲染所读账本版本号
	ScopeCount      int       `json:"scope_count"`      // 存档范围发现数(0=全部)
	Size            int64     `json:"size"`             // 原文字节数
	By              string    `json:"by,omitempty"`
	Stale           bool      `json:"stale"` // 待更新:证据版本已落后当前(绑定/定稿推进过)
}

// ArchiveTask 存档一次任务报告:证据版本比对 → 渲染全文 → 原文入 blob
// → 登记入账本。scope 固定全部发现(存档是任务级快照;筛选导出走既有
// 导出弹窗,不落档)。比对做两道:入口一道(客户端所见版本),落账前
// 一道(渲染期间的证据更新也拒)——两道之间落账的更新会让存档登记为
// 待更新(推导如实),不会静默陈旧。
func ArchiveTask(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string, evidenceVersion int64, by string) (*ArchiveView, error) {
	if taskID == "" {
		return nil, fmt.Errorf("report: 任务 ID 为空")
	}
	if _, current, err := store.EvidenceVersions(ctx, taskID); err != nil {
		return nil, err
	} else if current != evidenceVersion {
		return nil, &ErrEvidenceStale{Current: current, Submitted: evidenceVersion}
	}
	task, err := findTask(ctx, store, taskID)
	if err != nil {
		return nil, err
	}
	md, err := Render(ctx, store, blobs, taskID, nil)
	if err != nil {
		return nil, err
	}
	// 落账前复检:渲染期间的证据更新同样拒(存档必须锚定提交版本)。
	if _, current, err := store.EvidenceVersions(ctx, taskID); err != nil {
		return nil, err
	} else if current != evidenceVersion {
		return nil, &ErrEvidenceStale{Current: current, Submitted: evidenceVersion}
	}
	ref, err := blobs.Write([]byte(md))
	if err != nil {
		return nil, fmt.Errorf("report: 报告原文入原始数据: %w", err)
	}
	rec := rings.ReportRecord{
		Type: rings.ReportRecArchived, Format: FormatMD, Ref: ref,
		EvidenceVersion: evidenceVersion, LedgerRev: task.UpdatedRev,
		Size: int64(len(md)), By: by,
	}
	revs, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventReportRecord, Actor: by, Payload: rec,
	})
	if err != nil {
		return nil, fmt.Errorf("report: 存档登记入账: %w", err)
	}
	return &ArchiveView{
		Rev: revs[0], At: time.Now().UTC(), Format: rec.Format, Ref: ref,
		EvidenceVersion: rec.EvidenceVersion, LedgerRev: rec.LedgerRev,
		Size: rec.Size, By: by,
	}, nil
}

// Archives 存档清单(事件流倒序,最新在前)+ 当前任务证据版本。
// Stale 由版本差推导:证据在存档之后更新过(绑定/补绑/收割定稿)即
// 待更新——补绑自动标记的机制落点,无人工改档动作。
func Archives(ctx context.Context, store *rings.Store, taskID string) ([]ArchiveView, int64, error) {
	if taskID == "" {
		return nil, 0, fmt.Errorf("report: 任务 ID 为空")
	}
	_, current, err := store.EvidenceVersions(ctx, taskID)
	if err != nil {
		return nil, 0, err
	}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("report: 存档查事件流: %w", err)
	}
	var out []ArchiveView
	for _, ev := range evs {
		if ev.Kind != rings.EventReportRecord {
			continue
		}
		var r rings.ReportRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, 0, fmt.Errorf("report: 解码 report.record: %w", err)
		}
		out = append(out, ArchiveView{
			Rev: ev.Revision, At: ev.OccurredAt, Format: r.Format, Ref: r.Ref,
			EvidenceVersion: r.EvidenceVersion, LedgerRev: r.LedgerRev,
			ScopeCount: len(r.ClaimsScope), Size: r.Size, By: r.By,
			Stale: r.EvidenceVersion < current,
		})
	}
	// 倒序:最新在前。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, current, nil
}

// ArchiveByRev 取单条存档的登记(存在性检查用,下载入口先经此定位 Ref)。
func ArchiveByRev(ctx context.Context, store *rings.Store, taskID string, rev int64) (*rings.ReportRecord, error) {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("report: 存档定位查事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventReportRecord || ev.Revision != rev {
			continue
		}
		var r rings.ReportRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, fmt.Errorf("report: 解码 report.record: %w", err)
		}
		return &r, nil
	}
	return nil, errors.New("report: 该版本号无报告存档")
}

// ArchiveBody 读存档原文(blob 回放,字节级)。
func ArchiveBody(blobs *swarm.BlobStore, ref string) ([]byte, error) {
	if blobs == nil {
		return nil, errors.New("report: 无原始数据存储可用")
	}
	return blobs.Read(ref)
}
