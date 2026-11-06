"use client";

import * as React from "react";
import { toast } from "sonner";

import { downloadExport } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { Field, Fold, INPUT, NUM } from "@/components/form-bits";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { HARD_RULE_GROUPS, usePolling, postJSON } from "@/lib/api";
import { cn } from "@/lib/utils";

// 系统设置(P2-08c 收窄 + P2-08d 再收窄:模型库与主备在 LLM配置 页,agent
// 提示词与运行参数在 Agent 页;P2-08e 拦截规则;P3-03 搜索配置;P3-09 目标
// 凭据与面板密码)。P3-09 审核改版:全部条目统一概览卡形态(系统参数 /
// 年轮账本为基准)——卡面只放一句话摘要与关键数值,操作与完整展示点卡
// 弹二级界面(右上箭头示意可点)。
export function SettingsPage() {
  const { data, error } = usePolling("/api/config", 10000, { immediate: true });
  const sys = data?.system;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="系统设置" subtitle="系统级参数与全局拦截规则" />

      {error && (
        <div className="rounded-md bg-status-danger-soft px-3 py-2 text-sm text-status-danger">
          读取失败:{error}
        </div>
      )}

      {/* 设置行卡列表(与任务列表同款形态):每行固定两行内容,八行天然
          同高——信息量差异收进右侧关键值与二级弹窗,卡面不再有高度差 */}
      <div className="flex flex-col gap-3">
        <SystemParamsRow sys={sys} />

        <SearchConfigCard search={data?.search} />

        <SettingRow
          title="年轮账本"
          summary="账本是唯一事实来源,历史全保留"
          value="SQLite · 只增不改"
        />

        <GuardRulesCard />

        <UAConfigCard ua={data?.ua} />

        <CredentialsCard />

        <PanelPasswordCard />

        <DiagnosticsCard />
      </div>

      <p className="text-xs text-muted-foreground">
        模型配置在 LLM配置 页;牧者 / 主宰 / 虫群的提示词与运行参数在 Agent
        页;按任务的资产与动作边界由 RoE 面板管理。
      </p>
    </div>
  );
}

// SettingRow 设置行卡(P4-31 返工:概览卡网格 → 行卡列表,与任务列表
// 同款形态):左侧标题+摘要,右侧关键值,可点行带箭头弹二级界面;
// 每行固定两行内容,八行天然同高。
function SettingRow({ title, summary, value, valueMono, onOpen }) {
  return (
    <div
      role={onOpen ? "button" : undefined}
      tabIndex={onOpen ? 0 : undefined}
      onClick={onOpen}
      onKeyDown={
        onOpen
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onOpen();
              }
            }
          : undefined
      }
      className={cn(
        "flex min-h-14 items-center gap-4 rounded-xl border border-border/55 bg-card px-4 py-3 transition-colors duration-150",
        onOpen && "cursor-pointer hover:bg-hover-lift"
      )}
    >
      <div className="min-w-0 flex-1">
        <div className="t-sec">{title}</div>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">{summary}</p>
      </div>
      {value != null && value !== "" && (
        <span
          className={cn(
            "max-w-72 shrink-0 truncate text-[13px] text-muted-foreground",
            valueMono && "num"
          )}
          title={String(value)}
        >
          {value}
        </span>
      )}
      {onOpen &&
        React.createElement(iconOf("chevronRight"), {
          className: "size-4 shrink-0 text-muted-foreground",
        })}
    </div>
  );
}

// SystemParamsRow 系统参数行:卡面露出任务并发数,路径细节进弹窗;
// 监听/路径只读,任务并发可改(P4-34)。
function SystemParamsRow({ sys }) {
  const [open, setOpen] = React.useState(false);
  // override:保存响应的即时视图(远端轮询尚未带回新值时覆盖展示,
  // 轮询追上后自然回落)。
  const [override, setOverride] = React.useState(null);
  const conc = override ?? sys?.task_concurrency ?? null;
  React.useEffect(() => {
    setOverride(null);
  }, [sys?.task_concurrency?.value, sys?.task_concurrency?.overridden]);

  return (
    <>
      <SettingRow
        title="系统参数"
        summary="监听地址与路径只读,任务并发可改"
        value={conc ? `并发 ${conc.value}` : "—"}
        valueMono
        onOpen={() => setOpen(true)}
      />
      <SystemParamsDialog
        open={open}
        onOpenChange={setOpen}
        sys={sys}
        conc={conc}
        onSaved={setOverride}
      />
    </>
  );
}

