// 旧状态词残留检查(P4-56 起源;P5-02⑤ 三态化 + P5-03③ 过程态显示
// 退役 + P5-07⑧ 三页签台账收口):
// 构建期闸门——退役词作为徽标/筛选/时间线/结论句的显示词不得再出现:
//   - "锤炼中/待确认/挂账"禁(P4-56 沿革);"锤炼"整词随页签退役入禁
//     (P5-07⑧);
//   - 反证/验证过程态显示词(等待反证/反证中/反证暂停/验证暂停等)
//     随三段流水线退役(P5-03③);
//   - "候选"不上界面(机器态词,面板显示位零出现);
//   - "不成立"退役(P5-07⑧):refuted 显示词统一"被推翻"(与事实页/
//     事实侧同词);
//   - 三页签台账(P5-07①):意图/事实/漏洞在位,成果〔页签义〕/假设/
//     锤炼页签零残留。
// 判定口径(与 P4-28 同款,防误伤注释与机器侧文档):
//   - 前端显示位:`label: "旧词"`、筛选对 `["键", "旧词"]`、内联表
//     `键: "旧词"`、结论句 `当前结论：…旧词…`、字符串字面量含旧词
//     (注释行除外——整词族锤炼/不成立按字面量清零);
//   - Go 显示位:词表映射 `"键": "旧词"`、`return "旧词"`(显示词
//     函数)、字符串字面量含旧词(逐行剥注释后扫);
//   - 正向断言:三态新词前后端词表就位(防"删旧忘新")。
// 由 Makefile web 目标在 npm build 前置调用,go test 前同跑。零依赖。

import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// 退役词(显示位)。
// P5-02⑤:"锤炼中/待确认/挂账"仍禁。P5-03③:反证/验证过程态显示词
// 随三段流水线退役入禁(复测结论与正常步骤状态独立检查
// 服务存量节点详情,5-14 存疑解除口径保留)。
// P5-07⑧:"锤炼"整词与"不成立"入禁(refuted 显示词统一"被推翻")。
const OLD_WORDS = ["锤炼", "待确认", "挂账", "等待反证", "反证中", "反证暂停", "验证暂停", "不成立"];
// 事实侧"候选"词的显示位(筛选/徽标/空态文案)。
const OLD_FACT_WORDS = ["候选"];

let hits = 0;
const report = [];

function read(rel) {
  return readFileSync(join(ROOT, rel), "utf8");
}

function hit(msg) {
  hits++;
  report.push(msg);
}

// ---- 前端:views / components / lib ----
const FE_DIRS = ["web/views", "web/components", "web/lib"];
const { readdirSync, statSync } = await import("node:fs");
const feFiles = [];
function walk(dir) {
  for (const name of readdirSync(join(ROOT, dir)).sort()) {
    const rel = join(dir, name);
    const st = statSync(join(ROOT, rel));
    if (st.isDirectory()) continue;
    if (name.endsWith(".js")) feFiles.push(rel);
  }
}
for (const d of FE_DIRS) walk(d);

