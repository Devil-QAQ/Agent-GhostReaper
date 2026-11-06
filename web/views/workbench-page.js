"use client";

import * as React from "react";
import { toast } from "sonner";

import { CopyIDButton } from "@/components/copy-id-button";
import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { KpiCard } from "@/components/kpi-card";
import { StatusBadge } from "@/components/status-badge";
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
import { Textarea } from "@/components/ui/textarea";
import {
  CLAIM_STATUS,
  GOAL_STATUS,
  STEP_STATUS,
  SEVERITY,
  denyStatusLabel,
  postJSON,
  severityMeta,
  taskStatusBadge,
  usePolling,
} from "@/lib/api";
import { GraphCanvas } from "@/views/progress-page";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 任务空间·工作台(P2-10,8.3"总览"落点;P2-14b② 信息重组):单任务
// 「概要 → 关键数据 → 详情」三层——概要卡(任务名主视觉 + 状态 + 描述
// + 目标,ID 弱化为复制入口)、关键数据 KPI 行(#40 即导航)、详情层
// 四区同屏(目标 × 进度图 × 执行流 × 发现,布局按 8.9 §7 主次约 7:3):
// 左列目标区 + 内嵌进度图(GraphCanvas 复用进度图页全套,含节点抽屉
// 与聚焦),右列执行流(虫群紧凑视图,一行一单位【状态图标+意图摘要】,
// 点击跳该单位完整会话页——复用 P2-08b 会话页)与发现(Confirmed
// 战果,点击开详情抽屉)。
// P4-09:概要卡尾部三条并排细进度条(目标达成 n/m · 步骤 完成/在跑/
// 排队 · 资产覆盖 范围内已测/未测)——图载荷派生,资产入图/步骤迁移/
// 目标达成都落 node 事件,经 tasks 实时主题事件驱动刷新,零新增采集;
// 三条各自陈述,不做加权综合百分比。

// 单位状态 → 图标键(与虫群会话页状态词同源;无专属图标的终因用圆点)。
const UNIT_ICON = {
  running: "activity",
  dispatched: "circle",
  cancelled: "x",
  done: "check",
  exhausted: "timer",
  failed: "x",
  aborted: "unplug",
  approval_denied: "shieldBan",
  blocked: "shieldAlert",
};

