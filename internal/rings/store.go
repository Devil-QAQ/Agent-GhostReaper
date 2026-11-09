package rings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ghostreaper/internal/runlog"
	"ghostreaper/internal/secrets"

	// 纯 Go SQLite 驱动,注册 "sqlite" 驱动名(第六章 §3:交叉编译无痛)
	_ "modernc.org/sqlite"
)

var (
	ErrStaleRevision              = errors.New("rings: task changed before append")
	ErrClosed                     = errors.New("rings: store closed")
	ErrEmptyTaskID                = errors.New("rings: task id is empty")
	ErrUnknownKind                = errors.New("rings: unknown event kind")
	ErrUnknownNodeType            = errors.New("rings: unknown node type")
	ErrUnknownAssetClass          = errors.New("rings: unknown asset class")
	ErrNodeIDEmpty                = errors.New("rings: node id is empty")
	ErrNodeExists                 = errors.New("rings: node already exists")
	ErrNodeNotFound               = errors.New("rings: node not found")
	ErrAmendNotAllowed            = errors.New("rings: node amendment not allowed")
	ErrUnknownEdgeType            = errors.New("rings: unknown edge type")
	ErrEdgeEndpointsEmpty         = errors.New("rings: edge endpoint is empty")
	ErrEdgeSelfLoop               = errors.New("rings: edge self loop")
	ErrEdgeRuleViolation          = errors.New("rings: edge endpoints violate type rule")
	ErrEdgeExists                 = errors.New("rings: edge already exists")
	ErrRevisionNotIncreasing      = errors.New("rings: revision not strictly increasing")
	ErrUnknownTaskAction          = errors.New("rings: unknown task lifecycle action")
	ErrTaskExists                 = errors.New("rings: task already exists")
	ErrTaskNotFound               = errors.New("rings: task not found")
	ErrTaskMetaNoField            = errors.New("rings: task meta event carries no field to change")
	ErrTaskMetaDeleteNoReason     = errors.New("rings: tombstone requires reason")
	ErrUnknownSwarmRecType        = errors.New("rings: unknown swarm record type")
	ErrSwarmRecMissingField       = errors.New("rings: swarm record missing required field")
	ErrArbiterVerdictMissingField = errors.New("rings: arbiter verdict missing required field")
	ErrUnknownApprovalRecType     = errors.New("rings: unknown approval record type")
	ErrApprovalRecMissingField    = errors.New("rings: approval record missing required field")
	ErrInvalidActionLevel         = errors.New("rings: unknown action level")
	ErrUnknownDenyLine            = errors.New("rings: unknown approval deny line")
	ErrUnknownHarmClass           = errors.New("rings: unknown harm class")
	ErrUnknownTestClaimRecType    = errors.New("rings: unknown testclaim record type")
	ErrTestClaimRecMissingField   = errors.New("rings: testclaim record missing required field")
	ErrTestClaimPendingNoReason   = errors.New("rings: pending testclaim verdict requires shelving reason")
	ErrUnknownClaimDecision       = errors.New("rings: unknown testclaim verdict decision")
	ErrUnknownImpactLevel         = errors.New("rings: unknown impact level")
	ErrUnknownReopenTrigger       = errors.New("rings: unknown reopen trigger")
	ErrReopenNoReason             = errors.New("rings: reopen record requires reason")
	ErrAltResultMissingID         = errors.New("rings: alternative result missing id")
	ErrUnknownMirrorRecType       = errors.New("rings: unknown mirror record type")
	ErrMirrorRecMissingField      = errors.New("rings: mirror record missing required field")
	ErrUnknownHypothesisRecType   = errors.New("rings: unknown hypothesis record type")
	ErrUnknownHypothesisDecision  = errors.New("rings: unknown hypothesis evaluation decision")
	ErrUnknownHypothesisVerdict   = errors.New("rings: unknown hypothesis result verdict")
	ErrHypothesisRecMissingField  = errors.New("rings: hypothesis record missing required field")
	ErrHypothesisEvalNoReason     = errors.New("rings: hypothesis evaluation requires reason")
	ErrUnknownBlackboxRecType     = errors.New("rings: unknown blackbox record type")
	ErrTokenUsageMissingField     = errors.New("rings: token usage row missing task or agent")
	ErrUnknownSteerCommand        = errors.New("rings: unknown steer command")
	ErrUnknownSteerState          = errors.New("rings: unknown steer state")
	ErrInvalidSteerRecord         = errors.New("rings: invalid steer record")
	ErrUnknownOvermindRecType     = errors.New("rings: unknown overmind record type")
	ErrOvermindRecEmptyAssessment = errors.New("rings: empty assessment record rejected (no information)")
	ErrUnknownShepherdRecType     = errors.New("rings: unknown shepherd record type")
	ErrShepherdRecMissingField    = errors.New("rings: shepherd record missing required field")
	ErrUnknownShepherdMsgState    = errors.New("rings: unknown shepherd message state")
	ErrUnknownShepherdAdviceState = errors.New("rings: unknown shepherd advice state")
	ErrUnknownUnitCtlAction       = errors.New("rings: unknown unit control action")
	ErrUnknownUnitCtlTarget       = errors.New("rings: unknown unit control target")
	ErrInvalidUnitControl         = errors.New("rings: invalid unit control record")
	ErrUnknownInterjectRecType    = errors.New("rings: unknown interject record type")
	ErrUnknownInterjectTarget     = errors.New("rings: unknown interject target")
	ErrUnknownInterjectMode       = errors.New("rings: unknown interject mode")
	ErrUnknownInterjectMsgState   = errors.New("rings: unknown interject message state")
	ErrInterjectRecMissingField   = errors.New("rings: interject record missing required field")
	ErrSettingKeyEmpty            = errors.New("rings: setting key is empty")
	ErrSettingValueEmpty          = errors.New("rings: setting value is empty")
	ErrTaskPurged                 = errors.New("rings: task purged, appends rejected")
	ErrInvalidIntentRule          = errors.New("rings: invalid intent rule")
	ErrInvalidSessionMeta         = errors.New("rings: invalid session meta")
	ErrUnknownGuardRecType        = errors.New("rings: unknown guard record type")
	ErrGuardRecMissingField       = errors.New("rings: guard record missing required field")
	ErrEvidenceBindingInvalid     = errors.New("rings: invalid evidence binding")
	ErrUnknownRetestVerdict       = errors.New("rings: unknown retest verdict")
	ErrUnknownReplayVerdict       = errors.New("rings: unknown replay verdict")
	ErrReplayRecordInvalid        = errors.New("rings: invalid replay record")
	ErrReportReviewInvalid        = errors.New("rings: invalid report review record")
	ErrUnknownReportRecType       = errors.New("rings: unknown report record type")
	ErrReportRecMissingField      = errors.New("rings: report record missing required field")
	ErrUnknownClosureRecType      = errors.New("rings: unknown closure record type")
	ErrInvalidClosureRecord       = errors.New("rings: invalid closure record")
)

