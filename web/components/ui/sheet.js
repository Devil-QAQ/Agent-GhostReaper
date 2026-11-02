"use client";

import * as SheetPrimitive from "@radix-ui/react-dialog";
import { XIcon } from "lucide-react";
import * as React from "react";

import { cn } from "@/lib/utils";
import { FloatingLayer, useFloatingLayer, floatingShadow } from "@/components/ui/floating-layer";

// Sheet 抽屉:大对象详情专用(发现详情/资产详情),右侧滑出不离开列表上下文。
function Sheet({ nested = false, children, ...props }) {
  return (
    <FloatingLayer nested={nested}>
      <SheetPrimitive.Root {...props}>{children}</SheetPrimitive.Root>
    </FloatingLayer>
  );
}
const SheetTrigger = SheetPrimitive.Trigger;
const SheetClose = SheetPrimitive.Close;
const SheetPortal = SheetPrimitive.Portal;

function SheetOverlay({ className, ...props }) {
  return (
    <SheetPrimitive.Overlay
      data-slot="sheet-overlay"
      className={cn(
        "fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        className
      )}
      {...props}
    />
  );
}

function SheetContent({ className, children, side = "right", ...props }) {
  const depth = useFloatingLayer();
  return (
    <SheetPortal>
      <SheetOverlay />
      <SheetPrimitive.Content
        data-slot="sheet-content"
        data-layer-depth={depth}
        className={cn(
          // 8.9:层次靠材质——柔和大投影 + 表面色阶,不用描边;250ms 平滑
          // P4-31 ⑦抽屉:圆角 16、宽 ≤560、弱描边 55%
          "fixed z-50 flex flex-col gap-0 bg-popover transition ease-[cubic-bezier(.4,0,.2,1)] data-[state=closed]:duration-250 data-[state=open]:duration-250 data-[state=open]:animate-in data-[state=closed]:animate-out",
          side === "right" &&
            "inset-y-0 right-0 h-full w-[min(560px,100vw)] max-w-[560px] rounded-l-2xl border-y border-l border-border/55 data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right",
          side === "left" &&
            "inset-y-0 left-0 h-full w-[min(560px,100vw)] max-w-[560px] rounded-r-2xl border-y border-r border-border/55 data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left",
          floatingShadow(depth),
          className
        )}
        {...props}
      >
        {children}
        <SheetPrimitive.Close className="absolute top-4 right-4 flex size-8 items-center justify-center rounded-md opacity-70 transition-colors hover:bg-hover-lift hover:opacity-100 focus:outline-none disabled:pointer-events-none cursor-pointer">
          <XIcon />
          <span className="sr-only">关闭</span>
        </SheetPrimitive.Close>
      </SheetPrimitive.Content>
    </SheetPortal>
  );
}

function SheetHeader({ className, ...props }) {
  return (
    <div
      data-slot="sheet-header"
      className={cn("flex flex-col gap-2 p-5 pr-12", className)}
      {...props}
    />
  );
}

function SheetTitle({ className, ...props }) {
  const depth = useFloatingLayer();
  return (
    <SheetPrimitive.Title
      data-slot="sheet-title"
      className={cn(depth > 1 ? "t-float-nested" : "t-float", "text-foreground", className)}
      {...props}
    />
  );
}

function SheetDescription({ className, ...props }) {
  return (
    <SheetPrimitive.Description
      data-slot="sheet-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

export {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetOverlay,
  SheetPortal,
  SheetTitle,
  SheetTrigger,
};
