"use client";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { fetchJSON } from "@/lib/api";

export function CapabilityContent({ selected, onClose }) {
  const [offset, setOffset] = React.useState(0);
  const pinned = React.useRef({ selected: null, token: "" });
  const [content, setContent] = React.useState(null);
  const [error, setError] = React.useState("");
  React.useEffect(() => { setOffset(selected?.offset || 0); }, [selected]);
  React.useEffect(() => {
    if (!selected) return;
    const controller = new AbortController(); setContent(null); setError("");
    if (pinned.current.selected !== selected) pinned.current = { selected, token: selected.token || "" };
    const params = new URLSearchParams({ path: selected.path, content_version: pinned.current.token, offset: String(offset) });
    fetchJSON(`/api/capability-library/repositories/${selected.source_id}/content?${params}`, { signal: controller.signal }).then((r) => {
      if (controller.signal.aborted) return;
      if (r.ok) { pinned.current.token = r.body.token; setContent(r.body); } else setError(r.body.error || "原文读取失败");
    }).catch(() => { if (!controller.signal.aborted) setError("网络未连通"); });
    return () => controller.abort();
  }, [selected, offset]);
  return <Sheet open={Boolean(selected)} onOpenChange={(open) => { if (!open) onClose(); }}><SheetContent>
    <SheetHeader><SheetTitle>文件原文</SheetTitle><SheetDescription className="break-all">{selected?.source} · {selected?.path}</SheetDescription>
      <Button size="sm" variant="outline" onClick={() => { location.hash = `/capabilities?repo=${encodeURIComponent(selected.source_id)}&file=${encodeURIComponent(selected.path)}`; onClose(); }}>打开文件</Button>
    </SheetHeader>
    <div className="min-h-0 flex-1 overflow-auto px-5 pb-5">
      {error ? <p role="alert" className="text-xs text-status-danger">{error}</p> : !content ? <p className="text-xs text-muted-foreground">读取中</p> : content.binary ? <p className="text-xs text-muted-foreground">此文件不支持文本查看，可在仓库中下载</p> : <>
        <p className="mb-3 text-xs text-muted-foreground">字节范围 {content.offset}–{content.next_offset}{content.complete ? " · 已到末尾" : ""}</p>
        <pre className="whitespace-pre-wrap break-words num text-xs">{content.text || "文件为空"}</pre>
        {offset > 0 && <Button size="sm" variant="outline" onClick={() => setOffset(0)}>从头阅读</Button>}
        {!content.complete && <Button size="sm" variant="outline" onClick={() => setOffset(content.next_offset)}>下一段</Button>}
      </>}
    </div>
  </SheetContent></Sheet>;
}
