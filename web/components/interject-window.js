"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
import { ChatStream, tailGrowSig } from "@/components/chat-blocks";
import { SessionInfoBar } from "@/components/session-info-bar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { postJSON, usePolling } from "@/lib/api";
import { cn } from "@/lib/utils";

// 插话子会话窗(P2-08,第四章 §7 v1.8:在 agent 会话旁新开 /btw 式
// 子会话,独立留存不污染主轨迹)。问(不改执行,即时答;单位已结束
// 基于历史回答)与改(对虫群=舵杆注入带回执;对主宰=建议进正式规划,
// 裁决后改)共用一窗;顶部信息条组件复用(独立计);单位目标窗头挂
// 单位控制三按钮(冬眠/唤醒/熔断,熔断需确认)。
//
// target 形状:{ kind: "overmind" } 或
// { kind: "unit", unitID, stepID, sessionID, stepTitle, running, paused }

const DELIVERY = {
  queued: { label: "已排队", variant: "paused" },
  delivered: { label: "已送达", variant: "running" },
  replied: { label: "已回复", variant: "success" },
  failed: { label: "失败", variant: "danger" },
  issued: { label: "已下达", variant: "info" },
};

// 舵杆回执状态机(已排队 → 已送达 → 已采纳/被拒;生效前落地 → 已过时)。
const STEER_STATES = {
  queued: { label: "已排队", variant: "paused", hint: "等待单位下一次工具调用前注入" },
  delivered: { label: "已送达", variant: "running", hint: "已在调用边界注入单位上下文,等它表态" },
  adopted: { label: "已采纳", variant: "success", hint: "单位明示采纳,已按指令调整方向" },
  rejected: { label: "被拒", variant: "danger", hint: "单位明示不采纳(理由随回执)" },
  stale: { label: "已过时", variant: "info", hint: "生效前单位已干完,指令失去生效点" },
};

const ADVICE_STATES = {
  pending: { label: "待判定", variant: "paused" },
  adopted: { label: "已采纳", variant: "success" },
  rejected: { label: "未采纳", variant: "danger" },
};

