// 发现区三页签台账排序与翻页类级测试(P5-07③④;规则 15 类级验收
// 纪律):排序与分页规则语义为契约——
//   ① 三页统一落页时间倒序:意图=提出时刻(added_at)、事实/漏洞=
//      确认时刻(confirmed_at,未确认形态退 added_at),同刻版本号大的
//      在前;重开再确认随确认时刻更新位置(新的在上);
//   ② 唯一例外:证据不足灰区永远沉漏洞页底,灰区内同样时间倒序;
//   ③ 翻页:每页固定 20 条,45 条=3 页(20/20/5),越界页码钳回,
//      空集落第 1 页;搜索命中集内翻页照常(筛选只做减法)。
// 多形态样本集断言,不以单一实锤个案替代类级覆盖。挂 make web /
// make test 前置,零依赖。

import {
  LEDGER_PAGE_SIZE,
  byIntentTimeDesc,
  byLandedTimeDesc,
  byVulnLedgerDesc,
  landedTimeOf,
  ledgerPage,
  vulnZoneOf,
} from "../lib/finding-sort.mjs";

let failed = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) {
    failed++;
    console.error(`✗ ${name}\n    got  ${g}\n    want ${w}`);
  }
}

const ids = (list) => list.map((x) => x.id);
const sorted = (list, cmp) => [...list].sort(cmp);

// ---- ① 意图页:提出时刻倒序(byIntentTimeDesc) ----

// 多形态:状态任意(提出即落页,状态不参与定序)、时刻交错、同刻
// 版本号、乱序输入落位确定。
{
  const rows = [
    { id: "s-old", added_at: "2026-11-01 10:00:00", created_rev: 10, status: "done" },
    { id: "s-new", added_at: "2026-11-09 09:00:00", created_rev: 90, status: "dispatched" },
    { id: "s-mid", added_at: "2026-11-05 12:00:00", created_rev: 50, status: "failed" },
    { id: "s-edge", added_at: "2026-11-09 08:59:59", created_rev: 88, status: "running" },
  ];
  eq(
    "意图按提出时刻倒序(状态不参与)",
    ids(sorted(rows, byIntentTimeDesc)),
    ["s-new", "s-edge", "s-mid", "s-old"],
  );
}
{
  const rows = [
    { id: "t-a", added_at: "2026-11-09 10:00:00", created_rev: 12 },
    { id: "t-b", added_at: "2026-11-09 10:00:00", created_rev: 13 },
    { id: "t-c", added_at: "2026-11-09 10:00:00", created_rev: 11 },
  ];
  eq("同刻按版本号先后", ids(sorted(rows, byIntentTimeDesc)), ["t-b", "t-a", "t-c"]);
}
{
  const rows = [
    { id: "p-1", added_at: "2026-11-01 00:00:00", created_rev: 1 },
    { id: "p-2", added_at: "2026-11-02 00:00:00", created_rev: 2 },
    { id: "p-3", added_at: "2026-11-03 00:00:00", created_rev: 3 },
  ];
  const want = ["p-3", "p-2", "p-1"];
  for (const perm of [[0, 1, 2], [2, 1, 0], [1, 2, 0], [2, 0, 1], [1, 0, 2], [0, 2, 1]]) {
    eq(`乱序落位确定 perm=${perm.join("")}`, ids(sorted(perm.map((i) => rows[i]), byIntentTimeDesc)), want);
  }
}

// ---- ① 事实页/漏洞页正常区:落页(确认)时刻倒序(byLandedTimeDesc) ----

// confirmed_at 优先;未确认(证据不足等)退 added_at;created_at 兼容
// 旧行;落页时刻归一读取。
{
  const rows = [
    { id: "v-conf-old", confirmed_at: "2026-11-01 00:00:00", added_at: "2026-10-30 00:00:00", created_rev: 5 },
    { id: "v-conf-new", confirmed_at: "2026-11-09 00:00:00", added_at: "2026-11-02 00:00:00", created_rev: 40 },
    { id: "v-insuff", added_at: "2026-11-07 00:00:00", created_rev: 30, display: "insufficient" },
    { id: "v-legacy", created_at: "2026-11-04 00:00:00", created_rev: 20 },
  ];
  eq(
    "落页时刻倒序(确认优先,未确认退立账)",
    ids(sorted(rows, byLandedTimeDesc)),
    ["v-conf-new", "v-insuff", "v-legacy", "v-conf-old"],
  );
}
eq("landedTimeOf 全形态", {
  conf: landedTimeOf({ confirmed_at: "a", added_at: "b" }),
  added: landedTimeOf({ added_at: "b" }),
  legacy: landedTimeOf({ created_at: "c" }),
  bare: landedTimeOf({}),
}, { conf: "a", added: "b", legacy: "c", bare: "" });