export function WorkbenchPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  // P3-08:进度图(目标/步骤态,受阻步骤在其中)与发现走实时推送驱动
  // 刷新(主题 tasks = 图上节点迁移);推送未连接时按原节奏轮询降级。
  // 单位列表是执行器进程态(无账本单射),维持轮询。
  const { data: graph } = usePolling(tid ? `/api/tasks/${tid}/graph` : "/api/healthz", 3000, {
    immediate: Boolean(tid),
    live: tid ? ["tasks"] : null,
  });
  const { data: unitsBody } = usePolling(tid ? `/api/tasks/${tid}/units` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
  });
  const { data: claims } = usePolling(tid ? `/api/tasks/${tid}/claims` : "/api/healthz", 2000, {
    immediate: Boolean(tid),
    live: tid ? [`claims:${tid}`] : null,
  });
  const [sheet, setSheet] = React.useState(null);
  const [recovering, setRecovering] = React.useState(false);
  // 收口提醒配置入口(审核迭代 1):概要卡右侧按钮 + 小弹窗改任务级配置。
  const [closureOpen, setClosureOpen] = React.useState(false);
  // 人工标记达成(P3-14b ④):目标无完成条件时由操作者看到证据后手动
  // 收场,依据必填、入账留痕。
  const [achieveGoal, setAchieveGoal] = React.useState(null);
  const [achieveReason, setAchieveReason] = React.useState("");
  const [achieveBusy, setAchieveBusy] = React.useState(false);

  const closeAchieve = () => {
    setAchieveGoal(null);
    setAchieveReason("");
  };

  const markAchieved = async () => {
    if (!achieveGoal || !tid) return;
    const reason = achieveReason.trim();
    if (!reason) {
      toast.error("先写明依据");
      return;
    }
    setAchieveBusy(true);
    const { ok, body } = await postJSON(
      `/api/tasks/${tid}/goals/${achieveGoal.id}/achieve`,
      { reason }
    );
    setAchieveBusy(false);
    if (!ok) {
      toast.error(`被拒:${body?.error || "原因未说明"}`);
      return;
    }
    closeAchieve();
    toast.success("已标记达成并记录");
  };

  // 单任务恢复(P3-05c ⑥ 恢复按钮双入口之一):该任务存在受阻步骤时
  // 可用——重开受阻步骤并执行侧拉起(暂停任务随之拉起)。
  const recover = async () => {
    if (!tid) return;
    setRecovering(true);
    const { ok, body } = await postJSON(`/api/tasks/${tid}/recover`, {});
    setRecovering(false);
    if (!ok) {
      toast.error(`恢复失败:${body?.error || "原因未说明"}`);
      return;
    }
    if ((body?.steps || 0) === 0) {
      toast.info("该任务无受阻步骤");
      return;
    }
    toast.success(`已重开 ${body.steps} 个受阻步骤`);
  };

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <WorkbenchPlainHeader />
        <EmptyState
          icon="layoutDashboard"
          title="未选择任务"
          hint="工作台随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  const nodes = graph?.nodes || [];
  const edges = graph?.edges || [];
  const goals = nodes.filter((n) => n.type === "goal");
  const units = (unitsBody?.units || []).slice().reverse(); // 最新在先
  const claimList = Array.isArray(claims) ? claims : [];
  const wins = claimList
    .filter((c) => c.status === "confirmed")
    .sort((a, b) => {
      const ra = SEVERITY.indexOf(a.severity), rb = SEVERITY.indexOf(b.severity);
      return (ra < 0 ? 99 : ra) - (rb < 0 ? 99 : rb) || b.created_rev - a.created_rev;
    });

  // 关键数据(KPI 即导航):目标进度 / 资产覆盖 / 运行单位 / 战果。
  const assetNodes = nodes.filter((n) => n.type === "asset");
  // 覆盖口径(P4-05,与资产页一致):范围外资产不占分母。
  const assetsInScope = assetNodes.filter((a) => !a.excluded);
  const assetsExcluded = assetNodes.length - assetsInScope.length;
  const coveredInScope = assetsInScope.filter((a) => a.covered).length;
  const goalsActive = goals.filter((g) => GOAL_STATUS[g.status]?.variant === "running").length;
  const goalsAchieved = goals.filter((g) => g.status === "achieved").length;
  // 步骤三段(P4-09 ②):完成 / 在跑 / 排队;其余终态不占段,条上轨道
  // 留空如实呈现(失败/受阻等在各详情页可见)。
  const stepNodes = nodes.filter((n) => n.type === "step");
  const stepsDone = stepNodes.filter((s) => s.status === "done").length;
  const stepsRunning = stepNodes.filter((s) => s.status === "running").length;
  const stepsQueued = stepNodes.filter((s) => s.status === "dispatched").length;
  const stepsOther = stepNodes.length - stepsDone - stepsRunning - stepsQueued;
  const unitsRunning = units.filter((u) => u.status === "running" && !u.paused).length;
  const st = taskStatusBadge(currentTask);

  // 每目标的步骤进度(goal 派生边 Goal→Step)。
  const stepsOf = {};
  for (const e of edges) {
    if (e.type !== "goal") continue;
    (stepsOf[e.from] = stepsOf[e.from] || []).push(e.to);
  }
  const nodeByID = new Map(nodes.map((n) => [n.id, n]));

  return (
    <div className="flex flex-col gap-4">
      {/* 概要(P2-14b②):任务名主视觉,状态与目标随行;ID 只留复制入口 */}
      <Card>
        <CardContent className="flex flex-col gap-4 p-5">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="t-page">{currentTask.title || tid}</h1>
            <Badge variant={st.variant} title={st.title}>
              {st.label}
            </Badge>
            {/* P4-27 ② 绝对单显:状态只此一枚(受阻/冬眠并排形态已废,
                取态见 taskStatusBadge 全状态矩阵;受阻步骤数走行内
                「受阻 N 步」计数与恢复按钮) */}
            <CopyIDButton id={tid} />
            <span className="ml-auto flex items-center gap-2">
              {currentTask.blocked_steps > 0 && (
                <Button variant="outline" size="sm" disabled={recovering} onClick={recover}
                  title="重开本任务受阻的步骤并恢复执行(模型故障解除后用)">
                  {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                  {recovering ? "恢复中…" : `恢复(${currentTask.blocked_steps})`}
                </Button>
              )}
              {currentTask.closure_config && (
                <Button variant="outline" size="sm" onClick={() => setClosureOpen(true)}
                  title="任务连续多轮规划没有任何新发现(新事实、新资产、新发现记录)，且距上次新发现超过设定分钟数时，弹窗询问是否收口。两个条件同时满足才提醒；任务照常运行，不会被暂停。">
                  {React.createElement(iconOf("timer"), { className: "size-3.5" })}
                  收口提醒
                </Button>
              )}
            </span>
          </div>
          {/* P4-43 ③ 终态任务隐藏水位行:清点后执行中/排队/等前置已清零,
              还可新增/派发上限对已收场的任务无意义(隐藏而非清零,避免
              "还可新增 8"误导)。 */}
          {graph?.queue && currentTask?.status !== "stopped" && currentTask?.status !== "completed" && (
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground num" title="排队只计可领取步骤，等前置步骤预留容量；反证、假设验证与急活不受派发上限限制。反证优先领取下一个空位，不能保证立即起飞。">
              <span>执行中 {graph.queue.running}</span><span>排队 {graph.queue.queued}</span><span>等前置 {graph.queue.waiting}</span><span>还可新增 {graph.queue.available}</span><span>派发上限 {graph.queue.limit}</span>
              {graph.queue.available === 0 && <span>等待单位结果</span>}
            </div>
          )}
          {currentTask.description && (
            <p className="text-sm leading-relaxed text-muted-foreground">
              {currentTask.description}
            </p>
          )}
          {(currentTask.targets || []).length > 0 && (
            <div className="flex flex-wrap gap-2">
              {currentTask.targets.map((t) => (
                <span
                  key={t}
                  className="rounded-full border px-2 py-0.5 num text-xs text-muted-foreground"
                  title={t}
                >
                  {t}
                </span>
              ))}
            </div>
          )}

          {/* 三条并排细进度条(P4-09):悬停见口径数字;条宽即各段占比 */}
          <div className="grid grid-cols-1 gap-4 pt-2 sm:grid-cols-3">
            <MiniBar
              label="目标达成"
              value={`${goalsAchieved}/${goals.length}`}
              title={`已达成 ${goalsAchieved} · 进行中 ${goalsActive}`}
              segments={[{ cls: "bg-primary", w: pctOf(goalsAchieved, goals.length) }]}
            />
            <MiniBar
              label="步骤"
              value={`${stepsDone}/${stepNodes.length}`}
              title={
                `已完成 ${stepsDone} · 执行中 ${stepsRunning} · 排队中 ${stepsQueued}` +
                (stepsOther > 0 ? ` · 其余 ${stepsOther}` : "") +
                ` · 共 ${stepNodes.length} 步`
              }
              segments={[
                { cls: "bg-status-success", w: pctOf(stepsDone, stepNodes.length) },
                { cls: "bg-status-running", w: pctOf(stepsRunning, stepNodes.length) },
                { cls: "bg-status-paused", w: pctOf(stepsQueued, stepNodes.length) },
              ]}
            />
            <MiniBar
              label={assetsExcluded > 0 ? "资产覆盖(范围内)" : "资产覆盖"}
              value={`${coveredInScope}/${assetsInScope.length}`}
              title={
                `范围内已测 ${coveredInScope} · 未测 ${assetsInScope.length - coveredInScope}` +
                (assetsExcluded > 0 ? ` · 范围外 ${assetsExcluded}` : "")
              }
              segments={[{ cls: "bg-primary", w: pctOf(coveredInScope, assetsInScope.length) }]}
            />
          </div>
        </CardContent>
      </Card>

      {/* 关键数据(KPI 即导航,#40 副文案承载健康度) */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <KpiCard
          label="目标"
          value={`${currentTask.goals_achieved ?? 0}/${currentTask.goals_total ?? 0}`}
          hint={`已达成 ${currentTask.goals_achieved ?? 0} · 进行中 ${goalsActive}`}
        />
        <KpiCard
          label="资产"
          value={assetNodes.length}
          hint={
            assetsExcluded > 0
              ? `范围内 ${assetsInScope.length} · 范围外 ${assetsExcluded}`
              : `覆盖 ${coveredInScope}/${assetsInScope.length}`
          }
          href={`/task/${tid}/assets`}
        />
        <KpiCard
          label="单位"
          value={unitsRunning}
          hint={`运行中 · 历史 ${units.length} 个`}
          href={`/task/${tid}/sessions`}
        />
        <KpiCard
          label="已确认发现"
          value={wins.length}
          hint={`共 ${claimList.length} 条判断`}
          href={`/task/${tid}/findings`}
          tone={wins.length > 0 ? "success" : undefined}
        />
      </div>

      {/* 详情层:目标 × 进度图 × 执行流 × 发现 */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,7fr)_minmax(0,3fr)]">
        {/* 左列(主):目标区 + 内嵌进度图 */}
        <div className="flex min-w-0 flex-col gap-4">
          <div className="rounded-xl border border-border/55 bg-card px-4 py-3">
            <div className="mb-2 text-xs font-medium tracking-wide text-muted-foreground">目标</div>
            {goals.length === 0 ? (
              <div className="py-2 text-sm text-muted-foreground">暂无目标</div>
            ) : (
              <div className="flex flex-col gap-2">
                {goals.map((g) => {
                  const gst = GOAL_STATUS[g.status] || { label: g.status, variant: "info" };
                  const kids = (stepsOf[g.id] || []).map((id) => nodeByID.get(id)).filter(Boolean);
                  const done = kids.filter((s) => s.status === "done").length;
                  return (
                    <div key={g.id} className="flex items-center gap-3 text-sm">
                      <Badge variant={gst.variant}>{gst.label}</Badge>
                      <span className="min-w-0 flex-1 truncate" title={g.title}>
                        {g.title || g.id}
                      </span>
                      <span className="shrink-0 num text-xs text-muted-foreground">
                        步骤 {done}/{kids.length}
                      </span>
                      {g.status === "active" && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-6 shrink-0 px-2 text-xs"
                          onClick={() => setAchieveGoal(g)}
                        >
                          标记达成
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
          {/* 进度区:内嵌进度图(节点点击抽屉、?focus= 聚焦,全同进度图页;
              抽屉与本页发现区共用同一状态,不叠双层) */}
          {nodes.length === 0 ? (
            <div className="flex h-64 items-center justify-center rounded-xl border border-border/55 bg-card text-sm text-muted-foreground">
              主宰派发步骤后,血缘图在此生长
            </div>
          ) : graph ? (
            <GraphCanvas
              data={{ ...graph, tid }}
              focus={focus}
              heightClass="h-[520px]"
              sheet={sheet}
              onSheetChange={setSheet}
            />
          ) : (
            <div className="skeleton h-[520px] rounded-xl" />
          )}
        </div>

        {/* 右列(辅):执行流 + 发现 */}
        <div className="flex min-w-0 flex-col gap-4">
          <div className="flex flex-col rounded-xl border border-border/55 bg-card">
            <div className="flex items-center gap-2 px-4 pt-3 pb-2 text-xs font-medium tracking-wide text-muted-foreground">
              执行流
              <span className="ml-auto num">{units.length}</span>
            </div>
            <div className="max-h-[300px] overflow-y-auto px-2 pb-2">
              {units.length === 0 ? (
                <div className="px-2 py-4 text-sm text-muted-foreground">尚无虫群单位</div>
              ) : (
                units.map((u) => {
                  const paused = u.paused;
                  const label = u.status === "approval_denied" ? denyStatusLabel(u) : unitWord(u.status);
                  const ust = paused
                    ? { label: "冬眠中", icon: "moon" }
                    : { label, icon: UNIT_ICON[u.status] || "circle" };
                  const Icon = iconOf(ust.icon);
                  return (
                    <button
                      key={u.session_id}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover-lift cursor-pointer"
                      title={`${u.unit_id} ${u.step_id} · ${u.step_title || ""}${u.status === "approval_denied" ? "\n" + (u.deny_reason || label) : ""}`}
                      onClick={() => {
                        location.hash = `/task/${tid}/sessions?unit=${encodeURIComponent(u.session_id)}`;
                      }}
                    >
                      <Icon
                        className={cn(
                          "size-3.5 shrink-0",
                          u.status === "running" && !paused && "text-status-running",
                          u.status === "done" && "text-status-success",
                          (u.status === "failed" || u.status === "approval_denied") && "text-status-danger",
                          paused && "text-muted-foreground",
                        )}
                      />
                      <span className="shrink-0 num text-xs">{u.unit_id}</span>
                      <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                        {u.step_title || u.step_id}
                      </span>
                      <span className="max-w-32 shrink-0 truncate text-xs text-muted-foreground" title={label}>
                        {ust.label}
                      </span>
                    </button>
                  );
                })
              )}
            </div>
          </div>

          <div className="flex flex-col rounded-xl border border-border/55 bg-card">
            <div className="flex items-center gap-2 px-4 pt-3 pb-2 text-xs font-medium tracking-wide text-muted-foreground">
              发现
              <span className="ml-auto num">{wins.length}</span>
            </div>
            <div className="max-h-[300px] overflow-y-auto px-2 pb-2">
              {wins.length === 0 ? (
                <div className="px-2 py-4 text-sm text-muted-foreground">暂无锤炼完成成果</div>
              ) : (
                wins.map((c) => {
                  const sev = severityMeta(c.severity);
                  return (
                    <button
                      key={c.id}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover-lift cursor-pointer"
                      title={c.statement}
                      onClick={() => setSheet({ type: "testclaim", id: c.id })}
                    >
                      {sev ? (
                        <Badge variant={sev.variant}>{sev.label}</Badge>
                      ) : (
                        <StatusBadge meta={CLAIM_STATUS.confirmed} />
                      )}
                      <span className="min-w-0 flex-1 truncate text-xs">{c.statement}</span>
                    </button>
                  );
                })
              )}
            </div>
          </div>
        </div>
      </div>
      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} graph={graph} />
      <ClosureConfigDialog
        open={closureOpen}
        onOpenChange={setClosureOpen}
        taskID={tid}
        config={currentTask.closure_config}
      />
      <Dialog open={Boolean(achieveGoal)} onOpenChange={(o) => !o && closeAchieve()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>标记达成</DialogTitle>
            <DialogDescription>
              目标无完成条件时由人工判定;标记后记录,全部目标达成即任务完成。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            rows={3}
            placeholder="依据:看到的现象或证据(必填)"
            value={achieveReason}
            onChange={(e) => setAchieveReason(e.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={closeAchieve}>
              取消
            </Button>
            <Button onClick={markAchieved} disabled={achieveBusy || !achieveReason.trim()}>
              标记达成
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// WorkbenchPlainHeader 未选任务时的页面头(选任务后由概要卡承担)。
function WorkbenchPlainHeader() {
  return (
    <div className="border-b pb-4">
      <h1 className="t-page">工作台</h1>
    </div>
  );
}

// ClosureConfigDialog 收口提醒任务级配置弹窗(审核迭代 1):开关 +
// 轮数 + 间隔分钟;保存落账本,下一次研判轮生效,在跑任务不中断。
// 快照式保存:两个数字留空 = 该项用全局默认(会清掉本任务之前的单独
// 设定,占位符展示当前生效值便于核对)。
function ClosureConfigDialog({ open, onOpenChange, taskID, config }) {
  const [enabled, setEnabled] = React.useState(true);
  const [rounds, setRounds] = React.useState("");
  const [minutes, setMinutes] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open || !config) return;
    setEnabled(config.enabled);
    setRounds(config.task_rounds > 0 ? String(config.task_rounds) : "");
    setMinutes(config.task_minutes > 0 ? String(config.task_minutes) : "");
  }, [open, config]);

  const save = async () => {
    const r = rounds.trim() === "" ? 0 : parseInt(rounds, 10);
    const m = minutes.trim() === "" ? 0 : parseInt(minutes, 10);
    if (!Number.isFinite(r) || r < 0 || r > 100) {
      toast.error("收口信号轮数须为 1~100 的整数(留空用全局默认)");
      return;
    }
    if (!Number.isFinite(m) || m < 0 || m > 1440) {
      toast.error("收口信号间隔须为 1~1440 的整数(留空用全局默认)");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON(`/api/tasks/${taskID}/closure/config`, {
      enabled, rounds: r, minutes: m,
    });
    setBusy(false);
    if (!ok) {
      toast.error(`保存失败:${body?.error || "原因未说明"}`);
      return;
    }
    onOpenChange(false);
    toast.success("已保存:下一次规划轮生效");
  };

  const effRounds = config?.rounds ?? "—";
  const effMinutes = config?.minutes ?? "—";
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>收口提醒</DialogTitle>
          <DialogDescription>
            任务连续多轮规划没有任何新发现(新事实、新资产、新发现记录)，且距上次新发现超过设定分钟数时，弹窗询问是否收口。两个条件同时满足才提醒；任务照常运行，不会被暂停。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <label className="flex items-center gap-2 text-sm" title="关闭后本任务不再弹收口提醒">
            <Checkbox checked={enabled} onCheckedChange={(v) => setEnabled(Boolean(v))} />
            开启收口提醒
          </label>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <span className="text-xs text-muted-foreground" title="连续多少轮规划零新发现才算满足条件之一">
                轮数(轮)
              </span>
              <Input
                inputMode="numeric"
                disabled={!enabled}
                placeholder={`留空用默认 ${effRounds}`}
                value={rounds}
                onChange={(e) => setRounds(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <span className="text-xs text-muted-foreground" title="距上次新发现超过多少分钟才算满足条件之一">
                间隔(分钟)
              </span>
              <Input
                inputMode="numeric"
                disabled={!enabled}
                placeholder={`留空用默认 ${effMinutes}`}
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
              />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            轮数 = 连续多少轮规划零新发现；间隔 = 距上次新发现多少分钟。留空用全局默认(当前 {effRounds} 轮 / {effMinutes} 分钟，Agent 页可改)。
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={save} disabled={busy}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// unitWord 单位终因 → 状态词(与虫群会话页 STEP_STATUS 词汇同源)。
function unitWord(status) {
  return STEP_STATUS[status] || status;
}

// MiniBar 工作台细进度条(P4-09):标签 + n/m + 分段细条(步骤条三段:
// 已完成绿 / 执行中蓝 / 排队中灰),宽度即占比;悬停 title 见口径数字。
function MiniBar({ label, value, title, segments }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5" title={title}>
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="min-w-0 truncate text-muted-foreground">{label}</span>
        <span className="shrink-0 num">{value}</span>
      </div>
      <div className="flex h-1.5 w-full overflow-hidden rounded-full bg-muted">
        {segments.map((s, i) => (
          <div
            key={i}
            className={cn("h-full transition-[width]", s.cls)}
            style={{ width: `${s.w}%` }}
          />
        ))}
      </div>
    </div>
  );
}

// pctOf 段宽百分比(分母为零时空条)。
function pctOf(n, total) {
  return total > 0 ? (n / total) * 100 : 0;
}
