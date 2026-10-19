"use client";

import * as React from "react";

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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ASSET_CLASSES, postJSON, usePolling } from "@/lib/api";
import { toast } from "sonner";

const emptyRow = () => ({ cls: "domain", title: "", parent: "" });
const emptyConstraint = () => ({ action: "", disposition: "forbid", quota: "", scope: [], patterns: [], basis: "" });

// 批量粘贴识别(#9):启发式分类,识别结果追加为资产行(行编辑器是权威,
// 识别后仍可改类/改父/删行)。P4-05 起导出复用——资产页"新增资产"
// 弹窗与建任务种子表单同款能力(不复制平行实现)。
//   http(s)://…      → 服务
//   IP:端口          → 服务
//   纯 IP            → 主机
//   点分域名         → 子域名
//   其余             → 其他
export function recognizeLines(text) {
  const rows = [];
  for (const raw of text.split(/\r?\n/)) {
    const s = raw.trim();
    if (!s) continue;
    let cls = "other";
    if (/^https?:\/\//i.test(s)) cls = "service";
    else if (/^\d{1,3}(\.\d{1,3}){3}:\d+$/.test(s)) cls = "service";
    else if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) cls = "host";
    else if (/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9-]+)+$/i.test(s)) cls = "subdomain";
    rows.push({ cls, title: s, parent: "" });
  }
  return rows;
}

