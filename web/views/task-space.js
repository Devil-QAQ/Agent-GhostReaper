"use client";

import * as React from "react";

import { EmptyState } from "@/components/empty-state";
import { ErrorBoundary } from "@/components/error-boundary";
import { iconOf } from "@/components/icons";
import { AnnulusPage } from "@/views/annulus-page";
import { AssetsPage } from "@/views/assets-page";
import { FindingsPage } from "@/views/findings-page";
import { ProgressPage } from "@/views/progress-page";
import { ReportPage } from "@/views/report-page";
import { RoePage } from "@/views/roe-page";
import { SessionsPage } from "@/views/sessions-page";
import { WorkbenchPage } from "@/views/workbench-page";
import { usePolling, useTaskContext, taskBlockedDot } from "@/lib/api";
import { enabledTaskTabs } from "@/lib/nav";

// 任务空间(8.3,P2-06g 骨架):进入任务后的顶部标签容器。
// 路由 #/task/<taskID>/<tab> 携带任务上下文并在此同步进全局任务
// 上下文——各标签共享同一任务,切换不丢;P2-09 起路由可带 URL 定位
// 参数(#/task/<taskID>/<tab>?focus=<节点ID>,跳转协议「在图中查看」
// 的直达形式),focus 透传给对应标签页定位聚焦;P2-10 起工作台执行流
// 的单位定位走 ?unit=<会话ID>(透传给会话页)。
const TAB_PAGE_OF = {
  workbench: WorkbenchPage,
  sessions: SessionsPage,
  progress: ProgressPage,
  annulus: AnnulusPage,
  findings: FindingsPage,
  assets: AssetsPage,
  roe: RoePage,
  report: ReportPage,
};

// 冬眠提醒(P2-08):任务冬眠中在整个任务空间常显——无论当前在哪个
// 标签/会话组,冻结事实与恢复入口一目了然;有待裁决审批时附笔数
//(唤醒后恢复可裁决)。
function HibernateBanner({ task }) {
  const url = task?.status === "hibernated" && task?.id ? `/api/tasks/${task.id}/approvals` : null;
  const { data } = usePolling(url || "/api/healthz", 4000, {
    immediate: Boolean(url),
  });
  const pending = (data?.pending || []).length;
  return (
    <div className="flex items-center gap-2 rounded-md border border-status-warning/40 bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
      {React.createElement(iconOf("moon"), { className: "size-3.5 shrink-0" })}
      <span>
        任务冬眠中:主宰与虫群挂起,牧者对话冻结,计时器停走;唤醒后原地继续。
        {pending > 0 && ` 待审批 ${pending} 笔冻结为只读。`}
      </span>
    </div>
  );
}

export function TaskSpace({ tasks, taskID, tab, focus, unit }) {
  const { setTaskID } = useTaskContext();
  // 路由是任务空间的任务上下文权威:深链/行点击/标签切换都先落路由。
  React.useEffect(() => {
    if (taskID) setTaskID(taskID);
  }, [taskID, setTaskID]);

  const loaded = Array.isArray(tasks);
  const current = loaded ? tasks.find((t) => t.id === taskID) : null;

  // RoE 标签待审批计数(P4-23):红点+计数,approval 实时推送驱动
  //(推送未连接时轮询降级);点标签即到裁决面。
  const approvalsURL = current?.id ? `/api/tasks/${current.id}/approvals` : null;
  const { data: approvalsBody } = usePolling(approvalsURL || "/api/healthz", 4000, {
    immediate: Boolean(approvalsURL),
    live: approvalsURL ? [`approval:${current.id}`] : null,
  });
  const roePending = approvalsURL
    ? Array.isArray(approvalsBody?.pending ?? approvalsBody)
      ? (approvalsBody?.pending ?? approvalsBody).length
      : 0
    : 0;

  if (!loaded) {
    return <div className="py-12 text-center text-sm text-muted-foreground">读取中</div>;
  }
  if (!current) {
    return (
      <EmptyState
        icon="listChecks"
        title="任务不存在"
        hint="该任务不在年轮账本中(可能来自旧链接)"
        action={{ label: "回任务列表", href: "/tasks" }}
      />
    );
  }

  const tabs = enabledTaskTabs();
  const Page = TAB_PAGE_OF[tab];
  const pageProps =
    tab === "sessions" ? { currentTask: current, unitFocus: unit } : { currentTask: current, focus };
  return (
    <div className="flex flex-col gap-6">
      <div className="sticky top-[53px] z-30 -mx-2 bg-background/95 px-2 py-2 backdrop-blur">
        {/* 8.9 质感:分段控件去描边靠表面色阶,当前项浅表面+微投影(克制,
            不整块铺品牌色),hover 所在层提亮 */}
        <div className="flex w-fit gap-0.5 rounded-lg bg-muted p-1">
          {tabs.map((t) => (
            <button
              key={t.id}
              className={
                "flex items-center rounded-md px-3 py-1 text-[13px] transition-colors duration-150 cursor-pointer " +
                (tab === t.id
                  ? "bg-popover text-foreground shadow-xs"
                  : "text-muted-foreground hover:bg-hover-lift hover:text-foreground")
              }
              onClick={() => {
                location.hash = `/task/${current.id}/${t.id}`;
              }}
            >
              {t.title}
              {/* 受阻红点+计数挂"会话"标签(P4-57 ⑤ 三层递进之二:
                  点进会话页看到受阻单位与事件;悬停按归因分文案;头部
                  状态徽标不动——普遍故障单显"受阻",状态不是红点) */}
              {t.id === "sessions" &&
                (() => {
                  const bd = taskBlockedDot(current);
                  return bd ? (
                    <span
                      className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-none text-primary-foreground num"
                      title={bd.title}
                    >
                      {bd.count}
                    </span>
                  ) : null;
                })()}
              {/* 待审批红点+计数(P4-23):只标需要动作的事项,处理即消 */}
              {t.id === "roe" && roePending > 0 && (
                <span
                  className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-none text-primary-foreground num"
                  title={`待审批 ${roePending} 笔`}
                >
                  {roePending}
                </span>
              )}
              {/* 发现未读红点+计数(P4-47 A):汇总位——成果或事实任一
                  有未读即亮,两个子页签全看过才灭;点击进发现页 */}
              {t.id === "findings" &&
                (current.unread_claims > 0 || current.unread_facts > 0) && (
                  <span
                    className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-none text-primary-foreground num"
                    title={`未读:成果 ${current.unread_claims ?? 0} 条 · 事实 ${current.unread_facts ?? 0} 条`}
                  >
                    {(current.unread_claims ?? 0) + (current.unread_facts ?? 0)}
                  </span>
                )}
            </button>
          ))}
        </div>
      </div>
      {current.status === "hibernated" && <HibernateBanner task={current} />}
      {/* 页面级渲染错误边界(P4-60 ④):标签页崩降级为区块错误,标签栏
          与冬眠提醒不受影响、其余标签可用;key 随任务与标签换页重挂。 */}
      {Page ? (
        <ErrorBoundary key={`${current.id}:${tab}`}>
          <Page {...pageProps} />
        </ErrorBoundary>
      ) : null}
    </div>
  );
}