// Store 年轮账本存储:单写者排队 + WAL 并发读。
// 写路径:Append/RebuildProjections 经无缓冲 channel 排队,由唯一
// writer goroutine 逐项以单事务提交(事件插入与投影更新同事务,原子)。
// 读路径:直接走连接池,WAL 下多读并发、快照不撕裂。
// P4-38 数据库暂存保养:WAL 回收(写后超阈值被动回收 + 空闲全量回收,
// journal_size_limit 收缩暂存)全部在单写者循环内执行——checkpoint
// 进行期间结构上不可能并发写,也不阻塞读。
type Store struct {
	db        *sql.DB
	writes    chan writeRequest
	closing   chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	walPath   string // 暂存文件路径(P4-38 保养巡检用)
	// atRest 静态加密封装器(P3-09):敏感键(secrets.SensitiveSetting)
	// 的值入库前封装、出库后解封,对消费方透明。EnableAtRest 在开始
	// 并发读写之前调用一次(启动装配期),此后只读——无锁安全。
	atRest *secrets.Cipher
	// onCommit 事件批提交后的观察者(P3-08 实时推送:任务状态 /
	// 审批待决 / 发现 / 受阻一切面板关心的变化都经账本落账,在此单点
	// 通知订阅方)。写者协程读、注册方协程写——经 atomic 无锁同步;
	// SetOnCommit 注册,未注册即无观察者。
	onCommit atomic.Value
	// purged 已特权清除的任务(P4-40 ⑥):清除提交后对其一切账本写入
	// 拒收(ErrTaskPurged)——终态总结等异步写者可能在清除后才落笔,
	// 不拒收会让已销毁的数据复活。单写者协程私有(置位于 purge 提交、
	// 判位于 append/token 写路径,均在写者协程内),无需加锁;进程内
	// 同 ID 复活在 Manager 一侧本就不可能(任务句柄在册即拒建)。
	purged map[string]bool
}

