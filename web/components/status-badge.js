"use client";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// StatusBadge 带悬停人话的状态徽标(P4-28 ①②):meta 来自 api.js 词表
//(CLAIM_STATUS / HYP_STATUS / HYP_VERDICT / RETEST_VERDICT / FACT_STATUS),
// meta.live 过程态加呼吸点动画(gr-live,进行色),终态静态;meta.hint
// 为该状态的悬停人话。children 缺省渲染 meta.label(如"验证成立"带前缀
// 的场合由调用方给 children)。
export function StatusBadge({ meta, className, children }) {
  if (!meta) return null;
  return (
    <Badge
      variant={meta.variant || "info"}
      title={meta.hint}
      className={cn(meta.live && "gr-live", className)}
    >
      {children || meta.label}
    </Badge>
  );
}
