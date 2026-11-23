"use client";

import * as React from "react";
import { toast } from "sonner";
import { ReadableText } from "@/components/readable-text";

import { ClaimChainGraph } from "@/components/claim-chain-graph";
import { ClaimFollowupDialog } from "@/components/claim-followup-dialog";
import { CopyIDButton } from "@/components/copy-id-button";
import { NodeRefButton } from "@/components/node-ref-button";
import { iconOf } from "@/components/icons";
import { ExchangeRaw, statusVariant } from "@/components/exchange-raw";
import { ReproStepsPaged } from "@/components/repro-steps";
import { downloadExport } from "@/components/export-dialog";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetNavigationContext,
} from "@/components/ui/sheet";
import { FloatingLayerContext } from "@/components/ui/floating-layer";
import {
  FACT_RESULT_META,
  CLAIM_STATUS,
  EDGE_LABELS,
  FACT_STATUS,
  GOAL_STATUS,
  REPLAY_VERDICT,
  RETEST_VERDICT,
  STEP_STATUS,
  assetClassMeta,
  assetSourceMeta,
  claimDisplayMeta,
  jumpToNodeView,
  nodeTypeMeta,
  postJSON,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { TranscriptEntryView } from "@/views/sessions-page";


// ---- 通用件 ----

// ViewInGraphButton「在图中查看」:跳节点对应视图(资产→资产树定位,
// 其余→全景图聚焦),可直达的 URL 定位参数随行。
function ViewInGraphButton({ tid, nodeID, type, onClose }) {
  const graphTarget = type !== "asset" && type !== "goal";
  const { data, error } = usePolling(graphTarget ? `/api/tasks/${tid}/pview/links/${encodeURIComponent(nodeID)}` : "/api/healthz", 30000, {
    immediate: graphTarget, live: graphTarget ? [`pview:${tid}`] : null,
  });
  if (type === "goal" || (graphTarget && (!data || error))) return null;
  return (
    <Button
      variant="outline"
      size="sm"
      className="ml-auto shrink-0"
      onClick={() => {
        onClose?.();
        jumpToNodeView(tid, nodeID, type);
      }}
    >
      在图中查看
    </Button>
  );
}

// ViewInFactsButton「在事实列表查看」(P2-10 事实页签互跳):事实抽屉
// 跳回发现页·事实页签并定位该条(?focus= 定位参数)。
function ViewInFactsButton({ tid, nodeID, onClose }) {
  return (
    <Button
      variant="outline"
      size="sm"
      className="shrink-0"
      onClick={() => {
        onClose?.();
        location.hash = `/task/${tid}/findings?focus=${encodeURIComponent(nodeID)}`;
      }}
    >
      在事实列表查看
    </Button>
  );
}

// NodeTypeBadge 五类节点类型徽标(任务投影节点视觉语言的徽标形态)。
export function NodeTypeBadge({ type }) {
  const meta = nodeTypeMeta(type);
  return <Badge variant={meta.tone}>{meta.label}</Badge>;
}

function DetailSection({ title, children }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs font-medium tracking-wide text-muted-foreground">{title}</div>
      <div className="flex flex-col gap-1">{children}</div>
    </div>
  );
}

function Muted({ children }) {
  return <span className="text-xs text-muted-foreground">{children}</span>;
}

// ---- 通用节点抽屉(goal / step / fact) ----

const KIND_LABELS = { falsify: "反证" };

function detailValue(value) {
  if (value === null || value === undefined) return "";
  return typeof value === "object" ? JSON.stringify(value, null, 2) : String(value);
}

// 普通说明使用正文排印；命令与报文才使用等宽证据区。段落不按字数裁切。
const DetailText = ReadableText;

function DetailDisclosure({ title, children, count }) {
  return <details className="group/detail rounded-xl bg-muted/35">
    <summary className="flex cursor-pointer list-none items-center gap-2 rounded-xl px-4 py-3 text-sm hover:bg-hover-lift transition-colors [&::-webkit-details-marker]:hidden">
      {React.createElement(iconOf("chevronRight"), { className: "size-3.5 shrink-0 text-muted-foreground transition-transform duration-150 group-open/detail:rotate-90" })}
      <span>{title}</span>
      {count !== undefined && <span className="ml-auto num text-xs text-muted-foreground">{count}</span>}
    </summary>
    <div className="px-4 pb-4 pt-1">{children}</div>
  </details>;
}

function DetailProperties({ rows }) {
  const visible = rows.filter(([, value]) => value !== undefined && value !== null && value !== "");
  if (!visible.length) return null;
  return <dl className="flex flex-col gap-3">{visible.map(([label, value]) => <div key={label} className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
    <dt className="w-20 shrink-0 text-muted-foreground">{label}</dt>
    <dd className="min-w-0 flex-1 break-words whitespace-pre-wrap">{value}</dd>
  </div>)}</dl>;
}

function FactResultBadges({ kinds }) {
  return <>{(Array.isArray(kinds) && kinds.length ? kinds : ["unclassified"]).map(kind => {
    const meta = FACT_RESULT_META[kind] || FACT_RESULT_META.unclassified;
    return <Badge key={kind} variant={meta.variant} title={meta.hint}>{meta.label}</Badge>;
  })}</>;
}

function EvidenceDetail({ detail, tid, nodeID }) {
  const entries = [["证据摘要", detail?.evidence_head], ["证据记录", detail?.evidence]]
    .filter(([, value], i, all) => value && !all.slice(0, i).some(([, prior]) => detailValue(prior) === detailValue(value)));
  if (!entries.length && !detail?.evidence_ref) return null;
  return <DetailDisclosure title={detail?.evidence_ref ? "证据摘要" : "证据"} count={entries.length || undefined}>
    <div className="flex flex-col gap-4">{entries.map(([label, value]) => <div key={label} className="space-y-2">
      {entries.length > 1 && <div className="text-xs text-muted-foreground">{label}</div>}
      <pre className="max-h-72 overflow-auto rounded-lg bg-input-surface p-3 num text-xs leading-relaxed whitespace-pre-wrap break-words" tabIndex={0}>{detailValue(value)}</pre>
    </div>)}</div>
    {detail?.evidence_ref && tid && nodeID && <a className="mt-3 inline-flex text-sm text-primary hover:underline" href={`/api/tasks/${tid}/nodes/${encodeURIComponent(nodeID)}/evidence`}>下载原文</a>}
  </DetailDisclosure>;
}

function nodeStatusBadge(type, status) {
  if (type === "goal") {
    const meta = GOAL_STATUS[status];
    return meta ? <Badge variant={meta.variant}>{meta.label}</Badge> : null;
  }
  if (type === "fact") {
    const meta = FACT_STATUS[status];
    return meta ? <StatusBadge meta={meta} /> : null;
  }
  if (type === "step") return <Badge variant={status === "done" ? "success" : status === "running" ? "running" : "paused"}>{STEP_STATUS[status] || status}</Badge>;
  return null;
}

function DetailAssets({ assets, onSelectNode }) {
  if (!assets?.length) return null;
  return <DetailSection title="关联资产"><div className="flex flex-col items-start gap-2">{assets.map(asset =>
    <NodeRefButton key={asset.id} meta={{ id: asset.id, type: "asset", typeLabel: "资产" }} onSelectNode={onSelectNode}>{asset.title || asset.id}</NodeRefButton>
  )}</div></DetailSection>;
}

