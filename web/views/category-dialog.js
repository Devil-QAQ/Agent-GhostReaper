"use client";

import * as React from "react";
import { toast } from "sonner";

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
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { fetchJSON, postJSON } from "@/lib/api";

// 分类管理(P2-06h):分类增删改名(全局清单,键值表)+ 任务归类(逐任务
// 指派,task.meta 事件入账)。删除分类时,该分类下的任务自动移出。
export function CategoryDialog({ open, onOpenChange, tasks, onChanged }) {
  const [cats, setCats] = React.useState([]);
  const [name, setName] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [renaming, setRenaming] = React.useState(null); // 编辑中的分类 id
  const [renameVal, setRenameVal] = React.useState("");
  const [removing, setRemoving] = React.useState(null); // 待二次确认删除的分类

  const refresh = React.useCallback(async () => {
    const { ok, body } = await fetchJSON("/api/task-categories");
    if (ok && Array.isArray(body?.categories)) setCats(body.categories);
  }, []);

  React.useEffect(() => {
    if (open) refresh();
  }, [open, refresh]);

  const counts = React.useMemo(() => {
    const m = {};
    for (const t of tasks || []) if (t.category) m[t.category] = (m[t.category] || 0) + 1;
    return m;
  }, [tasks]);

  const create = async () => {
    if (!name.trim()) {
      toast.error("分类名称不能为空");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON("/api/task-categories", { name: name.trim() });
    setBusy(false);
    if (!ok) {
      toast.error(`新建失败:${body.error || "原因未说明"}`);
      return;
    }
    setName("");
    await refresh();
    if (onChanged) onChanged();
  };

  const rename = async (id) => {
    if (!renameVal.trim()) {
      toast.error("分类名称不能为空");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON(`/api/task-categories/${encodeURIComponent(id)}`, {
      name: renameVal.trim(),
    });
    setBusy(false);
    if (!ok) {
      toast.error(`改名失败:${body.error || "原因未说明"}`);
      return;
    }
    setRenaming(null);
    await refresh();
  };

  const remove = async (id) => {
    setBusy(true);
    const { ok, body } = await fetchJSON(`/api/task-categories/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
    setBusy(false);
    setRemoving(null);
    if (!ok) {
      toast.error(`删除失败:${body.error || "原因未说明"}`);
      return;
    }
    toast.success(`已删除分类${body.cleared_tasks ? `,${body.cleared_tasks} 个任务移出` : ""}`);
    await refresh();
    if (onChanged) onChanged();
  };

  const assign = async (taskID, category) => {
    const { ok, body } = await postJSON(`/api/tasks/${encodeURIComponent(taskID)}/meta`, {
      category: category === "none" ? "" : category,
    });
    if (!ok) {
      toast.error(`归类失败:${body.error || "原因未说明"}`);
      await refresh();
      return;
    }
    if (onChanged) onChanged();
  };

  const catName = (id) => cats.find((c) => c.id === id)?.name;

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-w-xl">
          <DialogHeader>
            <DialogTitle>分类管理</DialogTitle>
            <DialogDescription>
              分类为任务清单的组织维度;指派与变更入年轮账本。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-2">
            <div className="flex gap-2">
              <Input
                placeholder="新分类名称"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && create()}
              />
              <Button variant="outline" disabled={busy} onClick={create}>
                新建
              </Button>
            </div>
            {cats.length === 0 && (
              <div className="text-xs text-muted-foreground">尚无分类。</div>
            )}
            {cats.map((c) => (
              <div key={c.id} className="flex items-center gap-2 rounded-md border px-3 py-2 transition-colors duration-150 hover:bg-hover-lift">
                {renaming === c.id ? (
                  <>
                    <Input
                      className="h-8"
                      value={renameVal}
                      onChange={(e) => setRenameVal(e.target.value)}
                      onKeyDown={(e) => e.key === "Enter" && rename(c.id)}
                    />
                    <Button size="sm" disabled={busy} onClick={() => rename(c.id)}>
                      保存
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => setRenaming(null)}>
                      取消
                    </Button>
                  </>
                ) : (
                  <>
                    <span className="text-sm">{c.name}</span>
                    <Badge variant="outline">{counts[c.id] || 0} 个任务</Badge>
                    <span className="ml-auto flex gap-1">
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setRenaming(c.id);
                          setRenameVal(c.name);
                        }}
                      >
                        改名
                      </Button>
                      <Button size="sm" variant="outline" onClick={() => setRemoving(c.id)}>
                        删除
                      </Button>
                    </span>
                  </>
                )}
              </div>
            ))}
          </div>

          <div className="grid gap-2">
            <div className="text-sm font-medium">任务归类</div>
            <div className="flex max-h-56 flex-col gap-1 overflow-y-auto pr-1">
              {(tasks || []).map((t) => (
                <div key={t.id} className="flex items-center gap-2 rounded-md px-2 py-2 transition-colors duration-150 hover:bg-hover-lift">
                  <span className="min-w-0 flex-1 truncate text-sm" title={t.title || t.id}>{t.title || t.id}</span>
                  <span className="num text-xs text-muted-foreground">{t.id}</span>
                  <div className="w-40 shrink-0">
                    <Select
                      value={t.category && catName(t.category) ? t.category : "none"}
                      onValueChange={(v) => assign(t.id, v)}
                    >
                      <SelectTrigger className="h-8">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="none">未分类</SelectItem>
                        {cats.map((c) => (
                          <SelectItem key={c.id} value={c.id}>
                            {c.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>
              ))}
              {(!tasks || tasks.length === 0) && (
                <div className="text-xs text-muted-foreground">暂无任务可归类。</div>
              )}
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除分类二次确认 */}
      <Dialog open={Boolean(removing)} onOpenChange={(v) => !v && setRemoving(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>删除分类</DialogTitle>
            <DialogDescription>
              {removing ? `${catName(removing)} 下有 ${counts[removing] || 0} 个任务,删除后这些任务移出该分类(记录可查)。` : ""}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(null)}>
              取消
            </Button>
            <Button variant="destructive" disabled={busy} onClick={() => remove(removing)}>
              确认删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
