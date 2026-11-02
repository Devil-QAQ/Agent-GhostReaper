"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
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
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ASSET_CLASSES, postJSON } from "@/lib/api";
import { recognizeLines } from "@/views/new-task-dialog";

// 新增资产弹窗(P4-05,问题清单 #10):表单同建任务种子表单——12 类
// 选型 + 标识 + 父标识(指向图上既有资产或本批内另一行),批量粘贴
// 识别复用同款能力。入账后默认纳入授权范围;来源标记"手动"。
const emptyRow = () => ({ cls: "domain", title: "", parent: "" });

export function AssetAddDialog({ open, onOpenChange, taskID, assets, onAdded }) {
  const [rows, setRows] = React.useState([emptyRow()]);
  const [paste, setPaste] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setRows([emptyRow()]);
    setPaste("");
  }, [open]);

  const setRow = (i, patch) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  const removeRow = (i) =>
    setRows((rs) => (rs.length <= 1 ? [emptyRow()] : rs.filter((_, j) => j !== i)));

  const recognize = () => {
    const found = recognizeLines(paste);
    if (found.length === 0) {
      toast.error("未识别出任何资产行(每行一个标识)");
      return;
    }
    setRows((rs) => [...rs.filter((r) => r.title.trim() !== ""), ...found]);
    setPaste("");
    toast.success(`已识别 ${found.length} 行,可在行内修改`);
  };

  const submit = async () => {
    const list = rows
      .map((r) => ({ class: r.cls, title: r.title.trim(), parent: r.parent.trim() }))
      .filter((r) => r.title !== "");
    if (list.length === 0) {
      toast.error("至少填一行资产标识");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/assets`, { assets: list });
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success(`已记录 ${body.added} 个资产,已唤醒主宰`);
    if (onAdded) onAdded();
  };

  // 父标识候选:图上既有资产标识 + 本批已填标识(datalist 自动补全)。
  const parentOptions = React.useMemo(() => {
    const ids = new Set(assets.map((a) => a.title));
    for (const r of rows) {
      if (r.title.trim()) ids.add(r.title.trim());
    }
    return [...ids];
  }, [assets, rows]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>新增资产</DialogTitle>
          <DialogDescription>
            12 类选型入年轮账本,来源标记手动;默认纳入授权范围,不想测的在 RoE 摘除。父标识指向图上资产或本批另一行,构成父子层级。
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-2">
          <Label htmlFor="aa-paste">批量粘贴识别(可选)</Label>
          <Textarea
            id="aa-paste"
            rows={3}
            placeholder={"每行一个标识,自动识别:\nhttp://10.10.0.9:8080 → 服务\n10.10.0.10 → 主机\napi.example.com → 子域名"}
            className="num text-xs"
            value={paste}
            onChange={(e) => setPaste(e.target.value)}
          />
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={recognize}>
              识别
            </Button>
            <span className="text-xs text-muted-foreground">
              URL/带端口 IP → 服务 · 纯 IP → 主机 · 点分域名 → 子域名 · 其余 → 其他
            </span>
          </div>
        </div>

        <div className="grid gap-2">
          <Label>资产</Label>
          <div className="flex max-h-64 flex-col gap-2 overflow-y-auto pr-1">
            {rows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <div className="w-32 shrink-0">
                  <Select value={row.cls} onValueChange={(v) => setRow(i, { cls: v })}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ASSET_CLASSES.map(([v, label]) => (
                        <SelectItem key={v} value={v}>
                          {label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <Input
                  className="num text-xs"
                  placeholder="资产标识(如 http://10.10.0.9:8080)"
                  value={row.title}
                  onChange={(e) => setRow(i, { title: e.target.value })}
                />
                <Input
                  list="aa-parent-ids"
                  className="w-40 shrink-0 num text-xs"
                  placeholder="父标识(可选)"
                  value={row.parent}
                  onChange={(e) => setRow(i, { parent: e.target.value })}
                />
                <datalist id="aa-parent-ids">
                  {parentOptions.map((t) => (
                    <option key={t} value={t} />
                  ))}
                </datalist>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  title="删除此行"
                  onClick={() => removeRow(i)}
                >
                  {React.createElement(iconOf("x"), { className: "size-4" })}
                </Button>
              </div>
            ))}
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => setRows((rs) => [...rs, emptyRow()])}
          >
            添加资产
          </Button>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy}>
            记录并唤醒主宰
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
