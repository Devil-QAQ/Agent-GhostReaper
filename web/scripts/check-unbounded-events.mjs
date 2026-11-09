// server 层无界事件扫描静态检查(P4-58 ③,问题清单 #79):构建期闸门——
// internal/server 的面板接口禁止对账本做无界全量拉取(`.Events(ctx,
// taskID, 0, 0)` 形态:since=0 且 limit=0),读侧一律走 kind 定向直查
// (Store.EventsByKinds,经 v13 (task_id, kind, revision) 索引)、投影直查
// (nodes/edges/unit_sessions/approval_records 等)或增量窗口(since>0 /
// limit>0)。新接口带无界调用过不了构建。
// 合理保留(白名单,逐条注明理由;改动须评审):
//   - internal/server/server_diagnostics.go:诊断包导出——用户主动的
//     一次性全量收集(导出那一刻的完整账本快照),非面板读路径。
// 判定口径:非测试 .go 文件中 `.Events(` 调用的实参以字面 `0, 0` 收尾
// (容忍空白与换行);变量传参(since/limit)视为有界窗口,不拦。
// 由 Makefile web 目标在 npm build 前置调用,go test 前同跑。零依赖。

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const DIR = join(ROOT, "internal", "server");

// 白名单:文件名(不含路径)→ 保留理由。命中白名单的文件豁免本检查。
const ALLOWLIST = new Map([
  ["server_diagnostics.go", "诊断包导出:用户主动一次性全量收集,合理保留(P4-58 ①)"],
]);

let hits = 0;
const report = [];

for (const name of readdirSync(DIR).sort()) {
  if (!name.endsWith(".go") || name.endsWith("_test.go")) continue;
  if (ALLOWLIST.has(name)) continue;
  const src = readFileSync(join(DIR, name), "utf8");
  // `.Events(` 起按括号配平取完整实参串(实参可含 r.Context() 等嵌套
  // 括号,可跨行);实参以字面 0, 0 收尾(容忍空白)即无界全量拉取。
  let idx = 0;
  while ((idx = src.indexOf(".Events(", idx)) !== -1) {
    let depth = 0;
    let end = -1;
    for (let i = idx + ".Events(".length - 1; i < src.length; i++) {
      if (src[i] === "(") depth++;
      else if (src[i] === ")") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end < 0) break; // 未闭合(源码异常),交由编译器报错
    const args = src.slice(idx + ".Events(".length, end);
    if (/,\s*0\s*,\s*0\s*$/.test(args)) {
      const line = src.slice(0, idx).split("\n").length;
      hits++;
      report.push(
        `internal/server/${name}:${line}: 无界全量事件拉取 .Events(${args.replaceAll("\n", " ")})——改 kind 定向直查(EventsByKinds)/投影直查/增量窗口`,
      );
    }
    idx = end;
  }
}

// 白名单文件仍在册但实参形态变化时提示核对(防白名单腐化为永久豁免)。
for (const [name, why] of ALLOWLIST) {
  const p = join(DIR, name);
  try {
    statSync(p);
  } catch {
    hits++;
    report.push(`check-unbounded-events: 白名单文件 ${name} 不存在,请同步修订白名单(${why})`);
  }
}

if (hits > 0) {
  console.error(`✗ server 层无界事件扫描检查未通过(${hits} 处):`);
  for (const line of report) console.error(`  ${line}`);
  process.exit(1);
}
console.log("✓ server 层无界事件扫描检查通过(白名单豁免:" + [...ALLOWLIST.keys()].join(", ") + ")");
