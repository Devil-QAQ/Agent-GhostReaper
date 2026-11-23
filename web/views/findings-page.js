"use client";

import * as React from "react";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { CopyIDButton } from "@/components/copy-id-button";
import { ClaimFollowupDialog } from "@/components/claim-followup-dialog";
import { ExportDialog } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { SearchInput } from "@/components/search-input";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  CLAIM_DISPLAY,
  FACT_RESULT_META,
  RETEST_VERDICT,
  postJSON,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { PageBar } from "@/views/traffic-page";
import {
  LEDGER_PAGE_SIZE,
  byIntentTimeDesc,
  byLandedTimeDesc,
  byVulnLedgerDesc,
  ledgerPage,
} from "../lib/finding-sort.mjs";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 发现页三页签零状态台账(P5-07①,v1.3.0):页签只展示产出,不管过程——
// 意图(主宰提出的每一条,提出即落页)/ 事实(虫群确认的真结论)/ 漏洞
//(坐实,页签由"成果"改名)。三页统一落页时间倒序(意图=提出时刻、
// 事实/漏洞=确认时刻),唯一例外:证据不足灰区永远沉漏洞页底;每页
// 固定 20 条翻页,各页搜索框匹配标题与摘要(P5-07③④⑤)。两层未读
// 提醒(P5-08):页签数字红点(进页即消)＋条目淡红点(点开详情才消),
// 两层不联动。

// 页签 → 未读 kind(P5-08④):漏洞页沿 claims 键面(历史已读线同键续用)。
const TAB_KIND = { intents: "intents", facts: "facts", vulns: "claims" };

// 详情对象类型 → 条目级已读 kind(发现页三页签的条目;其余类型不入
// 未读体系)。
const SHEET_KIND = { step: "intents", fact: "facts", testclaim: "claims" };

// 追问按钮旁提示文案(P5-07② 锚例逐字,一字不改)。
const FOLLOWUP_HINT = "可问相关问题，也可让它深入继续挖";

// 事实结果类型：读账本明确分类；筛选、徽标、详情共用一张词表。
const LOOT_FILTERS = [
  ["all", "全部类型"],
  ...Object.entries(FACT_RESULT_META).map(([v, m]) => [v, m.label]),
];

// 三页签台账(P5-07①):意图/事实/漏洞;行内无状态无过程色(执行状态
// 在虫群会话页看,两边不重复)。
const FINDING_TABS = [
  ["intents", "意图"],
  ["facts", "事实"],
  ["vulns", "漏洞"],
];

// 漏洞页收录的显示键(confirmed/fixed:坐实漏洞,已修复带标记;
// insufficient:证据不足灰区沉底——仅此一个非成功态;被推翻/已废弃
// 不展示,账本留痕)。
const VULN_DISPLAYS = new Set(["confirmed", "fixed", "insufficient"]);

// 漏洞页等级筛选 chips(P5-07⑥:与事实页 5 类筛选同款形态的单选行;
// 证据不足可单独筛出)。
const VULN_FILTERS = [
  ["all", "全部"],
  ["critical", "Critical"],
  ["high", "High"],
  ["medium", "Medium"],
  ["low", "Low"],
  ["insufficient", "证据不足"],
];

// 搜索框(任务列表/观测页同款形态:图标 + Input,P5-07⑨ 零新样式)。
function SearchBox({ placeholder, value, onChange }) {
  return <SearchInput placeholder={placeholder} value={value} onChange={(e)=>onChange(e.target.value)} />;
}

function AssetContext({ assets }) {
 return <div className="flex min-w-0 items-center gap-3 text-xs text-muted-foreground" aria-label="地址">{assets?.length ? assets.map(asset => <span key={asset.id} className="truncate" title={asset.title || asset.id}>{asset.title || asset.id}</span>) : <span>未记录地址</span>}</div>;
}

