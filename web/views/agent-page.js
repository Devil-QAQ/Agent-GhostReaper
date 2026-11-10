"use client";

import * as React from "react";
import { toast } from "sonner";

import { Field, Fold, NUM } from "@/components/form-bits";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { postJSON, usePolling } from "@/lib/api";
import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// Agent 配置(P2-08d;审核迭代改卡片墙+弹窗,与 LLM配置页同风格):
// 牧者 / 主宰 / 虫群 / 插话子会话四张卡——提示词可编辑正文(固定段
// 只读)与运行参数(自系统设置迁入)。保存走键值表单写者带审计;正文
// 对新会话生效,参数按各自消费点节奏生效,在跑不中断。
const ROLE_ORDER = [
  { id: "shepherd", icon: "messageCircle" },
  { id: "overmind", icon: "brain" },
  { id: "swarm", icon: "bug" },
  { id: "interject", icon: "megaphone" },
];

// 每个角色的运行参数字段(弹窗表单、卡片摘要、审计行共用一张定义)。
// unlimited 字段留空 = 不限(提交 0,后端删键回默认)。
const PARAM_FIELDS = {
  overmind: [
    { key: "queue_limit", label: "派发上限", unit: "步", min: 1, max: 1000, get: (p) => p?.queue?.value, audit: (p) => p?.queue },
    { key: "heartbeat_seconds", label: "心跳巡检间隔", unit: "秒", min: 10, max: 86400, get: (p) => p?.heartbeat?.value, audit: (p) => p?.heartbeat },
    { key: "closure_rounds", label: "收口信号轮数", unit: "轮", min: 1, max: 100, get: (p) => p?.closure?.rounds?.value, audit: (p) => p?.closure?.rounds },
    { key: "closure_minutes", label: "收口信号间隔", unit: "分钟", min: 1, max: 1440, get: (p) => p?.closure?.minutes?.value, audit: (p) => p?.closure?.minutes },
  ],
  swarm: [
    { key: "max_concurrent_units", label: "并发上限", unit: "个单位", min: 1, max: 64, get: (p) => p?.units?.value, audit: (p) => p?.units },
    { key: "anchor_every_steps", label: "纠偏步数", unit: "步", min: 1, max: 1000, get: (p) => p?.reanchor?.value, audit: (p) => p?.reanchor },
    { key: "blocked_deep_minutes", label: "受阻判定时长", unit: "分钟", min: 1, max: 1440, get: (p) => p?.blocked_deep?.value, audit: (p) => p?.blocked_deep },
    { key: "max_turns", label: "轮次上限", unit: "轮", min: 1, max: 1000, unlimited: true, get: (p) => p?.budget?.max_turns, audit: (p) => p?.budget?.max_turns_by ? { by: p.budget.max_turns_by, updated_at: p.budget.max_turns_updated_at } : null },
    { key: "run_seconds", label: "时长上限", unit: "秒", min: 1, max: 86400, unlimited: true, get: (p) => p?.budget?.run_seconds, audit: (p) => p?.budget?.run_seconds_by ? { by: p.budget.run_seconds_by, updated_at: p.budget.run_seconds_updated_at } : null },
  ],
};

// fieldValue 表单值 → 提交值:unlimited 字段留空 = 0(不限);其余解析
// 整数(空/坏值为 NaN,由调用方校验)。
function fieldValue(f, form) {
  const raw = (form?.[f.key] ?? "").trim();
  if (f.unlimited && raw === "") return 0;
  return parseInt(raw, 10);
}

