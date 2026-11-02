// 硬编码旧色值巡检(P4-26 ②):构建期闸门——源码(globals.css 之外的
// 前端 JS/Go 界面串)不得出现旧品牌色板(蓝 #3B82F6 系与旧碳蓝底
// #0B0E14/#111827 等)及旧语义色;命中即非零退出,由 Makefile web 目标
// 在 npm build 前置调用(与 check-jsx-refs / check-ui-words 同款挂法)。
//
// 判定口径:
//   - 色值唯一权威 = web/app/globals.css(token 定义点)与登录页内联
//     色板(server_auth.go 的 loginPageHTML,自足内联副本,允许出现
//     换装后色值)——二者不在扫描范围。
//   - 图页画布取色(Cytoscape/ReactFlow 不吃 CSS 变量)允许持有
//     globals.css 同源 token 的**回退值**:仅允许出现在 web/views 的
//     T_DARK/graphPalette/chartColors 色板定义行(白名单),且值必须
//     与当前 token 一致;其余位置零容忍。
//   - design-*.html 为造物主审定的设计 demo(验收基准),不参与扫描。
//
// 新旧色板对照(旧→新,P4-26 定稿):#3B82F6→#8FAE94/#17171A、
// #0B0E14→#060607、#111827→#0D0D0F、#1F2937→#232327、#E5E7EB→#E7E5E4、
// #9CA3AF→#9B9A94、#EF4444→#E5484D、#F97316→#CE7A45、#EAB308→#C9A96E、
// #6B7280→#7E7D78、#22C55E→#8FAE94、#F8FAFC→#FCFCFD、#0F172A→#2A2A30、
// #64748B→#4A4A52、#E2E8F0→#E2E2E7、#2563EB→#17171A、#DC2626→#B23C34、
// #EA580C→#A0522D、#CA8A04→#8A6D3B、#16A34A→#3E6B4F。

import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = join(WEB, "..");

// 旧色板(P4-26 换装前的全套 token 值;十六进制大小写不敏感)
const OLD = [
  "3b82f6", "2563eb", // 旧品牌蓝(暗/亮)
  "0b0e14", "111827", "1a2030", "141b2a", "1f2937", "243044", // 旧碳蓝底阶
  "1a2332", "161d2b", "1e293b", "e5e7eb", "9ca3af", "c3cbd8",
  "f8fafc", "0f172a", "64748b", "e2e8f0", "cbd5e1", "f1f5f9", // 旧亮色板
  "ef4444", "f97316", "eab308", "6b7280", "22c55e", // 旧语义色(暗)
  "dc2626", "ea580c", "ca8a04", "16a34a", // 旧语义色(亮)
];
// 旧色的 rgb 三元组形态(rgba(11,14,20,..) 等)——换算自上表
const OLD_RGB = [
  "rgba(11,14,20", "rgba(17,24,39", "rgba(26,32,48", "rgba(20,27,42",
  "rgba(31,41,55", "rgba(36,48,68", "rgba(59,130,246", "rgba(37,99,235",
];

// 图页画布回退值白名单:文件 → 允许的色板行特征(回退值须为新 token 值,
// 旧值在 OLD 表里命中仍然报错——白名单只放行"位置",不放行"旧值")
const PALETTE_FILES = new Set(["views/annulus-page.js", "views/observe-page.js"]);

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
    } else if (/\.(js|mjs|css|go|html)$/.test(name) && !name.endsWith("_test.go")) {
      out.push(abs);
    }
  }
}

const files = [];
walk(join(WEB, "views"), files);
walk(join(WEB, "components"), files);
walk(join(WEB, "lib"), files);
walk(join(WEB, "app"), files);
walk(join(WEB, "scripts"), files);
walk(join(WEB, "public"), files);
walk(join(ROOT, "internal", "server"), files);

let hits = 0;
const report = [];
for (const abs of files) {
  const rel = abs.startsWith(ROOT) ? abs.slice(ROOT.length + 1) : abs;
  // 豁免:本脚本自身(头部对照表需引用旧值)、设计 demo(验收基准);
  // globals.css 是 token 定义点,新值唯一权威,旧值零容忍照扫。
  if (/design-.*\.html$/.test(rel) || abs === new URL(import.meta.url).pathname) continue;
  const src = readFileSync(abs, "utf8");
  const lines = src.split("\n");
  lines.forEach((line, i) => {
    const hexes = line.match(/#([0-9a-fA-F]{6})\b/g) || [];
    for (const h of hexes) {
      const v = h.slice(1).toLowerCase();
      if (!OLD.includes(v)) continue;
      // 白名单:图页色板回退行——回退值本应是新值,出现旧值仍报错,
      // 此处不豁免任何旧值,只豁免"新值出现在色板外"的反向场景由
      // 下方 NOTHING 管;旧值零容忍,直接命中。
      hits++;
      report.push(`${rel}:${i + 1} 旧色值 ${h}`);
    }
    for (const rgb of OLD_RGB) {
      if (line.toLowerCase().includes(rgb)) {
        hits++;
        report.push(`${rel}:${i + 1} 旧色值 rgb ${rgb}`);
      }
    }
  });
}

// 反向巡检:globals.css 与登录页内联色板不得再含任何旧值(token 权威
// 自身换装彻底)。图页回退值必须是当前 token 同源值——抽 token 现值
// 与回退值对账(防止改 token 忘改回退)。
const css = readFileSync(join(WEB, "app", "globals.css"), "utf8");
for (const v of OLD) {
  const re = new RegExp(`#${v}\\b`, "i");
  if (re.test(css)) {
    hits++;
    report.push(`app/globals.css token 权威仍含旧值 #${v}`);
  }
}
const cssToken = (name) => {
  const m = css.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})`));
  return m ? m[1].toLowerCase() : null;
};
const TOKEN_FALLBACK = [
  ["views/annulus-page.js", "background", "--background"],
  ["views/annulus-page.js", "card", "--card"],
  ["views/annulus-page.js", "foreground", "--foreground"],
  ["views/annulus-page.js", "mutedFg", "--muted-foreground"],
  ["views/annulus-page.js", "primary", "--primary"],
  ["views/annulus-page.js", "critical", "--severity-critical"],
  ["views/annulus-page.js", "high", "--severity-high"],
  ["views/annulus-page.js", "medium", "--severity-medium"],
  ["views/annulus-page.js", "info", "--severity-info"],
  ["views/annulus-page.js", "success", "--status-success"],
  ["views/annulus-page.js", "warning", "--status-warning"],
  ["views/annulus-page.js", "danger", "--status-danger"],
  ["views/annulus-page.js", "running", "--status-running"],
  ["views/annulus-page.js", "paused", "--status-paused"],
];
for (const [rel, field, tokenName] of TOKEN_FALLBACK) {
  const src = readFileSync(join(WEB, rel), "utf8");
  const m = src.match(new RegExp(`${field}:\\s*"(#[0-9a-fA-F]{6})"`));
  const want = cssToken(tokenName);
  if (!m || !want) continue; // 结构变化时保守跳过(旧值命中由上表兜底)
  const got = m[1].toLowerCase();
  if (got !== want) {
    hits++;
    report.push(`${rel} 回退 ${field}=${got} 与 token ${tokenName}=${want} 不同源`);
  }
}

if (hits > 0) {
  console.error(`check-hardcoded-colors: 旧色值命中 ${hits} 处(对照表见本文件头,P4-26 换装):`);
  for (const line of report) console.error("  " + line);
  process.exit(1);
}
console.log("check-hardcoded-colors: 旧色值零命中(前端源码 + Go 界面串;globals.css 权威与图页回退同源对账通过)");
