"use client";

import * as React from "react";
import { toast } from "sonner";

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

// 收口提醒弹窗(P4-18,问题清单 #30;P4-23 可直接关闭):
// 任务连续 N 轮研判零新发现且距上次新发现 ≥ 间隔(双条件,面板可配)
// 时弹出——数据来自任务列表的 closure 字段(实时推送驱动);两键即
// 裁决:结束 = 优雅收尾(在跑单位收尾后定终态、生成终版报告);
// 继续 = 任务不停,按同样条件重新计数。
// 直接关闭(未点继续/结束)= 搁置(P4-23):待决保留、弹窗不再自动弹,
// 任务列表该任务标红点;点红点经 target 重开本弹窗,处理才消。
// 提醒本身不打断任务(照常跑);多任务同时待决时逐个处理。
export function ClosureDialog({ tasks, target, onTargetDone }) {
  const list = Array.isArray(tasks) ? tasks : [];
  const [busy, setBusy] = React.useState(false);
  // 已裁决/已搁置的提醒键(任务:提醒时刻):POST 成功到轮询数据刷新
  // 之间的窗口内不再呈现同一条,防重复点击与闪回。
  const decidedRef = React.useRef("");

  // 目标态(任务行红点点开)优先,搁置中的提醒也呈——显式重开不看
  // 防闪回键(那把键拦的是裁决落账前的自动重弹,拦不住操作者的明确
  // 意图);自动态只弹未被搁置的提醒(搁置 = 操作者明示暂不处理,不
  // 反复打扰),裁决落账到数据刷新的窗口由防闪回键盖住。
  const reopened = list.find((t) => t.id === target && t.closure && t.status === "started");
  const pending =
    reopened ||
    list.find(
      (t) =>
        t.closure &&
        !t.closure.parked &&
        t.status === "started" &&
        `${t.id}:${t.closure.prompted_at}` !== decidedRef.current
    );

  const decide = async (action) => {
    if (!pending || busy) return;
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${pending.id}/closure`, { action });
    setBusy(false);
    if (!ok) {
      toast.error(action === "end" ? `结束失败:${body.error || "原因未说明"}` : `继续失败:${body.error || "原因未说明"}`);
      return;
    }
    decidedRef.current = `${pending.id}:${pending.closure.prompted_at}`;
    if (target) onTargetDone?.();
    if (action === "end") {
      toast.success("已结束:任务进入收尾", {
        description: "在跑单位收尾后结束,报告页出终版报告",
      });
    } else {
      toast.success("已继续:任务照常运行,按同样条件重新计数");
    }
  };

  // 直接关闭(关闭键/ESC/点击遮罩,未点继续/结束)= 搁置:先收弹窗再
  // 落账,落账失败撤回搁置键让弹窗重现(信息不无声丢失)。裁决在途
  //(busy)不搁置,与按钮禁用同口径——避免与裁决竞态误报关闭失败。
  const dismiss = () => {
    if (!pending || busy) return;
    const key = `${pending.id}:${pending.closure.prompted_at}`;
    const prev = decidedRef.current;
    decidedRef.current = key;
    if (target) onTargetDone?.();
    postJSON(`/api/tasks/${pending.id}/closure`, { action: "dismiss" }).then(({ ok, body }) => {
      if (!ok) {
        if (decidedRef.current === key) decidedRef.current = prev;
        toast.error(`关闭失败:${body.error || "原因未说明"}`);
      }
    });
  };

  if (!pending) return null;
  const c = pending.closure;
  const lead =
    c.anchor_kind === "finding"
      ? `连续 ${c.rounds} 轮规划零新发现,距上次新发现 ${c.minutes} 分钟`
      : c.anchor_kind === "continue"
        ? `连续 ${c.rounds} 轮规划零新发现,距上次选择继续 ${c.minutes} 分钟`
        : `开跑 ${c.minutes} 分钟,${c.rounds} 轮规划零新发现`;
  return (
    <Dialog open onOpenChange={(o) => !o && dismiss()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>收口提醒</DialogTitle>
          <DialogDescription>
            {`「${pending.title || pending.id}」${lead}。任务照常运行,未中断。`}
          </DialogDescription>
        </DialogHeader>
        <div className="text-xs text-muted-foreground">
          结束:停止派发,在跑单位收尾后结束、生成终版报告。继续:按同样条件重新计数。关闭:暂不处理,任务列表该任务标红点。
        </div>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => decide("continue")}>
            继续
          </Button>
          <Button disabled={busy} onClick={() => decide("end")}>
            结束
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
