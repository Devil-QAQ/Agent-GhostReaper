"use client";

import * as React from "react";
import "@xyflow/react/dist/style.css";
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
} from "@xyflow/react";
import cytoscape from "cytoscape";
import htmlLabel from "cytoscape-node-html-label";
import navigatorExt from "cytoscape-navigator";

import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { badgeVariants } from "@/components/ui/badge";
import { layoutGraph, routeEdge, grCardRects, EDGE_TYPES } from "@/views/progress-page";
import { NodeSheetHost } from "@/views/node-sheets";
import {
  DEAD_STATUSES,
  EDGE_LABELS,
  assetClassMeta,
  nodeStatusMeta,
  nodeTypeMeta,
  severityMeta,
  usePolling,
} from "@/lib/api";
import { cn } from "@/lib/utils";

htmlLabel(cytoscape);
navigatorExt(cytoscape);

// 年轮图页(任务空间·年轮图标签,P2-11;8.7 年轮图视图——账本全图投影):
// 统一图投影(GET /api/tasks/{id}/graph)之上的全图总览,四透镜页内切换
//——发散全景(Cytoscape 力发散,默认)/ 血缘(ReactFlow 从左向右 DAG,
// 复用进度图分层布局)/ 攻击链(组合声明的环节链)/ 资产网(资产互联)。
// 卡片式节点(类型色头部 + 状态/级别徽章 + 内容摘要),语义色贝塞尔边
// 带线上关系标签;六类节点过滤开关 + 边类型图例(可点隐藏)+ 缩放 +
// 小地图;节点点击走全站跳转协议(抽屉 + ?focus= 聚焦)。声明级别与
// 组合链读发现列表(账本审计流,同源零新增采集)。

// ---- 8.4 token 的画布取值(P4-26 品牌换装:Cytoscape 画布渲染不吃 CSS
// 变量,渲染时读当前主题 token——暗/亮自动生效;SSR 与取值失败的回退
// = 暗色 token 值,与 globals.css 同源) ----

const T_DARK = {
  background: "#060607",
  card: "#0d0d0f",
  foreground: "#e7e5e4",
  mutedFg: "#9b9a94",
  primary: "#8fae94",
  critical: "#a92e2e",
  high: "#e5484d",
  medium: "#d97f3e",
  info: "#7e7d78",
  success: "#8fae94",
  warning: "#c9a96e",
  danger: "#e5484d",
  running: "#8fae94",
  paused: "#7e7d78",
};

function graphPalette() {
  if (typeof window === "undefined") return T_DARK;
  const cs = getComputedStyle(document.body);
  const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
  return {
    background: v("--background", T_DARK.background),
    card: v("--card", T_DARK.card),
    foreground: v("--foreground", T_DARK.foreground),
    mutedFg: v("--muted-foreground", T_DARK.mutedFg),
    primary: v("--primary", T_DARK.primary),
    critical: v("--severity-critical", T_DARK.critical),
    high: v("--severity-high", T_DARK.high),
    medium: v("--severity-medium", T_DARK.medium),
    info: v("--severity-info", T_DARK.info),
    success: v("--status-success", T_DARK.success),
    warning: v("--status-warning", T_DARK.warning),
    danger: v("--status-danger", T_DARK.danger),
    running: v("--status-running", T_DARK.running),
    paused: v("--status-paused", T_DARK.paused),
  };
}

// 卡片色调(类型色头部/小地图节点色)→ 当前主题语义色。
function toneHex(tone) {
  const T = graphPalette();
  return (
    {
      running: T.running,
      high: T.high,
      success: T.success,
      critical: T.critical,
      warning: T.warning,
      info: T.info,
    }[tone] || T.info
  );
}

// 年轮图派生边的操作者语言(经由 X = 资产网透镜按共同锚点归并的
// 资产互联;chain = 攻击链透镜的环节序边);账本边走 EDGE_LABELS。
const ANNULUS_EDGE_LABELS = {
  via_fact: "经由事实",
  via_claim: "经由判断",
  via_hyp: "经由假设",
  chain: "环节",
};

// 边类型 → 语义色 / 虚线(goal 与 falsify 是投影层派生边,虚线与账本
// 边区分;verifies 虚线与 produces 同色区分)。色值随当前主题。
function edgeMeta(type) {
  const T = graphPalette();
  return (
    {
      goal: { color: T.running, dashed: true },
      falsify: { color: T.danger, dashed: true },
      produces: { color: T.success },
      derived_from: { color: T.high },
      about: { color: T.info },
      covers: { color: T.medium },
      depends_on: { color: T.running },
      verifies: { color: T.success, dashed: true },
      sub_of: { color: T.mutedFg, dashed: true },
      via_fact: { color: T.success },
      via_claim: { color: T.critical },
      via_hyp: { color: T.warning },
      chain: { color: T.primary },
    }[type] || { color: T.mutedFg }
  );
}

function edgeLabel(type) {
  return EDGE_LABELS[type] || ANNULUS_EDGE_LABELS[type] || type;
}

// ---- 卡片式节点(8.7:类型色头部 + 状态/级别徽章 + 内容摘要一行)----
// ReactFlow(血缘/攻击链)与 Cytoscape(发散/资产网,HTML 标签)两个
// 渲染器共用同一份卡片规格与类名,视觉一份定义。

const CARD_W = 184;

const SWATCH_TONE = {
  running: "bg-status-running/25 text-status-running",
  high: "bg-severity-high/25 text-severity-high",
  success: "bg-status-success/25 text-status-success",
  critical: "bg-severity-critical/25 text-severity-critical",
  warning: "bg-status-warning/25 text-status-warning",
  info: "bg-severity-info/25 text-severity-info",
};

const HEADER_TONE = {
  running: "border-status-running/30 bg-status-running/10 text-status-running",
  high: "border-severity-high/30 bg-severity-high/10 text-severity-high",
  success: "border-status-success/30 bg-status-success/10 text-status-success",
  critical: "border-severity-critical/30 bg-severity-critical/10 text-severity-critical",
  warning: "border-status-warning/30 bg-status-warning/10 text-status-warning",
  info: "border-severity-info/30 bg-severity-info/10 text-severity-info",
};

