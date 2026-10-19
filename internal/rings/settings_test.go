package rings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/secrets"
)

func openSettingsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSettingsKV 通用配置键值表(P2-06c,schema v6):upsert 带轻量审计
// (by/updated_at)、覆盖更新、缺省读 ok=false、删除与幂等删除、空键/空值
// 受控拒绝。
func TestSettingsKV(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()

	// 缺省读:ok=false 无错误。
	if _, ok, err := store.Setting(ctx, "swarm.budget.max_turns"); err != nil || ok {
		t.Fatalf("缺省读应为 ok=false 无错误: ok=%v err=%v", ok, err)
	}
	// 首写。
	if err := store.SetSetting(ctx, "swarm.budget.max_turns", "30", "creator"); err != nil {
		t.Fatalf("首写失败: %v", err)
	}
	e, ok, err := store.Setting(ctx, "swarm.budget.max_turns")
	if err != nil || !ok {
		t.Fatalf("读回失败: ok=%v err=%v", ok, err)
	}
	if e.Key != "swarm.budget.max_turns" || e.Value != "30" || e.UpdatedBy != "creator" {
		t.Fatalf("读回字段不符: %+v", e)
	}
	if e.UpdatedAt.IsZero() || time.Since(e.UpdatedAt) > time.Minute {
		t.Fatalf("updated_at 应为写入时刻附近: %v", e.UpdatedAt)
	}
	firstAt := e.UpdatedAt
	// 覆盖更新:值与审计字段都前进。
	time.Sleep(2 * time.Millisecond) // 保证时间戳可区分
	if err := store.SetSetting(ctx, "swarm.budget.max_turns", "40", "creator"); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	if e, ok, _ := store.Setting(ctx, "swarm.budget.max_turns"); !ok || e.Value != "40" || !e.UpdatedAt.After(firstAt) {
		t.Fatalf("覆盖后读回不符: ok=%v %+v", ok, e)
	}
	// 删除:键消失;重复删除幂等。
	if err := store.DeleteSetting(ctx, "swarm.budget.max_turns"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok, err := store.Setting(ctx, "swarm.budget.max_turns"); err != nil || ok {
		t.Fatalf("删除后应为缺省: ok=%v err=%v", ok, err)
	}
	if err := store.DeleteSetting(ctx, "swarm.budget.max_turns"); err != nil {
		t.Fatalf("幂等删除失败: %v", err)
	}
	// 受控拒绝:空键、空值。
	if err := store.SetSetting(ctx, "", "1", "creator"); err != ErrSettingKeyEmpty {
		t.Fatalf("空键应拒: %v", err)
	}
	if err := store.SetSetting(ctx, "k", "", "creator"); err != ErrSettingValueEmpty {
		t.Fatalf("空值应拒: %v", err)
	}
	if _, _, err := store.Setting(ctx, ""); err != ErrSettingKeyEmpty {
		t.Fatalf("空键读应拒: %v", err)
	}
}

// TestSettingsFrameworkExtensible 配置框架可扩展(完成标准):新配置项 =
// 新键直接读写,不触碰任何结构迁移——心跳间隔、per-model 覆盖等后续
// 设置项按同一键空间直接落。
func TestSettingsFrameworkExtensible(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()
	for _, k := range []string{
		"swarm.heartbeat.interval_seconds",
		"swarm.anchor.every_steps",
		"model.glm-5.3.budget.max_turns", // per-model 覆盖预留形态
	} {
		if err := store.SetSetting(ctx, k, "42", "creator"); err != nil {
			t.Fatalf("写新键 %s 失败: %v", k, err)
		}
		if e, ok, err := store.Setting(ctx, k); err != nil || !ok || e.Value != "42" {
			t.Fatalf("读新键 %s 不符: ok=%v err=%v %+v", k, ok, err, e)
		}
	}
}

// TestRoeIntentRulesProjection 任务级拦截规则随 roe.updated 快照落投影
// (P2-08e,schema v8):词表与结构校验 + CurrentRoE 读回一致;旧负载
// (无 intent_rules 字段)重放不受影响。
func TestRoeIntentRulesProjection(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()
	const task = "roe-rules"

	rules := []IntentRule{
		{ID: "ir-1", Action: string(IntentDataRead), AssetIDs: []string{"a1"}, Disposition: IntentForbid},
		{ID: "ir-2", Action: string(IntentUpload), AssetIDs: []string{"a1", "a2"}, Disposition: IntentAllow, Quota: 5, Note: "限次"},
	}
	if _, err := store.Append(ctx, task, Event{
		Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{IntentRules: rules},
	}); err != nil {
		t.Fatalf("合法规则快照入账失败: %v", err)
	}
	st, err := store.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("读 RoE 失败: %v", err)
	}
	if len(st.IntentRules) != 2 || st.IntentRules[0].ID != "ir-1" ||
		st.IntentRules[1].Quota != 5 || st.IntentRules[1].Action != string(IntentUpload) {
		t.Fatalf("规则投影读回不符: %+v", st.IntentRules)
	}

	// 结构校验(审核迭代三轮:动作描述自由文本,不再校验词表):
	// 空动作描述 / 坏处置 / 负上限 / 禁止携上限 / 重复 ID。
	bad := []RoeUpdated{
		{IntentRules: []IntentRule{{ID: "x", Action: "", Disposition: IntentForbid}}},
		{IntentRules: []IntentRule{{ID: "x", Action: "爬取", Disposition: "sometimes"}}},
		{IntentRules: []IntentRule{{ID: "x", Action: string(IntentCrawl), Disposition: "sometimes"}}},
		{IntentRules: []IntentRule{{ID: "x", Action: string(IntentCrawl), Disposition: IntentAllow, Quota: -1}}},
		{IntentRules: []IntentRule{{ID: "x", Action: string(IntentCrawl), Disposition: IntentForbid, Quota: 3}}},
		{IntentRules: []IntentRule{
			{ID: "x", Action: string(IntentCrawl), Disposition: IntentForbid},
			{ID: "x", Action: string(IntentCrawl), Disposition: IntentForbid},
		}},
	}
	for i, b := range bad {
		if _, err := store.Append(ctx, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: b}); err == nil {
			t.Fatalf("第 %d 条非法快照应被投影入口拒绝", i+1)
		}
	}

	// 旧负载(无规则字段)照常入账,读回空清单。
	if _, err := store.Append(ctx, task, Event{
		Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{DeniedTools: []string{"bash"}},
	}); err != nil {
		t.Fatalf("旧形态快照入账失败: %v", err)
	}
	st, err = store.CurrentRoE(ctx, task)
	if err != nil || len(st.IntentRules) != 0 || len(st.DeniedTools) != 1 {
		t.Fatalf("快照覆盖语义不符: %+v err=%v", st, err)
	}
}