function FactResultDetail({ result, onSelectNode, tid }) {
  const name = result.polished_title;
  const explanation = result.polished_statement || result.title;
  return <article className="space-y-3 rounded-xl bg-card p-4" data-detail-result={result.id}>
    <div className="flex flex-wrap items-center gap-2">
      <button className="min-w-0 text-left text-sm font-medium leading-relaxed break-words hover:text-primary transition-colors cursor-pointer" title="查看事实详情" onClick={() => onSelectNode?.({ id: result.id, type: "fact" })}>{name || "事实结果"}</button>
      {nodeStatusBadge("fact", result.status)}
    </div>
    {explanation !== name && <DetailText text={explanation} />}
    <EvidenceDetail detail={result.detail} tid={tid} nodeID={result.id} />
    {result.polished_statement && result.polished_statement !== result.title && <DetailDisclosure title="原始陈述"><DetailText text={result.title} /></DetailDisclosure>}
  </article>;
}

// 意图始终共用合并详情：先看意图和实际产出，再按需查执行与证据。
export function GenericNodeSheet({ tid, nodeID, nodeType, graph, onClose, onSelectNode, includeResults = false, resultIDs }) {
  const { data, error } = usePolling(
    nodeID ? includeResults ? `/api/tasks/${tid}/pview/details/${encodeURIComponent(nodeID)}${resultIDs ? `?facts=${encodeURIComponent(resultIDs.join(","))}` : ""}` : `/api/tasks/${tid}/nodes/${nodeID}` : "/api/healthz",
    3000,
    { immediate: Boolean(nodeID) },
  );
  const [followup, setFollowup] = React.useState(false);
  const n = includeResults ? data?.intent || null : data?.id ? data : null;
  const type = nodeType || n?.type;
  const lineage = React.useMemo(() => {
    if (!graph || !n) return [];
    const nodes = new Map((graph.nodes || []).map(node => [node.id, node]));
    return (graph.edges || []).flatMap(edge => {
      if (!EDGE_LABELS[edge.type]) return [];
      const upstream = edge.to === n.id;
      const id = upstream ? edge.from : edge.from === n.id ? edge.to : null;
      return id && nodes.has(id) ? [{ key: `${edge.type}-${upstream}-${id}`, node: nodes.get(id), relation: (upstream ? "上游 · " : "下游 · ") + EDGE_LABELS[edge.type] }] : [];
    });
  }, [graph, n]);
  if (!nodeID) return null;
  const detail = n?.detail || {};
  const name = n?.polished_title || (type === "goal" ? n?.title : nodeTypeMeta(type).label);
  const explanation = n?.polished_statement || (n?.title !== name ? n?.title : "");
  const assets = includeResults ? (data?.sources || []).filter(source => source.relation === "covers") : n?.assets;
  const sources = includeResults ? (data?.sources || []).filter(source => source.relation !== "covers") : [];
  const execution = n ? [
    ["优先级", ({ high: "高", normal: "普通", low: "低" })[detail.priority] || detail.priority],
    ["意图分级", detail.action_level],
    ["类型", KIND_LABELS[detail.kind]],
    ["反证轮次", detail.round],
    ["关联判断", detail.testclaim_id ? <NodeRefButton meta={{ id: detail.testclaim_id, type: "testclaim", typeLabel: "判断" }} onSelectNode={onSelectNode} /> : null],
    ["单位", n.unit_id || detail.unit_id],
    ["会话记录", (n.transcript_id || detail.transcript_id) ? <a className="text-primary hover:underline" href={`#/task/${tid}/sessions?unit=${encodeURIComponent(n.transcript_id || detail.transcript_id)}`}>{n.transcript_id || detail.transcript_id}</a> : null],
    ["归属目标", detail.goal_id ? <NodeRefButton meta={{ id: detail.goal_id, type: "goal", typeLabel: "目标" }} onSelectNode={onSelectNode} /> : null],
  ].filter(([, value]) => value !== undefined && value !== null && value !== "") : [];
  return <Sheet navigationFrame open={Boolean(nodeID)} onOpenChange={open => !open && onClose()}>
    <SheetContent className="overflow-y-auto" data-detail-kind={type}>
      {!n ? <div className="p-5 text-sm text-muted-foreground">{error ? `读取失败:${error}` : "读取中…"}</div> : <>
        <SheetHeader className="gap-4 pb-0">
          <div className="flex flex-wrap items-center gap-2 pr-4">
            <NodeTypeBadge type={type} />
            {nodeStatusBadge(type, n.status)}
            <CopyIDButton id={n.id} noun="节点标识" className="size-5" />
          </div>
          <SheetTitle className="break-words leading-relaxed">{name}</SheetTitle>
          <SheetDescription className="sr-only">{nodeTypeMeta(type).label}详情 · {n.id}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 p-5 text-sm">
          {explanation && <DetailText text={explanation} />}
          <DetailAssets assets={assets} onSelectNode={onSelectNode} />
          {type === "fact" && <div className="flex flex-wrap items-center gap-2"><FactResultBadges kinds={n.result_types} /><Button variant="outline" size="sm" title="可问相关问题，也可让它深入继续挖" onClick={() => setFollowup(true)}>{React.createElement(iconOf("messageCircle"), { className: "size-3.5" })}追问</Button></div>}
          {includeResults && <DetailSection title={`${resultIDs ? "相关事实" : "实际结果"} · ${data?.results?.length || 0} 条事实`}>
            {!data?.results?.length ? <p className="py-2 text-sm text-muted-foreground">{resultIDs ? "未记录关联事实" : "尚未产出事实"}</p> : <div className="flex flex-col gap-4">{data?.results?.map(result => <FactResultDetail key={result.id} result={result} onSelectNode={onSelectNode} tid={tid} />)}</div>}
          </DetailSection>}
          {detail.new_asset?.title && <DetailSection title="新发现资产"><DetailProperties rows={[["名称", detail.new_asset.title], ["类型", assetClassMeta(detail.new_asset.class).label], ["父资产", detail.new_asset.parent ? <NodeRefButton meta={{ id: detail.new_asset.parent, type: "asset", typeLabel: "资产" }} onSelectNode={onSelectNode} /> : null]]} /></DetailSection>}
          {type === "fact" && <EvidenceDetail detail={detail} tid={tid} nodeID={n.id} />}
          {type === "goal" && detail.predicate && <DetailSection title="完成条件"><DetailText text={detailValue(detail.predicate)} /></DetailSection>}
          {sources.length > 0 && <DetailDisclosure title="来源与关联" count={sources.length}><div className="space-y-3">{sources.map(source => <div key={`${source.relation}-${source.id}`} className="space-y-1"><div className="text-xs text-muted-foreground">{source.relation === "depends_on" ? "执行前置" : "来源"}</div><NodeRefButton meta={{ id: source.id, type: source.type, typeLabel: nodeTypeMeta(source.type).label }} onSelectNode={onSelectNode}>{source.polished_title || source.title}</NodeRefButton></div>)}</div></DetailDisclosure>}
          {(execution.length > 0 || detail.expected_observation || detail.verify_intent || detail.harm_class || detail.redispatch_reason || detail.cancellation_reason) && <DetailDisclosure title="执行信息">
            <div className="space-y-5">
              <DetailProperties rows={execution} />
              {[["预期观察", detail.expected_observation], ["验证意图", detail.verify_intent], ["危害声明", detail.harm_class], ["重派原因", detail.redispatch_reason], ["取消理由", detail.cancellation_reason]].filter(([, value]) => value).map(([label, value]) => <DetailSection key={label} title={label}><DetailText text={detailValue(value)} /></DetailSection>)}
            </div>
          </DetailDisclosure>}
          {(n.polished_title || n.polished_statement) && n.title !== name && n.title !== explanation && <DetailDisclosure title={type === "step" ? "完整意图" : "原始陈述"}><DetailText text={n.title} /></DetailDisclosure>}
          {lineage.length > 0 && !includeResults && <DetailDisclosure title="来源与去向" count={lineage.length}><div className="space-y-3">{lineage.map(({ key, node, relation }) => <div key={key} className="space-y-1"><div className="text-xs text-muted-foreground">{relation}</div><NodeRefButton meta={{ id: node.id, type: node.type, typeLabel: nodeTypeMeta(node.type).label }} onSelectNode={onSelectNode}>{node.title || node.id}</NodeRefButton></div>)}</div></DetailDisclosure>}
          <div className="flex flex-wrap items-center gap-2 pt-1"><ViewInGraphButton tid={tid} nodeID={n.id} type={type} onClose={onClose} />{type === "fact" && <ViewInFactsButton tid={tid} nodeID={n.id} onClose={onClose} />}</div>
        </div>
      </>}
    </SheetContent>
    {type === "fact" && <ClaimFollowupDialog tid={tid} anchor={{ kind: "fact", id: n?.id }} open={followup} onClose={() => setFollowup(false)} />}
  </Sheet>;
}

