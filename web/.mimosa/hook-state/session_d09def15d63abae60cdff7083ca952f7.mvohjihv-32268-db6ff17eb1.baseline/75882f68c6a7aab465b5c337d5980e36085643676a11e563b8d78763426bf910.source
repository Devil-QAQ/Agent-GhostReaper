"use client";

import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";

// 占位栏(P2-08c 侧栏一次调整):MCP / SKILL 有意占位(8.3:功能属
// 后续 P)。空状态呈现事实,不做假入口。(Agent 占位已由 P2-08d 的
// agent-page.js 正式页替换。)
export function MCPPlaceholderPage() {
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="MCP" subtitle="外部工具接入" />
      <EmptyState
        icon="plug"
        title="MCP 未实现"
        hint="MCP 接入属后续阶段,本栏为占位"
        action={{ label: "去任务列表", href: "/tasks" }}
      />
    </div>
  );
}

export function SKILLPlaceholderPage() {
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="SKILL" subtitle="技能接入" />
      <EmptyState
        icon="sparkles"
        title="SKILL 未实现"
        hint="技能接入属后续阶段,本栏为占位"
        action={{ label: "去任务列表", href: "/tasks" }}
      />
    </div>
  );
}
