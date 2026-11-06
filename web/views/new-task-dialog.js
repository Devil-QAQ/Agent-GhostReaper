"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
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
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ASSET_CLASSES, postJSON, taskTypeMeta, TASK_TYPES, usePolling } from "@/lib/api";
import { toast } from "sonner";

const emptyRow = () => ({ cls: "domain", title: "", parent: "" });
const emptyConstraint = () => ({ action: "", disposition: "forbid", quota: "", scope: [], patterns: [], basis: "" });

// 批量粘贴识别(#9):启发式分类,识别结果进资产行。P4-16 起建任务表单
// 资产框即粘贴口,提交前对框内残余文本兜底解析;P4-05 资产页"新增资产"
// 弹窗同款能力(不复制平行实现)。条目按换行与常见分隔符(顿号/逗号/
// 分号/空白)切分——标识(域名/IP/网址)不含这些字符,切分无损。
//   http(s)://…      → 服务
//   IP:端口          → 服务
//   纯 IP            → 主机
//   点分域名         → 子域名
//   其余             → 其他
export function recognizeLines(text) {
  const rows = [];
  for (const raw of text.split(/[\r\n]+/)) {
    for (const piece of raw.split(/[\s、，,;；]+/)) {
      const s = piece.trim();
      if (!s) continue;
      let cls = "other";
      if (/^https?:\/\//i.test(s)) cls = "service";
      else if (/^\d{1,3}(\.\d{1,3}){3}:\d+$/.test(s)) cls = "service";
      else if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) cls = "host";
      else if (/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9-]+)+$/i.test(s)) cls = "subdomain";
      rows.push({ cls, title: s, parent: "" });
    }
  }
  return rows;
}

// 字段标题:小号淡色标签 + 必填标记(问题清单 #29 ③ 视觉层次——提示
// 全部收进框内 placeholder,标题退为淡标签)。
function FieldLabel({ htmlFor, children, required = false }) {
  return (
    <Label htmlFor={htmlFor} className="text-xs font-medium text-muted-foreground">
      {children}
      {required && <span className="ml-0.5 text-status-danger">*</span>}
    </Label>
  );
}