// onCommitFn atomic.Value 的具体类型包装(保持存取类型一致)。
type onCommitFn struct {
	fn func(taskID string, events []Event)
}

// SetOnCommit 注册事件批提交后的观察者钩子(P3-08):在单写者循环内
// 同步调用——实现必须非阻塞且不得回调 Store(会死锁:写入通道无缓冲)。
// 线程安全;后注册覆盖先注册(单 Store 单观察者)。
func (s *Store) SetOnCommit(fn func(taskID string, events []Event)) {
	s.onCommit.Store(onCommitFn{fn: fn})
}

type writeRequest struct {
	expectedRevision *int64
	taskID           string
	events           []Event
	tokens           []TokenUsage
	rebuild          bool
	setting          *settingWrite
	purge            *purgeWrite
	resp             chan writeResult
}

// settingWrite 一次通用配置写入(v6 键值表):upsert 或删除(key 为空
// 非法)。与事件/token 同走单写者排队——配置写入与账本写入共用一把
// 写锁,不另立写通道。keepAudit 供静态加密迁移(P3-09)复用写路径
// 改写 value 而保持 by/updated_at 原值(内容未变,审计不虚记)。
type settingWrite struct {
	key       string
	value     string // delete 为 true 时忽略
	by        string
	delete    bool
	keepAudit bool
	updatedAt time.Time
}

// purgeWrite 特权清除(P4-40 ⑥):单事务删除一个任务在库内的全部行。
type purgeWrite struct {
	taskID string
}

type writeResult struct {
	revisions []int64
	rows      int64 // purge 专用:删除总行数
	err       error
}

// Open 打开(或创建)年轮库并启动单写者。path 为库文件路径;
// 驱动 modernc.org/sqlite(纯 Go,单二进制交叉编译无痛,第六章 §3),
// WAL 模式 + busy_timeout + synchronous(NORMAL) + journal_size_limit
// (P4-38:checkpoint 重置时收缩暂存文件,防高水位常驻)。
// P4-36:升级旧库跨过投影首版(v10)时,在写者启动后同步全量重放一次
// 回填新投影表(一次性启动成本;期间尚无读者,无撕裂窗口)。
// P4-38:WAL 保养协程随库启动(见 walkeep.go)。
func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=journal_size_limit(" + strconv.FormatInt(walJournalSizeLimit, 10) + ")"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("rings: 打开库 %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("rings: 连接库 %s: %w", path, err)
	}
	var fromVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&fromVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("rings: 读取 schema 版本: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{
		db:      db,
		writes:  make(chan writeRequest),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
		walPath: path + "-wal",
		purged:  map[string]bool{},
	}
	go s.writerLoop()
	if fromVersion < projectionFloor && len(migrations) >= projectionFloor {
		runlog.Infof("年轮库升级(v%d → v%d):全量重放回填读侧投影,一次性启动开销", fromVersion, len(migrations))
		if err := s.RebuildProjections(context.Background()); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("rings: 升级回填投影: %w", err)
		}
	}
	return s, nil
}

// Append 追加一批事件(单事务:要么全部落库,要么全部回滚),返回与
// 入参一一对应的任务内 revision。
//
// 排队语义:ctx 只影响排队等待,已入队的写入必然执行到提交为止——
// 账本写入不可凭取消消失。
func (s *Store) Append(ctx context.Context, taskID string, events ...Event) ([]int64, error) {
	return s.appendChecked(ctx, taskID, nil, events...)
}