// ---- 资产详情抽屉(原 assets-page 迁入,P2-09 起引用可下钻)----

export function AssetDetailSheet({ tid, nodeID, onClose, onSelectNode }) {
  const { data, error } = usePolling(
    nodeID ? `/api/tasks/${tid}/assets/${nodeID}` : "/api/healthz",
    2000,
    { immediate: Boolean(nodeID) },
  );
  if (!nodeID) return null;
  const a = data?.asset;
  const meta = a ? assetClassMeta(a.class) : null;
  return (
    <Sheet navigationFrame open={Boolean(nodeID)} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="overflow-y-auto">
        {!a ? (
          <div className="p-5 text-sm text-muted-foreground">
            {error ? `读取失败:${error}` : "读取中…"}
          </div>
        ) : (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-start gap-2 pr-4">
                {React.createElement(iconOf(meta.icon), { className: "size-4 shrink-0 text-muted-foreground" })}
                <span className="min-w-0 break-all">{a.title || a.id}</span>
              </SheetTitle>
              <SheetDescription>
                {meta.label} · <span className="num" title={`版本号 ${a.created_rev}`}>{a.id}</span>
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                {a.excluded ? (
                  <Badge variant="outline" className="text-muted-foreground">
                    {React.createElement(iconOf("shieldBan"), { className: "size-3" })}
                    范围外
                  </Badge>
                ) : (
                  <Badge variant={a.covered ? "success" : "paused"}>
                    {a.covered ? "已覆盖" : "未测"}
                  </Badge>
                )}
              </div>
              {[
                ["相关事实", data.facts, "fact"],
                ["相关漏洞", data.claims, "testclaim"],
              ].filter(([, items]) => items?.length > 0).map(([title, items, type]) => (
                <DetailSection key={type} title={title}>
                  {items.map(item => <div key={item.id} className="flex min-w-0 items-start gap-2">
                    <Button variant="ghost" className="h-auto min-w-0 flex-1 justify-start whitespace-normal px-0 py-1 text-left text-sm font-normal" title={item.title || item.id} onClick={() => onSelectNode?.({ id: item.id, type })}>
                      <span className="line-clamp-2 break-all">{item.title || item.id}</span>
                    </Button>
                    <StatusBadge meta={(type === "fact" ? FACT_STATUS : CLAIM_STATUS)[item.status] || { label: item.status, variant: "info" }} />
                  </div>)}
                </DetailSection>
              ))}
              {(data.relations || []).length > 0 && <DetailDisclosure title="资产关联" count={data.relations?.length}>
                <div className="space-y-3">{data.relations?.map(relation => <div key={`${relation.kind}:${relation.from}:${relation.to}`} className="space-y-1 text-xs">
                  <div className="flex flex-wrap items-center gap-2"><NodeRefButton meta={{id:relation.from,type:"asset",typeLabel:"资产"}} onSelectNode={onSelectNode}>{relation.from_title}</NodeRefButton><span className="text-muted-foreground">{relation.label}</span><NodeRefButton meta={{id:relation.to,type:"asset",typeLabel:"资产"}} onSelectNode={onSelectNode}>{relation.to_title}</NodeRefButton></div>
                  <div className="flex flex-wrap items-center gap-2 text-muted-foreground"><span>依据</span>{relation.fact_ids?.map(id => <NodeRefButton key={id} meta={{id,type:"fact",typeLabel:"事实"}} onSelectNode={onSelectNode}/>)}</div>
                </div>)}</div>
              </DetailDisclosure>}
              <DetailDisclosure title="来源">
                {a.source === "discovered" ? (
                  <div className="flex flex-wrap items-center gap-2 text-xs">
                    <span>发现入图</span>
                    {a.birth_unit && <span className="text-muted-foreground">单位 <span className="num">{a.birth_unit}</span></span>}
                    {a.birth_step && <NodeRefButton meta={{ id: a.birth_step, type: "step", typeLabel: "步骤" }} onSelectNode={onSelectNode} />}
                    {a.birth_fact && <NodeRefButton meta={{ id: a.birth_fact, type: "fact", typeLabel: "事实" }} onSelectNode={onSelectNode} />}
                  </div>
                ) : <span className="text-xs">{a.source === "manual" ? "手动补入" : "建任务种子入图"}</span>}
              </DetailDisclosure>
              <div><ViewInGraphButton tid={tid} nodeID={a.id} type="asset" onClose={onClose} /></div>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}


const DECISION_META = {
  confirm: { label: "已确认", tone: "success" },
  retake: { label: "待补考", tone: "running" },
  insufficient: { label: "证据不足", tone: "warning" },
  refuted: { label: "被推翻", tone: "info" },
  pending: { label: "证据不足(旧词)", tone: "warning" },
  advance_round: { label: "待下一轮反证(旧词)", tone: "running" },
};
const STATE_LABELS = {
  candidate: "待机器核验", confirmed: "已确认", insufficient: "证据不足",
  refuted: "被推翻", discarded: "已废弃", fixed: "已修复",
};
const REOPEN_TRIGGERS = [["new_evidence", "新反证"], ["manual", "人工复核"], ["expired", "结论过期"]];
const COND_LABELS = {
  evidence_traceable: "证据可追溯", countercheck_done: "自查有记录", standard_frozen: "标准未变",
  fact_confirmed: "事实已确认", tool_observed: "工具已观测",
};

function condLabel(kind) {
  return COND_LABELS[kind] || kind;
}

// ClaimDetailSheet 发现详情(8.8 完整版):头部(状态/类别/级别)+ 两维
// 声明分开 + 时间线 + 复现步骤 + 重开;资产锚点与攻击链
// 环节可下钻(跳转协议),头部「在图中查看」跳任务投影聚焦。
// 审核迭代 2:成果卡只留最终成果——名称/描述/修复建议 + 复现数据包与
// 攻击链条两个二层入口 + 在图中查看;过程性内容不上面板卡(报告导出
// 与账本留档)。导出与重开留底部(功能入口不删)。
// P5-03③:旧反证时间线逐轮人话与机器层已随反证流程退役删除,时间线
// 呈现交卷/状态/核验/重开条目;竞争解释已随 P5-02⑥ 砍除。
export function ClaimDetailSheet({ tid, claimID, onClose, onSelectNode }) {
  const { data, error } = usePolling(
    claimID ? `/api/tasks/${tid}/claims/${claimID}` : "/api/healthz",
    2000,
    { immediate: Boolean(claimID) },
  );
  // 第二层卡片:null = 未开;"repro" 复现数据包 | "chain" 攻击链条 | "reopen" 重开。
  // 状态入 claimLayerMemory:下钻换卡卸载本卡,返回时恢复到离开时的层。
  const [layer, setLayer] = React.useState(null);
  // 追问大窗(P5-07②):与描述 / 修复建议 / 数据与链路并列的追问区
  // 入口,一条目一会话、续接不新建。
  const [followup, setFollowup] = React.useState(false);
  React.useEffect(() => {
    setLayer(claimLayerMemory.get(claimID) || null);
  }, [claimID]);
  const openLayer = (l) => {
    setLayer(l);
    if (l) claimLayerMemory.set(claimID, l);
    else claimLayerMemory.delete(claimID);
  };

  // 发起复测(P3-06):POST 即返(会话异步驱动),层卡里盯进度。
  const startRetest = async () => {
    const { ok, status, body } = await postJSON(
      `/api/tasks/${tid}/claims/${claimID}/retest`,
      {},
    );
    if (ok) {
      toast.success(`复测第 ${body.round} 轮已发起`);
      openLayer("retest");
    } else {
      toast.error(`发起失败:${body?.error || `HTTP ${status}`}`);
    }
  };

  // 发起重放校验(P3-07):POST 即返(确定性驱动,零模型),层卡里看
  // 逐条交换的新旧对照。
  const startReplay = async () => {
    const { ok, status, body } = await postJSON(
      `/api/tasks/${tid}/claims/${claimID}/replay`,
      {},
    );
    if (ok) {
      toast.success(`重放第 ${body.round} 轮已发起`);
      openLayer("replay");
    } else {
      toast.error(`发起失败:${body?.error || `HTTP ${status}`}`);
    }
  };

  if (!claimID) return null;
  if (!data) {
    return (
      <Sheet navigationFrame open onOpenChange={(o) => !o && onClose()}>
        <SheetContent className="overflow-y-auto">
          <SheetHeader>
            <SheetTitle className="num">{claimID}</SheetTitle>
            <SheetDescription>
              {error ? `读取失败:${error}` : "读取发现详情…"}
            </SheetDescription>
          </SheetHeader>
        </SheetContent>
      </Sheet>
    );
  }
  const claim = data.claim || {};
  const st = claimDisplayMeta(claim) || CLAIM_STATUS[claim.status] || { label: claim.status, variant: "info" };
  const sev = severityMeta(claim.severity || data.repro?.severity);
  const repro = data.repro || null;
  const timeline = data.timeline || [];

  return (
    <>
      <Sheet navigationFrame open onOpenChange={(o) => !o && onClose()}>
        <SheetContent className="overflow-y-auto">
          <SheetHeader className="gap-4 pb-0">
            <div className="flex flex-wrap items-center gap-2 pr-4">
              <StatusBadge meta={st} />
              {sev && <Badge variant={sev.variant}>{sev.label}</Badge>}
              {claim.vulnclass && <Badge variant="outline">{claim.vulnclass}</Badge>}
              <CopyIDButton id={claim.id} noun="判断标识" className="size-5" />
            </div>
            <SheetTitle className="break-words leading-relaxed">{claim.title || "漏洞详情"}</SheetTitle>
            <SheetDescription className="sr-only">{claim.id}</SheetDescription>
          </SheetHeader>
          <div className="flex flex-col gap-6 p-5 text-sm">
            <DetailSection title="漏洞描述"><DetailText text={claim.statement} /></DetailSection>
            <DetailAssets assets={claim.assets} onSelectNode={onSelectNode} />
            {claim.status === "pending" && claim.pending_reason && (
              <div className="rounded-md bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
                证据不足:{claim.pending_reason}
              </div>
            )}
            {claim.report_excluded && (
              <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
                不纳入报告:{claim.report_excluded_reason || "信息性观察,无直接危害"}
              </div>
            )}

            <DetailSection title="影响范围"><DetailText text={claim.impact || "未申报影响范围"} /></DetailSection>
            {data.impact_note && data.impact_note !== claim.impact && <DetailDisclosure title="申报影响"><DetailText text={data.impact_note} /></DetailDisclosure>}

            <DetailSection title="修复建议">
              {(data.remediation || []).length > 0 ? <ol className="list-decimal space-y-2 pl-5 text-sm leading-relaxed">{data.remediation?.map((item, i) => <li key={i}>{item}</li>)}</ol> : <Muted>{claim.vulnclass ? "该类型暂无预置建议" : "未申报类型"}</Muted>}
            </DetailSection>
            <div><Button variant="outline" size="sm" title="可问相关问题，也可让它深入继续挖" onClick={() => setFollowup(true)}>{React.createElement(iconOf("messageCircle"), { className: "size-3.5" })}追问</Button></div>
            {(timeline.length > 0 || data.timing?.landed_at) && <DetailDisclosure title="验证记录" count={timeline.length}>
              <div className="space-y-5">
                {data.timing?.landed_at && <DetailProperties rows={[
                  ["起止时间", fmtTimingSpan(data.timing.landed_at, data.timing.terminal_at)],
                  ["排队", fmtDur(data.timing.queue_seconds)],
                  ["执行", fmtDur(data.timing.exec_seconds)],
                  ["验证轮次", data.timing.rounds],
                ]} />}
                {timeline.length > 0 && <ol className="relative flex flex-col gap-3 border-l pl-4">{timeline.map((entry, i) => <TimelineEntry key={`${entry.rev}-${entry.kind}-${i}`} entry={entry} />)}</ol>}
              </div>
            </DetailDisclosure>}

            {/* 数据与链路入口:细节入第二层卡片,关闭即回本层 */}
            <section className="flex flex-col gap-1">
              <div className="text-xs font-medium tracking-wide text-muted-foreground">数据与链路</div>
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" className="h-8 text-xs" onClick={() => openLayer("repro")}>
                  {React.createElement(iconOf("terminal"), { className: "size-3.5" })}
                  复现数据包
                  {React.createElement(iconOf("chevronRight"), { className: "size-3.5 text-muted-foreground" })}
                </Button>
                <Button variant="outline" className="h-8 text-xs" onClick={() => openLayer("chain")}>
                  {React.createElement(iconOf("gitBranch"), { className: "size-3.5" })}
                  攻击链条
                  {React.createElement(iconOf("chevronRight"), { className: "size-3.5 text-muted-foreground" })}
                </Button>
                {data.origin && (data.origin.discoverer || (data.origin.falsifiers || []).length > 0) && (
                  <Button variant="outline" className="h-8 text-xs" onClick={() => openLayer("origin")}>
                    {React.createElement(iconOf("history"), { className: "size-3.5" })}
                    溯源
                    {React.createElement(iconOf("chevronRight"), { className: "size-3.5 text-muted-foreground" })}
                  </Button>
                )}
                {((data.retests || []).length > 0 || claim.retest_running) && (
                  <Button variant="outline" className="h-8 text-xs" onClick={() => openLayer("retest")}>
                    {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                    复测记录
                    {React.createElement(iconOf("chevronRight"), { className: "size-3.5 text-muted-foreground" })}
                  </Button>
                )}
                {((data.replays || []).length > 0 || claim.replay_running) && (
                  <Button variant="outline" className="h-8 text-xs" onClick={() => openLayer("replay")}>
                    {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                    重放记录
                    {React.createElement(iconOf("chevronRight"), { className: "size-3.5 text-muted-foreground" })}
                  </Button>
                )}
              </div>
            </section>

            {/* 底部功能入口:导出与重开 */}
            <div className="mt-2 flex flex-wrap items-center gap-2 border-t pt-4">
              <ViewInGraphButton tid={tid} nodeID={claim.id} type="testclaim" onClose={onClose} />
              <span className="text-xs text-muted-foreground">导出本条</span>
              <Button
                variant="outline"
                className="h-8 px-2 text-xs"
                onClick={async () => {
                  try {
                    const name = await downloadExport(
                      `/api/tasks/${tid}/claims/${claim.id}/export?format=md`,
                      `finding-${claim.id}.md`,
                    );
                    toast.success(`已导出 ${name}`);
                  } catch (e) {
                    toast.error(`导出失败:${e?.message || "网络未连通"}`);
                  }
                }}
              >
                {React.createElement(iconOf("download"), { className: "size-3.5" })}
                .md
              </Button>
              <Button
                variant="outline"
                className="h-8 px-2 text-xs"
                onClick={async () => {
                  try {
                    const name = await downloadExport(
                      `/api/tasks/${tid}/claims/${claim.id}/export?format=zip`,
                      `finding-${claim.id}.zip`,
                    );
                    toast.success(`已导出 ${name}`);
                  } catch (e) {
                    toast.error(`导出失败:${e?.message || "网络未连通"}`);
                  }
                }}
              >
                {React.createElement(iconOf("download"), { className: "size-3.5" })}
                .zip(含证据包)
              </Button>
              {data.reopenable && (
                <Button
                  variant="outline"
                  className="h-8 px-2 text-xs"
                  onClick={() => openLayer("reopen")}
                >
                  {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                  重开
                </Button>
              )}
              {(claim.retestable || (data.retests || []).length > 0 || claim.retest_running) && (
                <Button
                  variant="outline"
                  className="h-8 px-2 text-xs"
                  onClick={() => openLayer("retest")}
                >
                  {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                  {claim.retest_running ? "复测中" : "复测"}
                </Button>
              )}
              {(claim.replayable || (data.replays || []).length > 0 || claim.replay_running) && (
                <Button
                  variant="outline"
                  className="h-8 px-2 text-xs"
                  onClick={() => openLayer("replay")}
                >
                  {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                  {claim.replay_running ? "重放中" : "重放"}
                </Button>
              )}
            </div>
          </div>
        </SheetContent>
      </Sheet>

      {/* ---- 第二层卡片(叠层 Sheet,关闭回第一层) ---- */}
      {layer === "repro" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>复现数据包</SheetTitle>
              <SheetDescription title={repro ? `版本号 ${repro.rev}` : undefined}>
                {repro
                  ? `检查归纳定稿${repro.severity ? ` · 级别 ${severityMeta(repro.severity)?.label || repro.severity}` : ""}`
                  : "尚无定稿复现步骤"}
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              {repro ? (
                <ReproStepsPaged tid={tid} claimID={claim.id} repro={repro} stepsTotal={claim.repro_steps} />
              ) : (
                <span className="text-xs text-muted-foreground">(反证轮未产生可复现的流量记录)</span>
              )}
              <ClaimEvidenceSection tid={tid} evidence={data.evidence || []} version={data.evidence_version ?? 0} />
            </div>
          </SheetContent>
        </Sheet>
      )}

      {layer === "chain" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>攻击链条</SheetTitle>
              <SheetDescription>
                意图／事实合并显示 · 箭头指向产出
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              {/* 来龙去脉(P4-13):定稿冻结的模型叙述,链路图上方先见叙述
                  再见图;旧定稿无叙述如实标注,不编造;文风校验未过的
                  叙述带说明行(P4-24) */}
              {repro && (
                <section className="flex flex-col gap-1">
                  <div className="text-xs font-medium tracking-wide text-muted-foreground">来龙去脉</div>
                  <div className="rounded-md border border-border/55 bg-card px-3 py-2 leading-relaxed">
                    <DetailText text={repro.narrative || "定稿时未生成叙述"} />
                    {repro.narrative_unpolished && (
                      <div className="mt-1 text-xs text-muted-foreground">文字未过文风校验，按原文保留</div>
                    )}
                  </div>
                </section>
              )}
              <ClaimChainGraph tid={tid} claimID={claim.id} onSelectNode={onSelectNode} />
              {claim.combined && (claim.chain || []).length > 0 && (
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="text-muted-foreground">环节角色:</span>
                  {claim.chain?.map((c, i) => (
                    <span key={c.fact_id + String(i)} className="flex items-center gap-1">
                      <Badge variant="outline">{c.role || `环节 ${i + 1}`}</Badge>
                      <NodeRefButton meta={{ id: c.fact_id, type: "fact", typeLabel: "事实" }} onSelectNode={onSelectNode} />
                    </span>
                  ))}
                </div>
              )}
            </div>
          </SheetContent>
        </Sheet>
      )}

      {layer === "origin" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>溯源</SheetTitle>
              <SheetDescription>
                发现单位与反证单位 · 点击直达对应虫群会话页追问
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              <section className="flex flex-col gap-1">
                <div className="text-xs font-medium tracking-wide text-muted-foreground">发现单位</div>
                {data.origin?.discoverer ? (
                  <OriginRow tid={tid} o={data.origin.discoverer} onClose={onClose} onSelectNode={onSelectNode} />
                ) : (
                  <span className="text-xs text-muted-foreground">(账本未记录发现单位)</span>
                )}
              </section>
              <section className="flex flex-col gap-1">
                <div className="text-xs font-medium tracking-wide text-muted-foreground">
                  反证单位({(data.origin?.falsifiers || []).length})
                </div>
                {(data.origin?.falsifiers || []).length === 0 ? (
                  <span className="text-xs text-muted-foreground">(尚未反证)</span>
                ) : (
                  data.origin.falsifiers?.map((f, i) => (
                    <OriginRow key={`${f.unit_id}-${i}`} tid={tid} o={f} onClose={onClose} onSelectNode={onSelectNode} />
                  ))
                )}
              </section>
              <span className="text-xs text-muted-foreground">
                反证单位与发现单位相异(发现者不得自证);会话页可插话追问执行细节。
              </span>
            </div>
          </SheetContent>
        </Sheet>
      )}

      {layer === "retest" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>复测</SheetTitle>
              <SheetDescription>
                按复现步骤重放,判定当前是否已修复 · 结论自动更新发现状态 · 流量经镜留痕
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                {claim.retest_running ? (
                  <Badge variant="running">复测中</Badge>
                ) : claim.retestable ? (
                  <Button size="sm" onClick={startRetest}>
                    {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                    发起复测
                  </Button>
                ) : (
                  <span className="text-xs text-muted-foreground">
                    当前状态不可复测(已确认的发现可发起复测)
                  </span>
                )}
                {claim.retest_verdict && !claim.retest_running && (
                  <span className="text-xs text-muted-foreground">
                    最近:第 {claim.retest_round} 轮{" "}
                    {RETEST_VERDICT[claim.retest_verdict]?.label || claim.retest_verdict} ·{" "}
                    {claim.retest_at}
                  </span>
                )}
              </div>
              {(data.retests || []).length === 0 ? (
                <span className="text-xs text-muted-foreground">尚未复测过</span>
              ) : (
                <div className="flex flex-col gap-3">
                  {[...data.retests].reverse().map((rt) => (
                    <RetestRoundCard key={rt.session_id} tid={tid} rt={rt} />
                  ))}
                </div>
              )}
            </div>
          </SheetContent>
        </Sheet>
      )}

      {layer === "replay" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>重放校验</SheetTitle>
              <SheetDescription>
                绑定的流量证据原样重发,比对原响应 · 只陈述一致与否,是否已修复归复测
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                {claim.replay_running ? (
                  <Badge variant="running">重放中</Badge>
                ) : claim.replayable ? (
                  <Button size="sm" onClick={startReplay}>
                    {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
                    发起重放
                  </Button>
                ) : (
                  <span className="text-xs text-muted-foreground">
                    未绑定流量证据(先在流量页绑定)
                  </span>
                )}
                {claim.replay_verdict && !claim.replay_running && (
                  <span className="text-xs text-muted-foreground">
                    最近:第 {claim.replay_round} 轮{" "}
                    {REPLAY_VERDICT[claim.replay_verdict]?.label || claim.replay_verdict} ·{" "}
                    {claim.replay_at}
                  </span>
                )}
              </div>
              {(data.replays || []).length === 0 ? (
                <span className="text-xs text-muted-foreground">尚未重放过</span>
              ) : (
                <div className="flex flex-col gap-3">
                  {[...data.replays].reverse().map((rp) => (
                    <ReplayRoundCard key={rp.anchor} rp={rp} />
                  ))}
                </div>
              )}
            </div>
          </SheetContent>
        </Sheet>
      )}

      {layer === "reopen" && (
        <Sheet nested open onOpenChange={(o) => !o && openLayer(null)}>
          <SheetContent className="overflow-y-auto">
            <SheetHeader>
              <SheetTitle>重开</SheetTitle>
              <SheetDescription>结论可反驳;重开后其他可能原因全部复活,重新反证</SheetDescription>
            </SheetHeader>
            <div className="p-4">
              <ReopenPanel tid={tid} claimID={claim.id} />
            </div>
          </SheetContent>
        </Sheet>
      )}

      {/* 追问大窗(P5-07②:锚定该漏洞的对话窗,窗内节点引用下钻自成
          抽屉栈) */}
      <ClaimFollowupDialog
        tid={tid}
        anchor={{ kind: "claim", id: claim.id }}
        open={followup}
        onClose={() => setFollowup(false)}
      />
    </>
  );
}

// OriginRow 溯源的一个单位行:单位号 + 步骤引用(可下钻)+ 反证轮次 +
// 直达会话页(P3-13b ① 跳转协议:?unit=<会话ID> 与工作台同源)。
function OriginRow({ tid, o, onClose, onSelectNode }) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2">
      <span className="num text-xs">{o.unit_id}</span>
      {o.step_id && (
        <NodeRefButton meta={{ id: o.step_id, type: "step", typeLabel: "步骤" }} onSelectNode={onSelectNode} />
      )}
      {o.round > 0 && <Badge variant="outline">第 {o.round} 轮反证</Badge>}
      <span className="flex-1" />
      {o.session_id ? (
        <Button
          variant="outline"
          size="sm"
          className="h-8 px-2 text-xs"
          title={`跳到 ${o.unit_id} 的会话页`}
          onClick={() => {
            onClose?.();
            location.hash = `/task/${tid}/sessions?unit=${encodeURIComponent(o.session_id)}`;
          }}
        >
          {React.createElement(iconOf("messageCircle"), { className: "size-3.5" })}
          查看会话
        </Button>
      ) : (
        <span className="text-xs text-muted-foreground">(无会话记录)</span>
      )}
    </div>
  );
}

// RetestRoundCard 一轮复测(P3-06):结论徽标 + 备注 + 证据截头 + 会话
// 原文折叠(转录端点懒拉,呈现与会话页同一组件)。
function RetestRoundCard({ tid, rt }) {
  const [open, setOpen] = React.useState(false);
  const [data, setData] = React.useState(null);
  const toggle = async () => {
    const next = !open;
    setOpen(next);
    if (next && !data) {
      try {
        const r = await fetch(`/api/tasks/${tid}/transcripts/${rt.session_id}`);
        const body = await r.json();
        setData(body);
      } catch {
        setData({ exists: false, entries: [] });
      }
    }
  };
  const vd = RETEST_VERDICT[rt.verdict] || { label: rt.verdict, variant: "info" };
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border/55 bg-card p-3">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge meta={vd}>复测{vd.label}</StatusBadge>
        <span className="text-xs text-muted-foreground">第 {rt.round} 轮</span>
        <span className="num text-[10px] text-muted-foreground">{rt.session_id}</span>
        <span className="ml-auto num text-[11px] text-muted-foreground">{rt.at}</span>
      </div>
      {rt.note && <div className="text-xs text-muted-foreground">{rt.note}</div>}
      {rt.evidence_head && (
        <div className="break-all rounded-md bg-muted/30 p-2 num text-[11px]">
          {rt.evidence_head}
        </div>
      )}
      <button
        className={
          "flex w-fit items-center gap-1.5 text-xs transition-colors cursor-pointer " +
          (open ? "text-foreground" : "text-muted-foreground hover:text-foreground")
        }
        onClick={toggle}
      >
        {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), { className: "size-3.5" })}
        会话原文
      </button>
      {open && (
        <div className="flex flex-col gap-1.5">
          {!data ? (
            <span className="text-xs text-muted-foreground">读取会话原文…</span>
          ) : !data.exists ? (
            <span className="text-xs text-muted-foreground">会话原文未保存(该轮无记录文件)</span>
          ) : (
            (data.entries || []).map((e, i) => <TranscriptEntryView key={i} entry={e} compact />)
          )}
        </div>
      )}
    </div>
  );
}

