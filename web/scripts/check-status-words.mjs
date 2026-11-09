// 旧状态词残留检查(P4-56 四页签,问题清单 #77;P4-28 ⑥/P4-46 ② 沿革):
// 构建期闸门——发现区重构为四页签漏斗(假设→事实→锤炼→成果)后,旧词
// 作为徽标/筛选/时间线/结论句的显示词不得再出现:
//   - 合并词"锤炼中"退役(跨对象复用词,拆为等待反证/反证中与等待验证/
//     验证中两族);
//   - "待确认"改"证据不足"(锤炼侧)与"新"(事实侧);
//   - "不成立"并入"已废弃"(被反证推翻/摘除连坐的终了档,灰);
//   - "挂账"改"搁置"(假设侧既有词归一);
//   - "候选"不上界面(事实侧新进标"新")。
// 判定口径(与 P4-28 同款,防误伤注释与机器侧文档):
//   - 前端显示位:`label: "旧词"`、筛选对 `["键", "旧词"]`、内联表
//     `键: "旧词"`、结论句 `当前结论：…旧词…`;
//   - Go 显示位:词表映射 `"键": "旧词"`、`return "旧词"`(显示词函数);
//   - 正向断言:新词前后端词表就位(防"删旧忘新"),四页签在位、
//     积压行(锤炼中：)退役。
// 由 Makefile web 目标在 npm build 前置调用,go test 前同跑。零依赖。

import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// P4-56 退役词(显示位)。
const OLD_WORDS = ["锤炼中", "待确认", "不成立", "挂账"];
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
      hit(`${rel}: label: "${w}"(P4-56 退役词作徽标词)`);
    }
    // ["键", "旧词"](筛选项)
    if (new RegExp(`\\["\\w+",\\s*"${w}"\\]`).test(src)) {
      hit(`${rel}: 筛选对 [..., "${w}"](P4-56 退役词作筛选项)`);
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
        hit(`${rel}: 内联表值 "${w}"(P4-56 退役词)`);
      }
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

// ---- 正向断言:四页签新词就位(P4-56 词表前后端同源) ----
const api = read("web/lib/api.js");
for (const w of [
  "等待反证", "反证中", "反证暂停", "证据不足", "已废弃",
  "等待验证", "验证中", "验证暂停", "搁置", "被推翻", "成立", "未能定论",
]) {
  if (!api.includes(w)) hit(`web/lib/api.js: 四页签词 "${w}" 缺失`);
}
if (!/label: "等待反证"/.test(api)) hit('web/lib/api.js: CLAIM_DISPLAY 等待反证 未入词表');
if (!/label: "反证中"/.test(api)) hit('web/lib/api.js: CLAIM_DISPLAY 反证中 未入词表');
if (!/label: "等待验证"/.test(api)) hit('web/lib/api.js: HYP_DISPLAY 等待验证 未入词表');
if (!/candidate: \{ label: "新"/.test(api)) hit('web/lib/api.js: FACT_STATUS.candidate 应为 "新"(P4-56)');
if (!/refuted: \{ label: "被推翻"/.test(api)) hit('web/lib/api.js: HYP_VERDICT.refuted 应为 "被推翻"(P4-56)');
const rings = read("internal/rings/statuswords.go");
for (const w of ["等待反证", "反证中", "等待验证", "验证中", "证据不足", "已废弃", "搁置", "已修复"]) {
  if (!rings.includes(w)) hit(`internal/rings/statuswords.go: 状态词 "${w}" 缺失`);
}
const reportWords = read("internal/report/report.go");
if (!reportWords.includes("## 证据不足清单")) {
  hit('internal/report/report.go: 报告清单标题应为 "证据不足清单"(P4-56)');
}
if (!/"dispatched":\s*"排队中"/.test(reportWords)) {
  hit('internal/report/report.go: 报告步骤词 dispatched 应为 "排队中"(P4-46 ②)');
}
const findings = read("web/views/findings-page.js");
for (const t of ["假设", "事实", "锤炼", "成果"]) {
  if (!new RegExp(`\\["\\w+",\\s*"${t}"\\]`).test(findings)) {
    hit(`web/views/findings-page.js: 四页签 "${t}" 缺失`);
  }
}
if (findings.includes("锤炼中：")) {
  hit("web/views/findings-page.js: 积压行(锤炼中：)应退役(P4-56 ④ 页签计数替代)");
}
const sheets = read("web/views/node-sheets.js");
if (!sheets.includes("疑点:") || !sheets.includes("怎么验的:")) {
  hit("web/views/node-sheets.js: 反证时间线人话三段(疑点/怎么验的)缺失(P4-56 ⑤)");
}

if (hits > 0) {
  console.error(`check-status-words: 旧状态词残留 ${hits} 处(P4-56 四页签口径,退役词=锤炼中/待确认/不成立/挂账/候选):`);
  for (const line of report) console.error("  " + line);
  process.exit(1);
}
console.log("check-status-words: 旧状态词零残留,四页签词前后端就位(P4-56)");
