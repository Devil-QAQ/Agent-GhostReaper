"use client";

import * as React from "react";

import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { KpiCard } from "@/components/kpi-card";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ASSET_CLASSES, assetClassMeta, assetSourceMeta, usePolling } from "@/lib/api";
import { AssetAddDialog } from "@/views/asset-add-dialog";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 资产树页(8.7 [P1]):12 类筛选轮盘(badge 计数,点击过滤)+ 覆盖进度条
// + 可折叠缩进树 + 点击节点 Sheet 抽屉(资产详情/事实与假设/挂载发现,
// P2-09 起引用可下钻、详情可跳图)。
// 覆盖徽标三态:未测(灰)/已覆盖(绿)/范围外(灰,P4-05 与 RoE 联动——
// 已从授权范围摘除,主宰不再派发);"测试中"数据路径未开(方案 §四)。
// 来源标记三态(P4-05):种子 / 手动 / 发现,与账本 detail.source 同源。
// P2-09 跳转协议:?focus=<资产ID> 直达定位——展开祖先链、滚动入视、
// 短暂高亮(进度图「在图中查看」等视图的落点)。
export function AssetsPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const { data, error } = usePolling(tid ? `/api/tasks/${tid}/assets` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
  });
  const [classFilter, setClassFilter] = React.useState(null);
  const [expanded, setExpanded] = React.useState({});
  const [detail, setDetail] = React.useState(null);
  const [addOpen, setAddOpen] = React.useState(false);
  const [focusPulse, setFocusPulse] = React.useState(null);
  const lastFocused = React.useRef(null);

  const assets = data?.assets || [];
  const hierarchy = data?.hierarchy || [];

  // 父子映射(sub_of 边 from=子、to=父);visited 防环兜底。
  const byID = {};
  for (const a of assets) byID[a.id] = a;
  const childrenOf = {};
  const parentOf = {};
  for (const e of hierarchy) {
    if (!byID[e.from] || !byID[e.to]) continue;
    parentOf[e.from] = e.to;
    (childrenOf[e.to] = childrenOf[e.to] || []).push(e.from);
  }

  // focus 定位:展开祖先链并高亮(ref 防轮询数据更新反复触发)。
  React.useEffect(() => {
    if (!focus) {
      lastFocused.current = null;
      return;
    }
    if (!byID[focus] || lastFocused.current === focus) return;
    lastFocused.current = focus;
    setExpanded((m) => {
      const next = { ...m };
      let cur = parentOf[focus];
      let guard = 0;
      while (cur && guard++ < 64) {
        next[cur] = true;
        cur = parentOf[cur];
      }
      return next;
    });
    setFocusPulse(focus);
    const t = setTimeout(() => setFocusPulse(null), 2400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, assets.length]);

  React.useEffect(() => {
    if (!focusPulse) return;
    document
      .querySelector(`[data-asset-row="${CSS.escape(focusPulse)}"]`)
      ?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focusPulse]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="资产树" subtitle="资产随虫群探测生长 · 覆盖进度实时刷新" />
        <EmptyState
          icon="network"
          title="未选择任务"
          hint="资产树随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  if (assets.length === 0) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="资产树" subtitle="资产随虫群探测生长 · 覆盖进度实时刷新">
          <Button onClick={() => setAddOpen(true)}>
            {React.createElement(iconOf("plus"), { className: "size-4" })}
            新增资产
          </Button>
        </PageHeader>
        <EmptyState
          icon="network"
          title="暂无资产"
          hint={error ? `读取失败:${error}` : "虫群探测回传后,资产树将在此生长;也可手动补入"}
          action={{ label: "新增资产", onClick: () => setAddOpen(true) }}
        />
        <AssetAddDialog
          open={addOpen}
          onOpenChange={setAddOpen}
          taskID={tid}
          assets={assets}
        />
      </div>
    );
  }

  // 类计数(总数 + 覆盖数,轮盘完整版 P2-10:12 类全部在场,零计数类
  // 置灰不隐藏;每类计数徽章 = 已覆盖/总数)。
  const classCount = {};
  const classCovered = {};
  for (const a of assets) {
    classCount[a.class] = (classCount[a.class] || 0) + 1;
    if (a.covered) classCovered[a.class] = (classCovered[a.class] || 0) + 1;
  }

  const roots = assets.filter((a) => !parentOf[a.id]);

  const matchFilter = (a) => !classFilter || a.class === classFilter;

  // 过滤态下,某节点的可见子孙数(决定是否显示)。
  const subtreeMatch = (id, visited) => {
    if (visited.has(id)) return false;
    const next = new Set(visited);
    next.add(id);
    if (matchFilter(byID[id])) return true;
    return (childrenOf[id] || []).some((k) => byID[k] && subtreeMatch(k, next));
  };

  // 缩进类名数组(Tailwind 需静态类名;v4 动态 spacing,10 层缩进足够 P1 树深)。
