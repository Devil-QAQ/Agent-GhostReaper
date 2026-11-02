"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import { cn } from "@/lib/utils";

// 会话顶部信息条(8.5 Agent 会话页组件,P2-07 建、P2-08b 复用):
// 当前模型 · 本会话 token 输入/输出/缓存 · 上下文消耗比例 · 会话时长。
// 数据由调用方从会话视图喂入(shepherd 读会话 API;08b 读各会话),
// 组件只做呈现——四项与 token 独立表 / 会话起止时间同源对账。
//
// info 形状:{ model, input_tokens, output_tokens, cache_tokens,
// context_window, context_ratio, calls };startedAt 为会话首事件时刻
// ("YYYY-MM-DD HH:mm:ss",空 = 会话未开始)。
//
// P4-02 ④ 时长口径 = 有效运行时长(billable):虫群单位会话由调用方
// 喂 billableSeconds(服务端账面合计 + 活动时钟,等待与挂起期间不涨,
// 随轮询刷新);牧者/主宰/插话无时间账,自然时长减冬眠抵扣
// (deductSeconds,含进行中的冬眠)+ frozen 冻结本地走秒——"世界暂停"
// 在界面为真。
export function SessionInfoBar({ info, startedAt, billableSeconds, deductSeconds, frozen, className }) {
  const i = info || {};
  const hasUsage = (i.calls || 0) > 0;
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-lg border border-border/55 bg-card px-4 py-2 text-xs text-muted-foreground",
        className
      )}
    >
      <span className="flex items-center gap-2">
        {React.createElement(iconOf("brain"), { className: "size-3.5" })}
        模型{" "}
        <span className="num text-foreground">
          {i.model ? shortModel(i.model) : "—"}
        </span>
      </span>
      <span className="flex items-center gap-2">
        {React.createElement(iconOf("zap"), { className: "size-3.5" })}
        输入 <span className="num text-foreground">{hasUsage ? fmtNum(i.input_tokens) : "—"}</span>
        {" · "}输出 <span className="num text-foreground">{hasUsage ? fmtNum(i.output_tokens) : "—"}</span>
        {" · "}缓存 <span className="num text-foreground">{hasUsage ? fmtNum(i.cache_tokens) : "—"}</span>
      </span>
      <span className="flex items-center gap-2">
        {React.createElement(iconOf("gauge"), { className: "size-3.5" })}
        上下文{" "}
        {i.context_window > 0 ? (
          <>
            <span className="num text-foreground">
              {hasUsage ? ((i.context_ratio || 0) * 100).toFixed(1) + "%" : "0.0%"}
            </span>
            <span className="relative inline-block h-1.5 w-16 overflow-hidden rounded-full bg-muted">
              <span
                className="absolute inset-y-0 left-0 rounded-full bg-primary"
                style={{
                  width: Math.min(100, (i.context_ratio || 0) * 100) + "%",
                }}
              />
            </span>
            <span title="消耗比例 = 最近请求输入 ÷ 模型上下文窗口">
              {(i.context_ratio || 0) >= 0.8 ? "接近上限" : ""}
            </span>
          </>
        ) : (
          <span className="num text-foreground">—</span>
        )}
      </span>
      <span className="ml-auto flex items-center gap-2" title="有效运行时长:审批等待 / 受阻 / 冬眠 / 挂起期间不计时">
        {React.createElement(iconOf("timer"), { className: "size-3.5" })}
        时长{" "}
        {billableSeconds != null ? (
          <BillableDuration seconds={billableSeconds} />
        ) : (
          <Duration startedAt={startedAt} deduct={deductSeconds || 0} frozen={Boolean(frozen)} />
        )}
      </span>
    </div>
  );
}

// BillableDuration 服务端口径的时长(虫群单位会话):值随轮询刷新,
// 不本地走秒——冻结与续走由服务端读数天然承载。
function BillableDuration({ seconds }) {
  return <span className="num text-foreground">{fmtDuration(seconds)}</span>;
}

// Duration 自然时长 − 冬眠抵扣,每秒刷新;frozen(冬眠中)锚定进入
// 冬眠时刻的读数定值——冻结期不推进不重算(P4-15 ③:抵扣随轮询更新,
// 若逐轮"客户端钟−服务端抵扣"重算,两钟采样差会让读数来回跳);
// 唤醒后随最新抵扣值续走。未开始呈 —;冻结期首次打开按当次采样
// 计一次后定值。
function Duration({ startedAt, deduct, frozen }) {
  const [sec, setSec] = React.useState(null);
  const frozenRef = React.useRef(frozen);
  const secRef = React.useRef(null);
  frozenRef.current = frozen;
  secRef.current = sec;
  React.useEffect(() => {
    if (!startedAt) {
      setSec(null);
      return;
    }
    const compute = () => {
      if (frozenRef.current && secRef.current != null) return; // 冻结期锚定,不重算
      const start = new Date(startedAt.replace(" ", "T")).getTime();
      if (!Number.isFinite(start)) {
        setSec(null);
        return;
      }
      setSec(Math.max(0, (Date.now() - start) / 1000 - deduct));
    };
    compute();
    const t = setInterval(() => {
      if (!frozenRef.current) compute();
    }, 1000);
    return () => clearInterval(t);
  }, [startedAt, deduct]);
  if (sec == null) return <span className="num text-foreground">—</span>;
  return <span className="num text-foreground">{fmtDuration(sec)}</span>;
}

// fmtDuration 秒 → HH:MM:SS(精确,tabular 数字由 num 承载)。
function fmtDuration(sec) {
  let s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

// shortModel 模型名呈现(超长截尾,保留可辨识前缀)。
function shortModel(m) {
  if (m.length <= 28) return m;
  return m.slice(0, 26) + "…";
}

// fmtNum 精确数字(千分位);文风第 5 条:数字精确。
function fmtNum(n) {
  return (n || 0).toLocaleString("en-US");
}
