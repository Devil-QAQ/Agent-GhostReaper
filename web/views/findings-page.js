"use client";

import * as React from "react";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { CopyIDButton } from "@/components/copy-id-button";
import { ExportDialog } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { ReproStepsList } from "@/components/repro-steps";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
  CLAIM_STATUS,
  HYP_STATUS,
  HYP_VERDICT,
  REPLAY_VERDICT,
  RETEST_VERDICT,
  SEVERITY,
  postJSON,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 事实成果类(红队战果清单,report.LootClass 服务端确定性分类)。
const LOOT_META = {
  credential: { label: "凭据", variant: "critical" },
  data: { label: "数据", variant: "high" },
  permission: { label: "权限", variant: "medium" },
  foothold: { label: "立足点", variant: "warning" },
};

const LOOT_FILTERS = [
  ["all", "全部类型"],
  ["credential", "凭据"],
  ["data", "数据"],
  ["permission", "权限"],
  ["foothold", "立足点"],
  ["plain", "普通观察"],
];

const FACT_TIME_FILTERS = [
  ["all", "全部时间"],
  ["today", "今天"],
  ["d3", "近 3 天"],
  ["d7", "近 7 天"],
];

// 发现页(8.3 发现域三页签,P2-10 改版):发现页签默认只显示
// Confirmed 成果(打开即战果、主动切才盯过程),状态切换「锤炼中」看
// 候选 / 反证中 / 待确认(含搁置理由);事实页签 = 已确认事实一等列表
//(成果类凭据/数据/权限/立足点战果清单,与图上事实节点跳转协议互跳);
// 假设页签同前。详情抽屉与引用下钻走全站跳转协议(node-sheets)。
export function FindingsPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const [tab, setTab] = React.useState(focus ? "facts" : "claims");

  // 图上事实节点跳回页签定位(?focus=<事实ID>):落在事实页签并高亮。
  const lastFocus = React.useRef(null);
  React.useEffect(() => {
    if (focus && lastFocus.current !== focus) {
      lastFocus.current = focus;
      setTab("facts");
    }
  }, [focus]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader
          title="发现"
          subtitle="成果与过程分列 · 已确认事实一等列出 · 假设回流"
        />
        <EmptyState
          icon="shieldAlert"
          title="未选择任务"
          hint="发现随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="发现"
        subtitle={
          tab === "claims"
            ? "打开即成果(Confirmed 按级别排) · 切「锤炼中」盯过程态"
            : tab === "facts"
              ? "已确认事实一等列出 · 凭据 / 数据 / 权限 / 立足点战果清单"
              : "虫群直觉 · 主宰评估三去向 · 独立单位验证 · 回流轨迹"
        }
      />
      <div className="flex w-fit rounded-lg border bg-card p-1">
        {[
          ["claims", "发现"],
          ["facts", "事实"],
          ["hypos", "假设"],
        ].map(([v, label]) => (
          <button
            key={v}
            className={
              "rounded-md px-4 py-2 text-sm transition-colors cursor-pointer " +
              (tab === v
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground")
            }
            onClick={() => setTab(v)}
          >
            {label}
          </button>
        ))}
      </div>
      {tab === "claims" ? (
        <ClaimsSection tid={tid} />
      ) : tab === "facts" ? (
        <FactsSection tid={tid} focus={focus} />
      ) : (
        <HypothesesSection tid={tid} />
      )}
    </div>
  );
}

// 锤炼中视图的过程态集合(候选 → 反证 → 待确认;Confirmed 归成果视图)。
const FORGING_STATUSES = new Set(["candidate", "testing", "awaiting_round", "pending"]);

