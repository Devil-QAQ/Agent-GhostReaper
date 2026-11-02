"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// CopyIDButton 标识复制入口(P2-14b①):名称/标题为主视觉,随机 ID
// 弱化为悬停可见的复制入口,不再作行内主信息。全站统一件;noun 指明
// 标识类别(缺省"任务标识",发现用"声明标识"等,toast 与悬停同词)。
export function CopyIDButton({ id, noun = "任务标识", className }) {
  const copy = async (e) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(id);
      toast.success(`已复制${noun} ${id}`);
    } catch {
      toast.error("复制失败,标识见悬停提示");
    }
  };
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("size-6 shrink-0 text-muted-foreground hover:text-foreground", className)}
      title={`复制${noun} ${id}`}
      onClick={copy}
    >
      {React.createElement(iconOf("copy"), { className: "size-3" })}
    </Button>
  );
}
