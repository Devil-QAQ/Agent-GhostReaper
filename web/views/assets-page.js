"use client";

import * as React from "react";

import { EmptyState } from "@/components/empty-state";
import { AssetNetView } from "@/components/asset-net-view";
import { iconOf } from "@/components/icons";
import { SearchInput } from "@/components/search-input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ASSET_CLASSES, assetClassMeta, assetSourceMeta, usePolling } from "@/lib/api";
import { AssetAddDialog } from "@/views/asset-add-dialog";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 资产树页(8.7 [P1]):12 类筛选轮盘(badge 计数,点击过滤)+ 覆盖进度条
// + 可折叠缩进树 + 点击节点 Sheet 抽屉(资产详情/事实/挂载发现,
// P2-09 起引用可下钻、详情可跳图)。
// 覆盖徽标三态:未测(灰)/已覆盖(绿)/范围外(灰,P4-05 与 RoE 联动——
// 已从授权范围摘除,主宰不再派发);"测试中"数据路径未开(方案 §四)。
// 来源标记三态(P4-05):种子 / 手动 / 发现,与账本 detail.source 同源。
// 范围分组(P4-44):渗透任务的范围外发现资产入"待批"分组(锁定不测);
// 红队/未标任务的入"发现的其他资产"分组(直接可测)。P4-52 ⑤:审批
// 集中 RoE——待批分组只展示＋计数,处置跳 RoE 审批区(卡区两键/批量)。
// P2-09 跳转协议:?focus=<资产ID> 直达定位——展开祖先链、滚动入视、
// 短暂高亮(任务投影「在图中查看」等视图的落点)。
export function AssetsPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const { data, error } = usePolling(tid ? `/api/tasks/${tid}/assets` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
  });
  const [classFilter, setClassFilter] = React.useState(null);
  const [query, setQuery] = React.useState("");
  const [view, setView] = React.useState("net");
  const [expanded, setExpanded] = React.useState({});
  const [defaultExpanded, setDefaultExpanded] = React.useState(true);
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
      <div className="flex flex-col gap-3">
        <PageHeader title="资产树" subtitle="查看目标资产、测试覆盖与关联发现" />
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
      <div className="flex flex-col gap-3">
        <PageHeader title="资产树" subtitle="查看目标资产、测试覆盖与关联发现">
          <Button size="sm" onClick={() => setAddOpen(true)}>
            {React.createElement(iconOf("plus"), { className: "size-4" })}
            新增资产
          </Button>
        </PageHeader>
        <EmptyState
          icon="network"
          title="暂无资产"
          hint={error ? `读取失败:${error}` : "暂无资产，可手动添加或等待任务发现"}
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


  const covered = assets.filter((a) => a.covered).length;
  const roots = assets.filter((a) => !parentOf[a.id] || !byID[parentOf[a.id]]);
  const branchIDs = assets.filter(a => (childrenOf[a.id] || []).some(id => byID[id])).map(a => a.id);
  const anyExpanded = branchIDs.some(id => expanded[id] ?? defaultExpanded);
  const toggleAll = () => {
    const open = !anyExpanded;
    setDefaultExpanded(open);
    setExpanded({});
  };

  const matchFilter = (a) => (!classFilter || a.class === classFilter) && (!query.trim() || String(`${a.title} ${a.id}`).toLowerCase().includes(query.trim().toLowerCase()));

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

// Source is explicit text so it is distinguishable from coverage color.
function SourceMark({ node }) {
  const meta = assetSourceMeta(node.source);
  return <span className="inline-flex shrink-0 items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-[11px] text-muted-foreground" title={`${meta.label}资产`}>
    {React.createElement(iconOf(meta.icon), { className: "size-3" })}{meta.label}
  </span>;
}

