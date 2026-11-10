// 无保护成员方法调用检查(P4-60 ③,问题清单 #81):构建期闸门——
// 属性链接收者(x.y.method / x.y.length)直调成员且方法名前无 ?. 时,
// 上游字段缺失/null 即 TypeError 整页白屏(v1.2.1 审批记录详情崩溃
// 即此类:decided_by 带 omitempty 字段不出现,undefined 吃进
// startsWith)。本脚本静态扫描全部前端源码,命中即非零退出,由
// Makefile web 目标在 npm build 前置调用——同类错误过不了构建。
// 零依赖,node 内建。
//
// 判定口径(边界如实钉死,宁可漏报不误拦):
//   - 只管属性链形态:方法/属性的接收者本身是点链成员表达式
//     (record.decided_by.startsWith / view.fixed.length)且成员访问
//     点前无 ?。已有 ?. 或守卫的放行;裸标识符接收者(v.startsWith,
//     接收者是函数参数,静态不可判数据来源)不归本闸门管,由渲染
//     错误边界兜底——不做数据流分析类过度工程。
//   - 接收者尾部是调用/下标表达式(f(x).map / obj["k"].length)、
//     数值字面量(1.5.toFixed)、展开符(...arr.map)不命中(保守);
//     x?.y.length 命中(前置可选链不豁免后续裸点,语义正确)。
//   - 扫描前剥注释与字符串(共享 stripLiterals),字符串/注释里的
//     调用形态不参与。
//   - 存量安全点(本地表单状态等,接收者来源本地初始化恒非空)逐处
//     人工核定入白名单文件随仓维护;白名单条目失配(代码已改)同样
//     不过——防白名单腐化。API 数据来源的接收者一律加 ?. 或守卫,
//     不靠白名单放行。

import { readFileSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { stripLiterals, listSource } from "./source-scan.mjs";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
// 前端源码目录(app 配置无组件,scripts 是工具自身,均不入扫)
const sourceDirs = ["app", "components", "views", "lib"];
const whitelistPath = join(webRoot, "scripts", "unprotected-methods-whitelist.txt");

// 受保护成员名单:字符串/数组方法 + length/size 属性——undefined/null
// 接收者必崩的那一族(条目口径与问题清单 #81 一致,等族补全)。
const CALL_METHODS = [
  // 字符串
  "startsWith", "endsWith", "slice", "substring", "substr", "split",
  "trim", "trimStart", "trimEnd", "toLowerCase", "toUpperCase",
  "toLocaleLowerCase", "toLocaleUpperCase", "normalize",
  "replace", "replaceAll", "match", "matchAll", "includes",
  "indexOf", "lastIndexOf", "charAt", "charCodeAt", "codePointAt",
  "padStart", "padEnd", "repeat", "concat", "at", "localeCompare",
  // 数组
  "map", "filter", "some", "every", "forEach", "reduce", "reduceRight",
  "find", "findIndex", "findLast", "findLastIndex", "flat", "flatMap",
  "join", "sort", "reverse", "fill", "keys", "values", "entries",
  // 通用
  "toString", "hasOwnProperty",
];
const PROP_NAMES = ["length", "size"]; // 非调用形态(无数尾括号)同样崩

// 读白名单:每行 "相对路径 :: 表达式" (# 后为核定注释)。
function loadWhitelist() {
  const entries = new Set();
  const src = readFileSync(whitelistPath, "utf8");
  for (const raw of src.split("\n")) {
    const line = raw.split("#")[0].trim();
    if (!line) continue;
    entries.add(line.replace(/\s+::\s+/, "::"));
  }
  return entries;
}

// checkFile 单文件检查,返回违规列表 { line, expr, name }。
// expr = 完整点链表达式(record.decided_by.startsWith),白名单键。
function checkFile(path) {
  const stripped = stripLiterals(readFileSync(path, "utf8"));
  const hits = [];
  const isW$ = (c) => /[\w$]/.test(c);
  const skipWs = (i) => { while (i >= 0 && /\s/.test(stripped[i])) i--; return i; };

  const examine = (dotIdx, name) => {
    // ?. 已保护:点前(跳空白)是 ? 即放行
    let j = skipWs(dotIdx - 1);
    if (j >= 0 && stripped[j] === "?") return;
    // 接收者尾段:回读标识符(跨行链容忍空白)
    let k = j;
    while (k >= 0 && isW$(stripped[k])) k--;
    const tail = stripped.slice(k + 1, j + 1);
    if (!tail) return; // 尾部是 )/]/. 等:调用/下标/链式调用结果,保守放行
    if (/^[0-9]+$/.test(tail)) return; // 数值字面量(1.5.toFixed)
    // 尾段标识符之前(跳空白)是 . 才算属性链接收者
    const m = skipWs(k);
    if (m < 0 || stripped[m] !== ".") return;
    if (m > 0 && stripped[m - 1] === ".") return; // 展开符 ...tail.method
    // 回读完整点链作白名单键(标识符/点/可选点;下标等复杂形态止于 ] 前;
    // 键内空白归一,跨行链同键)
    let s = m;
    while (s >= 0 && /[\w$?.]/.test(stripped[s])) s--;
    const expr = (stripped.slice(s + 1, dotIdx) + "." + name).replace(/\s+/g, "").replace(/^[?.]+/, "");
    const line = stripped.slice(0, dotIdx).split("\n").length;
    hits.push({ line, expr, name });
  };

  for (const name of CALL_METHODS) {
    const re = new RegExp(`\\.\\s*(${name})\\b\\s*\\(`, "g");
    let m;
    while ((m = re.exec(stripped))) examine(m.index, name);
  }
  for (const name of PROP_NAMES) {
    const re = new RegExp(`\\.\\s*(${name})\\b(?![\\w$(])`, "g");
    let m;
    while ((m = re.exec(stripped))) examine(m.index, name);
  }
  return hits;
}

const files = [];
for (const dir of sourceDirs) listSource(join(webRoot, dir), files);
const whitelist = loadWhitelist();
const usedWhitelist = new Set();
const violations = [];
for (const f of files) {
  const rel = relative(webRoot, f);
  for (const h of checkFile(f)) {
    const key = `${rel}::${h.expr}`;
    if (whitelist.has(key)) { usedWhitelist.add(key); continue; }
    violations.push(`${rel}:${h.line}  ${h.expr}  (成员 ${h.name} 前无 ?.)`);
  }
}
const stale = [...whitelist].filter((k) => !usedWhitelist.has(k));
if (violations.length) {
  console.error(`无保护成员方法调用检查未通过(${violations.length} 处,加 ?. 或守卫,存量安全点核定入白名单):`);
  for (const v of violations) console.error("  " + v);
}
if (stale.length) {
  console.error(`白名单失配(${stale.length} 条,代码已改请同步白名单):`);
  for (const s of stale) console.error("  " + s);
}
if (violations.length || stale.length) process.exit(1);
console.log(`无保护成员方法调用检查通过(${files.length} 个源文件,白名单 ${whitelist.size} 处)。`);
