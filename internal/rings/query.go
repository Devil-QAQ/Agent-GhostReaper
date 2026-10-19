package rings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RawEvent 账本事件原始行,负载保持原始 JSON,调用方按 Kind 自行解码。
type RawEvent struct {
	Seq        int64
	TaskID     string
	Revision   int64
	Kind       EventKind
	Actor      string
	Payload    json.RawMessage
	OccurredAt time.Time
	RecordedAt time.Time
}

// Events 按任务列事件(revision 升序)。sinceRevision 为排他下界
// (返回 revision > sinceRevision),limit <= 0 表示不限。
// task_id 隔离靠查询条件本身(第七章 §3)。
func (s *Store) Events(ctx context.Context, taskID string, sinceRevision int64, limit int) ([]RawEvent, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	q := `SELECT seq, task_id, revision, kind, actor, payload, occurred_at, recorded_at
	      FROM events WHERE task_id = ? AND revision > ?`
	args := []any{taskID, sinceRevision}
	if limit > 0 {
		q += ` ORDER BY revision LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY revision`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 事件: %w", taskID, err)
	}
	defer rows.Close()
	var out []RawEvent
	for rows.Next() {
		var e RawEvent
		var kind string
		var occurred, recorded string
		if err := rows.Scan(&e.Seq, &e.TaskID, &e.Revision, &kind, &e.Actor, &e.Payload, &occurred, &recorded); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 事件: %w", taskID, err)
		}
		e.Kind = EventKind(kind)
		if e.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
			return nil, fmt.Errorf("rings: 解析 occurred_at: %w", err)
		}
		if e.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, fmt.Errorf("rings: 解析 recorded_at: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CurrentRevision 返回任务当前最大 revision(无事件为 0)。
// 即任务当前的图版本:虫群领任务包时绑定、执行前复检用(R14,P1-04 起)。
func (s *Store) CurrentRevision(ctx context.Context, taskID string) (int64, error) {
	if taskID == "" {
		return 0, ErrEmptyTaskID
	}
	var rev int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM events WHERE task_id = ?`, taskID,
	).Scan(&rev); err != nil {
		return 0, fmt.Errorf("rings: 查询任务 %s 当前 revision: %w", taskID, err)
	}
	return rev, nil
}

// TaskRow 任务生命周期投影行(P1-04,第七章)。Category/Pinned/Deleted
// 为元数据列(P2-06h,task.meta 事件维护):分类指派、置顶(含次序锚
// PinnedRev)、墓碑标记——生命周期事件不触碰这些列,UpdatedRev 仍锚
// 最后一条生命周期事件(FIFO 重准入次序锚,不受元数据操作扰动)。
type TaskRow struct {
	ID         string
	Title      string
	Status     string // created|queued|started|paused|resumed|stopped|completed|settling
	Detail     map[string]any
	Category   string // 分类 ID(空 = 未分类)
	Pinned     bool   // 置顶
	PinnedRev  int64  // 置顶动作的 revision(置顶任务间的次序锚)
	Deleted    bool   // 墓碑标记(面板列表隐藏,账本历史保留)
	CreatedRev int64
	UpdatedRev int64
}

// taskOrderBy 任务列表排序(P2-06h):置顶任务在前(按置顶动作先后),
// 其余按创建次序。created_rev 是任务内 revision(每任务首事件恒为 1,
// 不能当全局创建序),真实创建序取该任务在事件表的全局最小 seq
// (首事件即 created,事件表只追加,seq 单调)。
const taskOrderBy = ` ORDER BY pinned DESC, pinned_rev ASC, created_rev ASC, task_id ASC`

// Tasks 列出全部任务(置顶在前,见 taskOrderBy;含墓碑任务,面板侧
// 自行过滤)。task_id 隔离靠查询本身,此处跨任务列举是 Manager 视角
// 的合法读。
func (s *Store) Tasks(ctx context.Context) ([]TaskRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, title, status, detail, category, pinned, pinned_rev, deleted, created_rev, updated_rev
		FROM tasks`+taskOrderBy)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务列表: %w", err)
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		var detail string
		var pinned, deleted int
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &detail, &t.Category, &pinned, &t.PinnedRev, &deleted, &t.CreatedRev, &t.UpdatedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务列表: %w", err)
		}
		t.Pinned, t.Deleted = pinned != 0, deleted != 0
		if err := json.Unmarshal([]byte(detail), &t.Detail); err != nil {
			return nil, fmt.Errorf("rings: 解码任务 %s detail: %w", t.ID, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MaxUnitSeq 全账本虫群记录里出现过的最大单位序号(uN 的 N)。
// 执行器重启后据此把单位号从账本最大值之后继续——否则从 u1 重新
// 计数会与历史单位撞号,并把两个单位的会话写进同一个 transcript
// 文件(归因锚与回放语境被污染,P2-14 实跑发现)。无虫群记录时返回 0。
func (s *Store) MaxUnitSeq(ctx context.Context) (int, error) {
	var m sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(CAST(SUBSTR(json_extract(payload, '$.unit_id'), 2) AS INTEGER))
		FROM events
		WHERE kind = ? AND json_extract(payload, '$.unit_id') LIKE 'u%'`,
		EventSwarmRecord).Scan(&m)
	if err != nil {
		return 0, fmt.Errorf("rings: 查询最大单位序号: %w", err)
	}
	if !m.Valid || m.Int64 < 0 {
		return 0, nil
	}
	return int(m.Int64), nil
}

// Task 取单个任务投影行(不存在报 ErrTaskNotFound)。任务级总闸
// (整体超时,P2-04)经 detail 透出,主宰循环与虫群领取据此推导收尾线。
func (s *Store) Task(ctx context.Context, taskID string) (*TaskRow, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	var t TaskRow
	var detail string
	var pinned, deleted int
	err := s.db.QueryRowContext(ctx, `
		SELECT task_id, title, status, detail, category, pinned, pinned_rev, deleted, created_rev, updated_rev
		FROM tasks WHERE task_id = ?`, taskID,
	).Scan(&t.ID, &t.Title, &t.Status, &detail, &t.Category, &pinned, &t.PinnedRev, &deleted, &t.CreatedRev, &t.UpdatedRev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: task %s", ErrTaskNotFound, taskID)
	}
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s: %w", taskID, err)
	}
	t.Pinned, t.Deleted = pinned != 0, deleted != 0
	if err := json.Unmarshal([]byte(detail), &t.Detail); err != nil {
		return nil, fmt.Errorf("rings: 解码任务 %s detail: %w", taskID, err)
	}
	return &t, nil
}

// TaskGate 任务级总闸锚点(P2-04,第四章 §6):从事件流读 created 事件的
// 整体超时(timeout_seconds)与发生时刻。事件流不可变——锚点不受后续
// 生命周期事件覆盖 tasks 投影 detail 的影响(暂停/收养等自带 detail 的
// 事件会整体替换投影 detail,收尾线必须钉死在事件流上)。created 恒为
// 任务首条事件(CreateTask 单批记账保证);无超时返回 0。
func (s *Store) TaskGate(ctx context.Context, taskID string) (timeoutSeconds int, createdAt time.Time, err error) {
	if taskID == "" {
		return 0, time.Time{}, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 1)
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(evs) == 0 || evs[0].Kind != EventTaskLifecycle {
		return 0, time.Time{}, nil
	}
	var p TaskLifecycle
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		return 0, time.Time{}, fmt.Errorf("rings: 解码任务 %s created 负载: %w", taskID, err)
	}
	if p.Action != "created" {
		return 0, time.Time{}, nil
	}
	secs, _ := p.Detail["timeout_seconds"].(float64)
	return int(secs), evs[0].OccurredAt, nil
}

// CreatedDetail 从事件流读 created 事件的 detail(任务级选项的事件流
// 锚点,同 TaskGate 纪律:不受后续生命周期事件覆盖 tasks 投影 detail 的
// 影响)。P2-05 起重锚定间隔(anchor_every_steps)在此透出;created 恒为
// 任务首条事件。无 detail 返回 nil。
func (s *Store) CreatedDetail(ctx context.Context, taskID string) (map[string]any, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 || evs[0].Kind != EventTaskLifecycle {
		return nil, nil
	}
	var p TaskLifecycle
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		return nil, fmt.Errorf("rings: 解码任务 %s created 负载: %w", taskID, err)
	}
	if p.Action != "created" {
		return nil, nil
	}
	return p.Detail, nil
}

// TokenUsageRows 按任务列 token 用量行(seq 升序 = 记账顺序)。
// 仪表盘与对账(P2-04 四类记录分工)的读侧入口。
func (s *Store) TokenUsageRows(ctx context.Context, taskID string) ([]TokenUsage, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, agent, model, transcript_id, turn,
		       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, occurred_at
		FROM token_usage WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s token 用量: %w", taskID, err)
	}
	defer rows.Close()
	var out []TokenUsage
	for rows.Next() {
		var r TokenUsage
		var occurred string
		if err := rows.Scan(&r.TaskID, &r.Agent, &r.Model, &r.TranscriptID, &r.Turn,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s token 用量: %w", taskID, err)
		}
		if r.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
			return nil, fmt.Errorf("rings: 解析 token 用量 occurred_at: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AllTokenUsageRows 列全部任务的 token 用量行(occurred_at 升序,同刻
// 按 seq)。P2-12 观测页全局仪表盘的读侧入口——跨任务汇总是 Manager
// 视角的合法读(同 Tasks),对账以本表为唯一真值。
func (s *Store) AllTokenUsageRows(ctx context.Context) ([]TokenUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, agent, model, transcript_id, turn,
		       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, occurred_at
		FROM token_usage ORDER BY occurred_at, seq`)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询全量 token 用量: %w", err)
	}
	defer rows.Close()
	var out []TokenUsage
	for rows.Next() {
		var r TokenUsage
		var occurred string
		if err := rows.Scan(&r.TaskID, &r.Agent, &r.Model, &r.TranscriptID, &r.Turn,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &occurred); err != nil {
			return nil, fmt.Errorf("rings: 扫描全量 token 用量: %w", err)
		}
		if r.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
			return nil, fmt.Errorf("rings: 解析 token 用量 occurred_at: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoeState RoE 当前值投影(P1-04,第四章 §4):虫群可用工具边界 +
// 审批门槛(P2-06b 三级处置 v2)。ApprovalThreshold 空值由消费侧归一为
// 默认宽(仅增删改弹门);MaxActionLevel(P1-07 分级上限)已废止,读兼容。
type RoeState struct {
	AllowedTools      []string
	DeniedTools       []string
	ApprovalThreshold ActionLevel
	IntentRules       []IntentRule // 任务级拦截规则(P2-08e)
	ExcludedAssets    []string     // 授权范围摘除清单(P4-05;空=全部纳入)
	MaxActionLevel    ActionLevel  // 已废止(P1-07 分级上限,读兼容)
	Note              string
	UpdatedRev        int64
}

// ExcludedAssetSet 摘除清单的集合形态(消费侧判定用)。
func (r RoeState) ExcludedAssetSet() map[string]bool {
	if len(r.ExcludedAssets) == 0 {
		return nil
	}
	m := make(map[string]bool, len(r.ExcludedAssets))
	for _, id := range r.ExcludedAssets {
		m[id] = true
	}
	return m
}

// ExcludedAssetView 摘除资产的呈现形态(ID + 标题,供约束块渲染与面板
// 读面共用;标题查不到时原样给 ID,历史资产被清场也能读)。
type ExcludedAssetView struct {
	ID    string
	Title string
}

// ExcludedAssets 当前摘除清单的资产明细(题名解析)。清单里的 ID 已不在
// 图上(不可能出现:无删除资产端点;防御性跳过)如实剔除,不报错——
// 约束块与面板读的是"现在摘除了什么"。
func (s *Store) ExcludedAssets(ctx context.Context, taskID string) ([]ExcludedAssetView, error) {
	roe, err := s.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(roe.ExcludedAssets) == 0 {
		return nil, nil
	}
	out := make([]ExcludedAssetView, 0, len(roe.ExcludedAssets))
	for _, id := range roe.ExcludedAssets {
		v := ExcludedAssetView{ID: id, Title: id}
		if n, err := s.Node(ctx, taskID, id); err == nil && n.Type == NodeAsset {
			v.Title = n.Title
		}
		out = append(out, v)
	}
	return out, nil
}

// CurrentRoE 返回任务当前 RoE。无 roe.updated 事件时返回零值(工具全集
// 不设白名单、无黑名单——边界从宽起步,收紧靠记账事件;审批门槛归一为
// 默认宽:仅增删改弹门)。
func (s *Store) CurrentRoE(ctx context.Context, taskID string) (RoeState, error) {
	if taskID == "" {
		return RoeState{}, ErrEmptyTaskID
	}
	var r RoeState
	var allowed, denied, maxLevel, threshold, rules, exAssets string
	err := s.db.QueryRowContext(ctx, `
		SELECT allowed_tools, denied_tools, max_action_level, approval_threshold, intent_rules, excluded_assets, note, updated_rev
		FROM roe_state WHERE task_id = ?`, taskID,
	).Scan(&allowed, &denied, &maxLevel, &threshold, &rules, &exAssets, &r.Note, &r.UpdatedRev)
	if errors.Is(err, sql.ErrNoRows) {
		return RoeState{}, nil
	}
	if err != nil {
		return RoeState{}, fmt.Errorf("rings: 查询任务 %s RoE: %w", taskID, err)
	}
	if err := json.Unmarshal([]byte(allowed), &r.AllowedTools); err != nil {
		return RoeState{}, fmt.Errorf("rings: 解码任务 %s allowed_tools: %w", taskID, err)
	}
	if err := json.Unmarshal([]byte(denied), &r.DeniedTools); err != nil {
		return RoeState{}, fmt.Errorf("rings: 解码任务 %s denied_tools: %w", taskID, err)
	}
	if err := json.Unmarshal([]byte(rules), &r.IntentRules); err != nil {
		return RoeState{}, fmt.Errorf("rings: 解码任务 %s intent_rules: %w", taskID, err)
	}
	if err := json.Unmarshal([]byte(exAssets), &r.ExcludedAssets); err != nil {
		return RoeState{}, fmt.Errorf("rings: 解码任务 %s excluded_assets: %w", taskID, err)
	}
	r.MaxActionLevel = ActionLevel(maxLevel)
	r.ApprovalThreshold = ActionLevel(threshold)
	return r, nil
}

// NodeView 节点在报表中的投影。
type NodeView struct {
	ID         string
	Type       NodeType
	Title      string
	Status     string
	AssetClass AssetClass
	CreatedRev int64
	UpdatedRev int64
}

// EdgeView 边在报表中的投影。
type EdgeView struct {
	Type       EdgeType
	From       string
	To         string
	CreatedRev int64
}

// AssetNodeView 资产图节点:层级由 sub_of 边表达(R5);
// Covered 表示是否有 Step─covers→该资产(覆盖查询"哪些资产还没测过");
// FactCount/HypothesisCount 为挂在该资产上的事实数与假设数(about 边按来源类型计);
// Source 为来源三态(seed/manual/discovered,Detail 归一,P4-05)。
type AssetNodeView struct {
	NodeView
	Covered         bool
	FactCount       int
	HypothesisCount int
	Source          string
}

// AssetGraphView 资产图最小投影:目标环境真值库的读模型。
type AssetGraphView struct {
	Assets    []AssetNodeView
	Hierarchy []EdgeView // sub_of 层级边
}

// AssetGraph 资产图最小投影查询。多语句读在同一事务快照内完成,
// 与并发写入不撕裂。
func (s *Store) AssetGraph(ctx context.Context, taskID string) (*AssetGraphView, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("rings: 开启资产图读事务: %w", err)
	}
	defer tx.Rollback()

	view := &AssetGraphView{}
	rows, err := tx.QueryContext(ctx, `
		SELECT n.node_id, n.title, n.status, n.asset_class, n.detail, n.created_rev, n.updated_rev,
		       EXISTS (
		         SELECT 1 FROM edges e
		         WHERE e.task_id = n.task_id AND e.edge_type = 'covers' AND e.to_node = n.node_id
		       ),
		       (
		         SELECT COUNT(*) FROM edges e
		         JOIN nodes src ON src.task_id = e.task_id AND src.node_id = e.from_node
		         WHERE e.task_id = n.task_id AND e.edge_type = 'about' AND e.to_node = n.node_id
		           AND src.node_type = 'fact'
		       ),
		       (
		         SELECT COUNT(*) FROM edges e
		         JOIN nodes src ON src.task_id = e.task_id AND src.node_id = e.from_node
		         WHERE e.task_id = n.task_id AND e.edge_type = 'about' AND e.to_node = n.node_id
		           AND src.node_type = 'hypothesis'
		       )
		FROM nodes n
		WHERE n.task_id = ? AND n.node_type = 'asset'
		ORDER BY n.node_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 资产: %w", taskID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var a AssetNodeView
		var class, detail string
		var covered, factCount, hypCount int
		if err := rows.Scan(&a.ID, &a.Title, &a.Status, &class, &detail, &a.CreatedRev, &a.UpdatedRev,
			&covered, &factCount, &hypCount); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 资产: %w", taskID, err)
		}
		a.Type = NodeAsset
		a.AssetClass = AssetClass(class)
		a.Covered = covered != 0
		a.FactCount = factCount
		a.HypothesisCount = hypCount
		a.Source = detailSource(detail)
		view.Assets = append(view.Assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rings: 遍历任务 %s 资产: %w", taskID, err)
	}

	view.Hierarchy, err = queryEdges(ctx, tx, taskID, `edge_type = 'sub_of'`)
	if err != nil {
		return nil, err
	}
	return view, nil
}

// detailSource 节点 detail JSON 的来源归一(AssetSourceOf 的字节串形态,
// 查询侧免整包反序列化)。
func detailSource(detail string) string {
	if detail == "" {
		return AssetSourceSeed
	}
	var d struct {
		Source string `json:"source"`
	}
	if json.Unmarshal([]byte(detail), &d) != nil || d.Source == "" {
		return AssetSourceSeed
	}
	return d.Source
}

// EdgeGoal 派生边类型(P2-09 统一图投影的投影层补边,不入账本):
// Goal → Step。Step 归属目标只存于 step.detail.goal_id(主宰派发时的
// 自由细节,无类型化边),进度图"Goal 为根向右生长"的血缘依赖这层投影
// 补齐;edgeRules 不含它——它永远不出现在账本事件里,只存在于图投影。
const EdgeGoal EdgeType = "goal"

// EdgeFalsify 派生边类型(P2-11 年轮图补,同 EdgeGoal 机制不入账本):
// Step → TestClaim。反证轮绑定只存于 step.detail.testclaim_id(P2-02
// 反证 Step 是独立 Step,无类型化边),全图视图的"反证轮血缘"依赖这层
// 投影补齐。语义 = 该步骤对本声明做了反证(结果在仲裁事件,边不表态);
// 设计示意标签「反驳」无对应账本边,按真实语义呈现为反证。
const EdgeFalsify EdgeType = "falsify"

// GraphNode 统一图投影节点(P2-09):六类节点全量。Covered/FactCount/
// HypothesisCount 仅对 asset 节点有值(派生计数,与 AssetGraph 同源:
// Covered=有 covers 边入;两计数=about 边按来源节点类型),非资产为零值;
// Source 同理仅资产有值(来源三态,P4-05)。
type GraphNode struct {
	NodeView
	Covered         bool
	FactCount       int
	HypothesisCount int
	Source          string
}

// GraphView 统一图投影(P2-09,8.7 三视图共用):资产树 / 进度图 / 年轮图
// 三种视图共用的"账本 → 图数据"唯一定义——全部六类节点 + 全部类型化边
// + Goal→Step 派生边。视图差异全部由消费侧投影,投影层不再各查各的。
type GraphView struct {
	Nodes []GraphNode
	Edges []EdgeView
}

// Graph 统一图投影查询。同一事务快照内完成,与并发写入不撕裂。
// 资产计数由边集在快照内派生(不经 AssetGraph 的子查询,一次读全)。
func (s *Store) Graph(ctx context.Context, taskID string) (*GraphView, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("rings: 开启图投影读事务: %w", err)
	}
	defer tx.Rollback()

	view := &GraphView{}
	rows, err := tx.QueryContext(ctx, `
		SELECT node_id, node_type, title, status, asset_class, created_rev, updated_rev
		FROM nodes WHERE task_id = ?
		ORDER BY created_rev, node_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 图节点: %w", taskID, err)
	}
	defer rows.Close()
	nodeType := make(map[string]NodeType, 64)
	for rows.Next() {
		var n GraphNode
		var nt, class string
		if err := rows.Scan(&n.ID, &nt, &n.Title, &n.Status, &class, &n.CreatedRev, &n.UpdatedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 图节点: %w", taskID, err)
		}
		n.Type, n.AssetClass = NodeType(nt), AssetClass(class)
		nodeType[n.ID] = n.Type
		view.Nodes = append(view.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rings: 遍历任务 %s 图节点: %w", taskID, err)
	}

	view.Edges, err = queryEdges(ctx, tx, taskID, `1=1`)
	if err != nil {
		return nil, err
	}

	// 资产派生计数(AssetGraph 同源语义):covers 边入=已覆盖;about 边
	// 按来源节点类型分计事实/假设。
	for i := range view.Nodes {
		if view.Nodes[i].Type != NodeAsset {
			continue
		}
		for _, e := range view.Edges {
			if e.To != view.Nodes[i].ID {
				continue
			}
			switch {
			case e.Type == EdgeCovers:
				view.Nodes[i].Covered = true
			case e.Type == EdgeAbout && nodeType[e.From] == NodeFact:
				view.Nodes[i].FactCount++
			case e.Type == EdgeAbout && nodeType[e.From] == NodeHypothesis:
				view.Nodes[i].HypothesisCount++
			}
		}
	}

	// 资产来源三态(P4-05):读 asset.detail 的 source(归一,缺省种子)。
	arows, err := tx.QueryContext(ctx, `
		SELECT node_id, detail FROM nodes
		WHERE task_id = ? AND node_type = 'asset'`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 资产来源: %w", taskID, err)
	}
	defer arows.Close()
	assetSource := make(map[string]string, 32)
	for arows.Next() {
		var id, detail string
		if err := arows.Scan(&id, &detail); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 资产来源: %w", taskID, err)
		}
		assetSource[id] = detailSource(detail)
	}
	if err := arows.Err(); err != nil {
		return nil, fmt.Errorf("rings: 遍历任务 %s 资产来源: %w", taskID, err)
	}
	for i := range view.Nodes {
		if view.Nodes[i].Type == NodeAsset {
			view.Nodes[i].Source = assetSource[view.Nodes[i].ID]
		}
	}

	// Goal→Step 派生边:读 step.detail.goal_id(只查步骤节点,大 detail
	// 的证据类节点不出投影)。同一次扫描兼收 testclaim_id(P2-11 反证轮
	// 派生边)。
	srows, err := tx.QueryContext(ctx, `
		SELECT node_id, detail FROM nodes
		WHERE task_id = ? AND node_type = 'step'`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 步骤归属: %w", taskID, err)
	}
	defer srows.Close()
	stepGoal := make(map[string]string, 16)
	stepClaim := make(map[string]string, 16)
	for srows.Next() {
		var id, detail string
		if err := srows.Scan(&id, &detail); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 步骤归属: %w", taskID, err)
		}
		var d struct {
			GoalID      string `json:"goal_id"`
			TestClaimID string `json:"testclaim_id"`
		}
		if json.Unmarshal([]byte(detail), &d) != nil {
			continue
		}
		if d.GoalID != "" && nodeType[d.GoalID] == NodeGoal {
			stepGoal[id] = d.GoalID
		}
		if d.TestClaimID != "" && nodeType[d.TestClaimID] == NodeTestClaim {
			stepClaim[id] = d.TestClaimID
		}
	}
	if err := srows.Err(); err != nil {
		return nil, fmt.Errorf("rings: 遍历任务 %s 步骤归属: %w", taskID, err)
	}
	for stepID, goalID := range stepGoal {
		view.Edges = append(view.Edges, EdgeView{Type: EdgeGoal, From: goalID, To: stepID})
	}
	for stepID, claimID := range stepClaim {
		view.Edges = append(view.Edges, EdgeView{Type: EdgeFalsify, From: stepID, To: claimID})
	}
	return view, nil
}

// ProgressGraphView 进度图最小投影:意图→事实→漏洞的血缘链读模型。
// 全局一张,每任务只是子图查询(视图隔离、数据统一)。
type ProgressGraphView struct {
	Goals      []NodeView
	Steps      []NodeView
	Facts      []NodeView
	TestClaims []NodeView
	Lineage    []EdgeView // produces(Step→Fact)+ derived_from(TestClaim→Step|Fact)
}

// ProgressGraph 进度图最小投影(P1-04 原独立查询,P2-09 起改为 Graph
// 统一图投影之上的兼容视图:图数据唯一定义在 Graph,此处仅按测试与
// 收割侧消费的 分组+血缘 形状重组,不再自查)。
func (s *Store) ProgressGraph(ctx context.Context, taskID string) (*ProgressGraphView, error) {
	g, err := s.Graph(ctx, taskID)
	if err != nil {
		return nil, err
	}
	view := &ProgressGraphView{}
	for _, n := range g.Nodes {
		var dst *[]NodeView
		switch n.Type {
		case NodeGoal:
			dst = &view.Goals
		case NodeStep:
			dst = &view.Steps
		case NodeFact:
			dst = &view.Facts
		case NodeTestClaim:
			dst = &view.TestClaims
		default:
			continue
		}
		*dst = append(*dst, n.NodeView)
	}
	for _, e := range g.Edges {
		if e.Type == EdgeProduces || e.Type == EdgeDerivedFrom {
			view.Lineage = append(view.Lineage, e)
		}
	}
	return view, nil
}

func queryEdges(ctx context.Context, tx *sql.Tx, taskID, predicate string) ([]EdgeView, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT edge_type, from_node, to_node, created_rev
		FROM edges WHERE task_id = ? AND `+predicate+`
		ORDER BY created_rev, from_node`, taskID)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 边(%s): %w", taskID, predicate, err)
	}
	defer rows.Close()
	var out []EdgeView
	for rows.Next() {
		var e EdgeView
		var et string
		if err := rows.Scan(&et, &e.From, &e.To, &e.CreatedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 边: %w", taskID, err)
		}
		e.Type = EdgeType(et)
		out = append(out, e)
	}
	return out, rows.Err()
}

// NodeFull 节点带自由细节的完整投影(P1-04 起虫群任务包/主宰研判需要
// 读 detail:goal_id、预期观察、证据引用等)。
type NodeFull struct {
	NodeView
	Detail map[string]any
}

// Node 按 ID 取单个节点(含 detail)。跨任务隔离:taskID 必须匹配。
func (s *Store) Node(ctx context.Context, taskID, nodeID string) (*NodeFull, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	var n NodeFull
	var nt, class, detail string
	err := s.db.QueryRowContext(ctx, `
		SELECT node_id, node_type, title, status, asset_class, detail, created_rev, updated_rev
		FROM nodes WHERE task_id = ? AND node_id = ?`, taskID, nodeID,
	).Scan(&n.ID, &nt, &n.Title, &n.Status, &class, &detail, &n.CreatedRev, &n.UpdatedRev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: task %s node %s", ErrNodeNotFound, taskID, nodeID)
	}
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 节点 %s: %w", taskID, nodeID, err)
	}
	n.Type = NodeType(nt)
	n.AssetClass = AssetClass(class)
	if err := json.Unmarshal([]byte(detail), &n.Detail); err != nil {
		return nil, fmt.Errorf("rings: 解码节点 %s detail: %w", nodeID, err)
	}
	return &n, nil
}

// NodesByType 按节点类型列出(可选按状态过滤,status 空为全部)。
func (s *Store) NodesByType(ctx context.Context, taskID string, nt NodeType, status string) ([]NodeFull, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	q := `
		SELECT node_id, title, status, asset_class, detail, created_rev, updated_rev
		FROM nodes WHERE task_id = ? AND node_type = ?`
	args := []any{taskID, string(nt)}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_rev, node_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 节点类型 %s: %w", taskID, nt, err)
	}
	defer rows.Close()
	var out []NodeFull
	for rows.Next() {
		var n NodeFull
		var class, detail string
		if err := rows.Scan(&n.ID, &n.Title, &n.Status, &class, &detail, &n.CreatedRev, &n.UpdatedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 节点: %w", taskID, err)
		}
		n.Type = nt
		n.AssetClass = AssetClass(class)
		if err := json.Unmarshal([]byte(detail), &n.Detail); err != nil {
			return nil, fmt.Errorf("rings: 解码节点 %s detail: %w", n.ID, err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimSearchRow 跨任务声明搜索结果行(P3-10 命令面板):声明定位四元组,
// 附所属任务标题供面板展示;不含状态机审计流(面板跳任务发现页看全貌)。
type ClaimSearchRow struct {
	TaskID    string
	ClaimID   string
	Title     string
	Status    string
	TaskTitle string
}

// SearchClaims 跨任务按子串搜声明(命令面板,P3-10):nodes 投影直查,
// 模式串先经 EscapeLike(% 与 _ 转义,防通配注入);墓碑任务不搜出。
// 只做定位不做汇总,limit 封顶由调用方给。
func (s *Store) SearchClaims(ctx context.Context, like string, limit int) ([]ClaimSearchRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.task_id, n.node_id, n.title, n.status, t.title
		FROM nodes n JOIN tasks t ON t.task_id = n.task_id
		WHERE n.node_type = ? AND t.deleted = 0
		  AND (n.title LIKE ? ESCAPE '\' OR n.node_id LIKE ? ESCAPE '\')
		ORDER BY n.task_id, n.created_rev, n.node_id
		LIMIT ?`,
		string(NodeTestClaim), like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("rings: 搜索声明: %w", err)
	}
	defer rows.Close()
	var out []ClaimSearchRow
	for rows.Next() {
		var r ClaimSearchRow
		if err := rows.Scan(&r.TaskID, &r.ClaimID, &r.Title, &r.Status, &r.TaskTitle); err != nil {
			return nil, fmt.Errorf("rings: 扫描声明搜索行: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EscapeLike 把用户输入转为 LIKE 子串模式(% 与 _ 及转义符本身转义),
// 配 ESCAPE '\' 使用——用户输入的通配符按字面匹配。
func EscapeLike(in string) string {
	repl := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + repl.Replace(in) + "%"
}

// Edges 列出任务的类型化边(可按类型过滤,空为全部)。虫群任务包构建
// (covers/about 血缘跳查)与收割研判用。
func (s *Store) Edges(ctx context.Context, taskID string, types ...EdgeType) ([]EdgeView, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	q := `SELECT edge_type, from_node, to_node, created_rev FROM edges WHERE task_id = ?`
	args := []any{taskID}
	if len(types) > 0 {
		ph := make([]string, len(types))
		for i, t := range types {
			ph[i] = "?"
			args = append(args, string(t))
		}
		q += ` AND edge_type IN (` + strings.Join(ph, ",") + `)`
	}
	q += ` ORDER BY created_rev, from_node`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("rings: 查询任务 %s 边: %w", taskID, err)
	}
	defer rows.Close()
	var out []EdgeView
	for rows.Next() {
		var e EdgeView
		var et string
		if err := rows.Scan(&et, &e.From, &e.To, &e.CreatedRev); err != nil {
			return nil, fmt.Errorf("rings: 扫描任务 %s 边: %w", taskID, err)
		}
		e.Type = EdgeType(et)
		out = append(out, e)
	}
	return out, rows.Err()
}

// HibernateCredit 任务冬眠的墙钟抵扣(P2-08,第七章「任务冬眠」:全部
// 计时器停走——任务总闸墙钟/收尾预算不因冬眠被吃):扫描生命周期事件,
// hibernated(入眠)→ 下一条生命周期事件(唤醒恢复)配对求时长和。
// 当前仍在冬眠的开口区间不计(唤醒事件的记账先于开门,醒后查询即闭合)。
func (s *Store) HibernateCredit(ctx context.Context, taskID string) (time.Duration, error) {
	if taskID == "" {
		return 0, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("rings: 冬眠抵扣查事件 %s: %w", taskID, err)
	}
	var total time.Duration
	var openAt time.Time
	for _, ev := range evs {
		if ev.Kind != EventTaskLifecycle {
			continue
		}
		var p TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		if p.Action == "hibernated" {
			openAt = ev.OccurredAt
			continue
		}
		if !openAt.IsZero() {
			total += ev.OccurredAt.Sub(openAt)
			openAt = time.Time{}
		}
	}
	return total, nil
}

// LastHibernateRev 最近一次入眠事件后的图版本(P2-08:冬眠唤醒后 R14
// 复检的锚点——冬眠期间 RoE/前置事实的变化都落在此版本之后)。从未
// 冬眠返回 0(调用方按"从领取版本起复检"退化)。
func (s *Store) LastHibernateRev(ctx context.Context, taskID string) (int64, error) {
	if taskID == "" {
		return 0, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("rings: 冬眠锚点查事件 %s: %w", taskID, err)
	}
	var rev int64
	for _, ev := range evs {
		if ev.Kind != EventTaskLifecycle {
			continue
		}
		var p TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "hibernated" {
			rev = ev.Revision
		}
	}
	return rev, nil
}

// HibernateDeduct 冬眠墙钟合计(含进行中的冬眠,P4-02 ④ 会话时长冻结
// 口径):已闭合区间求和 + 当前开口区间截至 now。与 HibernateCredit 的
// 分工——那边是收尾线抵扣(醒后查询才闭合,不含开口),这边是面板
// 呈现(冬眠中就要冻结,开口区间必须计入)。查询失败返回 0(呈现
// 降级为自然时长,不阻塞会话视图)。
func (s *Store) HibernateDeduct(ctx context.Context, taskID string, now time.Time) time.Duration {
	if taskID == "" {
		return 0
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0
	}
	var total time.Duration
	var openAt time.Time
	for _, ev := range evs {
		if ev.Kind != EventTaskLifecycle {
			continue
		}
		var p TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		if p.Action == "hibernated" {
			openAt = ev.OccurredAt
			continue
		}
		// 闭合判定与 HibernateCredit 同款:入眠后的下一条生命周期事件
		//(唤醒恢复前状态 started/settling,非固定词)。
		if !openAt.IsZero() {
			total += ev.OccurredAt.Sub(openAt)
			openAt = time.Time{}
		}
	}
	if !openAt.IsZero() && now.After(openAt) {
		total += now.Sub(openAt)
	}
	return total
}

// SessionLabel 一个会话的组织信息投影(P3-13b ②):session.meta 事件
// 流的末态——自定义名称(空 = 默认名)、备注、置顶(置顶动作的版本号
// 作置顶会话间的次序锚,同任务列表 pinned_rev 惯例)。
type SessionLabel struct {
	Title     string
	Note      string
	Pinned    bool
	PinnedRev int64
}

// SessionLabels 会话组织信息投影:扫该任务的全部 session.meta 事件,
// 按事件序应用(指针字段语义:携带即改,空串清除)。无事件返回空表。
func (s *Store) SessionLabels(ctx context.Context, taskID string) (map[string]SessionLabel, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	evs, err := s.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("rings: 会话标签查事件 %s: %w", taskID, err)
	}
	out := map[string]SessionLabel{}
	for _, ev := range evs {
		if ev.Kind != EventSessionMeta {
			continue
		}
		var p SessionMeta
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		l := out[p.SessionID]
		if p.Title != nil {
			l.Title = *p.Title
		}
		if p.Note != nil {
			l.Note = *p.Note
		}
		if p.Pinned != nil {
			l.Pinned, l.PinnedRev = *p.Pinned, ev.Revision
		}
		out[p.SessionID] = l
	}
	return out, nil
}
