// stripLiterals:前端源码静态扫描共用工具——注释/字符串/正则字面量
// 内容替换为空格(长度与换行不变,剥后文本的位置即原文位置),从
// check-jsx-refs.mjs 抽出共享(P4-60 起闸门族共用,不复制平行实现)。
// 状态机逐字符走,模板串 ${} 插值递归回代码态。

export function stripLiterals(src) {
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

// listSource:收集目录下全部 .js/.jsx/.mjs 源文件(跳过产物目录)。
import { readdirSync, statSync } from "node:fs";
import { join } from "node:path";

export function listSource(dir, out) {
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
