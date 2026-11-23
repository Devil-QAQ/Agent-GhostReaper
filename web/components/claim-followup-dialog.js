"use client";

import * as React from "react";
import { toast } from "sonner";

import { ChatStream, MessageBlock, nowStamp, tailGrowSig } from "@/components/chat-blocks";
import { LongText } from "@/components/long-text";
import { NodeRefButton } from "@/components/node-ref-button";
import { SessionInfoBar } from "@/components/session-info-bar";
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
import { postJSON, severityMeta, usePolling } from "@/lib/api";
import { NodeSheetHost } from "@/views/node-sheets";

// 追问大窗(P4-14 起;P5-07② 单一追问入口:锚定条目为漏洞或事实,
// 合并原"深入"——问→牧者读账本作答;吩咐继续挖→牧者落实成动作
//(急活通道,最高权限操作面既有),问与吩咐全走对话记账)。一条目
// 一会话、续接不新建——退出再开历史在;"新开一轮"次要按钮下一问起
// 新会话(旧轮留档,经牧者组条目回看)。开场第一条是链路摘要卡(与
// 所锚条目详情同源),不空白开场;答题者为牧者:读账本、人话回答,
// 引用节点可点(窗内节点抽屉下钻),查账本动作以工具折叠卡呈现。
// 信息条独立计(按本会话锚对账)。

// 追问输入区提示(P5-07② 锚例逐字,一个字不改)。
const FOLLOWUP_HINT = "可问相关问题，也可让它深入继续挖";

