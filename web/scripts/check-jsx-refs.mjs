// 未定义 JSX 引用检查(P4-15 ②,问题清单 #26):构建期闸门——
// 引用未导入/未声明的 JSX 组件在浏览器运行时才 ReferenceError
// (v1.0.1/v1.0.2 两版黑屏即此类:sessions-page 用 ThinkingCard 未导入,
// 构建器不解析标识符照常压缩)。本脚本静态扫描全部前端源码,命中即
// 非零退出,由 Makefile web 目标在 npm build 前置调用——同类错误
// 过不了构建。零依赖,node 内建。
//
// 判定口径(保守防误报,宁可漏报不误拦):
//   - 只查 JSX 标签引用:<Tag / </Tag,基名(点链首段)不在该文件的
//     导入 ∪ 声明集合即命中;小写无点 = HTML 原生标签跳过。
//   - 扫描前剥注释与字符串(内容替换为空格,保行列号),注释里的
//     "声明"、字符串里的 "<X>" 不参与;导入语句从原文解析(带模块
//     路径字符串)。
//   - 声明集合过度收集(function/class/const/let/var/函数形参,
//     不分作用域):嵌套定义的组件不误拦。
//   - 比较运算防误报:前一个字符是标识符/右括号/右方括号的 "<X"
//     (a<B、f(x)<Y 形)跳过;关闭标签 </X> 无此歧义,不豁免。

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
// 前端源码目录(app 配置文件无 JSX,scripts 是工具自身,均不入扫)
const sourceDirs = ["app", "components", "views", "lib"];

function listSource(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === "node_modules" || name === "out" || name === ".next") continue;
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      listSource(p, out);
    } else if (/\.(js|jsx|mjs)$/.test(name)) {
      out.push(p);
    }
  }
}

// stripLiterals 注释/字符串/正则字面量内容替换为空格(长度与换行
// 不变,剥后文本的位置即原文位置)。状态机逐字符走,模板串 ${} 插值
// 递归回代码态。
function stripLiterals(src) {
  const out = src.split("");
  const N = src.length;
  let i = 0;
  let lastSig = ""; // 代码态最近一个非空白字符(正则/除法判别)
  let lastWord = ""; // 代码态最近一个完整词(关键字判别)
  const tplStack = []; // 模板串 ${ 插值嵌套(元素 = 插值内花括号深度)
  const blank = (j) => (out[j] = src[j] === "\n" ? "\n" : " ");
  const regexPrecededBy = () =>
    lastSig !== "<" && // "</" = JSX 闭合标签,不是正则
    (lastSig === "" ||
    "([{,;=:?!&|+-*%~^>".includes(lastSig) ||
    ["return", "typeof", "instanceof", "in", "of", "case", "do", "else", "yield", "await", "new", "delete", "void", "throw"].includes(lastWord));
  // scanTemplate 模板串内容扫描(自 start 起):吞到反引号(闭串)或
  // ${ (回代码态并压一层插值)。返回扫描结束位置。
  const scanTemplate = (start) => {
    let j = start;
    while (j < N && src[j] !== "`") {
      if (src[j] === "\\") { blank(j++); if (j < N) blank(j++); continue; }
      if (src[j] === "$" && src[j + 1] === "{") {
        blank(j++); blank(j++);
        tplStack.push(0);
        return j;
      }
      blank(j++);
    }
    if (j < N) blank(j++);
    return j;
  };
  while (i < N) {
    const c = src[i];
    const d = i + 1 < N ? src[i + 1] : "";
    if (c === "/" && d === "/") {
      while (i < N && src[i] !== "\n") blank(i++);
      continue;
    }
    if (c === "/" && d === "*") {
      blank(i++); blank(i++);
      while (i < N && !(src[i] === "*" && src[i + 1] === "/")) blank(i++);
      if (i < N) { blank(i++); blank(i++); }
      continue;
    }
    if (c === "'" || c === '"') {
      const q = c;
      blank(i++);
      while (i < N && src[i] !== q) {
        if (src[i] === "\\") blank(i++);
        if (i < N) blank(i++);
      }
      if (i < N) blank(i++);
      lastSig = '"'; // 字符串是值语境:紧随的 / 只能是除法(防 /> 误判正则)
      lastWord = "";
      continue;
    }
    if (c === "`") {
      blank(i++);
      i = scanTemplate(i);
      lastSig = src[i - 1] === "`" ? "`" : ""; // 闭串=值语境;${ = 表达式起点
      lastWord = "";
      continue;
    }
    if (c === "}" && tplStack.length) {
      const top = tplStack.length - 1;
      if (tplStack[top] > 0) {
        tplStack[top]--; // 插值内普通闭括号(对象/函数体/JSX 表达式),留在代码态
      } else {
        tplStack.pop();
        blank(i++);
        i = scanTemplate(i);
        lastSig = src[i - 1] === "`" ? "`" : "";
        lastWord = "";
        continue;
      }
    }
    if (c === "/" && regexPrecededBy()) {
      // 正则字面量:吞到未转义的 /
      blank(i++);
      let inClass = false;
      while (i < N) {
        if (src[i] === "\\") { blank(i++); if (i < N) blank(i++); continue; }
        if (src[i] === "[") inClass = true;
        else if (src[i] === "]") inClass = false;
        else if (src[i] === "/" && !inClass) break;
        blank(i++);
      }
      if (i < N) blank(i++);
      while (i < N && /[a-z]/.test(src[i])) blank(i++); // 标志位
      lastSig = "/";
      continue;
    }
    if (/\s/.test(c)) { i++; continue; }
    if (/[\w$]/.test(c)) {
      let w = "";
      while (i < N && /[\w$]/.test(src[i])) w += src[i++];
      lastWord = w;
      lastSig = w[w.length - 1];
      continue;
    }
    if (c === "{" && tplStack.length) tplStack[tplStack.length - 1]++;
    lastSig = c;
    lastWord = "";
    i++;
  }
  return out.join("");
}

