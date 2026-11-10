// 发现区四页签排序(P4-61,问题清单 #84)——纯排序逻辑,零 React/数据层
// 依赖,发现页四页签与 live 推送落位共用本文件唯一一组比较器(禁止在
// 任何视图另写平行排序)。.mjs 后缀:Node 测试脚本直引零配置
// (task-status.mjs 同款先例),Next 解析同源。
//
// 契约(P4-61 条目,显示键与状态词零改动——只定序):
//   ① 成果页按发现时间倒序,新的最上;发现时间=判断立账时刻(节点
//      added_at,同刻按版本号先后);重开再确认不改立账时刻,位置不变;
//      报告导出仍按严重级(R17 既有规则,面板与报告口径分离)。
//   ② 假设/事实/锤炼三页签分三区:过程态置顶(等待验证/验证中/验证
//      暂停;等待反证/反证中/反证暂停;事实页"新"标条目)、中间正常区
//      (已确认/搁置/证据不足等非废弃非过程态)、废弃沉底(已废弃/被
//      推翻);区内一律时间倒序(口径同①)。
//   ③ live 推送新增条目按同规则落位——推送驱动重拉列表,渲染走同一
//      比较器,无独立落位路径。
//
// 时间口径:nodes 投影 added_at 经 FmtLocal 下发("YYYY-MM-DD HH:MM:SS"
// 定宽),字典序即时序;facts 行字段名为 created_at(同一列),归一读取。
// 立账时刻与版本号由写侧同事务维护,单调一致——同刻按版本号只作同秒
// 多条目的确定性次序。未知显示键归正常区(如实:不属过程也不属废弃)。

// 发现时间读取:claims/hypos 行为 added_at,facts 行为 created_at;
// 缺失回退空串(由版本号次序兜底,如实不断序)。
const findingTimeOf = (row) => row.added_at || row.created_at || "";

// 时间倒序比较器(成果页与三页签区内共用,P4-61 ①):added_at 新的在
// 前;同刻版本号大的在前;全同保持稳定(返回 0)。
export function byFindingTimeDesc(a, b) {
  const ta = findingTimeOf(a);
  const tb = findingTimeOf(b);
  if (ta !== tb) return tb > ta ? 1 : -1;
  return (b.created_rev || 0) - (a.created_rev || 0);
}

// ---- 三区(P4-61 ②):0 过程态置顶 / 1 正常区 / 2 废弃沉底 ----
// 锤炼页显示键:等待反证/反证中/反证暂停=过程;证据不足=正常;
// 已废弃=沉底(机器态 refuted 与 discarded 由显示层合并为已废弃)。
const CLAIM_ZONE = {
  wait_falsify: 0, falsifying: 0, falsify_paused: 0,
  insufficient: 1,
  discarded: 2,
};
// 假设页显示键:等待验证/验证中/验证暂停=过程;搁置=正常;已废弃=沉底
//(成立条目已迁入事实页,不在本页)。
const HYP_ZONE = {
  wait_verify: 0, verifying: 0, verify_paused: 0,
  held: 1,
  discarded: 2,
};

export const claimZoneOf = (c) => CLAIM_ZONE[c.display] ?? 1;
export const hypZoneOf = (h) => HYP_ZONE[h.display] ?? 1;
// 事实页:"新"标条目视为过程态置顶;被推翻沉底(防御性词位,状态机暂无
// 该迁移,出现即归沉底);其余(已确认/由假设升级迁入)正常区。
export const factZoneOf = (f) => (f.is_new ? 0 : f.status === "refuted" ? 2 : 1);

// 区内时间倒序比较器:先比区(过程<正常<废弃),同区走 byFindingTimeDesc。
export function byClaimZonesDesc(a, b) {
  return claimZoneOf(a) - claimZoneOf(b) || byFindingTimeDesc(a, b);
}
export function byHypZonesDesc(a, b) {
  return hypZoneOf(a) - hypZoneOf(b) || byFindingTimeDesc(a, b);
}
export function byFactZonesDesc(a, b) {
  return factZoneOf(a) - factZoneOf(b) || byFindingTimeDesc(a, b);
}
