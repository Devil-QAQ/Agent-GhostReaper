"use client";

import * as React from "react";

import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { KpiCard } from "@/components/kpi-card";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ASSET_CLASSES, assetClassMeta, assetSourceMeta, postJSON, usePolling } from "@/lib/api";
import { AssetAddDialog } from "@/views/asset-add-dialog";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

// 资产树页(8.7 [P1]):12 类筛选轮盘(badge 计数,点击过滤)+ 覆盖进度条
// + 可折叠缩进树 + 点击节点 Sheet 抽屉(资产详情/事实与假设/挂载发现,
// P2-09 起引用可下钻、详情可跳图)。
// 覆盖徽标三态:未测(灰)/已覆盖(绿)/范围外(灰,P4-05 与 RoE 联动——
// 已从授权范围摘除,主宰不再派发);"测试中"数据路径未开(方案 §四)。
// 来源标记三态(P4-05):种子 / 手动 / 发现,与账本 detail.source 同源。
// 范围分组(P4-44):渗透任务的范围外发现资产入"待批"分组(锁定不测,
// 放行/忽略);红队/未标任务的入"发现的其他资产"分组(直接可测)。
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
  const [scopeBusy, setScopeBusy] = React.useState(false);
  const lastFocused = React.useRef(null);

  const assets = data?.assets || [];
  const hierarchy = data?.hierarchy || [];

  // 范围分组(P4-44):待批=渗透任务的范围外发现资产(锁定不测,等放行/
  // 忽略);"发现的其他资产"=红队/未标任务的范围外发现资产(直接可测,
  // 归属标注呈现)。两类均不入主树与覆盖分母(待批不可测;"其他资产"
  // 单独分组呈现,计入总数不计入主树)。
  const pendingAssets = assets.filter((a) => a.scope_state === "pending");
  const otherAssets = assets.filter((a) => a.scope_state === "out_open");

  // 待批批量处置(P4-44 ③):放行=转入测试范围(下一批单位可测);忽略
  // =明确不测,离开待批分组(RoE 纳入可回待批)。列表 2 秒轮询自然刷新。
  const scopeBatch = async (verb, ids) => {
    if (scopeBusy || ids.length === 0) return;
    setScopeBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${tid}/scope/${verb}`, { assets: ids });
    setScopeBusy(false);
    if (!ok) {
      toast.error(`${verb === "approve" ? "放行" : "忽略"}失败:${body.error || "原因未说明"}`);
      return;
    }
    toast.success(
      `已${verb === "approve" ? "放行" : "忽略"} ${body.handled} 项${body.pending > 0 ? `,待批剩 ${body.pending} 项` : ",待批已清"}`
    );
  };

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

  // 覆盖口径(P4-05):摘除资产不计入覆盖分母——它不在授权域内,不是
  // 待测面;总数照实呈现。P4-44:待批资产不计入(锁定不可测,放行后回
  // 分母);"发现的其他资产"可测、计入分母(单独分组呈现,不在主树)。
  const inScope = assets.filter((a) => !a.excluded && a.scope_state !== "pending");
  const covered = inScope.filter((a) => a.covered).length;

  // 类计数(总数 + 覆盖数,轮盘完整版 P2-10:12 类全部在场,零计数类
  // 置灰不隐藏;每类计数徽章 = 已覆盖/总数)。口径与覆盖进度同源
  //(可测资产,含"发现的其他资产";待批与摘除不在内)。P4-44。
  const classCount = {};
  const classCovered = {};
  for (const a of inScope) {
    classCount[a.class] = (classCount[a.class] || 0) + 1;
    if (a.covered) classCovered[a.class] = (classCovered[a.class] || 0) + 1;
  }

  // 主树:范围内常规资产(待批与"发现的其他资产"在独立分组,不在主树;
  // 父节点被分走的子节点上提为根,如实不断链)。
  const treeAssets = assets.filter((a) => a.scope_state !== "pending" && a.scope_state !== "out_open");
  const groupState = (a) => a.scope_state === "pending" || a.scope_state === "out_open";
  const roots = treeAssets.filter((a) => {
    const p = parentOf[a.id];
    return !p || !byID[p] || groupState(byID[p]);
  });

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

// ScopeGroup 范围分组(P4-44):待批(渗透,锁定不测 + 放行/忽略)与
// "发现的其他资产"(红队/未标,归属标注)。折叠分组 + 计数;待批带
// 行内单件处置与头部批量(放行/忽略),其他资产纯呈现。开合态由页面
// 持有(轮询数据瞬时为空时卸载重挂不丢展开状态,与主树 expanded 同模式)。
function ScopeGroup({ title, hint, items, badgeText, open, onToggle, busy, onBatch, onOpenDetail }) {
  const [confirmAsk, setConfirmAsk] = React.useState(null); // {verb, ids, label}
  const ids = items.map((a) => a.id);
  const renderRow = (a) => {
    const meta = assetClassMeta(a.class);
    const Icon = iconOf(meta.icon);
    return (
      <div
        key={a.id}
        className="flex items-center gap-2 rounded-md py-1 pr-2 hover:bg-hover-lift cursor-pointer transition-colors duration-150"
        onClick={() => onOpenDetail(a.id)}
      >
        <span className="w-4 shrink-0" />
        <Icon className="size-4 shrink-0 text-muted-foreground" />
        <span className="truncate text-sm" title={a.title || a.id}>{a.title || a.id}</span>
        <Badge variant="outline" className={badgeText === "待批" ? "text-status-warning" : "text-muted-foreground"}>
          {badgeText}
        </Badge>
        <SourceMark node={a} />
        {onBatch && (
          <span className="ml-auto flex shrink-0 items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              className="h-6 px-2 text-xs"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                setConfirmAsk({ verb: "approve", ids: [a.id], label: a.title || a.id });
              }}
            >
              放行
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs text-muted-foreground"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                setConfirmAsk({ verb: "ignore", ids: [a.id], label: a.title || a.id });
              }}
            >
              忽略
            </Button>
          </span>
        )}
        {!onBatch && (
          <span className="ml-auto shrink-0 num text-xs text-muted-foreground">
            事实 {a.fact_count} · {a.id}
          </span>
        )}
      </div>
    );
  };
  return (
    <div className="rounded-xl border border-border/55 bg-card px-3 py-2">
      <div className="flex items-center gap-2 py-1">
        <button
          className="shrink-0 text-muted-foreground hover:text-foreground cursor-pointer"
          title={open ? "收起" : "展开"}
          onClick={onToggle}
        >
          {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), { className: "size-4" })}
        </button>
        <button className="flex items-center gap-2 cursor-pointer" onClick={onToggle}>
          <span className="text-[13px] font-medium">{title}</span>
          <span className="num text-xs text-muted-foreground">{items.length}</span>
          <span className="text-xs text-muted-foreground" title={hint}>{hint}</span>
        </button>
        {onBatch && open && (
          <span className="ml-auto flex items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              className="h-8 px-2 text-xs"
              disabled={busy}
              onClick={() => setConfirmAsk({ verb: "approve", ids, label: `全部 ${ids.length} 项` })}
            >
              全部放行
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-8 px-2 text-xs text-muted-foreground"
              disabled={busy}
              onClick={() => setConfirmAsk({ verb: "ignore", ids, label: `全部 ${ids.length} 项` })}
            >
              全部忽略
            </Button>
          </span>
        )}
      </div>
      {open && <div className="flex flex-col">{items.map(renderRow)}</div>}
      {/* 处置确认:放行扩大测试范围、忽略明确不测——一次确认后执行 */}
      <Dialog open={Boolean(confirmAsk)} onOpenChange={(o) => !o && setConfirmAsk(null)}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{confirmAsk?.verb === "approve" ? "放行待批资产" : "忽略待批资产"}</DialogTitle>
            <DialogDescription>
              {confirmAsk?.verb === "approve"
                ? `将 ${confirmAsk?.label} 转入测试范围,虫群可对它发起测试。`
                : `明确不测 ${confirmAsk?.label},它将离开待批分组。`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmAsk(null)}>
              取消
            </Button>
            <Button
              onClick={() => {
                const ask = confirmAsk;
                setConfirmAsk(null);
                onBatch(ask.verb, ask.ids);
              }}
            >
              {confirmAsk?.verb === "approve" ? "放行" : "忽略"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

const renderNode = (node, depth, visited) => {
    if (visited.has(node.id)) return null;
    const nextVisited = new Set(visited);
    nextVisited.add(node.id);
    const kids = (childrenOf[node.id] || [])
      .map((id) => byID[id])
      .filter(Boolean)
      .filter((k) => !groupState(k))
      .filter((k) => !classFilter || subtreeMatch(k.id, new Set()))
      .sort((x, y) => x.created_rev - y.created_rev || (x.id < y.id ? -1 : 1));
    const meta = assetClassMeta(node.class);
    const Icon = iconOf(meta.icon);
    const hasKids = (childrenOf[node.id] || []).some((k) => byID[k] && !groupState(byID[k]));
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

  // 覆盖口径的呈现数字(P4-44):范围外计数含摘除与待批(均不可测)。
  const excludedCount = assets.length - inScope.length - pendingAssets.length;

  const pct = inScope.length ? Math.round((covered / inScope.length) * 100) : 0;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="资产树" subtitle="资产随虫群探测生长 · 覆盖进度实时刷新">
        <Button onClick={() => setAddOpen(true)}>
          {React.createElement(iconOf("plus"), { className: "size-4" })}
          新增资产
        </Button>
      </PageHeader>

      {/* 待批分组(P4-44 ③:渗透任务的范围外发现资产,折叠 + 计数 +
          放行/忽略;默认收起,行内单件处置 + 头部批量) */}
      {pendingAssets.length > 0 && (
        <ScopeGroup
          title="待批"
          hint="虫群发现的范围外资产,未放行不测"
          items={pendingAssets}
          badgeText="待批"
          open={expanded["grp-pending"] === true}
          onToggle={() => setExpanded((m) => ({ ...m, "grp-pending": !(m["grp-pending"] === true) }))}
          busy={scopeBusy}
          onBatch={(verb, ids) => scopeBatch(verb, ids)}
          onOpenDetail={(id) => setDetail({ type: "asset", id })}
        />
      )}

      {/* 覆盖统计 + 进度条 */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <KpiCard
          label="资产"
          value={assets.length}
          hint={
            excludedCount > 0 || pendingAssets.length > 0
              ? `可测 ${inScope.length} · 范围外 ${excludedCount}${pendingAssets.length > 0 ? ` · 待批 ${pendingAssets.length}` : ""}`
              : `覆盖 ${covered}/${inScope.length}(${pct}%)`
          }
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
          title={`已覆盖 ${covered} / 可测 ${inScope.length}`}
          onClick={() => setClassFilter(null)}
        >
          全部 {covered}/{inScope.length}
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

      {/* 发现的其他资产(P4-44 ④:红队/未标任务的范围外发现资产,归属
          标注呈现;直接可测,无待批无操作) */}
      {otherAssets.length > 0 && (
        <ScopeGroup
          title="发现的其他资产"
          hint="任务范围外发现的相邻资产,直接可测"
          items={otherAssets}
          badgeText="范围外"
          open={expanded["grp-other"] === true}
          onToggle={() => setExpanded((m) => ({ ...m, "grp-other": !(m["grp-other"] === true) }))}
          onOpenDetail={(id) => setDetail({ type: "asset", id })}
        />
      )}

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