// cardSpec 卡片规格:六类节点统一产出(claims 提供声明的级别/组合标记,
// 资产带分类/覆盖/计数徽章;focused 为跳转协议聚焦描边)。
function cardSpec(n, claimOf, focusedID, role) {
  const meta = nodeTypeMeta(n.type);
  const st = nodeStatusMeta(n.type, n.status);
  const extras = [];
  if (n.type === "testclaim") {
    const c = claimOf?.(n.id);
    const sev = severityMeta(c?.severity);
    if (sev) extras.push({ label: sev.label, tone: sev.variant });
    if (c?.combined) extras.push({ label: "攻击链", tone: "default" });
  } else if (n.type === "asset") {
    extras.push({ label: assetClassMeta(n.asset_class).label, tone: "outline" });
    if (n.excluded) {
      // 范围外(P4-05):已从授权范围摘除,替代覆盖徽标呈现。
      extras.push({ label: "范围外", tone: "paused" });
    } else {
      extras.push({ label: n.covered ? "已覆盖" : "未测", tone: n.covered ? "success" : "paused" });
    }
    if (n.source === "manual") extras.push({ label: "手动", tone: "default" });
    if (n.fact_count) extras.push({ label: `事实 ${n.fact_count}`, tone: "outline" });
  }
  if (role) extras.push({ label: role, tone: "default" });
  return {
    id: n.id,
    type: n.type,
    title: n.title || n.id,
    dead: DEAD_STATUSES.has(n.status),
    focused: focusedID === n.id,
    typeName: meta.label,
    tone: meta.tone,
    shape: meta.shape,
    // 状态徽标(P4-28):hint 悬停人话、live 过程态呼吸点,随全站词表。
    status: st ? { label: st.label, variant: st.variant, hint: st.hint, live: st.live } : null,
    extras,
  };
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

function pillHTML(label, tone) {
  return `<span class="inline-flex items-center rounded-full border px-2 py-px text-[10px] leading-4 whitespace-nowrap ${badgeVariants({ variant: tone || "outline" })}">${esc(label)}</span>`;
}

// cardHTML Cytoscale HTML 标签渲染的卡片(与 AnnulusCard 同markup,
// 类名经 Tailwind 源码扫描生效;文本一律转义)。
function cardHTML(d) {
  const pills = (d.extras || []).map((e) => pillHTML(e.label, e.tone)).join("");
  return (
    `<div class="gr-cy-card box-border overflow-hidden rounded-xl border border-border/55 bg-card shadow-xs ${d.dead ? "opacity-40 saturate-50" : ""} ${d.focused ? "border-primary ring-2 ring-primary" : "border-border"}" style="width:${CARD_W}px">` +
    `<div class="flex items-center gap-2 border-b px-2 py-1 ${HEADER_TONE[d.tone] || HEADER_TONE.info}">` +
    `<span class="size-2.5 shrink-0 border border-current ${d.shape} ${SWATCH_TONE[d.tone] || SWATCH_TONE.info}"></span>` +
    `<span class="text-[10px] font-medium tracking-wide">${esc(d.typeName)}</span>` +
    (d.status ? `<span class="ml-auto inline-flex items-center rounded-full border px-2 py-px text-[10px] leading-4 whitespace-nowrap${d.status.live ? " gr-live" : ""} ${badgeVariants({ variant: d.status.variant })}"${d.status.hint ? ` title="${esc(d.status.hint)}"` : ""}>${esc(d.status.label)}</span>` : "") +
    `</div>` +
    `<div class="flex flex-col gap-1 px-2 py-2">` +
    `<div class="truncate text-xs leading-snug text-foreground">${esc(d.title)}</div>` +
    (pills ? `<div class="flex flex-wrap gap-1">${pills}</div>` : "") +
    `<div class="num text-[10px] leading-3 text-muted-foreground">${esc(d.id)}</div>` +
    `</div></div>`
  );
}

// AnnulusCard ReactFlow 卡片节点(血缘/攻击链透镜)。
function AnnulusCard({ data }) {
  const s = data.spec;
  return (
    <div
      className={cn(
        "gr-cy-card flex flex-col overflow-hidden rounded-xl border border-border/55 bg-card shadow-xs",
        s.dead && "opacity-40 saturate-50",
        s.focused ? "border-primary ring-2 ring-primary" : "border-border",
      )}
      style={{ width: CARD_W }}
      title={`${s.typeName} ${s.id}`}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} className="!invisible" />
      <Handle type="source" position={Position.Right} isConnectable={false} className="!invisible" />
      <div className={cn("flex items-center gap-2 border-b px-2 py-1", HEADER_TONE[s.tone] || HEADER_TONE.info)}>
        <span className={cn("size-2.5 shrink-0 border border-current", s.shape, SWATCH_TONE[s.tone] || SWATCH_TONE.info)} />
        <span className="text-[10px] font-medium tracking-wide">{s.typeName}</span>
        {s.status && (
          <span
            title={s.status.hint}
            className={cn(
              "ml-auto inline-flex items-center rounded-full border px-2 py-px text-[10px] leading-4 whitespace-nowrap",
              s.status.live && "gr-live",
              badgeVariants({ variant: s.status.variant }),
            )}
          >
            {s.status.label}
          </span>
        )}
      </div>
      <div className="flex flex-col gap-1 px-2 py-2">
        <div className="truncate text-xs leading-snug">{s.title}</div>
        {s.extras.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {s.extras.map((e, i) => (
              <span key={i} className={cn("inline-flex items-center rounded-full border px-2 py-px text-[10px] leading-4 whitespace-nowrap", badgeVariants({ variant: e.tone }))}>
                {e.label}
              </span>
            ))}
          </div>
        )}
        <div className="num text-[10px] leading-3 text-muted-foreground">{s.id}</div>
      </div>
    </div>
  );
}

const RF_NODE_TYPES = { annulusCard: AnnulusCard };

// ---- Cytoscape 透镜(发散全景 / 资产网)----

