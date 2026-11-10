// 发现区四页签排序类级测试(P4-61,问题清单 #84;规则 15 类级验收纪律):
// 排序规则语义为契约——时间倒序口径(立账时刻 added_at,同刻版本号)、
// 三区归类(过程置顶/正常/废弃沉底)、区优先于时间、重开再确认位置不变
//(排序键只有 added_at+created_rev,重开只改状态)、乱序输入落位确定
//(live 重拉同口径)、筛选与排序可交换(筛选后排序规则保持)。
// 多形态样本集断言:各显示键全键位 × 时间先后 × 平局 × 未知键防御位,
// 不以单一实锤个案替代类级覆盖。挂 make web / make test 前置,零依赖。

import {
  byFindingTimeDesc,
  byClaimZonesDesc,
  byHypZonesDesc,
  byFactZonesDesc,
  claimZoneOf,
  hypZoneOf,
  factZoneOf,
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

// ---- ① 成果页时间倒序(byFindingTimeDesc) ----

// 多形态:级别四档 × 时间先后交错——级别不参与定序(替换原按级别排的
// 类级证据:critical 旧于 low,low 在上)。
{
  const rows = [
    { id: "c-crit-old", added_at: "2026-11-01 10:00:00", created_rev: 10, severity: "critical" },
    { id: "c-low-new", added_at: "2026-11-09 09:00:00", created_rev: 90, severity: "low" },
    { id: "c-mid-mid", added_at: "2026-11-05 12:00:00", created_rev: 50, severity: "medium" },
    { id: "c-high-edge", added_at: "2026-11-09 08:59:59", created_rev: 88, severity: "high" },
    { id: "c-fixed-old", added_at: "2026-10-30 00:00:00", created_rev: 5, severity: "high", display: "fixed" },
  ];
  eq(
    "成果按发现时间倒序(级别不参与)",
    ids(sorted(rows, byFindingTimeDesc)),
    ["c-low-new", "c-high-edge", "c-mid-mid", "c-crit-old", "c-fixed-old"],
  );
}

// 同刻多条目:版本号大的在前(确定性次序)。
{
  const rows = [
    { id: "t-a", added_at: "2026-11-09 10:00:00", created_rev: 12 },
    { id: "t-b", added_at: "2026-11-09 10:00:00", created_rev: 13 },
    { id: "t-c", added_at: "2026-11-09 10:00:00", created_rev: 11 },
  ];
  eq("同刻按版本号先后", ids(sorted(rows, byFindingTimeDesc)), ["t-b", "t-a", "t-c"]);
}

// 重开再确认位置不变:排序键只有立账时刻与版本号——状态/级别/复测
// 结论等一切后续字段变化不改变相对位置(类级:任何后续字段组合)。
{
  const before = [
    { id: "r-1", added_at: "2026-11-02 08:00:00", created_rev: 20, display: "confirmed", severity: "high" },
    { id: "r-2", added_at: "2026-11-06 08:00:00", created_rev: 60, display: "confirmed", severity: "low" },
  ];
  const afterReconfirm = [
    { id: "r-1", added_at: "2026-11-02 08:00:00", created_rev: 20, display: "confirmed", severity: "low", retest_verdict: "unfixed" },
    { id: "r-2", added_at: "2026-11-06 08:00:00", created_rev: 60, display: "fixed", severity: "critical", retest_verdict: "fixed" },
  ];
  eq(
    "重开再确认位置不变(后续字段任意变化)",
    ids(sorted(afterReconfirm, byFindingTimeDesc)),
    ids(sorted(before, byFindingTimeDesc)),
  );
}

// 乱序输入落位确定(live 推送重拉同口径):任意排列进、同一序列出。
{
  const rows = [
    { id: "s-1", added_at: "2026-11-01 00:00:00", created_rev: 1 },
    { id: "s-2", added_at: "2026-11-02 00:00:00", created_rev: 2 },
    { id: "s-3", added_at: "2026-11-03 00:00:00", created_rev: 3 },
  ];
  const want = ["s-3", "s-2", "s-1"];
  for (const perm of [
    [0, 1, 2], [2, 1, 0], [1, 2, 0], [2, 0, 1], [1, 0, 2], [0, 2, 1],
  ]) {
    eq(`乱序落位确定 perm=${perm.join("")}`, ids(sorted(perm.map((i) => rows[i]), byFindingTimeDesc)), want);
  }
}

// facts 行字段名为 created_at(同一列):归一读取,与 added_at 同口径。
{
  const rows = [
    { id: "f-at", added_at: "2026-11-04 00:00:00", created_rev: 4 },
    { id: "f-cat", created_at: "2026-11-07 00:00:00", created_rev: 7 },
  ];
  eq("created_at 同口径", ids(sorted(rows, byFindingTimeDesc)), ["f-cat", "f-at"]);
}

// ---- ② 锤炼页三区(byClaimZonesDesc) ----

// 全键位 × 时间交错:区优先于时间——最旧的过程态仍置顶,最新的已废弃
// 仍沉底;区内时间倒序。
{
  const rows = [
    { id: "g-insuff-mid", display: "insufficient", added_at: "2026-11-07 00:00:00", created_rev: 70 },
    { id: "g-disc-new", display: "discarded", added_at: "2026-11-09 00:00:00", created_rev: 90 },
    { id: "g-wait-oldest", display: "wait_falsify", added_at: "2026-11-01 00:00:00", created_rev: 10 },
    { id: "g-pause-mid", display: "falsify_paused", added_at: "2026-11-05 00:00:00", created_rev: 50 },
    { id: "g-run-new", display: "falsifying", added_at: "2026-11-06 00:00:00", created_rev: 60 },
    { id: "g-insuff-old", display: "insufficient", added_at: "2026-11-02 00:00:00", created_rev: 20 },
    { id: "g-disc-old", display: "discarded", added_at: "2026-11-03 00:00:00", created_rev: 30 },
  ];
  eq(
    "锤炼三区:过程置顶/正常居中/废弃沉底,区内时间倒序",
    ids(sorted(rows, byClaimZonesDesc)),
    ["g-run-new", "g-pause-mid", "g-wait-oldest", "g-insuff-mid", "g-insuff-old", "g-disc-new", "g-disc-old"],
  );
}

// 区归类逐键断言(显示键全集 + 未知键防御位归正常区)。
eq("claimZoneOf 全键位", {
  wait_falsify: claimZoneOf({ display: "wait_falsify" }),
  falsifying: claimZoneOf({ display: "falsifying" }),
  falsify_paused: claimZoneOf({ display: "falsify_paused" }),
  insufficient: claimZoneOf({ display: "insufficient" }),
  discarded: claimZoneOf({ display: "discarded" }),
  unknown: claimZoneOf({ display: "not_a_key" }),
  missing: claimZoneOf({}),
}, {
  wait_falsify: 0, falsifying: 0, falsify_paused: 0,
  insufficient: 1, discarded: 2, unknown: 1, missing: 1,
});

// ---- ③ 假设页三区(byHypZonesDesc) ----

{
  const rows = [
    { id: "h-held-new", display: "held", added_at: "2026-11-08 00:00:00", created_rev: 80 },
    { id: "h-disc-new", display: "discarded", added_at: "2026-11-09 00:00:00", created_rev: 90 },
    { id: "h-verif-old", display: "wait_verify", added_at: "2026-11-01 00:00:00", created_rev: 10 },
    { id: "h-run-mid", display: "verifying", added_at: "2026-11-04 00:00:00", created_rev: 40 },
    { id: "h-pause-mid2", display: "verify_paused", added_at: "2026-11-05 00:00:00", created_rev: 50 },
    { id: "h-held-old", display: "held", added_at: "2026-11-02 00:00:00", created_rev: 20 },
    { id: "h-disc-old", display: "discarded", added_at: "2026-11-03 00:00:00", created_rev: 30 },
  ];
  eq(
    "假设三区:过程置顶/搁置居中/已废弃沉底,区内时间倒序",
    ids(sorted(rows, byHypZonesDesc)),
    ["h-pause-mid2", "h-run-mid", "h-verif-old", "h-held-new", "h-held-old", "h-disc-new", "h-disc-old"],
  );
}
eq("hypZoneOf 全键位", {
  wait_verify: hypZoneOf({ display: "wait_verify" }),
  verifying: hypZoneOf({ display: "verifying" }),
  verify_paused: hypZoneOf({ display: "verify_paused" }),
  held: hypZoneOf({ display: "held" }),
  discarded: hypZoneOf({ display: "discarded" }),
  unknown: hypZoneOf({ display: "?" }),
}, {
  wait_verify: 0, verifying: 0, verify_paused: 0,
  held: 1, discarded: 2, unknown: 1,
});

// ---- ④ 事实页三区(byFactZonesDesc) ----

// "新"标条目视为过程态置顶(即使立账早于已确认);"由假设升级"迁入条目
// 为已确认(正常区);被推翻沉底(防御性词位,状态机暂无该迁移)。
{
  const rows = [
    { id: "x-new-old", status: "candidate", is_new: true, created_at: "2026-11-01 00:00:00", created_rev: 10 },
    { id: "x-conf-new", status: "confirmed", created_at: "2026-11-09 00:00:00", created_rev: 90 },
    { id: "x-hyp-mid", status: "confirmed", kind: "hypothesis", from_hypothesis: true, created_at: "2026-11-06 00:00:00", created_rev: 60 },
    { id: "x-conf-old", status: "confirmed", created_at: "2026-11-03 00:00:00", created_rev: 30 },
    { id: "x-ref-newest", status: "refuted", created_at: "2026-11-10 00:00:00", created_rev: 99 },
  ];
  eq(
    "事实三区:新标置顶/已确认居中/被推翻沉底,区内时间倒序",
    ids(sorted(rows, byFactZonesDesc)),
    ["x-new-old", "x-conf-new", "x-hyp-mid", "x-conf-old", "x-ref-newest"],
  );
}
eq("factZoneOf 全形态", {
  cand: factZoneOf({ is_new: true, status: "candidate" }),
  conf: factZoneOf({ status: "confirmed" }),
  hyp: factZoneOf({ status: "confirmed", kind: "hypothesis" }),
  refuted: factZoneOf({ status: "refuted" }),
  bare: factZoneOf({}),
}, { cand: 0, conf: 1, hyp: 1, refuted: 2, bare: 1 });

// ---- ⑤ 筛选与排序可交换(筛选/搜索后排序规则保持,类级) ----

// 对任意谓词 P:sort∘filter 与 filter∘sort 结果一致——筛选只做减法,
// 排序口径不因筛选而变。
{
  const rows = [
    { id: "m-1", display: "falsifying", added_at: "2026-11-01 00:00:00", created_rev: 10 },
    { id: "m-2", display: "discarded", added_at: "2026-11-02 00:00:00", created_rev: 20 },
    { id: "m-3", display: "insufficient", added_at: "2026-11-03 00:00:00", created_rev: 30 },
    { id: "m-4", display: "wait_falsify", added_at: "2026-11-04 00:00:00", created_rev: 40 },
    { id: "m-5", display: "insufficient", added_at: "2026-11-05 00:00:00", created_rev: 50 },
  ];
  const predicates = {
    状态筛: (c) => c.display === "insufficient",
    其余全留: () => true,
    全筛除: () => false,
  };
  for (const [name, P] of Object.entries(predicates)) {
    eq(
      `筛选后排序保持(${name})`,
      ids(sorted(rows.filter(P), byClaimZonesDesc)),
      ids(sorted(rows, byClaimZonesDesc).filter(P)),
    );
  }
}

if (failed > 0) {
  console.error(`test-finding-sort: ${failed} 处断言失败(P4-61 四页签排序契约)`);
  process.exit(1);
}
console.log("test-finding-sort: 四页签排序契约全过(时间倒序/三区归类/区优先/重开不变/乱序落位/筛选交换)");
