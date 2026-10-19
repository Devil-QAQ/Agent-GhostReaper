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
import { fetchJSON, postJSON } from "@/lib/api";

// 冬眠/唤醒按钮(P2-08,第七章「任务冬眠」):任务行控制组。冬眠需
// 确认——对话框打开时拉取待裁决笔数如实提示(冻结为只读,唤醒后恢复
// 可裁决);唤醒无确认(唯一恢复入口,可再次冬眠)。
export function HibernateButton({ taskID, taskTitle, hibernated, onChanged }) {
  const [open, setOpen] = React.useState(false);
  const [pending, setPending] = React.useState(null); // 待裁决笔数(对话框开时拉取)
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) {
      setPending(null);
      return;
    }
    let alive = true;
    fetchJSON(`/api/tasks/${taskID}/approvals`)
      .then((d) => {
        if (alive) setPending((d?.pending || []).length);
      })
      .catch(() => alive && setPending(null));
    return () => {
      alive = false;
    };
  }, [open, taskID]);

  const hibernate = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/hibernate`, {});
    setBusy(false);
    setOpen(false);
    if (!ok) {
      toast.error(`冬眠失败:${body?.error || "网络错误"}`);
      return;
    }
    toast.success(
      `任务 ${taskTitle || taskID} 已冬眠${
        body?.pending_approvals ? `;待裁决审批 ${body.pending_approvals} 笔已冻结` : ""
      }`
    );
    if (onChanged) onChanged();
  };

  const wake = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/hibernate-wake`, {});
    setBusy(false);
    if (!ok) {
      toast.error(`唤醒失败:${body?.error || "网络错误"}`);
      return;
    }
    toast.success(`任务 ${taskTitle || taskID} 已唤醒`);
    if (onChanged) onChanged();
  };

  if (hibernated) {
    return (
      <Button
        variant="outline"
        size="sm"
        disabled={busy}
        title="唯一恢复入口:同一单位同一会话原地继续"
        onClick={wake}
      >
        {React.createElement(iconOf("sun"), { className: "size-3.5" })}
        唤醒
      </Button>
    );
  }
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        title="冬眠:全部暂停,计时停走;唤醒后原地继续"
        onClick={() => setOpen(true)}
      >
        {React.createElement(iconOf("moon"), { className: "size-3.5" })}
        冬眠
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>冬眠任务</DialogTitle>
            <DialogDescription>
              冬眠:全部暂停,计时停走;唤醒后同一单位同一会话原地继续。
              冬眠可唤醒,熔断不可逆。
            </DialogDescription>
          </DialogHeader>
          <div className="text-sm text-muted-foreground">
            {pending === null
              ? "正在读取待裁决审批…"
              : pending > 0
                ? `当前待裁决审批 ${pending} 笔:冬眠期间冻结为只读,唤醒后恢复可裁决。`
                : "当前无待裁决审批。"}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button onClick={hibernate} disabled={busy || pending === null}>
              确认冬眠
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