// ReplayRoundCard 一轮重放校验(P3-07):结论徽标 + 逐条交换的新旧对照
//(原状态 → 新状态、字节数、体一致性、失败原因;新流量按锚归因,流量页
// 可回放)。
function ReplayRoundCard({ rp }) {
  const vd = REPLAY_VERDICT[rp.verdict] || { label: rp.verdict, variant: "info" };
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border/55 bg-card p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={vd.variant}>{vd.label}</Badge>
        <span className="text-xs text-muted-foreground">第 {rp.round} 轮</span>
        <span className="num text-[10px] text-muted-foreground">{rp.anchor}</span>
        <span className="ml-auto num text-[11px] text-muted-foreground">{rp.at}</span>
      </div>
      <div className="flex flex-col gap-1.5">
        {(rp.items || []).map((it, i) => (
          <div
            key={it.exchange_id + String(i)}
            className="flex flex-col gap-0.5 rounded-md bg-muted/30 px-3 py-1.5 text-xs"
          >
            <div className="flex flex-wrap items-center gap-2">
              {it.error ? (
                <Badge variant="danger">失败</Badge>
              ) : it.body_same ? (
                <Badge variant="success">响应一致</Badge>
              ) : (
                <Badge variant="warning">响应有变化</Badge>
              )}
              <span className="num">
                {it.method} {it.url}
              </span>
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground">
              <span>
                状态 {it.old_status || "—"} → {it.new_status || "—"}
              </span>
              <span>
                字节 {it.old_bytes} → {it.new_bytes}
              </span>
              {it.new_exchange_id && (
                <span className="num text-[10px]">
                  新流量记录 {it.new_exchange_id}
                </span>
              )}
            </div>
            {it.error && <div className="text-status-danger" title={it.error}>该请求未完成</div>}
          </div>
        ))}
      </div>
    </div>
  );
}

