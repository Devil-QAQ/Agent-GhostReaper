import * as React from "react";

import { cn } from "@/lib/utils";

// 页面头三段式(8.1④):H1 标题 + 一句话语义副标题(#43,说明数据来源
// 与刷新机制)+ 右侧主操作槽位。
export function PageHeader({ title, subtitle, children, className }) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-end justify-between gap-3 border-b pb-4",
        className
      )}
    >
      <div className="min-w-0">
        {/* 8.4 字号阶梯:页面大标题 28px 加粗档 */}
        <h1 className="text-[28px] leading-tight font-semibold tracking-tight">{title}</h1>
        {subtitle && (
          <p className="mt-2 text-sm text-muted-foreground">{subtitle}</p>
        )}
      </div>
      {children && <div className="flex items-center gap-2">{children}</div>}
    </div>
  );
}
