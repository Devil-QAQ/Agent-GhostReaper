import * as React from "react";

import { cn } from "@/lib/utils";

// 页面头三段式(8.1④):H1 标题 + 一句话语义副标题(#43,说明数据来源
// 与刷新机制)+ 右侧主操作槽位。
// P4-31 ②:页面标题 22 粗(.t-page);分组靠留白不靠线(8.9 #2,去底线)。
export function PageHeader({ title, subtitle, children, className }) {
  return (
    <div
      className={cn("flex flex-wrap items-end justify-between gap-3 pb-2", className)}
    >
      <div className="min-w-0">
        <h1 className="t-page">{title}</h1>
        {subtitle && (
          <p className="mt-1 text-xs text-muted-foreground">{subtitle}</p>
        )}
      </div>
      {children && <div className="flex items-center gap-2">{children}</div>}
    </div>
  );
}
