package rings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 流量页与证据版本的读侧查询(P3-02,8.3 一级·流量 + 第五章流量证据
// 绑定三机制):全部从事件流确定性推导,零新增采集、零新投影表。

// TrafficFilter 流量页列表过滤(P3-02):任务/host 子串/方法/状态类
// (2xx~5xx)/类型/时间窗,以及分页。零值字段 = 不过滤。
type TrafficFilter struct {
	TaskID      string    // 空 = 全部任务(Manager 视角的合法跨任务读,同 Tasks)
	Host        string    // host 子串(大小写不敏感;host 从留痕 URL 解析)
	Method      string    // GET/POST/…(精确等值)
	StatusClass int       // 0=全部;2/3/4/5 → 2xx/3xx/4xx/5xx
	Type        string    // 空=全部;http|https|tunnel
	Since       time.Time // 含下界(零值 = 不限)
	Until       time.Time // 含上界(零值 = 不限)
	Limit       int       // ≤0 取默认;过滤后截取
	Offset      int       // 过滤后偏移(与 Limit 配合分页)
}

// TrafficFilterLimitDefault / Max 流量页分页口径(单机面板量级)。
const (
	TrafficFilterLimitDefault = 200
	TrafficFilterLimitMax     = 1000
)

// TrafficRow 流量页列表行:镜留痕摘要(MirrorRecord 字段 + host 派生 +
// 归属任务),不含原文——原文在 blob,详情按交换 ID 惰性回放(R18)。
type TrafficRow struct {
	TaskID       string
	ExchangeID   string
	Type         string
	StepID       string
	UnitID       string
	TranscriptID string
	Method       string
	URL          string
	Host         string // 从 URL 解析的 host(tunnel 为 CONNECT 目标 host)
	Status       int
	ReqBytes     int64
	RespBytes    int64
	BlobRef      string
	ElapsedMS    int64
	Error        string
	TLS          string
	OccurredAt   time.Time
}

// TrafficRows 流量页列表查询:事件流按写入序倒扫(最新在前),过滤后
// 截取 offset+limit 行,并返回过滤命中的总数(P3-05c ③ 标准分页:页码
// 跳转需要总页数 = ceil(total/page_size))。host 在 Go 侧解析(URL 结构
// 在 payload 里,SQL 抽不出可靠的 host 语义);其余过滤同样在 Go 侧逐行
// 判定——单写者排队下事件表只增,读为快照语义,量级为单机面板,全扫
// 可接受。
func (s *Store) TrafficRows(ctx context.Context, f TrafficFilter) ([]TrafficRow, int, error) {
	q := `SELECT task_id, occurred_at, payload FROM events WHERE kind = ?`
	args := []any{EventMirrorRecord}
	if f.TaskID != "" {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
	}
	q += ` ORDER BY seq DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("rings: 查询镜留痕: %w", err)
	}
	defer rows.Close()

	limit := f.Limit
	if limit <= 0 {
		limit = TrafficFilterLimitDefault
	}
	if limit > TrafficFilterLimitMax {
		limit = TrafficFilterLimitMax
	}
	hostWant := strings.ToLower(strings.TrimSpace(f.Host))
	methodWant := strings.ToUpper(strings.TrimSpace(f.Method))
	need := f.Offset + limit
	var out []TrafficRow
	total := 0
	for rows.Next() {
		var taskID, payload, occurred string
		if err := rows.Scan(&taskID, &occurred, &payload); err != nil {
			return nil, 0, fmt.Errorf("rings: 扫描镜留痕: %w", err)
		}
		var rec MirrorRecord
		if err := json.Unmarshal([]byte(payload), &rec); err != nil {
			return nil, 0, fmt.Errorf("rings: 解码 mirror.record: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, 0, fmt.Errorf("rings: 解析 occurred_at: %w", err)
		}
		if !f.match(&rec, at, hostWant, methodWant) {
			continue
		}
		total++
		if len(out) >= need {
			continue // 继续扫只为计总数,不再收集
		}
		out = append(out, TrafficRow{
			TaskID: taskID, ExchangeID: rec.ExchangeID, Type: rec.Type,
			StepID: rec.StepID, UnitID: rec.UnitID, TranscriptID: rec.TranscriptID,
			Method: rec.Method, URL: rec.URL, Host: hostOf(&rec),
			Status: rec.Status, ReqBytes: rec.ReqBytes, RespBytes: rec.RespBytes,
			BlobRef: rec.BlobRef, ElapsedMS: rec.ElapsedMS, Error: rec.Error,
			TLS: rec.TLS, OccurredAt: at,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rings: 遍历镜留痕: %w", err)
	}
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []TrafficRow{}, total, nil
		}
		out = out[f.Offset:]
	}
	return out, total, nil
}

// match 单行过滤判定。
func (f *TrafficFilter) match(rec *MirrorRecord, at time.Time, hostWant, methodWant string) bool {
	if f.Type != "" && rec.Type != f.Type {
		return false
	}
	if methodWant != "" && rec.Method != methodWant {
		return false
	}
	if f.StatusClass != 0 && (rec.Status < f.StatusClass*100 || rec.Status >= (f.StatusClass+1)*100) {
		return false
	}
	if hostWant != "" && !strings.Contains(strings.ToLower(hostOf(rec)), hostWant) {
		return false
	}
	if !f.Since.IsZero() && at.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && at.After(f.Until) {
		return false
	}
	return true
}

// hostOf 从留痕摘要解析 host:http/https 的 URL 是绝对形式代理 URL
// (scheme://host/path),tunnel 的 URL 是 CONNECT 目标(host:port)。
// 解析失败退空串(过滤按不命中处理,如实)。
func hostOf(rec *MirrorRecord) string {
	if rec.URL == "" {
		return ""
	}
	if u, err := url.Parse(rec.URL); err == nil && u.Host != "" {
		return u.Host
	}
	// tunnel 形态 host:port(url.Parse 对裸 host:port 会把 host 当 scheme,
	// Host 为空)——手工剥端口。
	if i := strings.LastIndex(rec.URL, ":"); i > 0 {
		return rec.URL[:i]
	}
	return rec.URL
}

// EvidenceVersions 任务证据版本(P3-02 绑定三机制的版本锚点):证据
// 定义事件——testclaim.record 的 evidence(绑定/补绑)与 repro(收割
// 定稿)——按声明累计计数;任务级总数 = 各声明之和。报告存档写入携
// 带当时的任务证据版本(乐观锁),证据再更新后:旧版本写入被写入口
// 拒(409),已存档的报告推导为"待更新"。
func (s *Store) EvidenceVersions(ctx context.Context, taskID string) (map[string]int64, int64, error) {
	if taskID == "" {
		return nil, 0, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("rings: 证据版本查事件 %s: %w", taskID, err)
	}
	perClaim := map[string]int64{}
	var total int64
	for _, ev := range evs {
		if ev.Kind != EventTestClaimRecord {
			continue
		}
		var r TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, 0, fmt.Errorf("rings: 证据版本解码 testclaim.record: %w", err)
		}
		switch r.Type {
		case TestClaimRecEvidence, TestClaimRecRepro:
			perClaim[r.ClaimID]++
			total++
		}
	}
	return perClaim, total, nil
}

// ClaimEvidence 声明的流量证据装配(P3-02):全部 evidence 绑定事件按
// 事件序展开为交换清单(同交换多次绑定的重复如实保留次序,读取侧再
// 去重呈现)。返回值 = 去重后的绑定交换 ID(首绑次序)+ 绑定事件
// 计数(声明证据版本的分项口径)。
func (s *Store) ClaimEvidence(ctx context.Context, taskID, claimID string) (exchanges []string, binds int, err error) {
	if taskID == "" {
		return nil, 0, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("rings: 判断证据查事件 %s: %w", taskID, err)
	}
	seen := map[string]bool{}
	for _, ev := range evs {
		if ev.Kind != EventTestClaimRecord {
			continue
		}
		var r TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, 0, fmt.Errorf("rings: 判断证据解码 testclaim.record: %w", err)
		}
		if r.ClaimID != claimID || r.Type != TestClaimRecEvidence {
			continue
		}
		binds++
		for _, ex := range r.Exchanges {
			if !seen[ex] {
				seen[ex] = true
				exchanges = append(exchanges, ex)
			}
		}
	}
	return exchanges, binds, nil
}
