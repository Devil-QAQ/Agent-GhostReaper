"use client";

import * as React from "react";

// NodeRefButton 节点引用按钮(fact#/tc#/资产锚点等引用的可点击形态):
// 点击换抽屉下钻(不离开上下文);无宿主时退化为纯文本。
// 自 node-sheets 抽出共用(P4-12:攻击链条卡的对象出生区同样引用,
// 单一实现防平行)。
export function NodeRefButton({ meta, label, onSelectNode, children }) {
  if (!onSelectNode || !meta?.id) {
    return <span className="font-mono text-xs text-muted-foreground">{label ?? meta?.id}</span>;
  }
  return (
    <button
      className="rounded border border-border bg-card px-2 py-0.5 font-mono text-xs text-muted-foreground transition-colors duration-150 hover:bg-hover-lift hover:text-foreground cursor-pointer"
      title={`查看${meta.typeLabel ?? ""} ${meta.id}`}
      onClick={() => onSelectNode(meta)}
    >
      {children ?? (meta.typeLabel ? `${meta.typeLabel} ` : "") + meta.id}
    </button>
  );
}
