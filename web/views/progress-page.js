"use client";

import * as React from "react";
import "@xyflow/react/dist/style.css";
import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  Handle,
  Position,
  ReactFlow,
} from "@xyflow/react";

import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import {
  DEAD_STATUSES,
  nodeStatusMeta,
  usePolling,
} from "@/lib/api";
import { NodeSheetHost } from "@/views/node-sheets";
import { cn } from "@/lib/utils";

// 进度图页(任务空间·进度图标签,P2-09;8.7 进度图视图——血缘投影,
//"生命树"的正脸):统一图投影接口(GET /api/tasks/{id}/graph,树/DAG/
// 全图三视图共用)之上的一次视图投影——Goal 为根向右生长的 ReactFlow
// 血缘 DAG,六类节点六种形状/颜色,废弃分支灰显保留(R10 可视化),
// 新节点长出高亮动画;节点点击 → 跳转协议 Sheet 抽屉,引用可下钻
//(fact# 跳转),「在图中查看」携带 ?focus= URL 定位参数可直达。

// ---- 节点视觉语言(8.7:形状 × 类型色,色值走 8.4 token) ----

const NODE_STYLE = {
  goal: { shape: "gr-shape-goal", tone: "running", width: 208, label: "目标" },
  step: { shape: "gr-shape-step", tone: "high", width: 192, label: "步骤" },
  fact: { shape: "gr-shape-fact", tone: "success", width: 184, label: "事实" },
  testclaim: { shape: "gr-shape-claim", tone: "critical", width: 204, label: "声明" },
  hypothesis: { shape: "gr-shape-hypo", tone: "warning", width: 188, label: "假设", dashed: true },
  asset: { shape: "gr-shape-asset", tone: "info", width: 172, label: "资产" },
};

// DAG 分层布局:Goal 为根向右生长(列 = 最长血缘深度),未挂锚的种子资产沉到最右一列;
// sub_of 仅资产树视图消费,这里不画。P2-11 起导出共用:年轮图·血缘透镜
// 同布局算法(全图含 falsify 派生边,反证步骤成为声明的父节点——反证也
// 走出血缘)。P2-14b④ 布局优化:同列节点按血缘重心重排(子女贴近
// 父节点,缩短走线、减少交叉与贴卡擦边)+ 各列垂直居中(消除单侧
// 大片空白)+ 间距加大 + 贝塞尔边(柔和弧线,不再沿卡片边缘直角蹭线)。
const COL_GAP = 264;
const ROW_GAP = 140;