export function InterjectWindow({ open, target, task, onClose }) {
  const taskID = task?.id;
  const isUnit = target?.kind === "unit";
  const query = isUnit
    ? `target=unit&session=${encodeURIComponent(target.sessionID)}`
    : "target=overmind";
  const url = open && taskID ? `/api/tasks/${taskID}/interject?${query}` : null;
  const { data, error, setData } = usePolling(url || "/api/healthz", 2000, {
    immediate: Boolean(url),
  });

  const [text, setText] = React.useState("");
  const [mode, setMode] = React.useState("ask");
  const [sending, setSending] = React.useState(false);
  const [confirmStop, setConfirmStop] = React.useState(false);

  const messages = data?.messages || [];
  const steers = data?.steers || [];
  const frozen = Boolean(data?.frozen);
  // 流内容签名(P4-22):对话条数 + 末条回答量,贴底跟滚判据。
  const sig = `${messages.length}|${tailGrowSig(messages[messages.length - 1])}`;

  const steerStateOf = (id) =>
    (steers.find((s) => s.steer_id === id) || {}).state || "queued";

  const send = async () => {
    const body = text.trim();
    if (!body || !taskID || !target) return;
    setSending(true);
    const payload = { target: target.kind, mode, text: body };
    if (isUnit) {
      payload.unit_id = target.unitID;
      payload.step_id = target.stepID;
      payload.session_id = target.sessionID;
    }
    const { ok, body: resp } = await postJSON(
      `/api/tasks/${taskID}/interject/messages`,
      payload
    );
    setSending(false);
    if (!ok) {
      toast.error(resp?.error || "发送失败");
      return;
    }
    setText("");
    // 改向即时呈现(回执随后流转);问经轮询呈现。
    if (mode === "steer" && data) {
      setData({
        ...data,
        messages: [
          ...data.messages,
          {
            id: resp.message_id,
            mode: "steer",
            text: body,
            state: "issued",
            ref_id: resp.ref_id,
            ref_kind: isUnit ? "steer" : "advice",
            advice_state: isUnit ? undefined : "pending",
            at: new Date().toLocaleTimeString("sv-SE").slice(0, 8),
          },
        ],
      });
    }
  };

  const unitControl = async (action) => {
    const { ok, body } = await postJSON(
      `/api/tasks/${taskID}/units/${target.stepID}/${action}`,
      {}
    );
    if (!ok) {
      toast.error(body?.error || "操作失败");
      return;
    }
    toast.success(action === "pause" ? "已冬眠" : "已唤醒");
  };

  const stopUnit = async () => {
    const { ok, body } = await postJSON(
      `/api/tasks/${taskID}/kill-work`,
      { step_id: target.stepID, reason: "插话窗终止:操作者结束该单位" }
    );
    setConfirmStop(false);
    if (!ok) {
      toast.error(body?.error || "终止失败");
      return;
    }
    toast.success("单位已终止");
    onClose();
  };

  const title = isUnit
    ? `插话 · 单位 ${target.unitID || ""}`
    : "插话 · 主宰";
  const desc = isUnit
    ? `步骤 ${target.stepID}${target.stepTitle ? " · " + target.stepTitle : ""}`
    : "问(即时答,不改执行)· 改(建议进正式规划,判定后改)";

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent className="flex flex-col gap-4 p-5">
        <SheetHeader className="p-0 pr-12">
          <SheetTitle className="flex items-center gap-2">
            {React.createElement(iconOf("megaphone"), { className: "size-4 text-primary" })}
            {title}
            {isUnit && target.running && (
              <Badge variant={target.paused ? "paused" : "running"}>
                {target.paused ? "冬眠中" : "执行中"}
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription className="truncate">{desc}</SheetDescription>
        </SheetHeader>
        <SessionInfoBar
          info={data?.info}
          startedAt={data?.started_at}
          deductSeconds={data?.hibernate_seconds}
          frozen={frozen}
        />
        {frozen && (
          <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-1 text-xs text-status-warning">
            任务冬眠中:插话入口冻结,唤醒后继续。
          </div>
        )}
        {error && (
          <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-1 text-xs text-status-danger">
            读取失败:{error}
          </div>
        )}
        {/* 对话流(贴底跟滚,P4-22) */}
        <ChatStream
          sig={sig}
          resetKey={target?.sessionID || target?.kind}
          className="space-y-3 rounded-xl border border-border/55 bg-card px-3 py-3"
        >
          {messages.length === 0 && (
            <div className="py-8 text-center text-[13px] text-muted-foreground">
              子会话为空。问相关问题(不改执行,即时答)或改方向(带回执)。
            </div>
          )}
          {messages.map((m) => (
            <InterjectBlock key={m.id} msg={m} steerStateOf={steerStateOf} />
          ))}
        </ChatStream>
        {/* 输入区:问/改模式 + 发言 */}
        <div className="rounded-lg border border-border/55 bg-card p-3">
          <div className="mb-2 flex items-center gap-1">
            {[
              ["ask", "问", "不改执行,即时答"],
              ["steer", "改", isUnit ? "发送纠偏,带回执" : "建议进正式规划,判定后改"],
            ].map(([v, label, hint]) => (
              <button
                key={v}
                disabled={frozen}
                title={hint}
                className={cn(
                  "rounded-md border px-2 py-0.5 text-xs transition-colors cursor-pointer",
                  mode === v
                    ? "border-primary/50 bg-primary/15 text-primary"
                    : "text-muted-foreground hover:bg-hover-lift",
                  frozen && "cursor-not-allowed opacity-50"
                )}
                onClick={() => setMode(v)}
              >
                {label}
              </button>
            ))}
            {isUnit && target.running && !frozen && (
              <span className="ml-auto flex items-center gap-1">
                {target.paused ? (
                  <Button size="sm" variant="outline" onClick={() => unitControl("resume")}>
                    唤醒
                  </Button>
                ) : (
                  <Button size="sm" variant="outline" onClick={() => unitControl("pause")}>
                    冬眠
                  </Button>
                )}
                <Button size="sm" variant="outline" onClick={() => setConfirmStop(true)}>
                  终止
                </Button>
              </span>
            )}
          </div>
          <div className="flex items-end gap-2">
            <Textarea
              value={text}
              disabled={frozen}
              placeholder={
                mode === "ask"
                  ? isUnit
                    ? "问这只单位相关问题(基于其会话历史回答)"
                    : "问主宰当前规划相关问题"
                  : isUnit
                    ? "给这只单位改方向(下一次工具调用前生效)"
                    : "给主宰提方向建议(判定后改变规划)"
              }
              className="min-h-[48px] text-[13px] resize-none"
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  send();
                }
              }}
            />
            <Button onClick={send} disabled={sending || !text.trim() || frozen || !taskID}>
              发送
            </Button>
          </div>
        </div>
        {/* 熔断二次确认(终止不可恢复) */}
        {confirmStop && (
          <div className="rounded-md border border-status-danger/40 bg-status-danger-soft px-3 py-2 text-xs">
            <div className="text-status-danger">
              终止该单位后不可恢复,主宰会看到原因。确认终止?
            </div>
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="danger" onClick={stopUnit}>
                确认终止
              </Button>
              <Button size="sm" variant="outline" onClick={() => setConfirmStop(false)}>
                取消
              </Button>
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}

// InterjectBlock 一条插话发言:问(气泡+送达态+回答)或改(指令+回执)。
// 会话页(P2-08b)的直连对话流复用同一渲染。
export function InterjectBlock({ msg, steerStateOf }) {
  if (msg.mode === "steer") {
    const isAdvice = msg.ref_kind === "advice";
    const steerState = steerStateOf(msg.ref_id);
    const st = isAdvice
      ? ADVICE_STATES[msg.advice_state || "pending"] || ADVICE_STATES.pending
      : STEER_STATES[steerState] || STEER_STATES.queued;
    const reason = isAdvice ? msg.advice_reason : STEER_STATES[steerState]?.hint || "";
    return (
      <div className="flex flex-col items-end gap-0.5">
        <div className="t-body max-w-[85%] rounded-lg bg-primary/15 px-3 py-2 whitespace-pre-wrap [overflow-wrap:anywhere]">
          {msg.text}
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="num">{msg.at?.slice(11)}</span>
          <Badge variant="outline">改向</Badge>
          <Badge variant={st.variant}>{st.label}</Badge>
        </div>
        {reason && (
          <div className="max-w-[85%] text-xs text-muted-foreground">{reason}</div>
        )}
      </div>
    );
  }
  const st = DELIVERY[msg.state] || DELIVERY.queued;
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-col items-end gap-0.5">
        <div className="t-body max-w-[85%] rounded-lg bg-primary/15 px-3 py-2 whitespace-pre-wrap [overflow-wrap:anywhere]">
          {msg.text}
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="num">{msg.at?.slice(11)}</span>
          <Badge variant={st.variant}>{st.label}</Badge>
        </div>
        {msg.state === "failed" && msg.note && (
          <div className="max-w-[85%] text-xs text-status-danger">{msg.note}</div>
        )}
      </div>
      {msg.reply && (
        <div className="flex flex-col items-start gap-0.5">
          <div className="t-body max-w-[90%] rounded-lg border border-border/55 bg-background px-3 py-2 whitespace-pre-wrap [overflow-wrap:anywhere]">
            {msg.reply}
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">回答</Badge>
            <span className="num">{msg.reply_at?.slice(11)}</span>
          </div>
        </div>
      )}
    </div>
  );
}