const renderNode = (node, depth, visited) => {
    if (visited.has(node.id)) return null;
    const nextVisited = new Set(visited);
    nextVisited.add(node.id);
    const kids = (childrenOf[node.id] || [])
      .map((id) => byID[id])
      .filter(Boolean)
      .filter((k) => (!classFilter && !query.trim()) || subtreeMatch(k.id, new Set()))
      .sort((x, y) => x.created_rev - y.created_rev || (x.id < y.id ? -1 : 1));
    const meta = assetClassMeta(node.class);
    const Icon = iconOf(meta.icon);
    const hasKids = (childrenOf[node.id] || []).some((k) => byID[k]);
    const isOpen = expanded[node.id] ?? defaultExpanded;

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
          <Icon className={cn("size-4 shrink-0", node.covered ? "text-status-success" : "text-muted-foreground/55")} />
          <span className={cn("min-w-0 flex-1 truncate text-sm", node.covered ? "text-status-success" : "text-muted-foreground")} title={node.title || node.id}>{node.display_title || node.title || node.id}</span>
          <SourceMark node={node} />
		  <Badge variant={node.covered ? "success" : "outline"} className={node.covered ? "" : "text-muted-foreground"}>
		    {node.covered ? "已覆盖" : "未测"}
		  </Badge>
          <span className="ml-auto hidden shrink-0 num text-xs text-muted-foreground sm:inline">
            {node.id}
          </span>
        </div>
        {isOpen && kids.map((k) => renderNode(k, depth + 1, nextVisited))}
      </div>
    );
  };

  const pct = assets.length ? Math.round((covered / assets.length) * 100) : 0;

  return (
    <div className="flex flex-col gap-3">
      <PageHeader title="资产" subtitle="查看目标资产、测试覆盖与关联发现" />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-lg bg-muted p-1" role="group" aria-label="资产视图">
          {[["net", "资产网"], ["tree", "资产树"]].map(([id, label]) => <button key={id} aria-pressed={view === id} className={cn("flex items-center rounded-md px-3 py-1 text-[13px] transition-colors cursor-pointer", view === id ? "bg-popover text-foreground shadow-xs" : "text-muted-foreground hover:bg-hover-lift hover:text-foreground")} onClick={() => setView(id)}>{label}</button>)}
        </div>
        <Button size="sm" className="h-8 text-xs" onClick={() => setAddOpen(true)}>
          {React.createElement(iconOf("plus"), { className: "size-4" })}新增资产
        </Button>
      </div>

      {view === "net" ? <AssetNetView key={tid} assets={assets} hierarchy={hierarchy} relations={data?.relations || []} focus={focus} onOpenDetail={setDetail} /> : <>

      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl bg-muted/35 px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-xs">
          <span className="text-muted-foreground">资产总数 <span className="ml-2 text-sm font-semibold text-foreground num">{assets.length}</span></span>
          <span className="text-muted-foreground" >已覆盖 <span className="ml-2 text-sm font-semibold text-status-success num">{covered}<span className="text-xs font-normal text-muted-foreground"> / {assets.length}</span></span></span>
          <div className="h-1 w-24 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="资产覆盖" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}><div className="h-full rounded-full bg-status-success transition-[width]" style={{ width: `${pct}%` }} /></div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="w-48 max-w-full"><SearchInput aria-label="搜索资产树" placeholder="搜索资产名称或编号" value={query} onChange={event => setQuery(event.target.value)} /></div>
          <Select value={classFilter || "all"} onValueChange={value => setClassFilter(value === "all" ? null : value)}><SelectTrigger className="h-8 w-36 text-xs" aria-label="资产树类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部类型</SelectItem>{ASSET_CLASSES.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
          {branchIDs.length > 0 && <Button size="sm" variant="outline" className="h-8 gap-1.5 text-xs" aria-expanded={Boolean(anyExpanded)} onClick={toggleAll}>
            {React.createElement(iconOf(anyExpanded ? "chevronRight" : "chevronDown"), { className: "size-3.5" })}{anyExpanded ? "全部收起" : "全部展开"}
          </Button>}
        </div>
      </div>

      {/* 缩进树 */}
      <div className="rounded-xl bg-card px-3 py-2">
        {roots.some((r) => (!classFilter && !query.trim()) || subtreeMatch(r.id, new Set())) ? (
          roots
            .filter((r) => (!classFilter && !query.trim()) || subtreeMatch(r.id, new Set()))
            .map((r) => renderNode(r, 0, new Set()))
        ) : (
          <div className="py-4 text-center text-sm text-muted-foreground">
            没有匹配资产
          </div>
        )}
      </div>

      </>}

      <NodeSheetHost tid={tid} value={detail} onChange={setDetail} animateNavigation />
      <AssetAddDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        taskID={tid}
        assets={assets}
      />
    </div>
  );
}
