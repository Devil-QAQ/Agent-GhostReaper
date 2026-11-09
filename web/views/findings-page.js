"use client";

import * as React from "react";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { CopyIDButton } from "@/components/copy-id-button";
import { ExportDialog } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { ReproStepsPaged } from "@/components/repro-steps";
import { StatusBadge } from "@/components/status-badge";
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
  FACT_STATUS,
  HYP_STATUS,
  HYP_VERDICT,
  REPLAY_VERDICT,
  RETEST_VERDICT,
  SEVERITY,
  claimDisplayMeta,
  hypDisplayMeta,
  postJSON,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 事实成果类(红队战果清单,report.LootClass 服务端确定性分类;hint 为
// 成果类悬停大白话,P4-28 ⑦——筛选下拉与徽标同文案、同一张表)。
const LOOT_META = {
  credential: { label: "凭据", variant: "critical", hint: "可直接使用的账号密码、密钥、令牌" },
  data: { label: "数据", variant: "high", hint: "拿到的敏感信息，如用户名单、源码" },
  permission: { label: "权限", variant: "medium", hint: "取得的访问能力，如某接口无需登录可调用" },
  foothold: { label: "立足点", variant: "warning", hint: "在目标里的攻击据点，如可控账号、上传的脚本" },
  plain: { label: "普通观察", variant: "outline", hint: "一般性记录，不属于以上收获" },
};

const LOOT_FILTERS = [
  ["all", "全部类型"],
  ...Object.entries(LOOT_META).map(([v, m]) => [v, m.label]),
];

const FACT_TIME_FILTERS = [
  ["all", "全部时间"],
  ["today", "今天"],
  ["d3", "近 3 天"],
  ["d7", "近 7 天"],
];

// 发现区四页签(P4-56,问题清单 #77):假设→事实→锤炼→成果漏斗,页签
// 按流程阶段分、条目往右迁移、左边不留——假设验证成立迁入事实页(带
// "由假设升级"来路),锤炼反证确认迁入成果页;跨对象"锤炼中"合并词与
// 内藏视图切换退役。页签计数徽标替代原"锤炼积压行"。
const FINDING_TABS = [
  ["hypos", "假设"],
  ["facts", "事实"],
  ["forging", "锤炼"],
  ["results", "成果"],
];

// 成果页收录的显示键(confirmed/fixed:已确认含已修复标记)。
const RESULT_DISPLAYS = new Set(["confirmed", "fixed"]);
// 锤炼页收录其余全部显示键(等待反证/反证中/反证暂停/证据不足/已废弃)。

export function FindingsPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const [tab, setTab] = React.useState(focus ? "facts" : "results");

  // 图上事实节点跳回页签定位(?focus=<事实ID>):落在事实页签并高亮。
  const lastFocus = React.useRef(null);
  React.useEffect(() => {
    if (focus && lastFocus.current !== focus) {
      lastFocus.current = focus;
      setTab("facts");
    }
  }, [focus]);

  // P4-47 A 未读红点(看过即消):成果与事实两个子页签独立记已读。
  // 计数轮询由发现 / 任务两主题实时驱动(确认落账即变);进入成果或
  // 事实子页签即向服务端盖"看过"戳(时刻为服务端时间,跨浏览器一致),
  // 响应即刻灭点。修订(造物主 2026-11-06):"看过"扩为前台停留期间
  // 持续看过——正停留的子页签新增红点亮起约 3 秒后自动盖戳消点,
  // 详见下方停留盖戳。
  const { data: unreadBody } = usePolling(
    tid ? `/api/tasks/${tid}/unread` : "/api/healthz",
    2000,
    { immediate: Boolean(tid), live: tid ? [`claims:${tid}`, "tasks"] : null },
  );
  // 盖戳响应的瞬时覆盖:轮询周期 2 秒,覆盖 3 秒后交还轮询值(期间
  // 任何轮询请求都发起于盖戳之后,值已一致,无新旧错判)。
  const [seenOverride, setSeenOverride] = React.useState(null);
  React.useEffect(() => {
    setSeenOverride(null);
    if (!tid || (tab !== "results" && tab !== "facts")) return;
    let alive = true;
    postJSON(`/api/tasks/${tid}/seen`, { kind: tab === "results" ? "claims" : "facts" }).then(
      ({ ok, body }) => {
        if (ok && alive) {
          setSeenOverride({
            claims: body?.claims ?? 0,
            facts: body?.facts ?? 0,
          });
        }
      },
    );
    return () => {
      alive = false;
    };
  }, [tid, tab]);
  React.useEffect(() => {
    if (!seenOverride) return;
    const t = setTimeout(() => setSeenOverride(null), 3000);
    return () => clearTimeout(t);
  }, [seenOverride]);
  const unread = {
    claims: seenOverride ? seenOverride.claims : unreadBody?.claims ?? 0,
    facts: seenOverride ? seenOverride.facts : unreadBody?.facts ?? 0,
  };

  // 页面可见性(停留即已读的判据之一):浏览器标签页在前台才自动盖戳,
  // 后台期间到达的未读保留,回前台停留满 3 秒才视为看过。
  const [visible, setVisible] = React.useState(true);
  React.useEffect(() => {
    const onVis = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", onVis);
    onVis();
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  // 停留即已读(修订):停留在成果/事实子页签且页面可见、该分线有
  // 未读时,亮起约 3 秒(给注意的时间)后自动盖戳消点——列表本身实时
  // 刷新,正看着即已看。任一条件变化(跳走子页签/转后台/计数再变)
  // 作废计时重新计:3 秒内跳走则不盖,红点保留,回来点一下才消。
  const dwellKind = tab === "results" ? "claims" : tab === "facts" ? "facts" : null;
  const dwellUnread = dwellKind ? unread[dwellKind] : 0;
  React.useEffect(() => {
    if (!tid || !dwellKind || !visible || dwellUnread <= 0) return;
    let alive = true;
    const t = setTimeout(() => {
      if (!alive || document.visibilityState !== "visible") return;
      postJSON(`/api/tasks/${tid}/seen`, { kind: dwellKind }).then(({ ok, body }) => {
        if (ok && alive) {
          setSeenOverride({ claims: body?.claims ?? 0, facts: body?.facts ?? 0 });
        }
      });
    }, 3000);
    return () => {
      alive = false;
      clearTimeout(t);
    };
  }, [tid, dwellKind, visible, dwellUnread]);

  // 页签计数徽标(P4-56 ④:计数替代原"锤炼积压行";数字与各页签条目
  // 同口径,零计数不占位)。假设页不含成立条目(已迁入事实页)。
  const { data: claimsData } = usePolling(tid ? `/api/tasks/${tid}/claims` : "/api/healthz", 4000, {
    immediate: Boolean(tid),
    live: tid ? [`claims:${tid}`] : null,
  });
  const { data: hyposData } = usePolling(
    tid ? `/api/tasks/${tid}/hypotheses` : "/api/healthz",
    4000,
    { immediate: Boolean(tid) },
  );
  const { data: factsData } = usePolling(tid ? `/api/tasks/${tid}/facts` : "/api/healthz", 6000, {
    immediate: Boolean(tid),
  });
  const counts = React.useMemo(() => {
    const claims = Array.isArray(claimsData) ? claimsData : [];
    const hypos = Array.isArray(hyposData) ? hyposData : [];
    const facts = Array.isArray(factsData?.facts) ? factsData.facts : [];
    return {
      hypos: hypos.filter((h) => h.status !== "confirmed").length,
      facts: facts.length,
      forging: claims.filter((c) => !RESULT_DISPLAYS.has(c.display || "")).length,
      results: claims.filter((c) => RESULT_DISPLAYS.has(c.display || "")).length,
    };
  }, [claimsData, hyposData, factsData]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="发现" subtitle="假设 → 事实 → 锤炼 → 成果" />
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
          tab === "hypos"
            ? "虫群直觉 · 等待验证与验证中 · 成立迁入事实"
            : tab === "facts"
              ? "已确认事实一等列出 · 凭据 / 数据 / 权限 / 立足点战果清单"
              : tab === "forging"
                ? "漏洞怀疑在锤炼 · 等待反证 → 反证中 → 结论 · 确认迁入成果"
                : "打开即成果(已确认按级别排) · 已修复带标记"
        }
      />
      <div className="flex w-fit rounded-lg border border-border/55 bg-card p-1">
        {FINDING_TABS.map(([v, label]) => (
          <button
            key={v}
            className={
              "flex items-center rounded-md px-4 py-2 text-sm transition-colors cursor-pointer " +
              (tab === v
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground")
            }
            onClick={() => setTab(v)}
          >
            {label}
            {/* 页签计数徽标(P4-56 ④):条目数,零不占位 */}
            {counts[v] > 0 && (
              <span
                className={
                  "ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] font-semibold leading-none num " +
                  (tab === v ? "bg-primary-foreground/20 text-primary-foreground" : "bg-muted text-muted-foreground")
                }
              >
                {counts[v]}
              </span>
            )}
            {/* 未读红点+计数(P4-47 A):看过即消,成果与事实各自独立;
                假设/锤炼过程态不标未读 */}
            {(v === "results" && unread.claims > 0) || (v === "facts" && unread.facts > 0) ? (
              <span
                className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-none text-primary-foreground num"
                title={
                  v === "results"
                    ? `新增已确认成果 ${unread.claims} 条`
                    : `新确认事实 ${unread.facts} 条`
                }
              >
                {v === "results" ? unread.claims : unread.facts}
              </span>
            ) : null}
          </button>
        ))}
      </div>
      {tab === "hypos" ? (
        <HypothesesSection tid={tid} />
      ) : tab === "facts" ? (
        <FactsSection tid={tid} focus={focus} />
      ) : tab === "forging" ? (
        <ForgingSection tid={tid} />
      ) : (
        <ResultsSection tid={tid} />
      )}
    </div>
  );
}

// ResultsSection 成果页签:Confirmed(含已修复标记)按级别排——四页签
// 漏斗的终点,打开即战果。P2-13:行内复现步骤折叠展开(点击展开步骤
// 详情,含 Burp 可验证数据包,惰性拉取详情,与报告同源);复选 +
// 「导出」走格式 × 范围矩阵弹窗。
function ResultsSection({ tid }) {
  // P3-08:发现新增 / 状态迁移 / 复测重放结论由实时推送驱动刷新;
  // 推送未连接时按 2s 轮询降级。
  const { data, error } = usePolling(`/api/tasks/${tid}/claims`, 2000, {
    live: [`claims:${tid}`],
  });
  const [textFilter, setTextFilter] = React.useState("");
  const [sheet, setSheet] = React.useState(null);
  const [selected, setSelected] = React.useState(() => new Set());
  const [exportOpen, setExportOpen] = React.useState(false);

  const claims = (Array.isArray(data) ? data : []).filter((c) =>
    RESULT_DISPLAYS.has(c.display || ""),
  );
  const filtered = claims.filter((c) => {
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
  // 成果按级别排(级别缺省沉底,同级按入账序)。
  filtered.sort((a, b) => {
    const ra = SEVERITY.indexOf(a.severity), rb = SEVERITY.indexOf(b.severity);
    return (ra < 0 ? 99 : ra) - (rb < 0 ? 99 : rb) || a.created_rev - b.created_rev;
  });

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
          title="暂无成果"
          hint={error ? `读取失败:${error}` : "反证确认成立的漏洞在此按级别列出;过程态切「锤炼」查看"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-56">
              {React.createElement(iconOf("search"), {
                className: "absolute top-3 left-2 size-4 text-muted-foreground",
              })}
              <Input
                className="pl-8"
                placeholder="按判断/类别/ID 搜索"
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
              {filtered.length}/{claims.length}
            </Label>
          </div>

          <div className="flex flex-col gap-4">
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
                无匹配成果
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

// ClaimRow 成果卡:复选 + 徽标行 + 声明 + 复现步骤折叠区(有定稿才显示
// 入口;展开时惰性拉取详情,缓存到组件外层状态)。卡片点击开详情抽屉;
// 复选/折叠/复测入口独立点击,不冒泡。P3-06:行内复测按钮(锤炼完成的
// 发现可发起,发起后开详情盯会话)与最近复测结论徽标。
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

  const retestVD = claim.retest_verdict ? RETEST_VERDICT[claim.retest_verdict] : null;
  const replayVD = claim.replay_verdict ? REPLAY_VERDICT[claim.replay_verdict] : null;
  const st = claimDisplayMeta(claim) || CLAIM_STATUS[claim.status] || { label: claim.status, variant: "info" };
  return (
    <div
      className="flex flex-col gap-2 rounded-xl border border-border/55 bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer"
      onClick={onOpen}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span onClick={(e) => e.stopPropagation()}>
          <Checkbox checked={checked} onCheckedChange={onToggle} />
        </span>
        <StatusBadge meta={st} />
        {(() => {
          const sev = severityMeta(claim.severity);
          return sev ? <Badge variant={sev.variant}>{sev.label}</Badge> : null;
        })()}
        {retestVD && (
          <StatusBadge meta={retestVD}>
            复测{retestVD.label}
          </StatusBadge>
        )}
        {replayVD && !claim.replay_running && (
          <Badge variant={replayVD.variant}>
            {replayVD.label}
          </Badge>
        )}
        {claim.combined && <Badge variant="outline">攻击链</Badge>}
        {claim.vulnclass && <Badge variant="outline">{claim.vulnclass}</Badge>}
        {/* P4-19 迭代:判断原文为主视觉,tc-ID 不上正文,悬停与复制回执可取 */}
        <CopyIDButton id={claim.id} noun="判断标识" className="size-5" />
        <span className="ml-auto flex items-center gap-2" title={`版本号 ${claim.created_rev}`}>
          {claim.replayable && (
            <Button
              variant="outline"
              size="sm"
              className="h-8 px-2 text-xs"
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
              className="h-8 px-2 text-xs"
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

        </span>
      </div>
      <div className="text-sm">{claim.statement}</div>
      {claim.report_excluded && (
        <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
          不纳入报告:{claim.report_excluded_reason || "信息性观察,无直接危害"}
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
              ) : detail?.repro ? (
                <ReproStepsPaged tid={tid} claimID={claim.id} repro={detail.repro} stepsTotal={claim.repro_steps} />
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

// 锤炼页签筛选(P4-56:按显示键筛,词与徽标同源)。
const FORGING_FILTERS = [
  ["all", "全部状态"],
  ["wait_falsify", "等待反证"],
  ["falsifying", "反证中"],
  ["falsify_paused", "反证暂停"],
  ["insufficient", "证据不足"],
  ["discarded", "已废弃"],
];

// ForgingSection 锤炼页签:漏洞怀疑的全部锤炼态(等待反证→反证中→
// 结论;证据不足/已废弃留在本页可见、理由直陈)。显示徽标带悬停细分
// (重派轮次/换法/等待主宰判定/任务完成中断);血缘引用的事实可点回
// (P4-56 ④ 迁移血缘)。确认后迁入成果页,本页不留。
function ForgingSection({ tid }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/claims`, 2000, {
    live: [`claims:${tid}`],
  });
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [sheet, setSheet] = React.useState(null);

  const claims = (Array.isArray(data) ? data : []).filter((c) =>
    !RESULT_DISPLAYS.has(c.display || ""),
  );
  const filtered = claims.filter((c) =>
    statusFilter === "all" ? true : (c.display || "") === statusFilter,
  );

  return (
    <div className="flex flex-col gap-6">
      {claims.length === 0 ? (
        <EmptyState
          icon="shieldAlert"
          title="暂无锤炼条目"
          hint={error ? `读取失败:${error}` : "有危害的漏洞怀疑经反证锤炼后在此呈现,确认迁入成果"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="w-32">
              <Select value={statusFilter} onValueChange={setStatusFilter}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {FORGING_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Label className="ml-auto text-xs text-muted-foreground">
              {filtered.length}/{claims.length}
            </Label>
          </div>

          <div className="flex flex-col gap-4">
            {filtered.map((c) => {
              const st = claimDisplayMeta(c) || CLAIM_STATUS[c.status] || { label: c.status, variant: "info" };
              return (
                <button
                  key={c.id}
                  className="flex flex-col gap-2 rounded-xl border border-border/55 bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer"
                  onClick={() => setSheet({ type: "testclaim", id: c.id })}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <StatusBadge meta={st} />
                    {/* 暂停态标"等待审批"(P4-56 ②:静止+标,单位在 RoE 等批准) */}
                    {c.display === "falsify_paused" && (
                      <span className="rounded-md bg-status-running-soft px-2 py-0.5 text-[10px] text-status-running">
                        等待审批
                      </span>
                    )}
                    {c.vulnclass && <Badge variant="outline">{c.vulnclass}</Badge>}
                    {(c.fact_refs || []).map((f) => (
                      <span
                        key={f.id}
                        className="rounded-md border px-2 py-0.5 text-[10px] text-muted-foreground"
                        title={`引用事实:${f.title}`}
                      >
                        事实:{f.title.length > 24 ? f.title.slice(0, 24) + "…" : f.title}
                      </span>
                    ))}
                    <CopyIDButton id={c.id} noun="判断标识" className="size-5" />
                    <span className="ml-auto num text-xs text-muted-foreground" title={`版本号 ${c.created_rev}`}>
                      {c.verifier ? `验证 ${c.verifier}` : ""}
                    </span>
                  </div>
                  <div className="text-sm">{c.statement}</div>
                  {(c.display === "insufficient" || c.status === "pending") && c.pending_reason && (
                    <div className="rounded-md bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
                      证据不足:{c.pending_reason}
                    </div>
                  )}
                </button>
              );
            })}
            {filtered.length === 0 && (
              <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                无匹配锤炼条目
              </div>
            )}
          </div>
        </>
      )}

      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
    </div>
  );
}

// FactsSection 事实页签(P2-10 + P4-56):已确认事实一等列表 + 新进
// 标"新"(收割确认后自动转正) + 已成立假设迁入(带"由假设升级"来路,
// 点击开假设抽屉);筛选 = 类型(成果类)/ 资产 / 时间;条目点击开事实
// 抽屉(抽屉内「在图中查看」跳图聚焦,图上事实节点「在事实列表查看」
// 跳回本页签定位)。
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
          title="暂无事实"
          hint={error ? `读取失败:${error}` : "经主宰检查归纳确认的事实在此一等列出"}
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
                    <SelectItem key={v} value={v} title={v === "all" ? undefined : LOOT_META[v].hint}>
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

          <div className="flex flex-col gap-4">
            {filtered.map((f) => {
              const loot = LOOT_META[f.loot || "plain"];
              return (
                <button
                  key={f.id}
                  data-fact-row={f.id}
                  className={cn(
                    "flex flex-col gap-2 rounded-xl border border-border/55 bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer",
                    focusPulse === f.id && "border-primary ring-2 ring-primary",
                  )}
                  onClick={() =>
                    setSheet({ type: f.kind === "hypothesis" ? "hypothesis" : "fact", id: f.id })
                  }
                >
                  <div className="flex flex-wrap items-center gap-2">
                    {f.kind === "hypothesis" ? (
                      // 迁入来路(P4-56 ④):验证成立迁入事实页,点开回假设抽屉
                      <Badge variant="success">已确认</Badge>
                    ) : f.is_new ? (
                      <StatusBadge meta={FACT_STATUS.candidate} />
                    ) : (
                      <StatusBadge meta={FACT_STATUS.confirmed} />
                    )}
                    {f.from_hypothesis && <Badge variant="outline">由假设升级</Badge>}
                    <StatusBadge meta={loot} />
                    {(f.assets || []).map((a) => (
                      <span
                        key={a.id}
                        className="rounded-md border px-2 py-0.5 num text-[10px] text-muted-foreground"
                        title={a.title || a.id}
                      >
                        {a.title || a.id}
                      </span>
                    ))}
                    <span className="num text-xs text-muted-foreground">{f.id}</span>
                    <span
                      className="ml-auto num text-xs text-muted-foreground"
                      title={`版本号 ${f.confirmed_rev}`}
                    >
                      {f.is_new ? "新进" : "确认"} {f.confirmed_at?.slice(5) || "—"}
                    </span>
                  </div>
                  <div className="text-sm">{f.statement}</div>
                  {f.evidence_head && (
                    <div className="truncate num text-xs text-muted-foreground" title={f.evidence_head}>
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

// 假设页签筛选(P4-56:按显示键筛;成立条目不在本页)。
const HYP_FILTERS = [
  ["all", "全部状态"],
  ["wait_verify", "等待验证"],
  ["verifying", "验证中"],
  ["verify_paused", "验证暂停"],
  ["held", "搁置"],
  ["discarded", "已废弃"],
];

// HypothesesSection 假设页签:虫群直觉,等待验证→验证中→结论;搁置与
// 已废弃留档可见(理由直陈,R10 直觉有账、废案有理由);验证成立迁入
// 事实页(带"由假设升级"),本页不留。
function HypothesesSection({ tid }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/hypotheses`, 2000);
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [sheet, setSheet] = React.useState(null);

  const hypos = (Array.isArray(data) ? data : []).filter((h) => h.status !== "confirmed");
  const filtered = hypos.filter((h) =>
    statusFilter === "all" ? true : (h.display || "") === statusFilter,
  );

  return (
    <div className="flex flex-col gap-6">
      {hypos.length === 0 ? (
        <EmptyState
          icon="sparkles"
          title="暂无假设"
          hint={error ? `读取失败:${error}` : "虫群单位提交直觉猜测,等待安排验证;成立迁入事实页"}
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
                  {HYP_FILTERS.map(([v, label]) => (
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

          <div className="flex flex-col gap-4">
            {filtered.map((h) => {
              const st = hypDisplayMeta(h) || HYP_STATUS[h.status] || { label: h.status, variant: "info" };
              return (
                <button
                  key={h.id}
                  className="flex flex-col gap-2 rounded-xl border border-border/55 bg-card p-4 text-left transition-shadow hover:shadow-card cursor-pointer"
                  onClick={() => setSheet({ type: "hypothesis", id: h.id })}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <StatusBadge meta={st} />
                    {h.display === "verify_paused" && (
                      <span className="rounded-md bg-status-running-soft px-2 py-0.5 text-[10px] text-status-running">
                        等待审批
                      </span>
                    )}
                    {(() => {
                      const vd = HYP_VERDICT[h.verdict];
                      return vd ? (
                        <StatusBadge meta={vd}>验证{vd.label}</StatusBadge>
                      ) : null;
                    })()}
                    {(h.asset_ids || []).map((id) => (
                      <span
                        key={id}
                        className="rounded-md border px-2 py-0.5 num text-[10px] text-muted-foreground"
                      >
                        {id}
                      </span>
                    ))}
                    <span className="num text-xs text-muted-foreground">{h.id}</span>

                  </div>
                  <div className="text-sm">{h.statement}</div>
                  {h.status === "held" && h.reason && (
                    <div className="rounded-md bg-status-warning-soft px-3 py-2 text-xs text-status-warning">
                      搁置理由:{h.reason}
                    </div>
                  )}
                  {h.status === "discarded" && h.reason && (
                    <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
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
