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
import { postJSON } from "@/lib/api";

// 存档断点按钮(P3-07 断点续接):任务行控制组,运行中/收尾中可点。
// 存档 = 在跑单位到边界收拢后进程可关;重启后任务自动原地续跑(原会话
// 续)。与冬眠的分工:冬眠保进程(内存冻结、唤醒原地继续),断点关进程
// (重启续跑)。确认对话框如实说明;已存档态不显示本按钮。
export function CheckpointButton({ taskID, taskTitle, onChanged }) {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  const checkpoint = async () => {
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/checkpoint`, {});
    setBusy(false);
    setOpen(false);
    if (!ok) {
      toast.error(`存档失败:${body?.error || "网络错误"}`);
      return;
    }
    if (body?.frozen === false) {
      toast.message(`已存档断点;仍有单位未收拢,可直接关闭进程(重启后自动续跑)`);
    } else {
      toast.success(`已存档断点,可关闭进程;重启后任务自动续跑`);
    }
    if (onChanged) onChanged();
  };

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        title="存档断点:在跑单位到边界收拢,进程可关;重启后任务自动原地续跑"
        onClick={() => setOpen(true)}
      >
        {React.createElement(iconOf("save"), { className: "size-3.5" })}
        存档断点
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>存档断点</DialogTitle>
            <DialogDescription>
              在跑单位到工具调用边界收拢后,进程可关闭;下次启动任务自动原地续跑
              (原会话续)。与冬眠不同:冬眠保持进程运行,断点为关闭进程准备。
            </DialogDescription>
          </DialogHeader>
          <div className="text-sm text-muted-foreground">
            {taskTitle || taskID} · 未完成的工作经会话保留,重启后从断点继续,不从头再来。
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button onClick={checkpoint} disabled={busy}>
              存档断点
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
