"use client";

import * as React from "react";
import { toast } from "sonner";
import { Field } from "@/components/form-bits";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { fetchJSON, postJSON } from "@/lib/api";
import { startFolderImport } from "@/lib/capability-import";

const ROOT = "/api/capability-library";
const blank = () => ({ name: "", kind: "github", url: "", branch: "" });

export function CapabilityConfigDialog({ open, onOpenChange, repositories, onReply, initialSource = null, updateOnly = false }) {
  const [editing, setEditing] = React.useState(undefined);
  const [form, setForm] = React.useState(blank);
  const [files, setFiles] = React.useState([]);
  const [deleting, setDeleting] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  React.useEffect(() => { if (open) { edit(initialSource); if (!initialSource) setEditing(undefined); } }, [open]);

  function edit(source) {
    setEditing(source || null);
    setForm(source ? { name: source.name, kind: source.kind, url: source.url || "", branch: source.branch || "" } : blank());
    setFiles([]);
    setDeleting("");
  }

  async function save(event) {
    event.preventDefault();
    if (!form.name?.trim()) { toast.error("填写仓库名称"); return; }
    if (form.kind === "github" && !form.url?.trim()) { toast.error("填写仓库链接"); return; }
    if (form.kind === "folder" && (!editing || updateOnly) && !files.length) { toast.error("选择要导入的文件夹"); return; }
    if (form.kind === "folder" && files.length) {
      try {
        startFolderImport({ id: editing?.id, name: form.name, files });
        setFiles([]);
        onOpenChange(false);
      } catch (error) { toast.error(error.message || "导入未开始"); }
      return;
    }
    setBusy(true);
    try {
      const r = await postJSON(`${ROOT}/repositories${editing ? `/${editing.id}` : ""}`, form);
      if (!r.ok) { toast.error(r.body.error || "保存失败"); return; }
      onReply(r.body);
      setEditing(undefined);
      setFiles([]);
      toast.success("已保存");
      if (updateOnly) onOpenChange(false);
    } catch { toast.error("网络未连通"); }
    finally { setBusy(false); }
  }

  async function remove(id) {
    setBusy(true);
    try {
      const r = await fetchJSON(`${ROOT}/repositories/${id}`, { method: "DELETE" });
      if (!r.ok) { toast.error(r.body.error || "删除失败"); return; }
      onReply(r.body);
      setDeleting("");
      toast.success("已开始删除");
    } catch { toast.error("网络未连通"); }
    finally { setBusy(false); }
  }

  return (
    <Dialog open={open} onOpenChange={(value) => { if (!busy) onOpenChange(value); }}>
      <DialogContent hideClose={busy}>
        <DialogHeader>
          <DialogTitle>{updateOnly ? "更新文件夹" : editing === undefined ? "仓库配置" : editing ? "编辑仓库" : "新增仓库"}</DialogTitle>
          <DialogDescription>{updateOnly ? "重新选择文件夹，校验后更新内容" : editing === undefined ? "GitHub 链接与文件夹来源" : form.kind === "folder" ? "保留原目录与文件内容" : "分支留空时跟随仓库默认分支"}</DialogDescription>
        </DialogHeader>
        {editing === undefined ? (
          <>
            <div className="flex flex-col gap-3">
              {repositories.length ? repositories.map((repo) => (
                <div key={repo.id} className="flex flex-col gap-2 rounded-lg border border-border/55 p-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{repo.name}</p>
                      <p className="truncate text-xs text-muted-foreground" title={repo.url}>{repo.kind === "folder" ? "文件夹导入" : repo.url}</p>
                    </div>
                    <Button size="sm" variant="ghost" disabled={busy || repo.updating || repo.cleanup_pending} aria-label={`编辑 ${repo.name}`} onClick={() => edit(repo)}>编辑</Button>
                    <Button size="sm" variant="ghost" disabled={busy || repo.updating} aria-label={`删除 ${repo.name}`} onClick={() => setDeleting(repo.id)}>删除</Button>
                  </div>
                  {deleting === repo.id && (
                    <div className="flex flex-wrap items-center gap-2 border-t pt-2">
                      <p className="flex-1 text-xs text-muted-foreground">删除仓库及全部库内文件，无法恢复。</p>
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDeleting("")}>取消</Button>
                      <Button size="sm" variant="destructive" disabled={busy} onClick={() => remove(repo.id)}>确认删除</Button>
                    </div>
                  )}
                </div>
              )) : <p className="text-xs text-muted-foreground">暂无仓库配置</p>}
            </div>
            <DialogFooter><Button size="sm" onClick={() => edit(null)}>新增仓库</Button></DialogFooter>
          </>
        ) : (
          <>
            <div className="flex flex-col gap-4">
              <Field label="来源">
                {editing ? <span className="text-sm">{form.kind === "folder" ? "文件夹" : "GitHub 链接"}</span> : (
                  <div className="flex gap-2" role="group" aria-label="来源类型">
                    <Button type="button" size="sm" variant={form.kind === "github" ? "secondary" : "outline"} onClick={() => setForm({ ...form, kind: "github" })}>GitHub 链接</Button>
                    <Button type="button" size="sm" variant={form.kind === "folder" ? "secondary" : "outline"} onClick={() => setForm({ ...form, kind: "folder" })}>文件夹</Button>
                  </div>
                )}
              </Field>
              <Field label="名称"><Input aria-label="仓库名称" maxLength={80} required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} disabled={busy} /></Field>
              {form.kind === "github" ? (
                <>
                  <Field label="链接"><Input aria-label="仓库链接" type="url" placeholder="https://github.com/owner/repository" required value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} disabled={busy} /></Field>
                  <Field label="分支"><Input aria-label="仓库分支" placeholder="默认分支" value={form.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} disabled={busy} /></Field>
                </>
              ) : (
                <Field label="文件夹">
                  <Input aria-label="导入文件夹" type="file" webkitdirectory="" directory="" multiple disabled={busy} onClick={(e) => { e.currentTarget.value = ""; }} onChange={(e) => {
                    const selected = Array.from(e.target.files || []);
                    setFiles(selected);
                    if (selected.length && !form.name?.trim()) setForm({ ...form, name: (selected[0].webkitRelativePath || "").split("/")[0] });
                  }} />
                  <span className="text-xs text-muted-foreground">{files.length ? `已选 ${files.length} 个文件` : updateOnly ? "选择更新后的文件夹" : editing ? "不选择文件夹时仅修改名称" : "选择本机文件夹"}</span>
                </Field>
              )}
            </div>
            <DialogFooter>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => updateOnly ? onOpenChange(false) : setEditing(undefined)}>{updateOnly ? "取消" : "返回"}</Button>
              <Button size="sm" disabled={busy} onClick={save}>{busy ? (form.kind === "folder" && files.length ? "导入中" : "保存中") : "保存"}</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