// TestGuardRecordValidation 拦截判定记录的受控入口校验(P2-08e):
// 类型词表、主键字段、计数记录的规则锚与序号。
func TestGuardRecordValidation(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()
	const task = "guard-rec"

	ok := GuardRecord{StepID: "s1", UnitID: "u1", Type: GuardRecQuotaCount,
		RuleID: "ir-1", Action: "上传", Nth: 1, ToolName: "bash"}
	if _, err := store.Append(ctx, task, Event{Kind: EventGuardRecord, Actor: "swarm:u1", Payload: ok}); err != nil {
		t.Fatalf("合法计数记录入账失败: %v", err)
	}
	bad := []GuardRecord{
		{StepID: "s1", UnitID: "u1", Type: "mystery"},
		{StepID: "", UnitID: "u1", Type: GuardRecQuotaCount, RuleID: "ir", Nth: 1},
		{StepID: "s1", UnitID: "u1", Type: GuardRecQuotaCount, Nth: 1},               // 缺 rule_id
		{StepID: "s1", UnitID: "u1", Type: GuardRecQuotaCount, RuleID: "ir", Nth: 0}, // 序号非法
	}
	for i, b := range bad {
		if _, err := store.Append(ctx, task, Event{Kind: EventGuardRecord, Actor: "swarm:u1", Payload: b}); err == nil {
			t.Fatalf("第 %d 条非法记录应被拒收", i+1)
		}
	}
}

// ---- 静态加密(P3-09) ----

// testCipher 测试用固定密钥的封装器。
func testCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

// rawSettingValue 直读库内原始值(绕过解封,验证存储层密文用)。
func rawSettingValue(t *testing.T, s *Store, key string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatalf("直读 %s: %v", key, err)
	}
	return v
}