export function ClaimFollowupDialog({ tid, anchor, round, open, onClose }) {
  // anchor = {kind: "claim"|"fact", id};kind 缺省按漏洞(既有调用点)。
  const kind = anchor?.kind || "claim";
  const claimID = anchor?.id || "";
  // 本窗查看的轮次:null = 最新轮(续问落点);入口可带旧轮(回看)。
  const [viewRound, setViewRound] = React.useState(round ?? null);
  React.useEffect(() => {
    if (open) setViewRound(round ?? null);
  }, [open, round, claimID]);
  // 锚定条与预载摘要卡的数据面(与所锚条目详情同一 API,同源对账)。
  const { data: detail } = usePolling(
    open && claimID
      ? kind === "fact"
        ? `/api/tasks/${tid}/nodes/${claimID}`
        : `/api/tasks/${tid}/claims/${claimID}`
      : "/api/healthz",
    2000,
    { immediate: Boolean(open && claimID) }
  );
  // 追问会话读视图(回答期间轮询加快)。
  const qaURL = open && claimID ? `/api/tasks/${tid}/shepherd/claim/${claimID}` : null;
  const { data: chat, setData: setChat } = usePolling(
    qaURL ? `${qaURL}${viewRound ? `?round=${viewRound}` : ""}` : "/api/healthz",
    (d) => (d?.busy ? 700 : 2000),
    { immediate: Boolean(qaURL), live: qaURL ? [`shepherd:${tid}:${claimID}`] : null }
  );

  const [text, setText] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [pendingNew, setPendingNew] = React.useState(false); // 新开一轮待生效
  const [node, setNode] = React.useState(null); // 窗内节点抽屉(引用下钻不卸载本窗)

  const claim = detail?.claim || {};
  const sev = kind === "claim" ? severityMeta(claim.severity || detail?.repro?.severity) : null;
  const anchorTitle =
    kind === "fact" ? detail?.polished_title || detail?.polished_statement || detail?.title || claimID : claim.title || claim.statement || claimID;
  const messages = chat?.messages || [];
  const hibernated = Boolean(chat?.hibernated);
  // 历史轮次只读:看的不是最新轮时不可续问(续接永远落在最新轮)。
  const viewingRound = chat?.round || 0;
  const latestRound = chat?.latest_round || viewingRound;
  const readOnly = viewingRound > 0 && viewingRound < latestRound;
  const frozen = hibernated || readOnly;
  // 流内容签名(P4-22):问答条数 + 末条回答量,贴底跟滚判据;换条目/
  // 换轮次视作新会话,回到默认在底部。
  const sig = `${messages.length}|${tailGrowSig(messages[messages.length - 1])}`;

  const send = async () => {
    const body = text.trim();
    if (!body || !tid || !claimID) return;
    setSending(true);
    const { ok, body: resp } = await postJSON(
      `/api/tasks/${tid}/shepherd/claim/${claimID}/messages`,
      { text: body, new_round: pendingNew }
    );
    setSending(false);
    if (!ok) {
      toast.error(resp?.error || "发送失败");
      return;
    }
    setText("");
    const wasPendingNew = pendingNew;
    setPendingNew(false);
    // 本地即显:已排队立刻上屏,回复经轮询到达;新开一轮即切到新轮
    //(读视图回到最新轮)。
    if (chat && resp?.message_id && !messages.some((m) => m.id === resp.message_id)) {
      setChat({
        ...chat,
        messages: [
          ...messages,
          { id: resp.message_id, text: body, state: "queued", at: nowStamp() },
        ],
      });
    }
    if (wasPendingNew) {
      toast.success(`已开第 ${resp.round} 轮`);
      setViewRound(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex h-[85vh] max-h-[85vh] w-[min(720px,calc(100vw-32px))] max-w-[720px] flex-col gap-4">
        <DialogHeader className="space-y-0 pb-0 text-left">
          {/* 锚定条:徽标(漏洞带严重级)+ 条目名 + 追问(轮次) */}
          <DialogTitle className="flex min-w-0 flex-wrap items-center gap-2">
            {React.createElement(iconOf("messageCircle"), { className: "size-4 shrink-0 text-primary" })}
            {sev && <Badge variant={sev.variant} className="shrink-0">{sev.label}</Badge>}
            <span className="shrink-0">追问</span>
            <LongText
              text={anchorTitle || "…"}
              max={44}
              variant="dialog"
              label="条目标题"
              className="min-w-0 flex-1 font-normal"
            />
            {(chat?.rounds || 0) > 1 && (
              <Badge variant="outline" className="shrink-0">
                第 {viewingRound}/{chat.rounds} 轮
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription className="truncate">
            牧者读账本作答 · 引用可点击下钻 · 一条目一会话,续接不新建
          </DialogDescription>
        </DialogHeader>
        <SessionInfoBar
          info={chat?.info}
          startedAt={chat?.started_at}
          deductSeconds={chat?.hibernate_seconds}
          frozen={hibernated}
        />
        {hibernated && (
          <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-1 text-xs text-status-warning">
            任务冬眠中:追问入口冻结,唤醒后继续。
          </div>
        )}
        {chat && chat.provider_on === false && !hibernated && (
          <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-1 text-xs text-status-warning">
            模型未配置:追问会记录并记为失败。在 LLM配置 页激活模型后重发。
          </div>
        )}
        {/* 对话流(贴底跟滚,P4-22):链路摘要卡开场 + 问答 */}
        <ChatStream
          sig={sig}
          resetKey={`${claimID}|${kind}|${viewRound}`}
          className="space-y-3 rounded-xl border border-border/55 bg-card px-3 py-3"
        >
          {kind === "fact" ? (
            <FactPreludeCard detail={detail} onSelectNode={setNode} />
          ) : (
            <ClaimPreludeCard detail={detail} onSelectNode={setNode} />
          )}
          {messages.length === 0 && (
            <div className="py-6 text-center text-[13px] text-muted-foreground">
              尚未追问。{FOLLOWUP_HINT}。
            </div>
          )}
          {messages.map((m) => (
            <MessageBlock key={m.id} msg={m} taskID={tid} onNode={setNode} />
          ))}
        </ChatStream>
        {/* 输入区 */}
        <div className="rounded-xl border border-border/55 bg-card p-3">
          <div className="mb-1 flex items-center gap-2 text-[11px] text-muted-foreground">
            <span>
              {readOnly
                ? `历史轮次(第 ${viewingRound} 轮)只读`
                : hibernated
                  ? "任务冬眠中"
                  : chat?.busy
                    ? "牧者在回答"
                    : messages.length > 0
                      ? "继续追问"
                      : "发起追问"}
            </span>
            <span className="flex-1" />
            {readOnly ? (
              <button
                className="cursor-pointer text-primary underline decoration-dotted underline-offset-2"
                onClick={() => setViewRound(null)}
              >
                回到最新一轮
              </button>
            ) : (
              pendingNew && (
                <button
                  className="cursor-pointer text-primary underline decoration-dotted underline-offset-2"
                  onClick={() => setPendingNew(false)}
                >
                  新开一轮:下一问起新会话 · 撤销
                </button>
              )
            )}
          </div>
          <div className="flex items-end gap-2">
            <Textarea
              value={text}
              disabled={frozen}
              placeholder={
                readOnly
                  ? "历史轮次只读;回到最新一轮后可续问"
                  : hibernated
                    ? "任务冬眠中,唤醒后继续追问"
                    : FOLLOWUP_HINT
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
            <Button onClick={send} disabled={sending || !text.trim() || frozen || !tid}>
              发送
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-9"
              title="下一问起新会话(旧轮留档可回看)"
              disabled={frozen}
              onClick={() => setPendingNew((p) => !p)}
            >
              {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
              新开一轮
            </Button>
          </div>
        </div>
      </DialogContent>
      {/* 窗内节点抽屉:引用下钻不卸载本窗(逐层返回) */}
      <NodeSheetHost tid={tid} value={node} onChange={setNode} />
    </Dialog>
  );
}

// ClaimPreludeCard 漏洞链路摘要卡(开场第一条):P4-13 定稿叙述 + 关键
// 节点引用行(发现单位 / 反证 / 终态时刻)——与漏洞详情同源数据,
// 确定性呈现。
function ClaimPreludeCard({ detail, onSelectNode }) {
  const repro = detail?.repro;
  const origin = detail?.origin;
  const discoverer = origin?.discoverer;
  const falsifiers = origin?.falsifiers || [];
  const confirmedAt = detail?.timing?.terminal_at;
  const terminalWord =
    detail?.claim?.status === "confirmed" ? "确认" : "结束";
  return (
    <div className="flex flex-col gap-1.5 rounded-lg border border-dashed bg-background px-3 py-3 text-xs">
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        <Badge variant="outline">链路摘要</Badge>
        <span>开场预载 · 与漏洞详情一致</span>
      </div>
      <div className="leading-relaxed [overflow-wrap:anywhere]">
        {repro?.narrative || "定稿时未生成叙述"}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span className="flex items-center gap-1">
          发现单位
          {discoverer ? (
            <>
              <span className="num">{discoverer.unit_id}</span>
              {discoverer.step_id && (
                <NodeRefButton
                  meta={{ id: discoverer.step_id, type: "step", typeLabel: "步骤" }}
                  onSelectNode={onSelectNode}
                />
              )}
            </>
          ) : (
            "无记录"
          )}
        </span>
        {falsifiers.length > 0 && (
          <span className="flex items-center gap-1">
            反证 {falsifiers.length} 轮
            {falsifiers.slice(0, 3).map((f, i) =>
              f.step_id ? (
                <NodeRefButton
                  key={`${f.step_id}-${i}`}
                  meta={{ id: f.step_id, type: "step", typeLabel: `第${f.round}轮` }}
                  onSelectNode={onSelectNode}
                />
              ) : null
            )}
          </span>
        )}
        <span>
          {terminalWord} {confirmedAt ? confirmedAt.slice(5, 16) : "进行中"}
        </span>
      </div>
    </div>
  );
}

// FactPreludeCard 事实链路摘要卡(P5-07② 事实锚定):陈述 + 推断标记 +
// 关联资产 + 发现单位 + 确认时刻 + 证据截头——与事实详情同源数据,
// 确定性呈现。
function FactPreludeCard({ detail, onSelectNode }) {
  if (!detail?.id) {
    return (
      <div className="rounded-lg border border-dashed bg-background px-3 py-3 text-xs text-muted-foreground">
        读取条目中…
      </div>
    );
  }
  const d = detail?.detail || {};
  return (
    <div className="flex flex-col gap-1.5 rounded-lg border border-dashed bg-background px-3 py-3 text-xs">
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        <Badge variant="outline">链路摘要</Badge>
        <span>开场预载 · 与事实详情一致</span>
      </div>
      <div className="leading-relaxed [overflow-wrap:anywhere]">
        {detail.polished_statement || detail.title || "(无陈述)"}
      </div>
      {(detail.assets || []).length > 0 && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
          <span className="flex items-center gap-1">
            关联资产
            {(detail.assets || []).map((a) => (
              <NodeRefButton
                key={a.id}
                meta={{ id: a.id, type: "asset", typeLabel: a.title || a.id }}
                onSelectNode={onSelectNode}
              />
            ))}
          </span>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        {d.unit_id && <span>发现单位 <span className="num">{d.unit_id}</span></span>}
        <span>
          确认 {(detail.confirmed_at || detail.added_at || "").slice(5, 16) || "—"}
        </span>
      </div>
      {(d.evidence_head || d.evidence) && (
        <div className="truncate num text-[11px] text-muted-foreground" title={d.evidence_head || d.evidence}>
          证据:{d.evidence_head || d.evidence}
        </div>
      )}
    </div>
  );
}