// NewTaskDialog 新建任务(也承载复制草稿:draft 传入时按其预填,操作者
// 确认后才创建——复制 = 同种子/同目标的草稿确认流)。
// P2-08f:新增「从描述提取」——粘贴授权描述,单次模型调用提取目标/
// 约束/资产候选,回填表单逐条人工确认(描述里没有的不编造;确认是落库
// 前的兜底)。
// draft:{ title, description, rows, category, timeout, anchor }
export function NewTaskDialog({ open, onOpenChange, onCreated, draft }) {
  const [title, setTitle] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [rows, setRows] = React.useState([emptyRow()]);
  const [paste, setPaste] = React.useState("");
  const [timeout, setTimeoutSecs] = React.useState("");
  const [anchor, setAnchor] = React.useState("");
  const [category, setCategory] = React.useState("none");
  const [busy, setBusy] = React.useState(false);
  // 从描述提取(P2-08f)。
  const [extractText, setExtractText] = React.useState("");
  const [extracting, setExtracting] = React.useState(false);
  const [extractIssues, setExtractIssues] = React.useState([]);
  const [goals, setGoals] = React.useState([]);
  const [goalDraft, setGoalDraft] = React.useState("");
  const [constraints, setConstraints] = React.useState([]);

  const { data: catBody } = usePolling("/api/task-categories", 8000, { immediate: open });
  const categories = Array.isArray(catBody?.categories) ? catBody.categories : [];

  // 打开时落初值:草稿(复制)或空白。
  React.useEffect(() => {
    if (!open) return;
    setTitle(draft?.title ?? "");
    setDescription(draft?.description ?? "");
    setRows(
      Array.isArray(draft?.rows) && draft.rows.length > 0
        ? draft.rows.map((r) => ({ cls: r.cls || "domain", title: r.title || "", parent: r.parent || "" }))
        : [emptyRow()]
    );
    setPaste("");
    setTimeoutSecs(draft?.timeout ?? "");
    setAnchor(draft?.anchor ?? "");
    setCategory(draft?.category || "none");
    setExtractText("");
    setExtracting(false);
    setExtractIssues([]);
    setGoals([]);
    setGoalDraft("");
    setConstraints([]);
  }, [open, draft]);

  const setRow = (i, patch) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  const removeRow = (i) =>
    setRows((rs) => (rs.length <= 1 ? [emptyRow()] : rs.filter((_, j) => j !== i)));

  const recognize = () => {
    const found = recognizeLines(paste);
    if (found.length === 0) {
      toast.error("未识别出任何资产行(每行一个标识)");
      return;
    }
    setRows((rs) => [...rs.filter((r) => r.title.trim() !== ""), ...found]);
    setPaste("");
    toast.success(`已识别 ${found.length} 行,可在行内修改`);
  };

  // 从描述提取:单次模型调用,候选回填表单(可改可删,确认前不落库)。
  const extract = async () => {
    const text = extractText.trim();
    if (!text) {
      toast.error("先粘贴一段授权描述");
      return;
    }
    setExtracting(true);
    const { ok, body } = await postJSON("/api/tasks/extract", { text });
    setExtracting(false);
    if (!ok) {
      toast.error(`提取失败:${body.error || "原因未说明"}`);
      return;
    }
    if (!title.trim() && body.title) setTitle(body.title);
    const known = new Set(rows.map((r) => r.title.trim()).filter(Boolean));
    const assets = (body.assets || []).filter((a) => a.title && !known.has(a.title));
    if (assets.length > 0) {
      setRows((rs) => [...rs.filter((r) => r.title.trim() !== ""), ...assets.map((a) => ({ cls: a.class, title: a.title, parent: "" }))]);
    }
    // 目标候选带判定口径建议(P3-14b ④):面板展示供核对,创建时只落标题。
    const suggested = (body.goals || [])
      .map((g) => (typeof g === "string" ? { title: g, pred: "" } : { title: g?.title || "", pred: g?.pred || "" }))
      .filter((g) => g.title);
    setGoals((gs) => [...gs, ...suggested.filter((g) => !gs.some((x) => x.title === g.title))]);
    setConstraints((cs) => [
      ...cs,
      ...(body.constraints || []).map((c) => ({
        action: c.action,
        disposition: c.disposition,
        quota: c.quota > 0 ? String(c.quota) : "",
        scope: Array.isArray(c.assets) ? c.assets : [],
        patterns: Array.isArray(c.patterns) ? c.patterns : [],
        basis: c.basis || "",
      })),
    ]);
    setExtractIssues(body.issues || []);
    const n =
      (body.title ? 1 : 0) + (body.assets?.length || 0) + (body.goals?.length || 0) + (body.constraints?.length || 0);
    toast.success(`已回填 ${n} 项,逐条核对后再创建`);
  };

  const formIDs = rows.map((r) => r.title.trim()).filter(Boolean);

  const toggleScope = (i, t) =>
    setConstraints((cs) =>
      cs.map((c, j) =>
        j === i
          ? { ...c, scope: c.scope.includes(t) ? c.scope.filter((x) => x !== t) : [...c.scope, t] }
          : c
      )
    );

  const setConstraint = (i, patch) =>
    setConstraints((cs) => cs.map((c, j) => (j === i ? { ...c, ...patch } : c)));

  const create = async () => {
    if (!title.trim()) {
      toast.error("任务标题不能为空");
      return;
    }
    const assets = rows
      .map((r) => ({ class: r.cls, title: r.title.trim(), parent: r.parent.trim() }))
      .filter((r) => r.title !== "");
    const t = parseInt(timeout, 10);
    if (timeout.trim() !== "" && (!Number.isFinite(t) || t <= 0)) {
      toast.error("整体超时须为正整数秒(留空不限)");
      return;
    }
    const a = parseInt(anchor, 10);
    if (anchor.trim() !== "" && (!Number.isFinite(a) || a < 1 || a > 1000)) {
      toast.error("纠偏间隔须为 1~1000 步(留空取 Agent 页全局默认)");
      return;
    }
    // 约束校验:动作描述非空;允许+次数须为正整数(不填 = 不限)。
    const rules = [];
    for (const c of constraints) {
      const action = c.action.trim();
      if (!action) {
        toast.error("约束的动作描述不能为空(删掉不用的约束行)");
        return;
      }
      let quota = 0;
      if (c.quota.trim() !== "") {
        quota = parseInt(c.quota, 10);
        if (!Number.isFinite(quota) || quota < 1) {
          toast.error(`约束「${action}」的次数上限须为正整数(不填 = 不限)`);
          return;
        }
      }
      rules.push({
        action,
        asset_ids: c.scope.filter((t2) => formIDs.includes(t2)),
        disposition: c.disposition,
        ...(quota > 0 ? { quota } : {}),
        ...(c.patterns.length > 0 ? { patterns: c.patterns } : {}),
        ...(c.basis ? { note: `从授权描述提取:${c.basis}` } : {}),
      });
    }
    setBusy(true);
    const goalTitles = goals.map((g) => (g?.title || "").trim()).filter(Boolean);
    const body = { title: title.trim(), assets, goals: goalTitles };
    if (description.trim()) body.description = description.trim();
    if (category !== "none") body.category = category;
    if (Number.isFinite(t) && t > 0) body.timeout_seconds = t;
    if (Number.isFinite(a) && a > 0) body.anchor_every_steps = a;
    if (rules.length > 0) body.roe = { intent_rules: rules };
    const { ok, body: resp } = await postJSON("/api/tasks", body);
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${resp.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    const extra = [
      assets.length > 0 ? `资产 ${assets.length} 个` : "",
      goalTitles.length > 0 ? `目标 ${goalTitles.length} 个` : "",
      rules.length > 0 ? `约束 ${rules.length} 条` : "",
    ].filter(Boolean);
    toast.success(extra.length > 0 ? `已建任务 · ${extra.join(",")},主宰已唤醒` : "已建任务(无资产种子)");
    if (onCreated) onCreated(resp.id);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{draft ? "复制任务" : "新建任务"}</DialogTitle>
          <DialogDescription>
            {draft
              ? "已按原任务预填种子与描述,确认后创建新任务。"
              : "12 类资产随任务种子入年轮账本;父标识指向表单内另一资产的标识,构成父子层级。"}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-2">
          <Label htmlFor="nt-extract">从描述提取(可选)</Label>
          <Textarea
            id="nt-extract"
            rows={3}
            placeholder={"粘贴授权描述,提取目标 / 约束 / 资产候选,回填后逐条核对:\n例:授权对 10.10.0.5 与 http://10.10.0.6:8080 测试,目标确认登录面暴露情况;不允许上传文件,爆破类操作最多 20 次。"}
            className="text-xs"
            value={extractText}
            onChange={(e) => setExtractText(e.target.value)}
          />
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={extract} disabled={extracting}>
              {extracting ? "提取中…" : "提取"}
            </Button>
            <span className="text-xs text-muted-foreground">
              单次模型调用;描述里没有的不编造,提取结果确认后才落库
            </span>
          </div>
          {extractIssues.length > 0 && (
            <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-2 py-1 text-xs text-status-warning">
              {extractIssues.length} 条候选项有问题被丢弃:{extractIssues.join(";")}
            </div>
          )}
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-title">任务标题</Label>
          <Input
            id="nt-title"
            placeholder="如:某授权目标首轮侦察"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-desc">描述(可选)</Label>
          <Input
            id="nt-desc"
            placeholder="一句话说明任务范围与目的"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        <div className="grid gap-2">
          <Label>目标(可核验的最终目标)</Label>
          <div className="flex max-h-32 flex-col gap-1 overflow-y-auto pr-1">
            {goals.length === 0 && (
              <div className="text-xs text-muted-foreground">无目标种子(可留空,RoE 面板随时补)</div>
            )}
            {goals.map((g, i) => (
              <div key={i} className="flex flex-col gap-0.5">
                <div className="flex items-center gap-2">
                  <span className="flex-1 rounded-md border bg-background px-2 py-1 text-xs">{g.title}</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    title="删除此目标"
                    onClick={() => setGoals((gs) => gs.filter((_, j) => j !== i))}
                  >
                    {React.createElement(iconOf("x"), { className: "size-4" })}
                  </Button>
                </div>
                {g.pred && <div className="pl-1 text-xs text-muted-foreground">判定:{g.pred}</div>}
              </div>
            ))}
          </div>
          <div className="flex items-center gap-2">
            <Input
              className="text-xs"
              placeholder="添加目标(写明怎样算完成,回车确认)"
              value={goalDraft}
              onChange={(e) => setGoalDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && goalDraft.trim()) {
                  e.preventDefault();
                  setGoals((gs) => [...gs, { title: goalDraft.trim(), pred: "" }]);
                  setGoalDraft("");
                }
              }}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                if (goalDraft.trim()) {
                  setGoals((gs) => [...gs, { title: goalDraft.trim(), pred: "" }]);
                  setGoalDraft("");
                }
              }}
            >
              添加
            </Button>
          </div>
        </div>

        <div className="grid gap-2">
          <Label>操作约束(任务级拦截规则)</Label>
          <div className="flex max-h-56 flex-col gap-2 overflow-y-auto pr-1">
            {constraints.length === 0 && (
              <div className="text-xs text-muted-foreground">无约束(默认按授权边界与审批门槛执行)</div>
            )}
            {constraints.map((c, i) => (
              <div key={i} className="flex flex-col gap-1 rounded-md border p-2">
                <div className="flex items-center gap-2">
                  <Input
                    className="h-8 flex-1 text-xs"
                    placeholder="动作描述(如 上传文件 / 数据读取)"
                    value={c.action}
                    onChange={(e) => setConstraint(i, { action: e.target.value })}
                  />
                  <div className="w-24 shrink-0">
                    <Select value={c.disposition} onValueChange={(v) => setConstraint(i, { disposition: v })}>
                      <SelectTrigger className="h-8 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="forbid">禁止</SelectItem>
                        <SelectItem value="allow">允许</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  {c.disposition === "allow" && (
                    <Input
                      className="h-8 w-28 shrink-0 text-xs"
                      inputMode="numeric"
                      placeholder="次数上限"
                      value={c.quota}
                      onChange={(e) => setConstraint(i, { quota: e.target.value })}
                    />
                  )}
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    title="删除此约束"
                    onClick={() => setConstraints((cs) => cs.filter((_, j) => j !== i))}
                  >
                    {React.createElement(iconOf("x"), { className: "size-4" })}
                  </Button>
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  <span className="text-xs text-muted-foreground">范围:</span>
                  {formIDs.length === 0 && <span className="text-xs text-muted-foreground">全部资产(表单内无资产行)</span>}
                  {formIDs.map((t2) => (
                    <button
                      key={t2}
                      type="button"
                      className={`rounded-full border px-2 py-0.5 font-mono text-xs ${
                        c.scope.includes(t2) ? "border-primary/60 bg-primary/15 text-foreground" : "text-muted-foreground"
                      }`}
                      onClick={() => toggleScope(i, t2)}
                    >
                      {t2}
                    </button>
                  ))}
                  {c.scope.filter((t2) => !formIDs.includes(t2)).length > 0 && (
                    <span className="text-xs text-status-warning">
                      {c.scope.filter((t2) => !formIDs.includes(t2)).length} 个范围引用不在资产行内,创建时按全部资产处理
                    </span>
                  )}
                </div>
                {c.basis && <div className="text-xs text-muted-foreground">描述原句:{c.basis}</div>}
              </div>
            ))}
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => setConstraints((cs) => [...cs, emptyConstraint()])}
          >
            添加约束
          </Button>
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-category">分类(可选)</Label>
          <div className="w-56">
            <Select value={category} onValueChange={setCategory}>
              <SelectTrigger id="nt-category">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">不分类</SelectItem>
                {categories.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-timeout">整体超时(秒,可选)</Label>
          <Input
            id="nt-timeout"
            inputMode="numeric"
            placeholder="留空不限;到点依序收场"
            value={timeout}
            onChange={(e) => setTimeoutSecs(e.target.value)}
          />
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-anchor">纠偏间隔(步,可选)</Label>
          <Input
            id="nt-anchor"
            inputMode="numeric"
            placeholder="留空取 Agent 页全局默认;每 N 次工具调用重注入一次任务锚"
            value={anchor}
            onChange={(e) => setAnchor(e.target.value)}
          />
        </div>

        <div className="grid gap-2">
          <Label htmlFor="nt-paste">批量粘贴识别(可选)</Label>
          <Textarea
            id="nt-paste"
            rows={3}
            placeholder={"每行一个标识,自动识别:\nhttp://10.10.0.5:8080 → 服务\n10.10.0.6 → 主机\napp.example.com → 子域名"}
            className="font-mono text-xs"
            value={paste}
            onChange={(e) => setPaste(e.target.value)}
          />
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={recognize}>
              识别
            </Button>
            <span className="text-xs text-muted-foreground">
              URL/带端口 IP → 服务 · 纯 IP → 主机 · 点分域名 → 子域名 · 其余 → 其他
            </span>
          </div>
        </div>

        <div className="grid gap-2">
          <Label>资产种子</Label>
          <div className="flex max-h-64 flex-col gap-2 overflow-y-auto pr-1">
            {rows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <div className="w-32 shrink-0">
                  <Select value={row.cls} onValueChange={(v) => setRow(i, { cls: v })}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ASSET_CLASSES.map(([v, label]) => (
                        <SelectItem key={v} value={v}>
                          {label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <Input
                  className="font-mono text-xs"
                  placeholder="资产标识(如 http://10.10.0.5:8080)"
                  value={row.title}
                  onChange={(e) => setRow(i, { title: e.target.value })}
                />
                <Input
                  list="nt-parent-ids"
                  className="w-40 shrink-0 font-mono text-xs"
                  placeholder="父标识(可选)"
                  value={row.parent}
                  onChange={(e) => setRow(i, { parent: e.target.value })}
                />
                <datalist id="nt-parent-ids">
                  {formIDs.map((id) => (
                    <option key={id} value={id} />
                  ))}
                </datalist>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  title="删除此行"
                  onClick={() => removeRow(i)}
                >
                  {React.createElement(iconOf("x"), { className: "size-4" })}
                </Button>
              </div>
            ))}
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => setRows((rs) => [...rs, emptyRow()])}
          >
            添加资产
          </Button>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={create} disabled={busy}>
            {draft ? "创建副本" : "创建任务"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
