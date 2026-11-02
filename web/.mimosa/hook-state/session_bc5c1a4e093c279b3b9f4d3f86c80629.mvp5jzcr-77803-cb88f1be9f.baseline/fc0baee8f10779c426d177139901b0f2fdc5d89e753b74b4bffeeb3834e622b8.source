"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// LongText 长文本呈现(P4-02 ③"长文本全站全文可查"的统一件):截断
// 保留 + 悬停完整 tooltip + 点击查看全文。全文按等宽字体呈现(命令 /
// 参数 / 状态行原文的审计价值在原文);variant:
//   inline —— 行内展开(卡片语境,展开区限高内滚)
//   dialog —— 弹层查看原文(紧凑行语境,不打乱布局)
// 短文本(≤max 字符)原样呈现,零交互噪音。
// lines > 0(P4-06 ① 意图摘要):按行数截断(默认一至两行,line-clamp
// 自然省略),点击行内展开全文(展开区限高内滚)——与字符截断同族
// 交互,忽略 max。
export function LongText({ text, max = 60, lines = 0, mono = false, variant = "inline", className, label }) {
  const [open, setOpen] = React.useState(false);
  const s = text || "";
  if (!s) return null;
  const title = label ? `${label}:${s}` : s;
  if (lines > 0) {
    return (
      <button
        type="button"
        className={cn("block min-w-0 max-w-full cursor-pointer text-left", className)}
        title={title}
        onClick={() => setOpen((o) => !o)}
      >
        {/* pre-line(保留换行、空串折叠)+ anywhere 换行(意图可含长
            URL/命令串,不断开会横向溢出被 clamp 裁切)。原生缩放下
            省略号只在末行(逐行变体实验亲证);页面放大(zoom≠1)时
            WebKit 会对每行画省略号——浏览器渲染伪影,不据此改实现。 */}
        <span
          className={cn(
            "block whitespace-pre-line [overflow-wrap:anywhere]",
            open ? "max-h-40 overflow-y-auto" : "overflow-hidden"
          )}
          style={open ? undefined : { display: "-webkit-box", WebkitBoxOrient: "vertical", WebkitLineClamp: lines }}
        >
          {s}
        </span>
      </button>
    );
  }
  if (s.length <= max) {
    return <span className={className}>{s}</span>;
  }
  return (
    <>
      <button
        type="button"
        className={cn(
          "inline-flex min-w-0 max-w-full cursor-pointer items-center gap-1 rounded-sm text-left underline decoration-dotted underline-offset-2",
          className
        )}
        title={title}
        onClick={() => setOpen((o) => (variant === "inline" ? !o : true))}
      >
        <span className={cn("truncate", mono && "font-mono")}>
          {s.slice(0, max)}…
        </span>
        {React.createElement(iconOf("maximize2"), { className: "size-3 shrink-0 text-muted-foreground" })}
      </button>
      {variant === "inline" ? (
        open && (
          <pre className="mt-1 max-h-56 overflow-y-auto rounded-md border bg-input-surface px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-all">
            {s}
          </pre>
        )
      ) : (
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle className="text-base">{label || "原文"}</DialogTitle>
              <DialogDescription>共 {s.length.toLocaleString("en-US")} 字</DialogDescription>
            </DialogHeader>
            <pre className="max-h-[60vh] overflow-y-auto rounded-md bg-input-surface p-3 font-mono text-xs whitespace-pre-wrap break-all">
              {s}
            </pre>
          </DialogContent>
        </Dialog>
      )}
    </>
  );
}