export function layoutGraph(nodes, edges) {
  const idSet = new Set(nodes.map((n) => n.id));
  const parents = new Map();
  const children = new Map();
  for (const e of edges) {
    if (e.type === "sub_of" || !idSet.has(e.from) || !idSet.has(e.to)) continue;
    // derived_from 的账本方向是 声明→来源(声明派生自事实);DAG 按
    // Goal→…→声明 向右生长,取来源为父(反向)。
    const [parent, child] = e.type === "derived_from" ? [e.to, e.from] : [e.from, e.to];
    if (!parents.has(child)) parents.set(child, []);
    parents.get(child).push(parent);
    if (!children.has(parent)) children.set(parent, []);
    children.get(parent).push(child);
  }

  const layer = new Map();
  const visiting = new Set();
  const depthOf = (id) => {
    if (layer.has(id)) return layer.get(id);
    if (visiting.has(id)) return 0; // 环兜底:视作同层,不再深挖
    visiting.add(id);
    let d = 0;
    for (const p of parents.get(id) || []) d = Math.max(d, depthOf(p) + 1);
    visiting.delete(id);
    layer.set(id, d);
    return d;
  };
  for (const n of nodes) depthOf(n.id);

  // 未挂锚资产(无任何入边)沉最右,不与 Goal 抢第 0 列。
  let maxLinked = 0;
  for (const n of nodes) {
    if (n.type !== "asset") maxLinked = Math.max(maxLinked, layer.get(n.id) || 0);
  }
  for (const n of nodes) {
    if (n.type === "asset" && !parents.has(n.id)) layer.set(n.id, maxLinked + 1);
  }

  const cols = new Map();
  for (const n of nodes) {
    const c = layer.get(n.id) || 0;
    if (!cols.has(c)) cols.set(c, []);
    cols.get(c).push(n);
  }
  const revOf = (n) => n.created_rev || 0;
  for (const list of cols.values()) {
    list.sort((a, b) => revOf(a) - revOf(b) || (a.id < b.id ? -1 : 1));
  }

  // 重心重排(Sugiyama 启发式):自左向右按父节点平均位置排,再自右
  // 向左按子节点平均位置回排,三遍收敛;无血缘的节点(种子资产)保持
  // 入账序沉在列尾。
  const colKeys = [...cols.keys()].sort((a, b) => a - b);
  const indexOf = new Map();
  const refreshIndex = () => {
    for (const list of cols.values()) list.forEach((n, i) => indexOf.set(n.id, i));
  };
  refreshIndex();
  const avgIndex = (ids) => {
    let sum = 0;
    let cnt = 0;
    for (const id of ids) {
      const i = indexOf.get(id);
      if (i !== undefined) {
        sum += i;
        cnt++;
      }
    }
    return cnt ? sum / cnt : null;
  };
  for (let pass = 0; pass < 5; pass++) {
    const downward = pass % 2 === 0; // 下行、上行交替,末遍下行
    const keys = downward ? colKeys : [...colKeys].reverse();
    for (const c of keys) {
      const list = cols.get(c);
      const scored = list.map((n, i) => {
        const rel = downward ? parents.get(n.id) : children.get(n.id);
        const s = rel ? avgIndex(rel) : null;
        return { n, s: s === null ? list.length + i : s, i };
      });
      scored.sort((a, b) => a.s - b.s || a.i - b.i);
      cols.set(c, scored.map((x) => x.n));
      refreshIndex();
    }
  }

  // 各列垂直居中(以最高列为基准),消除顶部/底部单侧空白。
  let maxSpan = 0;
  for (const list of cols.values()) maxSpan = Math.max(maxSpan, (list.length - 1) * ROW_GAP);
  const pos = new Map();
  for (const [c, list] of [...cols.entries()].sort((a, b) => a[0] - b[0])) {
    const y0 = (maxSpan - (list.length - 1) * ROW_GAP) / 2;
    list.forEach((n, i) => {
      pos.set(n.id, { x: c * COL_GAP, y: y0 + i * ROW_GAP, w: NODE_STYLE[n.type]?.width || 180 });
    });
  }
  return pos;
}

// GrNode 进度图节点卡:形状徽记(六类六形)+ 标题 + 状态徽标;废弃
// 分支灰显(降饱和降透明,R10 可视化),新生节点高亮描边渐隐,聚焦
// 节点主色描边。
function GrNode({ data }) {
  const { node } = data;
  const style = NODE_STYLE[node.type] || NODE_STYLE.asset;
  const dead = DEAD_STATUSES.has(node.status);
  const st = nodeStatusMeta(node.type, node.status);
  return (
    <div
      className={cn(
        "group relative flex flex-col gap-1 rounded-xl border bg-card px-3 py-2 shadow-xs transition-shadow duration-150 hover:shadow-card",
        dead && "opacity-40 saturate-50",
        data.focused && "border-primary ring-2 ring-primary",
        data.newborn && "gr-node-newborn border-primary/70 ring-2 ring-primary/70",
      )}
      style={{ width: style.width }}
      title={`${style.label} ${node.id}${st ? ` · ${st.label}` : ""}`}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} className="!invisible" />
      <Handle type="source" position={Position.Right} isConnectable={false} className="!invisible" />
      <div className="flex items-center gap-2">
        <span
          className={cn(
            "size-[18px] shrink-0 border-current",
            style.shape,
            style.tone === "running" && "bg-status-running/25 text-status-running",
            style.tone === "high" && "bg-severity-high/25 text-severity-high",
            style.tone === "success" && "bg-status-success/25 text-status-success",
            style.tone === "critical" && "bg-severity-critical/25 text-severity-critical",
            style.tone === "warning" && "bg-status-warning/25 text-status-warning",
            style.tone === "info" && "bg-severity-info/25 text-severity-info",
            style.dashed && "border-2 border-dashed",
          )}
        />
        <span className="text-[10px] tracking-wide text-muted-foreground">{style.label}</span>
        {st && (
          <Badge variant={st.variant} className="ml-auto">
            {st.label}
          </Badge>
        )}
      </div>
      <div className="line-clamp-2 text-[13px] leading-snug">{node.title || node.id}</div>
      <div className="font-mono text-[10px] text-muted-foreground">{node.id}</div>
    </div>
  );
}

