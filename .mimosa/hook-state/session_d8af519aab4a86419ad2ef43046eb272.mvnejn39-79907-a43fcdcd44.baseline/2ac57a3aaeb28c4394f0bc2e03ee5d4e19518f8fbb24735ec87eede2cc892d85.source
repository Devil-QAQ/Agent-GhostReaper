"use client";

import { Command as CommandPrimitive } from "cmdk";
import * as React from "react";

import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// 命令面板基件(P3-10):cmdk(搜索跳转版,动作版够用再升级)套
// 8.4 token 皮——浮层色 + 柔和大投影 + 250ms 平滑。禁用 cmdk 自带的
// ctrl+n/j/p/k vim 键位(与全局 ⌘J 开关冲突,键盘导航走方向键)。

function Command({ className, ...props }) {
  return (
    <CommandPrimitive
      vimBindings={false}
      className={cn(
        "flex h-full w-full flex-col overflow-hidden rounded-xl bg-popover text-popover-foreground",
        className
      )}
      {...props}
    />
  );
}

function CommandInput({ className, ...props }) {
  return (
    <div className="flex items-center gap-2 border-b border-border/60 px-4">
      {React.createElement(iconOf("search"), {
        className: "size-4 shrink-0 text-muted-foreground",
      })}
      <CommandPrimitive.Input
        className={cn(
          "flex h-11 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
          className
        )}
        {...props}
      />
    </div>
  );
}

function CommandList({ className, ...props }) {
  return (
    <CommandPrimitive.List
      className={cn("max-h-[min(384px,60vh)] overflow-y-auto overflow-x-hidden p-2", className)}
      {...props}
    />
  );
}

function CommandEmpty(props) {
  return (
    <CommandPrimitive.Empty
      className="py-6 text-center text-sm text-muted-foreground"
      {...props}
    />
  );
}

function CommandGroup({ className, ...props }) {
  return (
    <CommandPrimitive.Group
      className={cn(
        "flex flex-col gap-0.5 overflow-hidden p-1 text-foreground [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground",
        className
      )}
      {...props}
    />
  );
}

function CommandItem({ className, ...props }) {
  return (
    <CommandPrimitive.Item
      className={cn(
        // 当前项:浅表面 + 品牌色强调(克制,不整块铺色);150ms 平滑。
        "flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none select-none data-[selected=true]:bg-primary/10 data-[selected=true]:text-primary data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50",
        className
      )}
      {...props}
    />
  );
}

export {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
};
