// 审批记录折叠类级测试(P4-74 ④,问题清单 #90 ③;规则 15 类级验收
// 纪律):连续机器留痕段折叠成一行计数(同步骤放行/同组放行/性质判
// 定三族),穿插自然分段、不同步骤各归各行、操作者裁决与待决记录
// 恒为单条可见;折叠不丢记录(段内明细全量携带,账本与服务端零改动);
// 空参数判定(空串/"{}" 标注"参数未记录",畸形原文照实)。挂 make
// web / make test 前置,零依赖。

import {
  foldApprovalRecords,
  argsKnownOnCard,
  FOLDABLE_RECORD_TYPES,
} from "../lib/approval-fold.mjs";

let failed = 0;
function ok(name, cond, detail) {
  if (!cond) {
    failed++;
    console.error(`✗ ${name}${detail ? `\n    ${detail}` : ""}`);
  }
}

const rec = (type, i, over = {}) => ({
  type,
  request_id: `ap-${i}`,
  step_id: "s1",
  unit_id: "u1",
  occurred_at: `2026-11-13 10:0${i % 10}:00`,
  ...over,
});

// ---- ① 30 条 step_pass 连续段 → 一行折叠(完成标准④实测形态) ----
{
  const records = Array.from({ length: 30 }, (_, i) => rec("step_pass", i));
  const items = foldApprovalRecords(records);
  ok("30 条连续 step_pass 折叠为一行", items.length === 1 && items[0].kind === "fold", JSON.stringify(items.length));
  ok("计数为 30", items[0]?.count === 30);
  ok("标签为同步骤放行", items[0]?.label === FOLDABLE_RECORD_TYPES.step_pass);
  ok("明细全量携带(点开可达)", items[0]?.records?.length === 30);
  ok("明细内容不丢", items[0]?.records?.every((r) => r.type === "step_pass"));
}

// ---- ② 默认视图构成:操作者裁决与待决恒可见,机器留痕只以折叠行在 ----
{
  const records = [
    rec("approved", 1, { tool_name: "bash" }),
    ...Array.from({ length: 8 }, (_, i) => rec("step_pass", i + 2)),
    rec("requested", 10),
    rec("scope_wait", 11, { request_id: "sc-x.com:443" }),
    rec("denied", 12),
    rec("attack_wait", 13),
    rec("reversed", 14),
    rec("access", 15),
    rec("forced", 16),
  ];
  const items = foldApprovalRecords(records);
  const singles = items.filter((x) => x.kind === "record").map((x) => x.record.type);
  const folds = items.filter((x) => x.kind === "fold");
  ok("操作者裁决与待决全部单条可见",
    ["approved", "requested", "scope_wait", "denied", "attack_wait", "reversed", "access", "forced"]
      .every((t) => singles.includes(t)),
    JSON.stringify(singles));
  ok("机器留痕只以折叠行在", folds.length === 1 && folds[0].type === "step_pass" && folds[0].count === 8);
  ok("总条目数=可见单条+折叠行", items.length === 9);
}

// ---- ③ 分段与分组键:不同步骤各归各行、穿插分段、三族各归各行 ----
{
  const records = [
    ...Array.from({ length: 5 }, (_, i) => rec("step_pass", i, { step_id: "s1" })),
    ...Array.from({ length: 3 }, (_, i) => rec("step_pass", i + 5, { step_id: "s2" })),
    rec("approved", 9),
    ...Array.from({ length: 4 }, (_, i) => rec("step_pass", i + 10, { step_id: "s1" })),
    ...Array.from({ length: 2 }, (_, i) => rec("group_pass", i + 20)),
    rec("triage", 30, { request_id: "tg-h" }),
    rec("triage", 31, { request_id: "tg-h2" }),
  ];
  const items = foldApprovalRecords(records);
  const folds = items.filter((x) => x.kind === "fold");
  const s1Folds = folds.filter((f) => f.type === "step_pass");
  ok("step_pass 折叠行恰 3 段(s1/s2/s1)", s1Folds.length === 3, JSON.stringify(s1Folds.map((f) => f.count)));
  ok("s1 两段计数 5 与 4、s2 计数 3",
    s1Folds[0].count === 5 && s1Folds[1].count === 3 && s1Folds[2].count === 4);
  ok("group_pass 一行计数 2", folds.find((f) => f.type === "group_pass")?.count === 2);
  ok("triage 一行计数 2", folds.find((f) => f.type === "triage")?.count === 2);
  ok("裁决行穿插可见", items.some((x) => x.kind === "record" && x.record.type === "approved"));
  // 全量对账:折叠行携带的记录总数 = 输入记录数(零丢失)。
  const carried = items.reduce((n, x) => n + (x.kind === "fold" ? x.records.length : 1), 0);
  ok("折叠不丢记录(全量对账)", carried === records.length, `${carried} vs ${records.length}`);
}

// ---- ④ 边界:空输入/单条机器留痕/单条裁决/未知类型防御 ----
{
  ok("空输入零条目", foldApprovalRecords([]).length === 0 && foldApprovalRecords(null).length === 0);
  const one = foldApprovalRecords([rec("step_pass", 0)]);
  ok("单条机器留痕也折叠(计数 1)", one.length === 1 && one[0].kind === "fold" && one[0].count === 1);
  const oneR = foldApprovalRecords([rec("denied", 0)]);
  ok("单条裁决原样可见", oneR.length === 1 && oneR[0].kind === "record");
  const unknown = foldApprovalRecords([{ type: "mystery", request_id: "x" }]);
  ok("未知类型防御:按单条可见", unknown.length === 1 && unknown[0].kind === "record");
}

// ---- ⑤ 空参数判定(完成标准②):空串与 {} 无可核对内容 ----
{
  ok("空串=未记录", argsKnownOnCard("bash", "") === false);
  ok("空白串=未记录", argsKnownOnCard("bash", "  ") === false);
  ok("缺省=未记录", argsKnownOnCard("bash", undefined) === false);
  ok("{}=未记录(无参数调用形态)", argsKnownOnCard("bash", "{}") === false);
  ok("{} 带空白=未记录", argsKnownOnCard("bash", " {} ") === false);
  ok("正常参数原文可上卡", argsKnownOnCard("bash", `{"command":"pwd; ls -la"}`) === true);
  ok("畸形原文照实可上卡", argsKnownOnCard("bash", `{"command":""}`) === true);
  ok("port_probe 参数可上卡", argsKnownOnCard("port_probe", `{"host":"t.local","port":443}`) === true);
  ok("无工具名的卡不判参数(资产类)", argsKnownOnCard("", "") === true);
}

if (failed > 0) {
  console.error(`approval-fold: ${failed} 项断言失败`);
  process.exit(1);
}
console.log("approval-fold: 类级断言全部通过");
