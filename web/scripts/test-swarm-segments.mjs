// 虫群会话列表四段式类级测试(P5-06,规则 15 类级验收纪律):归类与
// 排序语义为契约——四段归属(执行中/等待/排队中/已完成)、等待词判定
//(等待审批/等待模型/已暂停只在跑单位亮)、活动时间倒序(活跃老单位
// 不沉底)、置顶段内提最前(置顶间保序)、排队段上位序原样保序(后端
// StepLaunchLess 同源,不自算)、缺字段容错(无 last_active 退
// started_at,再缺退空串不炸)。多形态样本集断言,不以单一实锤个案
// 替代类级覆盖。挂 make web / make test 前置,零依赖。

import { segmentOf, swarmSegments, waitKindOf, byActivityDesc } from "../lib/swarm-segments.mjs";

let failed = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) {
    failed++;
    console.error(`✗ ${name}\n    got  ${g}\n    want ${w}`);
  }
}

// ---- ① 四段归属:状态 × 进程标记多形态 ----
{
  const cases = [
    // [名, 行, 期望段]
    ["在跑无等待→执行中", { running: true, status: "running" }, "running"],
    ["在跑带审批等待→等待", { running: true, status: "running", wait: "approval" }, "waiting"],
    ["在跑带模型等待→等待", { running: true, status: "running", wait: "model" }, "waiting"],
    ["在跑带暂停闩→等待", { running: true, status: "running", paused: true }, "waiting"],
    ["排队条目→排队中", { queued: true, status: "dispatched" }, "queued"],
    // 让位停等态四族(会话行,不在跑)→排队中,一律不标原因。
    ["范围停等趴窝→排队中", { running: false, status: "scope_wait" }, "queued"],
    ["弹门停等趴窝→排队中", { running: false, status: "gate_wait" }, "queued"],
    ["暂停让位趴窝→排队中", { running: false, status: "pause_wait" }, "queued"],
    ["模型让位趴窝→排队中", { running: false, status: "model_wait" }, "queued"],
    ["已唤醒待领取会话行→排队中", { running: false, status: "dispatched" }, "queued"],
    // 收场终态族→已完成(徽标词各异,段归属同一)。
    ["正常完成→已完成", { running: false, status: "done" }, "done"],
    ["预算耗尽→已完成", { running: false, status: "exhausted" }, "done"],
    ["失败→已完成", { running: false, status: "failed" }, "done"],
    ["已中止→已完成", { running: false, status: "aborted" }, "done"],
    ["审批拒绝终止→已完成", { running: false, status: "approval_denied" }, "done"],
    ["受阻→已完成", { running: false, status: "blocked" }, "done"],
    // 已收场行不亮等待词:wait/paused 是在飞进程态,只对 running 生效。
    ["已收场带残留 wait 字段→按状态归段", { running: false, status: "done", wait: "approval" }, "done"],
    ["空行防御→已完成", null, "done"],
  ];
  for (const [name, row, want] of cases) eq(`归属·${name}`, segmentOf(row), want);
}

// ---- ② 等待词判定:waitKindOf 只在跑单位判 ----
{
  eq("等待词·暂停闩", waitKindOf({ running: true, paused: true }), "paused");
  eq("等待词·审批", waitKindOf({ running: true, wait: "approval" }), "approval");
  eq("等待词·模型", waitKindOf({ running: true, wait: "model" }), "model");
  eq("等待词·活动中为空", waitKindOf({ running: true, wait: "" }), null);
  eq("等待词·不在跑不判", waitKindOf({ running: false, paused: true, wait: "approval" }), null);
}

// ---- ③ 活动时间倒序:活跃老单位不沉底 ----
{
  const rows = [
    { id: "new-idle", session_id: "a", running: true, status: "running", started_at: "2026-11-17 10:00:00", last_active: "2026-11-17 10:00:05" },
    // 长跑老单位:开始早但最近仍在动——应在段顶(谁最近动过谁在上)。
    { id: "old-active", session_id: "b", running: true, status: "running", started_at: "2026-11-16 08:00:00", last_active: "2026-11-17 10:40:00" },
    { id: "mid", session_id: "c", running: true, status: "running", started_at: "2026-11-17 09:00:00", last_active: "2026-11-17 09:59:59" },
  ];
  const segs = swarmSegments(rows);
  eq(
    "执行中按活动时间倒序(老单位活跃在顶)",
    segs.running.map((u) => u.id),
    ["old-active", "new-idle", "mid"],
  );
}

// 缺 last_active 退 started_at;全缺退空串不炸(稳定保后端序)。
{
  eq(
    "缺活动时刻退开始时刻",
    byActivityDesc(
      { started_at: "2026-11-17 10:00:00" },
      { last_active: "2026-11-17 09:00:00", started_at: "2026-11-17 09:00:00" },
    ),
    -1,
  );
  eq("字段全缺不炸", byActivityDesc({}, {}), 0);
  const segs = swarmSegments([
    { session_id: "x", running: true, status: "running", started_at: "2026-11-17 10:00:00" },
    { session_id: "y", running: true, status: "running" },
  ]);
  eq("无时刻行退开始序在后", segs.running.map((u) => u.session_id), ["x", "y"]);
}