const NODE_TYPES = { grNode: GrNode };

// ---- 边避让路由(P2-14b④) ----
// 卡片外包估计(两行标题的进度图节点 / 单行标题的年轮图卡片,统一按
// 88 高估算,外扩 6px 余量);布局坐标即 ReactFlow 节点左上角。
export const GR_CARD_H = 88;
const GR_CARD_PAD = 6;

// routeEdge 选边形态:相邻列取高曲率(纵向变位收在目标侧列间通道);
// 跨列边在弓高候选集中采样贝塞尔,以命中罚基 + 穿透深度加权的总代价
// 最小者胜出(平局取幅度小),把长边抬离水平带绕开中转卡片。
export function routeEdge(a, b, rects, ignore) {
  const x1 = a.x + a.w;
  const y1 = a.y + GR_CARD_H / 2;
  const x2 = b.x;
  const y2 = b.y + GR_CARD_H / 2;
  const span = Math.abs(b.x - a.x) / COL_GAP;
  if (span <= 1.5) return { curv: 0.55, bow: 0 };
  const bows = [0, 32, -32, 64, -64, 96, -96, 128, -128, 160, -160, 192, -192];
  const curvs = [0.12, 0.25, 0.42, 0.62]; // 通道内坡 / 晚坡 / 中坡 / 早坡
  let best = { curv: 0.42, bow: 0, cost: Infinity };
  for (const curv of curvs) {
    const dx = (x2 - x1) * curv;
    for (const bow of bows) {
      const cy1 = y1 + bow;
      const cy2 = y2 + bow;
      let cost = 0;
      for (let k = 1; k < 32; k++) {
        const t = k / 32;
        const mt = 1 - t;
        const x = mt ** 3 * x1 + 3 * mt ** 2 * t * (x1 + dx) + 3 * mt * t ** 2 * (x2 - dx) + t ** 3 * x2;
        const y = mt ** 3 * y1 + 3 * mt ** 2 * t * cy1 + 3 * mt * t ** 2 * cy2 + t ** 3 * y2;
        for (const r of rects) {
          if (ignore.has(r.id)) continue;
          if (
            x > r.left - GR_CARD_PAD &&
            x < r.right + GR_CARD_PAD &&
            y > r.top - GR_CARD_PAD &&
            y < r.bottom + GR_CARD_PAD
          ) {
            const pen = Math.min(
              x - (r.left - GR_CARD_PAD),
              r.right + GR_CARD_PAD - x,
              y - (r.top - GR_CARD_PAD),
              r.bottom + GR_CARD_PAD - y,
            );
            cost += 4 + pen;
          }
        }
      }
      if (cost < best.cost) best = { curv, bow, cost };
    }
  }
  return { curv: best.curv, bow: best.bow };
}

// grCardRects 布局坐标 → 卡片外包矩形清单(避让采样用)。
export function grCardRects(positions) {
  const out = [];
  for (const [id, p] of positions) {
    out.push({ id, left: p.x, right: p.x + p.w, top: p.y, bottom: p.y + GR_CARD_H });
  }
  return out;
}

// GrEdge 跨度感知贝塞尔(P2-14b④):按 routeEdge 选出的曲率与弓高走线;
// 可选 label 沿弧顶呈现(年轮图血缘透镜的线上关系标签)。
function GrEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  style,
  markerEnd,
  label,
  labelColor,
  labelBg,
  data,
}) {
  const curv = data?.curv ?? 0.35;
  const bow = data?.bow || 0;
  const dx = (targetX - sourceX) * curv;
  const d = `M ${sourceX},${sourceY} C ${sourceX + dx},${sourceY + bow} ${targetX - dx},${targetY + bow} ${targetX},${targetY}`;
  return (
    <>
      <BaseEdge id={id} path={d} style={style} markerEnd={markerEnd} />
      {label ? (
        <EdgeLabelRenderer>
          <div
            style={{
              position: "absolute",
              transform: `translate(-50%,-50%) translate(${(sourceX + targetX) / 2}px,${
                (sourceY + targetY) / 2 + bow * 0.75
              }px)`,
              color: labelColor,
              background: labelBg,
              borderRadius: 4,
              padding: "1px 4px",
              fontSize: 10,
              pointerEvents: "none",
            }}
            className="nodrag nopan"
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      ) : null}
    </>
  );
}