const INDENT = [
  "ml-0", "ml-6", "ml-12", "ml-18", "ml-24",
  "ml-30", "ml-36", "ml-42", "ml-48", "ml-54",
];

// 来源标记(P4-05 三态):图标 + 悬停说明,与账本 detail.source 同源。
const SourceMark = ({ node }) => {
  const meta = assetSourceMeta(node.source);
  return (
    <span title={`${meta.label}资产`} className="shrink-0">
      {React.createElement(iconOf(meta.icon), {
        className: cn(
          "size-3.5",
          node.source === "manual" ? "text-primary" : "text-status-success"
        ),
      })}
    </span>
  );
};

const renderNode = (node, depth, visited) => {
    if (visited.has(node.id)) return null;
    const nextVisited = new Set(visited);
    nextVisited.add(node.id);
    const kids = (childrenOf[node.id] || [])
      .map((id) => byID[id])
      .filter(Boolean)
      .filter((k) => !classFilter || subtreeMatch(k.id, new Set()))
      .sort((x, y) => x.created_rev - y.created_rev || (x.id < y.id ? -1 : 1));
    const meta = assetClassMeta(node.class);
    const Icon = iconOf(meta.icon);
    const hasKids = (childrenOf[node.id] || []).length > 0;
    const isOpen = expanded[node.id] !== false; // 默认展开

    return (
      <div key={node.id}>
        <div
          data-asset-row={node.id}
          className={cn(
            "flex items-center gap-2 rounded-md py-1 pr-2 hover:bg-hover-lift cursor-pointer transition-colors duration-150",
            INDENT[Math.min(depth, INDENT.length - 1)],
            focusPulse === node.id && "bg-primary/10 ring-2 ring-primary"
          )}
          onClick={() => setDetail({ type: "asset", id: node.id })}
        >
          {hasKids ? (
            <button
              className="shrink-0 text-muted-foreground hover:text-foreground cursor-pointer"
              title={isOpen ? "收起" : "展开"}
              onClick={(e) => {
                e.stopPropagation();
                setExpanded((m) => ({ ...m, [node.id]: !isOpen }));
              }}
            >
              {React.createElement(iconOf(isOpen ? "chevronDown" : "chevronRight"), {
                className: "size-4",
              })}
            </button>
          ) : (
            <span className="w-4 shrink-0" />
          )}
          <Icon className="size-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-sm" title={node.title || node.id}>{node.title || node.id}</span>
          {node.excluded ? (
            <Badge variant="outline" className="text-muted-foreground">
              {React.createElement(iconOf("shieldBan"), { className: "size-3" })}
              范围外
            </Badge>
          ) : (
            <Badge variant={node.covered ? "success" : "paused"}>
              {node.covered ? "已覆盖" : "未测"}
            </Badge>
          )}
          <SourceMark node={node} />
          <span className="ml-auto shrink-0 num text-xs text-muted-foreground">
            事实 {node.fact_count} · 假设 {node.hypothesis_count} · {node.id}
          </span>
        </div>
        {isOpen && kids.map((k) => renderNode(k, depth + 1, nextVisited))}
      </div>
    );
  };

  // 覆盖口径(P4-05):范围外资产不计入覆盖分母——它不在授权域内,
  // 不是待测面;总数照实呈现。
  const inScope = assets.filter((a) => !a.excluded);
  const excludedCount = assets.length - inScope.length;
  const covered = inScope.filter((a) => a.covered).length;

  const pct = inScope.length ? Math.round((covered / inScope.length) * 100) : 0;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="资产树" subtitle="资产随虫群探测生长 · 覆盖进度实时刷新">
        <Button onClick={() => setAddOpen(true)}>
          {React.createElement(iconOf("plus"), { className: "size-4" })}
          新增资产
        </Button>
      </PageHeader>

      {/* 覆盖统计 + 进度条 */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <KpiCard
          label="资产"
          value={assets.length}
          hint={excludedCount > 0 ? `范围内 ${inScope.length} · 范围外 ${excludedCount}` : `覆盖 ${covered}/${inScope.length}(${pct}%)`}
        />
        <KpiCard label="已覆盖" value={covered} hint="已有测试步骤覆盖" tone="success" />
        <KpiCard
          label="事实合计"
          value={assets.reduce((s, a) => s + a.fact_count, 0)}
          hint="按来源资产归集"
        />
        <KpiCard
          label="假设合计"
          value={assets.reduce((s, a) => s + a.hypothesis_count, 0)}
          hint="待排除的其他解释"
        />
      </div>
      <div>
        <div className="mb-1 flex justify-between text-xs text-muted-foreground">
          <span>覆盖进度{excludedCount > 0 ? "(范围内)" : ""}</span>
          <span>
            {covered}/{inScope.length} · {pct}%
          </span>
        </div>
        <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
          <div
            className="h-full rounded-full bg-primary transition-[width]"
            style={{ width: `${pct}%` }}
          />
        </div>
      </div>

      {/* 12 类筛选轮盘完整版(计数徽章 = 已覆盖/总数,点击过滤/再点取消;
          零计数类置灰在场) */}
      <div className="flex flex-wrap gap-2">
        <button
          className={cn(
            "rounded-full border px-3 py-0.5 text-xs cursor-pointer transition-colors",
            !classFilter
              ? "border-primary bg-primary/15 font-medium text-primary"
              : "border-border text-muted-foreground hover:text-foreground"
          )}
          title={`已覆盖 ${covered} / 共 ${assets.length}`}
          onClick={() => setClassFilter(null)}
        >
          全部 {covered}/{assets.length}
        </button>
        {ASSET_CLASSES.map(([v, label]) => {
          const total = classCount[v] || 0;
          const cov = classCovered[v] || 0;
          return (
            <button
              key={v}
              disabled={total === 0}
              className={cn(
                "rounded-full border px-3 py-0.5 text-xs transition-colors",
                total === 0
                  ? "cursor-not-allowed border-border text-muted-foreground/40"
                  : "cursor-pointer",
                total > 0 && classFilter === v
                  ? "border-primary bg-primary/15 font-medium text-primary"
                  : total > 0 && "border-border text-muted-foreground hover:text-foreground"
              )}
              title={total > 0 ? `${label}:已覆盖 ${cov} / 共 ${total}` : `${label}:暂无资产`}
              onClick={() => setClassFilter(classFilter === v ? null : v)}
            >
              {label} {total > 0 ? `${cov}/${total}` : 0}
            </button>
          );
        })}
      </div>

      {/* 缩进树 */}
      <div className="rounded-xl border border-border/55 bg-card px-3 py-2">
        {roots.some((r) => !classFilter || subtreeMatch(r.id, new Set())) ? (
          roots
            .filter((r) => !classFilter || subtreeMatch(r.id, new Set()))
            .map((r) => renderNode(r, 0, new Set()))
        ) : (
          <div className="py-4 text-center text-sm text-muted-foreground">
            该类暂无资产
          </div>
        )}
      </div>

      <NodeSheetHost tid={tid} value={detail} onChange={setDetail} />
      <AssetAddDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        taskID={tid}
        assets={assets}
      />
    </div>
  );
}
