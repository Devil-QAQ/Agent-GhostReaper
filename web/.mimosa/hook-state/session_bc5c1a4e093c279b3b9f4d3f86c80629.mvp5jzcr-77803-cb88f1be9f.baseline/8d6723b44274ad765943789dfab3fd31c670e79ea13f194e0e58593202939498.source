"use client";

import * as React from "react";

import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

// KPI 卡(#40):副文案承载健康度,整卡可点击跳转(hash 路由)。
// tone=danger 时数值用状态色(如待审批>0)。
// P4-31 ①②④:圆角 14、内边距 16×18、等宽标签带字距、30px 粗等宽大数字。
export function KpiCard({ label, value, hint, href, onClick, tone, className }) {
  const clickable = Boolean(href || onClick);
  const handle = () => {
    if (href) location.hash = href;
    if (onClick) onClick();
  };
  return (
    <Card
      className={cn(
        "gap-1 rounded-[14px] px-[18px] py-4",
        clickable && "cursor-pointer transition-shadow hover:shadow-card",
        className
      )}
      onClick={clickable ? handle : undefined}
    >
      <CardContent className="p-0">
        <div className="k-label">{label}</div>
        <div
          className={cn(
            "k-value mt-1.5",
            tone === "danger" && "text-status-danger",
            tone === "warning" && "text-status-warning",
            tone === "success" && "text-status-success"
          )}
        >
          {value}
        </div>
        {hint && (
          <div className="mt-1 text-[11px] text-muted-foreground">{hint}</div>
        )}
      </CardContent>
    </Card>
  );
}
