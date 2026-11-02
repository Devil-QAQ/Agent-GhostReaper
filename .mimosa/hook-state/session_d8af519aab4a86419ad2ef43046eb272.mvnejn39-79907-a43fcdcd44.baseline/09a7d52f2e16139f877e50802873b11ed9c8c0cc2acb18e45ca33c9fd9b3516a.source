"use client";

import * as React from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// 导出弹窗(P2-13,第五章 §3:格式 × 范围矩阵的组合下载承载)。
// 格式固定四选;范围选项由调用方声明(发现页:全部/当前筛选/勾选项;
// 报告页:仅全部;跨任务页:不显示范围)。下载 URL 由调用方按
//(format, scope) 构造;文件名取服务端规约(Content-Disposition)。

const FORMATS = [
  ["md", "md 单文件"],
  ["zip", "md 分文件 zip"],
  ["csv", "csv 清单"],
  ["json", "json"],
];

// downloadExport 下载一个导出产物(文件名取服务端规约;供导出弹窗与
// 直连按钮共用)。
export async function downloadExport(url, fallbackName) {
  const r = await fetch(url);
  if (!r.ok) {
    throw new Error((await r.text()) || `HTTP ${r.status}`);
  }
  const name =
    (r.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/)
      ?.[1] || fallbackName || "export";
  const blobUrl = URL.createObjectURL(await r.blob());
  const a = document.createElement("a");
  a.href = blobUrl;
  a.download = name;
  a.click();
  URL.revokeObjectURL(blobUrl);
  return name;
}

export function ExportDialog({
  open,
  onOpenChange,
  title,
  description,
  urlOf,
  scopes,
  fallbackName,
}) {
  const [format, setFormat] = React.useState("md");
  const [scope, setScope] = React.useState(scopes?.[0]?.[0] || "all");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (open) {
      setFormat("md");
      setScope(scopes?.[0]?.[0] || "all");
    }
  }, [open, scopes]);

  const download = async () => {
    setBusy(true);
    try {
      const name = await downloadExport(urlOf(format, scope), fallbackName);
      toast.success(`已导出 ${name}`);
      onOpenChange(false);
    } catch (e) {
      toast.error(`导出失败:${e?.message || "网络未连通"}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4 px-1 py-2">
          <div className="flex flex-col gap-2">
            <div className="text-xs font-medium text-muted-foreground">格式</div>
            <div className="flex flex-wrap gap-2">
              {FORMATS.map(([v, label]) => (
                <button
                  key={v}
                  className={
                    "rounded-md border px-3 py-2 text-[13px] transition-colors cursor-pointer " +
                    (format === v
                      ? "border-primary bg-primary text-primary-foreground"
                      : "bg-card text-muted-foreground hover:text-foreground")
                  }
                  onClick={() => setFormat(v)}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
          {scopes && scopes.length > 1 && (
            <div className="flex flex-col gap-2">
              <div className="text-xs font-medium text-muted-foreground">范围</div>
              <div className="flex flex-wrap gap-2">
                {scopes.map(([v, label]) => (
                  <button
                    key={v}
                    className={cn(
                      "rounded-md border px-3 py-2 text-[13px] transition-colors cursor-pointer",
                      scope === v
                        ? "border-primary bg-primary text-primary-foreground"
                        : "bg-card text-muted-foreground hover:text-foreground",
                    )}
                    onClick={() => setScope(v)}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button onClick={download} disabled={busy}>
            {busy ? "导出中…" : "导出"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