for (const rel of feFiles) {
  const src = read(rel);
  for (const w of OLD_WORDS) {
    // label: "旧词"(词表/徽标直取)
    if (new RegExp(`label:\\s*"${w}"`).test(src)) {
      hit(`${rel}: label: "${w}"(退役词作徽标词)`);
    }
    // ["键", "旧词"](筛选项)
    if (new RegExp(`\\["\\w+",\\s*"${w}"\\]`).test(src)) {
      hit(`${rel}: 筛选对 [..., "${w}"](退役词作筛选项)`);
    }
    // 当前结论句里的退役词(详情头部直陈)
    if (src.includes(`当前结论：`) && new RegExp(`当前结论：[^"\n]*${w}`).test(src)) {
      hit(`${rel}: 当前结论句含退役词 "${w}"`);
    }
  }
  if (rel === "web/lib/api.js" || rel === "web/views/node-sheets.js") {
    for (const w of OLD_WORDS) {
      // 内联显示词表(键: "旧词" 形态,词表文件里即显示位)
      if (new RegExp(`:\\s*"${w}"[,\n]`).test(src) && !new RegExp(`//[^\\n]*${w}`).test(src.split(new RegExp(`:\\s*"${w}"[,\n]`))[0].split("\n").pop() || "")) {
        hit(`${rel}: 内联表值 "${w}"(退役词)`);
      }
    }
  }
  // 字符串字面量含整词族退役词(P5-07⑧ 清零口径;注释行除外)
  for (const w of ["锤炼", "不成立"]) {
    for (const m of src.matchAll(/["'`]([^"'`\n]*锤炼[^"'`\n]*|[^"'`\n]*不成立[^"'`\n]*)["'`]/g)) {
      const lineNo = src.slice(0, m.index).split("\n").length;
      const line = src.split("\n")[lineNo - 1] || "";
      if (line.trim().startsWith("//") || line.trim().startsWith("*")) continue;
      hit(`${rel}: 字符串字面量含 "${w}"(${m[1].slice(0, 30)})`);
    }
  }
  // 事实侧"候选"不上界面(空态/筛选文案;注释不计)
  for (const w of OLD_FACT_WORDS) {
    for (const m of src.matchAll(/["'`]([^"'`\n]*候选[^"'`\n]*)["'`]/g)) {
      const lineNo = src.slice(0, m.index).split("\n").length;
      const line = src.split("\n")[lineNo - 1] || "";
      if (line.trim().startsWith("//") || line.trim().startsWith("*")) continue;
      hit(`${rel}: 字符串含 "候选"(${m[1].slice(0, 30)})`);
    }
  }
}

// P4-46 ② 沿用:全站显示文件零出现(dispatched→排队中、aborted→已中止)。
const P46_OLD_WORDS = ["已派发", "已终止"];
for (const rel of feFiles) {
  const src = read(rel);
  for (const w of P46_OLD_WORDS) {
    if (src.includes(w)) {
      hit(`${rel}: 旧状态词 "${w}" 残留(P4-46 ②:dispatched→排队中、aborted→已终止→已中止)`);
    }
  }
}

// ---- 后端:Go 显示词 ----
const GO_FILES = [
  "internal/rings/statuswords.go",
  "internal/rings/claimdisplay.go",
  "internal/report/report.go",
  "internal/report/export.go",
  "internal/report/claim.go",
  "internal/swarm/tools.go",
  "internal/swarm/retest.go",
  "internal/shepherd/tools.go",
];
for (const rel of GO_FILES) {
  if (!existsSync(join(ROOT, rel))) continue;
  const src = read(rel);
  for (const w of OLD_WORDS) {
    // "键": "旧词" / return "旧词"(显示词函数与报告文案)
    if (new RegExp(`"\\w+":\\s*"${w}"`).test(src)) {
      hit(`${rel}: 旧状态词 "${w}" 残留(映射值)`);
    }
    if (new RegExp(`return "${w}"`).test(src)) {
      hit(`${rel}: 旧状态词 "${w}" 残留(return)`);
    }
    // 报告正文里的旧词(Fprintf 字符串含旧词即命中;注释不计——
    // 逐行剥注释后扫)
    const codeOnly = src.split("\n").filter((l) => !l.trim().startsWith("//")).join("\n");
    for (const m of codeOnly.matchAll(new RegExp(`"([^"\\n]*${w}[^"\\n]*)"`, "g"))) {
      hit(`${rel}: 字符串字面量含 "${w}"(${m[1].slice(0, 36)})`);
    }
  }
  if (/"Confirmed"/.test(src)) {
    hit(`${rel}: 报告导出状态词仍用英文 "Confirmed"`);
  }
}

// ---- 正向断言:三态新词前后端词表就位(P5-02⑤/P5-07⑧ 口径) ----
const api = read("web/lib/api.js");
for (const w of [
  "待机器核验", "证据不足", "被推翻", "已废弃", "已确认", "已修复",
]) {
  if (!api.includes(w)) hit(`web/lib/api.js: 三态词 "${w}" 缺失`);
}
if (!/candidate: \{\s*$/.test(api) && !/candidate: \{/.test(api)) hit("web/lib/api.js: CLAIM_DISPLAY.candidate 未入词表");
if (!/label: "待机器核验"/.test(api)) hit('web/lib/api.js: CLAIM_DISPLAY 待机器核验 未入词表');
if (!/label: "被推翻"[\s\S]{0,80}refuted|refuted[\s\S]{0,80}label: "被推翻"/.test(api)) hit('web/lib/api.js: CLAIM_DISPLAY.refuted 应为 "被推翻"(P5-07⑧)');
if (!/candidate: \{ label: "未确认"/.test(api)) hit('web/lib/api.js: FACT_STATUS.candidate 应为 "未确认"(P5-08② 候选标"新"退役;图节点卡粗口径,P5-09/10 随批收口)');
if (/label: "新"/.test(api)) hit('web/lib/api.js: FACT_STATUS 候选词面 "新" 应已退役(P5-08②)');
if (/HYP_(DISPLAY|STATUS|DECISION|VERDICT)/.test(api)) hit("web/lib/api.js: 假设词表应已退役");
// P5-03③:过程态显示键零残留(CLAIM_DISPLAY/CLAIM_STATUS)。
for (const k of ["wait_falsify", "falsifying", "falsify_paused", "verify_paused"]) {
  if (new RegExp(`\b${k}:`).test(api)) hit(`web/lib/api.js: 过程态显示键 "${k}" 应已退役(P5-03③)`);
}
if (/CLAIM_HINT/.test(api)) hit("web/lib/api.js: CLAIM_HINT 悬停细分表应已退役(P5-03③)");
const rings = read("internal/rings/statuswords.go");
// P5-02⑤:声明状态机 6→3;后端词表三态+保留终态。
for (const w of ["待机器核验", "已确认", "证据不足", "被推翻", "已废弃", "已修复"]) {
  if (!rings.includes(w)) hit(`internal/rings/statuswords.go: 状态词 "${w}" 缺失(P5-02⑤ 三态/P5-07⑧)`);
}
const reportWords = read("internal/report/report.go");
if (!reportWords.includes("## 证据不足清单")) {
  hit('internal/report/report.go: 报告清单标题应为 "证据不足清单"(P4-56)');
}
if (!/"dispatched":\s*"排队中"/.test(reportWords)) {
  hit('internal/report/report.go: 报告步骤词 dispatched 应为 "排队中"(P4-46 ②)');
}
const findings = read("web/views/findings-page.js");
// P5-07①:三页签(意图/事实/漏洞)在位,旧页签(成果/假设)零残留。
for (const t of ["意图", "事实", "漏洞"]) {
  if (!new RegExp(`\\["\\w+",\\s*"${t}"\\]`).test(findings)) {
    hit(`web/views/findings-page.js: 页签 "${t}" 缺失`);
  }
}
for (const t of ["成果", "假设"]) {
  if (new RegExp(`\\["\\w+",\\s*"${t}"\\]`).test(findings)) {
    hit(`web/views/findings-page.js: 旧页签 "${t}" 应已退役(P5-07①)`);
  }
}
// P5-07②:追问提示文案锚例逐字在(发现页与详情抽屉至少各一处)。
const FOLLOWUP_HINT = "可问相关问题，也可让它深入继续挖";
const nodeSheets = read("web/views/node-sheets.js");
if (!findings.includes(FOLLOWUP_HINT)) {
  hit("web/views/findings-page.js: 追问提示文案锚例缺失(P5-07②)");
}
if (!nodeSheets.includes(FOLLOWUP_HINT)) {
  hit("web/views/node-sheets.js: 追问提示文案锚例缺失(P5-07②)");
}
if (/深入<\/|>深入<|深入\n/.test(nodeSheets) && /DiveDialog/.test(nodeSheets)) {
  hit("web/views/node-sheets.js: 深入按钮应已退役(P5-07②)");
}

if (hits > 0) {
  console.error(`check-status-words: 旧状态词残留 ${hits} 处(退役词=锤炼/待确认/挂账/等待反证/反证中/反证暂停/验证暂停/不成立/候选):`);
  for (const line of report) console.error("  " + line);
  process.exit(1);
}
console.log("check-status-words: 旧状态词零残留,三页签台账与三态词前后端就位(P5-07⑧)");