// AppendAtRevision 在同一写事务中复检读取版本，避免准入与队列取消的读写竞态。
func (s *Store) AppendAtRevision(ctx context.Context, taskID string, revision int64, events ...Event) ([]int64, error) {
	return s.appendChecked(ctx, taskID, &revision, events...)
}

func (s *Store) appendChecked(ctx context.Context, taskID string, expected *int64, events ...Event) ([]int64, error) {
	if taskID == "" {
		return nil, ErrEmptyTaskID
	}
	req := writeRequest{taskID: taskID, events: events, expectedRevision: expected, resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closing:
		return nil, ErrClosed
	}
	res := <-req.resp
	return res.revisions, res.err
}

// RebuildProjections 清空投影并按 (task_id, revision) 全量重放事件流。
// 换库迁移、投影校验、故障恢复共用(第六章 §4 后路)。同样经单写者排队。
func (s *Store) RebuildProjections(ctx context.Context) error {
	req := writeRequest{rebuild: true, resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closing:
		return ErrClosed
	}
	return (<-req.resp).err
}

// RecordTokens 追加一批 token 用量行(独立记账表,单事务提交;不进事件
// 流、不进投影——第四章 §6 逐次用量与账本分工)。排队语义同 Append:
// ctx 只影响排队等待,已入队的写入必然执行到提交。
func (s *Store) RecordTokens(ctx context.Context, rows ...TokenUsage) error {
	if err := validateTokenRows(rows); err != nil {
		return err
	}
	req := writeRequest{tokens: rows, resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closing:
		return ErrClosed
	}
	return (<-req.resp).err
}

// validateTokenRows token 行结构校验:无主用量不可记账(任务/agent 必填)。
func validateTokenRows(rows []TokenUsage) error {
	for i, r := range rows {
		if r.TaskID == "" || r.Agent == "" {
			return fmt.Errorf("%w: 第 %d 行", ErrTokenUsageMissingField, i+1)
		}
	}
	return nil
}

// ---- 通用配置键值表(P2-06c,schema v6) ----

// SettingEntry 一条通用配置的读结果(value 原样字符串,类型由消费方
// 解析)与轻量审计字段。
type SettingEntry struct {
	Key       string
	Value     string
	UpdatedBy string
	UpdatedAt time.Time
}

// SetSetting 写入(或覆盖)一条全局配置。经单写者排队落库,排队语义
// 同 Append:ctx 只影响排队等待,已入队的写入必然执行到提交。
// by 记改动者(面板写 = creator);updated_at 取写入时刻。
// P3-09:敏感键(secrets.SensitiveSetting)的值在入队前封装——存储层
// 密文,消费方读回透明解封。
func (s *Store) SetSetting(ctx context.Context, key, value, by string) error {
	if key == "" {
		return ErrSettingKeyEmpty
	}
	if value == "" {
		return ErrSettingValueEmpty
	}
	// 配置修改落运行日志(P3-13b ③):只记键名与改动者,值一律
	// 不记(红线:key / 凭据零记录——值可能含敏感内容)。by=system 是
	// 装配播种(内置硬规则等每次启动写缺失键),降 DEBUG 防刷屏。
	if by == "system" {
		runlog.Debugf("配置播种: %s", key)
	} else {
		runlog.Infof("配置修改: %s(by %s)", key, by)
	}
	if s.atRest != nil && secrets.SensitiveSetting(key) {
		sealed, err := s.atRest.Seal(value)
		if err != nil {
			return fmt.Errorf("rings: 封装配置 %s: %w", key, err)
		}
		value = sealed
	}
	req := writeRequest{setting: &settingWrite{key: key, value: value, by: by, updatedAt: time.Now().UTC()},
		resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closing:
		return ErrClosed
	}
	return (<-req.resp).err
}

