"use client";

import * as React from "react";

import { EmptyState } from "@/components/empty-state";
import { ExportDialog } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { TASK_STATUS, usePolling } from "@/lib/api";

// 一级导航·报告页(P2-13):跨任务报告导出——勾选任务 × 格式矩阵
//(md / zip / csv / json),报告全部确定性组装、零 LLM(R17)。任务空间
// 内的「报告」标签只做本任务展示,跨任务导出集中在此。
export function ReportsPage() {
  const { data, error } = usePolling("/api/tasks", 5000);
  const [selected, setSelected] = React.useState(() => new Set());
  const [exportOpen, setExportOpen] = React.useState(false);

  const tasks = Array.isArray(data) ? data : [];

  const toggle = (id) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };
  const allOn = tasks.length > 0 && tasks.every((t) => selected.has(t.id));
  const toggleAll = () => {
    setSelected(allOn ? new Set() : new Set(tasks.map((t) => t.id)));
  };

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="报告"
        subtitle="跨任务导出 · 报告由账本直接组装 · 本任务报告在任务空间的报告标签"
      />
      {tasks.length === 0 ? (
        <EmptyState
          icon="fileText"
          title="暂无任务"
          hint={error ? `读取失败:${error}` : "创建任务后在此导出报告"}
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div
              role="button"
              tabIndex={0}
              className="flex items-center gap-2 rounded-md border bg-card px-3 py-2 text-[13px] transition-colors cursor-pointer hover:bg-hover-lift"
              onClick={toggleAll}
            >
              <Checkbox checked={allOn} indeterminate={!allOn && selected.size > 0} onCheckedChange={toggleAll} />
              {allOn ? "取消全选" : "全选"}
            </div>
            <Button
              variant="outline"
              className="h-9"
              disabled={selected.size === 0}
              onClick={() => setExportOpen(true)}
            >
              {React.createElement(iconOf("download"), { className: "size-4" })}
              导出所选({selected.size})
            </Button>
            <Label className="ml-auto text-xs text-muted-foreground">
              已选 {selected.size}/{tasks.length}
            </Label>
          </div>

          <div className="flex flex-col gap-3">
            {tasks.map((t) => {
              const st = TASK_STATUS[t.status] || { label: t.status, variant: "info" };
              const on = selected.has(t.id);
              return (
                <div
                  key={t.id}
                  role="button"
                  tabIndex={0}
                  className={
                    "flex flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-shadow cursor-pointer " +
                    (on ? "border-primary/60" : "hover:shadow-card")
                  }
                  onClick={() => toggle(t.id)}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Checkbox checked={on} onCheckedChange={() => toggle(t.id)} />
                    <Badge variant={st.variant}>{st.label}</Badge>
                    <span className="text-sm font-medium">{t.title}</span>
                    <span className="font-mono text-xs text-muted-foreground">{t.id}</span>
                    <span className="ml-auto font-mono text-xs text-muted-foreground">
                      目标 {t.goals_achieved}/{t.goals_total}
                    </span>
                  </div>
                  {(t.targets || []).length > 0 && (
                    <div className="truncate font-mono text-xs text-muted-foreground" title={(t.targets || []).join(" · ")}>
                      {(t.targets || []).join(" · ")}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </>
      )}

      <ExportDialog
        open={exportOpen}
        onOpenChange={setExportOpen}
        title="跨任务导出"
        description={`导出 ${selected.size} 个任务的报告;zip 为每任务一目录(报告 + 逐发现分文件 + 证据包)。`}
        urlOf={(format) =>
          `/api/report/export?format=${format}&tasks=${[...selected].join(",")}`
        }
        fallbackName="reports"
      />
    </div>
  );
}
