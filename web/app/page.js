"use client";

import { AppShell } from "@/components/app-shell";
import { ErrorBoundary } from "@/components/error-boundary";

// 面板入口:终态导航骨架(P1-09b 返工,布局方案见 Process/P1-09b布局方案.md)。
// 全局渲染错误边界(P4-60 ④):壳层自身渲染 bug 兜底为区块错误卡,
// 不再整页白屏。
export default function Home() {
  return (
    <ErrorBoundary level="global">
      <AppShell />
    </ErrorBoundary>
  );
}