// SetSettingQuiet 写入(或覆盖)一条全局配置,不留"配置修改"运行日志行
// (P4-40 任务草稿:草稿保存不是配置修改,防日志刷屏;键值表存储与单写者
// 排队语义同 SetSetting)。任务草稿(task.draft.* 键)专用,其余配置写
// 仍走 SetSetting 留痕。
func (s *Store) SetSettingQuiet(ctx context.Context, key, value, by string) error {
	if key == "" {
		return ErrSettingKeyEmpty
	}
	if value == "" {
		return ErrSettingValueEmpty
	}
	if s.atRest != nil && secrets.SensitiveSetting(key) {
		sealed, err := s.atRest.Seal(value)
		if err != nil {
			return fmt.Errorf("rings: 封装配置 %s: %w", key, err)
		}
		value = sealed
	}
	req := writeRequest{setting: &settingWrite{key: key, value: value, by: by,
		updatedAt: time.Now().UTC()}, resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closing:
		return ErrClosed
	}
	return (<-req.resp).err
}

// EnableAtRest 启用静态加密并迁移存量(P3-09):此后敏感键的值写入前
// 封装、读回解封(对消费方透明);库里已有的敏感明文就地改写为密文,
// by/updated_at 保持原值(内容未变,审计不虚记)。必须在开始并发读写
// 之前调用(启动装配期一次);返回迁移条数。密文格式见 internal/secrets。
func (s *Store) EnableAtRest(ctx context.Context, c *secrets.Cipher) (int, error) {
	if c == nil {
		return 0, errors.New("rings: 静态加密封装器不可为空")
	}
	s.atRest = c
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_by, updated_at FROM settings`)
	if err != nil {
		return 0, fmt.Errorf("rings: 静态加密迁移读: %w", err)
	}
	type pending struct {
		w settingWrite
	}
	var queue []pending
	for rows.Next() {
		var key, value, by, at string
		if err := rows.Scan(&key, &value, &by, &at); err != nil {
			rows.Close()
			return 0, fmt.Errorf("rings: 静态加密迁移读行: %w", err)
		}
		if !secrets.SensitiveSetting(key) || secrets.IsSealed(value) {
			continue
		}
		sealed, err := c.Seal(value)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("rings: 迁移封装 %s: %w", key, err)
		}
		queue = append(queue, pending{w: settingWrite{key: key, value: sealed, by: by,
			keepAudit: true, updatedAt: parseTimeLoose(at)}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("rings: 静态加密迁移读: %w", err)
	}
	rows.Close()
	for _, p := range queue {
		req := writeRequest{setting: &p.w, resp: make(chan writeResult, 1)}
		select {
		case s.writes <- req:
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-s.closing:
			return 0, ErrClosed
		}
		if err := (<-req.resp).err; err != nil {
			return 0, fmt.Errorf("rings: 迁移写回 %s: %w", p.w.key, err)
		}
	}
	return len(queue), nil
}

// SnapshotTo 把账本一致性快照写到目标文件(VACUUM INTO;P4-32 诊断包
// 账本库进包用)。WAL 下与写者并发安全(快照取单一时刻的一致视图);
// 目标文件须不存在,删除由调用方负责。
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("rings: 账本快照: %w", err)
	}
	return nil
}

// DeleteSetting 删除一条全局配置(恢复装配默认的消费语义)。经单写者
// 排队;键不存在幂等成功。
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if key == "" {
		return ErrSettingKeyEmpty
	}
	req := writeRequest{setting: &settingWrite{key: key, delete: true, updatedAt: time.Now().UTC()},
		resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closing:
		return ErrClosed
	}
	return (<-req.resp).err
}

// PurgeTask 特权清除:单事务删除该任务在账本库内的全部行(逐表清单见
// purgeTaskTx,与 schema 带 task_id 的表一一对应)。经单写者排队——与
// 一切账本写入共用一把写锁,清行不与在途写竞态(调用方须先确保该任务
// 运行件已收场,见 Manager.WaitSettled)。不落墓碑事件(账本行连同任务
// 行一并清除,运行日志留痕由调用方负责)。返回删除总行数。任务无任何
// 行时幂等返回 0。
func (s *Store) PurgeTask(ctx context.Context, taskID string) (int64, error) {
	if taskID == "" {
		return 0, ErrEmptyTaskID
	}
	req := writeRequest{purge: &purgeWrite{taskID: taskID}, resp: make(chan writeResult, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.closing:
		return 0, ErrClosed
	}
	res := <-req.resp
	return res.rows, res.err
}

// purgeTaskTx 单事务逐表删除该任务行(events 事件流 / nodes+edges 双图
// 投影 / tasks 生命周期 / roe_state RoE 当前值 / token_usage token 记账 /
// 其余 P4-36 读侧投影),累计删除行数。逐条显式字面量语句、task_id 一律
// 参数化;blobs 内容寻址仓不在库内(跨任务共享,一律不动),settings
// 键值表无 task_id 维度(草稿键另由草稿端点管理)。
func purgeTaskTx(tx *sql.Tx, taskID string) (int64, error) {
	var total int64
	count := func(res sql.Result) {
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	if res, err := tx.Exec(`DELETE FROM events WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 events: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM nodes WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 nodes: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM edges WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 edges: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM tasks WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 tasks: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM roe_state WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 roe_state: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM token_usage WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 token_usage: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM unit_sessions WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 unit_sessions: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM session_labels WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 session_labels: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM approval_records WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 approval_records: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM claim_cases WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 claim_cases: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM hyp_states WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 hyp_states: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM task_extras WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 task_extras: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM closure_states WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 closure_states: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM claim_qa WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 claim_qa: %w", taskID, err)
	} else {
		count(res)
	}
	if res, err := tx.Exec(`DELETE FROM mirror_briefs WHERE task_id = ?`, taskID); err != nil {
		return 0, fmt.Errorf("rings: 特权清除任务 %s 表 mirror_briefs: %w", taskID, err)
	} else {
		count(res)
	}
	return total, nil
}