// ---- ④ 置顶段内提最前(置顶间保持原相对次序;排队段不吃置顶) ----
{
  const rows = [
    { id: "plain-new", session_id: "a", running: true, status: "running", last_active: "2026-11-17 11:00:00", meta: {} },
    { id: "pin-old", session_id: "b", running: true, status: "running", last_active: "2026-11-17 08:00:00", meta: { pinned: true } },
    { id: "pin-older", session_id: "c", running: false, status: "done", last_active: "2026-11-15 08:00:00", meta: { pinned: true } },
    { id: "done-new", session_id: "d", running: false, status: "done", last_active: "2026-11-17 09:00:00", meta: {} },
  ];
  const segs = swarmSegments(rows);
  eq("执行中置顶提最前", segs.running.map((u) => u.id), ["pin-old", "plain-new"]);
  eq("已完成置顶提最前(置顶间保序)", segs.done.map((u) => u.id), ["pin-older", "done-new"]);
}

// ---- ⑤ 排队段上位序原样保序(不自算;队首最上由后端供) ----
{
  // 后端序:已批准待上位(会话行)→ 普通排队条目 → 停等趴窝(会话行);
  // 乱序输入的其余段重排,排队段必须原样。
  const rows = [
    { id: "q-normal", queued: true, status: "dispatched" },
    { id: "s-head", queued: false, running: false, status: "dispatched", session_id: "s1", last_active: "2026-11-17 07:00:00", meta: { pinned: true } },
    { id: "q-second", queued: true, status: "dispatched" },
    { id: "s-park", queued: false, running: false, status: "pause_wait", session_id: "s2", last_active: "2026-11-17 12:00:00" },
  ];
  const segs = swarmSegments(rows);
  eq(
    "排队段按输入序保序(置顶/时刻均不插队)",
    segs.queued.map((u) => u.id),
    ["q-normal", "s-head", "q-second", "s-park"],
  );
}

// ---- ⑥ 分组完整性:每行恰入一段 ----
{
  const rows = [
    { queued: true, status: "dispatched" },
    { running: true, status: "running" },
    { running: true, status: "running", wait: "approval" },
    { running: true, status: "running", paused: true },
    { running: false, status: "gate_wait" },
    { running: false, status: "done" },
  ];
  const segs = swarmSegments(rows);
  const total =
    segs.running.length + segs.waiting.length + segs.queued.length + segs.done.length;
  eq("分组守恒(每行恰一段)", total, rows.length);
  eq("等待段收齐挂等三形态", segs.waiting.length, 2);
}

// ---- ⑦ 单列序(2026-11-17 造物主定:分区呈现取消):执行中在前 → 等待
// → 排队中 → 已完成沉底;同类内规则不变(活动倒序/上位序保序)。 ----
{
  // 多状态混排输入(乱序给),单列拼接序对照;同类内——执行中活跃在上
  //(old-active 活动时刻最新在首)、排队段后端上位序原样(head 行进队首
  // 档在前)。
  const rows = [
    { id: "done-b", session_id: "d1", running: false, status: "done", last_active: "2026-11-17 18:00:00" },
    { id: "q-plain", queued: true, status: "dispatched" },
    { id: "run-old-active", session_id: "r1", running: true, status: "running", started_at: "2026-11-15 08:00:00", last_active: "2026-11-17 12:00:00" },
    { id: "wait-model", session_id: "w1", running: true, status: "running", wait: "model", last_active: "2026-11-17 10:00:00" },
    { id: "q-head-sess", session_id: "q1", running: false, status: "dispatched", last_active: "2026-11-17 09:00:00" },
    { id: "done-a", session_id: "d2", running: false, status: "aborted", last_active: "2026-11-17 16:00:00" },
    { id: "run-new-idle", session_id: "r2", running: true, status: "running", started_at: "2026-11-17 11:00:00", last_active: "2026-11-17 11:00:05" },
  ];
  const segs = swarmSegments(rows);
  const single = [...segs.running, ...segs.waiting, ...segs.queued, ...segs.done];
  eq(
    "单列序:执行中→等待→排队中→已完成沉底(同类内活跃在上;排队段输入=后端上位序原样)",
    single.map((u) => u.id),
    ["run-old-active", "run-new-idle", "wait-model", "q-plain", "q-head-sess", "done-b", "done-a"],
  );
}

if (failed > 0) {
  console.error(`test-swarm-segments: ${failed} 处断言失败`);
  process.exit(1);
}
console.log("test-swarm-segments: 四段归类/排序类级断言全过(P5-06)");