// ClaimsSection 发现页签:成果(Confirmed,按级别排)/ 锤炼中(过程态,
// 待确认必附搁置理由)两视图切换——默认成果,主动切换才盯过程。
// P2-13:行内复现步骤折叠展开(点击展开步骤详情,含 Burp 可验证数据包,
// 惰性拉取详情,与报告同源);复选 + 「导出」走格式 × 范围矩阵弹窗。
function ClaimsSection({ tid }) {
  // P3-08:发现新增 / 状态迁移 / 复测重放结论由实时推送驱动刷新;
  // 推送未连接时按 2s 轮询降级。
  const { data, error } = usePolling(`/api/tasks/${tid}/claims`, 2000, {
    live: [`claims:${tid}`],
  });
  const [view, setView] = React.useState("wins");
  const [textFilter, setTextFilter] = React.useState("");
  const [sheet, setSheet] = React.useState(null);
  const [selected, setSelected] = React.useState(() => new Set());
  const [exportOpen, setExportOpen] = React.useState(false);

  const claims = Array.isArray(data) ? data : [];
  // 成果视图含已修复(P3-06:fixed 是已确认发现的修复标记,不消失);
  // 锤炼中视图仍是过程态。
  const visible = claims.filter((c) =>
    view === "wins"
      ? c.status === "confirmed" || c.status === "fixed"
      : FORGING_STATUSES.has(c.status),
  );
  const filtered = visible.filter((c) => {
    const q = textFilter.trim().toLowerCase();
    if (
      q &&
      !`${c.statement}${c.impact || ""}${c.vulnclass || ""}${c.id}`
        .toLowerCase()
        .includes(q)
    )
      return false;
    return true;
  });
  // 成果视图按级别排(级别缺省沉底,同级按入账序)。
  if (view === "wins") {
    filtered.sort((a, b) => {
      const ra = SEVERITY.indexOf(a.severity), rb = SEVERITY.indexOf(b.severity);
      return (ra < 0 ? 99 : ra) - (rb < 0 ? 99 : rb) || a.created_rev - b.created_rev;
    });
  }

  const toggleSel = (id) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const scopeIDs = (scope) =>
    scope === "selected"
      ? [...selected]
      : scope === "filtered"
        ? filtered.map((c) => c.id)
        : null;
  const scopes = [
    ["all", "全部发现"],
    ["filtered", "当前筛选"],
  ];
  if (selected.size > 0) {
    scopes.push(["selected", `勾选项(${selected.size})`]);
  }

  return (
    <div className="flex flex-col gap-6">
      {claims.length === 0 ? (
        <EmptyState
          icon="shieldAlert"
          title="暂无发现"
          hint={error ? `读取失败:${error}` : "虫群单位提交可证伪声明并经反证后在此呈现"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex w-fit rounded-lg border bg-card p-1">
              {[
                ["wins", "成果"],
                ["forging", "锤炼中"],
              ].map(([v, label]) => (
                <button
                  key={v}
                  className={
                    "rounded-md px-3 py-2 text-[13px] transition-colors cursor-pointer " +
                    (view === v
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground")
                  }
                  onClick={() => setView(v)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div className="relative w-56">
              {React.createElement(iconOf("search"), {
                className: "absolute top-3 left-2 size-4 text-muted-foreground",
              })}
              <Input
                className="pl-8"
                placeholder="按声明/类别/ID 搜索"
                value={textFilter}
                onChange={(e) => setTextFilter(e.target.value)}
              />
            </div>
            <Button
              variant="outline"
              className="h-9"
              onClick={() => setExportOpen(true)}
            >
              {React.createElement(iconOf("download"), { className: "size-4" })}
              导出
            </Button>
            <Label className="ml-auto text-xs text-muted-foreground">
              {filtered.length}/{visible.length}
            </Label>
          </div>

          <div className="flex flex-col gap-3">
            {filtered.map((c) => (
              <ClaimRow
                key={c.id}
                tid={tid}
                claim={c}
                checked={selected.has(c.id)}
                onToggle={() => toggleSel(c.id)}
                onOpen={() => setSheet({ type: "testclaim", id: c.id })}
              />
            ))}

            {filtered.length === 0 && (
              <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                {view === "wins" ? "暂无锤炼完成成果;过程态切「锤炼中」查看" : "无过程态声明"}
              </div>
            )}
          </div>
        </>
      )}

      <ExportDialog
        open={exportOpen}
        onOpenChange={setExportOpen}
        title="导出发现"
        description="报告由账本确定性组装,零 LLM;zip 含逐发现分文件与流量证据包。"
        scopes={scopes}
        urlOf={(format, scope) => {
          const ids = scopeIDs(scope);
          const q = ids ? `&claims=${ids.join(",")}` : "";
          return `/api/tasks/${tid}/report/export?format=${format}${q}`;
        }}
        fallbackName={`findings-${tid}`}
      />
      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
    </div>
  );
}

// ClaimRow 单条发现卡:复选 + 徽标行 + 声明 + 待确认搁置理由 + 复现步骤
// 折叠区(有定稿才显示入口;展开时惰性拉取详情,缓存到组件外层状态)。
// 卡片点击开详情抽屉;复选/折叠/复测入口独立点击,不冒泡。P3-06:行内
// 复测按钮(锤炼完成的发现可发起,发起后开详情盯会话)与最近复测结论徽标。
function ClaimRow({ tid, claim, checked, onToggle, onOpen }) {
  const [expanded, setExpanded] = React.useState(false);
  const [detail, setDetail] = React.useState(null);
  const [detailError, setDetailError] = React.useState(null);

  const loadDetail = async () => {
    if (detail || detailError) return;
    try {
      const r = await fetch(`/api/tasks/${tid}/claims/${claim.id}`);
      const body = await r.json();
      if (r.ok) {
        setDetail(body);
      } else {
        setDetailError(body?.error || `HTTP ${r.status}`);
      }
    } catch {
      setDetailError("网络未连通");
    }
  };

  // 发起复测(P3-06):POST 即返,会话异步驱动;开详情抽屉盯进度。
  const startRetest = async () => {
    const { ok, status, body } = await postJSON(
      `/api/tasks/${tid}/claims/${claim.id}/retest`,
      {},
    );
    if (ok) {
      toast.success(`复测第 ${body.round} 轮已发起`);
      onOpen();
    } else {
      toast.error(`发起失败:${body?.error || `HTTP ${status}`}`);
    }
  };

  // 发起重放校验(P3-07):POST 即返,确定性驱动(零模型);开详情抽屉
  // 看逐条交换的新旧对照。
  const startReplay = async () => {
    const { ok, status, body } = await postJSON(
      `/api/tasks/${tid}/claims/${claim.id}/replay`,
      {},
    );
    if (ok) {
      toast.success(`重放第 ${body.round} 轮已发起`);
      onOpen();
    } else {
      toast.error(`发起失败:${body?.error || `HTTP ${status}`}`);
    }
  };

  const steps = detail?.repro?.steps;
  const retestVD = claim.retest_verdict ? RETEST_VERDICT[claim.retest_verdict] : null;
  const replayVD = claim.replay_verdict ? REPLAY_VERDICT[claim.replay_verdict] : null;
  return (
    <div
      className="flex flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer"
      onClick={onOpen}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span onClick={(e) => e.stopPropagation()}>
          <Checkbox checked={checked} onCheckedChange={onToggle} />
        </span>
        <Badge variant={CLAIM_STATUS[claim.status]?.variant || "info"}>
          {CLAIM_STATUS[claim.status]?.label || claim.status}
        </Badge>
        {(() => {
          const sev = severityMeta(claim.severity);
          return sev ? <Badge variant={sev.variant}>{sev.label}</Badge> : null;
        })()}
        {retestVD && (
          <Badge variant={retestVD.variant}>
            复测{retestVD.label}
          </Badge>
        )}
        {replayVD && !claim.replay_running && (
          <Badge variant={replayVD.variant}>
            {replayVD.label}
          </Badge>
        )}
        {claim.combined && <Badge variant="outline">攻击链</Badge>}
        {claim.vulnclass && <Badge variant="outline">{claim.vulnclass}</Badge>}
        {/* P3-11a ③:声明原文为主视觉,tc-ID 弱化为次要可复制小字 */}
        <span className="font-mono text-xs text-muted-foreground">{claim.id}</span>
        <CopyIDButton id={claim.id} noun="声明标识" className="size-5" />
        <span className="ml-auto flex items-center gap-2">
          {claim.replayable && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-xs"
              disabled={claim.replay_running}
              onClick={
                claim.replay_running
                  ? undefined
                  : (e) => {
                      e.stopPropagation();
                      startReplay();
                    }
              }
            >
              {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
              {claim.replay_running ? "重放中" : "重放"}
            </Button>
          )}
          {claim.retestable && (
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-xs"
              disabled={claim.retest_running}
              onClick={
                claim.retest_running
                  ? undefined
                  : (e) => {
                      e.stopPropagation();
                      startRetest();
                    }
              }
            >
              {React.createElement(iconOf("rotateCcw"), { className: "size-3.5" })}
              {claim.retest_running ? "复测中" : "复测"}
            </Button>
          )}
          <span className="font-mono text-xs text-muted-foreground">
            版本号 {claim.created_rev}
          </span>
        </span>
      </div>
      <div className="text-sm">{claim.statement}</div>
      {claim.status === "pending" && claim.pending_reason && (
        <div className="rounded-md bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
          搁置理由:{claim.pending_reason}
        </div>
      )}
      {claim.repro_steps > 0 && (
        <div className="flex flex-col gap-2" onClick={(e) => e.stopPropagation()}>
          <button
            className={
              "flex w-fit items-center gap-2 rounded-md border px-3 py-1 text-xs transition-colors cursor-pointer " +
              (expanded
                ? "border-primary/50 text-foreground"
                : "text-muted-foreground hover:text-foreground")
            }
            onClick={() => {
              setExpanded(!expanded);
              if (!expanded) {
                loadDetail();
              }
            }}
          >
            {React.createElement(iconOf(expanded ? "chevronDown" : "chevronRight"), {
              className: "size-3.5",
            })}
            复现步骤 {claim.repro_steps} 步
          </button>
          {expanded && (
            <div className="rounded-lg bg-muted/30 p-2">
              {detailError ? (
                <span className="text-xs text-status-danger">读取失败:{detailError}</span>
              ) : steps ? (
                <ReproStepsList tid={tid} steps={steps} />
              ) : (
                <span className="text-xs text-muted-foreground">读取复现步骤…</span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// FactsSection 事实页签(P2-10):已确认事实一等列表,筛选 = 类型(成果
// 类)/ 资产 / 时间;条目点击开事实抽屉(抽屉内「在图中查看」跳图聚焦,
// 图上事实节点「在事实列表查看」跳回本页签定位)。
function FactsSection({ tid, focus }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/facts`, 3000);
  const [lootFilter, setLootFilter] = React.useState("all");
  const [assetFilter, setAssetFilter] = React.useState("all");
  const [timeFilter, setTimeFilter] = React.useState("all");
  const [sheet, setSheet] = React.useState(null);
  const [focusPulse, setFocusPulse] = React.useState(null);
  const lastFocused = React.useRef(null);

  const facts = (data?.facts || []).slice().reverse(); // 最新确认在先

  // 资产筛选项自数据派生(有已确认事实的资产)。
  const assetOptions = [];
  const seenAsset = new Set();
  for (const f of data?.facts || []) {
    for (const a of f.assets || []) {
      if (!seenAsset.has(a.id)) {
        seenAsset.add(a.id);
        assetOptions.push(a);
      }
    }
  }

  const withinTime = (at, mode) => {
    if (mode === "all") return true;
    const t = new Date(at.replace(" ", "T"));
    if (Number.isNaN(t.getTime())) return true; // 时刻缺失不筛除(如实呈现)
    const days = mode === "today" || mode === "d3" || mode === "d7" ? { today: 0, d3: 2, d7: 6 }[mode] : 0;
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    start.setDate(start.getDate() - days);
    return t >= start;
  };

  const filtered = facts.filter((f) => {
    if (lootFilter !== "all") {
      const want = lootFilter === "plain" ? "" : lootFilter;
      if ((f.loot || "") !== want) return false;
    }
    if (assetFilter !== "all") {
      if (!(f.assets || []).some((a) => a.id === assetFilter)) return false;
    }
    if (!withinTime(f.confirmed_at || f.created_at, timeFilter)) return false;
    return true;
  });

  // focus 定位(图上事实节点跳回):滚动入视 + 高亮 2.4 秒。
  React.useEffect(() => {
    if (!focus) {
      lastFocused.current = null;
      return;
    }
    if (lastFocused.current === focus) return;
    lastFocused.current = focus;
    setFocusPulse(focus);
    const t = setTimeout(() => setFocusPulse(null), 2400);
    return () => clearTimeout(t);
  }, [focus, facts.length]);

  React.useEffect(() => {
    if (!focusPulse) return;
    document
      .querySelector(`[data-fact-row="${CSS.escape(focusPulse)}"]`)
      ?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focusPulse]);

  return (
    <div className="flex flex-col gap-6">
      {(data?.facts || []).length === 0 ? (
        <EmptyState
          icon="check"
          title="暂无已确认事实"
          hint={error ? `读取失败:${error}` : "主宰收割转正的事实在此一等列出"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="w-32">
              <Select value={lootFilter} onValueChange={setLootFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LOOT_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="w-44">
              <Select value={assetFilter} onValueChange={setAssetFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部资产</SelectItem>
                  {assetOptions.map((a) => (
                    <SelectItem key={a.id} value={a.id}>
                      {a.title || a.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="w-32">
              <Select value={timeFilter} onValueChange={setTimeFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {FACT_TIME_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Label className="ml-auto text-xs text-muted-foreground">
              {filtered.length}/{facts.length}
            </Label>
          </div>

          <div className="flex flex-col gap-3">
            {filtered.map((f) => {
              const loot = LOOT_META[f.loot];
              return (
                <button
                  key={f.id}
                  data-fact-row={f.id}
                  className={cn(
                    "flex flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer",
                    focusPulse === f.id && "border-primary ring-2 ring-primary",
                  )}
                  onClick={() => setSheet({ type: "fact", id: f.id })}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    {loot ? (
                      <Badge variant={loot.variant}>{loot.label}</Badge>
                    ) : (
                      <Badge variant="outline">观察</Badge>
                    )}
                    {(f.assets || []).map((a) => (
                      <span
                        key={a.id}
                        className="rounded-md border px-2 py-0.5 font-mono text-[10px] text-muted-foreground"
                        title={a.title || a.id}
                      >
                        {a.title || a.id}
                      </span>
                    ))}
                    <span className="font-mono text-xs text-muted-foreground">{f.id}</span>
                    <span className="ml-auto font-mono text-xs text-muted-foreground">
                      确认 {f.confirmed_at?.slice(5) || "—"} · 版本号 {f.confirmed_rev}
                    </span>
                  </div>
                  <div className="text-sm">{f.statement}</div>
                  {f.evidence_head && (
                    <div className="truncate font-mono text-xs text-muted-foreground" title={f.evidence_head}>
                      证据:{f.evidence_head}
                    </div>
                  )}
                </button>
              );
            })}
            {filtered.length === 0 && (
              <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                无匹配事实
              </div>
            )}
          </div>
        </>
      )}

      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
    </div>
  );
}

const HYP_STATUS_FILTERS = [
  ["all", "全部状态"],
  ["candidate", "候选"],
  ["testing", "验证中"],
  ["held", "挂账"],
  ["confirmed", "成立"],
  ["discarded", "已废弃"],
];

// HypothesesSection 假设列表:候选 → 主宰评估三去向(转验证/挂账/废弃)
// → 验证结论;挂账与废弃必带理由(R10 直觉有账、废案有理由)。
function HypothesesSection({ tid }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/hypotheses`, 2000);
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [sheet, setSheet] = React.useState(null);

  const hypos = Array.isArray(data) ? data : [];
  const filtered = hypos.filter((h) => statusFilter === "all" || h.status === statusFilter);

  return (
    <div className="flex flex-col gap-6">
      {hypos.length === 0 ? (
        <EmptyState
          icon="sparkles"
          title="暂无假设"
          hint={error ? `读取失败:${error}` : "虫群单位提交直觉候选,主宰收割裁决后在此呈现"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="w-36">
              <Select value={statusFilter} onValueChange={setStatusFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {HYP_STATUS_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Label className="ml-auto text-xs text-muted-foreground">
              {filtered.length}/{hypos.length}
            </Label>
          </div>

          <div className="flex flex-col gap-3">
            {filtered.map((h) => {
              const st = HYP_STATUS[h.status] || { label: h.status, variant: "info" };
              return (
                <button
                  key={h.id}
                  className="flex flex-col gap-2 rounded-lg border bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer"
                  onClick={() => setSheet({ type: "hypothesis", id: h.id })}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={st.variant}>{st.label}</Badge>
                    {(() => {
                      const vd = HYP_VERDICT[h.verdict];
                      return vd ? (
                        <Badge variant={vd.tone === "success" ? "success" : vd.tone === "danger" ? "danger" : "warning"}>
                          验证{vd.label}
                        </Badge>
                      ) : null;
                    })()}
                    {(h.asset_ids || []).map((id) => (
                      <span
                        key={id}
                        className="rounded-md border px-2 py-0.5 font-mono text-[10px] text-muted-foreground"
                      >
                        {id}
                      </span>
                    ))}
                    <span className="font-mono text-xs text-muted-foreground">{h.id}</span>
                    <span className="ml-auto font-mono text-xs text-muted-foreground">
                      版本号 {h.created_rev}
                    </span>
                  </div>
                  <div className="text-sm">{h.statement}</div>
                  {h.status === "held" && h.reason && (
                    <div className="rounded-md bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
                      挂账理由:{h.reason}
                    </div>
                  )}
                  {h.status === "discarded" && h.reason && (
                    <div className="rounded-md bg-status-danger-soft px-3 py-2 text-xs text-status-danger">
                      废弃理由:{h.reason}
                    </div>
                  )}
                </button>
              );
            })}
            {filtered.length === 0 && (
              <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                无匹配假设
              </div>
            )}
          </div>
        </>
      )}

      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
    </div>
  );
}
