// 旧状态词残留检查(P4-28 ⑥,问题清单 #48;P4-46 ② 追加):构建期闸门——
// 状态显示层合并 12→6("待验证/反证中/待下轮反证"并入"锤炼中")后,旧
// 状态词作为徽标/筛选/时间线显示词、机器状态词作为报告导出词均不得再
// 出现;悬停细分句(api.js hint)与积压行口径词(findings-page)为规范内
// 用法,按"词以显示位形态出现"的精确模式扫描,不误伤。P4-46 ② 追加:
// 步骤 dispatched 显示词统一"排队中"(旧词"已派发")、单位/步骤 aborted
// 统一"已中止"(旧词"已终止")——显示文件零出现,词表正向断言就位。
// 由 Makefile web 目标在 npm build 前置调用(与 check-ui-words 同款挂
// 法),go test 前同跑。零依赖。
//
// 判定口径:
//   - 旧词显示位模式(前端):`label: "旧词"`、筛选对 `["状态值", "旧词"]`、
//     词表键值 `状态值: "旧词"`(覆盖 STATE_LABELS 类内联表)。
//   - Go 显示位:词表映射 `"状态值": "旧词"`、`return "旧词"`
//     (claimStatusLabel 类)、报告裸状态 `状态 → %s`。
//   - 英文残留:报告导出状态词不得再用 "Confirmed"(P4-19 词表禁用,
//     此前因无 CJK 漏过 ui-words 闸)。
//   - 正向断言:六词在前后端词表就位(防"删了旧词忘了放新词")。

import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const OLD_WORDS = ["待验证", "反证中", "待下轮反证", "验证中"];
const STATUS_KEYS = ["candidate", "testing", "awaiting_round"];

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
      hit(`${rel}: label: "${w}"(旧状态词作徽标词)`);
    }
    // ["状态值", "旧词"](筛选项)
    if (new RegExp(`\\["\\w+",\\s*"${w}"\\]`).test(src)) {
      hit(`${rel}: 筛选对 [..., "${w}"](旧状态词作筛选项)`);
    }
    // 状态值: "旧词"(内联显示词表)
    for (const k of STATUS_KEYS) {
      if (new RegExp(`${k}:\\s*"${w}"`).test(src)) {
        hit(`${rel}: ${k}: "${w}"(旧状态词内联表)`);
      }
    }
  }
}

// P4-46 ②:全站显示文件零出现(dispatched→排队中、aborted→已中止后,
// "已派发/已终止"不再是任何界面词;toast/tooltip 文案同样不得回潮)。
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
  for (const w of OLD_WORDS.slice(0, 3)) {
    // "candidate": "旧词" / return "旧词"(旧词表残留)
    if (new RegExp(`"\\w+":\\s*"${w}"`).test(src) || new RegExp(`return "${w}"`).test(src)) {
      hit(`${rel}: 旧状态词 "${w}" 残留`);
    }
  }
  if (/"Confirmed"/.test(src)) {
    hit(`${rel}: 报告导出状态词仍用英文 "Confirmed"`);
  }
  if (/orDash\(e\.Status\)|状态 → %s", e\.Status/.test(src)) {
    hit(`${rel}: 报告时间线裸状态(状态词函数未接)`);
  }
}

// ---- 正向断言:六词就位 + P4-46 步骤词就位 ----
const api = read("web/lib/api.js");
for (const w of ["锤炼中", "已确认", "待确认", "成立", "不成立", "未能定论"]) {
  if (!api.includes(w)) hit(`web/lib/api.js: 六词之 "${w}" 缺失`);
}
if (!api.includes('label: "锤炼中"')) hit("web/lib/api.js: 合并词 锤炼中 未入词表");
if (!/dispatched:\s*"排队中"/.test(api)) hit('web/lib/api.js: STEP_STATUS.dispatched 应为 "排队中"(P4-46 ②)');
if (!/aborted:\s*"已中止"/.test(api)) hit('web/lib/api.js: STEP_STATUS.aborted 应为 "已中止"(P4-46 ②)');
const rings = read("internal/rings/statuswords.go");
for (const w of ["锤炼中", "已确认", "待确认", "已修复", "搁置", "成立", "已废弃"]) {
  if (!rings.includes(w)) hit(`internal/rings/statuswords.go: 状态词 "${w}" 缺失`);
}
const reportWords = read("internal/report/report.go");
if (!/"dispatched":\s*"排队中"/.test(reportWords)) {
  hit('internal/report/report.go: 报告步骤词 dispatched 应为 "排队中"(P4-46 ②)');
}
const findings = read("web/views/findings-page.js");
if (!findings.includes("锤炼中：")) hit("web/views/findings-page.js: 积压行(锤炼中：)缺失");

if (hits > 0) {
  console.error(`check-status-words: 旧状态词残留 ${hits} 处(P4-28 ⑥ 合并口径,六词=锤炼中/已确认/待确认/成立/不成立/未能定论):`);
  for (const line of report) console.error("  " + line);
  process.exit(1);
}
console.log("check-status-words: 旧状态词零残留,六词前后端就位(P4-28 ⑥)");
