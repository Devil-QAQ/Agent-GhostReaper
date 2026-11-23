// 发现区三页签台账排序与翻页(P5-07③④;纯排序/分页逻辑,零 React/
// 数据层依赖,发现页与 live 推送落位共用本文件唯一一组比较器,禁止
// 在任何视图另写平行排序)。.mjs 后缀:Node 测试脚本直引零配置
//(task-status.mjs 同款先例),Next 解析同源。
//
// 契约(P5-07 子计划第三节,只定序与页切片,显示键与状态词零改动):
//   ① 三页统一落页时间倒序:意图=提出时刻(added_at)、事实=确认时刻
//      (confirmed_at)、漏洞=确认时刻(confirmed_at;证据不足等未确认
//      形态退立账时刻 added_at——落页时刻归一);同刻版本号大的在前;
//      重开再确认随确认时刻更新位置(新确认的在上)。
//   ② 唯一例外:证据不足灰区永远沉漏洞页底,灰区内同样时间倒序。
//   ③ 每页固定 20 条,底部页码+总数;搜索命中集内排序不变、翻页照常
//      (筛选只做减法,排序与分页口径不因筛选而变)。
//
// 时间口径:nodes 投影时刻经 FmtLocal 下发("YYYY-MM-DD HH:MM:SS"
// 定宽),字典序即时序;立账时刻与版本号由写侧同事务维护,单调一致。

// 台账页固定每页条数(P5-07④:定值,不做每页条数选择)。
export const LEDGER_PAGE_SIZE = 20;

// 落页时刻读取:确认时刻优先(confirmed_at),未确认形态退立账时刻
//(added_at),再退 created_at 兼容旧行;缺失空串由版本号次序兜底。
export const landedTimeOf = (row) => row.confirmed_at || row.added_at || row.created_at || "";

// 立账时刻读取(意图页排序键:提出时刻)。
const findingTimeOf = (row) => row.added_at || row.created_at || "";

// 时间倒序比较器(通用:时刻字段名由取值函数给定;同刻版本号大的
// 在前,全同保持稳定)。
const byTimeDescWith = (timeOf) => (a, b) => {
  const ta = timeOf(a);
  const tb = timeOf(b);
  if (ta !== tb) return tb > ta ? 1 : -1;
  return (b.created_rev || 0) - (a.created_rev || 0);
};

// 意图页比较器:提出时刻倒序(added_at)。
export const byIntentTimeDesc = byTimeDescWith(findingTimeOf);

// 事实页/漏洞页正常区比较器:落页(确认)时刻倒序。
export const byLandedTimeDesc = byTimeDescWith(landedTimeOf);

// ---- 漏洞页灰区:0 正常区 / 1 证据不足灰区(唯一非成功态,永远沉底) ----
export const vulnZoneOf = (row) => (row.display === "insufficient" ? 1 : 0);

// 漏洞页比较器:先比区(灰区沉底),同区落页时间倒序。
export function byVulnLedgerDesc(a, b) {
  return vulnZoneOf(a) - vulnZoneOf(b) || byLandedTimeDesc(a, b);
}

// ledgerPage 台账页切片(P5-07④):page 从 1 起,越界钳到最后有内容的
// 页(数据减少时页码自动回落,空集落第 1 页);返回当前页条目与总页数。
export function ledgerPage(rows, page, size = LEDGER_PAGE_SIZE) {
  const pages = Math.max(1, Math.ceil(rows.length / Math.max(1, size)));
  const cur = Math.min(Math.max(1, page), pages);
  const start = (cur - 1) * size;
  return { rows: rows.slice(start, start + size), page: cur, pages };
}
