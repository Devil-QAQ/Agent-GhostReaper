"use client";

import * as React from "react";
import { toast } from "sonner";

import { ClaimChainGraph } from "@/components/claim-chain-graph";
import { ClaimFollowupDialog } from "@/components/claim-followup-dialog";
import { CopyIDButton } from "@/components/copy-id-button";
import { DiveDialog } from "@/components/dive-dialog";
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
} from "@/components/ui/sheet";
import {
  CLAIM_STATUS,
  EDGE_LABELS,
  FACT_STATUS,
  GOAL_STATUS,
  HYP_DECISION,
  HYP_STATUS,
  HYP_VERDICT,
  REPLAY_VERDICT,
  RETEST_VERDICT,
  STEP_STATUS,
  assetClassMeta,
  assetSourceMeta,
  claimDisplayMeta,
  hypDisplayMeta,
  jumpToNodeView,
  nodeTypeMeta,
  postJSON,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { TranscriptEntryView } from "@/views/sessions-page";

// 全站节点跳转协议(P2-09):任何图/列表节点点击 → Sheet 详情抽屉
//(不离开上下文,NodeSheetHost 统一承载,内部引用可换抽屉下钻)+
//「在图中查看」跳节点对应视图并聚焦(URL ?focus= 定位参数,可直达)。
// 本模块是抽屉层的唯一定义:发现/假设/资产详情自原页面迁入,进度图
//(P2-09)与发现/资产列表共用,不另建平行实现。

// ---- 通用件 ----

// ViewInGraphButton「在图中查看」:跳节点对应视图(资产→资产树定位,
// 其余→进度图聚焦),可直达的 URL 定位参数随行。
function ViewInGraphButton({ tid, nodeID, type, onClose }) {
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

// NodeTypeBadge 六类节点类型徽标(进度图节点视觉语言的徽标形态)。
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

// ---- 通用节点抽屉(goal / step / fact)----

// detail 自由字段的操作者语言(表外词按 8.5 术语门槛翻译;未收录键
// 原样呈现——字段名即数据事实,不编造解释)。
const DETAIL_KEYS = {
  cancellation_reason: "取消理由",
  goal_id: "归属目标",
  action_level: "意图分级",
  intent: "意图",
  unit_id: "单位",
  transcript_id: "会话记录",
  expected_observation: "预期观察",
  harm_class: "危害声明",
  priority: "优先级",
  evidence_head: "证据",
  evidence: "证据原文",
  evidence_ref: "原始数据",
  vulnclass: "类别",
  impact: "影响面",
  standard: "判定标准",
  predicate: "完成谓词",
  kind: "类型",
  testclaim_id: "判断",
  new_asset: "新对象申报",
  reason: "理由",
  note: "备注",
};

function detailValue(v) {
  if (v === null || v === undefined) return "(空)";
  if (typeof v === "object") return JSON.stringify(v, null, 2);
  return String(v);
}

// GenericNodeSheet 目标/步骤/事实详情:节点投影 + detail 字段逐项 +
// 血缘上下文(上游/下游引用可下钻,fact# 跳转的抽屉侧落点)。
// 事实带"深入"入口(P4-48:一切状态可深入,终态任务置灰)。
export function GenericNodeSheet({ tid, nodeID, nodeType, graph, onClose, onSelectNode }) {
  const { data, error } = usePolling(
    nodeID ? `/api/tasks/${tid}/nodes/${nodeID}` : "/api/healthz",
    3000,
    { immediate: Boolean(nodeID) },
  );
  // 深入指令窗(P4-48)。
  const [dive, setDive] = React.useState(false);
  if (!nodeID) return null;
  const n = data?.id ? data : null;
  const type = nodeType || n?.type;

  const statusBadge = (t, status) => {
    if (t === "goal") {
      const g = GOAL_STATUS[status];
      return g ? <Badge variant={g.variant}>{g.label}</Badge> : null;
    }
    if (t === "fact") {
      const f = FACT_STATUS[status];
      return f ? <StatusBadge meta={f} /> : null;
    }
    if (t === "step") {
      return <Badge variant={status === "done" ? "success" : status === "running" ? "running" : "paused"}>{STEP_STATUS[status] || status}</Badge>;
    }
    return null;
  };

  // 血缘上下文:自统一图投影快照取上下游(graph 缺省时该区不呈现)。
  const lineage = React.useMemo(() => {
    if (!graph || !n) return null;
    const titleOf = new Map((graph.nodes || []).map((x) => [x.id, x]));
    const up = [];
    const down = [];
    for (const e of graph.edges || []) {
      const label = EDGE_LABELS[e.type];
      if (!label) continue;
      if (e.to === n.id && titleOf.has(e.from)) {
        up.push({ id: e.from, edge: label, node: titleOf.get(e.from) });
      }
      if (e.from === n.id && titleOf.has(e.to)) {
        down.push({ id: e.to, edge: label, node: titleOf.get(e.to) });
      }
    }
    return { up, down };
  }, [graph, n]);

  return (
    <Sheet open={Boolean(nodeID)} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="overflow-y-auto">
        {!n ? (
          <div className="p-5 text-sm text-muted-foreground">
            {error ? `读取失败:${error}` : "读取中…"}
          </div>
        ) : (
          <>
            <SheetHeader>
              <SheetTitle className="flex flex-wrap items-center gap-2">
                <NodeTypeBadge type={type} />
                {statusBadge(type, n.status)}
                <ViewInGraphButton tid={tid} nodeID={n.id} type={type} onClose={onClose} />
                {type === "fact" && <ViewInFactsButton tid={tid} nodeID={n.id} onClose={onClose} />}
              </SheetTitle>
              <SheetDescription>
                <span className="num" title={`版本号 ${n.created_rev}`}>{n.id}</span>
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 p-5 text-sm">
              <DetailSection title="标题">
                <div className="rounded-md border border-border/55 bg-card px-3 py-2 break-words">
                  {n.title || "(无标题)"}
                </div>
              </DetailSection>

              {n.detail && Object.keys(n.detail).length > 0 && (
                <DetailSection title="节点字段">
                  <div className="flex flex-col gap-2">
                    {Object.entries(n.detail).map(([k, v]) => {
                      const text = detailValue(v);
                      const long = text.length > 96 || typeof v === "object";
                      const goalRef = k === "goal_id" && typeof v === "string";
                      return (
                        <div key={k} className="flex flex-col gap-0.5 rounded-md border border-border/55 bg-card px-3 py-2">
                          <div className="text-xs text-muted-foreground">{DETAIL_KEYS[k] || k}</div>
                          {goalRef ? (
                            <NodeRefButton
                              meta={{ id: String(v), type: "goal", typeLabel: "目标" }}
                              onSelectNode={onSelectNode}
                            />
                          ) : long ? (
                            <pre className="overflow-auto rounded-md bg-input-surface p-2 num text-xs leading-relaxed whitespace-pre-wrap break-all">
                              {text}
                            </pre>
                          ) : (
                            <div className="break-all">{text}</div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </DetailSection>
              )}

              {lineage && (lineage.up.length > 0 || lineage.down.length > 0) && (
                <DetailSection title="血缘(引用可下钻)">
                  <div className="flex flex-col gap-1">
                    {/* 上游行:引用在前、标签在后,读作「<引用> ─标签→ 本节点」
                        (如 判断派生自本事实);下行行:标签在前,读作
                        「本节点 ─标签→ <引用>」(如 产出某事实)。 */}
                    {lineage.up.map((r) => (
                      <div key={`u-${r.id}-${r.edge}`} className="flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2">
                        <NodeRefButton
                          meta={{ id: r.id, type: r.node.type, typeLabel: nodeTypeMeta(r.node.type).label }}
                          onSelectNode={onSelectNode}
                        >
                          {`${nodeTypeMeta(r.node.type).label} ${r.id}`}
                        </NodeRefButton>
                        <Badge variant="outline">{r.edge} 本节点</Badge>
                        <span className="min-w-0 flex-1 truncate text-xs" title={r.node.title}>{r.node.title}</span>
                      </div>
                    ))}
                    {lineage.down.map((r) => (
                      <div key={`d-${r.id}-${r.edge}`} className="flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2">
                        <Badge variant="outline">{r.edge}</Badge>
                        <NodeRefButton
                          meta={{ id: r.id, type: r.node.type, typeLabel: nodeTypeMeta(r.node.type).label }}
                          onSelectNode={onSelectNode}
                        >
                          {`${nodeTypeMeta(r.node.type).label} ${r.id}`}
                        </NodeRefButton>
                        <span className="min-w-0 flex-1 truncate text-xs" title={r.node.title}>{r.node.title}</span>
                      </div>
                    ))}
                  </div>
                </DetailSection>
              )}

              {/* 深入区(P4-48):从该事实出发继续深入,一切状态可深入;
                  终态任务置灰(悬停"任务已结束") */}
              {type === "fact" && (
                <section className="flex flex-col gap-1">
                  <div className="text-xs font-medium tracking-wide text-muted-foreground">深入</div>
                  <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2">
                    <Button
                      variant="outline"
                      className="h-8 text-xs"
                      disabled={Boolean(data?.dive_disabled)}
                      title={data?.dive_disabled || "从该事实出发继续深入,方向可选"}
                      onClick={() => setDive(true)}
                    >
                      {React.createElement(iconOf("zap"), { className: "size-3.5" })}
                      深入
                    </Button>
                    <span className="text-xs text-muted-foreground">
                      确定即唤醒主宰规划 · 方向由主宰判定
                    </span>
                  </div>
                </section>
              )}
            </div>
          </>
        )}
      </SheetContent>
      {/* 深入指令窗(P4-48):从该事实出发继续深入 */}
      {type === "fact" && (
        <DiveDialog
          tid={tid}
          node={{ id: n?.id, title: n?.title, badge: FACT_STATUS[n?.status] }}
          open={dive}
          onClose={() => setDive(false)}
        />
      )}
    </Sheet>
  );
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
    <Sheet open={Boolean(nodeID)} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="overflow-y-auto">
        {!a ? (
          <div className="p-5 text-sm text-muted-foreground">
            {error ? `读取失败:${error}` : "读取中…"}
          </div>
        ) : (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                {React.createElement(iconOf(meta.icon), { className: "size-4 text-muted-foreground" })}
                {a.title || a.id}
                <ViewInGraphButton tid={tid} nodeID={a.id} type="asset" onClose={onClose} />
              </SheetTitle>
              <SheetDescription>
                {meta.label} ·{" "}
                <span className="num" title={`版本号 ${a.created_rev}`}>{a.id}</span>
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
                <Badge variant="info">事实 {a.fact_count}</Badge>
                <Badge variant="info">假设 {a.hypothesis_count}</Badge>
                <Badge variant="outline">
                  {React.createElement(iconOf(assetSourceMeta(a.source).icon), { className: "size-3" })}
                  {assetSourceMeta(a.source).label}
                </Badge>
              </div>

              {/* 对象出生(P4-12):资产怎么入图的——发现型带发现单位与出生
                  引用(步骤/事实,可下钻),种子/手动如实陈述;词与攻击链条
                  卡的对象出生区同一份。 */}
              <DetailSection title="对象出生">
                {a.source === "discovered" ? (
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-border/55 bg-card px-3 py-2 text-xs">
                    <span>发现入图</span>
                    {a.birth_unit ? (
                      <span className="text-muted-foreground">
                        单位 <span className="num">{a.birth_unit}</span>
                      </span>
                    ) : null}
                    {a.birth_step && (
                      <NodeRefButton meta={{ id: a.birth_step, type: "step", typeLabel: "步骤" }} onSelectNode={onSelectNode} />
                    )}
                    {a.birth_fact && (
                      <NodeRefButton meta={{ id: a.birth_fact, type: "fact", typeLabel: "事实" }} onSelectNode={onSelectNode} />
                    )}
                  </div>
                ) : a.source === "manual" ? (
                  <div className="rounded-md border border-border/55 bg-card px-3 py-2 text-xs">手动补入(面板新增)</div>
                ) : (
                  <div className="rounded-md border border-border/55 bg-card px-3 py-2 text-xs">建任务种子入图</div>
                )}
              </DetailSection>

              <DetailSection title="事实(fact#)">
                {(data.facts || []).length === 0 ? (
                  <Muted>暂无</Muted>
                ) : (
                  (data.facts || []).map((f) => (
                    <div key={f.id} className="flex items-center gap-2">
                      <NodeRefButton meta={{ id: f.id, type: "fact", typeLabel: "事实" }} onSelectNode={onSelectNode} />
                      <span className="min-w-0 flex-1 truncate" title={f.title}>{f.title}</span>
                      <StatusBadge meta={FACT_STATUS[f.status] || { label: f.status, variant: "info" }} />
                    </div>
                  ))
                )}
              </DetailSection>

              <DetailSection title="假设">
                {(data.hypotheses || []).length === 0 ? (
                  <Muted>暂无</Muted>
                ) : (
                  (data.hypotheses || []).map((h) => (
                    <div key={h.id} className="flex items-center gap-2">
                      <NodeRefButton meta={{ id: h.id, type: "hypothesis", typeLabel: "假设" }} onSelectNode={onSelectNode} />
                      <span className="min-w-0 flex-1 truncate" title={h.title}>{h.title}</span>
                    </div>
                  ))
                )}
              </DetailSection>

              <DetailSection title="挂在此资产上的发现">
                {(data.claims || []).length === 0 ? (
                  <Muted>暂无</Muted>
                ) : (
                  (data.claims || []).map((c) => (
                    <div key={c.id} className="flex items-center gap-2">
                      <NodeRefButton meta={{ id: c.id, type: "testclaim", typeLabel: "判断" }} onSelectNode={onSelectNode} />
                      <StatusBadge meta={CLAIM_STATUS[c.status] || { label: c.status, variant: "info" }} />
                      <span className="min-w-0 flex-1 truncate" title={c.title}>{c.title}</span>
                    </div>
                  ))
                )}
              </DetailSection>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ---- 发现与假设详情抽屉(原 findings-page 迁入)----

const DECISION_META = {
  confirm: { label: "终审确认", tone: "success" },
  refuted: { label: "终审已废弃", tone: "info" },
  pending: { label: "终审证据不足", tone: "warning" },
  advance_round: { label: "再反证", tone: "running" },
};
// 时间线"状态 →"行的显示词(P4-56 四页签口径,与 CLAIM_STATUS 同词;
// 时间线单独成表是刻度叙事行,不套徽标)。
const STATE_LABELS = {
  candidate: "等待反证",
  testing: "反证中",
  awaiting_round: "等待反证",
  confirmed: "已确认",
  refuted: "已废弃",
  discarded: "已废弃",
  pending: "证据不足",
  fixed: "已修复",
};

const REOPEN_TRIGGERS = [
  ["new_evidence", "新反证"],
  ["manual", "人工复核"],
  ["expired", "结论过期"],
];

const COND_LABELS = {
  falsify_completed: "反证完成",
  existence_not_refuted: "存在性未推翻",
  alternatives_killed: "其他可能原因全排除",
  impact_supported: "影响面有证据",
  verifier_independent: "验证者独立",
  fact_confirmed: "事实已确认",
  tool_observed: "工具已观测",
};

function condLabel(kind) {
  return COND_LABELS[kind] || kind;
}

// ClaimDetailSheet 发现详情(8.8 完整版):头部(状态/类别/级别)+ 两维
// 声明分开 + 竞争解释 + 反证时间线 + 复现步骤 + 重开;资产锚点与攻击链
// 环节可下钻(跳转协议),头部「在图中查看」跳进度图聚焦。
// 审核迭代 2:成果卡只留最终成果——名称/描述/修复建议 + 复现数据包与
// 攻击链条两个二层入口 + 在图中查看;过程性内容(反证时间线/竞争解释)
// 不上面板卡(报告导出与账本留档)。导出与重开留底部(功能入口不删)。
export function ClaimDetailSheet({ tid, claimID, onClose, onSelectNode }) {
  const { data, error } = usePolling(
    claimID ? `/api/tasks/${tid}/claims/${claimID}` : "/api/healthz",
    2000,
    { immediate: Boolean(claimID) },
  );
  // 第二层卡片:null = 未开;"repro" 复现数据包 | "chain" 攻击链条 | "reopen" 重开。
  // 状态入 claimLayerMemory:下钻换卡卸载本卡,返回时恢复到离开时的层。
  const [layer, setLayer] = React.useState(null);
  // 成果追问大窗(P4-14):与描述 / 修复建议 / 数据与链路并列的追问区
  // 入口,一成果一会话、续接不新建。
  const [followup, setFollowup] = React.useState(false);
  // 深入指令窗(P4-48):节点锚定的推动,与追问并排(问是问、令是令)。
  const [dive, setDive] = React.useState(false);
  // 反证时间线展开态(P4-28 ④:默认折叠,点击展开)。
  const [tlOpen, setTlOpen] = React.useState(false);
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
      <Sheet open onOpenChange={(o) => !o && onClose()}>
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
  // 当前结论一句话(P4-28 ③):终态直陈结果,不让人读理由原文猜结果;
  // 过程态(等待反证/反证中)尚未出结论,不呈现本行。
  const conclusion =
    claim.status === "confirmed"
      ? "当前结论：成立，已确认"
      : claim.status === "fixed"
        ? "当前结论：成立，已确认（复测已修复）"
        : claim.status === "refuted"
          ? "当前结论：已废弃（被反证推翻，新证据可重开）"
          : claim.status === "discarded"
            ? "当前结论：已废弃（理由留档）"
            : claim.status === "pending"
              ? "当前结论：证据不足（可被新证据推翻重审）"
              : "";

  return (
    <>
      <Sheet open onOpenChange={(o) => !o && onClose()}>
        <SheetContent className="overflow-y-auto">
          <SheetHeader>
            <SheetTitle className="flex flex-wrap items-center gap-2">
              <StatusBadge meta={st} />
              {sev && <Badge variant={sev.variant}>{sev.label}</Badge>}
              {claim.vulnclass && <Badge variant="outline">{claim.vulnclass}</Badge>}
              {/* P4-19 迭代:判断原文为标题主视觉,tc-ID 不上标题,悬停与复制可取 */}
              <CopyIDButton id={claim.id} noun="判断标识" className="size-5" />
              <ViewInGraphButton tid={tid} nodeID={claim.id} type="testclaim" onClose={onClose} />
            </SheetTitle>
            {/* 漏洞名称即存在性判断原文 */}
            <SheetDescription className="text-sm text-foreground">
              {claim.statement}
            </SheetDescription>
          </SheetHeader>
          <div className="flex flex-col gap-5 p-5 text-sm">
            {/* 结论区置顶(P4-28 ④):状态徽标(头部)+ 当前结论 + 严重级
                (头部徽标)+ 一句话声明(头部描述)先行,证据/反证时间线/
                血缘等过程信息折叠其下。 */}
            {conclusion && (
              <section className="flex flex-col gap-1">
                <div className="text-xs font-medium tracking-wide text-muted-foreground">当前结论</div>
                <div className="rounded-md border border-border/55 bg-card px-3 py-2 text-sm font-medium">
                  {conclusion}
                </div>
              </section>
            )}
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

            <section className="flex flex-col gap-1">
              <div className="text-xs font-medium tracking-wide text-muted-foreground">漏洞描述</div>
              <div className="rounded-md border border-border/55 bg-card px-3 py-2 leading-relaxed">
                {claim.impact || "(未申报影响面)"}
              </div>
            </section>

            {/* 耗时拆解(P4-08 ②;P4-19 迭代整行改写):从开始到
                结束的排队/干活/验证轮次,账本时间戳直读;进行中无结束
                段,如实不造。 */}
            {data.timing && data.timing.landed_at && (
              <section className="flex flex-col gap-1">
                <div className="text-xs font-medium tracking-wide text-muted-foreground">耗时拆解</div>
                <div className="rounded-md border border-border/55 bg-card px-3 py-2 num text-xs text-muted-foreground">
                  {fmtTimingSpan(data.timing.landed_at, data.timing.terminal_at)}
                  {" · "}排队 {fmtDur(data.timing.queue_seconds)} · 干活{" "}
                  {fmtDur(data.timing.exec_seconds)} · 验证 {data.timing.rounds} 轮
                </div>
              </section>
            )}

            {/* 修复建议区(8.8:查表生成,与报告同源逐字一致;未命中不编造) */}
            <section className="flex flex-col gap-1">
              <div className="text-xs font-medium tracking-wide text-muted-foreground">修复建议</div>
              <div className="rounded-md border border-border/55 bg-card px-3 py-2 leading-relaxed">
                {(data.remediation || []).length > 0 ? (
                  <div className="flex flex-col gap-1 text-sm">
                    {data.remediation.map((item, i) => (
                      <div key={i}>{i + 1}. {item}</div>
                    ))}
                  </div>
                ) : (
                  <span className="text-xs text-muted-foreground">
                    {claim.vulnclass ? "该类型暂无预置建议" : "未申报类型,无预置建议"}
                  </span>
                )}
              </div>
            </section>

            {/* 追问与深入(P4-14 追问 / P4-48 深入):问是问、令是令并排;
                追问一成果一会话,深入是节点锚定指令(每次新事件可溯) */}
            <section className="flex flex-col gap-1">
              <div className="text-xs font-medium tracking-wide text-muted-foreground">追问与深入</div>
              <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2">
                <Button
                  variant="outline"
                  className="h-8 text-xs"
                  onClick={() => setFollowup(true)}
                >
                  {React.createElement(iconOf("messageCircle"), { className: "size-3.5" })}
                  {data.followup?.round
                    ? `继续追问(已 ${data.followup.questions} 问)`
                    : "向牧者追问"}
                </Button>
                <Button
                  variant="outline"
                  className="h-8 text-xs"
                  disabled={Boolean(data.dive_disabled)}
                  title={data.dive_disabled || "从该成果出发继续深入,方向可选"}
                  onClick={() => setDive(true)}
                >
                  {React.createElement(iconOf("zap"), { className: "size-3.5" })}
                  深入
                </Button>
                <span className="text-xs text-muted-foreground">
                  问归牧者 · 深入唤醒主宰
                </span>
              </div>
            </section>

            {/* 反证时间线(P4-28 ④:过程信息折叠其下,默认收起;数据与
                报告导出同源同一装配)。P4-56 ⑤:定稿冻结的每轮人话三段
                (疑点→怎么验的→结论)为人话层,机器原文与证据折叠其下。 */}
            {timeline.length > 0 && (
              <section className="flex flex-col gap-1">
                <div className="text-xs font-medium tracking-wide text-muted-foreground">反证时间线</div>
                <div>
                  <button
                    className={
                      "flex w-fit items-center gap-2 rounded-md border px-3 py-1 text-xs transition-colors cursor-pointer " +
                      (tlOpen
                        ? "border-primary/50 text-foreground"
                        : "text-muted-foreground hover:text-foreground")
                    }
                    onClick={() => setTlOpen(!tlOpen)}
                  >
                    {React.createElement(iconOf(tlOpen ? "chevronDown" : "chevronRight"), {
                      className: "size-3.5",
                    })}
                    完整轨迹 {timeline.length} 条(提出 → 反证 → 判定)
                  </button>
                  {tlOpen && (
                    <ol className="relative mt-2 flex flex-col gap-3 border-l pl-4">
                      {timeline.map((e, i) => (
                        <TimelineEntry
                          key={`${e.rev}-${e.kind}-${i}`}
                          entry={e}
                          humanOf={(no) =>
                            (repro?.rounds || []).find((r) => r.round === no) || null
                          }
                        />
                      ))}
                    </ol>
                  )}
                </div>
              </section>
            )}

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
                血缘邻域(资产 → 步骤 → 事实 → 漏洞)· 节点可点击下钻
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
                    {repro.narrative || (
                      <span className="text-xs text-muted-foreground">定稿时未生成叙述</span>
                    )}
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
                  {claim.chain.map((c, i) => (
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
                  data.origin.falsifiers.map((f, i) => (
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
                    当前状态不可复测(锤炼完成后可复测)
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

      {/* 成果追问大窗(P4-14):居中弹出,窗内节点引用下钻自成抽屉栈 */}
      <ClaimFollowupDialog
        tid={tid}
        claimID={claim.id}
        open={followup}
        onClose={() => setFollowup(false)}
      />

      {/* 深入指令窗(P4-48):从该成果出发继续深入 */}
      <DiveDialog
        tid={tid}
        node={{ id: claim.id, title: claim.statement, badge: st }}
        open={dive}
        onClose={() => setDive(false)}
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

function TimelineEntry({ entry, humanOf }) {
  switch (entry.kind) {    case "created":
      return (
        <TimelineItem
          rev={entry.rev}
          title="判断已记录，等待安排反证"
          tone="running"
          detail={
            <>
              发现单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
            </>
          }
        />
      );
    case "falsify":
      return (
        <FalsifyTimelineEntry entry={entry} humanOf={humanOf} />
      );
    case "verdict": {
      const meta = DECISION_META[entry.decision] || {
        label: entry.decision,
        tone: "running",
      };
      return (
        <TimelineItem
          rev={entry.rev}
          title={`仲裁者轮末评估 · ${meta.label}`}
          tone={meta.tone}
          detail={
            <>
              {entry.pending_reason && <div>{entry.pending_reason}</div>}
              {(entry.conditions || []).length > 0 && (
                <div className="mt-1 flex flex-wrap gap-2">
                  {entry.conditions.map((c, i) => (
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
              {" · "}其他可能原因全部复活,重新反证
              {entry.reason && <div>理由:{entry.reason}</div>}
            </>
          }
        />
      );
    case "suspend":
      return (
        <TimelineItem
          rev={entry.rev}
          title="方向收窄,反证挂起"
          tone="warning"
          detail={
            <>
              {entry.reason && <div>理由:{entry.reason}</div>}
              <div>重新聚焦该方向时自动恢复反证</div>
            </>
          }
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
              严重级别:{severityMeta(entry.severity)?.label || entry.severity || "未定级"}(随末轮影响面证据定级)
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
              : entry.status === "pending"
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

// FalsifyTimelineEntry 一轮反证(P4-56 ⑤ 人话化):定稿冻结的三段人话
// (疑点→怎么验的→结论)为默认呈现层;机器原文(存在性/影响面/解释
// 结论/证据/备注)折叠在"机器原文"里,点开可查(等宽与原始数据走详情
// 其余区)。无人话(旧定稿/重开后未再定稿)如实只呈现机器层。
function FalsifyTimelineEntry({ entry, humanOf }) {
  const [rawOpen, setRawOpen] = React.useState(false);
  const rh = humanOf ? humanOf(entry.round || 1) : null;
  const hasHuman = rh && (rh.doubt || rh.how || rh.conclusion);
  return (
    <TimelineItem
      rev={entry.rev}
      title={`第 ${entry.round || 1} 轮反证(独立单位)`}
      tone="running"
      detail={
        <>
          验证单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
          {entry.step_id && (
            <>
              {" "}· 反证步骤{" "}
              <span className="num text-xs">{entry.step_id}</span>
            </>
          )}
          {hasHuman ? (
            <div className="mt-1 flex flex-col gap-1">
              {rh.doubt && (
                <div>
                  <span className="text-muted-foreground">疑点:</span>
                  {rh.doubt}
                </div>
              )}
              {rh.how && (
                <div>
                  <span className="text-muted-foreground">怎么验的:</span>
                  {rh.how}
                </div>
              )}
              {rh.conclusion && (
                <div>
                  <span className="text-muted-foreground">结论:</span>
                  {rh.conclusion}
                </div>
              )}
              {rh.unpolished && (
                <div className="text-muted-foreground">(文字未过文风校验,按原文保留)</div>
              )}
            </div>
          ) : (
            <div className="mt-1 flex flex-col gap-0.5">
              <FalsifyMachineDetail entry={entry} />
            </div>
          )}
          {hasHuman && (
            <div className="mt-1">
              <button
                className={
                  "flex w-fit items-center gap-1 rounded-md border px-2 py-0.5 text-[10px] transition-colors cursor-pointer " +
                  (rawOpen
                    ? "border-primary/50 text-foreground"
                    : "text-muted-foreground hover:text-foreground")
                }
                onClick={() => setRawOpen(!rawOpen)}
              >
                {React.createElement(iconOf(rawOpen ? "chevronDown" : "chevronRight"), {
                  className: "size-3",
                })}
                机器原文与证据
              </button>
              {rawOpen && (
                <div className="mt-1 flex flex-col gap-0.5">
                  <FalsifyMachineDetail entry={entry} />
                </div>
              )}
            </div>
          )}
        </>
      }
    />
  );
}

// FalsifyMachineDetail 一轮反证的机器层(结构化结论、逐条解释排除、
// 证据截头与备注——仲裁求值依据,审计可查)。
function FalsifyMachineDetail({ entry }) {
  return (
    <>
      <span>
        存在性:{entry.existence_refuted ? "被推翻" : "未推翻"} · 影响面:
        {entry.impact_supported
          ? `证据充分${entry.impact_level ? `(定级 ${severityMeta(entry.impact_level)?.label || entry.impact_level})` : ""}`
          : "证据不足"}
      </span>
      {(entry.alt_results || []).length > 0 && (
        <span className="flex flex-wrap gap-2">
          解释结论:
          {entry.alt_results.map((a) => (
            <span
              key={a.id}
              className={
                "rounded px-2 py-0.5 text-[10px] " +
                (a.refuted
                  ? "bg-status-success-soft text-status-success"
                  : "bg-status-warning-soft text-status-warning")
              }
            >
              {a.label || a.id} {a.refuted ? "已排除" : "未排除"}
            </span>
          ))}
        </span>
      )}
      {entry.evidence_head && (
        <span className="text-muted-foreground">证据:{entry.evidence_head}</span>
      )}
      {entry.note && (
        <span className="text-muted-foreground">
          备注(不参与机器判定):{entry.note}
        </span>
      )}
    </>
  );
}

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

// HypothesisDetailSheet 假设详情:陈述/依据 + 回流轨迹(提出步骤 ─产出→
// 假设 ─关于→ 资产;验证步骤 ─验证→ 假设)+ 完整时间线;轨迹中的步骤
// 与资产引用可下钻(跳转协议)。
export function HypothesisDetailSheet({ tid, hypID, onClose, onSelectNode }) {
  const { data, error } = usePolling(
    hypID ? `/api/tasks/${tid}/hypotheses/${hypID}` : "/api/healthz",
    2000,
    { immediate: Boolean(hypID) },
  );
  // 时间线展开态(P4-28 ④:默认折叠)。
  const [hypTlOpen, setHypTlOpen] = React.useState(false);
  if (!hypID) return null;
  if (!data) {
    return (
      <Sheet open onOpenChange={(o) => !o && onClose()}>
        <SheetContent className="overflow-y-auto">
          <SheetHeader>
            <SheetTitle className="num">{hypID}</SheetTitle>
            <SheetDescription>
              {error ? `读取失败:${error}` : "读取假设详情…"}
            </SheetDescription>
          </SheetHeader>
        </SheetContent>
      </Sheet>
    );
  }
  const h = data.hypothesis || {};
  const st = hypDisplayMeta(h) || HYP_STATUS[h.status] || { label: h.status, variant: "info" };
  const timeline = data.timeline || [];
  const traj = data.trajectory || {};
  // 当前结论一句话(P4-28 ③ 同款):终态直陈结果;搁置与过程态不呈现。
  // P4-56:已废弃为三种出局合一(理由留档);成立迁入事实页。
  const hypConclusion =
    h.status === "confirmed"
      ? "当前结论：成立，已迁入事实页"
      : h.status === "discarded"
        ? "当前结论：已废弃，理由留档"
        : "";

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="flex flex-wrap items-center gap-2">
            <StatusBadge meta={st} />
            <span className="num text-sm font-normal text-muted-foreground">{h.id}</span>
            <ViewInGraphButton tid={tid} nodeID={h.id} type="hypothesis" onClose={onClose} />
          </SheetTitle>
          <SheetDescription>{h.statement}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-5 p-5 text-sm">
          {/* 结论区置顶(P4-28 ④ 同款:结论先行,过程折叠其下) */}
          {hypConclusion && (
            <section className="flex flex-col gap-1">
              <div className="text-xs font-medium tracking-wide text-muted-foreground">当前结论</div>
              <div className="rounded-md border border-border/55 bg-card px-3 py-2 text-sm font-medium">
                {hypConclusion}
              </div>
            </section>
          )}
          <section className="flex flex-col gap-1">
            <div className="text-xs font-medium tracking-wide text-muted-foreground">依据(允许弱依据)</div>
            <div className="rounded-md border border-border/55 bg-card px-3 py-2">{h.basis || "(未记录)"}</div>
          </section>

          <section className="flex flex-col gap-2">
            <div className="text-xs font-medium tracking-wide text-muted-foreground">
              回传轨迹(提出 → 评估 → 验证)
            </div>
            <div className="flex flex-col gap-2 rounded-md border border-border/55 bg-card px-3 py-2 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">提出步骤</span>
                <NodeRefButton meta={{ id: traj.discover_step, type: "step", typeLabel: "步骤" }} onSelectNode={onSelectNode} />
                <span className="text-muted-foreground">(产出)</span>
                <span className="text-muted-foreground">→ 假设</span>
                <span className="num">{h.id}</span>
                {h.discoverer_unit && (
                  <span className="text-muted-foreground">· 提出单位 {h.discoverer_unit}</span>
                )}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">指向资产</span>
                {(traj.assets || []).length === 0 ? (
                  <span className="text-muted-foreground">(无)</span>
                ) : (
                  traj.assets.map((id) => (
                    <NodeRefButton key={id} meta={{ id, type: "asset", typeLabel: "资产" }} onSelectNode={onSelectNode} />
                  ))
                )}
                <span className="text-muted-foreground">(关于)</span>
              </div>
              {(traj.verify_steps || []).length === 0 ? (
                <div className="text-muted-foreground">验证步骤:(尚未派发)</div>
              ) : (
                traj.verify_steps.map((vs) => {
                  const vd = HYP_VERDICT[vs.verdict];
                  return (
                    <div key={vs.step_id} className="flex flex-wrap items-center gap-2">
                      <span className="text-muted-foreground">验证步骤</span>
                      <NodeRefButton meta={{ id: vs.step_id, type: "step", typeLabel: "步骤" }} onSelectNode={onSelectNode} />
                      <span className="text-muted-foreground">(验证)</span>
                      <Badge variant="outline">{STEP_STATUS[vs.status] || vs.status}</Badge>
                      {vs.unit && (
                        <span className="text-muted-foreground">· 验证单位 {vs.unit}</span>
                      )}
                      {vd && (
                        <StatusBadge meta={vd} />
                      )}
                      <span className="text-muted-foreground break-all">{vs.intent}</span>
                    </div>
                  );
                })
              )}
            </div>
          </section>

          {/* 时间线(P4-28 ④:过程信息折叠,默认收起) */}
          <section className="flex flex-col gap-2">
            <div className="text-xs font-medium tracking-wide text-muted-foreground">
              时间线(年轮版本刻度)
            </div>
            <button
              className={
                "flex w-fit items-center gap-2 rounded-md border px-3 py-1 text-xs transition-colors cursor-pointer " +
                (hypTlOpen
                  ? "border-primary/50 text-foreground"
                  : "text-muted-foreground hover:text-foreground")
              }
              onClick={() => setHypTlOpen(!hypTlOpen)}
            >
              {React.createElement(iconOf(hypTlOpen ? "chevronDown" : "chevronRight"), {
                className: "size-3.5",
              })}
              完整轨迹 {timeline.length} 条(提出 → 评估 → 验证)
            </button>
            {hypTlOpen && (
              <ol className="relative flex flex-col gap-3 border-l pl-4">
                {timeline.map((e, i) => (
                  <HypTimelineEntry key={`${e.rev}-${i}`} entry={e} />
                ))}
              </ol>
            )}
          </section>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function HypTimelineEntry({ entry }) {
  switch (entry.kind) {
    case "created":
      return (
        <TimelineItem
          rev={entry.rev}
          title="猜测已记录，等待评估"
          tone="running"
          detail={
            <>
              提出单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
            </>
          }
        />
      );
    case "evaluation": {
      const meta = HYP_DECISION[entry.decision] || { label: entry.decision, tone: "running" };
      return (
        <TimelineItem
          rev={entry.rev}
          title={`主宰检查归纳后的评估 · ${meta.label}`}
          tone={meta.tone}
          detail={
            <>
              {entry.reason && <div>理由:{entry.reason}</div>}
              {entry.verify_intent && (
                <div className="text-muted-foreground">验证意图:{entry.verify_intent}</div>
              )}
              {entry.step_id && (
                <div className="text-muted-foreground">
                  验证步骤 <span className="num text-xs">{entry.step_id}</span>
                </div>
              )}
            </>
          }
        />
      );
    }
    case "result": {
      const vd = HYP_VERDICT[entry.verdict] || { label: entry.verdict, variant: "warning" };
      return (
        <TimelineItem
          rev={entry.rev}
          title={`独立单位验证结论 · ${vd.label}`}
          tone={vd.variant}
          detail={
            <>
              验证单位 <span className="num text-xs">{entry.unit_id || "—"}</span>
              {entry.step_id && (
                <>
                  {" "}· 验证步骤 <span className="num text-xs">{entry.step_id}</span>
                </>
              )}
              {entry.evidence_head && (
                <div className="text-muted-foreground">证据:{entry.evidence_head}</div>
              )}
              {entry.note && (
                <div className="text-muted-foreground">备注(不构成结论):{entry.note}</div>
              )}
            </>
          }
        />
      );
    }
    case "state":
      return (
        <TimelineItem
          rev={entry.rev}
          title={`状态 → ${HYP_STATUS[entry.status]?.label || entry.status}`}
          tone={
            entry.status === "confirmed"
              ? "success"
              : entry.status === "discarded"
                ? "info"
                : entry.status === "held"
                  ? "warning"
                  : "running"
          }
          detail=""
        />
      );
    default:
      return null;
  }
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
export function NodeSheetHost({ tid, value, onChange, graph }) {
  const stackRef = React.useRef([]);
  const expectedRef = React.useRef(null); // 本宿主最近发出的换卡(下钻/退栈),用于区分外部替换

  React.useEffect(() => {
    if (!value) {
      stackRef.current = [];
      expectedRef.current = null;
      return;
    }
    const exp = expectedRef.current;
    if (exp && value.id !== exp.id) {
      stackRef.current = []; // 外部直接换卡,栈作废
    }
    expectedRef.current = null;
  }, [value]);

  if (!value?.id) return null;
  const close = () => {
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
    if (!meta?.id || meta.id === value.id) return;
    stackRef.current.push({ type: value.type, id: value.id });
    expectedRef.current = { type: meta.type, id: meta.id };
    onChange({ type: meta.type, id: meta.id });
  };
  switch (value.type) {
    case "testclaim":
      return (
        <ClaimDetailSheet tid={tid} claimID={value.id} onClose={close} onSelectNode={drill} />
      );
    case "hypothesis":
      return (
        <HypothesisDetailSheet tid={tid} hypID={value.id} onClose={close} onSelectNode={drill} />
      );
    case "asset":
      return (
        <AssetDetailSheet tid={tid} nodeID={value.id} onClose={close} onSelectNode={drill} />
      );
    case "goal":
    case "step":
    case "fact":
      return (
        <GenericNodeSheet
          tid={tid}
          nodeID={value.id}
          nodeType={value.type}
          graph={graph}
          onClose={close}
          onSelectNode={drill}
        />
      );
    default:
      return null;
  }
}
