// 界面禁用词检查(P4-19,问题清单 #31):构建期闸门——按审定词表扫
// 全部界面字符串字面量,命中即非零退出,由 Makefile web 目标在 npm
// build 前置调用(与 check-jsx-refs 同款挂法);go test 前也跑一遍,
// 后端动态串(报告/toast/审计行/提示词模板)同表治理。零依赖。
//
// 判定口径:
//   - 界面字符串 = 前端源码(JS)的字符串字面量与 JSX 裸文本(标签间
//     文字,如 >熔断<),加 Go 源码(非 _test)的字符串字面量;先剥
//     注释(行注释/跨行 JSX 块注释 {/* */})再提取,注释不参与。
//   - 词表为造物主审定稿(2026-10-23,18 条映射;P4-19 审核迭代补
//     5 条:落地/终态/Confirmed/竞争性替代解释/交换异常);禁用词含
//     同形异义的动词形态时按白名单放行(危害声明 = 拒绝线的危害申报
//     义,造物主裁定保留)。
//   - 机器语模式(P4-19 审核迭代 ②):界面串里不得出现 tc- 判断/
//     判断链 ID、alt 编号、裸版本号数字、Go 错误特征串——ID 与版本
//     号走悬停/详情,原始错误串收证据详情;同样只查含 CJK 的界面串
//     (纯代码字面量如 ID 前缀 "tc-chain-" 不受影响)。
//   - 提示词内部同样受扫:引擎模板串在会话转录/提示词查看器可见,
//     且其词汇会被模型带进操作者可见叙述(词表第 9 条常驻约束)。

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// 禁用词表(P4-19 审定稿 + 审核迭代 5 条;新增界面词先报造物主
// 批再入表)
const BANNED = [
  "收割", "熔断", "研判", "入账", "消费", "弹门",
  "候选假设", "候选", "挂账", "收养", "舵杆", "回流",
  "可证伪", "直觉候选", "转正", "声明", "裁决",
  "落地", "终态", "Confirmed", "竞争性替代解释", "交换异常",
];

// 机器语模式(审核迭代 ②):界面文本零出现。动态值(模板变量)扫不到,
// 由面板活体模式扫描兜底。
const MACHINE = [
  [/tc-[a-z0-9-]{6,}/, "tc-ID"],
  [/\balt\d+\b/, "alt编号"],
  [/版本\s*\d/, "裸版本号"],
  [/\bEOF\b/, "Go错误EOF"],
  [/Get \"|Post \"/, "Go错误Get/Post"],
  [/context deadline exceeded/i, "Go错误超时"],
  [/connection refused|dial tcp|no such host/i, "Go错误网络"],
];

// 白名单:同形异义的保留用法(造物主裁定;新增先报批)
const ALLOW_SUBSTRINGS = ["危害声明"];

const FE_DIRS = ["web/views", "web/components", "web/lib"];
const GO_DIRS = [
  "internal/server", "internal/report", "internal/swarm", "internal/overmind",
  "internal/shepherd", "internal/manager", "internal/arbiter", "internal/rings",
  "internal/blackbox", "internal/mirror", "cmd/ghostreaper",
];

const CJK = /[\u4e00-\u9fff]/;
// JS 字符串字面量(双引号/单引号/模板串,处理转义)
const JS_STR = /"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`/g;
// Go 字符串字面量(双引号;反引号原生串整段)
const GO_STR = /"(?:[^"\\\n]|\\.)*"|`[^`]*`/g;

function walk(dir, ext, out) {
  let entries;
  try { entries = readdirSync(join(ROOT, dir)); } catch { return; }
  for (const name of entries.sort()) {
    const rel = join(dir, name);
    const abs = join(ROOT, rel);
    let st;
    try { st = statSync(abs); } catch { continue; }
    if (st.isDirectory()) {
      if (name === "node_modules" || name === "dist" || name.startsWith(".")) continue;
      walk(rel, ext, out);
    } else if (name.endsWith(ext) && !name.endsWith(".test" + ext) && !name.endsWith("_test.go")) {
      out.push(rel);
    }
  }
}

function stripLineComments(line, go) {
  if (go) {
    const i = line.indexOf("//");
    if (i >= 0) {
      // 仅当 // 不在字符串内(粗判:该行此前引号数为偶数)
      const head = line.slice(0, i);
      const quotes = (head.match(/["`]/g) || []).length;
      if (quotes % 2 === 0) return head;
    }
    return line;
  }
  return line.replace(/\/\/.*$/, "");
}

// scanJS 前端单文件:字符串字面量 + JSX 裸文本(剥注释与字符串后
// 剩余的 CJK 即裸文本;跨行 {/* */} 块注释用状态机跳过)。
function scanJS(rel, src) {
  let inBlock = false; // 处于 {/* ... */} JSX 块注释内
  src.split("\n").forEach((raw, idx) => {
    let line = stripLineComments(raw, false);
    if (inBlock) {
      const end = line.indexOf("*/");
      if (end < 0) return;
      line = line.slice(end + 2);
      inBlock = false;
    }
    while (true) {
      const start = line.indexOf("{/*");
      if (start < 0) break;
      const end = line.indexOf("*/}", start);
      if (end < 0) {
        line = line.slice(0, start);
        inBlock = true;
        break;
      }
      line = line.slice(0, start) + " " + line.slice(end + 3);
    }
    // 字符串字面量
    for (const m of line.matchAll(JS_STR)) {
      const s = m[0].slice(1, -1);
      check(rel, idx + 1, s);
    }
    // JSX 裸文本:剥掉字符串字面量后剩余的 CJK 段
    const bare = line.replace(JS_STR, " ");
    for (const m of bare.matchAll(/[\u4e00-\u9fff][\u4e00-\u9fff0-9A-Za-z（）()·、,，:：;；。…\-\s]*[\u4e00-\u9fff）。,，:：;；…]?/g)) {
      check(rel, idx + 1, m[0].trim());
    }
  });
}

let hits = 0;
const report = [];

function check(rel, lineNo, s) {
  if (!s || !CJK.test(s)) return;
  for (const [re, name] of MACHINE) {
    if (re.test(s)) {
      hits++;
      report.push(`${rel}:${lineNo} [${name}] ${s.slice(0, 80)}`);
      return;
    }
  }
  for (const w of BANNED) {
    if (!s.includes(w)) continue;
    if (ALLOW_SUBSTRINGS.some((a) => s.includes(a))) continue;
    hits++;
    report.push(`${rel}:${lineNo} [${w}] ${s.slice(0, 80)}`);
    break;
  }
}

for (const [dirs, ext, isGo] of [[FE_DIRS, ".js", false], [GO_DIRS, ".go", true]]) {
  for (const dir of dirs) {
    const files = [];
    walk(dir, ext, files);
    for (const rel of files) {
      const src = readFileSync(join(ROOT, rel), "utf8");
      if (!isGo) {
        scanJS(rel, src);
        continue;
      }
      src.split("\n").forEach((raw, idx) => {
        const line = stripLineComments(raw, true);
        for (const m of line.matchAll(GO_STR)) check(rel, idx + 1, m[0].slice(1, -1));
      });
    }
  }
}

if (hits > 0) {
  console.error(`check-ui-words: 界面禁用词命中 ${hits} 处(词表见本文件头,P4-19 审定稿):`);
  for (const line of report) console.error("  " + line);
  process.exit(1);
}
console.log("check-ui-words: 界面禁用词零命中(前端源码 + Go 界面串,词表 23 条 + 机器语 7 模式)");