// Setting 读一条全局配置;ok=false 表示未配置(消费方退装配默认)。
// 读路径走连接池(WAL 并发读),updated_at 解析失败按零值如实返回。
// P3-09:敏感键密文透明解封;未封装的历史明文原样返回(迁移兜底语义)。
func (s *Store) Setting(ctx context.Context, key string) (e SettingEntry, ok bool, err error) {
	if key == "" {
		return e, false, ErrSettingKeyEmpty
	}
	var at string
	if err := s.db.QueryRowContext(ctx,
		`SELECT value, updated_by, updated_at FROM settings WHERE key = ?`, key,
	).Scan(&e.Value, &e.UpdatedBy, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SettingEntry{Key: key}, false, nil
		}
		return e, false, fmt.Errorf("rings: 读配置 %s: %w", key, err)
	}
	e.Key, e.UpdatedAt = key, parseTimeLoose(at)
	if s.atRest != nil && secrets.SensitiveSetting(key) {
		v, oerr := s.atRest.Open(e.Value)
		if oerr != nil {
			return SettingEntry{}, false, fmt.Errorf("rings: 解封配置 %s: %w", key, oerr)
		}
		e.Value = v
	}
	return e, true, nil
}

// SettingsWithPrefix 按前缀读一批全局配置(键序稳定);读路径走连接池。
// P2-06e 模型库清单等批量读用。P3-09:敏感键密文透明解封(单条解封
// 失败整批报错,不静默丢配置)。
func (s *Store) SettingsWithPrefix(ctx context.Context, prefix string) ([]SettingEntry, error) {
	if prefix == "" {
		return nil, ErrSettingKeyEmpty
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value, updated_by, updated_at FROM settings WHERE key LIKE ? ORDER BY key`,
		prefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("rings: 按前缀读配置 %s: %w", prefix, err)
	}
	defer rows.Close()
	var out []SettingEntry
	for rows.Next() {
		var e SettingEntry
		var at string
		if err := rows.Scan(&e.Key, &e.Value, &e.UpdatedBy, &at); err != nil {
			return nil, fmt.Errorf("rings: 按前缀读配置 %s: %w", prefix, err)
		}
		e.UpdatedAt = parseTimeLoose(at)
		if s.atRest != nil && secrets.SensitiveSetting(e.Key) {
			v, oerr := s.atRest.Open(e.Value)
			if oerr != nil {
				return nil, fmt.Errorf("rings: 解封配置 %s: %w", e.Key, oerr)
			}
			e.Value = v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// parseTimeLoose RFC3339Nano 宽松解析:坏值退零值(审计字段展示用,
// 不因个别坏行拖垮读取)。
func parseTimeLoose(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// Close 停止接收新写入,等单写者排干在途请求后关闭库。可重复调用。
func (s *Store) Close() error {
	s.closeOnce.Do(func() { close(s.closing) })
	<-s.done
	return s.db.Close()
}

// writerLoop 单写者循环:写请求逐项单事务提交;空闲计时器(P4-38)
// 静默超过空闲窗即转入 WAL 保养(TRUNCATE 全量回收)。保养与写同
// goroutine——checkpoint 进行期间结构上不可能并发写;写请求优先于
// 保养(有空闲写请求时先排干再保养)。恢复写入即停计时,不误伤。
func (s *Store) writerLoop() {
	defer close(s.done)
	var idleTimer *time.Timer
	stopIdle := func() {
		if idleTimer != nil {
			idleTimer.Stop()
			idleTimer = nil
		}
	}
	armIdle := func() {
		stopIdle()
		idleTimer = time.NewTimer(walKeeperIdle)
	}
	defer stopIdle()
	armIdle()
	handle := func(req writeRequest) {
		res := s.handle(req)
		req.resp <- res
		// 提交后通知观察者(P3-08):仅在事件批成功落库时;失败批与
		// 配置/token 写入不构成推送源。同步调用发生在单写者循环内,
		// 观察者必须非阻塞。
		if res.err == nil && len(res.revisions) > 0 {
			if v, ok := s.onCommit.Load().(onCommitFn); ok && v.fn != nil {
				v.fn(req.taskID, req.events)
			}
		}
		// P4-38 写后保养:暂存超阈值补一发被动回收(此刻无并发写)。
		s.walKeepPostWrite()
		armIdle()
	}
	for {
		// 写请求优先:有空闲写请求时先排干,保养让路。
		select {
		case req := <-s.writes:
			handle(req)
			continue
		default:
		}
		var idleC <-chan time.Time
		if idleTimer != nil {
			idleC = idleTimer.C
		}
		select {
		case req := <-s.writes:
			handle(req)
		case <-idleC:
			stopIdle()
			s.walKeepIdle()
			armIdle()
		case <-s.closing:
			// 无缓冲 channel:尚未完成交接的发送方会经自身的
			// closing/ctx 分支自行返回,此处直接退出不会留悬挂等待者。
			return
		}
	}
}

func (s *Store) handle(req writeRequest) writeResult {
	switch {
	case req.rebuild:
		return writeResult{err: s.withTx(rebuildProjections)}
	case req.tokens != nil:
		// 已特权清除任务的迟到用量行丢弃(P4-40 ⑥:数据已销毁,不再
		// 复活);批内其余任务照常入账。
		rows := req.tokens[:0:0]
		for _, r := range req.tokens {
			if !s.purged[r.TaskID] {
				rows = append(rows, r)
			}
		}
		return writeResult{err: s.withTx(func(tx *sql.Tx) error { return insertTokenRows(tx, rows) })}
	case req.setting != nil:
		return writeResult{err: s.withTx(func(tx *sql.Tx) error { return applySetting(tx, req.setting) })}
	case req.purge != nil:
		var rows int64
		err := s.withTx(func(tx *sql.Tx) error {
			var perr error
			rows, perr = purgeTaskTx(tx, req.purge.taskID)
			return perr
		})
		if err == nil {
			// 拒收闸落位(单写者协程内,见 Store.purged 注释)。
			s.purged[req.purge.taskID] = true
		}
		return writeResult{rows: rows, err: err}
	default:
		revs, err := s.appendTx(req.taskID, req.events, req.expectedRevision)
		return writeResult{revisions: revs, err: err}
	}
}

// applySetting 单事务落一条通用配置:upsert(带 by/updated_at 轻量审计)
// 或删除。删除不存在的键幂等成功。keepAudit(P3-09 迁移)只改 value、
// 保持原 by/updated_at(行不存在时按原审计值插入,不虚记迁移动作)。
func applySetting(tx *sql.Tx, w *settingWrite) error {
	if w.delete {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, w.key); err != nil {
			return fmt.Errorf("rings: 删除配置 %s: %w", w.key, err)
		}
		return nil
	}
	if w.keepAudit {
		res, err := tx.Exec(`UPDATE settings SET value = ? WHERE key = ?`, w.value, w.key)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				return nil
			}
		} else {
			return fmt.Errorf("rings: 迁移改写配置 %s: %w", w.key, err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO settings (key, value, updated_by, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		w.key, w.value, w.by, w.updatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("rings: 写入配置 %s: %w", w.key, err)
	}
	return nil
}

// insertTokenRows 单事务写入一批 token 用量行。occurred_at 缺省取记账时刻。
func insertTokenRows(tx *sql.Tx, rows []TokenUsage) error {
	for i, r := range rows {
		occurred := r.OccurredAt
		if occurred.IsZero() {
			occurred = time.Now().UTC()
		}
		if _, err := tx.Exec(
			`INSERT INTO token_usage (task_id, agent, model, transcript_id, turn,
			   input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, occurred_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.TaskID, r.Agent, r.Model, r.TranscriptID, r.Turn,
			r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens,
			occurred.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("rings: 写入 token 用量第 %d 行: %w", i+1, err)
		}
	}
	return nil
}