// SystemParamsDialog 系统参数弹窗:监听地址/账本路径/任务目录三行只读,
// 第四行任务并发可编辑(P4-34)——整数 1~16,越界人话拒改;清空保存回
// 默认 2;调大保存有空位即拉起排队任务。
function SystemParamsDialog({ open, onOpenChange, sys, conc, onSaved }) {
  const [input, setInput] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  // 打开时以远端生效值打底(已配置显数值、未配置留空提示默认);关闭
  // 即弃本地态。
  React.useEffect(() => {
    if (open) setInput(conc?.overridden ? String(conc.value) : "");
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const dirty = (() => {
    const cur = conc?.overridden ? String(conc.value) : "";
    return input.trim() !== cur;
  })();

  const submit = async () => {
    const v = input.trim();
    if (v !== "") {
      const n = Number(v);
      if (!Number.isInteger(n) || n < 1 || n > 16) {
        toast.error("任务并发须为 1–16 的整数,清空恢复默认 2");
        return;
      }
    }
    setBusy(true);
    const { ok, body } = await postJSON("/api/config/system", {
      task_concurrency: v,
    });
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    onSaved(body?.system?.task_concurrency ?? null);
    onOpenChange(false);
    toast.success("已保存,对下一轮调度生效");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(520px,94vw)] max-w-none gap-4 p-6">
        <DialogHeader>
          <DialogTitle>系统参数</DialogTitle>
          <DialogDescription>监听地址与路径为启动参数,只读</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Kv k="监听地址" v={sys?.addr || "—"} mono />
          <Kv k="账本路径" v={sys?.db_path || "—"} mono />
          <Kv k="任务目录" v={sys?.tasks_dir || "—"} mono />
          <Field label="任务并发">
            <Input
              className={NUM}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={conc?.overridden ? "清空恢复默认 2" : "默认 2"}
            />
            <span className="text-xs text-muted-foreground">
              同时运行的任务数，超出的排队
            </span>
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            关闭
          </Button>
          <Button onClick={submit} disabled={busy || !dirty}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Kv 详情信息行(系统参数弹窗等):标签 13px 次级色定宽,值 13px 正文档
//(mono 数据位等宽),与 Field 表单行同档。
function Kv({ k, v, mono }) {
  return (
    <div className="flex items-center gap-3">
      <span className="w-20 shrink-0 text-[13px] text-muted-foreground">{k}</span>
      <span className={cn("min-w-0 truncate text-[13px]", mono && "num")}>{v}</span>
    </div>
  );
}

// ---- 拦截规则(P2-08e 全局内置硬规则) ----

// GUARD_TOOL_SCOPES 适用工具选项(空值=全部工具)。
const GUARD_TOOL_SCOPES = [
  { value: "", label: "全部工具" },
  { value: "bash", label: "bash" },
  { value: "http_probe", label: "http_probe" },
  { value: "port_probe", label: "port_probe" },
  { value: "read", label: "read" },
  { value: "web_search", label: "web_search" },
  { value: "web_fetch", label: "web_fetch" },
];

// GuardRulesCard 概览卡:规则数与启用数;点开弹管理界面。
function GuardRulesCard() {
  const { data, error } = usePolling("/api/guard/rules", 4000);
  const [open, setOpen] = React.useState(false);
  const rules = Array.isArray(data?.rules) ? data.rules : [];
  const enabled = rules.filter((r) => r.enabled).length;

  return (
    <>
      <SettingRow
        title="拦截规则"
        summary="全局命令底线,全部任务生效"
        value={error ? "读取失败" : `${rules.length} 条 · 启用 ${enabled}`}
        onOpen={() => setOpen(true)}
      />
      <GuardRulesDialog open={open} onOpenChange={setOpen} rules={rules} error={error} />
    </>
  );
}

// GuardRulesDialog 拦截规则管理界面:分组列表 + 行卡启停/编辑/删除 + 新增;
// 代理旁路组默认禁用(P4-21),组头带一键启用。
function GuardRulesDialog({ open, onOpenChange, rules, error }) {
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState(null); // null=新建
  const [deleting, setDeleting] = React.useState(null);
  const [enabling, setEnabling] = React.useState(false);

  // 组序:词表组在前,库内新组(服务端扩词表)按字母序续后。
  const seen = new Map();
  for (const r of rules) seen.set(r.group, (seen.get(r.group) || 0) + 1);
  const groups = [
    ...HARD_RULE_GROUPS.filter((g) => seen.has(g)),
    ...[...seen.keys()].filter((g) => !HARD_RULE_GROUPS.includes(g)).sort(),
  ];

  // 一键启用代理旁路组:把组内禁用的条目逐条启用(严审计部署自行开启)。
  const enableProxyGroup = async () => {
    const disabled = rules.filter((r) => r.group === "代理旁路" && !r.enabled);
    if (disabled.length === 0) return;
    setEnabling(true);
    let n = 0;
    for (const rule of disabled) {
      const { ok, body } = await postJSON("/api/guard/rules", {
        id: rule.id,
        group: rule.group,
        name: rule.name,
        pattern: rule.pattern,
        tool_scope: rule.tool_scope || "",
        action: rule.action || "",
        enabled: true,
        note: rule.note || "",
      });
      if (!ok) {
        toast.error(`启用失败:${body.error || "原因未说明"}`);
        setEnabling(false);
        return;
      }
      n++;
    }
    setEnabling(false);
    toast.success(`已启用 ${n} 条,对下一次判定生效`);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] w-[min(760px,94vw)] max-w-none flex-col gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>拦截规则</DialogTitle>
          <DialogDescription>
            全局命令底线,全部任务生效;拒绝即终止该单位,需批准的等人工审批
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-end">
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            新增规则
          </Button>
        </div>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {rules.length === 0 && !error && (
          <div className="rounded-lg border border-dashed py-6 text-center text-[13px] text-muted-foreground">
            无规则
          </div>
        )}
        {groups.map((g) => (
          <div key={g} className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-medium text-muted-foreground">{g}</span>
              {g === "外泄管道" && (
                <span className="text-xs text-muted-foreground">
                  默认禁用:反弹 shell 与数据回传是渗透常用动作,需要时手动启用
                </span>
              )}
              {g === "代理旁路" && (
                <>
                  <span className="text-xs text-muted-foreground">
                    默认禁用:直连命令直接执行,流量不经镜
                  </span>
                  {rules.some((r) => r.group === g && !r.enabled) && (
                    <Button
                      size="sm"
                      variant="outline"
                      className="ml-auto h-8 px-2 text-xs"
                      disabled={enabling}
                      onClick={enableProxyGroup}
                    >
                      一键启用
                    </Button>
                  )}
                </>
              )}
            </div>
            <div className="flex flex-col gap-2">
              {rules
                .filter((r) => r.group === g)
                .map((r) => (
                  <GuardRuleRow
                    key={r.id}
                    rule={r}
                    onEdit={() => {
                      setEditing(r);
                      setDialogOpen(true);
                    }}
                    onDelete={() => setDeleting(r)}
                  />
                ))}
            </div>
          </div>
        ))}
        <GuardRuleDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          rule={editing}
        />
        <ConfirmDeleteDialog rule={deleting} onClose={() => setDeleting(null)} />
      </DialogContent>
    </Dialog>
  );
}

function GuardRuleRow({ rule, onEdit, onDelete }) {
  // P4-47 B ① 乐观更新:启停点下即翻、后台提交、失败回弹+人话提示;
  // 本地态在服务端轮询跟上后自动交还。
  const [local, setLocal] = React.useState(null); // null = 跟随服务端
  React.useEffect(() => {
    setLocal((cur) => (cur !== null && rule.enabled === cur ? null : cur));
  }, [rule.enabled]);

  const toggle = async (next) => {
    setLocal(next);
    let ok = false;
    let body = null;
    // 网络/服务端失败统一走回弹(postJSON 在网络不通时抛异常,一并兜住)。
    try {
      ({ ok, body } = await postJSON("/api/guard/rules", {
        id: rule.id,
        group: rule.group,
        name: rule.name,
        pattern: rule.pattern,
        tool_scope: rule.tool_scope || "",
        action: rule.action || "",
        enabled: next,
        note: rule.note || "",
      }));
    } catch {
      ok = false;
    }
    if (!ok) {
      // 失败回弹:交还服务端现值(本次点击未被改动)。
      setLocal(null);
      toast.error(`未生效:${body?.error || "网络未连通"},已还原勾选`);
      return;
    }
    toast.success(next ? "已启用,对下一次判定生效" : "已禁用,对下一次判定生效");
  };

  const enabled = local ?? rule.enabled;
  const builtin = rule.name.startsWith("[内置]");
  return (
    <div
      className={`flex items-center gap-2 rounded-lg border border-border/55 bg-card px-3 py-2 text-[13px] transition-colors duration-150 hover:bg-hover-lift ${
        enabled ? "" : "opacity-60"
      }`}
    >
      <Checkbox
        checked={enabled}
        onCheckedChange={(v) => toggle(Boolean(v))}
        title={enabled ? "启用中" : "已禁用"}
      />
      {builtin && <Badge variant="outline">内置</Badge>}
      {rule.action === "gate" && <Badge variant="warning">需批准</Badge>}
      <span className="min-w-0 truncate font-medium" title={rule.name}>
        {builtin ? rule.name.replace(/^\[内置\]\s*/, "") : rule.name}
      </span>
      <span className="ml-auto hidden truncate num text-xs text-muted-foreground md:block md:max-w-56" title={rule.pattern}>
        {rule.pattern}
      </span>
      <span className="shrink-0 text-xs text-muted-foreground">
        {rule.tool_scope || "全部工具"}
      </span>
      <Button size="sm" variant="ghost" className="h-8 px-2 text-xs" onClick={onEdit}>
        编辑
      </Button>
      <Button
        size="sm"
        variant="ghost"
        className="h-8 px-2 text-xs text-status-danger"
        onClick={onDelete}
      >
        删除
      </Button>
    </div>
  );
}

function GuardRuleDialog({ open, onOpenChange, rule }) {
  const isBuiltin = !!rule?.id && rule.name?.startsWith("[内置]");
  const [form, setForm] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [advanced, setAdvanced] = React.useState(false);

  React.useEffect(() => {
    if (open) {
      setForm({
        name: rule ? rule.name.replace(/^\[内置\]\s*/, "") : "",
        group: rule?.group || HARD_RULE_GROUPS[0],
        pattern: rule?.pattern || "",
        tool_scope: rule?.tool_scope || "",
        action: rule?.action || "",
        note: rule?.note || "",
      });
      setAdvanced(false);
    } else {
      setForm(null);
    }
  }, [open, rule]);

  // dirty:编辑时与原值比对;新增时名称与匹配模式填齐才可保存。
  const dirty = (() => {
    if (!form) return false;
    if (!rule) return form.name.trim() !== "" && form.pattern.trim() !== "";
    return (
      form.name !== rule.name.replace(/^\[内置\]\s*/, "") ||
      form.pattern !== rule.pattern ||
      form.group !== rule.group ||
      (form.tool_scope || "") !== (rule.tool_scope || "") ||
      (form.action || "") !== (rule.action || "") ||
      (form.note || "") !== (rule.note || "")
    );
  })();

  const submit = async () => {
    setBusy(true);
    const payload = {
      id: rule?.id || "",
      group: form.group,
      // 内置条目保存时补回前缀,与库内名称保持一致(服务端校验冒名)。
      name: isBuiltin ? `[内置] ${form.name}` : form.name,
      pattern: form.pattern,
      tool_scope: form.tool_scope,
      action: form.action || "",
      // 启停由行卡开关承担,弹窗透传原值(新增默认启用)。
      enabled: rule ? rule.enabled : true,
      note: form.note,
    };
    const { ok, body } = await postJSON("/api/guard/rules", payload);
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success("已保存,对下一次判定生效");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] w-[min(620px,94vw)] max-w-none gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>
            {isBuiltin ? "编辑内置规则" : rule ? "编辑规则" : "新增规则"}
          </DialogTitle>
          <DialogDescription>
            对工具参数文本做匹配;拒绝即终止该单位,需批准的等人工审批
          </DialogDescription>
        </DialogHeader>
        {!form ? (
          <div className="text-sm text-muted-foreground">读取中…</div>
        ) : (
          <div className="flex flex-col gap-4">
            <Field label="名称">
              <Input
                className={INPUT}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="如:清理临时目录"
              />
            </Field>
            <Field label="匹配模式" className="items-start" labelClassName="mt-2">
              <Textarea
                rows={3}
                className="min-w-0 flex-1 resize-none bg-input-surface num text-xs"
                value={form.pattern}
                onChange={(e) => setForm({ ...form, pattern: e.target.value })}
                placeholder={"匹配参数文本,如 \\bshred\\b"}
              />
            </Field>

            {/* 高级选项折叠:横线 — 箭头 — 文字 — 横线 */}
            <Fold open={advanced} onToggle={() => setAdvanced((v) => !v)}>
              高级选项
            </Fold>
            {advanced && (
              <div className="flex animate-rise flex-col gap-4">
                <Field label="组">
                  <Select
                    value={form.group}
                    onValueChange={(v) => setForm({ ...form, group: v })}
                  >
                    <SelectTrigger className={INPUT}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {HARD_RULE_GROUPS.map((g) => (
                        <SelectItem key={g} value={g}>
                          {g}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="适用工具">
                  <Select
                    value={form.tool_scope}
                    onValueChange={(v) => setForm({ ...form, tool_scope: v })}
                  >
                    <SelectTrigger className={INPUT}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {GUARD_TOOL_SCOPES.map((s) => (
                        <SelectItem key={s.value} value={s.value}>
                          {s.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="处置">
                  <Select
                    value={form.action || "deny"}
                    onValueChange={(v) => setForm({ ...form, action: v === "gate" ? "gate" : "" })}
                  >
                    <SelectTrigger className={cn(INPUT, "w-40")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">拒绝</SelectItem>
                      <SelectItem value="gate">需批准</SelectItem>
                    </SelectContent>
                  </Select>
                  <span className="text-xs text-muted-foreground">
                    需批准:命中提交面板审批,批准后执行原文命令
                  </span>
                </Field>
                <Field label="说明">
                  <Input
                    className={INPUT}
                    value={form.note}
                    onChange={(e) => setForm({ ...form, note: e.target.value })}
                    placeholder="可选"
                  />
                </Field>
              </div>
            )}
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !dirty || !form?.pattern?.trim()}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConfirmDeleteDialog({ rule, onClose }) {
  const [busy, setBusy] = React.useState(false);
  const open = !!rule;

  const remove = async () => {
    setBusy(true);
    const r = await fetch(`/api/guard/rules/${rule.id}`, { method: "DELETE" });
    const body = await r.json().catch(() => ({}));
    setBusy(false);
    if (!r.ok) {
      toast.error(`删除失败:${body.error || "原因未说明"}`);
      return;
    }
    onClose();
    toast.success("已删除,对下一次判定生效");
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>删除规则</DialogTitle>
          <DialogDescription>
            删除「{rule?.name?.replace(/^\[内置\]\s*/, "")}」后不再拦截;内置规则删除后重启不恢复。
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button variant="destructive" onClick={remove} disabled={busy}>
            确认删除
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 搜索配置(P3-03,web_search 工具的后端与出口) ----

// SEARCH_BACKENDS 后端选项(值与后端设置词汇一致)。
const SEARCH_BACKENDS = [
  { value: "ddgs", label: "DuckDuckGo(免 key)" },
  { value: "brave", label: "Brave(免费 key)" },
  { value: "tavily", label: "Tavily(付费 key)" },
];

function backendLabel(v) {
  return SEARCH_BACKENDS.find((b) => b.value === v)?.label || v || "—";
}

// SearchConfigCard 概览卡:后端与 key/代理状态;点开弹配置表单。
function SearchConfigCard({ search }) {
  const [open, setOpen] = React.useState(false);
  // override:保存响应的即时视图(远端轮询尚未带回新值时覆盖展示,
  // 轮询追上后自然回落)。
  const [override, setOverride] = React.useState(null);
  const view = override ?? search;
  React.useEffect(() => {
    setOverride(null);
  }, [search?.backend, search?.brave_has_key, search?.tavily_has_key, search?.proxy_set]);

  return (
    <>
      <SettingRow
        title="搜索配置"
        summary="web_search 工具的搜索后端、key 与独立出口代理"
        value={backendLabel(view?.backend)}
        onOpen={() => setOpen(true)}
      />
      <SearchConfigDialog open={open} onOpenChange={setOpen} view={view} onSaved={setOverride} />
    </>
  );
}

// SearchConfigDialog 搜索配置表单:key 三态(未提交=不改、空串=清除、
// 非空=设置);保存后关闭,卡面经 override 即时刷新。
function SearchConfigDialog({ open, onOpenChange, view, onSaved }) {
  const [backend, setBackend] = React.useState("ddgs");
  const [keys, setKeys] = React.useState({ brave: "", tavily: "" });
  const [proxy, setProxy] = React.useState("");
  // 标记清除:key/代理已配置时可标记清除(保存时提交空串);再点取消。
  const [marked, setMarked] = React.useState({ brave: false, tavily: false, proxy: false });
  const [busy, setBusy] = React.useState(false);

  // 打开时以远端生效值打底;关闭即弃本地态(下次打开重新同步)。
  React.useEffect(() => {
    if (open) {
      setBackend(view?.backend || "ddgs");
      setKeys({ brave: "", tavily: "" });
      setProxy("");
      setMarked({ brave: false, tavily: false, proxy: false });
    }
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const keyField = (which, label, mask, hasKey) => (
    <Field label={label}>
      <Input
        type="password"
        autoComplete="off"
        className={`min-w-0 flex-1 num text-xs ${INPUT}`}
        value={marked[which] ? "" : keys[which]}
        disabled={marked[which]}
        onChange={(e) => setKeys({ ...keys, [which]: e.target.value })}
        placeholder={
          marked[which]
            ? "保存后清除"
            : hasKey
              ? `已配置 ${mask}`
              : which === "brave"
                ? "免费 key"
                : "付费 key"
        }
      />
      {hasKey && (
        <Button
          size="sm"
          variant={marked[which] ? "secondary" : "ghost"}
          className="h-8 px-2 text-xs"
          onClick={() => setMarked({ ...marked, [which]: !marked[which] })}
        >
          {marked[which] ? "取消清除" : "清除"}
        </Button>
      )}
    </Field>
  );

  const submit = async () => {
    setBusy(true);
    const payload = { backend };
    for (const which of ["brave", "tavily"]) {
      if (marked[which]) payload[`${which}_key`] = "";
      else if (keys[which].trim()) payload[`${which}_key`] = keys[which].trim();
    }
    if (marked.proxy) payload.proxy = "";
    else if (proxy.trim()) payload.proxy = proxy.trim();
    const { ok, body } = await postJSON("/api/config/search", payload);
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    onSaved(body);
    onOpenChange(false);
    toast.success("已保存,对下一次搜索生效");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(480px,94vw)] max-w-none gap-4 p-6">
        <DialogHeader>
          <DialogTitle>搜索配置</DialogTitle>
          <DialogDescription>
            web_search 工具的后端与出口;保存后对下一次搜索生效
          </DialogDescription>
        </DialogHeader>
        <Field label="后端">
          <Select value={backend} onValueChange={setBackend}>
            <SelectTrigger className={INPUT}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SEARCH_BACKENDS.map((b) => (
                <SelectItem key={b.value} value={b.value}>
                  {b.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {keyField("brave", "Brave key", view?.brave_key_mask, view?.brave_has_key)}
        {keyField("tavily", "Tavily key", view?.tavily_key_mask, view?.tavily_has_key)}
        <Field label="出口代理">
          <Input
            className={`min-w-0 flex-1 num text-xs ${INPUT}`}
            value={marked.proxy ? "" : proxy}
            disabled={marked.proxy}
            onChange={(e) => setProxy(e.target.value)}
            placeholder={
              marked.proxy
                ? "保存后清除(直连)"
                : view?.proxy_set
                  ? view.proxy_mask
                  : "http://127.0.0.1:7890(可选,搜索流量专用)"
            }
          />
          {view?.proxy_set && (
            <Button
              size="sm"
              variant={marked.proxy ? "secondary" : "ghost"}
              className="h-8 px-2 text-xs"
              onClick={() => setMarked({ ...marked, proxy: !marked.proxy })}
            >
              {marked.proxy ? "取消清除" : "清除"}
            </Button>
          )}
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- UA 策略(P3-13c,http_probe / web_fetch 的 User-Agent) ----

// UA_MODES 模式选项(值与设置词汇一致)。
const UA_MODES = [
  { value: "random", label: "随机浏览器(默认)" },
  { value: "fixed", label: "固定自定义" },
  { value: "tools", label: "模拟常见工具" },
];

function uaModeLabel(v) {
  return UA_MODES.find((m) => m.value === v)?.label || v || "—";
}

// UAConfigCard 概览卡:模式与固定值;点开弹配置表单。
function UAConfigCard({ ua }) {
  const [open, setOpen] = React.useState(false);
  // override:保存响应的即时视图(轮询追上后自然回落)。
  const [override, setOverride] = React.useState(null);
  const view = override ?? ua;
  React.useEffect(() => {
    setOverride(null);
  }, [ua?.mode, ua?.fixed]);

  return (
    <>
      <SettingRow
        title="UA 策略"
        summary="http_probe 与 web_fetch 请求的 User-Agent"
        value={uaModeLabel(view?.mode)}
        onOpen={() => setOpen(true)}
      />
      <UAConfigDialog open={open} onOpenChange={setOpen} view={view} onSaved={setOverride} />
    </>
  );
}

// UAConfigDialog 配置表单:模式下拉 + 固定值输入(fixed 模式必填);
// 保存后关闭,卡面经 override 即时刷新。
function UAConfigDialog({ open, onOpenChange, view, onSaved }) {
  const [mode, setMode] = React.useState("random");
  const [fixed, setFixed] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  // 打开时以远端生效值打底;关闭即弃本地态。
  React.useEffect(() => {
    if (open) {
      setMode(view?.mode || "random");
      setFixed(view?.mode === "fixed" ? view?.fixed || "" : "");
    }
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const submit = async () => {
    const v = fixed.trim();
    if (mode === "fixed" && !v) {
      toast.error("固定自定义模式须填写 User-Agent 原文");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON("/api/config/ua", { mode, fixed: mode === "fixed" ? v : "" });
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    onSaved(body);
    onOpenChange(false);
    toast.success("已保存,对下一次请求生效");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(480px,94vw)] max-w-none gap-4 p-6">
        <DialogHeader>
          <DialogTitle>UA 策略</DialogTitle>
          <DialogDescription>
            http_probe 与 web_fetch 请求的 User-Agent;保存后对下一次请求生效
          </DialogDescription>
        </DialogHeader>
        <Field label="模式">
          <Select value={mode} onValueChange={setMode}>
            <SelectTrigger className={INPUT}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {UA_MODES.map((m) => (
                <SelectItem key={m.value} value={m.value}>
                  {m.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {mode === "fixed" && (
          <Field label="固定值">
            <Input
              className={`min-w-0 flex-1 num text-xs ${INPUT}`}
              value={fixed}
              onChange={(e) => setFixed(e.target.value)}
              placeholder="Mozilla/5.0 (自定义 User-Agent 原文)"
            />
          </Field>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 目标凭据(P3-09:授权目标测试凭据的存储与调用) ----

// CredentialsCard 概览卡:凭据数;点开弹管理界面。执行侧调用不经本卡——
// 任务包携带名称与环境变量名,秘密注入 bash 环境。
function CredentialsCard() {
  const { data, error } = usePolling("/api/credentials", 8000);
  const [open, setOpen] = React.useState(false);
  const [override, setOverride] = React.useState(null); // 写后即时视图
  const creds = override ?? (Array.isArray(data?.credentials) ? data.credentials : null);
  // 轮询带回新值后撤销本地覆盖。
  React.useEffect(() => {
    setOverride(null);
  }, [data?.credentials]);

  return (
    <>
      <SettingRow
        title="目标凭据"
        summary="授权目标的测试凭据;执行单位经环境变量调用"
        value={error ? "读取失败" : creds ? `${creds.length} 条` : "…"}
        onOpen={() => setOpen(true)}
      />
      <CredentialsDialog open={open} onOpenChange={setOpen} creds={creds} error={error} onRefresh={setOverride} />
    </>
  );
}

// CredentialsDialog 凭据管理界面:清单(密码打码 + 环境变量名)+ 新增/
// 编辑/删除(嵌套弹窗)。
function CredentialsDialog({ open, onOpenChange, creds, error, onRefresh }) {
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState(null); // null=新增
  const [deleting, setDeleting] = React.useState(null);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] w-[min(680px,94vw)] max-w-none flex-col gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle>目标凭据</DialogTitle>
          <DialogDescription>
            授权目标的测试凭据;秘密只存服务端,执行单位经环境变量引用
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-end">
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            新增凭据
          </Button>
        </div>
        {error && <div className="text-xs text-status-danger">读取失败:{error}</div>}
        {creds && creds.length === 0 && (
          <div className="rounded-lg border border-dashed py-6 text-center text-[13px] text-muted-foreground">
            无凭据
          </div>
        )}
        {creds &&
          creds.map((c) => (
            <div
              key={c.id}
              className="flex flex-wrap items-center gap-2 rounded-lg border border-border/55 bg-card px-3 py-2 text-[13px] transition-colors duration-150 hover:bg-hover-lift"
            >
              <span className="min-w-0 truncate font-medium" title={c.name}>
                {c.name}
              </span>
              {c.username && (
                <span className="truncate text-muted-foreground" title={c.username}>
                  {c.username}
                </span>
              )}
              <span className="shrink-0 text-xs text-muted-foreground">
                {c.has_secret ? `密码 ${c.secret_mask}` : "无密码"}
              </span>
              <code className="ml-auto hidden shrink-0 num text-xs text-muted-foreground md:inline">
                ${c.env_var}
              </code>
              <Button
                size="sm"
                variant="ghost"
                className="h-8 px-2 text-xs"
                onClick={() => {
                  setEditing(c);
                  setDialogOpen(true);
                }}
              >
                编辑
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="h-8 px-2 text-xs text-status-danger"
                onClick={() => setDeleting(c)}
              >
                删除
              </Button>
            </div>
          ))}
        <CredentialDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          cred={editing}
          onSaved={onRefresh}
        />
        <CredentialDeleteDialog
          cred={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={async () => {
            const r = await fetch("/api/credentials");
            if (r.ok) onRefresh((await r.json()).credentials);
          }}
        />
      </DialogContent>
    </Dialog>
  );
}

function CredentialDialog({ open, onOpenChange, cred, onSaved }) {
  const [form, setForm] = React.useState(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (open) {
      setForm({
        name: cred?.name || "",
        username: cred?.username || "",
        secret: "",
        note: cred?.note || "",
      });
    } else {
      setForm(null);
    }
  }, [open, cred]);

  const dirty = form && (cred ? true : form.name.trim() !== "");

  const submit = async () => {
    setBusy(true);
    const payload = {
      id: cred?.id || "",
      name: form.name,
      username: form.username,
      note: form.note,
    };
    if (form.secret.trim()) payload.secret = form.secret.trim();
    const { ok, body } = await postJSON("/api/credentials", payload);
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    onSaved(body.credentials);
    toast.success("已保存,对下一次执行生效");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(480px,94vw)] max-w-none gap-4 p-6">
        <DialogHeader>
          <DialogTitle>{cred ? "编辑凭据" : "新增凭据"}</DialogTitle>
          <DialogDescription>秘密只存服务端;界面不回明文</DialogDescription>
        </DialogHeader>
        {!form ? (
          <div className="text-sm text-muted-foreground">读取中…</div>
        ) : (
          <div className="flex flex-col gap-4">
            <Field label="名称">
              <Input
                className={INPUT}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="如 dvwa-admin(须含字母或数字)"
              />
            </Field>
            <Field label="用户名">
              <Input
                className={INPUT}
                value={form.username}
                onChange={(e) => setForm({ ...form, username: e.target.value })}
                placeholder="可选"
              />
            </Field>
            <Field label="秘密">
              <Input
                type="password"
                autoComplete="new-password"
                className={`min-w-0 flex-1 num text-xs ${INPUT}`}
                value={form.secret}
                onChange={(e) => setForm({ ...form, secret: e.target.value })}
                placeholder={
                  cred?.has_secret ? `已配置 ${cred.secret_mask},留空保留` : "密码或 key"
                }
              />
            </Field>
            <Field label="说明">
              <Input
                className={INPUT}
                value={form.note}
                onChange={(e) => setForm({ ...form, note: e.target.value })}
                placeholder="可选"
              />
            </Field>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !dirty || !form?.name?.trim()}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CredentialDeleteDialog({ cred, onClose, onDeleted }) {
  const [busy, setBusy] = React.useState(false);
  const open = !!cred;

  const remove = async () => {
    setBusy(true);
    const r = await fetch(`/api/credentials/${cred.id}`, { method: "DELETE" });
    const body = await r.json().catch(() => ({}));
    setBusy(false);
    if (!r.ok) {
      toast.error(`删除失败:${body.error || "原因未说明"}`);
      return;
    }
    onClose();
    onDeleted?.();
    toast.success("已删除,对下一次执行生效");
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>删除凭据</DialogTitle>
          <DialogDescription>删除「{cred?.name}」后执行单位不可再调用。</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button variant="destructive" onClick={remove} disabled={busy}>
            确认删除
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 面板密码(P3-09:本机单用户认证) ----

// PanelPasswordCard 概览卡:状态;点开弹修改表单。
function PanelPasswordCard() {
  const [open, setOpen] = React.useState(false);

  return (
    <>
      <SettingRow
        title="面板密码"
        summary="登录面板的密码,本机单用户;修改后其他登录退出"
        value="已设置"
        onOpen={() => setOpen(true)}
      />
      <PanelPasswordDialog open={open} onOpenChange={setOpen} />
    </>
  );
}

function PanelPasswordDialog({ open, onOpenChange }) {
  const [form, setForm] = React.useState({ current: "", next: "", confirm: "" });
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) setForm({ current: "", next: "", confirm: "" });
  }, [open]);

  const submit = async () => {
    if (form.next !== form.confirm) {
      toast.error("两次输入不一致");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON("/api/auth/password", {
      current: form.current,
      next: form.next,
    });
    setBusy(false);
    if (!ok) {
      toast.error(`修改失败:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success("已修改,其他登录已退出");
  };

  const ready = form.current && form.next && form.confirm && form.next === form.confirm;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="w-[min(480px,94vw)] max-w-none gap-4 p-6">
        <DialogHeader>
          <DialogTitle>面板密码</DialogTitle>
          <DialogDescription>本机单用户;修改后其他登录退出</DialogDescription>
        </DialogHeader>
        <Field label="当前密码">
          <Input
            type="password"
            autoComplete="current-password"
            className={INPUT}
            value={form.current}
            onChange={(e) => setForm({ ...form, current: e.target.value })}
          />
        </Field>
        <Field label="新密码">
          <Input
            type="password"
            autoComplete="new-password"
            className={INPUT}
            value={form.next}
            onChange={(e) => setForm({ ...form, next: e.target.value })}
            placeholder="至少 8 位"
          />
        </Field>
        <Field label="再次输入">
          <Input
            type="password"
            autoComplete="new-password"
            className={INPUT}
            value={form.confirm}
            onChange={(e) => setForm({ ...form, confirm: e.target.value })}
          />
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={submit} disabled={busy || !ready}>
            修改密码
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 导出诊断包(P3-13b ④ + P4-32 增强) ----

// LOG_KINDS 日志类型选项(P4-32 ②:按日志行既有类型字样在导出时
// 过滤,写入侧不动;id 与后端 logKinds 一致)。
const LOG_KINDS = [
  ["error", "错误", "失败与异常的日志行"],
  ["blocked", "受阻与恢复", "任务卡住与自动恢复的记录"],
  ["approval", "审批与拦截", "需批准的命令与被拦截的命令"],
  ["units", "单位起落", "虫群单位的开始与结束"],
];

// DiagnosticsCard 诊断包行:一键打包日志 + 版本 + 平台 + 脱敏配置为
// zip;可按任务筛选或全选,日志可按类型过滤,账本库与会话转录按需
// 加勾(P4-32)。日志级别设置已删,日志固定全量记录。
function DiagnosticsCard() {
  const [open, setOpen] = React.useState(false);
  // /api/tasks 返回行数组;deleted 为墓碑标记,不进勾选清单。
  const { data: tasksData } = usePolling("/api/tasks", 15000, { immediate: true });
  const allTasks = Array.isArray(tasksData) ? tasksData : tasksData?.tasks || [];
  const tasks = allTasks.filter((t) => !t.deleted);

  return (
    <>
      <SettingRow
        title="导出诊断包"
        summary="打包日志与配置摘要,发给维护者远程排查"
        onOpen={() => setOpen(true)}
      />
      <DiagnosticsDialog open={open} onOpenChange={setOpen} tasks={tasks} />
    </>
  );
}

// DiagnosticsDialog 诊断包二级界面(P4-32):导出内容勾选(运行日志
// 默认勾,账本库与会话转录按需加勾)+ 日志范围(全部 / 按类型多选)
// + 任务勾选(全选 / 按任务筛选)+ 一键导出。
function DiagnosticsDialog({ open, onOpenChange, tasks }) {
  const [picked, setPicked] = React.useState(null); // null = 全选模式
  const [busy, setBusy] = React.useState(false);
  // 导出内容:运行日志默认勾(P4-32 ③ 默认最小包)。
  const [withLogs, setWithLogs] = React.useState(true);
  const [withDB, setWithDB] = React.useState(false);
  const [withTranscripts, setWithTranscripts] = React.useState(false);
  // 日志范围:all = 全部日志;pick = 按类型多选。
  const [scope, setScope] = React.useState("all");
  const [kinds, setKinds] = React.useState(() => new Set());
  const allPicked = picked === null;
  const pickedCount = allPicked ? tasks.length : picked.size;
  const kindsEmpty = scope === "pick" && kinds.size === 0;

  React.useEffect(() => {
    if (open) {
      setWithLogs(true);
      setWithDB(false);
      setWithTranscripts(false);
      setScope("all");
      setKinds(new Set());
    }
  }, [open]);

  const toggleAll = () => {
    // 全选←→逐选清空;逐选补齐→回全选模式(缺省 tasks 参数即全部)。
    if (allPicked) setPicked(new Set());
    else if (picked.size >= tasks.length) setPicked(null);
    else setPicked(new Set(tasks.map((t) => t.id)));
  };
  const toggleTask = (id) => {
    const next = new Set(allPicked ? tasks.map((t) => t.id) : picked);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    // P4-47 B ②:逐选勾满仍是逐选(显式清单),不自动跳全选模式;
    // 全选模式只由上方「全选」行触发。
    setPicked(next);
  };
  const toggleKind = (id) => {
    const next = new Set(kinds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setKinds(next);
  };

  const exportZip = async () => {
    setBusy(true);
    try {
      const params = [];
      if (!allPicked) params.push(`tasks=${encodeURIComponent([...picked].join(","))}`);
      if (scope === "pick") params.push(`kinds=${encodeURIComponent([...kinds].join(","))}`);
      const content = [
        withLogs && "logs",
        withDB && "db",
        withTranscripts && "transcripts",
      ].filter(Boolean);
      params.push(`content=${content.join(",")}`);
      const name = await downloadExport(
        `/api/diagnostics?${params.join("&")}`,
        "ghostreaper-diagnostics.zip",
      );
      toast.success(`已导出 ${name}`);
      onOpenChange(false);
    } catch (e) {
      toast.error(`导出失败:${e?.message || "网络未连通"}`);
    }
    setBusy(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>导出诊断包</DialogTitle>
          <DialogDescription>
            打包成 zip 发给维护者;默认只含运行日志与任务摘要
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <div className="text-[13px] font-medium">导出内容</div>
            <div className="flex flex-col gap-1 rounded-md border border-border/55 bg-card p-2">
              <label className="flex cursor-pointer items-start gap-2 rounded px-1 py-1 text-[13px]">
                <Checkbox
                  className="mt-0.5"
                  checked={withLogs}
                  onCheckedChange={(v) => setWithLogs(v === true)}
                />
                <span className="min-w-0 flex-1">
                  运行日志
                  <span className="block text-xs text-muted-foreground">
                    系统运行的流水记录,排查基础材料
                  </span>
                </span>
              </label>
              <label className="flex cursor-pointer items-start gap-2 rounded px-1 py-1 text-[13px]">
                <Checkbox
                  className="mt-0.5"
                  checked={withDB}
                  onCheckedChange={(v) => setWithDB(v === true)}
                />
                <span className="min-w-0 flex-1">
                  账本数据库
                  <span className="block text-xs text-muted-foreground">
                    任务与结论的完整数据库,模型 key 与凭据已脱敏
                  </span>
                </span>
              </label>
              <label className="flex cursor-pointer items-start gap-2 rounded px-1 py-1 text-[13px]">
                <Checkbox
                  className="mt-0.5"
                  checked={withTranscripts}
                  onCheckedChange={(v) => setWithTranscripts(v === true)}
                />
                <span className="min-w-0 flex-1">
                  会话转录
                  <span className="block text-xs text-muted-foreground">
                    已勾任务里每个单位的对话原文,体积大
                  </span>
                </span>
              </label>
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2 text-[13px]">
              <span className="font-medium">日志范围</span>
              <span className="text-xs text-muted-foreground">只影响日志部分</span>
            </div>
            <div className="w-44">
              <Select value={scope} onValueChange={setScope}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部日志</SelectItem>
                  <SelectItem value="pick">按类型筛选</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {scope === "pick" && (
              <div className="flex flex-col gap-1 rounded-md border border-border/55 bg-card p-2">
                {LOG_KINDS.map(([v, label, hint]) => (
                  <label
                    key={v}
                    className="flex cursor-pointer items-start gap-2 rounded px-1 py-1 text-[13px]"
                  >
                    <Checkbox
                      className="mt-0.5"
                      checked={kinds.has(v)}
                      onCheckedChange={() => toggleKind(v)}
                    />
                    <span className="min-w-0 flex-1">
                      {label}
                      <span className="block text-xs text-muted-foreground">{hint}</span>
                    </span>
                  </label>
                ))}
              </div>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2 text-[13px]">
              <span className="font-medium">任务范围</span>
              <span className="text-xs text-muted-foreground">
                {allPicked ? `全选 ${tasks.length} 个任务` : `已选 ${pickedCount} 个任务`}
              </span>
            </div>
            <div className="flex max-h-56 flex-col gap-1 overflow-y-auto rounded-md border border-border/55 bg-card p-2">
              <label className="flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-[13px] font-medium">
                <Checkbox
                checked={allPicked || picked.size === tasks.length}
                onCheckedChange={toggleAll}
                />
                全选(全部任务的摘要进包)
              </label>
              {tasks.length === 0 && (
                <span className="px-1 py-1 text-xs text-muted-foreground">暂无任务</span>
              )}
              {tasks.map((t) => (
                <label key={t.id} className="flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-[13px]">
                  <Checkbox
                  checked={allPicked || picked.has(t.id)}
                  onCheckedChange={() => toggleTask(t.id)}
                  />
                  <span className="min-w-0 flex-1 truncate" title={t.title || t.id}>{t.title || t.id}</span>
                  <span className="num text-[10px] text-muted-foreground">{t.id}</span>
                </label>
              ))}
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            敏感信息不进包:模型 key 与凭据打码,流量正文只字不收。
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            关闭
          </Button>
          <Button onClick={exportZip} disabled={busy || pickedCount === 0 || kindsEmpty}>
            {busy ? "导出中…" : "导出"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
