"use client";

import * as React from "react";
import { toast } from "sonner";

import { CapabilityContent } from "@/components/capability-content";
import { CapabilityQuery } from "@/components/capability-query";
import { CapabilityConfigDialog } from "@/components/capability-config-dialog";
import { ErrorBoundary } from "@/components/error-boundary";
import { TopTabs } from "@/components/top-tabs";
import { SearchConfigPage } from "@/views/search-config-page";
import { ToolsPlaceholderPage } from "@/views/placeholder-page";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { SearchInput } from "@/components/search-input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fetchJSON, postJSON, usePolling } from "@/lib/api";

const ROOT = "/api/capability-library";
const CAPABILITY_TABS = [
  { id: "poc", title: "POC库管理" },
  { id: "search", title: "搜索配置" },
  { id: "tools", title: "工具管理" },
];
const bytes = (n) => `${Number(n || 0).toLocaleString("zh-CN")} 字节`;
const time = (value) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";

function progressText(status) {
  const p = status?.progress;
  if (!status?.updating || !p || status.stage === "检查更新") return "";
  if (status.stage === "删除文件" || status.stage === "删除仓库") return "正在清理磁盘文件，可切换页面";
  if (status.stage === "下载文件") {
    return `已下载 ${bytes(p.bytes)}${p.total_bytes ? ` / ${bytes(p.total_bytes)}（${Math.min(100, Math.floor(p.bytes / p.total_bytes * 100))}%）` : ""}`;
  }
  return `${status.stage === "上传文件" ? "已接收" : "已处理"} ${p.files}${p.total_files ? `/${p.total_files}` : ""} 个文件 · ${bytes(p.bytes)}`;
}

function RepositoryBadge({ status, kind, starting = false }) {
  if (starting || status?.updating) return <Badge variant="running">{status?.stage || "检查更新"}</Badge>;
  if (status?.cleanup_pending) return <Badge variant="danger">清理未完成</Badge>;
  return <Badge variant={status?.ready ? "success" : "outline"}>{status?.ready ? (kind === "folder" ? "已导入" : "已下载") : kind === "folder" ? "未导入" : "未下载"}</Badge>;
}

export function CapabilityPage() {
  const [route, setRoute] = React.useState({ tab: "poc", sourceID: "" });
  React.useEffect(() => {
    const sync = () => {
      const params = new URLSearchParams(String(location.hash).split("?")[1] || "");
      const tab = params.get("tab");
      setRoute({ tab: CAPABILITY_TABS.some((item) => item.id === tab) ? tab : "poc", sourceID: params.get("repo") || "" });
    };
    sync();
    window.addEventListener("hashchange", sync);
    return () => window.removeEventListener("hashchange", sync);
  }, []);

  function selectTab(tab) {
    const params = new URLSearchParams(String(location.hash).split("?")[1] || "");
    if (tab === "poc") params.delete("tab"); else params.set("tab", tab);
    const query = params.toString();
    location.hash = `/capabilities${query ? `?${query}` : ""}`;
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <TopTabs tabs={CAPABILITY_TABS} value={route.tab} onSelect={selectTab} label="能力库标签" data-capability-navigation />
      <ErrorBoundary key={route.tab}>
        {route.tab === "search" ? <SearchConfigPage /> : route.tab === "tools" ? <ToolsPlaceholderPage /> : <PocLibraryPage sourceID={route.sourceID} />}
      </ErrorBoundary>
    </div>
  );
}

