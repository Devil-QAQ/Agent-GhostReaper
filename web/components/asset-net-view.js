"use client";

import * as React from "react";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { SearchInput } from "@/components/search-input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ASSET_CLASSES, assetClassMeta } from "@/lib/api";
import { AssetNetEngine } from "@/lib/asset-net-engine";
import { useViewportHeight } from "@/lib/use-viewport-height";

export function AssetNetView({ assets, hierarchy, relations = [], focus, onOpenDetail }) {
  const viewport = useViewportHeight(420), canvas = React.useRef(null), minimap = React.useRef(null), engine = React.useRef(null);
  const sequence = React.useRef(0), latest = React.useRef({ assets, hierarchy, relations, focus, onOpenDetail });
  latest.current = { assets, hierarchy, relations, focus, onOpenDetail };
  const [selected, setSelected] = React.useState(null), [hover, setHover] = React.useState(null), [layout, setLayout] = React.useState(null), [failure, setFailure] = React.useState(""), [query, setQuery] = React.useState(""), [kind, setKind] = React.useState("all"), [retry, setRetry] = React.useState(0);
  const [location, setLocation] = React.useState(null);
  // Canonical structural key avoids rerunning layout for unchanged poll results.
  const structure = JSON.stringify([assets.map(a => [a.id, a.class]).sort(), hierarchy.map(e => [e.from, e.to]).sort(), relations.map(e => [e.kind,e.from,e.to]).sort()]);
  React.useEffect(() => {
    const eng = new AssetNetEngine(canvas.current, minimap.current, {
      onNodeClick: node => { setHover(null); eng.setHoveredAsset(null); eng.focusAsset(node.id); },
      onOpenDetail: node => latest.current.onOpenDetail({ type: "asset", id: node.id }),
      onFocus: id => setSelected(id),
      onHover: node => { eng.setHoveredAsset(node?.id || null); setHover(node?.id || null); },
      onBackgroundClick: () => { setHover(null); eng.setHoveredAsset(null); eng.exitFocus(); },
    });
    engine.current = eng;
    return () => { eng.destroy(); engine.current = null; };
  }, []);
  React.useEffect(() => {
    let alive = true; setFailure("");
    const job = new Worker(new URL("../lib/asset-net.worker.mjs", import.meta.url), { type: "module" });
    job.onmessage = ({ data }) => {
      if (!alive || data.sequence !== sequence.current) return;
      if (data.error) { setFailure(data.error); return; }
      setLayout(data.layout);
    };
    job.onerror = () => { if (alive) setFailure("资产网布局失败"); };
    const id = ++sequence.current;
    job.postMessage({ sequence: id, assets: latest.current.assets, hierarchy: latest.current.hierarchy, relations:latest.current.relations });
    return () => { alive = false; job.terminate(); };
  }, [structure, retry]);
  React.useEffect(() => {
    const eng = engine.current; if (!eng || !layout) return;
    const fresh = new Map(assets.map(a => [a.id, a]));
    const nodes = layout.nodes?.map(n => ({ ...n, ...fresh.get(n.id), ti: fresh.get(n.id)?.display_title || fresh.get(n.id)?.title || n.id }));
    const visible = new Set(nodes.filter(n => kind === "all" || n.class === kind).map(n => n.id));
    // Include actual ancestors to retain a readable solar context when filtering.
    let changed = true;
    while (changed) {
      changed = false;
      for (const e of layout.edges || []) {
        if (e.rel !== "sub_of") continue;
        const parent = e.from, child = e.to;
        if (visible.has(child) && !visible.has(parent)) { visible.add(parent); changed = true; }
      }
    }
    eng.setData({ ...layout, nodes: nodes.filter(n => visible.has(n.id)), edges: layout.edges?.filter(e => visible.has(e.from) && visible.has(e.to)) });
  }, [layout, assets, kind]);
  React.useEffect(() => {
    const eng = engine.current;
    if (eng?.focus && eng.byId.has(eng.focus)) { eng.focusAsset(eng.focus, true); }
  }, [layout, kind]);
  React.useEffect(() => {
    const eng = engine.current, id = location?.id || focus;
    if (id && eng?.byId.has(id)) eng.focusAsset(id);
  }, [layout, focus, location]);
  const matches = query.trim() ? assets.filter(a => String(`${a.title} ${a.id}`).toLowerCase().includes(query.trim().toLowerCase())).slice(0, 8) : [];
  const selectedAsset = assets.find(a => a.id === selected), hoveredAsset = assets.find(a => a.id === hover);
  const locate = id => { setQuery(""); setKind("all"); setLocation({ id }); };
  return <div ref={viewport} className="flex min-h-[420px] min-w-0 flex-col gap-3" data-asset-net>
    <PageHeader title="资产网" subtitle={<span className="inline-flex flex-wrap items-center gap-3"><span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-status-success" />已覆盖</span><span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-muted-foreground/35" />未覆盖</span><Select value={kind} onValueChange={value => { setKind(value); engine.current?.exitFocus(); }}><SelectTrigger className="h-8 w-36 text-xs" aria-label="资产类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部类型</SelectItem>{ASSET_CLASSES.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select></span>}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
      <div className="relative w-48 max-w-full"><SearchInput aria-label="搜索资产" placeholder="搜索资产名称或编号" value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => { if (e.key === "Escape") setQuery(""); if (e.key === "Enter" && matches[0]) locate(matches[0].id); }} />
        {query.trim() && <div className="absolute top-9 left-0 z-20 w-80 max-w-[calc(100vw-48px)] rounded-xl bg-popover p-1 shadow-lg">{matches.length ? matches.map(a => <button key={a.id} className="flex w-full flex-col gap-1 rounded-lg px-3 py-2 text-left text-xs hover:bg-hover-lift cursor-pointer" onClick={() => locate(a.id)}><span className="break-all">{a.title}</span><span className="text-muted-foreground">{assetClassMeta(a.class).label} · {a.id}</span></button>) : <div className="px-3 py-2 text-xs text-muted-foreground">没有匹配资产</div>}</div>}
      </div>
      <Button size="sm" variant="outline" className="h-8 text-xs" onClick={() => engine.current?.initialView()}>初始视野</Button>
      <Button size="sm" variant="outline" className="h-8 text-xs" onClick={() => engine.current?.fitAll()}>查看全景</Button>
      {selectedAsset && <><Button size="sm" variant="outline" className="h-8 text-xs" onClick={() => engine.current?.exitFocus()}>上一步</Button><span className="max-w-48 truncate text-xs" title={selectedAsset.title}>{selectedAsset.title}</span><Button size="sm" variant="outline" className="h-8 text-xs" onClick={() => latest.current.onOpenDetail({ type: "asset", id: selected })}>详情</Button></>}
      </div>
    </PageHeader>
    <div className="relative min-h-0 flex-1 overflow-hidden rounded-2xl bg-background shadow-sm" data-asset-net-state={selected ? "focus" : "overview"}>
      <canvas ref={canvas} className="block h-full w-full touch-none" aria-label="资产网，拖动平移，滚轮缩放，点击资产查看关联" />
      {!layout && !failure && <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-background"><div className="h-32 w-48 animate-pulse rounded-xl bg-muted" aria-label="读取中" /></div>}
      {failure && <div className="absolute inset-0 flex items-center justify-center gap-3 bg-background text-sm text-muted-foreground" role="alert">{failure}<Button size="sm" variant="outline" onClick={() => setRetry(value => value + 1)}>重试</Button></div>}
      {hoveredAsset && <div className="pointer-events-none absolute top-3 left-3 max-w-xs rounded-xl bg-popover px-3 py-2 text-xs shadow-lg"><div className="break-all">{hoveredAsset.title}</div><div className="mt-1 text-muted-foreground">{assetClassMeta(hoveredAsset.class).label} · {hoveredAsset.id}{hoveredAsset.scope_state === "pending" ? " · 待批" : hoveredAsset.excluded ? " · 范围外" : ""}</div></div>}
      <div className="absolute right-3 bottom-3 flex flex-col items-end gap-2"><div className="flex gap-1 rounded-lg bg-popover p-1 shadow-sm"><Button size="sm" variant="ghost" className="h-8 w-8 p-0" aria-label="放大" onClick={() => engine.current?.zoomBy(1.25)}>+</Button><Button size="sm" variant="ghost" className="h-8 w-8 p-0" aria-label="缩小" onClick={() => engine.current?.zoomBy(.8)}>−</Button></div><canvas ref={minimap} className="h-24 w-40 cursor-crosshair rounded-lg opacity-80 shadow-sm" onClick={e => engine.current?.minimapJump(e)} aria-label="资产网小地图" /></div>
    </div>
    <div className="flex flex-wrap justify-between gap-2 text-xs text-muted-foreground"><span className="num">资产 {assets.length} · 资产组 {layout?.roots?.length || 0} · 关联 {layout?.edges?.length || 0}</span><span>拖动平移 · 点击资产看关联 · 点击空白返回</span></div>
  </div>;
}
