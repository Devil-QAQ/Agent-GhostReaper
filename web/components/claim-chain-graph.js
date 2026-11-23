"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { usePolling } from "@/lib/api";
import { ClaimChainEngine } from "@/lib/claim-chain-engine";

export function ClaimChainGraph({ tid, claimID, onSelectNode }) {
  return <ClaimChainTask key={`${tid}:${claimID}`} tid={tid} claimID={claimID} onSelectNode={onSelectNode}/>;
}

function ClaimChainTask({ tid, claimID, onSelectNode }) {
  const canvasRef = React.useRef(null), engineRef = React.useRef(null), workerRef = React.useRef(null), sequenceRef = React.useRef(0);
  const selectRef = React.useRef(onSelectNode);
  selectRef.current = onSelectNode;
  const [layout, setLayout] = React.useState(null), [failed, setFailed] = React.useState(false), [retry, setRetry] = React.useState(0), [selected, setSelected] = React.useState(null);
  const { data: skeleton, error } = usePolling(tid ? `/api/tasks/${tid}/pview/skeleton?read=${retry}` : "/api/healthz", 30000, { immediate: Boolean(tid), live: tid ? [`pview:${tid}`] : null });
  React.useEffect(() => {
    const worker = new Worker(new URL("../lib/claim-chain.worker.mjs", import.meta.url), { type: "module" });
    workerRef.current = worker;
    worker.onmessage = ({ data }) => {
      if (data.sequence !== sequenceRef.current) return;
      if (data.error) { setFailed(true); return; }
      setLayout(data.layout); setFailed(false);
    };
    worker.onerror = () => setFailed(true);
    return () => { sequenceRef.current++; workerRef.current = null; worker.terminate(); };
  }, []);
  React.useEffect(() => {
    if (!skeleton || !tid || !claimID) return;
    const controller = new AbortController(), sequence = ++sequenceRef.current;
    setFailed(false);
    (async () => {
      try {
        let current = skeleton;
        for (let attempt = 0; attempt < 3; attempt++) {
          const response = await fetch(`/api/tasks/${tid}/pview/links/${encodeURIComponent(claimID)}`, { signal: controller.signal });
          if (!response.ok) throw new Error();
          const links = await response.json();
          const check = await fetch(`/api/tasks/${tid}/pview/skeleton`, { signal: controller.signal });
          if (!check.ok) throw new Error();
          const latest = await check.json();
          if (latest.rev !== current.rev) { current = latest; continue; }
          if (sequence === sequenceRef.current && !controller.signal.aborted) workerRef.current?.postMessage({ sequence, skeleton: current, links });
          return;
        }
        throw new Error();
      } catch { if (!controller.signal.aborted && sequence === sequenceRef.current) setFailed(true); }
    })();
    return () => controller.abort();
  }, [skeleton, tid, claimID, retry]);
  const ready = Boolean(layout?.nodes?.length);
  React.useEffect(() => {
    if (!ready || !canvasRef.current) return;
    const engine = new ClaimChainEngine(canvasRef.current, null, {
      initialFocus: claimID,
      onNodeClick: n => { engine.select(n.id); setSelected(n); },
      onOpenDetail: target => selectRef.current?.({ ...target, panorama: target.type === "step" }),
    });
    engineRef.current = engine;
    return () => { engine.destroy(); engineRef.current = null; };
  }, [ready, claimID]);
  React.useEffect(() => {
    const engine = engineRef.current;
    if (!engine || !layout) return;
    engine.setData(layout); engine.select(engine.focus || claimID);
    setSelected(engine.byId.get(engine.focus));
  }, [layout, ready, claimID]);
  return <div className="flex min-w-0 flex-col gap-3">
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <span>箭头：来源 → 产出</span>
      <Button variant="outline" size="sm" onClick={() => engineRef.current?.fitAll()}>查看全景</Button>
      <Button variant="ghost" size="sm" aria-label="缩小链路" onClick={() => engineRef.current?.zoomBy(1 / 1.3)}>−</Button>
      <Button variant="ghost" size="sm" aria-label="放大链路" onClick={() => engineRef.current?.zoomBy(1.3)}>＋</Button>
      {selected && onSelectNode && <Button variant="outline" size="sm" onClick={() => onSelectNode({ id: selected.id, type: selected.t === "claim" ? "testclaim" : selected.t, panorama: selected.t === "step" })}>详情</Button>}
    </div>
    <div className="relative h-72 min-w-0 overflow-hidden rounded-lg bg-background" data-claim-chain>
      {ready && <canvas ref={canvasRef} className="absolute inset-0 size-full touch-none" aria-label="攻击链条图"/>}
      {!ready && !failed && !error && <div className="flex flex-col gap-4 p-6" aria-label="读取链路"><div className="h-12 w-48 animate-pulse rounded-lg bg-card"/><div className="h-12 w-32 animate-pulse rounded-lg bg-card"/></div>}
      {(failed || error) && <div className="absolute inset-x-0 bottom-0 flex items-center gap-2 bg-background p-3 text-xs" role="status">读取链路失败<Button variant="ghost" size="sm" onClick={() => setRetry(n => n + 1)}>重试</Button></div>}
      {layout && !ready && !failed && <div className="p-6 text-xs text-muted-foreground">暂无来源链路</div>}
    </div>
    {layout && layout.edges?.length === 0 && <span className="text-xs text-muted-foreground">未记录产生来源</span>}
  </div>;
}
