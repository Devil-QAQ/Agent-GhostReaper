// 融合设计律扫描闸(P4-31 ⑨):构建期闸门——字号五级制 / 间距阶梯 /
// 组件高度收敛 / 圆角尺寸表 / 写死色值,五类规则扫 views 与 components
// (含 ui/ 底层原件与弹窗抽屉模板),命中即非零退出,由 Makefile web 与
// test 目标在构建前置调用(与既有六道闸并列)。
//
// 规则口径(子计划 P4-31,验收基准 web/design-fusion.html):
//   - 字号五级:页面标题 .t-page(22 粗) / 浮层标题 .t-float(16,嵌套
//     15=text-[15px]) / 区块标题 .t-sec(13) / 正文 .t-body(12.5)~
//     text-sm(14) / 辅助 text-xs(12)~text-[11px] / 微标 text-[10px]。
//     任意值只许 {10,11,13,15};标准档只许 xs/sm,base 及以上全禁。
//   - 间距阶梯:8.4 阶梯 4/8/12/16/24/32/48 + 卡片间 14~16 + 卡片内
//     16~22 + 区块间 24~28(tailwind 步长 0.5~12 中取
//     2/4/6/8/12/14/16/18/20/22/24/28/32/48px);10px(2.5 步)与
//     36/40/44/56/64/72/80px 等大步长清零(资产树缩进列除外——层级
//     ×24px 结构轴,白名单行)。任意值仅许 {4,9,11,18} 组件尺寸表值
//     与 {80,92} 表单标签列对齐值。
//   - 高度:h-7 清零(按钮 36=默认 / 紧凑与菜单项 32=h-8 两档收敛)。
//   - 圆角:尺寸表 sm(6)/md(8)/lg(10)/xl(12)/2xl(16) + KPI 卡
//     [14px] + 复选框 [4px] + full;其余任意值与 3xl 报错。
//   - 色值:组件层零写死(文字色一律 var(--fg*) 族 token);画布取色
//     例外仅 annulus/observe 两页(Cytoscape/uPlot 不吃 CSS 变量,
//     值与 token 同源由 check-hardcoded-colors 把关);弹窗遮罩
//     bg-black/NN 允许(遮罩语义,非主题色)。
//   - design-*.html 为造物主审定 demo(验收基准),不参与扫描;
//     globals.css 是 token 与工具类定义点,不在扫描范围。

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");

// ---- 字号 ----
const TEXT_OK_ARBITRARY = new Set(["10", "11", "13", "15"]);
const TEXT_BANNED_STEP = /\btext-(base|lg|xl|[2-9]xl)\b/;
const TEXT_ARBITRARY = /\btext-\[([0-9.]+)px\]/g;

// ---- 间距(tailwind 步长) ----
const SPACE_OK_STEPS = new Set([
  "0", "0.5", "1", "1.5", "2", "3", "3.5", "4", "4.5", "5", "5.5", "6", "7", "8", "12",
]);
const SPACE_OK_ARBITRARY = new Set(["4", "9", "11", "18", "36", "80", "92"]);
const SPACE_PREFIX =
  "(?:p|px|py|pt|pb|pl|pr|m|mx|my|mt|mb|ml|mr|gap|gap-x|gap-y|space-x|space-y)";
const SPACE_STEP = new RegExp(`\\b${SPACE_PREFIX}-(-?\\d+(?:\\.\\d+)?)\\b`, "g");
const SPACE_ARBITRARY = new RegExp(`\\b${SPACE_PREFIX}-\\[([0-9.]+)px\\]`, "g");

// ---- 高度 / 圆角 ----
const H_BANNED = /\bh-7\b/;
const RADIUS_ARBITRARY = /\brounded(-[tblr]+)?-\[([0-9.]+)px\]/g;
const RADIUS_OK_ARBITRARY = new Set(["4", "14"]);
const RADIUS_BANNED = /\brounded(-[tblr]+)?-3xl\b/;

