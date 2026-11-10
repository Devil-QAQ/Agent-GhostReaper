"use client";

import * as React from "react";

import { ExchangeRaw } from "@/components/exchange-raw";

// 复现步骤列表(P2-13 建;P3-02 审核迭代;P4-08 改口径;P4-35 最小集
// 三层结构):收割定稿步骤的统一渲染——发现列表折叠展开与成果卡「复现
// 数据包」二层共用(同一 API 数据、同一组件,面板所见即报告所印)。
// 每步三层(P4-35 ②):标题 = 序号+短名("① 拿会话凭据"式;旧记录无
// 短名退"步骤 N")+ 做什么(指令式人话,P4-08 ④ 定稿时模型生成并冻结)
// + 作用(次级色小字)。数据包:有镜交换 ID 的步骤按 请求包/返回包
// 分侧回放(与流量页同一组件;请求包 origin-form、带复制,复制即贴
// Burp Repeater 可发——P4-08 ⑥/P4-35 ③;返回包无复制);默认全部
// 展开,超长正文限高内滚;旧记录无交换 ID 退回预渲染原文(面板同口径
// 截断,全文在原始数据/证据包)。工具调用 op 行撤下面板(P4-08 ⑤:
// 证据包收);对比交换/反证变体/枚举试错等过程流量不进列表,只报计数
// (P4-35 ①:全量在证据包)。

// stepOrdinal 步骤序号词形:1~20 圈号(①..⑳),超出"N."(与报告
// reproStepHead 同款口径,所见即所印)。
function stepOrdinal(n) {
  if (n >= 1 && n <= 20) return String.fromCodePoint(0x245f + n);
  return `${n}.`;
}

// stepHead 步骤标题:序号+短名;序号按列表位置(最小集是候选池子集,
// 存储 seq 保留候选编号,呈现序号与存储解耦——与后端 reproStepHead
// 同款口径);旧记录无短名退"步骤 N"。
function stepHead(pos, st) {
  return st.short ? `${stepOrdinal(pos)} ${st.short}` : `步骤 ${pos}`;
}

// processUnlisted 未进最小集的过程交换条数(P4-35 ①;与后端
// ReproFinal.ProcessUnlisted 同口径:新记录 = process_count − 步骤总数,
// 旧记录 = variants_count,负值按 0。P4-36 起计数随详情下发,不再依赖
// 大数组)。
export function processUnlisted(repro, stepsTotal = 0) {
  if (!repro) return 0;
  const n = Math.max(0, (repro.process_count || 0) - stepsTotal);
  return n + (repro.variants_count || 0);
}

// emptyReproText 空复现区文案:P4-62 A 起兜底直出废除——无复现包的
// 定稿带冻结原因句(no_repro_reason,面板与报告同句);旧记录无原因
// 退既有空态句。
function emptyReproText(repro) {
  return repro?.no_repro_reason || "(反证轮未产生可复现的流量记录)";
}

export function ReproStepsList({ tid, steps, process = 0 }) {
  if (!steps || steps.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        (反证轮未产生可复现的流量记录)
      </span>
    );
  }
  return (
    <ol className="flex flex-col gap-3">
      {steps.map((st, i) => (
        <ReproStepItem key={st.seq || i} pos={i + 1} st={st} tid={tid} />
      ))}
      {process > 0 && (
        <li className="text-xs text-muted-foreground">
          过程流量 {process} 条不在此列，全量在证据包
        </li>
      )}
    </ol>
  );
}

// ReproStepItem 单步卡片(三层 + 数据包;列表与分段共用同一渲染)。
function ReproStepItem({ pos, st, tid }) {
  return (
    <li className="flex flex-col gap-2 rounded-md border border-border/55 bg-card p-3">
      <div className="text-[13px] font-medium leading-relaxed">{stepHead(pos, st)}</div>
      {st.desc && <div className="text-[13px] leading-relaxed">{st.desc}</div>}
      {st.purpose && (
        <div className="text-xs leading-relaxed text-muted-foreground">作用:{st.purpose}</div>
      )}
      {st.unpolished && (
        <div className="text-xs text-muted-foreground">文字未过文风校验，按原文保留</div>
      )}
      {st.note && <div className="text-xs text-status-warning">{st.note}</div>}
      {st.exchange_id && tid ? (
        <ExchangeRaw tid={tid} exID={st.exchange_id} bare respCopy={false} />
      ) : st.raw ? (
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            HTTP 原文(Burp 可验证)
            {st.raw_clip && (
              <span className="text-status-warning">(面板截断,全文在原始数据)</span>
            )}
          </div>
          {st.tls_word && (
            <div className="text-xs text-muted-foreground">目标证书:{st.tls_word}</div>
          )}
          <pre className="max-h-72 overflow-auto rounded-md bg-muted/50 p-2 num text-xs leading-relaxed whitespace-pre-wrap break-all">
            {st.raw}
          </pre>
          {/* P3-11a ③:hash 收悬停,主视觉只留"原始数据"一行 */}
          <div className="num text-[10px] text-muted-foreground" title={st.blob_ref}>
            原始数据
          </div>
        </div>
      ) : null}
    </li>
  );
}

// ReproStepsPaged 复现步骤分段拉取(P4-36 ②):详情只带计数,步骤按页
// 端点(/claims/{cid}/repro)逐页拉齐、边到边渲染——存量巨记录(数百步)
// 逐页消化;渲染与 ReproStepsList 同一组件,视觉零改动。依赖锚是
// repro.rev 的值(轮询刷新下对象身份会漂移,值不变不重启;重开再确认
// 后新 rev 落定自动重拉)。
export function ReproStepsPaged({ tid, claimID, repro, stepsTotal = 0 }) {
  const [steps, setSteps] = React.useState([]);
  const [done, setDone] = React.useState(false);
  const [failed, setFailed] = React.useState(null);
  const targetRev = repro ? repro.rev : null;

  React.useEffect(() => {
    if (targetRev == null) return;
    let dead = false;
    setSteps([]);
    setDone(false);
    setFailed(null);
    (async () => {
      const collected = [];
      let offset = 0;
      let hasMore = true;
      try {
        while (hasMore && !dead) {
          const r = await fetch(
            `/api/tasks/${tid}/claims/${encodeURIComponent(claimID)}/repro?offset=${offset}`,
          );
          const b = await r.json().catch(() => ({}));
          if (dead) return;
          if (!r.ok) {
            setFailed(b?.error || `HTTP ${r.status}`);
            return;
          }
          collected.push(...(b.steps || []));
          setSteps([...collected]);
          hasMore = Boolean(b.has_more);
          offset = (b.offset || 0) + (b.steps || []).length;
        }
        if (!dead) setDone(true);
      } catch {
        if (!dead) setFailed("网络未连通");
      }
    })();
    return () => {
      dead = true;
    };
  }, [tid, claimID, targetRev]);

  if (!repro) {
    return (
      <span className="text-xs text-muted-foreground">
        (反证轮未产生可复现的流量记录)
      </span>
    );
  }
  if (failed && steps.length === 0) {
    return <span className="text-xs text-status-danger">读取失败:{failed}</span>
  }
  if (steps.length === 0) {
    return done ? (
      <span className="text-xs text-muted-foreground">{emptyReproText(repro)}</span>
    ) : (
      <span className="text-xs text-muted-foreground">读取复现步骤…</span>
    )
  }
  return (
    <ReproStepsList tid={tid} steps={steps} process={processUnlisted(repro, stepsTotal || steps.length)} />
  );
}