export const EDGE_TYPES = { grEdge: GrEdge };

// GraphCanvas 图主体。轮询刷新:新增节点 diff 出来打新生标(2.6 秒后
// 渐隐)。focus 定位用 onInit 捕获的实例 + 自有布局坐标 setCenter——
// 不依赖库内部节点测量(该路径在部分内嵌浏览器失效)。
// heightClass 供内嵌形态(工作台 P2-10)覆盖画布高度,缺省独立全屏;
// sheet/onSheetChange 供宿主页共用同一抽屉(不传则内部自理,独立页
// 的既有行为不变)。
export function GraphCanvas({ data, focus, heightClass, sheet, onSheetChange }) {
  const [innerSheet, setInnerSheet] = React.useState(null);
  const openSheet = onSheetChange || setInnerSheet;
  const activeSheet = sheet !== undefined ? sheet : innerSheet;
  const [newborn, setNewborn] = React.useState(() => new Set());
  const [focusedID, setFocusedID] = React.useState(null);
  const seen = React.useRef(null);
  const newbornTimer = React.useRef(null);
  const rfRef = React.useRef(null);
  const reassertTimer = React.useRef(null);
  const ringTimer = React.useRef(null);
  const focusedOnce = React.useRef("");

  const nodes = data?.nodes || [];
  const edges = data?.edges || [];
  const positions = React.useMemo(() => layoutGraph(nodes, edges), [nodes, edges]);
  const positionsRef = React.useRef(positions);
  positionsRef.current = positions;
  const ready = positions.size > 0;

  // 新节点长出高亮:与上一拍快照 diff,新 ID 打标(首拍全量不算新)。
  React.useEffect(() => {
    if (!data) return;
    const ids = new Set(nodes.map((n) => n.id));
    if (seen.current) {
      const fresh = nodes.filter((n) => !seen.current.has(n.id)).map((n) => n.id);
      if (fresh.length > 0) {
        setNewborn(new Set(fresh));
        clearTimeout(newbornTimer.current);
        newbornTimer.current = setTimeout(() => setNewborn(new Set()), 2600);
      }
    }
    seen.current = ids;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  // 卸载时清尾。
  React.useEffect(
    () => () => {
      clearTimeout(newbornTimer.current);
      clearTimeout(reassertTimer.current);
      clearTimeout(ringTimer.current);
    },
    [],
  );

  // focus 定位(URL ?focus= 直达或「在图中查看」跳入):按自有布局坐标
  // setCenter 居中 + 主色描边 3 秒后恢复。首次调用可能在库实例就绪前,
  // 450ms 复述一次兜底;定时器走 ref,不被后续渲染的 effect 重跑清掉
  //(依赖只挂 focus 与 ready,轮询快照更新不重触发)。
  React.useEffect(() => {
    if (!focus) {
      focusedOnce.current = "";
      return;
    }
    if (!ready || focusedOnce.current === focus) return;
    const pos = positionsRef.current.get(focus);
    if (!pos) return;
    focusedOnce.current = focus;
    const center = () => {
      if (rfRef.current) {
        rfRef.current.setCenter(pos.x + pos.w / 2, pos.y + 40, { zoom: 1.05, duration: 250 });
      }
    };
    center();
    clearTimeout(reassertTimer.current);
    reassertTimer.current = setTimeout(center, 450);
    setFocusedID(focus);
    clearTimeout(ringTimer.current);
    ringTimer.current = setTimeout(() => setFocusedID(null), 3000);
  }, [focus, ready]);

  const rfNodes = React.useMemo(
    () =>
      nodes.map((n) => ({
        id: n.id,
        type: "grNode",
        position: { x: positions.get(n.id)?.x || 0, y: positions.get(n.id)?.y || 0 },
        data: { node: n, newborn: newborn.has(n.id), focused: focusedID === n.id },
        draggable: false,
        selectable: false,
      })),
    [nodes, positions, newborn, focusedID],
  );

  const idSet = new Set(nodes.map((n) => n.id));
  const deadSet = new Set(nodes.filter((n) => DEAD_STATUSES.has(n.status)).map((n) => n.id));
  const cardRects = React.useMemo(() => grCardRects(positions), [positions]);
  const rfEdges = React.useMemo(
    () =>
      edges
        .filter((e) => e.type !== "sub_of" && idSet.has(e.from) && idSet.has(e.to))
        .map((e, i) => {
          // 避让路由:按渲染方向(父子从左向右)选曲率与弓高。
          const srcID = e.type === "derived_from" ? e.to : e.from;
          const tgtID = e.type === "derived_from" ? e.from : e.to;
          const route =
            positions.get(srcID) && positions.get(tgtID)
              ? routeEdge(positions.get(srcID), positions.get(tgtID), cardRects, new Set([e.from, e.to]))
              : { curv: 0.35, bow: 0 };
          return {
            id: `${e.type}-${e.from}-${e.to}-${i}`,
            source: srcID,
            target: tgtID,
            type: "grEdge",
            data: route,
            style: {
              stroke:
                deadSet.has(e.from) && deadSet.has(e.to) ? "var(--border)" : "var(--muted-foreground)",
              strokeWidth: 1.2,
            },
          };
        }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [edges, nodes, cardRects],
  );

  return (
    <>
      <div
        className={cn(
          "overflow-hidden rounded-xl border bg-card",
          heightClass || "h-[calc(100vh-236px)] min-h-[420px]",
        )}
      >
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          nodeTypes={NODE_TYPES}
          edgeTypes={EDGE_TYPES}
          onInit={(inst) => {
            rfRef.current = inst;
          }}
          onNodeClick={(_, n) => openSheet({ type: n.data.node.type, id: n.id })}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          fitView
          fitViewOptions={{ padding: 0.22, maxZoom: 1 }}
          minZoom={0.15}
          maxZoom={2}
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={26} size={1.2} color="var(--border)" />
          <Controls showInteractive={false} position="bottom-right" />
        </ReactFlow>
      </div>
      <NodeSheetHost tid={data.tid} value={activeSheet} onChange={openSheet} graph={data} />
    </>
  );
}

// 进度图标签页(8.7:Goal 为根向右生长;看过程——结论怎么一步步来的)。
export function ProgressPage({ currentTask, focus }) {
  const tid = currentTask?.id;
  const { data, error } = usePolling(tid ? `/api/tasks/${tid}/graph` : "/api/healthz", 3000, {
    immediate: Boolean(tid),
  });

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="进度图" subtitle="看过程——结论怎么一步步来的" />
        <EmptyState
          icon="gitBranch"
          title="未选择任务"
          hint="进度图随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  const nodes = data?.nodes || [];
  if (nodes.length === 0) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="进度图" subtitle="看过程——结论怎么一步步来的" />
        <EmptyState
          icon="gitBranch"
          title="暂无节点"
          hint={
            error
              ? `读取失败:${error}`
              : "主宰派发步骤、虫群回流事实与声明后,血缘链在此生长"
          }
          action={{ label: "去会话", href: `/task/${tid}/sessions` }}
        />
      </div>
    );
  }

  const counts = {};
  for (const n of nodes) counts[n.type] = (counts[n.type] || 0) + 1;

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title="进度图" subtitle="看过程——结论怎么一步步来的" />
      {/* 六类节点图例(形状徽记 × 类型色 × 计数;废弃分支灰显保留) */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
        {Object.entries(NODE_STYLE).map(([type, style]) =>
          counts[type] ? (
            <span key={type} className="flex items-center gap-2">
              <span
                className={cn(
                  "size-3 border-current",
                  style.shape,
                  style.tone === "running" && "bg-status-running/25 text-status-running",
                  style.tone === "high" && "bg-severity-high/25 text-severity-high",
                  style.tone === "success" && "bg-status-success/25 text-status-success",
                  style.tone === "critical" && "bg-severity-critical/25 text-severity-critical",
                  style.tone === "warning" && "bg-status-warning/25 text-status-warning",
                  style.tone === "info" && "bg-severity-info/25 text-severity-info",
                  style.dashed && "border-2 border-dashed",
                )}
              />
              {style.label} {counts[type]}
            </span>
          ) : null,
        )}
        <span className="ml-auto">废弃分支灰显保留 · 点击节点看详情</span>
      </div>
      {data ? (
        <GraphCanvas data={{ ...data, tid }} focus={focus} />
      ) : (
        <div className="skeleton h-[calc(100vh-236px)] min-h-[420px]" />
      )}
    </div>
  );
}
