// 终态导航信息架构(关键思想第八章 8.3 任务空间架构,P2-06g 落地;
// P2-08c 侧栏一次调整:新增「LLM配置」「Agent」「MCP」「SKILL」四栏,
// 设置改名系统设置并收窄):
// 一级侧栏只放全局功能;任务的一切面板在任务空间顶部标签内
//(TASK_TABS,路由 #/task/<taskID>/<tab>)。终态清单一次写全,enabled
// 决定是否渲染——未实现项隐藏而非缺失,后续 P 上线时点亮=改一个布尔
// 值,新页面只往骨架上挂,不重构(8.2)。

// 一级侧栏(全局)。
export const NAV_ITEMS = [
  { id: "tasks", title: "任务", icon: "listChecks", phase: "P1", enabled: true },
  { id: "reports", title: "报告", icon: "fileText", phase: "P2-13 跨任务导出", enabled: true },
  { id: "observe", title: "观测", icon: "gauge", phase: "P2-12", enabled: true },
  { id: "traffic", title: "流量", icon: "waves", phase: "P3-02", enabled: true },
  { id: "llm", title: "LLM配置", icon: "cpu", phase: "P2-08c", enabled: true },
  { id: "agent", title: "Agent", icon: "bot", phase: "P2-08d", enabled: true },
  { id: "mcp", title: "MCP", icon: "plug", phase: "占位(后续 P)", enabled: true },
  { id: "skill", title: "SKILL", icon: "sparkles", phase: "占位(后续 P)", enabled: true },
  { id: "settings", title: "系统设置", icon: "settings", phase: "P1 最简,P2-08c 收窄", enabled: true },
];

// 任务空间顶部标签(进入任务后)。顺序按 8.3
// "总览 → 驾驶 → 图 → 成果 → 基础数据 → 约束 → 交付"。
export const TASK_TABS = [
  { id: "workbench", title: "工作台", phase: "P2-10", enabled: true },
  { id: "sessions", title: "会话", phase: "P2-07 牧者+布局 / P2-08/08b 补全", enabled: true },
  { id: "progress", title: "进度图", phase: "P2-09", enabled: true },
  { id: "annulus", title: "年轮图", phase: "P2-11", enabled: true },
  { id: "findings", title: "发现", phase: "P1 迁入", enabled: true },
  { id: "assets", title: "资产", phase: "P1 迁入", enabled: true },
  { id: "roe", title: "RoE", phase: "P1 迁入", enabled: true },
  { id: "report", title: "报告", phase: "P1 迁入", enabled: true },
];

// 已点亮标签;进入任务空间缺省落在首个点亮标签(后续 P 点亮工作台后
// 自动前移,无需改路由代码)。
export function enabledTaskTabs() {
  return TASK_TABS.filter((t) => t.enabled);
}

export function defaultTaskTab() {
  return enabledTaskTabs()[0]?.id || "findings";
}

export function isTaskTabEnabled(id) {
  return TASK_TABS.some((t) => t.id === id && t.enabled);
}

// P2-06g 前的平铺路由(四页已迁入任务空间)——旧书签/旧链接按当前
// 任务重定向到任务空间对应标签。
export const LEGACY_TASK_ROUTES = ["assets", "findings", "roe", "report"];

// 当前点亮的平铺可路由页面(hash 路由 id → 组件);任务空间页走
// #/task/<taskID>/<tab>,不在此列。
export const ROUTES = ["tasks", "reports", "observe", "traffic", "llm", "agent", "mcp", "skill", "settings"];
