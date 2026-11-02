"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// 复选框(8.5 数据表批量复选 [P2]):手绘实现,选中态主色底 + 勾。
// 半选(表头批量列)呈短横线。
function Checkbox({ className, checked = false, indeterminate = false, onCheckedChange, ...props }) {
  const state = indeterminate ? "mixed" : checked ? "true" : "false";
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={state}
      title={props.title}
      className={cn(
        "flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-[4px] border transition-colors",
        checked || indeterminate
          ? "border-primary bg-primary text-primary-foreground"
          : "border-muted-foreground/50 hover:border-primary",
        className
      )}
      onClick={(e) => {
        e.stopPropagation();
        onCheckedChange?.(!checked);
      }}
    >
      {indeterminate
        ? React.createElement(iconOf("minus"), { className: "size-3" })
        : checked
          ? React.createElement(iconOf("check"), { className: "size-3" })
          : null}
    </button>
  );
}

export { Checkbox };