// 大图降采样阈值(P4-58):HTML 卡片标签逐节点成 DOM,缩放/平移每帧
// 全量重排——数千节点时交互冻结(实测 3340 节点约 4fps)。超过阈值改
// 画布原生节点(类型色点 + 标题文字,纹理快照下交互零重排),点击
// 详情/过滤/聚焦语义不变;阈值内与原 HTML 卡片完全一致。
const CY_RICH_LABEL_MAX = 800;

function cyStyle(mass) {
  const T = graphPalette();
  if (mass) {
    return [
      {
        selector: "node",
        style: {
          width: 20,
          height: 20,
          shape: "round-rectangle",
          "background-color": "data(dotColor)",
          "border-width": 1,
          "border-color": T.background,
          label: "data(title)",
          color: T.foreground,
          "font-size": 9,
          "text-background-color": T.background,
          "text-background-opacity": 0.85,
          "text-background-padding": 2,
          "text-valign": "bottom",
          "text-margin-y": 7,
          "text-wrap": "wrap",
          "text-max-width": 88,
          "min-zoomed-font-size": 10,
          cursor: "pointer",
        },
      },
      {
        selector: "edge",
        style: {
          "curve-style": "bezier",
          "line-color": "data(color)",
          "target-arrow-shape": "triangle",
          "target-arrow-color": "data(color)",
          width: 1.4,
          label: "data(label)",
          "font-size": 10,
          color: T.foreground,
          "text-background-color": T.background,
          "text-background-opacity": 0.85,
          "text-background-padding": 4,
          "text-valign": "top",
          "text-margin-y": -10,
          "min-zoomed-font-size": 8,
        },
      },
      { selector: "edge.dashed", style: { "line-style": "dashed" } },
    ];
  }
  return [
    {
      selector: "node",
      style: {
        width: CARD_W + 8,
        height: 84,
        shape: "rectangle",
        "background-opacity": 0,
        "border-width": 0,
        cursor: "pointer",
      },
    },
    {
      selector: "edge",
      style: {
        "curve-style": "bezier",
        "line-color": "data(color)",
        "target-arrow-shape": "triangle",
        "target-arrow-color": "data(color)",
        width: 1.4,
        label: "data(label)",
        "font-size": 10,
        color: T.foreground,
        "text-background-color": T.card,
        "text-background-opacity": 1,
        "text-background-padding": 4,
        "text-valign": "top",
        "text-margin-y": -10,
      },
    },
    { selector: "edge.dashed", style: { "line-style": "dashed" } },
  ];
}

// eliminateOverlaps 布局后消重叠(P2-14b④):按节点外包尺寸做成对推开
// 松弛,直到零重叠或 80 轮上限——确定性、不动连线。W/H 为外包尺寸
// (富卡片 CARD_W+24 × 120;降采样色点 44 × 44)。
// P4-58:大图改空间网格配对(同外包尺寸格子,只与邻格比对)——原全对
// 两两比对在数千节点上是 O(80×N²) 单段长任务(实测 3340 节点冻结主
// 线程 150 秒),网格化后每轮近线性,推开语义不变(同尺寸外包、同侧
// 推开、同轮上限)。
function eliminateOverlaps(cy, W, H) {
  const list = cy.nodes().map((n) => ({ n, p: { ...n.position() } }));
  // 邻格偏移固定序(含自身格),保证确定性。
  const CELLS = [
    [-1, -1], [0, -1], [1, -1],
    [-1, 0], [0, 0], [1, 0],
    [-1, 1], [0, 1], [1, 1],
  ];
  const cellKey = (x, y) => x + "," + y;
  for (let iter = 0; iter < 80; iter++) {
    let moved = false;
    // 位置入格:格宽高 = 外包尺寸,重叠只可能发生在同格与八邻格。
    const grid = new Map();
    for (const it of list) {
      const k = cellKey(Math.floor(it.p.x / W), Math.floor(it.p.y / H));
      if (!grid.has(k)) grid.set(k, []);
      grid.get(k).push(it);
    }
    // 同格内按稳定序(i<j)成对推开,邻格单向(右/下向)处理避免重复对。
    for (const it of list) {
      const cx = Math.floor(it.p.x / W);
      const cyy = Math.floor(it.p.y / H);
      const partners = [];
      for (const [dx, dy] of CELLS) {
        const bucket = grid.get(cellKey(cx + dx, cyy + dy));
        if (bucket) partners.push(...bucket);
      }
      for (const other of partners) {
        if (other === it || other.n.id() <= it.n.id()) continue;
        const a = it.p;
        const b = other.p;
        const dx = b.x - a.x;
        const dy = b.y - a.y;
        const ox = W - Math.abs(dx);
        const oy = H - Math.abs(dy);
        if (ox > 0 && oy > 0) {
          moved = true;
          let px = 0;
          let py = 0;
          if (dx === 0 && dy === 0) {
            px = (other.n.id() < it.n.id() ? 1 : -1) * (W / 2);
          } else if (ox < oy) {
            px = (dx >= 0 ? 1 : -1) * (ox / 2 + 1);
          } else {
            py = (dy >= 0 ? 1 : -1) * (oy / 2 + 1);
          }
          a.x -= px;
          a.y -= py;
          b.x += px;
          b.y += py;
        }
      }
    }
    if (!moved) break;
  }
  cy.batch(() => list.forEach(({ n, p }) => n.position(p)));
}

