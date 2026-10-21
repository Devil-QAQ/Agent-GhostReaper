"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// 牧者侧对话块共用组件(自会话页抽出单一实现,P4-14 起牧者主对话与
// 成果追问大窗共用):一条问答(送达态/思考/工具折叠卡/建议内联卡/
// 回复引用可点)。数据形状同 shepherd 读会话 API 的 messages 项。

// DELIVERY 送达状态词表(queued|delivered|replied|failed → 徽标)。
export const DELIVERY = {
  queued: { label: "已排队", variant: "paused" },
  delivered: { label: "已送达", variant: "running" },
  replied: { label: "已回复", variant: "success" },
  failed: { label: "失败", variant: "danger" },
};

// nowStamp 本地时间戳(与账本事件侧 at 同格式,本地即显的消息用)。
export const nowStamp = () => new Date().toLocaleString("sv-SE").slice(0, 19);

// MessageBlock 一条问答:造物主消息(带送达状态)→ 思考段 → 工具折叠卡
// 流 → 牧者回复(引用可点击)。
export function MessageBlock({ msg, taskID, onNode }) {
  const st = DELIVERY[msg.state] || DELIVERY.queued;
  // 系统收尾提示(任务终态,P2-08f):非造物主发言,居中窄条呈现;
  // 牧者的一句话进展总结照常挂在其下。
  if (msg.system) {
    return (
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2 py-0.5">
          <span className="h-px flex-1 bg-border" />
          <span className="text-[11px] text-muted-foreground">{msg.text.split("。")[0]}</span>
          <span className="h-px flex-1 bg-border" />
        </div>
        {(msg.thinking || (msg.tool_calls && msg.tool_calls.length) > 0 || msg.reply) && (
          <div className="flex flex-col items-start gap-2">
            {msg.thinking && <ThinkingCard text={msg.thinking} />}
            {(msg.tool_calls || []).map((c, i) => (
              <ToolCallCard key={i} call={c} />
            ))}
            {msg.reply && (
              <div className="max-w-[90%] rounded-lg border bg-background px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
                <RefText text={msg.reply} taskID={taskID} onNode={onNode} />
                <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                  <Badge variant="outline">进展总结</Badge>
                  <span className="font-mono">{msg.reply_at?.slice(11)}</span>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      {/* 造物主 */}
      <div className="flex flex-col items-end gap-0.5">
        <div className="max-w-[85%] rounded-lg bg-primary/15 px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
          {msg.text}
        </div>
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span className="font-mono">{msg.at?.slice(11)}</span>
          <Badge variant={st.variant}>{st.label}</Badge>
        </div>
        {msg.state === "failed" && msg.note && (
          <div className="max-w-[85%] text-[11px] text-status-danger">{msg.note}</div>
        )}
      </div>
      {/* 牧者 */}
      {(msg.thinking || (msg.tool_calls && msg.tool_calls.length) > 0 || msg.reply) && (
        <div className="flex flex-col items-start gap-2">
          {msg.thinking && <ThinkingCard text={msg.thinking} />}
          {(msg.tool_calls || []).map((c, i) => (
            <ToolCallCard key={i} call={c} />
          ))}
          {(msg.advices || []).map((a) => (
            <InlineAdvice key={a.id} advice={a} />
          ))}
          {msg.reply && (
            <div className="max-w-[90%] rounded-lg border bg-background px-3 py-2 text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">
              <RefText text={msg.reply} taskID={taskID} onNode={onNode} />
              <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                <Badge variant="outline">回复</Badge>
                <span className="font-mono">{msg.reply_at?.slice(11)}</span>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ThinkingCard 思考段(消息类型徽标:thinking)。
export function ThinkingCard({ text }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="max-w-[85%] rounded-lg border bg-background">
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] text-muted-foreground cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">思考</Badge>
        <span className="truncate">{text.slice(0, 60)}…</span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "ml-auto size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <div className="border-t px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
          {text}
        </div>
      )}
    </div>
  );
}

// ToolCallCard 工具调用折叠卡(8.5:聚合一次回合的工具调用,展开为
// 步骤卡片流——状态色边 + 工具名;此处一条一卡,参数与结果头部展开)。
export function ToolCallCard({ call }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div
      className={cn(
        "max-w-[85%] rounded-lg border bg-background",
        call.is_error && "border-status-danger/60"
      )}
    >
      <button
        className="flex w-full items-center gap-2 px-3 py-1 text-[11px] cursor-pointer"
        onClick={() => setOpen((o) => !o)}
      >
        <Badge variant="outline">{call.is_error ? "工具出错" : "工具调用"}</Badge>
        <span className="truncate font-mono">{call.name}</span>
        <span className="ml-auto shrink-0 text-muted-foreground">
          {call.at?.slice(11)}
        </span>
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
          className: "size-3.5 shrink-0",
        })}
      </button>
      {open && (
        <div className="space-y-2 border-t px-3 py-2 text-xs">
          {call.args && (
            <div>
              <div className="mb-0.5 text-muted-foreground">参数</div>
              <pre className="overflow-x-auto rounded-md bg-input-surface p-2 font-mono whitespace-pre-wrap break-all">
                {call.args}
              </pre>
            </div>
          )}
          {call.result_head && (
            <div>
              <div className="mb-0.5 text-muted-foreground">结果</div>
              <pre className="max-h-56 overflow-y-auto rounded-md bg-input-surface p-2 font-mono whitespace-pre-wrap break-all">
                {call.result_head}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// InlineAdvice 对主宰的建议(纳谏)内联卡:牧者在某条消息里提交的建议,
// 卡片落在对话流的原位;裁决状态原地更新(待裁决/已采纳/未采纳+理由)。
export function InlineAdvice({ advice }) {
  const meta = (st) =>
    st === "pending"
      ? { label: "待裁决", variant: "paused" }
      : st === "adopted"
        ? { label: "已采纳", variant: "success" }
        : { label: "未采纳", variant: "danger" };
  const m = meta(advice.state);
  return (
    <div className="w-[85%] rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-[12px]">
      <div className="flex items-center gap-2">
        {React.createElement(iconOf("sparkles"), { className: "size-3 shrink-0 text-primary" })}
        <span className="text-muted-foreground">对主宰的建议</span>
        <Badge variant={m.variant}>{m.label}</Badge>
        <span className="font-mono text-[11px] text-muted-foreground">{advice.id}</span>
      </div>
      <div className="mt-1 whitespace-pre-wrap [overflow-wrap:anywhere]">{advice.text}</div>
      {advice.reason && (
        <div className="mt-1 text-[11px] text-muted-foreground">裁决:{advice.reason}</div>
      )}
    </div>
  );
}

// ---- fact# 等引用可点击 ----

// 引用词形两种:带前缀编号(fact:f-xx / claim:c-xx / step:s1 / asset:a-xx,
// 牧者提示词约定)与图上 ID 的列表写法(f-xxxx: / tc-chain-xxxx: /
// h-xxxx: / a-xxxx: / g-xxxx:,后跟冒号的定义式——模型的常见列表格式)。
const REF_RE =
  /(fact|claim|step|asset|goal|hypo)[:#]([A-Za-z0-9_-]+)|\b((?:tc-chain-|f-|h-|a-|g-)[a-z0-9]+)(?=\**:)/g;

export function RefText({ text, taskID, onNode }) {
  const parts = [];
  let last = 0;
  for (const m of text.matchAll(REF_RE)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const nodeID = m[2] || m[3]; // 带前缀形态取编号;列表形态整词即 ID
    parts.push(
      <button
        key={m.index}
        className="cursor-pointer font-mono text-primary underline decoration-dotted underline-offset-2"
        title="查看节点"
        onClick={async () => {
          const r = await fetch(`/api/tasks/${taskID}/nodes/${nodeID}`);
          if (!r.ok) {
            toast.error("节点不在账本中");
            return;
          }
          const n = await r.json();
          onNode({ type: n.type, id: n.id });
        }}
      >
        {m[0]}
      </button>
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}
