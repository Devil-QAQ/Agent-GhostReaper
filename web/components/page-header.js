import * as React from "react";

import { cn } from "@/lib/utils";

// 当前页面由侧栏/任务页签标明；保留无障碍标题，提示与操作共用一行。
// 待退役的旧图保留原内部页头，外层导航仍由任务空间统一。
export function PageHeader({ title, subtitle, children, className, compact = true }) {
  return (
    <div
      className={cn("flex flex-wrap justify-between", compact ? "min-h-8 items-center gap-2" : "items-end gap-3 pb-2", className)}
      data-page-header={compact ? "compact" : "legacy"}
    >
      <div className="min-w-0">
        <h1 className={compact ? "sr-only" : "t-page"}>{title}</h1>
        {subtitle && (
          <p className={cn("text-xs text-muted-foreground", !compact && "mt-1")}>{subtitle}</p>
        )}
      </div>
      {children && <div className="flex min-w-0 flex-wrap items-center gap-2">{children}</div>}
    </div>
  );
}