// ClaimEvidenceSection 声明的流量证据区(P3-02):绑定清单(首绑次序)
// + 声明证据版本;行展开原文分段回放(与流量页同一组件同一装配)。
function ClaimEvidenceSection({ tid, evidence, version }) {
  const [openID, setOpenID] = React.useState("");
  if (evidence.length === 0) {
    return (
      <section className="flex flex-col gap-1">
        <div className="text-xs font-medium tracking-wide text-muted-foreground">
          流量证据(判断证据版本 {version})
        </div>
        <span className="text-xs text-muted-foreground">
          (未绑定;在流量页选一条流量可绑定为证据,支持补绑)
        </span>
      </section>
    );
  }
  return (
    <section className="flex flex-col gap-2">
      <div className="text-xs font-medium tracking-wide text-muted-foreground">
        流量证据({evidence.length} 条 · 判断证据版本 {version})
      </div>
      <ul className="flex flex-col gap-2">
        {evidence.map((ev) => {
          const open = openID === ev.exchange_id;
          return (
            <li key={ev.exchange_id} className="flex flex-col gap-1 rounded-md border border-border/55 bg-card p-3">
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <button
                  className="flex flex-wrap items-center gap-2 cursor-pointer"
                  onClick={() => setOpenID(open ? "" : ev.exchange_id)}
                >
                  {React.createElement(iconOf(open ? "chevronDown" : "chevronRight"), {
                    className: "size-3.5 text-muted-foreground",
                  })}
                  <span className="num font-medium">{ev.method || ev.type}</span>
                  <span className="max-w-72 truncate num text-muted-foreground" title={ev.url}>
                    {ev.url}
                  </span>
                  {ev.status ? (
                    <Badge variant={statusVariant(ev.status)}>{ev.status}</Badge>
                  ) : (
                    <Badge variant="paused">无响应</Badge>
                  )}
                  <span
                    className="text-[10px] text-muted-foreground"
                    title={`绑定于版本号 ${ev.bound_rev}`}
                  >
                    已绑定
                  </span>
                </button>
              </div>
              {open && <ExchangeRaw tid={tid} exID={ev.exchange_id} />}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// fmtTimingSpan 耗时拆解的时间跨度:锚例「10-23 19:48 到 19:54」;
// 进行中只给起点(「10-23 19:48 起」);跨日时终点带日期。
function fmtTimingSpan(landed, terminal) {
  const start = (landed || "").slice(5, 16);
  if (!terminal) return `${start} 起`;
  const end = terminal.slice(5, 16);
  if (end.slice(0, 6) === start.slice(0, 6)) return `${start} 到 ${end.slice(6)}`;
  return `${start} 到 ${end}`;
}

// fmtDur 耗时拆解的时长词(<90 秒精确到 0.1 秒,更长为分秒;与会话页
// 计费口径同源)。
function fmtDur(s) {
  if (!Number.isFinite(s) || s < 90) return `${Math.round((s || 0) * 10) / 10} 秒`;
  const m = Math.floor(s / 60);
  return `${m} 分 ${Math.round(s - m * 60)} 秒`;
}

function TimelineEntry({ entry }) {
  switch (entry.kind) {    case "created":
      return (
        <TimelineItem
          rev={entry.rev}
          title="漏洞已交卷，等待核验"
          tone="running"
          detail={
            <>
              发现单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
            </>
          }
        />
      );
    case "submit":
      // 补考重交(P5-02④:同单第二次交换单;首次交卷由 created 承载)。
      return (
        <TimelineItem
          rev={entry.rev}
          title="再次交卷，待机器核验"
          tone="running"
          detail={
            <>
              单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
              {entry.note && <div>{entry.note}</div>}
            </>
          }
        />
      );
    case "verdict": {
      const meta = DECISION_META[entry.decision] || {
        label: entry.decision,
        tone: "running",
      };
      return (
        <TimelineItem
          rev={entry.rev}
          title={`机器核验 · ${meta.label}`}
          tone={meta.tone}
          detail={
            <>
              {entry.pending_reason && <div>{entry.pending_reason}</div>}
              {(entry.conditions || []).length > 0 && (
                <div className="mt-1 flex flex-wrap gap-2">
                  {entry.conditions?.map((c, i) => (
                    <span
                      key={i}
                      title={`${condLabel(c.kind)}(${c.kind}): ${c.actual}/${c.required}`}
                      className={
                        "rounded px-2 py-0.5 num text-[10px] " +
                        (c.satisfied
                          ? "bg-status-success-soft text-status-success"
                          : "bg-status-danger-soft text-status-danger")
                      }
                    >
                      {condLabel(c.kind)} {c.actual}/{c.required}
                    </span>
                  ))}
                </div>
              )}
            </>
          }
        />
      );
    }
    case "reopen":
      return (
        <TimelineItem
          rev={entry.rev}
          title="重开(结论可反驳)"
          tone="warning"
          detail={
            <>
              触发:{REOPEN_TRIGGERS.find(([v]) => v === entry.trigger)?.[1] || entry.trigger}
              {" · "}旧战役证据失效,走新交卷重验
              {entry.reason && <div>理由:{entry.reason}</div>}
            </>
          }
        />
      );
    case "suspend":
      // 存量旧记录如实呈现(挂起机制已随立判断链退役,P5-03①)。
      return (
        <TimelineItem
          rev={entry.rev}
          title="挂起(存量记录)"
          tone="warning"
          detail={<>{entry.reason && <div>理由:{entry.reason}</div>}</>}
        />
      );
    case "repro":
      return (
        <TimelineItem
          rev={entry.rev}
          title="检查归纳定稿(复现步骤 + 级别)"
          tone="success"
          detail={
            <>
              严重级别:{severityMeta(entry.severity)?.label || entry.severity || "未定级"}(交换单自报)
            </>
          }
        />
      );
    case "state":
      return (
        <TimelineItem
          rev={entry.rev}
          title={`状态 → ${STATE_LABELS[entry.status] || entry.status}`}
          tone={
            entry.status === "confirmed" || entry.status === "fixed"
              ? "success"
              : entry.status === "insufficient" || entry.status === "pending"
                ? "warning"
                : entry.status === "refuted" || entry.status === "discarded"
                  ? "info"
                  : "running"
          }
          detail=""
        />
      );
    default:
      return null;
  }
}

// (旧 FalsifyTimelineEntry/FalsifyMachineDetail 反证轮条目已随反证流程
// 退役删除,P5-03③——逐轮人话与机器层无生成时点,条目不再呈现。)

function TimelineItem({ rev, title, detail, tone }) {
  return (
    <li className="relative">
      <span
        className={
          "absolute top-1 -left-[21px] size-2 rounded-full " +
          (tone === "success"
            ? "bg-status-success"
            : tone === "warning"
              ? "bg-status-warning"
              : tone === "danger"
                ? "bg-status-danger"
                : tone === "info"
                  ? "bg-status-paused"
                  : "bg-status-running")
        }
      />
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="text-sm font-medium" title={rev != null ? `版本号 ${rev}` : undefined}>
          {title}
        </span>
      </div>
      <div className="text-xs text-muted-foreground">{detail}</div>
    </li>
  );
}

function ReopenPanel({ tid, claimID }) {
  const [reason, setReason] = React.useState("");
  const [trigger, setTrigger] = React.useState("new_evidence");
  const [busy, setBusy] = React.useState(false);

  const reopen = async () => {
    if (!reason.trim()) {
      toast.error("重开必须说明理由");
      return;
    }
    setBusy(true);
    const { ok, body } = await postJSON(
      `/api/tasks/${tid}/claims/${claimID}/reopen`,
      { reason: reason.trim(), trigger },
    );
    setBusy(false);
    if (!ok) {
      toast.error(`重开被拒:${body.error || "原因未说明"}`);
      return;
    }
    toast.success("已重开:其他可能原因全部复活,重新进入反证");
    setReason("");
  };

  return (
    <section className="flex flex-col gap-2 rounded-md border border-status-warning/40 bg-status-warning-soft/40 p-3">
      <div className="text-xs font-medium tracking-wide text-muted-foreground">
        重开(结论可反驳 · 重开后其他可能原因全部复活重新反证)
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-44">
          <Select value={trigger} onValueChange={setTrigger}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {REOPEN_TRIGGERS.map(([v, label]) => (
                <SelectItem key={v} value={v}>
                  {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Input
          className="w-64"
          placeholder="重开理由(必填,记录可审计)"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <Button variant="destructive" size="sm" disabled={busy} onClick={reopen}>
          {busy ? "重开中…" : "重开反证"}
        </Button>
      </div>
    </section>
  );
}

// ---- 跳转协议宿主 ----

// claimLayerMemory 成果卡第二层(复现数据包/攻击链条/重开)的记忆:下钻
// 换卡会卸载成果卡,返回时按声明 ID 恢复到离开时的那一层(关闭整个
// 成果卡时清除)。模块级、随面板进程存活。
const claimLayerMemory = new Map();

// NodeSheetHost 节点详情抽屉宿主:按节点类型分发到对应详情抽屉。下钻
// 经内部返回栈——点引用换卡时压栈,关闭当前卡退栈回上一层(栈空才真
// 关),下钻多深都能逐层返回;父级直接换卡(列表另点一行)视为全新
// 入口,历史栈作废。value = {type, id} | null。
export function NodeSheetHost({ tid, value, onChange, graph, dismissAll = false, animateNavigation = false }) {
  const stackRef = React.useRef([]);
  const expectedRef = React.useRef(null); // 本宿主最近发出的换卡(下钻/退栈),用于区分外部替换
  const sequence = React.useRef(0);
  const [frames, setFrames] = React.useState([]);
  // Keep a claim and its open chain mounted through descendant details. The
  // panorama's dismiss-all host retains its existing replacement semantics.
  const preserveNavigation = animateNavigation || (!dismissAll && (value?.type === "testclaim" || stackRef.current?.some(node => node.type === "testclaim")));

  React.useEffect(() => {
    if (!value) {
      stackRef.current = [];
      expectedRef.current = null;
      return;
    }
    const exp = expectedRef.current;
    if ((exp && value.id !== exp.id) || (preserveNavigation && !exp)) {
      stackRef.current = []; // 外部直接换卡,栈作废
    }
    expectedRef.current = null;
  }, [value, preserveNavigation]);

  React.useEffect(() => {
    if (!preserveNavigation) return;
    const wanted = value?.id ? [...stackRef.current, value] : [];
    setFrames(previous => {
      const opened = previous.filter(frame => frame.open);
      let common = 0;
      while (common < wanted.length && common < opened.length && wanted[common].id === opened[common].node.id && wanted[common].type === opened[common].node.type) common++;
      const retained = new Set(opened.slice(0, common).map(frame => frame.key));
      const next = previous.map(frame => retained.has(frame.key) || !frame.open ? frame : { ...frame, open: false });
      for (let depth = common; depth < wanted.length; depth++) next.push({ key: ++sequence.current, node: wanted[depth], depth, open: true });
      return next;
    });
  }, [value, preserveNavigation]);

  if (!value?.id && !preserveNavigation) return null;
  const close = () => {
    if (!value?.id) return;
    if (dismissAll) {
      if (value.type === "testclaim") claimLayerMemory.delete(value.id);
      for (const item of stackRef.current) if (item.type === "testclaim") claimLayerMemory.delete(item.id);
      stackRef.current = [];
      expectedRef.current = null;
      onChange(null);
      return;
    }
    const prev = stackRef.current.pop();
    if (prev) {
      expectedRef.current = prev;
      onChange(prev);
    } else {
      if (value.type === "testclaim") claimLayerMemory.delete(value.id);
      onChange(null);
    }
  };
  const drill = (meta) => {
    if (!value?.id || !meta?.id || meta.id === value.id) return;
    stackRef.current.push({ type: value.type, id: value.id, panorama: value.panorama, resultIDs: value.resultIDs });
    expectedRef.current = { type: meta.type, id: meta.id };
    onChange({ type: meta.type, id: meta.id });
  };
  if (preserveNavigation) {
    const top = frames.filter(frame => frame.open).at(-1)?.key;
    return frames.map(frame => <FloatingLayerContext.Provider key={frame.key} value={frame.depth}>
      <SheetNavigationContext.Provider value={{ open: frame.open, nested: frame.depth > 0, onExited: () => setFrames(current => current.filter(item => item.key !== frame.key || item.open)) }}>
        <NodeSheetView tid={tid} value={frame.node} graph={graph} onClose={() => { if (frame.key === top) close(); }} onSelectNode={meta => { if (frame.key === top) drill(meta); }} />
      </SheetNavigationContext.Provider>
    </FloatingLayerContext.Provider>);
  }
  return <NodeSheetView tid={tid} value={value} graph={graph} onClose={close} onSelectNode={drill} />;
}

function NodeSheetView({ tid, value, graph, onClose: close, onSelectNode: drill }) {
  switch (value.type) {
    case "testclaim":
      return (
        <ClaimDetailSheet key={value.id} tid={tid} claimID={value.id} onClose={close} onSelectNode={drill} />
      );
    case "asset":
      return (
        <AssetDetailSheet key={value.id} tid={tid} nodeID={value.id} onClose={close} onSelectNode={drill} />
      );
    case "goal":
    case "step":
    case "fact":
      return (
        <GenericNodeSheet
          key={value.id}
          tid={tid}
          nodeID={value.id}
          nodeType={value.type}
          graph={graph}
          onClose={close}
          onSelectNode={drill}
          includeResults={value.type === "step"}
          resultIDs={value.resultIDs}
        />
      );
    default:
      return null;
  }
}