// CyLens:props.elements = { nodes: [cardSpec], edges: [{id,type,from,to}] }。
// 数据同步走 diff(增删 + 就地改 data;拓扑变化才重跑布局,轮询属性
// 更新不扰动布局);小地图 cytoscape-navigator,缩放按钮为自有控件。
// mass(P4-58 降采样):超过 CY_RICH_LABEL_MAX 节点时由父组件以 key 换
// 挂本组件——HTML 卡片标签不绑定,节点走画布原生色点 + 标题,交互期
// 纹理快照零重排;点击/聚焦/过滤语义不变。
function CyLens({ elements, focus, onNodeClick, heightClass, mass = false }) {
  const wrapRef = React.useRef(null);
  const cyRef = React.useRef(null);
  const clickRef = React.useRef(onNodeClick);
  clickRef.current = onNodeClick;
  const fitReassert = React.useRef(null);
  const navClass = React.useRef("gr-cy-nav-" + Math.random().toString(36).slice(2, 8)).current;
  const focusedOnce = React.useRef("");

  // fit + 450ms 复述:部分内嵌浏览器画布测量在挂载初期未就绪,首拍
  // fit 可能落在错误视口上(实测症状:主画布空白、小地图正常);复述
  // 一次兜底,容器尺寸变化(resize)同样重适配。
  const fitWithReassert = () => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.fit(undefined, 48);
    clearTimeout(fitReassert.current);
    fitReassert.current = setTimeout(() => cyRef.current?.fit(undefined, 48), 450);
  };

  React.useEffect(() => {
    const cy = cytoscape({
      container: wrapRef.current,
      style: cyStyle(mass),
      elements: [],
      wheelSensitivity: 0.22,
      minZoom: 0.1,
      maxZoom: 2.5,
      // P4-58:交互期(缩放/平移)画静态纹理快照并藏边——数千节点时
      // 逐元素重绘冻结视图;松手恢复全量绘制,静止画面不变。降采样
      // 模式再降渲染像素比(retina 全比填充成本 ×4),大图流畅优先。
      textureOnViewport: true,
      hideEdgesOnViewport: true,
      pixelRatio: mass ? 1 : undefined,
    });
    // 第二参数必须给(哪怕空对象):省略时扩展不设 pointer-events:none,
    // 标签浮层会盖住画布吞掉节点点击与缩放控件。降采样模式不绑 HTML
    // 标签(画布原生色点 + 标题)。
    if (!mass) {
      cy.nodeHtmlLabel([{ query: "node", halign: "center", valign: "center", tpl: (d) => cardHTML(d) }], {});
    }
    cy.on("tap", "node", (ev) => {
      clickRef.current?.({ type: ev.target.data("type"), id: ev.target.data("id") });
    });
    cy.on("resize", () => fitWithReassert());
    let nav = null;
    // 小地图是第二张全量画布,每次视口变化整图重渲——数千节点时与主图
    // 同量级(P4-58 实测每次手势 ~200ms 主线程占用),降采样模式不挂,
    // 主图流畅优先;阈值内行为不变。
    if (!mass) {
      try {
        nav = cy.navigator({ container: "." + navClass });
      } catch {
        // 小地图初始化失败不阻断主图
      }
    }
    cyRef.current = cy;
    return () => {
      clearTimeout(fitReassert.current);
      try {
        nav?.destroy?.();
      } catch {
        // 同上,清理失败交由 cy.destroy 兜底
      }
      cy.destroy();
      cyRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navClass, mass]);

  React.useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;
    const { nodes, edges } = elements;
    const wantN = new Map(nodes.map((n) => [n.id, n]));
    const wantE = new Map(edges.map((e) => [e.id, e]));
    let topo = false;
    const dotColorOf = (spec) => toneHex(spec.tone);
    cy.batch(() => {
      const rmN = cy.nodes().filter((n) => !wantN.has(n.id()));
      const rmE = cy.edges().filter((e) => !wantE.has(e.id()));
      if (rmN.length > 0 || rmE.length > 0 || cy.nodes().length !== wantN.size) topo = true;
      rmN.remove();
      rmE.remove();
      const addN = [];
      for (const n of nodes) {
        const el = cy.getElementById(n.id);
        if (el.nonempty()) {
          if (el.data("__v") !== n.__v) el.data(mass ? { ...n, dotColor: dotColorOf(n) } : { ...n });
        } else {
          addN.push({ group: "nodes", data: mass ? { ...n, dotColor: dotColorOf(n) } : { ...n } });
          topo = true;
        }
      }
      const addE = [];
      for (const e of edges) {
        if (cy.getElementById(e.id).nonempty()) continue;
        addE.push({
          group: "edges",
          data: { id: e.id, source: e.from, target: e.to, label: e.label, color: e.color },
          classes: e.dash ? ["dashed"] : [],
        });
      }
      if (addN.length > 0) cy.add(addN);
      if (addE.length > 0) cy.add(addE);
    });
    if (topo) {
      // P2-14b④:发散布局参数调优——低重力防"纺锤形"纵向聚拢(横向
      // 展开),更大理想边长/斥力/分量间距拉开净距;cose 原生重叠规避
      // 对卡片型节点不收敛,布局后追加确定性消重叠通道。
      // P4-58:降采样模式(大图)改网格布局——cose 逐对斥力 O(N²)/迭代,
      // 数千节点即便降档迭代仍是数秒级同步长任务(实测 3340 节点满额
      // 1200 冻结主线程 150 秒、降 30 次仍 7.7 秒);网格 O(N) 即时完成,
      // 排布形态变化(力导本就每次随机),节点/边/标签/点击语义不变,
      // 净距仍由消重叠通道兜底。富卡片模式维持 cose,迭代按节点数降档。
      if (mass) {
        cy.layout({ name: "grid", avoidOverlap: true, condense: false, spacing: 56, sort: undefined }).run();
      } else {
        const nCount = cy.nodes().length;
        const numIter = nCount <= 600 ? 1200 : 600;
        cy
          .layout({
            name: "cose",
            animate: false,
            randomize: true,
            padding: 80,
            numIter,
            idealEdgeLength: (e) => (e.isEdge() ? 170 : 100),
            nodeRepulsion: () => 20000,
            nodeOverlap: 36,
            componentSpacing: 200,
            edgeElasticity: 0.4,
            gravity: 0.08,
          })
          .run();
      }
      eliminateOverlaps(cy, mass ? 44 : CARD_W + 24, mass ? 44 : 120);
      fitWithReassert();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [elements]);

  // focus 聚焦(?focus= 直达 /「在图中查看」跳入):居中放大 + 卡片
  // 主色描边(描边由 data.focused 驱动,页级统一计时)。
  React.useEffect(() => {
    if (!focus) {
      focusedOnce.current = "";
      return;
    }
    const cy = cyRef.current;
    if (!cy || focusedOnce.current === focus) return;
    const el = cy.getElementById(focus);
    if (el.empty()) return;
    focusedOnce.current = focus;
    cy.zoom(1);
    cy.center(el);
  }, [focus, elements]);

  const zoomBy = (k) => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.zoom({ level: Math.min(Math.max(cy.zoom() * k, 0.1), 2.5), renderedPosition: { x: cy.width() / 2, y: cy.height() / 2 } });
  };

  return (
    <div className={cn("relative overflow-hidden rounded-xl border border-border/55 bg-card", heightClass || "h-[calc(100vh-318px)] min-h-[420px]")}>
      {/* 画布容器定位走内联样式:Cytoscape 初始化会注入
          .__________cytoscape_container{position:relative} 样式表,晚于
          Tailwind 生效会把 absolute 类顶掉、容器塌成 0 高(卡片浮层照常
          渲染但命中区与 fit 全部失效)——内联样式优先级高于注入表。 */}
      <div ref={wrapRef} style={{ position: "absolute", top: 0, left: 0, right: 0, bottom: 0 }} />
      <div className="absolute right-3 top-3 z-20 flex flex-col gap-1">
        {[
          { icon: "plus", title: "放大", fn: () => zoomBy(1.25) },
          { icon: "minus", title: "缩小", fn: () => zoomBy(0.8) },
          { icon: "maximize2", title: "适配全图", fn: () => cyRef.current?.fit(undefined, 48) },
        ].map((b) => (
          <button
            key={b.icon}
            title={b.title}
            className="rounded-md border border-border/55 bg-card/95 p-2 text-muted-foreground shadow-xs transition-colors duration-150 hover:bg-hover-lift hover:text-foreground cursor-pointer"
            onClick={b.fn}
          >
            {React.createElement(iconOf(b.icon), { className: "size-3.5" })}
          </button>
        ))}
      </div>
      {!mass && <div className={cn("gr-cy-nav", navClass)} />}
    </div>
  );
}

