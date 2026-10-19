"use client";

import * as React from "react";
import { toast } from "sonner";

import { downloadExport } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { Field, Fold, INPUT } from "@/components/form-bits";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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

      <div className="grid gap-4 lg:grid-cols-2">
        <SettingCard title="系统参数" subtitle="启动时指定的参数,面板只读">
          <Kv k="监听地址" v={sys?.addr || "—"} mono />
          <Kv k="账本路径" v={sys?.db_path || "—"} mono />
          <Kv k="任务目录" v={sys?.tasks_dir || "—"} mono />
        </SettingCard>

        <SettingCard title="年轮账本" subtitle="账本是唯一事实来源">
          <Kv k="存储" v="SQLite,历史全保留" />
          <Kv k="写入" v="只增不改" />
        </SettingCard>

        <GuardRulesCard />

        <SearchConfigCard search={data?.search} />

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

// SettingCard 概览卡(系统参数/年轮账本为基准形态):onOpen 缺省为纯展示
// 卡;携带 onOpen 时整卡可点(hover 所在层提亮 + 右上箭头),弹出二级界面。
function SettingCard({ title, subtitle, onOpen, children }) {
  return (
    <Card
      onClick={onOpen}
      className={cn(
        "gap-3 transition-colors duration-150",
        onOpen && "cursor-pointer hover:bg-hover-lift"
      )}
    >
      <CardContent className="flex flex-col gap-3 p-6">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="text-[15px] font-semibold">{title}</div>
            <p className="mt-1 text-xs text-muted-foreground">{subtitle}</p>
          </div>
          {onOpen &&
            React.createElement(iconOf("chevronRight"), {
              className: "mt-0.5 size-4 shrink-0 text-muted-foreground",
            })}
        </div>
        {children}
      </CardContent>
    </Card>
  );
}

function Kv({ k, v, mono }) {
  return (
    <div className="flex items-center gap-3">
      <span className="w-20 shrink-0 text-muted-foreground">{k}</span>
      <span className={mono ? "font-mono text-xs" : ""}>{v}</span>
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
      <SettingCard
        title="拦截规则"
        subtitle="全局命令底线,全部任务生效"
        onOpen={() => setOpen(true)}
      >
        {error ? (
          <div className="text-xs text-status-danger">读取失败:{error}</div>
        ) : (
          <>
            <Kv k="规则" v={`${rules.length} 条`} />
            <Kv k="已启用" v={`${enabled} 条`} />
          </>
        )}
      </SettingCard>
      <GuardRulesDialog open={open} onOpenChange={setOpen} rules={rules} error={error} />
    </>
  );
}

