// 用违规样例检验构建闸，防止新增检查只报“零命中”却实际不拦截。
import { inspectFusionSource } from "./check-fusion-rules.mjs";

const cases = [
  ['<div className="num text-[13px] gap-4" />', true],
  ['<div className="text-5xl" />', false],
  ['<div className="text-[1.5rem]" />', false],
  ['<div style={{fontSize: 17}} />', false],
  ['<div className="font-mono" />', false],
  ['<div className="text-blue-500" />', false],
  ['<div className="gap-[10px]" />', false],
  ['<DialogContent className="p-4" />', false],
  ['<SheetContent className="max-w-3xl" />', false],
  ['<SheetTitle className="text-sm" />', false],
];
for (const [src, allowed] of cases) {
  const findings = inspectFusionSource("components/fusion-rule-probe.js", src);
  if ((findings.length === 0) !== allowed) {
    throw new Error(`融合律闸门验证失败: ${src}\n${findings.join("\n")}`);
  }
}
console.log(`融合律闸门回归通过: ${cases.length} 个正反样例`);