// appendTx 单事务追加一批事件并同步维护投影。
// revision 分配:任务内取当前最大值;显式指定的必须严格大于当前值
// (单调递增),重复或回退一律拒绝,UNIQUE(task_id, revision) 为物理兜底。
// 已特权清除的任务拒收(P4-40 ⑥:清除后迟到的异步写入不得让已销毁的
// 数据复活)。
func (s *Store) appendTx(taskID string, events []Event, expected *int64) ([]int64, error) {
	if s.purged[taskID] {
		return nil, ErrTaskPurged
	}
	var revisions []int64
	err := s.withTx(func(tx *sql.Tx) error {
		revisions = nil
		var next int64
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(revision), 0) FROM events WHERE task_id = ?`, taskID,
		).Scan(&next); err != nil {
			return fmt.Errorf("rings: 读取任务 %s 当前 revision: %w", taskID, err)
		}
		if expected != nil && next != *expected {
			return ErrStaleRevision
		}
		for i, ev := range events {
			rev := ev.Revision
			if rev == 0 {
				next++
				rev = next
			} else {
				if rev <= next {
					return fmt.Errorf("rings: 任务 %s 第 %d 个事件显式 revision %d 未严格大于当前 %d: %w",
						taskID, i+1, rev, next, ErrRevisionNotIncreasing)
				}
				next = rev
			}
			occurred := ev.OccurredAt
			if occurred.IsZero() {
				occurred = time.Now().UTC()
			}
			recorded := time.Now().UTC()
			payload, err := marshalPayload(ev.Payload)
			if err != nil {
				return fmt.Errorf("rings: 任务 %s 第 %d 个事件序列化负载: %w", taskID, i+1, err)
			}
			if _, err := tx.Exec(
				`INSERT INTO events (task_id, revision, kind, actor, payload, occurred_at, recorded_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				taskID, rev, string(ev.Kind), ev.Actor, payload,
				occurred.Format(time.RFC3339Nano), recorded.Format(time.RFC3339Nano),
			); err != nil {
				return fmt.Errorf("rings: 任务 %s 追加事件 rev %d: %w", taskID, rev, err)
			}
			if err := applyEvent(tx, taskID, rev, ev.Kind, ev.Actor, payload, occurred); err != nil {
				return fmt.Errorf("rings: 任务 %s 事件 rev %d 应用投影: %w", taskID, rev, err)
			}
			revisions = append(revisions, rev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revisions, nil
}

func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("rings: 开启事务: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rings: 提交事务: %w", err)
	}
	return nil
}

func marshalPayload(p any) ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return b, nil
}
