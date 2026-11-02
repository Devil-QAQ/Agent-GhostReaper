import * as React from "react";

import { cn } from "@/lib/utils";

function Input({ className, type, ...props }) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        // 8.4 表面色阶:输入件独立表面层(--input-surface),区别于所在面板
        "flex h-9 w-full min-w-0 rounded-md border border-input bg-input-surface px-3 py-1 text-sm whitespace-nowrap outline-none transition-colors duration-150 focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  );
}

export { Input };
