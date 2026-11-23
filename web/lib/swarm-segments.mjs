// 虫群会话列表四段式(P5-06,设计稿第九节)——纯归类与排序逻辑,零
// React/数据层依赖,会话页虫群列表唯一一组归类/排序函数(禁止在任何
// 视图另写平行实现)。.mjs 后缀:Node 测试脚本直引零配置
// (finding-sort.mjs 同款先例),Next 解析同源。
//
// 契约(只定段与序,显示词走 api.js 词表,本文件零状态词):
//   ① 四段从上到下:执行中 → 等待(等待审批/等待模型/已暂停——挂等
//      宽限期内,wait 字段与 paused 标记判定,P5-05 供数)→ 排队中
//      (排队条目 + 让位趴窝/已唤醒会话行,一律"排队中"不标原因)→
//      已完成(收场会话)。
//   ② 段内排序:执行中/等待/已完成按最后一次活动时间倒序(last_active,
//      缺失退 started_at——谁最近动过谁在上,活跃老单位不沉底);置顶
//      (P3-13b ②)在段内提至最前(置顶间保持原相对次序);排队中按
//      后端供的上位序原样保序(StepLaunchLess 同源,队首最上——调度序
//      时间序排不了,置顶不得插队歪曲领取次序)。
//   ③ 时间口径:后端 last_active/started_at 为本地定宽格式
//      ("YYYY-MM-DD HH:MM:SS"),字典序即时序。

// 排队段状态集(dispatched=排队条目/已唤醒待领取;停等态四族=让位趴窝,
// 显示一律"排队中"不标原因——2026-11-16 造物主定)。
const QUEUE_STATES = new Set([
  "dispatched",
  "scope_wait",
  "gate_wait",
  "pause_wait",
  "model_wait",
]);

// waitKindOf 挂等宽限期的等待种类("paused"=人工暂停闩/"approval"=
// 等待审批/"model"=等待模型;null=不在等待态)。只在跑单位判定——
// 已收场/排队行不亮等待词(词表翻译归消费面 WAIT_KINDS/UNIT_STATUS)。
export function waitKindOf(u) {
  if (!u || !u.running) return null;
  if (u.paused) return "paused";
  return u.wait || null;
}

// segmentOf 行 → 四段之一(显示键与状态词零改动,只定归属)。
export function segmentOf(u) {
  if (!u) return "done";
  if (u.queued) return "queued";
  if (!u.running) return QUEUE_STATES.has(u.status) ? "queued" : "done";
  return waitKindOf(u) ? "waiting" : "running";
}

// 最后一次活动时间(倒序排序键;缺失退开始时刻,再缺退空串保底不炸)。
const activityOf = (u) => u.last_active || u.started_at || "";

// byActivityDesc 活动时间倒序(执行中/等待/已完成段内共用);全同保持
// 稳定(后端序兜底)。
export function byActivityDesc(a, b) {
  const ta = activityOf(a);
  const tb = activityOf(b);
  if (ta !== tb) return tb > ta ? 1 : -1;
  return 0;
}

// 段内置顶提最前(置顶间保持原相对次序;仅活动时间排序段使用)。
const pinnedFirst = (list) => [
  ...list.filter((u) => u.meta?.pinned),
  ...list.filter((u) => !u.meta?.pinned),
];

// swarmSegments 行集 → 四段分组:执行中/等待/已完成按(置顶,活动时间
// 倒序)定序,排队中原样保序(后端上位序);输入行序无关(活动段重排,
// 排队段以输入序为准)。
export function swarmSegments(rows) {
  const segs = { running: [], waiting: [], queued: [], done: [] };
  for (const u of rows || []) segs[segmentOf(u)].push(u);
  for (const key of ["running", "waiting", "done"]) {
    segs[key] = pinnedFirst(segs[key].sort(byActivityDesc));
  }
  return segs;
}
