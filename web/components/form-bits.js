import * as React from "react";

import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// 设置类弹窗共用小件(P2-13b 自 llm/agent 两页抽取统一):
// Field 左标签右控件的表单行 · Fold 横线—箭头—文字—横线高级折叠 · 输入规格。

export const INPUT = "h-9";
export const NUM = "h-9 w-24 text-right num text-xs";

// Field 表单行:标签定宽左对齐,控件区占满右侧(不换行、不错位)。
export function Field({ label, children, className, labelClassName }) {
  return (
    <div className={cn("flex items-center gap-3", className)}>
      <span className={cn("w-20 shrink-0 text-[13px] text-muted-foreground", labelClassName)}>
        {label}
      </span>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{children}</div>
    </div>
  );
}

// Fold 折叠触发器:横线 — 箭头 — 文字 — 横线;withArrow=false 为纯分隔标题
// (无箭头不折叠,如"运行参数"段)。展开区由调用方条件渲染。
export function Fold({ open, onToggle, children, withArrow = true, className }) {
  if (!withArrow) {
    return (
      <div className={cn("flex w-full items-center gap-3 py-1 text-xs text-muted-foreground", className)}>
        <span className="h-px flex-1 bg-border" />
        <span>{children}</span>
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }
  return (
    <button
      type="button"
      className={cn(
        "flex w-full cursor-pointer items-center gap-3 py-1 text-xs text-muted-foreground transition-colors duration-150 hover:text-foreground",
        className
      )}
      onClick={onToggle}
    >
      <span className="h-px flex-1 bg-border" />
      <span className="flex items-center gap-1">
        {React.createElement(iconOf("chevronDown"), {
          className: cn("size-3.5 transition-transform duration-150", open && "rotate-180"),
        })}
        {children}
      </span>
      <span className="h-px flex-1 bg-border" />
    </button>
  );
}