// cardParamLine 卡片摘要行:固定段数 + 各角色参数一行(数值精确呈现;
// 预算 0 显示为不限)。
function cardParamLine(id, params) {
  if (!params) return "";
  const budgetVal = (key) => {
    const v = params.budget?.[key];
    return v > 0 ? v : "不限";
  };
  if (id === "overmind") {
    return `派发上限 ${params.queue?.value ?? "—"} 步 · 心跳巡检 ${params.heartbeat?.value ?? "—"} 秒 · 收口 ${params.closure?.rounds?.value ?? "—"} 轮 / ${params.closure?.minutes?.value ?? "—"} 分钟 · 停滞窗口 ${params.stall_window_seconds ?? "—"} 秒`;
  }
  if (id === "swarm") {
    return `并发 ${params.units?.value ?? "—"} · 纠偏 ${params.reanchor?.value ?? "—"} 步 · 受阻判定 ${params.blocked_deep?.value ?? "—"} 分钟 · 轮次 ${budgetVal("max_turns")} · 时长 ${budgetVal("run_seconds")}`;
  }
  return "";
}

export function AgentPage() {
  const { data, error, setData } = usePolling("/api/agents", 15000, { immediate: true });
  const [editing, setEditing] = React.useState(null); // 编辑中的角色 id
  const loading = !data && !error;

  const closeDialog = () => setEditing(null);

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Agent"
        subtitle="牧者 / 主宰 / 虫群 / 插话子会话 · 提示词与运行参数"
      />

      {error && (
        <div className="rounded-md bg-status-danger-soft px-3 py-2 text-sm text-status-danger">
          读取失败:{error}
        </div>
      )}

      {loading ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {[0, 1].map((i) => (
            <Card key={i}>
              <CardContent className="flex flex-col gap-4 p-5">
                <div className="skeleton h-5 w-36" />
                <div className="skeleton h-4 w-48" />
                <div className="skeleton h-3 max-w-72 w-full" />
                <div className="skeleton h-3 w-56" />
                <div className="skeleton mt-2 h-8 w-64" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {ROLE_ORDER.filter(({ id }) => data?.roles?.[id]).map(({ id, icon }, i) => (
            <div key={id} className="animate-rise" style={{ animationDelay: `${Math.min(i, 6) * 40}ms` }}>
              <AgentCard
                id={id}
                icon={icon}
                view={data.roles[id]}
                params={data?.params}
                onEdit={() => setEditing(id)}
              />
            </div>
          ))}
        </div>
      )}

      <AgentDialog
        roleId={editing}
        view={editing ? data?.roles[editing] : null}
        params={data?.params}
        onReply={setData}
        onClose={closeDialog}
      />
    </div>
  );
}

// AgentCard 卡片墙单元:名称与正文状态一行、职责与参数摘要灰阶分层,
// 覆盖审计只在有覆盖时出现,操作沉底;hover 微投影(与模型卡同款)。
function AgentCard({ id, icon, view, params, onEdit }) {
  const paramLine = cardParamLine(id, params);
  const fixed = view.fixed || []; // 空固定段防崩(服务端恒非空,防患)
  return (
    <Card className="h-full transition-shadow duration-150 hover:shadow-card">
      <CardContent className="flex h-full flex-col gap-4 p-5">
        <div className="flex items-center gap-3">
          <span className="t-sec flex items-center gap-2 leading-none">
            {React.createElement(iconOf(icon), { className: "size-4 text-muted-foreground" })}
            {view.title}
          </span>
          {view.overridden ? (
            <Badge variant="info">正文已自定义</Badge>
          ) : (
            <Badge variant="outline">默认正文</Badge>
          )}
        </div>

        <div className="flex flex-col gap-1">
          <div className="text-[13px]">{view.summary}</div>
          <div className="text-xs text-muted-foreground">
            固定段 {fixed.length} 段 · 内置,编辑不可删
          </div>
          {paramLine && (
            <div className="truncate num text-xs text-muted-foreground" title={paramLine}>{paramLine}</div>
          )}
          {view.overridden && view.editable_by && view.editable_updated_at && (
            <div className="text-xs text-muted-foreground">
              {view.editable_by} 于 {view.editable_updated_at} 修改
            </div>
          )}
        </div>

        <div className="mt-auto flex items-center gap-2 border-t pt-4">
          <span className="ml-auto">
            <Button variant="ghost" size="sm" onClick={onEdit}>
              编辑
            </Button>
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

// AgentDialog 编辑弹窗:可编辑正文 + 固定段只读折叠 + 运行参数(主宰带
// 心跳;虫群带并发/纠偏/预算)。保存只提交改动项;恢复默认两段式。
function AgentDialog({ roleId, view, params, onReply, onClose }) {
  const [text, setText] = React.useState("");
  const [form, setForm] = React.useState(null); // 参数字段本地值 {key: "600"}
  const [fixedOpen, setFixedOpen] = React.useState(false);
  const [armed, setArmed] = React.useState(false); // 恢复默认的两段式确认
  const [busy, setBusy] = React.useState(false);
  const open = !!roleId && !!view;

  React.useEffect(() => {
    if (open) {
      setText(view.editable_now);
      setFixedOpen(false);
      setArmed(false);
      const init = {};
      for (const f of PARAM_FIELDS[roleId] || []) {
        const v = f.get(params);
        init[f.key] = f.unlimited && !(v > 0) ? "" : String(v ?? "");
      }
      setForm(init);
    }
  }, [open, roleId]);

  React.useEffect(() => {
    if (armed) {
      const t = setTimeout(() => setArmed(false), 3000);
      return () => clearTimeout(t);
    }
  }, [armed]);

  if (!open) return null;
  const fields = PARAM_FIELDS[roleId] || [];
  const fixed = view.fixed || []; // 空固定段防崩(服务端恒非空,防患)
  const promptDirty = text.trim() !== view.editable_now;
  const paramsDirty = fields.some((f) => fieldValue(f, form) !== f.get(params));
  const dirty = promptDirty || paramsDirty;

  const reset = async () => {
    if (!armed) {
      setArmed(true);
      return;
    }
    setArmed(false);
    setBusy(true);
    const { ok, body } = await postJSON("/api/agents/prompt/reset", { role: roleId });
    setBusy(false);
    if (!ok) {
      toast.error(`恢复失败:${body.error || "原因未说明"}`);
      return;
    }
    onReply(body);
    setText(body.roles[roleId].editable_now);
    toast.success("已恢复默认正文");
  };

  const save = async () => {
    const body = text.trim();
    if (!body) {
      toast.error("正文不能为空;要恢复默认请用「恢复默认」");
      return;
    }
    if (body.length > 4000) {
      toast.error("正文过长(上限 4000 字)");
      return;
    }
    const payload = {};
    for (const f of fields) {
      const n = fieldValue(f, form);
      if (f.unlimited) {
        if (!Number.isFinite(n) || n < 0 || n > f.max) {
          toast.error(`${f.label}须为 0–${f.max} 的整数,留空为不限(${f.unit})`);
          return;
        }
      } else if (!Number.isFinite(n) || n < f.min || n > f.max) {
        toast.error(`${f.label}须为 ${f.min}–${f.max} 的整数(${f.unit})`);
        return;
      }
      if (n !== f.get(params)) payload[f.key] = n;
    }
    setBusy(true);
    let rep = null;
    if (promptDirty) {
      const r = await postJSON("/api/agents/prompt", { role: roleId, body });
      if (!r.ok) {
        setBusy(false);
        toast.error(`保存失败:${r.body.error || "原因未说明"}`);
        return;
      }
      rep = r.body;
    }
    if (Object.keys(payload).length > 0) {
      const r = await postJSON("/api/agents/params", payload);
      if (!r.ok) {
        setBusy(false);
        toast.error(`保存失败:${r.body.error || "原因未说明"}`);
        return;
      }
      rep = r.body;
    }
    setBusy(false);
    if (rep) {
      onReply(rep);
      toast.success("已保存");
    }
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[88vh] w-[min(620px,94vw)] max-w-none gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>编辑 {view.title}</DialogTitle>
          <DialogDescription>
            {view.summary} · 固定段由内置,编辑不可删
          </DialogDescription>
        </DialogHeader>

        {/* 可编辑正文 */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <span className="text-[13px] text-muted-foreground">可编辑正文</span>
            <span className="text-xs text-muted-foreground">保存后对之后的新会话生效</span>
          </div>
          <Textarea
            className="min-h-32 text-sm leading-relaxed"
            value={text}
            onChange={(e) => setText(e.target.value)}
            spellCheck={false}
          />
          {view.overridden && view.editable_by && view.editable_updated_at && (
            <div className="text-xs text-muted-foreground">
              {view.editable_by} 于 {view.editable_updated_at} 修改
            </div>
          )}
        </div>

        {/* 固定段折叠:横线 — 箭头 — 文字 — 横线 */}
        <Fold open={fixedOpen} onToggle={() => setFixedOpen((v) => !v)}>
          固定段 · {fixed.length} 段(只读)
        </Fold>
        {fixedOpen && (
          <div className="flex animate-rise flex-col gap-2">
            {fixed.map((seg) => (
              <div key={seg.label} className="rounded-md border border-border/55 bg-input-surface p-3">
                <div className="mb-2 text-xs font-medium text-muted-foreground">{seg.label}</div>
                <div className="whitespace-pre-wrap text-xs leading-relaxed text-muted-foreground">
                  {seg.text}
                </div>
              </div>
            ))}
          </div>
        )}

        {/* 运行参数(牧者 / 插话子会话无参数) */}
        {fields.length > 0 && (
          <>
            <Fold withArrow={false}>运行参数</Fold>
            <div className="flex flex-col gap-3">
              {fields.map((f) => {
                const a = f.audit(params);
                return (
                  <div key={f.key} className="flex flex-col gap-1">
                    <Field label={f.label}>
                      <Input
                        className={NUM}
                        type="number"
                        min={f.unlimited ? 0 : f.min}
                        max={f.max}
                        inputMode="numeric"
                        placeholder={f.unlimited ? "不限" : undefined}
                        value={form?.[f.key] ?? ""}
                        onChange={(e) => setForm({ ...form, [f.key]: e.target.value })}
                      />
                      <span className="text-xs text-muted-foreground">
                        {f.unlimited ? `${f.unit}(留空为不限)` : `${f.unit}(${f.min}–${f.max})`}
                      </span>
                    </Field>
                    {a?.by && a?.updated_at && (
                      <div className="pl-[92px] text-xs text-muted-foreground">
                        {a.by} 于 {a.updated_at} 修改
                      </div>
                    )}
                  </div>
                );
              })}
              {roleId === "overmind" && (
                <p className="text-xs text-muted-foreground">
                  心跳每轮自动检查在跑单位,有异常才唤醒主宰;改间隔对下一轮生效。停滞窗口与消耗异常上限只读:
                  {" "}{params?.stall_window_seconds ?? "—"} 秒 · {params?.burn_anomaly_tokens ?? "—"} token/轮。
                  <br />
                  收口信号两项是全局默认,任务未单独配置时用此值;建任务时与任务运行中(工作台)可按任务改。
                </p>
              )}
              {roleId === "swarm" && (
                <p className="text-xs text-muted-foreground">
                  并发与预算对之后领取的单位生效;纠偏步数是新任务的默认间隔,建任务时可单独改;收尾轮
                  {" "}{params?.budget?.settle_turns ?? "—"} 轮(只读默认)。
                </p>
              )}
            </div>
          </>
        )}

        <DialogFooter>
          {view.overridden && (
            <Button variant="outline" disabled={busy} onClick={reset}>
              {armed ? "确认恢复?" : "恢复默认"}
            </Button>
          )}
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button disabled={busy || !dirty} onClick={save}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Field/Fold/NUM 为 components/form-bits 共享件(P2-13b 抽取,LLM配置页同款)。
