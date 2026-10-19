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
  const [reason, setReason] = React.useState("面板熔断");
  const [busy, setBusy] = React.useState(false);

  const fuse = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/stop`, {
      reason: reason.trim() || "面板熔断",
    });
    setBusy(false);
    if (!ok) {
      toast.error(`熔断失败:${body.error || "原因未说明"}`);
      return;
    }
    setOpen(false);
    toast.success(`任务 ${taskTitle || taskID} 已熔断(账本可审计)`);
    if (onFused) onFused();
  };

  return (
    <>
      <Button
        variant="destructive"
        size="sm"
        title="熔断任务,不可恢复;原因入账本"
        onClick={() => setOpen(true)}
      >
        熔断
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>熔断任务</DialogTitle>
            <DialogDescription>
              熔断即终止任务,不可恢复;谁 · 何时 · 为何全部入账本。
              运行中的虫群单位一并熔断。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="fuse-reason">熔断理由(入账本)</Label>
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
              确认熔断
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
