"use client";
import * as React from "react";
import { CapabilityContent } from "@/components/capability-content";
import { SearchInput } from "@/components/search-input";
import { Button } from "@/components/ui/button";
import { usePolling } from "@/lib/api";
export function CapabilityQuery({ repositories, onConfigure }) {
  const [input, setInput] = React.useState(""); const [query, setQuery] = React.useState("");
  const [scope, setScope] = React.useState(""); const [page, setPage] = React.useState(1);
  const [selected, setSelected] = React.useState(null);
  const { data, error: queryError } = usePolling(`/api/capability-library/search?${new URLSearchParams({ q: query, source: scope, page: String(page) })}`, 2000);
  React.useEffect(() => { const t = setTimeout(() => { setQuery(input.trim()); setPage(1); }, 250); return () => clearTimeout(t); }, [input]);
  return <section className="flex min-w-0 flex-col gap-3 rounded-xl border p-4" aria-label="资料查询">
    <div className="flex flex-wrap gap-2"><SearchInput aria-label="查询资料" placeholder="搜索文件和正文" value={input} onChange={(e) => setInput(e.target.value)} /><select aria-label="查询范围" className="h-9 rounded-md border bg-background px-3 text-sm" value={scope} onChange={(e) => { setScope(e.target.value); setPage(1); }}><option value="">全部仓库</option>{repositories.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select><Button size="sm" variant="outline" className="ml-auto" onClick={onConfigure}>配置</Button></div>
    <p className="text-xs text-muted-foreground">按路径和原文字面匹配；排名不代表 PoC 适用或有效。</p>
    {data?.coverage?.length > 0 && <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">可查正文 {data?.coverage?.reduce((n, c) => n + c.indexed - c.unsearchable, 0)} 个 · 仅路径 {data?.coverage?.reduce((n, c) => n + c.unsearchable, 0)} 个 · 待处理 {data?.coverage?.reduce((n, c) => n + c.pending, 0)} 个</summary>{data?.coverage?.map((c) => <p key={c.source_id} className="text-xs text-muted-foreground">{c.name} · 可查正文 {c.indexed - c.unsearchable}/{c.files} 个 · 原文待处理 {c.pending} 个 · 仅路径 {c.unsearchable} 个</p>)}</details>}
    {data?.coverage?.filter((c) => c.error)?.map((c) => <p key={c.source_id} role="alert" className="text-xs text-status-danger">{c.name} · {c.error}</p>)}
    {queryError && <p role="alert" className="text-xs text-status-danger">查询失败：{queryError}</p>}
    {query && data?.query === query && data?.source_id === scope && !queryError && <>
      {data.hits?.map((h) => <div key={`${h.source_id}:${h.path}`} className="flex min-w-0 flex-col gap-2 border-b py-3">
        <button className="cursor-pointer break-all text-left text-sm text-primary hover:underline" onClick={() => setSelected(h)}>{h.path}</button>
        <p className="break-all text-xs text-muted-foreground">{h.source} · {h.match}命中</p>
        <p className="whitespace-pre-wrap break-words text-xs text-muted-foreground">{h.snippet || h.reason || "路径匹配"}</p>
        <div className="flex gap-2"><Button size="sm" variant="ghost" onClick={() => setSelected(h)}>原文</Button></div>
      </div>)}
      {!data.total && <p className="text-xs text-muted-foreground">当前可查范围内无命中</p>}
      <div className="flex items-center gap-2 text-xs text-muted-foreground"><span>共 {data.total} 项 · 第 {data.page} 页</span><Button size="sm" variant="outline" disabled={data.page <= 1} onClick={() => setPage(data.page - 1)}>上一页</Button><Button size="sm" variant="outline" disabled={data.page * data.page_size >= data.total} onClick={() => setPage(data.page + 1)}>下一页</Button></div>
    </>}
    <CapabilityContent selected={selected} onClose={() => setSelected(null)} />
  </section>;
}
