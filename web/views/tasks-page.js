"use client";

import * as React from "react";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { CopyIDButton } from "@/components/copy-id-button";
import { FuseButton } from "@/components/fuse-button";
import { CheckpointButton } from "@/components/checkpoint-button";
import { HibernateButton } from "@/components/hibernate-button";
import { KpiCard } from "@/components/kpi-card";
import { PageHeader } from "@/components/page-header";
import { iconOf } from "@/components/icons";
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
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TASK_STATUS, fetchJSON, postJSON, usePolling, useTaskContext } from "@/lib/api";
import { defaultTaskTab } from "@/lib/nav";
import { cn } from "@/lib/utils";
import { CategoryDialog } from "@/views/category-dialog";
import { NewTaskDialog } from "@/views/new-task-dialog";

const STATUS_FILTERS = [
  ["all", "全部状态"],
  ["started", "运行中"],
  ["settling", "收尾中"],
  ["hibernated", "冬眠中"],
  ["checkpointed", "已存档"],
  ["queued", "排队中"],
  ["created", "已创建"],
  ["paused", "已暂停"],
  ["completed", "已完成"],
  ["stopped", "已熔断"],
];

// 任务列表页完整版(P2-06h,8.3 一级·任务 / 参考图规格):
// 工具栏(搜索(名称/描述)/ 状态·分类筛选 / 计数 / 恢复全部)+
// 行(复选 · 名称与 ID 徽标 · 描述截断 · 目标截断 · 状态 · 目标进度 n/m ·
// 运行中虫群单位数 · 控制组(熔断;冬眠/唤醒属 P2-08)+ 进入/置顶/复制/
// 删除(墓碑))+ 批量操作(二次确认)+ 分类管理。任务创建即入队,
// 无"启动"按钮——恢复语义由唤醒(P2-08)覆盖。
// P2-06e 沿革:头部「恢复全部」归入工具栏;行内受阻徽标。
// P2-06g 沿革:点击名称进入任务空间(选定任务,各标签共享上下文)。
export function TasksPage({ tasks, currentTask }) {
  const { setTaskID, clearIfGone } = useTaskContext();

  // 进入任务空间:选定任务并落到首个点亮标签。
  const enterTask = (id) => {
    setTaskID(id);
    location.hash = `/task/${id}/${defaultTaskTab()}`;
  };

  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [draft, setDraft] = React.useState(null); // 复制草稿(确认后创建)
  const [categoryOpen, setCategoryOpen] = React.useState(false);
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [categoryFilter, setCategoryFilter] = React.useState("all");
  const [textFilter, setTextFilter] = React.useState("");
  const [selected, setSelected] = React.useState(() => new Set());
  const [recovering, setRecovering] = React.useState(false);
  const [deleteTarget, setDeleteTarget] = React.useState(null); // 待确认删除的任务
  const [batch, setBatch] = React.useState(null); // {action:"stop"|"delete"} 待确认

  const { data: catBody } = usePolling("/api/task-categories", 8000);
  const categories = Array.isArray(catBody?.categories) ? catBody.categories : [];

  const recoverAll = async () => {
    setRecovering(true);
    const { ok, body } = await postJSON("/api/tasks/recover-all", {});
    setRecovering(false);
    if (!ok) {
      toast.error(`恢复失败:${body.error || "原因未说明"}`);
      return;
    }
    if ((body.steps || 0) === 0) {
      toast.info("当前没有受阻任务");
      return;
    }
    toast.success(`已重开 ${body.tasks} 个任务的 ${body.steps} 个受阻步骤`);
  };

  // 当前任务侧 KPI 数据(资产/发现/待审批),无当前任务时不轮询。
  const tid = currentTask?.id;
  const { data: assetsBody } = usePolling(tid ? `/api/tasks/${tid}/assets` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
  });
  const { data: claimsBody } = usePolling(tid ? `/api/tasks/${tid}/claims` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
  });
  const { data: approvalsBody } = usePolling(tid ? `/api/tasks/${tid}/approvals` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
    live: tid ? [`approval:${tid}`] : null,
  });

  const assets = tid && assetsBody?.assets ? assetsBody.assets : null;
  const claims = tid && Array.isArray(claimsBody) ? claimsBody : null;
  const pending = tid && Array.isArray(approvalsBody?.pending ?? approvalsBody) ? (approvalsBody?.pending ?? approvalsBody).length : null;
  const confirmed = claims
    ? claims.filter((c) => c.status === "confirmed").length
    : null;

  const running = tasks.filter((t) => t.status === "started").length;
  const blockedCount = tasks.filter((t) => t.blocked).length;
  // 已收尾 = 整体超时收尾落的停止终态(与操作者熔断区分);已完成含
  // 收尾后判定达成者(达成就是完成,不因走收尾流程而换词)。
  const fused = tasks.filter((t) => t.status === "stopped" && !t.settled).length;
  const settled = tasks.filter((t) => t.status === "stopped" && t.settled).length;
  const doneCount = tasks.filter((t) => t.status === "completed").length;

  // 搜索(名称/描述)+ 状态筛选 + 分类筛选;排序沿用服务端(置顶在前)。
  const filtered = tasks.filter((t) => {
    if (statusFilter !== "all" && t.status !== statusFilter) return false;
    if (categoryFilter === "none" && t.category) return false;
    if (categoryFilter !== "all" && categoryFilter !== "none" && t.category !== categoryFilter) return false;
    const q = textFilter.trim().toLowerCase();
    if (q && !`${t.title || ""}${t.description || ""}`.toLowerCase().includes(q)) return false;
    return true;
  });

  const filteredIDs = filtered.map((t) => t.id);
  const allSelected = filteredIDs.length > 0 && filteredIDs.every((id) => selected.has(id));
  const someSelected = filteredIDs.some((id) => selected.has(id)) && !allSelected;
  const selectedIDs = filteredIDs.filter((id) => selected.has(id));
  const selectedTasks = filtered.filter((t) => selected.has(t.id));

  const toggleAll = () => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (allSelected) filteredIDs.forEach((id) => next.delete(id));
      else filteredIDs.forEach((id) => next.add(id));
      return next;
    });
  };

  const toggleOne = (id) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // 置顶 / 取消置顶(task.meta 入账)。
  const togglePin = async (t) => {
    const { ok, body } = await postJSON(`/api/tasks/${t.id}/meta`, { pinned: !t.pinned });
    if (!ok) {
      toast.error(`置顶失败:${body.error || "原因未说明"}`);
      return;
    }
    toast.success(t.pinned ? `已取消置顶:${t.title || t.id}` : `已置顶:${t.title || t.id}`);
  };

  // 复制任务:同种子/同目标的草稿(拉取资产图还原种子行,确认后创建)。
  const copyTask = async (t) => {
    const { ok, body } = await fetchJSON(`/api/tasks/${t.id}/assets`);
    let rows = [];
    if (ok && Array.isArray(body?.assets)) {
      const titleOf = Object.fromEntries(body.assets.map((a) => [a.id, a.title]));
      const parentOf = {};
      for (const e of body.hierarchy || []) parentOf[e.from] = titleOf[e.to] || "";
      rows = body.assets.map((a) => ({ cls: a.class, title: a.title, parent: parentOf[a.id] || "" }));
    }
    setDraft({
      title: `${t.title || t.id} 副本`,
      description: t.description || "",
      rows,
      category: t.category || "",
    });
    setDialogOpen(true);
  };

  // 删除(墓碑):确认后先停(在跑时)再标记,账本历史保留。
  const doDelete = async (t, reason) => {
    const { ok, body } = await fetchJSON(`/api/tasks/${encodeURIComponent(t.id)}`, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason }),
    });
    if (!ok) {
      toast.error(`删除失败:${body.error || "原因未说明"}`);
      return;
    }
    clearIfGone(t.id);
    toast.success(`已删除:${t.title || t.id}(账本历史保留)`);
  };

  // 批量操作(确认由对话框承担)。
  const doBatch = async (action, reason) => {
    const { ok, body } = await postJSON("/api/tasks/batch", {
      ids: selectedIDs,
      action,
      reason,
    });
    if (!ok) {
      toast.error(`批量操作失败:${body.error || "原因未说明"}`);
      return;
    }
    const fails = Array.isArray(body?.failures) ? body.failures : [];
    if (action === "delete") selectedIDs.forEach((id) => clearIfGone(id));
    setSelected(new Set());
    const verb =
      action === "stop" ? "已熔断" : action === "hibernate" ? "已冬眠" : "已删除";
    toast.success(
      `${verb} ${body.done} 个任务${fails.length ? `,${fails.length} 个失败` : ""}`
    );
  };

  const openCreate = () => {
    setDraft(null);
    setDialogOpen(true);
  };

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="任务"
        subtitle="年轮账本内的全部任务 · 主宰拆解派发、虫群单位执行"
      >
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => (location.hash = "/agent")}>
            {React.createElement(iconOf("settings"), { className: "size-4" })}
            并发设置
          </Button>
          <Button variant="outline" onClick={() => setCategoryOpen(true)}>
            {React.createElement(iconOf("tags"), { className: "size-4" })}
            分类管理
          </Button>
          <Button onClick={openCreate}>
            {React.createElement(iconOf("plus"), { className: "size-4" })}
            新建任务
          </Button>
        </div>
      </PageHeader>

      {/* KPI 行(#40:KPI 即导航,副文案承载健康度) */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <KpiCard
          label="任务"
          value={tasks.length}
          hint={`运行中 ${running} · 已完成 ${doneCount} · 已收尾 ${settled} · 已熔断 ${fused}${blockedCount ? ` · 受阻 ${blockedCount}` : ""}`}
        />
        <KpiCard
          label="资产"
          value={tid ? (assets ? assets.length : "…") : "—"}
          hint={tid ? `已覆盖 ${assets ? assets.filter((a) => a.covered).length : "…"}/${assets ? assets.length : "…"}` : "未选择任务"}
          href={tid ? `/task/${tid}/assets` : "/tasks"}
        />
        <KpiCard
          label="Confirmed 发现"
          value={tid ? (claims ? confirmed : "…") : "—"}
          hint={tid ? `共 ${claims ? claims.length : "…"} 条声明` : "未选择任务"}
          href={tid ? `/task/${tid}/findings` : "/tasks"}
          tone={confirmed > 0 ? "success" : undefined}
        />
        <KpiCard
          label="待审批"
          value={tid ? (pending ?? "…") : "—"}
          hint={tid ? "审批弹门待裁决" : "未选择任务"}
          href={tid ? `/task/${tid}/roe` : "/tasks"}
          tone={pending > 0 ? "danger" : undefined}
        />
      </div>

      {/* 工具栏:搜索(名称/描述)+ 状态/分类筛选 + 计数 + 恢复全部(P2-06e 归入) */}
      {tasks.length === 0 ? (
        <EmptyState
          icon="listChecks"
          title="尚无任务"
          hint="新建第一个任务,种子资产入图后主宰开始拆解"
          action={{ label: "新建任务", onClick: openCreate }}
        />
      ) : (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-56">
              {React.createElement(iconOf("search"), {
                className: "absolute top-3 left-2 size-4 text-muted-foreground",
              })}
              <Input
                className="pl-8"
                placeholder="按名称或描述搜索"
                value={textFilter}
                onChange={(e) => setTextFilter(e.target.value)}
              />
            </div>
            <div className="w-32">
              <Select value={statusFilter} onValueChange={setStatusFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATUS_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="w-36">
              <Select value={categoryFilter} onValueChange={setCategoryFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部分类</SelectItem>
                  <SelectItem value="none">未分类</SelectItem>
                  {categories.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Label className="ml-auto text-xs text-muted-foreground tnum">
              {filtered.length}/{tasks.length} 项
            </Label>
            {blockedCount > 0 && (
              <Button variant="outline" disabled={recovering} onClick={recoverAll}>
                {recovering ? "恢复中…" : `恢复全部(${blockedCount})`}
              </Button>
            )}
          </div>

          {/* 批量操作条(复选后出现;均二次确认) */}
          {selectedIDs.length > 0 && (
            <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card px-3 py-2">
              <span className="text-sm">已选 {selectedIDs.length} 项</span>
              <span className="ml-auto flex gap-2">
                <Button size="sm" variant="outline" onClick={() => setBatch({ action: "hibernate" })}>
                  批量冬眠
                </Button>
                <Button size="sm" variant="destructive" onClick={() => setBatch({ action: "stop" })}>
                  批量熔断
                </Button>
                <Button size="sm" variant="destructive" onClick={() => setBatch({ action: "delete" })}>
                  批量删除
                </Button>
                <Button size="sm" variant="outline" onClick={() => setSelected(new Set())}>
                  清除选择
                </Button>
              </span>
            </div>
          )}

          {/* 任务表(行高较高、细分隔线,暗色扁平 8.4 token) */}
          <div className="rounded-lg border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-10">
                    <Checkbox
                      checked={allSelected}
                      indeterminate={someSelected}
                      onCheckedChange={toggleAll}
                      title="全选 / 清除本页选择"
                    />
                  </TableHead>
                  <TableHead>任务</TableHead>
                  <TableHead className="min-w-32">描述</TableHead>
                  <TableHead className="min-w-36">目标</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">进度</TableHead>
                  <TableHead className="text-right">单位</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((t) => {
                  const isCurrent = currentTask?.id === t.id;
                  const terminal = t.status === "stopped" || t.status === "completed";
                  const targets = (t.targets || []).join("、");
                  return (
                    <TableRow
                      key={t.id}
                      className={cn(
                        "group h-16",
                        isCurrent && "border-l-2 border-l-primary bg-primary/5"
                      )}
                    >
                      <TableCell>
                        <Checkbox
                          checked={selected.has(t.id)}
                          onCheckedChange={() => toggleOne(t.id)}
                          title="选择此任务"
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          {t.pinned &&
                            React.createElement(iconOf("star"), {
                              className: "size-3.5 shrink-0 text-status-warning",
                            })}
                          <button
                            className="min-w-0 truncate text-left font-medium underline-offset-4 hover:text-primary hover:underline cursor-pointer"
                            title={t.title || t.id}
                            onClick={() => enterTask(t.id)}
                          >
                            {t.title || t.id}
                          </button>
                          {/* P2-14b①:随机 ID 弱化为悬停可见的复制入口,
                              不再作行内主信息 */}
                          <CopyIDButton id={t.id} className="opacity-0 group-hover:opacity-100" />
                          {isCurrent && (
                            <span className="shrink-0 text-xs text-primary">当前任务</span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="max-w-48" title={t.description || ""}>
                        <span className="block truncate text-muted-foreground">
                          {t.description || "—"}
                        </span>
                      </TableCell>
                      <TableCell className="max-w-44" title={targets || undefined}>
                        <span className="block truncate font-mono text-xs text-muted-foreground">
                          {targets || "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap items-center gap-1">
                          {t.settled && t.status === "stopped" ? (
                            <Badge variant="paused">已收尾</Badge>
                          ) : (
                            <Badge variant={TASK_STATUS[t.status]?.variant || "info"}>
                              {TASK_STATUS[t.status]?.label || t.status}
                            </Badge>
                          )}
                          {t.blocked && (
                            <span title={t.blocked_reason || ""}>
                              <Badge variant="danger">受阻</Badge>
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs">
                        <span title={`目标达成 ${t.goals_achieved ?? 0}/${t.goals_total ?? 0}`}>
                          {t.goals_achieved ?? 0}/{t.goals_total ?? 0}
                        </span>
                      </TableCell>
                      <TableCell className="text-right font-mono text-xs">
                        <span title="运行中虫群单位数">{t.running_units ?? 0}</span>
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                          {/* 任务控制组(P2-08):冬眠/唤醒 + 熔断(终止不可恢复);
                              P3-07:存档断点(运行中可点,收拢后进程可关) */}
                          {!terminal && (
                            <HibernateButton taskID={t.id} taskTitle={t.title || t.id} hibernated={t.status === "hibernated"} />
                          )}
                          {!terminal && t.status !== "checkpointed" && (
                            <CheckpointButton taskID={t.id} taskTitle={t.title || t.id} />
                          )}
                          {!terminal && <FuseButton taskID={t.id} taskTitle={t.title || t.id} />}
                          <Button
                            variant="ghost"
                            size="icon"
                            title="进入任务空间"
                            onClick={() => enterTask(t.id)}
                          >
                            {React.createElement(iconOf("arrowRight"), { className: "size-4" })}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t.pinned ? "取消置顶" : "置顶"}
                            onClick={() => togglePin(t)}
                          >
                            {React.createElement(iconOf(t.pinned ? "pinOff" : "pin"), {
                              className: "size-4",
                            })}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title="复制任务(同种子同目标,草稿确认)"
                            onClick={() => copyTask(t)}
                          >
                            {React.createElement(iconOf("copy"), { className: "size-4" })}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="text-status-danger"
                            title="删除(列表移除,账本历史保留)"
                            onClick={() => setDeleteTarget(t)}
                          >
                            {React.createElement(iconOf("trash2"), { className: "size-4" })}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
                {filtered.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={8} className="text-center text-muted-foreground">
                      无匹配任务
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </div>
      )}

      <NewTaskDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreated={(id) => setTaskID(id)}
        draft={draft}
      />

      <CategoryDialog
        open={categoryOpen}
        onOpenChange={setCategoryOpen}
        tasks={tasks}
        onChanged={() => {
          // 指派变化由 /api/tasks 轮询自然刷新,此处无额外动作。
        }}
      />

      <ConfirmDeleteDialog
        target={deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={(reason) => {
          if (deleteTarget) doDelete(deleteTarget, reason);
          setDeleteTarget(null);
        }}
      />

      <ConfirmBatchDialog
        batch={batch}
        count={selectedIDs.length}
        titles={selectedTasks.map((t) => t.title || t.id)}
        onClose={() => setBatch(null)}
        onConfirm={(reason) => {
          if (batch) doBatch(batch.action, reason);
          setBatch(null);
        }}
      />
    </div>
  );
}

// ConfirmDeleteDialog 单任务删除二次确认。
function ConfirmDeleteDialog({ target, onClose, onConfirm }) {
  const [reason, setReason] = React.useState("面板删除");
  React.useEffect(() => {
    if (target) setReason("面板删除");
  }, [target]);
  return (
    <Dialog open={Boolean(target)} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>删除任务</DialogTitle>
          <DialogDescription>
            删除后列表不再显示,账本历史全部保留;在跑任务先熔断再删除。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <Label htmlFor="del-reason">删除理由(入账本)</Label>
          <Input id="del-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button
            variant="destructive"
            disabled={!reason.trim()}
            onClick={() => onConfirm(reason.trim())}
          >
            确认删除
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ConfirmBatchDialog 批量冬眠/熔断/删除二次确认。
function ConfirmBatchDialog({ batch, count, titles, onClose, onConfirm }) {
  const [reason, setReason] = React.useState("");
  React.useEffect(() => {
    if (batch)
      setReason(
        batch.action === "stop" ? "批量熔断" : batch.action === "hibernate" ? "批量冬眠" : "批量删除"
      );
  }, [batch]);
  const isStop = batch?.action === "stop";
  const isHiber = batch?.action === "hibernate";
  return (
    <Dialog open={Boolean(batch)} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{isStop ? "批量熔断" : isHiber ? "批量冬眠" : "批量删除"}</DialogTitle>
          <DialogDescription>
            {isStop
              ? `将对 ${count} 个任务熔断:终止且不可恢复,操作入账本。`
              : isHiber
                ? `将对 ${count} 个任务冬眠:全部暂停,计时停走;唤醒后原地继续。`
                : `将对 ${count} 个任务删除(列表移除,账本历史保留);在跑任务先熔断再删除。`}
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <div className="max-h-24 overflow-y-auto text-xs text-muted-foreground">
            {titles.join("、")}
          </div>
          <Label htmlFor="batch-reason">理由(入账本)</Label>
          <Input id="batch-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button
            variant={isStop || !isHiber ? "destructive" : "default"}
            disabled={!reason.trim()}
            onClick={() => onConfirm(reason.trim())}
          >
            确认
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
