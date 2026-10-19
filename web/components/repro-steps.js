"use client";

import * as React from "react";

import { ExchangeRaw, statusVariant } from "@/components/exchange-raw";
import { Badge } from "@/components/ui/badge";

// 复现步骤列表(P2-13 建;P3-02 审核迭代升级):收割定稿步骤的统一渲染
// ——发现列表折叠展开与成果卡「复现数据包」二层共用(同一 API 数据、
// 同一组件,面板所见即报告所印)。每步 = 一句话描述(P3-05b,旧记录
// 缺省不显)+ 操作方法 + 数据包:有镜交换 ID 的步骤按 请求包/返回包
// 分侧回放(分段读、各自复制,与流量页同一组件);旧记录无交换 ID
// 退回预渲染原文(面板同口径截断,全文在原始数据/证据包)。
export function ReproStepsList({ tid, steps }) {
  if (!steps || steps.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        (反证轮未产生可复现的流量记录)
      </span>
    );
  }
  return (
    <ol className="flex flex-col gap-3">
      {steps.map((st) => (
        <li key={st.seq} className="flex flex-col gap-2 rounded-md border bg-card p-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Badge variant="outline">步骤 {st.seq}</Badge>
            {st.round > 0 && <Badge variant="info">第 {st.round} 轮</Badge>}
            {st.status ? <Badge variant={statusVariant(st.status)}>{st.status}</Badge> : null}
          </div>
          {st.desc && <div className="text-[13px] leading-relaxed">{st.desc}</div>}
          {st.op && (
            <div className="truncate font-mono text-xs text-muted-foreground" title={st.op}>
              {st.op}
            </div>
          )}
          {st.note && <div className="text-xs text-status-warning">{st.note}</div>}
          {st.exchange_id && tid ? (
            <ExchangeRaw tid={tid} exID={st.exchange_id} bare />
          ) : st.raw ? (
            <div className="flex flex-col gap-1">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                HTTP 原文(Burp 可验证)
                {st.raw_clip && (
                  <span className="text-status-warning">(面板截断,全文在原始数据)</span>
                )}
              </div>
              <pre className="max-h-72 overflow-auto rounded-md bg-muted/50 p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
                {st.raw}
              </pre>
              {/* P3-11a ③:hash 收悬停,主视觉只留"原始数据"一行 */}
              <div className="font-mono text-[10px] text-muted-foreground" title={st.blob_ref}>
                原始数据
              </div>
            </div>
          ) : null}
        </li>
      ))}
    </ol>
  );
}
