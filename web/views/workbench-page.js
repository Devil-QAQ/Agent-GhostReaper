"use client";

import * as React from "react";
import { toast } from "sonner";

import { CopyIDButton } from "@/components/copy-id-button";
import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { KpiCard } from "@/components/kpi-card";
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
import { Textarea } from "@/components/ui/textarea";
import {
  CLAIM_STATUS,
  GOAL_STATUS,
  STEP_STATUS,
  SEVERITY,
  TASK_STATUS,
  denyStatusLabel,
  postJSON,
  severityMeta,
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

// 单位状态 → 图标键(与虫群会话页状态词同源;无专属图标的终因用圆点)。
const UNIT_ICON = {
  running: "activity",
  dispatched: "circle",
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
    toast.success("已标记达成并入账");
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
  const covered = assetNodes.filter((a) => a.covered).length;
  const goalsActive = goals.filter((g) => GOAL_STATUS[g.status]?.variant === "running").length;
  const unitsRunning = units.filter((u) => u.status === "running" && !u.paused).length;
  const st = TASK_STATUS[currentTask.status] || { variant: "info", label: currentTask.status };

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
        <CardContent className="flex flex-col gap-3 p-6">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-xl font-semibold tracking-tight">{currentTask.title || tid}</h1>
            {currentTask.settled && currentTask.status === "stopped" ? (
              <Badge variant="paused">已收尾</Badge>
            ) : (
              <Badge variant={st.variant}>{st.label}</Badge>
            )}
            {currentTask.blocked && <Badge variant="warning">受阻</Badge>}
            <CopyIDButton id={tid} />
            <span className="ml-auto flex items-center gap-2">
              {currentTask.blocked && (
                <Button variant="outline" size="sm" disabled={recovering} onClick={recover}
                  title="重开本任务受阻的步骤并恢复执行(模型故障解除后用)">
                  {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                  {recovering ? "恢复中…" : "恢复"}
                </Button>
              )}
            </span>
          </div>
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
                  className="rounded-full border px-2 py-0.5 font-mono text-xs text-muted-foreground"
                  title={t}
                >
                  {t}
                </span>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {/* 关键数据(KPI 即导航,#40 副文案承载健康度) */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <KpiCard
          label="目标"
          value={`${currentTask.goals_achieved ?? 0}/${currentTask.goals_total ?? 0}`}
          hint={`已达成 ${currentTask.goals_achieved ?? 0} · 进行中 ${goalsActive}`}
        />
        <KpiCard
          label="资产"
          value={assetNodes.length}
          hint={`覆盖 ${covered}/${assetNodes.length}`}
          href={`/task/${tid}/assets`}
        />
        <KpiCard
          label="单位"
          value={unitsRunning}
          hint={`运行中 · 历史 ${units.length} 个`}
          href={`/task/${tid}/sessions`}
        />
        <KpiCard
          label="Confirmed 发现"
          value={wins.length}
          hint={`共 ${claimList.length} 条声明`}
          href={`/task/${tid}/findings`}
          tone={wins.length > 0 ? "success" : undefined}
        />
      </div>

      {/* 详情层:目标 × 进度图 × 执行流 × 发现 */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,7fr)_minmax(0,3fr)]">
        {/* 左列(主):目标区 + 内嵌进度图 */}
        <div className="flex min-w-0 flex-col gap-4">
          <div className="rounded-xl border bg-card px-4 py-3">
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
                      <span className="shrink-0 font-mono text-xs text-muted-foreground">
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
            <div className="flex h-64 items-center justify-center rounded-xl border bg-card text-sm text-muted-foreground">
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
          <div className="flex flex-col rounded-xl border bg-card">
            <div className="flex items-center gap-2 px-4 pt-3 pb-2 text-xs font-medium tracking-wide text-muted-foreground">
              执行流
              <span className="ml-auto font-mono">{units.length}</span>
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
                      <span className="shrink-0 font-mono text-xs">{u.unit_id}</span>
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

          <div className="flex flex-col rounded-xl border bg-card">
            <div className="flex items-center gap-2 px-4 pt-3 pb-2 text-xs font-medium tracking-wide text-muted-foreground">
              发现
              <span className="ml-auto font-mono">{wins.length}</span>
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
                        <Badge variant={CLAIM_STATUS.confirmed.variant}>
                          {CLAIM_STATUS.confirmed.label}
                        </Badge>
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
      <Dialog open={Boolean(achieveGoal)} onOpenChange={(o) => !o && closeAchieve()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="text-base">标记达成</DialogTitle>
            <DialogDescription>
              目标无完成条件时由人工判定;标记后入账,全部目标达成即任务完成。
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
      <h1 className="text-[28px] leading-tight font-semibold tracking-tight">工作台</h1>
    </div>
  );
}

// unitWord 单位终因 → 状态词(与虫群会话页 STEP_STATUS 词汇同源)。
function unitWord(status) {
  return STEP_STATUS[status] || status;
}