// ---- 色值 ----
const HEX = /#[0-9a-fA-F]{3,8}\b/;
const RGB = /\brgba?\(/;
const NAMED_BANNED = /\b(text|bg)-(white|black)\b(?!\S*\/)/; // bg-black/NN 遮罩放行
const PALETTE = /\b(?:text|bg|border|fill|stroke)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d+\b/;
const COLOR_FILE_WHITELIST = new Set([
  "views/annulus-page.js",
  "views/observe-page.js",
]);

function walk(dir, out) {
  let entries;
  try { entries = readdirSync(dir); } catch { return; }
  for (const name of entries.sort()) {
    const abs = join(dir, name);
    let st;
    try { st = statSync(abs); } catch { continue; }
    if (st.isDirectory()) {
      if (name === "node_modules" || name === "out" || name === ".next" || name.startsWith(".")) continue;
      walk(abs, out);
    } else if (/\.jsx?$/.test(name)) {
      out.push(abs);
    }
  }
}

const files = [];
walk(join(WEB, "views"), files);
walk(join(WEB, "components"), files);
walk(join(WEB, "lib"), files);
walk(join(WEB, "app"), files);

export function inspectFusionSource(rel, src) {
  const report = [];
  const note = (rel, line, msg) => report.push(`${rel}:${line} ${msg}`);
  const lines = src.split("\n");
  lines.forEach((line, i) => {
    if (TEXT_BANNED_STEP.test(line)) {
      note(rel, i + 1, `字号越级 ${line.match(TEXT_BANNED_STEP)[0]}(五级外)`);
    }
    for (const m of line.matchAll(TEXT_ARBITRARY)) {
      if (!TEXT_OK_ARBITRARY.has(m[1])) note(rel, i + 1, `字号越级 text-[${m[1]}px](五级外)`);
    }
    // 资产树缩进数组行(assets-page 层级 ×24px 结构轴,INDENT 两行):整行白名单
    const isTreeIndent = rel === "views/assets-page.js" && /"ml-\d+"/.test(line);
    for (const m of line.matchAll(SPACE_STEP)) {
      if (isTreeIndent) continue;
      if (!SPACE_OK_STEPS.has(m[1].replace(/^-(?=\d)/, ""))) {
        note(rel, i + 1, `间距混档 ${m[0].replace(`${m[1]}`, m[1])}(阶梯外)`);
      }
    }
    for (const m of line.matchAll(SPACE_ARBITRARY)) {
      if (!SPACE_OK_ARBITRARY.has(m[1])) note(rel, i + 1, `间距任意值 ${m[0]}(阶梯外)`);
    }
    if (H_BANNED.test(line)) note(rel, i + 1, "高度 h-7(收敛 h-8/h-9)");
    for (const m of line.matchAll(RADIUS_ARBITRARY)) {
      if (!RADIUS_OK_ARBITRARY.has(m[2])) note(rel, i + 1, `圆角 ${m[0]}(尺寸表外)`);
    }
    if (RADIUS_BANNED.test(line)) note(rel, i + 1, "圆角 3xl(尺寸表外)");
    if (!COLOR_FILE_WHITELIST.has(rel)) {
      if (HEX.test(line)) note(rel, i + 1, `写死色值 ${line.match(HEX)[0]}`);
      if (RGB.test(line)) note(rel, i + 1, "写死色值 rgb()");
      if (NAMED_BANNED.test(line)) note(rel, i + 1, `写死色值 ${line.match(NAMED_BANNED)[0]}`);
    }
    if (PALETTE.test(line)) note(rel, i + 1, `写死色板 ${line.match(PALETTE)[0]}`);
    if (/\b(?:font-mono|tnum)\b/.test(line) && !line.trim().startsWith("//")) {
      note(rel, i + 1, "旧数据字体类(统一使用 num，含等宽字体与 tabular-nums)");
    }
    if (/\btext-\[(?:\d+(?:\.\d+)?)(?:rem|em|vw|vh|%)\]/.test(line)) {
      note(rel, i + 1, "字号绕过五级(px 以外的任意字号)");
    }
    if (/\bfontSize\s*:/.test(line) && !/fontSize:\s*"var\(--text-micro\)"/.test(line) && !COLOR_FILE_WHITELIST.has(rel)) {
      note(rel, i + 1, "内联字号(统一使用排版工具类)");
    }
  });
  // 数值白名单不能代替组件用途约束：阻止页面覆盖底层浮层尺寸。
  for (const m of src.matchAll(/<(DialogContent|SheetContent|SheetTitle)\b[^>]*>/g)) {
    const tag = m[0];
    const line = src.slice(0, m.index).split("\n").length;
    if (m[1] === "DialogContent" && /\bp-(?:[0-5](?:\.\d+)?)\b/.test(tag)) {
      note(rel, line, "弹窗内边距低于 24px");
    }
    if (m[1] === "SheetContent" && /\bmax-w-(?:xl|[2-9]xl)\b/.test(tag)) {
      note(rel, line, "抽屉宽度覆盖 560px 上限");
    }
    if (m[1] === "SheetTitle" && /\btext-(?:xs|sm)\b/.test(tag)) {
      note(rel, line, "抽屉标题被正文档覆盖");
    }
  }
  return report;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const report = files.flatMap((abs) => inspectFusionSource(abs.slice(WEB.length + 1), readFileSync(abs, "utf8")));
  if (report.length > 0) {
    console.error(`check-fusion-rules: 融合律违规 ${report.length} 处(字号/间距/尺寸/色值/用途):`);
    for (const line of report) console.error("  " + line);
    process.exit(1);
  }
  console.log("check-fusion-rules: 字号/间距/尺寸/色值/用途零违规(views+components 含 ui/ 原件与浮层模板)");
}
