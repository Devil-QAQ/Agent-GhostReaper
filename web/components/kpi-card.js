"use client";

import * as React from "react";

import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

// KPI 卡(#40):副文案承载健康度,整卡可点击跳转(hash 路由)。
// tone=danger 时数值用状态色(如待审批>0)。
export function KpiCard({ label, value, hint, href, onClick, tone, className }) {
  const clickable = Boolean(href || onClick);
  const handle = () => {
    if (href) location.hash = href;
    if (onClick) onClick();
  };
  return (
    <Card
      className={cn(
        "gap-1 py-3",
        clickable && "cursor-pointer transition-shadow hover:shadow-card",
        className
      )}
      onClick={clickable ? handle : undefined}
    >
      <CardContent className="px-4">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div
          className={cn(
            "mt-1 text-2xl font-semibold tabular-nums",
            tone === "danger" && "text-status-danger",
            tone === "warning" && "text-status-warning",
            tone === "success" && "text-status-success"
          )}
        >
          {value}
        </div>
        {hint && (
          <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>
        )}
      </CardContent>
    </Card>
  );
}