// GuardRulesDialog 拦截规则管理界面:分组列表 + 行卡启停/编辑/删除 + 新增。
function GuardRulesDialog({ open, onOpenChange, rules, error }) {
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState(null); // null=新建
  const [deleting, setDeleting] = React.useState(null);

  // 组序:词表组在前,库内新组(服务端扩词表)按字母序续后。
  const seen = new Map();
  for (const r of rules) seen.set(r.group, (seen.get(r.group) || 0) + 1);
  const groups = [
    ...HARD_RULE_GROUPS.filter((g) => seen.has(g)),
    ...[...seen.keys()].filter((g) => !HARD_RULE_GROUPS.includes(g)).sort(),
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] w-[min(760px,94vw)] max-w-none flex-col gap-4 overflow-y-auto p-6">
        <DialogHeader>
          <DialogTitle className="text-base">拦截规则</DialogTitle>
          <DialogDescription>
            全局命令底线,全部任务生效;拒绝即终止该单位,弹门则人工裁决
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-end">
          <Button
            size="sm"
            variant="outline"
            className="h-9"
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
          <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
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
                <span className="text-xs text-muted-foreground">
                  默认启用,命中弹门人工裁决:绕过代理即绕过流量审计
                </span>
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
  const [busy, setBusy] = React.useState(false);

  const toggle = async (enabled) => {
    setBusy(true);
    const { ok, body } = await postJSON("/api/guard/rules", {
      id: rule.id,
      group: rule.group,
      name: rule.name,
      pattern: rule.pattern,
      tool_scope: rule.tool_scope || "",
      action: rule.action || "",
      enabled,
      note: rule.note || "",
    });
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body.error || "原因未说明"}`);
      return;
    }
    toast.success(enabled ? "已启用,对下一次判定生效" : "已禁用,对下一次判定生效");
  };

  const builtin = rule.name.startsWith("[内置]");
  return (
    <div
      className={`flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors duration-150 hover:bg-hover-lift ${
        rule.enabled ? "" : "opacity-60"
      }`}
    >
      <Checkbox
        checked={rule.enabled}
        disabled={busy}
        onCheckedChange={toggle}
        title={rule.enabled ? "启用中" : "已禁用"}
      />
      {builtin && <Badge variant="outline">内置</Badge>}
      {rule.action === "gate" && <Badge variant="warning">弹门</Badge>}
      <span className="min-w-0 truncate font-medium" title={rule.name}>
        {builtin ? rule.name.replace(/^\[内置\]\s*/, "") : rule.name}
      </span>
      <span className="ml-auto hidden truncate font-mono text-xs text-muted-foreground md:block md:max-w-56" title={rule.pattern}>
        {rule.pattern}
      </span>
      <span className="shrink-0 text-xs text-muted-foreground">
        {rule.tool_scope || "全部工具"}
      </span>
      <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={onEdit}>
        编辑
      </Button>
      <Button
        size="sm"
        variant="ghost"
        className="h-7 px-2 text-xs text-status-danger"
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
          <DialogTitle className="text-base">
            {isBuiltin ? "编辑内置规则" : rule ? "编辑规则" : "新增规则"}
          </DialogTitle>
          <DialogDescription>
            对工具参数文本做匹配;拒绝即终止该单位,弹门则人工裁决
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
                className="min-w-0 flex-1 resize-none bg-input-surface font-mono text-xs"
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
                      <SelectItem value="gate">弹门</SelectItem>
                    </SelectContent>
                  </Select>
                  <span className="text-xs text-muted-foreground">
                    弹门:命中提交面板裁决,批准执行原文命令
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
      <SettingCard
        title="搜索配置"
        subtitle="web_search 工具的搜索后端、key 与独立出口代理"
        onOpen={() => setOpen(true)}
      >
        <Kv k="后端" v={backendLabel(view?.backend)} />
        <Kv k="Brave key" v={view?.brave_has_key ? "已配置" : "未配置"} />
        <Kv k="Tavily key" v={view?.tavily_has_key ? "已配置" : "未配置"} />
        <Kv k="出口代理" v={view?.proxy_set ? view.proxy_mask : "直连"} mono={!!view?.proxy_set} />
      </SettingCard>
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
        className={`min-w-0 flex-1 font-mono text-xs ${INPUT}`}
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
          <DialogTitle className="text-base">搜索配置</DialogTitle>
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
            className={`min-w-0 flex-1 font-mono text-xs ${INPUT}`}
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
      <SettingCard
        title="UA 策略"
        subtitle="http_probe 与 web_fetch 请求的 User-Agent"
        onOpen={() => setOpen(true)}
      >
        <Kv k="模式" v={uaModeLabel(view?.mode)} />
        {view?.mode === "fixed" && <Kv k="固定值" v={view?.fixed || "—"} mono />}
      </SettingCard>
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
          <DialogTitle className="text-base">UA 策略</DialogTitle>
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
              className={`min-w-0 flex-1 font-mono text-xs ${INPUT}`}
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
      <SettingCard
        title="目标凭据"
        subtitle="授权目标的测试凭据;执行单位经环境变量调用"
        onOpen={() => setOpen(true)}
      >
        {error ? (
          <div className="text-xs text-status-danger">读取失败:{error}</div>
        ) : (
          <Kv k="凭据" v={creds ? `${creds.length} 条` : "…"} />
        )}
      </SettingCard>
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
          <DialogTitle className="text-base">目标凭据</DialogTitle>
          <DialogDescription>
            授权目标的测试凭据;秘密只存服务端,执行单位经环境变量引用
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-end">
          <Button
            size="sm"
            variant="outline"
            className="h-9"
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
          <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
            无凭据
          </div>
        )}
        {creds &&
          creds.map((c) => (
            <div
              key={c.id}
              className="flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors duration-150 hover:bg-hover-lift"
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
              <code className="ml-auto hidden shrink-0 font-mono text-xs text-muted-foreground md:inline">
                ${c.env_var}
              </code>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs"
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
                className="h-7 px-2 text-xs text-status-danger"
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
          <DialogTitle className="text-base">{cred ? "编辑凭据" : "新增凭据"}</DialogTitle>
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
                className={`min-w-0 flex-1 font-mono text-xs ${INPUT}`}
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
      <SettingCard
        title="面板密码"
        subtitle="登录面板的密码,本机单用户"
        onOpen={() => setOpen(true)}
      >
        <Kv k="状态" v="已设置" />
        <Kv k="修改后" v="其他登录退出" />
      </SettingCard>
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
          <DialogTitle className="text-base">面板密码</DialogTitle>
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

// ---- 导出诊断包(P3-13b ④) ----

// LOG_LEVELS 日志级别选项(P3-13b ③ 级别可配:排查期开 DEBUG)。
const LOG_LEVELS = [
  ["debug", "DEBUG(排查)"],
  ["info", "INFO(常规)"],
  ["warn", "WARN(仅警告)"],
  ["error", "ERROR(仅错误)"],
];

// DiagnosticsCard 诊断包卡:一键打包最近日志 + 版本 + 平台 + 脱敏配置
// 为 zip;可按任务筛选或全选(勾选任务的账本摘要进包)。附日志级别
// 配置(保存即生效)。
function DiagnosticsCard() {
  const [open, setOpen] = React.useState(false);
  const { data: levelData } = usePolling("/api/config/log-level", 15000, { immediate: true });
  // /api/tasks 返回行数组;deleted 为墓碑标记,不进勾选清单。
  const { data: tasksData } = usePolling("/api/tasks", 15000, { immediate: true });
  const allTasks = Array.isArray(tasksData) ? tasksData : tasksData?.tasks || [];
  const level = (levelData?.level || "INFO").toLowerCase();
  const tasks = allTasks.filter((t) => !t.deleted);

  return (
    <>
      <SettingCard title="导出诊断包" subtitle="打包日志与配置摘要,发给维护者远程排查" onOpen={() => setOpen(true)}>
        <Kv k="日志级别" v={level.toUpperCase()} />
        <Kv k="日志位置" v="logs/ 目录,按大小与日期滚动" />
      </SettingCard>
      <DiagnosticsDialog open={open} onOpenChange={setOpen} tasks={tasks} level={level} />
    </>
  );
}

// DiagnosticsDialog 诊断包二级界面:任务勾选(全选 / 按任务筛选)+
// 日志级别选择(保存即生效)+ 一键导出。
function DiagnosticsDialog({ open, onOpenChange, tasks, level }) {
  const [picked, setPicked] = React.useState(null); // null = 全选模式
  const [busy, setBusy] = React.useState(false);
  const [lv, setLv] = React.useState(level);
  const allPicked = picked === null;
  const pickedCount = allPicked ? tasks.length : picked.size;

  React.useEffect(() => {
    if (open) setLv(level);
  }, [open, level]);

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
    setPicked(next.size === tasks.length ? null : next);
  };

  const saveLevel = async (v) => {
    setLv(v);
    const { ok, body } = await postJSON("/api/config/log-level", { level: v });
    if (!ok) {
      toast.error(body?.error || "保存失败");
      return;
    }
    toast.success(`日志级别已设为 ${String(body.level || v).toUpperCase()}`);
  };

  const exportZip = async () => {
    setBusy(true);
    try {
      const qs = allPicked ? "" : `?tasks=${encodeURIComponent([...picked].join(","))}`;
      const name = await downloadExport(`/api/diagnostics${qs}`, "ghostreaper-diagnostics.zip");
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
            包含运行日志、版本与平台、脱敏配置摘要;勾选任务的账本摘要一并打包
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-3">
            <span className="w-20 shrink-0 text-muted-foreground">日志级别</span>
            <div className="w-44">
              <Select value={lv} onValueChange={saveLevel}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LOG_LEVELS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <span className="text-xs text-muted-foreground">保存即生效</span>
          </div>
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground">任务范围</span>
              <span className="text-xs text-muted-foreground">
                {allPicked ? `全选 ${tasks.length} 个任务` : `已选 ${pickedCount} 个任务`}
              </span>
            </div>
            <div className="flex max-h-56 flex-col gap-1 overflow-y-auto rounded-md border bg-card p-2">
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
                  <span className="font-mono text-[10px] text-muted-foreground">{t.id}</span>
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
          <Button onClick={exportZip} disabled={busy || pickedCount === 0}>
            {busy ? "导出中…" : "导出 zip"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