// ---- ReactFlow 透镜(血缘 / 攻击链)----

// RfLens:nodes/edges 为 ReactFlow 格式(spec 卡片数据;边带语义色与
// 线上标签)。聚焦沿用进度图管线(onInit 捕获实例 + setCenter,不依赖
// 库内部测量);MiniMap 节点色随类型色。
function RfLens({ rfNodes, rfEdges, focus, onNodeClick, heightClass }) {
  const rfRef = React.useRef(null);
  const reassert = React.useRef(null);
  const fitRefit = React.useRef(null);
  const posRef = React.useRef(null);
  posRef.current = React.useMemo(
    () => new Map(rfNodes.map((n) => [n.id, n.position])),
    [rfNodes],
  );
  const focusedOnce = React.useRef("");

  React.useEffect(
    () => () => {
      clearTimeout(reassert.current);
      clearTimeout(fitRefit.current);
    },
    [],
  );

  // fitView 复述一次:节点尺寸测量竞态下首拍 fit 可能只圈到已测节点
  //(血证:攻击链透镜首拍只见链 1),600ms 后按全量重适配。
  React.useEffect(() => {
    clearTimeout(fitRefit.current);
    fitRefit.current = setTimeout(() => {
      rfRef.current?.fitView({ padding: 0.22, maxZoom: 1, duration: 250 });
    }, 600);
  }, [rfNodes]);

  React.useEffect(() => {
    if (!focus) {
      focusedOnce.current = "";
      return;
    }
    const pos = posRef.current.get(focus);
    if (!pos || focusedOnce.current === focus) return;
    focusedOnce.current = focus;
    const center = () => {
      if (rfRef.current) rfRef.current.setCenter(pos.x + CARD_W / 2, pos.y + 36, { zoom: 1.05, duration: 250 });
    };
    center();
    clearTimeout(reassert.current);
    reassert.current = setTimeout(center, 450);
  }, [focus, rfNodes]);

  return (
    <div className={cn("overflow-hidden rounded-xl border border-border/55 bg-card", heightClass || "h-[calc(100vh-318px)] min-h-[420px]")}>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={RF_NODE_TYPES}
        edgeTypes={EDGE_TYPES}
        onInit={(inst) => {
          rfRef.current = inst;
        }}
        onNodeClick={(_, n) => onNodeClick({ type: n.data.spec.type, id: n.id })}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        onlyRenderVisibleElements
        fitView
        fitViewOptions={{ padding: 0.22, maxZoom: 1 }}
        minZoom={0.12}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={26} size={1.2} color="var(--border)" />
        <Controls showInteractive={false} position="bottom-right" />
        <MiniMap
          position="bottom-left"
          pannable
          zoomable
          maskColor="color-mix(in srgb, var(--background) 72%, transparent)"
          nodeColor={(n) => toneHex(n.data?.spec?.tone)}
          nodeStrokeColor="var(--background)"
          nodeBorderRadius={3}
          style={{ width: 168, height: 116, borderRadius: 10, border: "1px solid var(--border)", background: "var(--card)" }}
        />
      </ReactFlow>
    </div>
  );
}

// ---- 透镜定义与数据投影 ----

const LENSES = [
  { id: "panorama", icon: "network", label: "发散全景" },
  { id: "lineage", icon: "sprout", label: "血缘" },
  { id: "chain", icon: "link", label: "攻击链" },
  { id: "assetsnet", icon: "server", label: "资产网" },
];

function specVersion(spec) {
  return JSON.stringify(spec);
}