function PocLibraryPage({ sourceID }) {
  const { data, error, setData } = usePolling(ROOT, 2000);
  const [configOpen, setConfigOpen] = React.useState(false);
  const repositories = Array.isArray(data?.repositories) ? data.repositories : [];
  const source = repositories.find((repo) => repo.id === sourceID);
  const back = () => { location.hash = "/capabilities"; };
  async function retryCleanup(id) {
    try {
      const r = await postJSON(`${ROOT}/repositories/${id}/cleanup`);
      if (r.ok) { setData(r.body); toast.success("已开始删除"); }
      else toast.error(r.body.error || "删除未开始");
    } catch { toast.error("网络未连通"); }
  }
  if (sourceID && source) return <RepositoryBrowser key={`${source.id}:${source.url || ""}:${source.branch || ""}`} source={source} onBack={back} onCatalogReply={setData} />;
  return (
    <div className="flex flex-col gap-3">
      <CapabilityQuery repositories={repositories} onConfigure={() => setConfigOpen(true)} />
      {error && <p role="alert" className="text-xs text-status-danger">{error}</p>}
      {sourceID && data && <p role="alert" className="text-xs text-status-danger">仓库已移除</p>}
      {!data && !error ? <p className="text-xs text-muted-foreground">读取中</p> : repositories.length ? (
        <div className="grid auto-rows-fr grid-cols-1 gap-3 lg:grid-cols-2">
          {repositories.map((repo) => (
            <Card key={repo.id} data-card-kind="capability" role="button" tabIndex={repo.deleting ? -1 : 0} aria-disabled={repo.deleting || undefined} aria-label={`打开 ${repo.name}`} className="h-full min-w-0 cursor-pointer transition-shadow duration-150 hover:shadow-card" onClick={() => { if (!repo.deleting) location.hash = `/capabilities?repo=${encodeURIComponent(repo.id)}`; }} onKeyDown={(e) => {
              if (repo.deleting || e.target !== e.currentTarget) return;
              if (e.key === "Enter" || e.key === " ") { e.preventDefault(); location.hash = `/capabilities?repo=${encodeURIComponent(repo.id)}`; }
            }}>
              <CardContent className="flex h-full min-w-0 flex-col gap-3 p-4">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="t-sec flex min-w-0 items-center gap-2 leading-none">{React.createElement(iconOf("braces"), { className: "size-4 shrink-0 text-muted-foreground" })}<span className="break-all">{repo.name}</span></span>
                  <RepositoryBadge status={repo} kind={repo.kind} />
                </div>
                <div className="flex flex-col gap-1">
                  <p className="truncate text-xs text-muted-foreground" title={repo.kind === "folder" ? "本地文件夹" : repo.url}>{repo.kind === "folder" ? "本地文件夹" : repo.url}</p>
                  <p className="text-xs text-muted-foreground">文件 {repo.version?.files || 0} 个</p>
                  {progressText(repo) && <p role="status" className="text-xs text-muted-foreground">{progressText(repo)}</p>}
                  {repo.error && <p className="text-xs text-status-danger">{repo.error}</p>}
                  {repo.cleanup_pending && !repo.updating && <Button size="sm" variant="outline" className="w-fit" onClick={(e) => { e.stopPropagation(); retryCleanup(repo.id); }}>重试删除</Button>}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      ) : !error && <EmptyState icon="braces" title="暂无仓库" action={{ label: "新增仓库", onClick: () => setConfigOpen(true) }} />}
      <CapabilityConfigDialog open={configOpen} onOpenChange={setConfigOpen} repositories={repositories} onReply={setData} />
    </div>
  );
}

function RepositoryBrowser({ source, onBack, onCatalogReply }) {
  const base = `${ROOT}/repositories/${source.id}`;
  const { data: status, error: statusError, setData: setStatus } = usePolling(base, 2000);
  const [epoch, setEpoch] = React.useState(0);
  const [editingFile, setEditingFile] = React.useState(false);
  const [draft, setDraft] = React.useState("");
  const [pendingDelete, setPendingDelete] = React.useState(null);
  const [changesOpen, setChangesOpen] = React.useState(false);
  const [upstream, setUpstream] = React.useState(null);
  const [mutating, setMutating] = React.useState(false);
  const { data: localData, error: localError } = usePolling(`${base}/changes?v=${epoch}`, 2000);
  const [starting, setStarting] = React.useState(false);
  const [folderUpdateOpen, setFolderUpdateOpen] = React.useState(false);
  const [dir, setDir] = React.useState("");
  const [input, setInput] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [listing, setListing] = React.useState(null);
  const [listError, setListError] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [selected, setSelected] = React.useState(() => { const p = new URLSearchParams(String(location.hash).split("?")[1] || ""); return p.get("file") ? { path: p.get("file"), commit: source.version?.commit } : null; });
  const [preview, setPreview] = React.useState(null);
  const [segment, setSegment] = React.useState(null);
  const [previewError, setPreviewError] = React.useState("");
  const version = status?.version;
  const ready = Boolean(status?.ready);
  const updating = starting || Boolean(status?.updating);
  const cleanupPending = Boolean(status?.cleanup_pending);

  React.useEffect(() => {
    const timer = setTimeout(() => { setQuery(input.trim()); setPage(1); }, 250);
    return () => clearTimeout(timer);
  }, [input]);

  React.useEffect(() => {
    if (!ready) return;
    const controller = new AbortController();
    setLoading(true);
    setListError("");
    const params = new URLSearchParams({ dir, q: query, page: String(page) });
    fetchJSON(`${base}/files?${params}`, { signal: controller.signal }).then((r) => {
      if (controller.signal.aborted) return;
      if (r.ok) setListing(r.body);
      else { setListing(null); setListError(r.body.error || "目录读取失败"); }
    }).catch((error) => {
      if (error.name !== "AbortError") { setListing(null); setListError("网络未连通"); }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [base, dir, query, page, ready, version?.commit, status?.local_revision, epoch]);

  React.useEffect(() => {
    setPreview(null);
    setEditingFile(false);setUpstream(null);
    setPreviewError("");
    if (!selected) return;
    const controller = new AbortController();
    const params = new URLSearchParams({ path: selected.path, version: selected.commit });
    fetchJSON(`${base}/file?${params}`, { signal: controller.signal }).then((r) => {
      if (controller.signal.aborted) return;
      if (r.ok) {setPreview(r.body);setDraft(r.body.text || "");}
      else setPreviewError(r.body.error || "文件读取失败");
    }).catch((error) => { if (error.name !== "AbortError") setPreviewError("网络未连通"); });
    return () => controller.abort();
  }, [base, selected]);

  async function update() {
    setStarting(true);
    try {
      const r = await postJSON(`${base}/update`);
      if (!r.ok) { toast.error(r.body.error || "更新未开始"); return; }
      setStatus(r.body.status);
    } catch { toast.error("网络未连通"); }
    finally { setStarting(false); }
  }

  async function retryCleanup() {
    try {
      const r = await postJSON(`${base}/cleanup`);
      if (!r.ok) { toast.error(r.body.error || "删除未开始"); return; }
      onCatalogReply(r.body);
      const next = r.body.repositories?.find((repo) => repo.id === source.id);
      if (next) setStatus(next); else onBack();
    } catch { toast.error("网络未连通"); }
  }

  async function mutate(path, action, revision, commit, text = "") {
    setMutating(true);
    try {
      const r = await postJSON(`${base}/file`, { path, action, revision, version: commit, text });
      if (!r.ok) { toast.error(r.body.error || "操作失败"); return; }
      setStatus(r.body);setEpoch((n) => n + 1);setPendingDelete(null);
      if (action === "delete") setSelected(null);
      else if (selected) setSelected({ ...selected, commit: r.body.version.commit, token: "" });
      toast.success(action === "delete" ? "已开始删除" : "已保存");
    } catch { toast.error("网络未连通"); } finally { setMutating(false); }
  }
  async function readUpstream() {
    const r = await fetchJSON(`${base}/file?${new URLSearchParams({ path: selected.path, version: preview.commit, upstream: "1" })}`);
    setUpstream(r.ok ? r.body.text : (r.body.error || "上游文件已移除"));
  }

  function navigate(next) {
    setDir(next);
    setInput("");
    setQuery("");
    setPage(1);
  }

  const totalPages = Math.max(1, Math.ceil((listing?.total || 0) / (listing?.page_size || 50)));
  const parent = dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : "";
  const downloadURL = selected ? `${base}/file?${new URLSearchParams({ path: selected.path, version: selected.commit, download: "1" })}` : "";

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader title="POC库管理">
        <Button size="sm" variant="ghost" onClick={onBack}>返回</Button>
        <Button size="sm" variant="outline" onClick={() => setChangesOpen(true)}>本地改动</Button>
        {source.kind === "folder" && <Button size="sm" variant="outline" disabled={updating || cleanupPending} onClick={() => setFolderUpdateOpen(true)}>更新内容</Button>}
        {source.kind === "github" && <Button size="sm" variant="outline" disabled={updating || cleanupPending || !status} onClick={update}>
          {React.createElement(iconOf(ready ? "rotateCcw" : "download"), { className: "size-4" })}
          {updating ? "更新中" : ready ? "在线更新" : "下载仓库"}
        </Button>}
      </PageHeader>

      <section className="flex min-w-0 flex-col gap-3 rounded-xl border border-border/55 bg-card p-4">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          {source.kind === "github" ? <a className="min-w-0 break-all text-sm font-medium text-primary hover:underline" href={source.url} target="_blank" rel="noreferrer">{source.name}</a> : <span className="min-w-0 break-all text-sm font-medium">{source.name}</span>}
          <RepositoryBadge status={status} kind={source.kind} starting={starting} />
        </div>
        {version && (
          <div className="flex flex-wrap gap-x-4 gap-y-2 text-xs text-muted-foreground">
            <span>文件 {version.files} 个</span>
            <span>{bytes(version.bytes)}</span>
            <span title={version.commit}>版本号 {version.commit?.slice(0, 12)}</span>
            <span>{source.kind === "folder" ? "导入时间" : "下载时间"} {time(version.updated_at)}</span>
            <span>检查时间 {time(version.checked_at)}</span>
            {source.kind === "github" && <span>{version.added_files == null ? "上次更新新增：未记录" : `上次更新新增 ${version.added_files} 个文件`}</span>}
          </div>
        )}
        {progressText(status) && <p role="status" className="text-xs text-muted-foreground">{progressText(status)}</p>}
        {(statusError || status?.error) && <p role="alert" className="text-xs text-status-danger">{statusError || status.error}</p>}
        {cleanupPending && !updating && <Button size="sm" variant="outline" className="w-fit" onClick={retryCleanup}>重试删除</Button>}
      </section>

      {status?.deleting ? <EmptyState icon="braces" title={updating ? "正在删除仓库" : "删除未完成"} hint={updating ? "清理完成后移除仓库，可切换页面" : "点击“重试删除”继续清理"} /> : !ready ? (
        <EmptyState icon="braces" title={updating ? (source.kind === "folder" ? "正在导入文件夹" : "正在下载仓库") : "暂无本地文件"} hint={updating ? "完成后显示目录，可切换页面" : source.kind === "folder" ? "点击“更新内容”重新导入" : "点击“下载仓库”获取资料"} />
      ) : (
        <>
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Button variant="ghost" size="sm" onClick={() => navigate("")}>全部文件</Button>
            {dir && <Button variant="outline" size="sm" onClick={() => navigate(parent)}>上一级</Button>}
            <span className="min-w-0 flex-1 break-all text-xs text-muted-foreground">{dir || "根目录"}</span>
            <SearchInput aria-label="搜索文件名或路径" placeholder="搜索文件名或路径" value={input} onChange={(e) => setInput(e.target.value)} />
          </div>

          {listError ? <p role="alert" className="text-xs text-status-danger">{listError}</p> : loading ? (
            <p className="py-4 text-xs text-muted-foreground" role="status">读取中</p>
          ) : listing?.entries?.length ? (
            <div className="min-w-0 rounded-xl border border-border/55 bg-card">
              <Table>
                <TableHeader><TableRow><TableHead>名称</TableHead><TableHead className="w-24">类型</TableHead><TableHead className="text-right">大小</TableHead><TableHead>操作</TableHead></TableRow></TableHeader>
                <TableBody>
                  {listing?.entries?.map((entry) => (
                    <TableRow key={entry.path}>
                      <TableCell className="max-w-0 w-full">
                        <button className="w-full cursor-pointer truncate text-left text-sm hover:text-primary" title={entry.path} onClick={() => entry.directory ? navigate(entry.path) : setSelected({ path: entry.path, commit: listing.commit, token: entry.fingerprint || `${listing.commit}:${entry.path}` })}>
                          {query ? entry.path : entry.name}
                        </button>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{entry.directory ? "目录" : "文件"}</TableCell>
                      <TableCell className="text-right text-xs text-muted-foreground num">{entry.directory ? "—" : bytes(entry.size)}</TableCell>
                    <TableCell><Button size="sm" variant="ghost" disabled={updating || cleanupPending || mutating} aria-label={`删除 ${entry.name}`} onClick={() => setPendingDelete({ ...entry, revision: listing.revision, commit: listing.commit })}>删除</Button></TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          ) : <EmptyState icon="search" title={query ? "没有匹配文件" : "目录为空"} />}

          {listing && !loading && !listError && (
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
              <span>共 {listing.total} 项 · 第 {listing.page}/{totalPages} 页</span>
              <div className="flex gap-2">
                <Button size="sm" variant="outline" disabled={listing.page <= 1} onClick={() => setPage(listing.page - 1)}>上一页</Button>
                <Button size="sm" variant="outline" disabled={listing.page >= totalPages} onClick={() => setPage(listing.page + 1)}>下一页</Button>
              </div>
            </div>
          )}
        </>
      )}

      <Sheet open={Boolean(selected)} onOpenChange={(open) => { if (!open) setSelected(null); }}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>文件内容</SheetTitle>
            <SheetDescription className="break-all">{selected?.path}</SheetDescription>
            {selected && preview && !preview.binary && !preview.truncated && <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="outline" disabled={mutating || updating || cleanupPending} onClick={() => setEditingFile(!editingFile)}>{editingFile ? "取消编辑" : "编辑"}</Button>
              {editingFile && <Button size="sm" disabled={mutating} onClick={() => mutate(selected.path, "save", preview.revision, preview.commit, draft)}>保存</Button>}
            </div>}
            {selected && <Button size="sm" variant="outline" onClick={() => setSegment({ ...selected, source_id: source.id, source: source.name })}>分段阅读</Button>}
            {selected && <Button size="sm" variant="outline" className="w-fit" asChild><a href={downloadURL}>下载文件</a></Button>}
          </SheetHeader>
          <div className="min-h-0 flex-1 overflow-auto px-5 pb-5">
            {previewError ? <p role="alert" className="text-xs text-status-danger">{previewError}</p> : !preview ? <p className="text-xs text-muted-foreground">读取中</p> : preview.binary ? (
              <p className="text-xs text-muted-foreground">此文件不支持文本预览，可下载文件</p>
            ) : (
              <>
                {preview.truncated && <p className="mb-3 text-xs text-muted-foreground">仅显示前 256 KiB，完整内容可下载文件</p>}
                {preview.conflict && <div className="mb-3 flex flex-col gap-2 rounded-lg border p-3">
                  <p className="text-xs text-status-warning">上游内容已变化，本地修改已保留</p>
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" onClick={readUpstream}>查看上游</Button>
                    <Button size="sm" disabled={mutating} onClick={() => mutate(selected.path, "keep", preview.revision, preview.commit)}>保留本地</Button>
                    <Button size="sm" variant="outline" disabled={mutating} onClick={() => mutate(selected.path, "restore", preview.revision, preview.commit)}>采用上游</Button>
                  </div>
                  {upstream !== null && <pre className="whitespace-pre-wrap break-words num text-xs">{upstream}</pre>}
                </div>}
                {editingFile ? <Textarea aria-label="文件编辑内容" className="min-h-64 num text-xs" value={draft} onChange={(e) => setDraft(e.target.value)} /> : <pre className="whitespace-pre-wrap break-words num text-xs">{preview.text || "文件为空"}</pre>}
              </>
            )}
          </div>
        </SheetContent>
      </Sheet>
      <CapabilityContent selected={segment} onClose={() => setSegment(null)} />
      <Dialog open={Boolean(pendingDelete)} onOpenChange={(open) => { if (!open && !mutating) setPendingDelete(null); }}>
        <DialogContent><DialogHeader><DialogTitle>删除内容</DialogTitle><DialogDescription>删除库内文件及历史副本，无法恢复，更新不会补回。</DialogDescription></DialogHeader>
          <p className="break-all text-sm">{pendingDelete?.path}</p>
          <DialogFooter><Button variant="outline" disabled={mutating} onClick={() => setPendingDelete(null)}>取消</Button><Button variant="destructive" disabled={mutating} onClick={() => mutate(pendingDelete.path, "delete", pendingDelete.revision, pendingDelete.commit)}>确认删除</Button></DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={changesOpen} onOpenChange={setChangesOpen}>
        <DialogContent><DialogHeader><DialogTitle>本地改动</DialogTitle><DialogDescription>编辑可撤销，删除项在更新时跳过</DialogDescription></DialogHeader>
          {localError && <p role="alert" className="text-xs text-status-danger">{localError}</p>}
          {(localData?.changes || []).length ? localData.changes?.map((change) => <div key={change.path} className="flex items-center gap-2 border-b py-2">
            <div className="min-w-0 flex-1"><p className="break-all text-xs">{change.path}</p><p className="text-xs text-muted-foreground">{change.deleted ? `已删除 · ${time(change.deleted_at)}` : change.conflict ? "上游有变化" : "已编辑"}</p></div>
            {!change.deleted && <Button size="sm" variant="outline" disabled={mutating || updating || cleanupPending} onClick={() => mutate(change.path, "restore", localData.revision, localData.version)}>撤销编辑</Button>}
          </div>) : <p className="text-xs text-muted-foreground">暂无本地改动</p>}
        </DialogContent>
      </Dialog>
      <CapabilityConfigDialog open={folderUpdateOpen} onOpenChange={setFolderUpdateOpen} repositories={[source]} initialSource={source} updateOnly onReply={(data) => {
        onCatalogReply(data);
        const next = data.repositories?.find((repo) => repo.id === source.id);
        if (next) setStatus(next);
      }} />
    </div>
  );
}

