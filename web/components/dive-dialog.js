"use client";

import * as React from "react";
import { toast } from "sonner";

import { LongText } from "@/components/long-text";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { iconOf } from "@/components/icons";
import { postJSON } from "@/lib/api";

// 深入指令窗(P4-48,问题清单 #67):从成果/事实节点出发"推一把"的
// 节点锚定指令。形态复用 P4-14 追问窗(居中弹窗 + 锚定条:徽标 + 名称
// + "深入");方向输入可选(留空 = 主宰自行判断)。确定即记账并立即
// 唤醒主宰——立即规划是硬承诺,方向采纳由主宰裁决;同一节点可多次
// 深入,各自可溯。
//
// node = { id, title, badge } — badge 为锚定条徽标元数据(状态/严重级,
// 可缺省);disabled 非空时按钮不可用(终态任务,悬停文案由按钮侧给)。

export function DiveDialog({ tid, node, open, onClose }) {
  const [direction, setDirection] = React.useState("");
  const [sending, setSending] = React.useState(false);
  React.useEffect(() => {
    if (open) setDirection("");
  }, [open, node?.id]);

  const submit = async () => {
    if (!tid || !node?.id) return;
    setSending(true);
    const { ok, status, body } = await postJSON(`/api/tasks/${tid}/dive`, {
      node_id: node.id,
      direction: direction.trim(),
    });
    setSending(false);
    if (!ok) {
      toast.error(`下达失败:${body?.error || `HTTP ${status}`}`);
      return;
    }
    toast.success("深入指令已下达,主宰立即规划");
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="w-[min(560px,calc(100vw-32px))] max-w-[560px]">
        <DialogHeader className="space-y-0 pb-0 text-left">
          <DialogTitle className="flex min-w-0 flex-wrap items-center gap-2">
            {React.createElement(iconOf("zap"), { className: "size-4 shrink-0 text-primary" })}
            {node?.badge && (
              <Badge variant={node.badge.variant} className="shrink-0">{node.badge.label}</Badge>
            )}
            <span className="shrink-0">深入</span>
            <LongText
              text={node?.title || node?.id || "…"}
              max={44}
              variant="dialog"
              label="节点标题"
              className="min-w-0 flex-1 font-normal"
            />
          </DialogTitle>
          <DialogDescription>
            从该节点出发继续深入 · 确定即唤醒主宰规划 · 方向由主宰判定
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Textarea
              value={direction}
              placeholder="如：继续暴力破解／拿出 web 账号密码"
              className="min-h-[72px] text-[13px] resize-none"
              onChange={(e) => setDirection(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">留空由主宰自行判断方向</span>
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button onClick={submit} disabled={sending || !tid || !node?.id}>
              确定
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