// 通用元素集(发散全景 = 全图;血缘 = 全图去 sub_of)。规格带 __v 供
// Cytoscape 就地 diff。
function buildElements(nodes, edges, withSubOf) {
  const idSet = new Set(nodes.map((n) => n.id));
  const out = {
    nodes: nodes.map((n) => ({ ...n, __v: specVersion(n) })),
    edges: edges
      .filter((e) => (withSubOf ? true : e.type !== "sub_of") && idSet.has(e.from) && idSet.has(e.to))
      .map((e) => {
        const m = edgeMeta(e.type);
        return {
          id: `${e.type}:${e.from}->${e.to}`,
          type: e.type,
          from: e.from,
          to: e.to,
          label: edgeLabel(e.type),
          color: m.color,
          dash: Boolean(m.dashed),
        };
      }),
  };
  // 同对同型平行边不重复(账本唯一,防御即可)
  const seen = new Set();
  out.edges = out.edges.filter((e) => (seen.has(e.id) ? false : (seen.add(e.id), true)));
  return out;
}

// 血缘透镜:layoutGraph 分层(derived_from 反向取父子,同进度图),边
// 从左向右指(parent→child);跨列边走同一套避让路由(P2-14b④)。
function buildLineage(nodes, edges) {
  const T = graphPalette();
  const { nodes: specs, edges: ledges } = buildElements(nodes, edges, false);
  const rawEdges = edges.filter((e) => e.type !== "sub_of");
  const pos = layoutGraph(specs, rawEdges);
  const cardRects = grCardRects(pos);
  const rfNodes = specs.map((n) => ({
    id: n.id,
    type: "annulusCard",
    position: { x: pos.get(n.id)?.x || 0, y: pos.get(n.id)?.y || 0 },
    data: { spec: n },
    draggable: false,
    selectable: false,
  }));
  const rfEdges = ledges.map((e) => {
    const rev = e.type === "derived_from";
    const srcID = rev ? e.to : e.from;
    const tgtID = rev ? e.from : e.to;
    const route =
      pos.get(srcID) && pos.get(tgtID)
        ? routeEdge(pos.get(srcID), pos.get(tgtID), cardRects, new Set([e.from, e.to]))
        : { curv: 0.35, bow: 0 };
    return {
      id: e.id,
      source: srcID,
      target: tgtID,
      type: "grEdge",
      data: route,
      label: e.label,
      labelColor: T.foreground,
      labelBg: `color-mix(in srgb, ${T.card} 92%, transparent)`,
      style: { stroke: e.color, strokeWidth: 1.3, strokeDasharray: e.dash ? "6 4" : undefined },
      markerEnd: { type: MarkerType.ArrowClosed, color: e.color, width: 15, height: 15 },
    };
  });
  return { rfNodes, rfEdges };
}

// 攻击链透镜:每条组合声明一行——声明在左,环节事实按链序向右(角色
// 入徽章),资产锚点沉在各自环节下方;环节边角色即线上标签。
const CHAIN_COL = 268;
const CHAIN_ASSET_BASE = 152;
const CHAIN_ASSET_ROW = 118;
const CHAIN_BLOCK_GAP = 72;

function buildChain(nodes, edges, claims, hiddenEdges, focusedID) {
  const T = graphPalette();
  const nodeOf = new Map(nodes.map((n) => [n.id, n]));
  const assetsOf = (id) => edges.filter((e) => e.type === "about" && e.from === id).map((e) => e.to);
  const showAbout = !hiddenEdges.has("about");
  const rfNodes = [];
  const rfEdges = [];
  let y = 0;
  let chains = 0;
  for (const c of claims || []) {
    if (!c.combined) continue;
    const facts = (c.chain || [])
      .map((l) => ({ role: l.role || "", node: nodeOf.get(l.fact_id) }))
      .filter((f) => f.node);
    if (facts.length === 0) continue;
    chains++;
    const claimSpec = cardSpec(nodeOf.get(c.id) || { id: c.id, type: "testclaim", title: c.statement, status: c.status }, null, focusedID);
    rfNodes.push({ id: c.id, type: "annulusCard", position: { x: 0, y }, data: { spec: claimSpec } });
    const used = new Set();
    let maxAssets = 0;
    facts.forEach((f, k) => {
      const x = (k + 1) * CHAIN_COL;
      const spec = cardSpec(f.node, null, focusedID, f.role);
      rfNodes.push({ id: f.node.id, type: "annulusCard", position: { x, y }, data: { spec } });
      rfEdges.push({
        id: `chain:${c.id}->${f.node.id}`,
        source: c.id,
        target: f.node.id,
        type: "default",
        label: f.role || "环节",
        labelStyle: { fill: T.mutedFg, fontSize: 10 },
        labelBgStyle: { fill: T.card, fillOpacity: 0.92 },
        labelBgPadding: [4, 2],
        labelBgBorderRadius: 4,
        style: { stroke: T.primary, strokeWidth: 1.4 },
        markerEnd: { type: MarkerType.ArrowClosed, color: T.primary, width: 15, height: 15 },
      });
      if (!showAbout) return;
      let ai = 0;
      for (const aid of assetsOf(f.node.id)) {
        const a = nodeOf.get(aid);
        if (!a || used.has(aid)) continue;
        used.add(aid);
        rfNodes.push({
          id: a.id,
          type: "annulusCard",
          position: { x, y: y + CHAIN_ASSET_BASE + ai * CHAIN_ASSET_ROW },
          data: { spec: cardSpec(a, null, focusedID) },
        });
        rfEdges.push({
          id: `about:${f.node.id}->${a.id}`,
          source: f.node.id,
          target: a.id,
          type: "default",
          label: "关于",
          labelStyle: { fill: T.mutedFg, fontSize: 10 },
          labelBgStyle: { fill: T.card, fillOpacity: 0.92 },
          labelBgPadding: [4, 2],
          labelBgBorderRadius: 4,
          style: { stroke: T.info, strokeWidth: 1.2 },
          markerEnd: { type: MarkerType.ArrowClosed, color: T.info, width: 14, height: 14 },
        });
        ai++;
      }
      maxAssets = Math.max(maxAssets, ai);
    });
    if (showAbout) {
      let ai = 0;
      for (const aid of c.asset_ids || []) {
        const a = nodeOf.get(aid);
        if (!a || used.has(aid)) continue;
        used.add(aid);
        rfNodes.push({
          id: a.id,
          type: "annulusCard",
          position: { x: 0, y: y + CHAIN_ASSET_BASE + ai * CHAIN_ASSET_ROW },
          data: { spec: cardSpec(a, null, focusedID) },
        });
        rfEdges.push({
          id: `about:${c.id}->${a.id}`,
          source: c.id,
          target: a.id,
          type: "default",
          label: "关于",
          labelStyle: { fill: T.mutedFg, fontSize: 10 },
          labelBgStyle: { fill: T.card, fillOpacity: 0.92 },
          labelBgPadding: [4, 2],
          labelBgBorderRadius: 4,
          style: { stroke: T.info, strokeWidth: 1.2 },
          markerEnd: { type: MarkerType.ArrowClosed, color: T.info, width: 14, height: 14 },
        });
        ai++;
      }
      maxAssets = Math.max(maxAssets, ai);
    }
    y += CHAIN_ASSET_BASE + maxAssets * CHAIN_ASSET_ROW + CHAIN_BLOCK_GAP;
  }
  return { rfNodes, rfEdges, chains };
}