// TestAtRestSealOnWrite 敏感键写入即封装(P3-09):库内原始值为 v1 密文
// 且不含明文;读回(Setting / SettingsWithPrefix)透明解封与原文一致;
// 非敏感键不受影响。
func TestAtRestSealOnWrite(t *testing.T) {
	store := openSettingsStore(t)
	if n, err := store.EnableAtRest(context.Background(), testCipher(t)); err != nil || n != 0 {
		t.Fatalf("EnableAtRest: n=%d err=%v", n, err)
	}
	ctx := context.Background()

	if err := store.SetSetting(ctx, "model.library.gpt", `{"api_key":"sk-plain-secret"}`, "creator"); err != nil {
		t.Fatalf("写敏感键失败: %v", err)
	}
	raw := rawSettingValue(t, store, "model.library.gpt")
	if !secrets.IsSealed(raw) {
		t.Fatalf("库内应为密文: %q", raw)
	}
	if strings.Contains(raw, "sk-plain-secret") {
		t.Fatal("库内密文含明文")
	}
	if e, ok, err := store.Setting(ctx, "model.library.gpt"); err != nil || !ok || e.Value != `{"api_key":"sk-plain-secret"}` {
		t.Fatalf("读回应透明解封: ok=%v err=%v %+v", ok, err, e)
	}
	entries, err := store.SettingsWithPrefix(ctx, "model.library.")
	if err != nil || len(entries) != 1 || entries[0].Value != `{"api_key":"sk-plain-secret"}` {
		t.Fatalf("前缀读应透明解封: err=%v entries=%+v", err, entries)
	}

	// 非敏感键:明文入库,读回原样。
	if err := store.SetSetting(ctx, "auth.password", "pbkdf2-record", "creator"); err != nil {
		t.Fatalf("写非敏感键失败: %v", err)
	}
	if raw := rawSettingValue(t, store, "auth.password"); raw != "pbkdf2-record" {
		t.Fatalf("非敏感键不应封装: %q", raw)
	}
}

// TestAtRestMigrateLegacy 存量明文迁移(P3-09):启用前落库的敏感明文,
// 启用后改写为密文且 by/updated_at 保持原值;已封装的不再重复迁移;
// 非敏感明文不动。
func TestAtRestMigrateLegacy(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()
	if err := store.SetSetting(ctx, "model.library.old", `{"api_key":"sk-legacy"}`, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "cred.c-1", `{"secret":"hunter2-long"}`, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "auth.password", "hash-record", "carol"); err != nil {
		t.Fatal(err)
	}
	legacyAt := func() time.Time {
		e, _, _ := store.Setting(ctx, "model.library.old")
		return e.UpdatedAt
	}
	before := legacyAt()

	n, err := store.EnableAtRest(ctx, testCipher(t))
	if err != nil {
		t.Fatalf("EnableAtRest: %v", err)
	}
	if n != 2 {
		t.Fatalf("应迁移 2 条敏感明文,得 %d", n)
	}
	if raw := rawSettingValue(t, store, "model.library.old"); !secrets.IsSealed(raw) || strings.Contains(raw, "sk-legacy") {
		t.Fatalf("迁移后应为密文: %q", raw)
	}
	if raw := rawSettingValue(t, store, "cred.c-1"); !secrets.IsSealed(raw) || strings.Contains(raw, "hunter2-long") {
		t.Fatalf("迁移后应为密文: %q", raw)
	}
	if raw := rawSettingValue(t, store, "auth.password"); raw != "hash-record" {
		t.Fatalf("非敏感键不应被迁移: %q", raw)
	}
	// 审计保持:by 与 updated_at 为原值(内容未变,不虚记迁移动作)。
	if e, ok, _ := store.Setting(ctx, "model.library.old"); !ok || e.UpdatedBy != "alice" || !e.UpdatedAt.Equal(before) {
		t.Fatalf("迁移不应改审计字段: %+v(want by=alice at=%v)", e, before)
	}
	// 读回明文一致。
	if e, ok, _ := store.Setting(ctx, "model.library.old"); !ok || e.Value != `{"api_key":"sk-legacy"}` {
		t.Fatalf("迁移后读回不符: %+v", e)
	}
	// 二次启用:已封装的不再迁移。
	if n, err := store.EnableAtRest(ctx, testCipher(t)); err != nil || n != 0 {
		t.Fatalf("二次启用应零迁移: n=%d err=%v", n, err)
	}
}

// TestAtRestWrongKey 异密钥读敏感值:解封失败如实报错(不静默丢配置)。
func TestAtRestWrongKey(t *testing.T) {
	store := openSettingsStore(t)
	ctx := context.Background()
	if _, err := store.EnableAtRest(ctx, testCipher(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "cred.c-9", `{"secret":"s3cr3t-value"}`, "creator"); err != nil {
		t.Fatal(err)
	}
	other, err := secrets.NewCipher([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	store.atRest = other
	if _, _, err := store.Setting(ctx, "cred.c-9"); err == nil {
		t.Fatal("异密钥读应报错")
	}
	if _, err := store.SettingsWithPrefix(ctx, "cred."); err == nil {
		t.Fatal("异密钥前缀读应报错")
	}
}
