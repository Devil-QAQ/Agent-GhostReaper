"use client";

import * as DialogPrimitive from "@radix-ui/react-dialog";
import { XIcon } from "lucide-react";
import * as React from "react";

import { cn } from "@/lib/utils";
import { FloatingLayer, useFloatingLayer, floatingShadow } from "@/components/ui/floating-layer";
import { ErrorBoundary } from "@/components/error-boundary";

function Dialog({ nested = false, children, ...props }) {
  return (
    <FloatingLayer nested={nested}>
      <DialogPrimitive.Root {...props}>{children}</DialogPrimitive.Root>
    </FloatingLayer>
  );
}
const DialogTrigger = DialogPrimitive.Trigger;
const DialogPortal = DialogPrimitive.Portal;
const DialogClose = DialogPrimitive.Close;

function DialogOverlay({ className, ...props }) {
  return (
    <DialogPrimitive.Overlay
      data-slot="dialog-overlay"
      className={cn(
        "fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        className
      )}
      {...props}
    />
  );
}

function DialogContent({ className, children, hideClose, ...props }) {
  const depth = useFloatingLayer();
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        data-slot="dialog-content"
        data-layer-depth={depth}
      className={cn(
        // 8.9 #3:浮层柔和大投影 + 表面色阶分层;250ms 平滑(无弹跳)
        // P4-31 ⑦模态弹窗:圆角 16、内边距 24、行距 16、弱描边 55%
        "fixed top-1/2 left-1/2 z-50 grid w-[calc(100vw-32px)] max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 rounded-2xl border border-border/55 bg-popover p-6 duration-250 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
        floatingShadow(depth),
        className
      )}
        {...props}
      >
        {/* 浮层渲染错误边界(P4-60 ④):内容崩降级为区块错误,弹窗框
            与关闭钮在、不塌整页。 */}
        <ErrorBoundary level="overlay">{children}</ErrorBoundary>
        {!hideClose && (
          <DialogPrimitive.Close className="absolute top-4 right-4 flex size-8 items-center justify-center rounded-md opacity-70 transition-colors hover:bg-hover-lift hover:opacity-100 focus:outline-none disabled:pointer-events-none cursor-pointer">
            <XIcon />
            <span className="sr-only">关闭</span>
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  );
}

function DialogHeader({ className, ...props }) {
  return (
    <div
      data-slot="dialog-header"
      className={cn("flex flex-col gap-1.5 text-left", className)}
      {...props}
    />
  );
}

function DialogFooter({ className, ...props }) {
  return (
    <div
      data-slot="dialog-footer"
      className={cn("flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)}
      {...props}
    />
  );
}

function DialogTitle({ className, ...props }) {
  const depth = useFloatingLayer();
  return (
    <DialogPrimitive.Title
      data-slot="dialog-title"
      className={cn(depth > 1 ? "t-float-nested" : "t-float", "pr-8", className)}
      {...props}
    />
  );
}

function DialogDescription({ className, ...props }) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

export {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
};