// 资产网透镜:资产互联——sub_of 层级 + 共同锚点归并(同一事实/声明/
// 假设挂在多枚资产上时,资产两两互联,边上注明经由何种节点)。
function buildAssetsNet(nodes, edges) {
  const assets = nodes.filter((n) => n.type === "asset");
  const assetSet = new Set(assets.map((n) => n.id));
  const nodeTypeOf = new Map(nodes.map((n) => [n.id, n.type]));
  const outEdges = [];
  for (const e of edges) {
    if (e.type !== "sub_of" || !assetSet.has(e.from) || !assetSet.has(e.to)) continue;
    const m = edgeMeta("sub_of");
    outEdges.push({ id: `sub_of:${e.from}->${e.to}`, type: "sub_of", from: e.from, to: e.to, label: edgeLabel("sub_of"), color: m.color, dash: true });
  }
  const anchors = new Map();
  for (const e of edges) {
    if (e.type !== "about" || !assetSet.has(e.to)) continue;
    if (!anchors.has(e.from)) anchors.set(e.from, []);
    anchors.get(e.from).push(e.to);
  }
  const seen = new Set();
  for (const [src, list] of anchors) {
    const st = nodeTypeOf.get(src);
    const viaType = st === "testclaim" ? "via_claim" : st === "hypothesis" ? "via_hyp" : st === "fact" ? "via_fact" : null;
    if (!viaType || list.length < 2) continue;
    const uniq = [...new Set(list)];
    for (let i = 0; i < uniq.length; i++) {
      for (let j = i + 1; j < uniq.length; j++) {
        const [a, b] = [uniq[i], uniq[j]].sort();
        const key = `${viaType}:${a}|${b}`;
        if (seen.has(key)) continue;
        seen.add(key);
        const m = edgeMeta(viaType);
        outEdges.push({ id: key, type: viaType, from: a, to: b, label: edgeLabel(viaType), color: m.color, dash: false });
      }
    }
  }
  return {
    nodes: assets.map((n) => ({ ...n, __v: specVersion(n) })),
    edges: outEdges,
  };
}

// ---- 工具栏(透镜切换 + 六类节点过滤开关 + 边类型图例)----

function FilterChips({ nodes, edges, hiddenTypes, hiddenEdges, onToggleType, onToggleEdge }) {
  const typeCounts = {};
  for (const n of nodes) typeCounts[n.type] = (typeCounts[n.type] || 0) + 1;
  const edgeCounts = {};
  for (const e of edges) edgeCounts[e.type] = (edgeCounts[e.type] || 0) + 1;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      {NODE_ORDER.map((type) => {
        const meta = nodeTypeMeta(type);
        const on = !hiddenTypes.has(type);
        return (
          <button
            key={type}
            className={cn(
              "flex items-center gap-2 rounded-full border border-border/55 bg-card px-2 py-0.5 transition-opacity duration-150 cursor-pointer hover:bg-hover-lift",
              !on && "opacity-40 saturate-50",
            )}
            title={on ? `隐藏${meta.label}节点` : `显示${meta.label}节点`}
            onClick={() => onToggleType(type)}
          >
            <span className={cn("size-2.5 border border-current", meta.shape, SWATCH_TONE[meta.tone])} />
            {meta.label} {typeCounts[type] || 0}
          </button>
        );
      })}
      <span className="h-4 w-px bg-border" />
      {Object.entries(edgeCounts)
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([type, count]) => {
          const m = edgeMeta(type);
          const on = !hiddenEdges.has(type);
          return (
            <button
              key={type}
              className={cn(
                "flex items-center gap-2 rounded-full border border-border/55 bg-card px-2 py-0.5 transition-opacity duration-150 cursor-pointer hover:bg-hover-lift",
                !on && "opacity-40 saturate-50",
              )}
              title={on ? `隐藏${edgeLabel(type)}关系` : `显示${edgeLabel(type)}关系`}
              onClick={() => onToggleEdge(type)}
            >
              <span
                className="w-4 border-t-2"
                style={{ borderColor: m.color, borderStyle: m.dashed ? "dashed" : "solid" }}
              />
              {edgeLabel(type)} {count}
            </button>
          );
        })}
    </div>
  );
}

const NODE_ORDER = ["goal", "step", "fact", "testclaim", "hypothesis", "asset"];

// ---- 年轮图标签页 ----

