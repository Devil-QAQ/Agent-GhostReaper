"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import { Button } from "@/components/ui/button";

// 渲染错误边界(P4-60 ④,问题清单 #81;捞起 P4-15 ③ 挂账):三级——
// global=App 根兜底卡 / page=页面组件外包(侧栏与标签栏不受影响,其余
// 区域可用) / overlay=Sheet·Dialog 内容体(抽屉崩溃不塌整页,关闭钮
// 可用)。任何渲染 bug 降级为区块错误:一句陈述+刷新入口,不再整页
// 白屏;错误原文与组件栈 console.error 保留(随运行日志与诊断包走,
// 不上界面)。挂载点:app/page.js(全局)、app-shell.js(平铺页)、
// task-space.js(任务空间各标签页)、ui/sheet.js 与 ui/dialog.js
// 内容体(全部浮层一处接入)。
const FALLBACK_OF = {
  global: "面板显示出错",
  page: "页面显示出错",
  overlay: "内容显示出错",
};

export class ErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidCatch(error, info) {
    console.error("[渲染错误]", error, info?.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="flex min-h-40 flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-12 text-center">
        {React.createElement(iconOf("shieldAlert"), { className: "size-8 text-tertiary-foreground" })}
        <div className="text-[13px] font-medium">{FALLBACK_OF[this.props.level] || FALLBACK_OF.page}</div>
        <Button variant="outline" size="sm" onClick={() => location.reload()}>
          刷新页面
        </Button>
      </div>
    );
  }
}
