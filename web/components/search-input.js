"use client";

import * as React from "react";
import { iconOf } from "@/components/icons";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

// 页面搜索统一为192×32；查询、筛选与弹出结果仍由原页面负责。
export function SearchInput({ className, ...props }) {
  return (
    <div className="relative w-48 max-w-full">
      {React.createElement(iconOf("search"), { className: "pointer-events-none absolute top-2 left-2 size-4 text-muted-foreground" })}
      <Input className={cn("h-8 pl-8 text-xs", className)} {...props} />
    </div>
  );
}