export function AnnulusPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const { data: graph, error } = usePolling(tid ? `/api/tasks/${tid}/graph` : "/api/healthz", 3000, {
    immediate: Boolean(tid),
  });
  const { data: claims } = usePolling(tid ? `/api/tasks/${tid}/claims` : "/api/healthz", 4000, {
    immediate: Boolean(tid),
  });

  const [lens, setLens] = React.useState("panorama");
  const [hiddenTypes, setHiddenTypes] = React.useState(() => new Set());
  const [hiddenEdges, setHiddenEdges] = React.useState(() => new Set());
  const [sheet, setSheet] = React.useState(null);
  const [focusedID, setFocusedID] = React.useState(null);
  const ringTimer = React.useRef(null);

  React.useEffect(
    () => () => {
      clearTimeout(ringTimer.current);
    },
    [],
  );

  // focus 聚焦(URL ?focus= 直达或「在图中查看」跳入):描边 3 秒渐隐。
  React.useEffect(() => {
    if (!focus || !graph) return;
    setFocusedID(focus);
    clearTimeout(ringTimer.current);
    ringTimer.current = setTimeout(() => setFocusedID(null), 3000);
  }, [focus, graph]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="年轮图" subtitle="看总览——全貌与关联" />
        <EmptyState
          icon="network"
          title="未选择任务"
          hint="年轮图随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  const nodes = (graph?.nodes || []).filter((n) => !hiddenTypes.has(n.type));
  const rawNodes = graph?.nodes || [];
  const rawEdges = graph?.edges || [];
  if (rawNodes.length === 0) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="年轮图" subtitle="看总览——全貌与关联" />
        <EmptyState
          icon="network"
          title="暂无节点"
          hint={error ? `读取失败:${error}` : "账本记录后,全图在此生长"}
          action={{ label: "去会话", href: `/task/${tid}/sessions` }}
        />
      </div>
    );
  }

  const claimsMap = new Map((claims || []).map((c) => [c.id, c]));
  const claimOf = (id) => claimsMap.get(id);
  const specs = nodes.map((n) => cardSpec(n, claimOf, focusedID));
  const visEdges = rawEdges.filter((e) => !hiddenEdges.has(e.type));

  // 各透镜投影(同一账本数据):链透镜在声明类被过滤时如实清空;资产网
  // 透镜只留资产与互联边;血缘透镜不画 sub_of。
  const chainClaims = hiddenTypes.has("testclaim") ? [] : claims || [];
  const chainData = buildChain(nodes, visEdges, chainClaims, hiddenEdges, focusedID);
  const assetsEl = buildAssetsNet(specs, visEdges);
  const assetIDs = new Set(assetsEl.nodes.map((n) => n.id));
  assetsEl.edges = assetsEl.edges.filter((e) => assetIDs.has(e.from) && assetIDs.has(e.to));
  const legendEdges =
    lens === "panorama"
      ? rawEdges
      : lens === "lineage"
        ? rawEdges.filter((e) => e.type !== "sub_of")
        : lens === "chain"
          ? [
              ...rawEdges.filter((e) => e.type === "about"),
              ...chainData.rfEdges.filter((e) => e.id.startsWith("chain:")).map((e) => ({ type: "chain" })),
            ]
          : assetsEl.edges;

  const toggleType = (t) =>
    setHiddenTypes((s) => {
      const next = new Set(s);
      if (next.has(t)) next.delete(t);
      else next.add(t);
      return next;
    });
  const toggleEdge = (t) =>
    setHiddenEdges((s) => {
      const next = new Set(s);
      if (next.has(t)) next.delete(t);
      else next.add(t);
      return next;
    });

  const edgeTotal = rawEdges.length;
  const edgeVisible = visEdges.length;

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title="年轮图" subtitle="看总览——全貌与关联" />

      {/* 透镜切换 + 计数(同一账本数据,四透镜不同投影) */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex w-fit gap-0.5 rounded-lg bg-muted p-1">
          {LENSES.map((l) => (
            <button
              key={l.id}
              className={
                "flex items-center gap-2 rounded-md px-3 py-1 text-[13px] transition-colors duration-150 cursor-pointer " +
                (lens === l.id
                  ? "bg-popover text-foreground shadow-xs"
                  : "text-muted-foreground hover:bg-hover-lift hover:text-foreground")
              }
              onClick={() => setLens(l.id)}
            >
              {React.createElement(iconOf(l.icon), { className: "size-3.5" })}
              {l.label}
            </button>
          ))}
        </div>
        <span className="ml-auto text-xs text-muted-foreground">
          节点 {nodes.length}/{rawNodes.length} · 关系 {edgeVisible}/{edgeTotal} · 点击节点看详情
        </span>
      </div>

      <FilterChips
        nodes={rawNodes}
        edges={legendEdges}
        hiddenTypes={hiddenTypes}
        hiddenEdges={hiddenEdges}
        onToggleType={toggleType}
        onToggleEdge={toggleEdge}
      />

      {!graph ? (
        <div className="skeleton h-[calc(100vh-318px)] min-h-[420px]" />
      ) : lens === "panorama" ? (
        (() => {
          const el = buildElements(specs, visEdges, true);
          // P4-58 降采样:超过阈值换挂画布原生节点模式(key 换挂重初始化,
          // 阈值内与原 HTML 卡片完全一致)。
          const mass = el.nodes.length > CY_RICH_LABEL_MAX;
          return (
            <CyLens key={mass ? "mass" : "rich"} elements={el} focus={focus} onNodeClick={setSheet} mass={mass} />
          );
        })()
      ) : lens === "assetsnet" ? (
        assetsEl.nodes.length === 0 ? (
          <div className="flex h-[calc(100vh-318px)] min-h-[420px] items-center justify-center rounded-xl border border-border/55 bg-card">
            <EmptyState icon="server" title="暂无资产" hint="资产记录后在此互联" />
          </div>
        ) : (
          <CyLens elements={assetsEl} focus={focus} onNodeClick={setSheet} />
        )
      ) : lens === "chain" ? (
        chainData.chains === 0 ? (
          <div className="flex h-[calc(100vh-318px)] min-h-[420px] items-center justify-center rounded-xl border border-border/55 bg-card">
            <EmptyState
              icon="link"
              title="暂无组合发现"
              hint="主宰把多条线索合并成攻击链级发现后在此呈现"
            />
          </div>
        ) : (
          <RfLens rfNodes={chainData.rfNodes} rfEdges={chainData.rfEdges} focus={focus} onNodeClick={setSheet} />
        )
      ) : (
        (() => {
          const { rfNodes, rfEdges } = buildLineage(specs, visEdges);
          return <RfLens rfNodes={rfNodes} rfEdges={rfEdges} focus={focus} onNodeClick={setSheet} />;
        })()
      )}

      <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} graph={graph ? { ...graph, tid } : undefined} />
    </div>
  );
}
