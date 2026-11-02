import { cva } from "class-variance-authority";
import * as React from "react";

import { cn } from "@/lib/utils";

// 徽标变体=8.4 状态色/严重级色全站统一映射(胶囊:软底+全彩字+同色边)。
const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap [&_svg]:size-3",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground",
        secondary: "border-transparent bg-secondary text-secondary-foreground",
        outline: "border-border text-foreground",
        destructive: "border-transparent bg-destructive text-destructive-foreground",
        success:
          "border-status-success bg-status-success-soft text-status-success",
        warning:
          "border-status-warning bg-status-warning-soft text-status-warning",
        danger: "border-status-danger bg-status-danger-soft text-status-danger",
        running:
          "border-status-running bg-status-running-soft text-status-running",
        paused: "border-status-paused bg-status-paused-soft text-status-paused",
        critical:
          "border-severity-critical bg-severity-critical-soft text-severity-critical",
        high: "border-severity-high bg-severity-high-soft text-severity-high",
        medium:
          "border-severity-medium bg-severity-medium-soft text-severity-medium",
        low: "border-severity-low bg-severity-low-soft text-severity-low",
        info: "border-severity-info bg-severity-info-soft text-severity-info",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

function Badge({ className, variant, ...props }) {
  return (
    <span
      data-slot="badge"
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  );
}

export { Badge, badgeVariants };