export function FindingsPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const [tab, setTab] = React.useState(focus ? "facts" : "vulns");

  // 图上事实节点跳回页签定位(?focus=<事实ID>):落在事实页签并高亮。
  const lastFocus = React.useRef(null);
  React.useEffect(() => {
    if (focus && lastFocus.current !== focus) {
      lastFocus.current = focus;
      setTab("facts");
    }
  }, [focus]);

  // 两层未读提醒(P5-08,服务端键值表存,跨浏览器一致):① 页面级数字
  // ——三页签各自独立已读线(上次进页时刻服务端盖戳),口径=已读线后
  // 新落页条目数(意图新提出/漏洞新确认/事实新确认),进到该子页即消;
  // 正停留的子页签新增红点亮起约 3 秒后自动盖戳消点(P4-47 修订语义
  // 沿用,详见下方停留盖戳)。② 条目级淡红点——该条不在服务端已看
  // 集合即亮,点开该条详情才消;数字消而条目点仍亮是正常两层语义
  //(数字管"知道有新的",条目点管"还没细看"),不要求同步。
  const { data: unreadBody } = usePolling(
    tid ? `/api/tasks/${tid}/unread` : "/api/healthz",
    2000,
    { immediate: Boolean(tid), live: tid ? [`claims:${tid}`, "tasks"] : null },
  );
  // 盖戳/记已看响应的瞬时覆盖:轮询周期 2 秒,覆盖 3 秒后交还轮询值(期间
  // 任何轮询请求都发起于盖戳之后,值已一致,无新旧错判)。
  const [seenOverride, setSeenOverride] = React.useState(null);
  // 条目级乐观已看:点开详情先本地灭点,服务端回执后由覆盖值接棒。
  const [localSeen, setLocalSeen] = React.useState({ intents: [], facts: [], claims: [] });
  React.useEffect(() => {
    setSeenOverride(null);
    setLocalSeen({ intents: [], facts: [], claims: [] });
  }, [tid]);

  // 进子页签即盖"看过"戳(时刻为服务端时间),响应即刻消数字(条目点
  // 不动)。
  React.useEffect(() => {
    if (!tid) return;
    const kind = TAB_KIND[tab];
    if (!kind) return;
    let alive = true;
    postJSON(`/api/tasks/${tid}/seen`, { kind }).then(({ ok, body }) => {
      if (ok && alive) {
        setSeenOverride(body);
      }
    });
    return () => {
      alive = false;
    };
  }, [tid, tab]);
  React.useEffect(() => {
    if (!seenOverride) return;
    const t = setTimeout(() => setSeenOverride(null), 3000);
    return () => clearTimeout(t);
  }, [seenOverride]);

  const unread = React.useMemo(() => {
    const src = seenOverride ?? unreadBody ?? {};
    const seenOf = (kind) => new Set([...(src.seen?.[kind] || []), ...(localSeen[kind] || [])]);
    return {
      counts: {
        intents: src.intents ?? 0,
        facts: src.facts ?? 0,
        claims: src.claims ?? 0,
      },
      seen: {
        intents: seenOf("intents"),
        facts: seenOf("facts"),
        claims: seenOf("claims"),
      },
    };
  }, [seenOverride, unreadBody, localSeen]);

  // 页面可见性(停留即已读的判据之一):浏览器标签页在前台才自动盖戳,
  // 后台期间到达的未读保留,回前台停留满 3 秒才视为看过。
  const [visible, setVisible] = React.useState(true);
  React.useEffect(() => {
    const onVis = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", onVis);
    onVis();
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  // 停留即已读(P4-47 修订沿用):停留在某子页签且页面可见、该分线有
  // 未读时,亮起约 3 秒(给注意的时间)后自动盖戳消点——列表本身实时
  // 刷新,正看着即已看。任一条件变化(跳走子页签/转后台/计数再变)
  // 作废计时重新计:3 秒内跳走则不盖,红点保留,回来点一下才消。
  const dwellKind = TAB_KIND[tab] || null;
  const dwellUnread = dwellKind ? unread.counts[dwellKind] : 0;
  React.useEffect(() => {
    if (!tid || !dwellKind || !visible || dwellUnread <= 0) return;
    let alive = true;
    const t = setTimeout(() => {
      if (!alive || document.visibilityState !== "visible") return;
      postJSON(`/api/tasks/${tid}/seen`, { kind: dwellKind }).then(({ ok, body }) => {
        if (ok && alive) {
          setSeenOverride(body);
        }
      });
    }, 3000);
    return () => {
      alive = false;
      clearTimeout(t);
    };
  }, [tid, dwellKind, visible, dwellUnread]);

  // 条目记已看(条目级):点开详情先本地灭点,再落服务端已看集合
  //(跨浏览器一致);已看集合只增不减,kind 按详情对象类型对表。
  const markItemSeen = (kind, id) => {
    if (!tid || !kind || !id) return;
    setLocalSeen((prev) =>
      prev[kind]?.includes(id)
        ? prev
        : { ...prev, [kind]: [...(prev[kind] || []), id] },
    );
    postJSON(`/api/tasks/${tid}/seen`, { kind, item: id }).then(({ ok, body }) => {
      if (ok) {
        setSeenOverride(body);
      }
    });
  };

  // 页签计数徽标:数字与各页签条目同口径,零计数不占位。
  const { data: claimsData } = usePolling(tid ? `/api/tasks/${tid}/claims` : "/api/healthz", 4000, {
    immediate: Boolean(tid),
    live: tid ? [`claims:${tid}`] : null,
  });
  const { data: factsData } = usePolling(tid ? `/api/tasks/${tid}/facts` : "/api/healthz", 6000, {
    immediate: Boolean(tid),
    live: tid ? ["tasks"] : null,
  });
  const { data: intentsData } = usePolling(tid ? `/api/tasks/${tid}/intents` : "/api/healthz", 6000, {
    immediate: Boolean(tid),
    live: tid ? ["tasks"] : null,
  });
  const counts = React.useMemo(() => {
    const claims = Array.isArray(claimsData) ? claimsData : [];
    const facts = Array.isArray(factsData?.facts) ? factsData.facts : [];
    const intents = Array.isArray(intentsData) ? intentsData : [];
    return {
      intents: intents.length,
      facts: facts.length,
      vulns: claims.filter((c) => VULN_DISPLAYS.has(c.display || "")).length,
    };
  }, [claimsData, factsData, intentsData]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-3">
        <PageHeader title="发现" subtitle="意图 · 事实 · 漏洞" />
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
    <div className="flex flex-col gap-3">
      <PageHeader
        title="发现"
        subtitle={
          tab === "intents"
            ? "主宰提出的每条意图，行尾可看产出"
            : tab === "facts"
              ? "虫群确认的结论 · 凭据 / 数据 / 权限 / 立足点 / 普通观察 / 未分类"
              : "坐实漏洞，新确认的在上 · 证据不足沉底如实标注"
        }
      />
      <div className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-lg bg-muted p-1">
        {FINDING_TABS.map(([v, label]) => (
          <button
            key={v}
            className={
              "flex items-center rounded-md px-3 py-1 text-[13px] transition-colors cursor-pointer " +
              (tab === v
                ? "bg-popover text-foreground shadow-xs"
                : "text-muted-foreground hover:bg-hover-lift hover:text-foreground")
            }
            onClick={() => setTab(v)}
          >
            {label}
            {/* 页签计数徽标:条目数,零不占位 */}
            {counts[v] > 0 && (
              <span
                className={
                  "ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] font-semibold leading-none num " +
                  "bg-muted text-muted-foreground"
                }
              >
                {counts[v]}
              </span>
            )}
            {/* 未读数字红点(P5-08① 页面级):上次进该子页后新落页条目数,
                进页即消(条目淡红点不受影响);三页签各自独立 */}
            {unread.counts[TAB_KIND[v]] > 0 && (
              <span
                className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-none text-primary-foreground num"
                title={
                  v === "intents"
                    ? `新意图 ${unread.counts.intents} 条`
                    : v === "facts"
                      ? `新确认事实 ${unread.counts.facts} 条`
                      : `新确认漏洞 ${unread.counts.claims} 条`
                }
              >
                {unread.counts[TAB_KIND[v]]}
              </span>
            )}
          </button>
        ))}
      </div>
      {tab === "intents" ? (
        <IntentsSection
          tid={tid}
          seenSet={unread.seen.intents}
          markSeen={markItemSeen}
        />
      ) : tab === "facts" ? (
        <FactsSection
          tid={tid}
          focus={focus}
          seenSet={unread.seen.facts}
          markSeen={markItemSeen}
        />
      ) : (
        <VulnsSection
          tid={tid}
          seenSet={unread.seen.claims}
          markSeen={markItemSeen}
        />
      )}
    </div>
  );
}

// 未读条目淡红点(P5-08② 条目级):行前小圆点,不在已看集合即亮,点开
// 该条详情才消;常驻占位(已看时透明),灭点不引起行内跳动。三页统一。
function UnreadDot({ on }) {
  return (
    <span
      className={
        "size-2 shrink-0 rounded-full " + (on ? "bg-unread-dot" : "bg-transparent")
      }
    />
  );
}

// openSheetOf 点开详情的未读记账包装(P5-08②):开抽屉同时按详情对象
// 类型记条目已看(意图页行尾"→ 漏洞/→ 事实"点开的声明/事实详情同样
// 记对应页签的已看;非发现页条目类型不记)。
function openSheetOf(setSheet, markSeen, v) {
  setSheet(v);
  const kind = v && SHEET_KIND[v.type];
  if (kind) {
    markSeen(kind, v.id);
  }
}

// 台账行卡片(三页同款形态):行即卡片,零状态零过程色;hover 提亮。
function LedgerRow({ className, children, ...rest }) {
  return (
    <div
      className={cn(
        "flex flex-col gap-2 rounded-xl border border-border/55 bg-card px-4 py-3 text-left transition-shadow hover:shadow-card cursor-pointer [&>div:first-child]:min-h-8",
        className,
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

// 台账时间列(等宽数字,精确呈现)。
function LedgerTime({ children, title }) {
  return (
    <span className="hidden shrink-0 num text-xs text-muted-foreground sm:inline" title={title}>
      {children}
    </span>
  );
}

// 追问按钮(P5-07② 单一入口:事实/漏洞每条一个;问与吩咐同窗,悬停
// 提示锚例逐字)。按钮形态与全站行内操作按钮同款。
function FollowupButton({ onClick }) {
  return (
    <Button
      variant="outline"
      size="sm"
      className="h-8 px-2 text-xs"
      title={FOLLOWUP_HINT}
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
    >
      {React.createElement(iconOf("messageCircle"), { className: "size-3.5" })}
      追问
    </Button>
  );
}

// IntentsSection 意图页签(P5-07①):主宰提出的每一个意图(提出即落页,
// 节点投影直查)。行 = 编号 + 意图摘要 + 行尾下文标记 + 时间;下文
// 标记为静态台账栏:→ 漏洞 / → 事实(次级色小字,点击弹出该条详情
// 抽屉不跳页),无产出"—"淡灰不可点。无状态、无过程色;意图页不加
// 追问入口(执行中改方向走会话页插话,跑完的看行尾下文)。
function IntentsSection({ tid, seenSet, markSeen }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/intents`, 5000, {
    live: ["tasks"],
  });
  const [textFilter, setTextFilter] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [sheet, setSheet] = React.useState(null);

  const rows = React.useMemo(() => {
    const list = [...(Array.isArray(data) ? data : [])];
    list.sort(byIntentTimeDesc);
    const q = textFilter.trim().toLowerCase();
    return q ? list.filter((r) => `${r.title || ""}${r.id}${(r.assets || []).map(a=>a.title || a.id).join(" ")}`.toLowerCase().includes(q)) : list;
  }, [data, textFilter]);

  // 筛选后页码回落(命中集变小越界时钳回)。
  const viewed = ledgerPage(rows, page);
  React.useEffect(() => {
    if (page !== viewed.page) setPage(viewed.page);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewed.page]);

  return (
    <div className="flex flex-col gap-3">
      {(Array.isArray(data) ? data.length : 0) === 0 ? (
        <EmptyState
          icon="arrowRight"
          title="暂无意图"
          hint={error ? `读取失败:${error}` : "主宰提出的每条意图在此落页，行尾可看产出"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <SearchBox
              placeholder="按意图摘要搜索"
              value={textFilter}
              onChange={setTextFilter}
            />
            <Label className="ml-auto text-xs text-muted-foreground">{rows.length} 条</Label>
          </div>
          <div className="flex flex-col gap-4">
            {viewed.rows.map((r) => (
              <LedgerRow
                key={r.id}
                onClick={() => openSheetOf(setSheet, markSeen, { type: "step", id: r.id })}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <UnreadDot on={!seenSet.has(r.id)} />
                  <span className="num text-xs text-muted-foreground">{r.id}</span>
                  <span className="min-w-0 flex-1 truncate text-sm font-medium" title={r.title}>
                    {r.title || "(无摘要)"}
                  </span>
                  <span className="ml-auto flex shrink-0 items-center gap-2">
                    {(r.outcomes || []).length === 0 ? (
                      <span className="text-xs text-muted-foreground/60">—</span>
                    ) : (
                      <>
                        {(r.outcomes || []).slice(0, 5).map((o) => (
                          <button
                            key={o.id}
                            className="hidden text-xs text-muted-foreground transition-colors cursor-pointer hover:text-primary md:inline"
                            title={`查看${o.word} ${o.id}`}
                            onClick={(e) => {
                              e.stopPropagation();
                              openSheetOf(setSheet, markSeen, { type: o.kind, id: o.id });
                            }}
                          >
                            → {o.word}
                          </button>
                        ))}
                        {(r.outcomes || []).length > 5 && (
                          <span
                            className="num text-xs text-muted-foreground/60"
                            title={`共 ${(r.outcomes || []).length} 个产出,其余经事实/漏洞页查看`}
                          >
                            +{(r.outcomes || []).length - 5}
                          </span>
                        )}
                      </>
                    )}
                    <LedgerTime title={`提出 ${r.added_at || ""}`}>{r.added_at?.slice(5) || "—"}</LedgerTime>
                  </span>
                </div>
                <AssetContext assets={r.assets}/>
              </LedgerRow>
            ))}
            {viewed.rows.length === 0 && (
              <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
                无匹配意图
              </div>
            )}
          </div>
          <PageBar page={viewed.page} pages={viewed.pages} total={rows.length} onPage={setPage} />
        </>
      )}
      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
    </div>
  );
}

// FactsSection 事实页签(P5-07①):虫群确认的真结论。行 = 5 类徽标 +
// 摘要 + 时间(候选/被推翻不展示,账本留痕);搜索匹配事实摘要;每条
// 一个追问按钮;点击行开事实抽屉(抽屉内可跳图/看血缘)。图上事实节点
// ?focus= 跳回本页签定位。
function FactsSection({ tid, focus, seenSet, markSeen }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/facts`, 3000, {
    live: ["tasks"],
  });
  const [textFilter, setTextFilter] = React.useState("");
  const [lootFilter, setLootFilter] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [sheet, setSheet] = React.useState(null);
  const [followup, setFollowup] = React.useState(null);
  const [focusPulse, setFocusPulse] = React.useState(null);
  const lastFocused = React.useRef(null);

  const rows = React.useMemo(() => {
    const list = [...(data?.facts || [])];
    list.sort(byLandedTimeDesc);
    return list;
  }, [data]);

  const filtered = rows.filter((f) => {
    if (lootFilter !== "all") {
      const want = lootFilter;
      if (!(f.result_types || ["unclassified"]).includes(want)) return false;
    }
    const q = textFilter.trim().toLowerCase();
    if (q && !`${f.title || ""}${f.statement || ""}${f.id}${(f.assets || []).map(a=>a.title || a.id).join(" ")}`.toLowerCase().includes(q)) return false;
    return true;
  });

  const viewed = ledgerPage(filtered, page);
  React.useEffect(() => {
    if (page !== viewed.page) setPage(viewed.page);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewed.page]);

  // focus 定位(图上事实节点跳回):滚动入视 + 高亮 2.4 秒。
  React.useEffect(() => {
    if (!focus) {
      lastFocused.current = null;
      return;
    }
    if (lastFocused.current === focus || !rows.length) return;
    const index = rows.findIndex(row => row.id === focus);
    if (index < 0) return;
    lastFocused.current = focus;
    setTextFilter("");
    setLootFilter("all");
    setPage(Math.floor(index / LEDGER_PAGE_SIZE) + 1);
    setFocusPulse(focus);
  }, [focus, rows.length]);

  React.useEffect(() => {
    if (!focusPulse) return;
    const timer = setTimeout(() => setFocusPulse(null), 2400);
    return () => clearTimeout(timer);
  }, [focusPulse]);

  React.useEffect(() => {
    if (!focusPulse) return;
    document
      .querySelector(`[data-fact-row="${CSS.escape(focusPulse)}"]`)
      ?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focusPulse, viewed.page, rows.length]);

  return (
    <div className="flex flex-col gap-3">
      {(data?.facts || []).length === 0 ? (
        <EmptyState
          icon="check"
          title="暂无事实"
          hint={error ? `读取失败:${error}` : "虫群确认的真结论在此列出"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <SearchBox
              placeholder="按事实摘要搜索"
              value={textFilter}
              onChange={setTextFilter}
            />
            <div className="w-32">
              <Select value={lootFilter} onValueChange={setLootFilter}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LOOT_FILTERS.map(([v, label]) => (
                    <SelectItem key={v} value={v} title={v === "all" ? undefined : FACT_RESULT_META[v].hint}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Label className="ml-auto text-xs text-muted-foreground">
              {filtered.length}/{rows.length}
            </Label>
          </div>
          <div className="flex flex-col gap-4">
            {viewed.rows.map((f) => {
              const resultTypes = f.result_types?.length ? f.result_types : ["unclassified"];
              return (
                <LedgerRow
                  key={f.id}
                  data-fact-row={f.id}
                  className={cn(focusPulse === f.id && "border-primary ring-2 ring-primary")}
                  onClick={() => openSheetOf(setSheet, markSeen, { type: "fact", id: f.id })}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <UnreadDot on={!seenSet.has(f.id)} />
                    {resultTypes.map(kind => <StatusBadge key={kind} meta={FACT_RESULT_META[kind] || FACT_RESULT_META.unclassified}/>)}
                    <span className="min-w-0 flex-1 truncate text-sm font-medium" title={f.statement}>
                      {f.title || f.statement}
                    </span>
                    <span className="ml-auto flex shrink-0 items-center gap-2">
                      <FollowupButton onClick={() => setFollowup({ kind: "fact", id: f.id })} />
                      <LedgerTime title={`确认 ${f.confirmed_at || ""}`}>
                        {f.confirmed_at?.slice(5) || "—"}
                      </LedgerTime>
                    </span>
                  </div>
                <AssetContext assets={f.assets}/>
                </LedgerRow>
              );
            })}
            {viewed.rows.length === 0 && (
              <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
                无匹配事实
              </div>
            )}
          </div>
          <PageBar page={viewed.page} pages={viewed.pages} total={filtered.length} onPage={setPage} />
        </>
      )}

      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} />
      {followup && (
        <ClaimFollowupDialog
          tid={tid}
          anchor={followup}
          open
          onClose={() => setFollowup(null)}
        />
      )}
    </div>
  );
}

// VulnsSection 漏洞页签(P5-07①⑥):坐实漏洞。行 = 严重级徽标 + 标题 +
// 时间 + 已修复标记;证据不足灰区沉底(灰色条目 + "证据不足"标注,
// 追问入口与正常条目同款);等级筛选 chips 单选;总数注明含证据不足
// 条数;复选 + 导出走格式 × 范围矩阵弹窗(P2-13 既有)。
function VulnsSection({ tid, seenSet, markSeen }) {
  const { data, error } = usePolling(`/api/tasks/${tid}/claims`, 2000, {
    live: [`claims:${tid}`],
  });
  const [textFilter, setTextFilter] = React.useState("");
  const [sevFilter, setSevFilter] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [sheet, setSheet] = React.useState(null);
  const [followup, setFollowup] = React.useState(null);
  const [selected, setSelected] = React.useState(() => new Set());
  const [exportOpen, setExportOpen] = React.useState(false);

  const claims = (Array.isArray(data) ? data : []).filter((c) =>
    VULN_DISPLAYS.has(c.display || ""),
  );

  const rows = React.useMemo(() => {
    const list = claims.filter((c) => {
      if (sevFilter === "insufficient") return c.display === "insufficient";
      if (sevFilter !== "all" && (c.severity || "") !== sevFilter) return false;
      return true;
    });
    const q = textFilter.trim().toLowerCase();
    const hit = q
      ? list.filter((c) => `${c.statement}${c.vulnclass || ""}${c.id}`.toLowerCase().includes(q))
      : list;
    hit.sort(byVulnLedgerDesc);
    return hit;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, sevFilter, textFilter]);

  const viewed = ledgerPage(rows, page);
  React.useEffect(() => {
    if (page !== viewed.page) setPage(viewed.page);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewed.page]);

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
        ? rows.map((c) => c.id)
        : null;
  const scopes = [
    ["all", "全部发现"],
    ["filtered", "当前筛选"],
  ];
  if (selected.size > 0) {
    scopes.push(["selected", `勾选项(${selected.size})`]);
  }

  return (
    <div className="flex flex-col gap-3">
      {claims.length === 0 ? (
        <EmptyState
          icon="shieldAlert"
          title="暂无漏洞"
          hint={error ? `读取失败:${error}` : "坐实漏洞在此列出；证据不足沉底如实标注"}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <SearchBox
              placeholder="按漏洞名搜索"
              value={textFilter}
              onChange={setTextFilter}
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => setExportOpen(true)}
            >
              {React.createElement(iconOf("download"), { className: "size-4" })}
              导出
            </Button>
            {/* 等级筛选 chips(P5-07⑥:单选行,同事实页 5 类筛选形态;选中
                态与翻页器当前页同款高亮) */}
            <div className="flex flex-wrap items-center gap-2 text-xs">
              {VULN_FILTERS.map(([v, label]) => (
                <button
                  key={v}
                  className={
                    "rounded-full border border-border/55 bg-card px-2 py-0.5 transition-colors cursor-pointer hover:bg-hover-lift " +
                    (sevFilter === v ? "bg-primary/15 text-primary" : "text-muted-foreground")
                  }
                  onClick={() => setSevFilter(v)}
                >
                  {label}
                </button>
              ))}
            </div>
            <Label className="ml-auto text-xs text-muted-foreground">
              {rows.length}/{claims.length}
            </Label>
          </div>

          <div className="flex flex-col gap-3">
            {viewed.rows.map((c) => (
              <VulnRow
                key={c.id}
                claim={c}
                checked={selected.has(c.id)}
                fresh={!seenSet.has(c.id)}
                onToggle={() => toggleSel(c.id)}
                onOpen={() => openSheetOf(setSheet, markSeen, { type: "testclaim", id: c.id })}
                onFollowup={() => setFollowup({ kind: "claim", id: c.id })}
              />
            ))}
            {viewed.rows.length === 0 && (
              <div className="rounded-lg border border-dashed py-6 text-center text-sm text-muted-foreground">
                无匹配漏洞
              </div>
            )}
          </div>

          {/* 总数注明含证据不足条数(P5-07④,按当前命中集计数);页码
              跳转与流量页同款 */}
          <div className="flex flex-wrap items-center gap-3">
            <PageBar page={viewed.page} pages={viewed.pages} total={rows.length} onPage={setPage} />
            <span className="text-xs text-muted-foreground num">
              含证据不足 {rows.filter((c) => c.display === "insufficient").length} 条
            </span>
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
      {followup && (
        <ClaimFollowupDialog
          tid={tid}
          anchor={followup}
          open
          onClose={() => setFollowup(null)}
        />
      )}
    </div>
  );
}

// The list shows name and bound addresses; full evidence remains in the formal detail.
function VulnRow({ claim, checked, fresh, onToggle, onOpen, onFollowup }) {
  const gray = claim.display === "insufficient";
  const fixed = claim.display === "fixed";
  const sev = severityMeta(claim.severity);
  const retestVD = claim.retest_verdict ? RETEST_VERDICT[claim.retest_verdict] : null;
  const landedAt = claim.confirmed_at || claim.added_at || "";
  return (
    <LedgerRow
      className={gray && "bg-muted/40"}
      onClick={onOpen}
    >
      <div className="flex items-center gap-2">
        <UnreadDot on={fresh} />
        <span onClick={(e) => e.stopPropagation()}>
          <Checkbox checked={checked} onCheckedChange={onToggle} />
        </span>
        {gray ? (
          <StatusBadge meta={CLAIM_DISPLAY.insufficient} />
        ) : (
          sev && <Badge variant={sev.variant}>{sev.label}</Badge>
        )}
        {fixed && <Badge variant={CLAIM_DISPLAY.fixed.variant}>{CLAIM_DISPLAY.fixed.label}</Badge>}
        {retestVD && (
          <StatusBadge meta={retestVD}>
            复测{retestVD.label}
          </StatusBadge>
        )}
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={claim.title || claim.statement}>{claim.title || claim.statement}</span>
        {/* 判断原文为主视觉,tc-ID 不上正文,悬停与复制回执可取 */}
        <CopyIDButton id={claim.id} noun="判断标识" className="size-5" />
        <span className="ml-auto flex shrink-0 items-center gap-2">
          <FollowupButton onClick={onFollowup} />
          <LedgerTime title={gray ? `落页 ${landedAt}` : `确认 ${landedAt}`}>
            {landedAt.slice(5) || "—"}
          </LedgerTime>
        </span>
      </div>
      <AssetContext assets={claim.addresses?.map(address => ({ id: address, title: address }))}/>
    </LedgerRow>
  );
}