// NewTaskDialog 新建任务(也承载复制草稿:draft 传入时按其预填,操作者
// 确认后才创建——复制 = 同种子/同目标的草稿确认流)。
// P4-16(问题清单 #29)表单按定稿布局:任务名(可选)→ 任务分类(可选,
// 搜索或输入新名称回车创建)→ 描述(可选,贴"粘贴描述一键提取"——LLM
// 从描述回填目标与资产,复用既有提取通道)→ 目标(必填,每行一条)→
// 注意事项(P4-49,可选多行,对做法的软要求)→
// 资产(必填,可批量粘贴,贴"一键优化整理格式"——LLM 只规整不增删,
// 守恒红线由服务端确定性把关)→ 高级选项折叠(最长跑多久 · 拦截规则 ·
// 纠偏步数)→ 按钮沉底。
// P4-40(问题清单 #59)按钮区三键:暂存(全部字段存为服务端草稿并关
// 闭;从草稿行打开继续编辑时暂存=覆盖原草稿不新增)/ 取消(有内容先
// 确认丢弃)/ 创建(成功后对应草稿删除)。taskDraft = 从任务列表草稿行
// 打开的草稿({id, draft});draft = 复制任务预填(两者互斥)。
// draft:{ title, description, rows, goals, category, timeout, anchor }
export function NewTaskDialog({ open, onOpenChange, onCreated, draft, taskDraft, onDraftSaved }) {
  const [title, setTitle] = React.useState("");
  const [description, setDescription] = React.useState("");
  // 任务类型(P4-44):必填二选一,默认渗透;建后不可改。草稿携带,
  // 提取按描述倾向预选(可改)。
  const [taskType, setTaskType] = React.useState("pentest");
  const [goalsText, setGoalsText] = React.useState("");
  // 注意事项(P4-49,问题清单 #70):对做法的软要求,多行每行一条,可选;
  // 与拦截规则分工——方法偏好写这里,绝对红线配拦截规则。随 roe 快照
  // 落库,建后可在 RoE 页改。
  const [notes, setNotes] = React.useState("");
  const [rows, setRows] = React.useState([]);
  const [paste, setPaste] = React.useState("");
  const [timeout, setTimeoutSecs] = React.useState("");
  const [anchor, setAnchor] = React.useState("");
  // 收口提醒任务级初始配置(审核迭代 1):开关默认开;数字留空 = 用全局默认。
  const [closureOn, setClosureOn] = React.useState(true);
  const [closureRounds, setClosureRounds] = React.useState("");
  const [closureMinutes, setClosureMinutes] = React.useState("");
  const [advOpen, setAdvOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  // 任务分类:搜索选择,或输入新名称回车创建(建任务时随种子指派)。
  const [catID, setCatID] = React.useState("");
  const [catText, setCatText] = React.useState("");
  const [catOpen, setCatOpen] = React.useState(false);
  // 从描述提取(P2-08f,描述框即提取源)。
  const [extracting, setExtracting] = React.useState(false);
  const [extractIssues, setExtractIssues] = React.useState([]);
  const [constraints, setConstraints] = React.useState([]);
  // 资产一键优化整理(P4-16,守恒把关在服务端)。
  const [organizing, setOrganizing] = React.useState(false);
  const [organizeIssues, setOrganizeIssues] = React.useState([]);
  // 取消丢弃确认(P4-40:表单有内容时确认"未暂存,确定丢弃?")。
  const [discardAsk, setDiscardAsk] = React.useState(false);

  const { data: catBody } = usePolling("/api/task-categories", 8000, { immediate: open });
  const categories = Array.isArray(catBody?.categories) ? catBody.categories : [];

  // 打开时落初值:任务草稿(P4-40,服务端暂存)、复制草稿或空白。
  React.useEffect(() => {
    if (!open) return;
    const src = taskDraft?.draft;
    setTitle(src?.title ?? draft?.title ?? "");
    setDescription(src?.description ?? draft?.description ?? "");
    setTaskType(src?.task_type === "redteam" ? "redteam" : "pentest");
    const goalsIn = src ? src.goals : Array.isArray(draft?.goals) ? draft.goals.join("\n") : "";
    setGoalsText(typeof goalsIn === "string" ? goalsIn : "");
    setNotes(src?.notes ?? "");
    const rowsIn = src?.rows ?? draft?.rows;
    setRows(
      Array.isArray(rowsIn) && rowsIn.length > 0
        ? rowsIn.map((r) => ({ cls: r.cls || r.class || "domain", title: r.title || "", parent: r.parent || "" }))
        : []
    );
    setPaste(src?.paste ?? "");
    setTimeoutSecs(src?.timeout ?? draft?.timeout ?? "");
    setAnchor(src?.anchor ?? draft?.anchor ?? "");
    const cl = src?.closure;
    setClosureOn(cl ? cl.on !== false : true);
    setClosureRounds(cl?.rounds || "");
    setClosureMinutes(cl?.minutes || "");
    setAdvOpen(false);
    setCatID(src?.category ?? draft?.category ?? "");
    setCatText("");
    setCatOpen(false);
    setExtracting(false);
    setExtractIssues([]);
    setConstraints(
      Array.isArray(src?.constraints)
        ? src.constraints.map((c) => ({
            action: c.action || "",
            disposition: c.disposition || "forbid",
            quota: c.quota || "",
            scope: Array.isArray(c.scope) ? c.scope : [],
            patterns: Array.isArray(c.patterns) ? c.patterns : [],
            basis: c.basis || "",
          }))
        : []
    );
    setOrganizing(false);
    setOrganizeIssues([]);
    setDiscardAsk(false);
  }, [open, draft, taskDraft]);

  // 分类草稿:清单载入后按 ID 呈现名称;清单已拉到而 ID 不在 = 引用
  // 失效(P4-40 ②),如实置空不报错(catBody 未回时不动,防误清)。
  React.useEffect(() => {
    if (!catID || catText) return;
    const hit = categories.find((c) => c.id === catID);
    if (hit) {
      setCatText(hit.name);
      return;
    }
    if (catBody) setCatID("");
  }, [categories, catID, catText, catBody]);

  const setRow = (i, patch) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  const removeRow = (i) => setRows((rs) => rs.filter((_, j) => j !== i));

  // 分类:回车 = 精确命中即选,未命中即创建后选。
  const pickOrCreateCategory = async () => {
    const text = catText.trim();
    if (!text) {
      setCatID("");
      setCatOpen(false);
      return;
    }
    const exact = categories.find((c) => c.name === text);
    if (exact) {
      setCatID(exact.id);
      setCatText(exact.name);
      setCatOpen(false);
      return;
    }
    const { ok, body } = await postJSON("/api/task-categories", { name: text });
    if (!ok) {
      toast.error(`创建分类失败:${body.error || "原因未说明"}`);
      return;
    }
    setCatID(body.id);
    setCatText(body.name);
    setCatOpen(false);
  };

  const filteredCategories = catID
    ? categories
    : categories.filter((c) => c.name.toLowerCase().includes(catText.trim().toLowerCase()));

  // 资产:框内残余文本确定性识别,与行编辑器(权威)合并、按标识去重。
  const assetsOfForm = React.useMemo(() => {
    const known = new Set(rows.map((r) => r.title.trim()).filter(Boolean));
    const fromPaste = recognizeLines(paste).filter((r) => !known.has(r.title));
    return [...rows.filter((r) => r.title.trim() !== ""), ...fromPaste];
  }, [rows, paste]);
  const formIDs = assetsOfForm.map((r) => r.title.trim()).filter(Boolean);

  // 从描述提取:单次模型调用,候选回填表单(可改可删,确认前不落库)。
  const extract = async () => {
    const text = description.trim();
    if (!text) {
      toast.error("先填写描述");
      return;
    }
    setExtracting(true);
    const { ok, body } = await postJSON("/api/tasks/extract", { text });
    setExtracting(false);
    if (!ok) {
      toast.error(`提取失败:${body.error || "原因未说明"}`);
      return;
    }
    let filledTitle = false;
    if (!title.trim() && body.title) {
      setTitle(body.title);
      filledTitle = true;
    }
    // 任务类型倾向预选(P4-44):模型按描述给的 pentest/redteam,仅预选可改。
    if (body.task_type === "pentest" || body.task_type === "redteam") {
      setTaskType(body.task_type);
    }
    // 目标候选回描述旁的目标框(每行一条,去重);判定口径建议不上面板
    //——正式完成条件在 RoE 目标表单锁定。
    const goals = (body.goals || [])
      .map((g) => (typeof g === "string" ? g : g?.title || ""))
      .map((g) => g.trim())
      .filter(Boolean);
    if (goals.length > 0) {
      setGoalsText((t) => {
        const lines = t.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
        const known = new Set(lines);
        return [...lines, ...goals.filter((g) => !known.has(g))].join("\n");
      });
    }
    const knownAssets = new Set(rows.map((r) => r.title.trim()).filter(Boolean));
    const assets = (body.assets || []).filter((a) => a.title && !knownAssets.has(a.title));
    if (assets.length > 0) {
      setRows((rs) => [...rs, ...assets.map((a) => ({ cls: a.class, title: a.title, parent: "" }))]);
    }
    // 注意事项回填(P4-49 ②):语义辨认的候选进注意事项栏,仅预填可改,
    // 已有条目去重合并(与目标框同款)。
    const gotNotes = (body.creator_notes || []).map((n) => (typeof n === "string" ? n.trim() : "")).filter(Boolean);
    if (gotNotes.length > 0) {
      setNotes((t) => {
        const lines = t.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
        const known = new Set(lines);
        return [...lines, ...gotNotes.filter((g) => !known.has(g))].join("\n");
      });
    }
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
    if ((body.constraints || []).length > 0) setAdvOpen(true);
    const parts = [
      filledTitle ? "任务名 1" : "",
      goals.length > 0 ? `目标 ${goals.length}` : "",
      gotNotes.length > 0 ? `注意事项 ${gotNotes.length}` : "",
      assets.length > 0 ? `资产 ${assets.length}` : "",
      (body.constraints || []).length > 0 ? `拦截规则 ${body.constraints.length}` : "",
    ].filter(Boolean);
    toast.success(parts.length > 0 ? `已回填：${parts.join(" · ")},逐条核对` : "描述里没有可提取的内容");
  };

  // 一键优化整理格式:LLM 只规整(合并同义/拆行/归类/去重);服务端
  // 逐条对账原文守恒(新增丢弃、丢失按原文保留),结果回填为资产行。
  const organize = async () => {
    const lines = [
      ...paste.split(/\r?\n/).map((l) => l.trim()),
      ...rows.map((r) => r.title.trim()),
    ].filter(Boolean);
    if (lines.length === 0) {
      toast.error("资产框没有可整理的条目");
      return;
    }
    setOrganizing(true);
    const { ok, body } = await postJSON("/api/tasks/assets-organize", { lines });
    setOrganizing(false);
    if (!ok) {
      toast.error(`整理失败:${body.error || "原因未说明"}`);
      return;
    }
    setRows((body.assets || []).map((a) => ({ cls: a.class, title: a.title, parent: "" })));
    setPaste("");
    setOrganizeIssues(body.issues || []);
    toast.success(`已整理为 ${(body.assets || []).length} 条,逐条核对`);
  };

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

  // 表单是否有内容(P4-40):任何字段非默认即算——取消前据此决定是否
  // 确认丢弃。
  const formHasContent = () =>
    Boolean(
      title.trim() ||
        description.trim() ||
        goalsText.trim() ||
        notes.trim() ||
        paste.trim() ||
        timeout.trim() ||
        anchor.trim() ||
        catID ||
        catText.trim() ||
        taskType !== "pentest" ||
        constraints.some((c) => c.action.trim() || c.basis) ||
        rows.some((r) => r.title.trim() || r.parent.trim()) ||
        !closureOn ||
        closureRounds.trim() ||
        closureMinutes.trim()
    );

  // 关闭请求经此口:用户侧关闭(取消按钮 / ESC / 点遮罩)有内容先确认;
  // 暂存与创建成功的关闭直接走 onOpenChange(不确认)。
  const requestClose = (o) => {
    if (o) return;
    if (formHasContent()) setDiscardAsk(true);
    else onOpenChange(false);
  };

  // 暂存草稿(P4-40 ①):全部字段存为服务端草稿并关闭;从草稿行打开的
  // (taskDraft.id 在)覆盖原草稿不新增。
  const saveDraft = async () => {
    setBusy(true);
    const payload = {
      title: title.trim(),
      category: catID,
      description,
      task_type: taskType,
      goals: goalsText,
      notes,
      rows: rows.map((r) => ({ class: r.cls, title: r.title.trim(), parent: r.parent.trim() })),
      paste,
      timeout,
      anchor,
      closure: { on: closureOn, rounds: closureRounds, minutes: closureMinutes },
      constraints: constraints.map((c) => ({
        action: c.action,
        disposition: c.disposition,
        quota: c.quota,
        scope: c.scope,
        patterns: c.patterns,
        basis: c.basis,
      })),
    };
    const { ok, body } = await postJSON("/api/task-drafts", { id: taskDraft?.id || "", draft: payload });
    setBusy(false);
    if (!ok) {
      toast.error(`暂存失败:${body.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success(payload.title ? `已暂存草稿「${payload.title}」` : "已暂存草稿");
    if (onDraftSaved) onDraftSaved(body.id);
  };

  const create = async () => {
    const goalTitles = goalsText.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
    if (goalTitles.length === 0) {
      toast.error("目标不能为空：写明这个任务要拿到什么，如：拿下管理权限");
      return;
    }
    const assets = assetsOfForm.map((r) => ({ class: r.cls, title: r.title.trim(), parent: r.parent.trim() }));
    if (assets.length === 0) {
      toast.error("资产不能为空：至少填一个域名 / IP / 网址");
      return;
    }
    const t = parseInt(timeout, 10);
    if (timeout.trim() !== "" && (!Number.isFinite(t) || t <= 0)) {
      toast.error("最长跑多久须为正整数秒（留空不限）");
      return;
    }
    const a = parseInt(anchor, 10);
    if (anchor.trim() !== "" && (!Number.isFinite(a) || a < 1 || a > 1000)) {
      toast.error("纠偏步数须为 1~1000（留空取全局默认）");
      return;
    }
    // 收口提醒初始配置(审核迭代 1):数字留空 = 用全局默认。
    const cr = closureRounds.trim() === "" ? 0 : parseInt(closureRounds, 10);
    const cm = closureMinutes.trim() === "" ? 0 : parseInt(closureMinutes, 10);
    if (!Number.isFinite(cr) || cr < 0 || cr > 100) {
      toast.error("收口信号轮数须为 1~100 的整数（留空用全局默认）");
      return;
    }
    if (!Number.isFinite(cm) || cm < 0 || cm > 1440) {
      toast.error("收口信号间隔须为 1~1440 的整数（留空用全局默认）");
      return;
    }
    // 拦截规则校验:动作描述非空;允许+次数须为正整数(不填 = 不限)。
    const rules = [];
    for (const c of constraints) {
      const action = c.action.trim();
      if (!action) {
        toast.error("拦截规则的动作不能为空（删掉不用的规则行）");
        return;
      }
      let quota = 0;
      if (c.quota.trim() !== "") {
        quota = parseInt(c.quota, 10);
        if (!Number.isFinite(quota) || quota < 1) {
          toast.error(`拦截规则「${action}」的次数上限须为正整数（不填 = 不限）`);
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
    const body = { title: title.trim(), task_type: taskType, assets, goals: goalTitles };
    if (description.trim()) body.description = description.trim();
    if (catID) body.category = catID;
    if (Number.isFinite(t) && t > 0) body.timeout_seconds = t;
    if (Number.isFinite(a) && a > 0) body.anchor_every_steps = a;
    // 只在偏离默认时提交(开 + 全留空 = 缺省,不落任务级配置)。
    if (!closureOn || cr > 0 || cm > 0) {
      body.closure = { enabled: closureOn, rounds: cr, minutes: cm };
    }
    // 注意事项随 roe 快照提交(P4-49):有规则或有注意事项才带 roe 对象;
    // 注意事项为空时不带该键(服务端省略 = 保持语义,建任务即无)。
    const notesTrim = notes.trim();
    if (rules.length > 0 || notesTrim !== "") {
      body.roe = { intent_rules: rules };
      if (notesTrim !== "") body.roe.creator_notes = notesTrim;
    }
    // 来源草稿(P4-40):创建成功后服务端删除对应草稿。
    if (taskDraft?.id) body.draft_id = taskDraft.id;
    const { ok, body: resp } = await postJSON("/api/tasks", body);
    setBusy(false);
    if (!ok) {
      toast.error(`被拒:${resp.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    const extra = [
      `目标 ${goalTitles.length} 个`,
      `资产 ${assets.length} 个`,
      rules.length > 0 ? `拦截规则 ${rules.length} 条` : "",
    ].filter(Boolean);
    toast.success(`已建任务 · ${extra.join(",")},主宰已唤醒`);
    if (onCreated) onCreated(resp.id);
  };

  return (
    <Dialog open={open} onOpenChange={requestClose}>
      <DialogContent className="flex max-h-[92vh] max-w-2xl flex-col gap-4 overflow-hidden">
        <DialogHeader>
          <DialogTitle>{draft ? "复制任务" : "新建任务"}</DialogTitle>
          {draft ? (
            <DialogDescription>已按原任务预填，确认后创建新任务。</DialogDescription>
          ) : taskDraft ? (
            <DialogDescription>正在编辑草稿，暂存将覆盖原草稿。</DialogDescription>
          ) : (
            <DialogDescription className="sr-only">填目标与资产创建任务</DialogDescription>
          )}
        </DialogHeader>

        <div className="-mr-1 flex-1 overflow-y-auto rounded-xl pr-1">
          <div className="grid gap-4">
            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-name">任务名</FieldLabel>
              <Input
                id="nt-name"
                placeholder="给任务起个便于识别的名字，如：xx官网渗透"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </div>

            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-category">任务分类</FieldLabel>
              <div className="relative">
                <Input
                  id="nt-category"
                  placeholder="搜索分类，或输入新名称回车创建"
                  value={catText}
                  onChange={(e) => {
                    setCatText(e.target.value);
                    setCatID("");
                    setCatOpen(true);
                  }}
                  onFocus={() => setCatOpen(true)}
                  onBlur={() => {
                    setCatOpen(false);
                    if (catID) {
                      const hit = categories.find((c) => c.id === catID);
                      setCatText(hit ? hit.name : "");
                    } else {
                      setCatText("");
                    }
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      pickOrCreateCategory();
                    }
                    if (e.key === "Escape") {
                      e.stopPropagation();
                      setCatOpen(false);
                    }
                  }}
                />
                {catID && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="absolute top-1/2 right-1 size-6 -translate-y-1/2"
                    title="清除分类"
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => {
                      setCatID("");
                      setCatText("");
                    }}
                  >
                    {React.createElement(iconOf("x"), { className: "size-3.5" })}
                  </Button>
                )}
                {catOpen && (
                  <div className="absolute top-full left-0 z-20 mt-1 w-full rounded-lg border border-border/55 bg-popover p-1 shadow-float">
                    <div className="max-h-44 overflow-y-auto">
                      {catID && (
                        <button
                          type="button"
                          className="flex h-8 w-full items-center gap-2 rounded-md px-3 text-left text-[13px] text-muted-foreground hover:bg-primary/10"
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={() => {
                            setCatID("");
                            setCatText("");
                          }}
                        >
                          不分类
                        </button>
                      )}
                      {filteredCategories.map((c) => (
                        <button
                          key={c.id}
                          type="button"
                          className="flex h-8 w-full items-center gap-2 rounded-md px-3 text-left text-[13px] hover:bg-primary/10 data-[on=true]:text-primary"
                          data-on={c.id === catID}
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={() => {
                            setCatID(c.id);
                            setCatText(c.name);
                            setCatOpen(false);
                          }}
                        >
                          {React.createElement(iconOf("check"), {
                            className: `size-3.5 ${c.id === catID ? "opacity-100" : "opacity-0"}`,
                          })}
                          {c.name}
                        </button>
                      ))}
                      {!catID && catText.trim() && !categories.some((c) => c.name === catText.trim()) && (
                        <button
                          type="button"
                          className="flex h-8 w-full items-center gap-2 rounded-md px-3 text-left text-[13px] text-primary hover:bg-primary/10"
                          onMouseDown={(e) => e.preventDefault()}
                          onClick={pickOrCreateCategory}
                        >
                          {React.createElement(iconOf("plus"), { className: "size-3.5" })}
                          新建分类「{catText.trim()}」
                        </button>
                      )}
                      {categories.length === 0 && !catText.trim() && (
                        <div className="px-2 py-1.5 text-sm text-muted-foreground">输入名称回车创建</div>
                      )}
                    </div>
                  </div>
                )}
              </div>
            </div>

            <div className="grid gap-2">
              <FieldLabel required>任务类型</FieldLabel>
              {/* P4-44:必填二选一,默认渗透,建后不可改;当前项说明一行 */}
              <div className="flex gap-2">
                {Object.entries(TASK_TYPES).map(([v, meta]) => (
                  <button
                    key={v}
                    type="button"
                    className={
                      "h-8 flex-1 rounded-md border px-3 text-[13px] transition-colors cursor-pointer " +
                      (taskType === v
                        ? "border-primary/60 bg-primary/15 font-medium text-foreground"
                        : "border-border text-muted-foreground hover:text-foreground")
                    }
                    onClick={() => setTaskType(v)}
                  >
                    {meta.label}
                  </button>
                ))}
              </div>
              <div className="text-xs text-muted-foreground">{taskTypeMeta(taskType).hint}</div>
            </div>

            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-desc">描述</FieldLabel>
              <Textarea
                id="nt-desc"
                rows={2}
                placeholder="测试对象与背景，如：测试 example.com 这个站点"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
              <div className="flex items-center gap-2">
                <Button type="button" variant="outline" size="sm" className="h-8" onClick={extract} disabled={extracting}>
                  {extracting ? "提取中…" : "粘贴描述一键提取"}
                </Button>
              </div>
              {extractIssues.length > 0 && (
                <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-2 py-1 text-xs text-status-warning">
                  {extractIssues.length} 条提取项有问题被丢弃:{extractIssues.join(";")}
                </div>
              )}
            </div>

            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-goals" required>
                目标
              </FieldLabel>
              <Textarea
                id="nt-goals"
                rows={2}
                placeholder="如：拿下管理权限、获取 xx 数据"
                value={goalsText}
                onChange={(e) => setGoalsText(e.target.value)}
              />
            </div>

            {/* 注意事项(P4-49):目标说"要什么",这里说"怎么做的要求";
                留空 = 无 */}
            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-notes">注意事项</FieldLabel>
              <Textarea
                id="nt-notes"
                rows={2}
                placeholder="如：用小字典，不要用大字典；不要修改任何数据"
                value={notes}
                onChange={(e) => setNotes(e.target.value)}
              />
            </div>

            <div className="grid gap-2">
              <FieldLabel htmlFor="nt-assets" required>
                资产
              </FieldLabel>
              <Textarea
                id="nt-assets"
                rows={2}
                placeholder="如：example.com、192.168.1.10、https://xx.com/login（可批量粘贴）"
                className="num text-xs"
                value={paste}
                onChange={(e) => setPaste(e.target.value)}
              />
              <div className="flex items-center gap-2">
                <Button type="button" variant="outline" size="sm" className="h-8" onClick={organize} disabled={organizing}>
                  {organizing ? "整理中…" : "一键优化整理格式"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-8 px-2 text-xs text-muted-foreground"
                  onClick={() => setRows((rs) => [...rs, emptyRow()])}
                >
                  添加资产
                </Button>
              </div>
              {organizeIssues.length > 0 && (
                <div className="rounded-md border border-status-warning/40 bg-status-warning-soft px-2 py-1 text-xs text-status-warning">
                  {organizeIssues.join(";")}
                </div>
              )}
              {rows.length > 0 && (
                <div className="grid gap-2">
                  {rows.map((row, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <div className="w-28 shrink-0">
                        <Select value={row.cls} onValueChange={(v) => setRow(i, { cls: v })}>
                          <SelectTrigger className="h-8 text-xs">
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
                        className="h-8 num text-xs"
                        placeholder="资产标识(如 http://10.10.0.5:8080)"
                        value={row.title}
                        onChange={(e) => setRow(i, { title: e.target.value })}
                      />
                      <Input
                        list="nt-parent-ids"
                        className="h-8 w-36 shrink-0 num text-xs"
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
              )}
            </div>

            <div className="grid gap-4">
              <button
                type="button"
                className="flex items-center gap-1 self-start text-xs text-muted-foreground transition-colors hover:text-foreground"
                onClick={() => setAdvOpen((v) => !v)}
              >
                {React.createElement(iconOf(advOpen ? "chevronDown" : "chevronRight"), { className: "size-3.5" })}
                高级选项
              </button>
              {advOpen && (
                <div className="grid gap-4 rounded-xl bg-input-surface/60 p-4">
                  <div className="grid gap-2">
                    <FieldLabel htmlFor="nt-timeout">最长跑多久</FieldLabel>
                    <Input
                      id="nt-timeout"
                      inputMode="numeric"
                      placeholder="秒数，留空不限；到点依序收场"
                      value={timeout}
                      onChange={(e) => setTimeoutSecs(e.target.value)}
                    />
                  </div>

                  <div className="grid gap-2">
                    <FieldLabel>拦截规则</FieldLabel>
                    <div className="flex max-h-56 flex-col gap-2 overflow-y-auto rounded-xl pr-1">
                      {constraints.length === 0 && (
                        <div className="text-xs text-muted-foreground">未添加，默认按授权边界与审批门槛执行</div>
                      )}
                      {constraints.map((c, i) => (
                        <div key={i} className="flex flex-col gap-1 rounded-md border border-border/50 p-2">
                          <div className="flex items-center gap-2">
                            <Input
                              className="h-8 flex-1 text-xs"
                              placeholder="动作(如 上传文件 / 数据读取)"
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
                              title="删除此规则"
                              onClick={() => setConstraints((cs) => cs.filter((_, j) => j !== i))}
                            >
                              {React.createElement(iconOf("x"), { className: "size-4" })}
                            </Button>
                          </div>
                          <div className="flex flex-wrap items-center gap-1">
                            <span className="text-xs text-muted-foreground">范围:</span>
                            {formIDs.length === 0 && <span className="text-xs text-muted-foreground">全部资产</span>}
                            {formIDs.map((t2) => (
                              <button
                                key={t2}
                                type="button"
                                className={`rounded-full border px-2 py-0.5 num text-xs ${
                                  c.scope.includes(t2) ? "border-primary/60 bg-primary/15 text-foreground" : "text-muted-foreground"
                                }`}
                                onClick={() => toggleScope(i, t2)}
                              >
                                {t2}
                              </button>
                            ))}
                            {c.scope.filter((t2) => !formIDs.includes(t2)).length > 0 && (
                              <span className="text-xs text-status-warning">
                                {c.scope.filter((t2) => !formIDs.includes(t2)).length} 个范围引用不在资产内,创建时按全部资产处理
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
                      添加拦截规则
                    </Button>
                  </div>

                  <div className="grid gap-2">
                    <FieldLabel htmlFor="nt-anchor">纠偏步数</FieldLabel>
                    <Input
                      id="nt-anchor"
                      inputMode="numeric"
                      title="每 N 次工具调用重注入一次任务锚"
                      placeholder="留空取全局默认"
                      value={anchor}
                      onChange={(e) => setAnchor(e.target.value)}
                    />
                  </div>

                  <div className="grid gap-2">
                    <label
                      className="flex items-center gap-2 text-sm"
                      title="任务连续多轮规划没有任何新发现（新事实、新资产、新发现记录），且距上次新发现超过设定分钟数时，弹窗询问是否收口。两个条件同时满足才提醒；任务照常运行，不会被暂停。"
                    >
                      <Checkbox checked={closureOn} onCheckedChange={(v) => setClosureOn(Boolean(v))} />
                      收口提醒
                    </label>
                    <div className="grid grid-cols-2 gap-2">
                      <Input
                        inputMode="numeric"
                        disabled={!closureOn}
                        title="连续多少轮规划零新发现才算满足条件之一"
                        placeholder="轮数，留空用默认"
                        value={closureRounds}
                        onChange={(e) => setClosureRounds(e.target.value)}
                      />
                      <Input
                        inputMode="numeric"
                        disabled={!closureOn}
                        title="距上次新发现超过多少分钟才算满足条件之一"
                        placeholder="间隔分钟，留空用默认"
                        value={closureMinutes}
                        onChange={(e) => setClosureMinutes(e.target.value)}
                      />
                    </div>
                    <div className="text-xs text-muted-foreground">
                      轮数 = 连续多少轮规划零新发现；间隔 = 距上次新发现多少分钟。留空用全局默认（Agent 页可改），任务运行中可在工作台改。
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => requestClose(false)}>
            取消
          </Button>
          <Button variant="outline" onClick={saveDraft} disabled={busy}>
            暂存
          </Button>
          <Button onClick={create} disabled={busy}>
            {draft ? "创建副本" : "创建任务"}
          </Button>
        </DialogFooter>
      </DialogContent>

      {/* 取消丢弃确认(P4-40 ①):表单有内容时取消先确认 */}
      <Dialog open={discardAsk} onOpenChange={setDiscardAsk}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>丢弃修改？</DialogTitle>
            <DialogDescription>未暂存，确定丢弃？</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDiscardAsk(false)}>
              继续编辑
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setDiscardAsk(false);
                onOpenChange(false);
              }}
            >
              丢弃
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Dialog>
  );
}
