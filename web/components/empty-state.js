"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// 空状态(#41):文案解释 + 行动引导("去 xx →"式)。
// action={ label, onClick 或 href }。
// P4-31 ⑦空态:图标 32 灰三级 + 一句陈述 13 + 一个行动入口。
export function EmptyState({ icon = "circleHelp", title, hint, action, className }) {
  const Icon = iconOf(icon);
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-12 text-center",
        className
      )}
    >
      <Icon className="size-8 text-tertiary-foreground" />
      <div className="text-[13px] font-medium">{title}</div>
      {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      {action && (
        <button
          className="mt-2 text-[13px] text-primary underline-offset-4 hover:underline cursor-pointer"
          onClick={() => {
            if (action.href) location.hash = action.href;
            if (action.onClick) action.onClick();
          }}
        >
          {action.label} →
        </button>
      )}
    </div>
  );
}
