package rings

import (
	"database/sql"
	"fmt"
)

// migrations 按 PRAGMA user_version 顺次应用,已应用版本跳过(Q11:Schema
// 迁移版本化)。迁移只增不改:events 信封(task_id+revision)是全系统地基,
// 结构变更属伤筋动骨级决策(第三章 §7、第六章 §4 后路),必须先评审。
// 每个迁移是逐条独立执行的语句列表(不依赖驱动对多语句 Exec 的支持)。
var migrations = [][]string{
	{
		// v1:P1-03 年轮地基
		`
		CREATE TABLE events (
			seq         INTEGER PRIMARY KEY,
			task_id     TEXT    NOT NULL,
			revision    INTEGER NOT NULL,
			kind        TEXT    NOT NULL,
			actor       TEXT    NOT NULL,
			payload     TEXT    NOT NULL,
			occurred_at TEXT    NOT NULL,
			recorded_at TEXT    NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_events_task_rev ON events (task_id, revision)`,
		`
		CREATE TABLE nodes (
			task_id     TEXT    NOT NULL,
			node_id     TEXT    NOT NULL,
			node_type   TEXT    NOT NULL,
			status      TEXT    NOT NULL DEFAULT '',
			title       TEXT    NOT NULL DEFAULT '',
			asset_class TEXT    NOT NULL DEFAULT '',
			detail      TEXT    NOT NULL DEFAULT '{}',
			created_rev INTEGER NOT NULL,
			updated_rev INTEGER NOT NULL,
			PRIMARY KEY (task_id, node_id)
		) WITHOUT ROWID`,
		`CREATE INDEX idx_nodes_task_type ON nodes (task_id, node_type)`,
		`
		CREATE TABLE edges (
			task_id     TEXT    NOT NULL,
			edge_type   TEXT    NOT NULL,
			from_node   TEXT    NOT NULL,
			to_node     TEXT    NOT NULL,
			detail      TEXT    NOT NULL DEFAULT '{}',
			created_rev INTEGER NOT NULL,
			PRIMARY KEY (task_id, edge_type, from_node, to_node)
		) WITHOUT ROWID`,
		`CREATE INDEX idx_edges_from ON edges (task_id, from_node, edge_type)`,
		`CREATE INDEX idx_edges_to ON edges (task_id, to_node, edge_type)`,
	},
	{
		// v2:P1-04 任务生命周期投影(tasks)与 RoE 当前值投影(roe_state)。
		// 二者都是事件流的确定性投影:重放重建可完整复原。
		`
		CREATE TABLE tasks (
			task_id     TEXT    NOT NULL PRIMARY KEY,
			title       TEXT    NOT NULL DEFAULT '',
			status      TEXT    NOT NULL DEFAULT '',
			detail      TEXT    NOT NULL DEFAULT '{}',
			created_rev INTEGER NOT NULL,
			updated_rev INTEGER NOT NULL
		) WITHOUT ROWID`,
		`
		CREATE TABLE roe_state (
			task_id       TEXT    NOT NULL PRIMARY KEY,
			allowed_tools TEXT    NOT NULL DEFAULT '[]',
			denied_tools  TEXT    NOT NULL DEFAULT '[]',
			note          TEXT    NOT NULL DEFAULT '',
			updated_rev   INTEGER NOT NULL
		) WITHOUT ROWID`,
	},
	{
		// v3:P1-07 RoE 意图分级上限(第四章 §3"极高危按策略禁止"的策略侧)。
		// 旧库默认空 = 上限"利用深入"(消费侧归一,见 swarm.DefaultMaxActionLevel)。
		`ALTER TABLE roe_state ADD COLUMN max_action_level TEXT NOT NULL DEFAULT ''`,
	},
	{
		// v4:P2-04 token 独立记账表(第四章 §6 账本分工)。独立于事件流:
		// 逐次 LLM 调用的 token 用量走本表,防事件流膨胀;业务事件、会话
		// 原文(JSONL 文件)、系统事件(blackbox.record 事件流)与本表四类
		// 各归其位。表无投影、纯追加,重建投影不触碰。
		`
		CREATE TABLE token_usage (
			seq                INTEGER PRIMARY KEY,
			task_id            TEXT    NOT NULL,
			agent              TEXT    NOT NULL,
			model              TEXT    NOT NULL DEFAULT '',
			transcript_id      TEXT    NOT NULL DEFAULT '',
			turn               INTEGER NOT NULL DEFAULT 0,
			input_tokens       INTEGER NOT NULL DEFAULT 0,
			output_tokens      INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			occurred_at        TEXT    NOT NULL
		)`,
		`CREATE INDEX idx_token_usage_task ON token_usage (task_id, agent)`,
	},
	{
		// v5:P2-06b RoE 审批门槛(第四章 §3 v2 三级处置):该级及以上的
		// 意图弹门等待面板批准。旧库默认空 = 默认宽(仅增删改弹门,
		// 消费侧归一,见 swarm.DefaultApprovalThreshold)。旧列
		// max_action_level(P1-07 分级上限)随 v2 废止,保留不删(读兼容)。
		`ALTER TABLE roe_state ADD COLUMN approval_threshold TEXT NOT NULL DEFAULT ''`,
	},
	{
		// v6:P2-06c 通用配置键值表(全局共享,进程级而非任务级)。设计为
		// 通用配置框架:键为点分命名空间字符串(如 swarm.budget.max_turns),
		// 新增配置项 = 新键,无需迁移结构;键空间预留 per-model 覆盖余地
		// (model.<name>.budget.max_turns 形态)。值一律 TEXT,类型由消费方
		// 解析,解析失败退装配默认(fail-safe)。带轻量审计:谁(by)在何时
		// (updated_at)改的。写入经单写者排队(见 Store.SetSetting);
		// 键值表无投影、不进事件流(全局配置不是任务账本事实)。
		`
		CREATE TABLE settings (
			key        TEXT NOT NULL PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_by TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		) WITHOUT ROWID`,
	},
	{
		// v7:P2-06h 任务列表完整版——任务元数据投影列(task.meta 事件
		// 维护,生命周期事件不触碰):分类指派 / 置顶(含置顶次序锚
		// pinned_rev,置顶任务按其排序)/ 墓碑标记。旧库默认空 = 未分类、
		// 未置顶、未删除。分类清单本体存全局键值表(task.category.<id>),
		// 此处只存指派。
		`ALTER TABLE tasks ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN pinned_rev INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`,
	},
	{
		// v8:P2-08e 任务级拦截规则列(第四章 §4 v2.1):规则随 RoE 快照
		// 整体提交,是 roe_state"当前值"投影的组成部分。旧库默认空 =
		// 无任务级规则(判定链退化为全局硬规则 + 意图分级,行为不变)。
		`ALTER TABLE roe_state ADD COLUMN intent_rules TEXT NOT NULL DEFAULT '[]'`,
	},
	{
		// v9:P4-05 授权范围摘除清单列(问题清单 #10):资产默认全部纳入
		// 授权范围,摘除清单随 RoE 快照整体提交。旧库默认空 = 全部纳入
		//(历史语义不变:图上资产即授权域)。
		`ALTER TABLE roe_state ADD COLUMN excluded_assets TEXT NOT NULL DEFAULT '[]'`,
	},
}

// migrate 将库结构推进到最新版本。仅在 Open 时、写者启动前调用,
// 单进程单 Store 前提下无并发写者,无需排队。
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("rings: 读取 schema 版本: %w", err)
	}
	for v := version; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("rings: 开启 schema v%d 迁移事务: %w", v+1, err)
		}
		for _, stmt := range migrations[v] {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("rings: 应用 schema v%d: %w", v+1, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("rings: 写入 schema 版本 v%d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("rings: 提交 schema v%d: %w", v+1, err)
		}
	}
	return nil
}