// parseImports 从原文解析 import 绑定名(默认名/命名/命名重命名/命名空间)。
function parseImports(src) {
  const names = new Set();
  const re = /\bimport\s+([^;'`]*?)\s*from\s*['"][^'"]+['"]/g;
  let m;
  while ((m = re.exec(src))) {
    let clause = m[1].replace(/\*\/|\/\*/g, " ").replace(/\/\/[^\n]*/g, " ");
    const ns = clause.match(/\*\s*as\s+([A-Za-z_$][\w$]*)/);
    if (ns) names.add(ns[1]);
    clause = clause.replace(/\*[^{]*/, "");
    const def = clause.match(/^([A-Za-z_$][\w$]*)/);
    if (def) names.add(def[1]);
    const braces = clause.match(/\{([^}]*)\}/);
    if (braces) {
      for (let part of braces[1].split(",")) {
        part = part.trim();
        if (!part) continue;
        const as = part.match(/\bas\s+([A-Za-z_$][\w$]*)/);
        names.add(as ? as[1] : part.split(/[:\s=]/)[0].trim());
      }
    }
  }
  return names;
}

// parseDeclarations 从剥后文本收集声明名(过度收集,不分作用域)。
function parseDeclarations(stripped) {
  const names = new Set();
  const declRe = /\b(?:function\*?\s+|class\s+|(?:const|let|var)\s+)([A-Za-z_$][\w$]*)/g;
  let m;
  while ((m = declRe.exec(stripped))) names.add(m[1]);
  // 解构绑定:const/let/var { a, b: c, d = 1 } / [x, y](取 = 前段提词)
  const destrRe = /\b(?:const|let|var)\s*([[{][^=;]*?[}\]])\s*=/g;
  while ((m = destrRe.exec(stripped))) {
    for (const w of m[1].match(/[A-Za-z_$][\w$]*/g) || []) names.add(w);
  }
  // 函数形参(组件作参不误拦):function foo(...) 折叠括号段
  const fnRe = /\bfunction\*?\s+[A-Za-z_$][\w$]*\s*\(/g;
  while ((m = fnRe.exec(stripped))) {
    let depth = 0, j = m.index + m[0].length - 1;
    for (; j < stripped.length; j++) {
      if (stripped[j] === "(") depth++;
      else if (stripped[j] === ")") { depth--; if (!depth) break; }
    }
    for (const w of stripped.slice(m.index + m[0].length, j).match(/[A-Za-z_$][\w$]*/g) || []) names.add(w);
  }
  return names;
}

// checkFile 单文件检查,返回违规列表 { line, tag, base }。
function checkFile(path) {
  const src = readFileSync(path, "utf8");
  const stripped = stripLiterals(src);
  const defs = new Set([...parseImports(src), ...parseDeclarations(stripped)]);
  const hits = [];
  const tagRe = /<(\/?)([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)/g;
  let m;
  while ((m = tagRe.exec(stripped))) {
    const closing = m[1] === "/";
    const base = m[2].split(".")[0];
    if (!m[2].includes(".") && base[0] === base[0].toLowerCase()) continue; // HTML 原生标签
    if (defs.has(base)) continue;
    if (!closing) {
      // 比较运算防误报:前邻字符为标识符/右括号/右方括号的 <X 跳过
      const prev = stripped[m.index - 1] || "";
      if (/[\w$)\]]/.test(prev)) continue;
    }
    const line = stripped.slice(0, m.index).split("\n").length;
    hits.push({ line, tag: m[2] });
  }
  return hits;
}

const files = [];
for (const dir of sourceDirs) listSource(join(webRoot, dir), files);
const violations = [];
for (const f of files) {
  for (const h of checkFile(f)) {
    violations.push(`${relative(webRoot, f)}:${h.line}  未定义的 JSX 引用 <${h.tag}>`);
  }
}
if (violations.length) {
  console.error(`未定义 JSX 引用检查未通过(${violations.length} 处):`);
  for (const v of violations) console.error("  " + v);
  process.exit(1);
}
console.log(`未定义 JSX 引用检查通过(${files.length} 个源文件)。`);