// 重开再确认位置更新:确认时刻随最新迁移变化——重开后再确认的条目
// 以新确认时刻落位(新的在上),旧确认时刻不再生效。
{
  const before = [
    { id: "r-1", confirmed_at: "2026-11-02 08:00:00", created_rev: 20 },
    { id: "r-2", confirmed_at: "2026-11-06 08:00:00", created_rev: 60 },
  ];
  const afterReconfirm = [
    { id: "r-1", confirmed_at: "2026-11-02 08:00:00", created_rev: 20 },
    { id: "r-2", confirmed_at: "2026-11-11 09:00:00", created_rev: 60 },
  ];
  eq("重开再确认以新确认时刻置顶", ids(sorted(afterReconfirm, byLandedTimeDesc)), ["r-2", "r-1"]);
  eq("旧序对照", ids(sorted(before, byLandedTimeDesc)), ["r-2", "r-1"]);
}

// ---- ② 漏洞页灰区沉底(byVulnLedgerDesc) ----

// 证据不足灰区永远沉底(即使灰区条目确认时刻最新);灰区内时间倒序;
// 已修复与已确认同区;被推翻/已废弃不入页(数据面前置过滤,此处断言
// 比较器对正常显示键不敏感)。
{
  const rows = [
    { id: "g-ins-old", display: "insufficient", added_at: "2026-11-01 00:00:00", created_rev: 10 },
    { id: "g-conf-new", display: "confirmed", confirmed_at: "2026-11-09 00:00:00", created_rev: 90 },
    { id: "g-fixed", display: "fixed", confirmed_at: "2026-11-05 00:00:00", created_rev: 50 },
    { id: "g-ins-new", display: "insufficient", added_at: "2026-11-10 00:00:00", created_rev: 99 },
  ];
  eq(
    "灰区永远沉底,灰区内时间倒序",
    ids(sorted(rows, byVulnLedgerDesc)),
    ["g-conf-new", "g-fixed", "g-ins-new", "g-ins-old"],
  );
}
eq("vulnZoneOf 全形态", {
  ins: vulnZoneOf({ display: "insufficient" }),
  conf: vulnZoneOf({ display: "confirmed" }),
  fixed: vulnZoneOf({ display: "fixed" }),
  bare: vulnZoneOf({}),
}, { ins: 1, conf: 0, fixed: 0, bare: 0 });

// ---- ③ 翻页(ledgerPage):每页固定 20 条 ----

// 45 条 = 3 页:第 1/2 页各 20 条、末页 5 条(P5-07④ 完成标准④)。
{
  const rows = Array.from({ length: 45 }, (_, i) => ({ id: `c-${i + 1}` }));
  const p1 = ledgerPage(rows, 1);
  const p2 = ledgerPage(rows, 2);
  const p3 = ledgerPage(rows, 3);
  eq("45 条共 3 页", [p1.pages, p2.pages, p3.pages], [3, 3, 3]);
  eq("第 1 页 20 条", p1.rows.length, 20);
  eq("第 2 页 20 条", p2.rows.length, 20);
  eq("末页 5 条", p3.rows.length, 5);
  eq("末页内容恰为第 41~45 条", p3.rows.map((r) => r.id), ["c-41", "c-42", "c-43", "c-44", "c-45"]);
  eq("页切片边界不重不漏", p1.rows[19].id, "c-20");
  eq("第 2 页首条", p2.rows[0].id, "c-21");
}

// 页大小为定值 20(P5-07④:不做每页条数选择)。
eq("台账页大小定值", LEDGER_PAGE_SIZE, 20);

// 越界钳回:数据减少后页码自动回落到最后有内容的页;空集落第 1 页。
{
  eq("越界页钳回", ledgerPage(Array.from({ length: 30 }, (_, i) => ({ id: i })), 5).page, 2);
  eq("空集落第 1 页", ledgerPage([], 3).page, 1);
  eq("空集页内容为空", ledgerPage([], 1).rows, []);
  eq("不足一页", ledgerPage([{ id: 1 }, { id: 2 }], 1).pages, 1);
}

// 搜索命中集内翻页照常(P5-07⑤:命中集内排序不变、翻页照常)——
// 守恒性质:命中集逐页拼接恰为命中集本体(不重不漏),每页不超页
// 大小;页序稳定(同输入同输出)。
{
  const rows = Array.from({ length: 45 }, (_, i) => ({
    id: `m-${i + 1}`,
    hit: i % 2 === 0,
  }));
  const predicates = {
    命中偶数: (r) => r.hit,
    全留: () => true,
    全筛除: () => false,
  };
  for (const [name, P] of Object.entries(predicates)) {
    const hit = rows.filter(P);
    const collected = [];
    const first = ledgerPage(hit, 1);
    for (let p = 1; p <= first.pages; p++) {
      const pg = ledgerPage(hit, p);
      if (pg.rows.length > LEDGER_PAGE_SIZE) {
        eq(`命中集分页超页大小(${name})`, pg.rows.length, LEDGER_PAGE_SIZE);
      }
      collected.push(...pg.rows);
    }
    eq(`命中集分页守恒(${name})`, collected, hit);
    eq(`命中集页序稳定(${name})`, ledgerPage(hit, 2).rows, ledgerPage(hit, 2).rows);
  }
}

if (failed > 0) {
  console.error(`test-finding-sort: ${failed} 处断言失败(排序/分页契约)`);
  process.exit(1);
}
console.log("test-finding-sort: 排序与分页契约全过(落页倒序/灰区沉底/重开置顶/乱序落位/45 条 3 页/越界钳回/筛选交换)");
