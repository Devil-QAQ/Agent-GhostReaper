"use client";

import * as React from "react";

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
import { postJSON } from "@/lib/api";
import { toast } from "sonner";

// 熔断按钮(8.5):任务行内红色 + 二次确认;触发后先记控制事件再取消
// 任务 ctx(次序由后端 Manager 保证),账本可审计"谁·何时·哪个任务"。
export function FuseButton({ taskID, taskTitle, onFused }) {
  const [open, setOpen] = React.useState(false);
  const [reason, setReason] = React.useState("面板终止");
  const [busy, setBusy] = React.useState(false);

  const fuse = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/stop`, {
      reason: reason.trim() || "面板终止",
    });
    setBusy(false);
    if (!ok) {
      toast.error(`终止失败:${body.error || "原因未说明"}`);
      return;
    }
    setOpen(false);
    toast.success(`任务 ${taskTitle || taskID} 已终止(账本可审计)`);
    if (onFused) onFused();
  };

  return (
    <>
      <Button
        variant="destructive"
        size="sm"
        title="终止任务,不可恢复;原因记录在账本"
        onClick={() => setOpen(true)}
      >
        终止
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>终止任务</DialogTitle>
            <DialogDescription>
              终止后任务不可恢复;谁 · 何时 · 为何全部记录在账本。
              运行中的虫群单位一并终止。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="fuse-reason">终止理由(记录在账本)</Label>
            <Input
              id="fuse-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button variant="destructive" onClick={fuse} disabled={busy}>
              确认终止
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
