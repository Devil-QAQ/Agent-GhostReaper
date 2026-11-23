// 全景图自研 Canvas 引擎。节点布局来自服务端；这里只计算相机投影、
// 连线绘制与坐标拾取。展示与交互依据已确认 demo 及本轮实际预览。
// 相机保留进入关联前的完整视野；详情按钮只通知页面打开正式侧卡。
// 颜色读全站主题 token，文字始终随卡片缩放，尊重系统减少动态效果。

"use client";

// ---- 8.4 token 的画布取值(回退=暗色 token 值,与 globals.css 同源) ----

const PANOV_DARK = {
  background: "#060607",
  card: "#0d0d0f",
  foreground: "#e7e5e4",
  mutedFg: "#9b9a94",
  border: "#232327",
  primary: "#8fae94",
  intent: "#b4a1cc",
  critical: "#a92e2e",
  high: "#e5484d",
  medium: "#d97f3e",
  low: "#7fa3d4",
  info: "#7e7d78",
  warning: "#c9a96e",
};

function panoPalette() {
  if (typeof window === "undefined") return { ...PANOV_DARK, asset: PANOV_DARK.primary };
  const cs = getComputedStyle(document.body);
  const v = (name, fb) => cs.getPropertyValue(name).trim() || fb;
  return {
    background: v("--background", PANOV_DARK.background),
    card: v("--card", PANOV_DARK.card),
    foreground: v("--foreground", PANOV_DARK.foreground),
    mutedFg: v("--muted-foreground", PANOV_DARK.mutedFg),
    border: v("--border", PANOV_DARK.border),
    primary: v("--primary", PANOV_DARK.primary),
    asset: v("--status-success", PANOV_DARK.primary),
    intent: v("--pano-intent", PANOV_DARK.intent),
    critical: v("--severity-critical", PANOV_DARK.critical),
    high: v("--severity-high", PANOV_DARK.high),
    medium: v("--severity-medium", PANOV_DARK.medium),
    low: v("--severity-low", PANOV_DARK.low),
    info: v("--severity-info", PANOV_DARK.info),
    warning: v("--status-warning", PANOV_DARK.warning),
  };
}

// 漏洞严重级四色(critical/high/medium/low;证据不足=灰,已修复=级别
// 色空心)。前端词表与服务端 claim_cases.severity 同词。
function vulnColor(p, sev) {
  return (
    { critical: p.critical, high: p.high, medium: p.medium, low: p.low }[sev] || p.info
  );
}

// panoFadeRGB 主题底色(#rrggbb)→ 渐隐渐变的 rgb 三元组串(四边渐隐
// 用,亮暗主题各自推导;非十六进制形态退暗色底)。
function panoFadeRGB(bg) {
  const m = /^#([0-9a-f]{6})$/i.test(bg) ? bg.slice(1) : "";
  if (!m) return "6,6,7";
  const n = parseInt(m, 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255].join(",");
}

export const PANO_SIZE = { cardW: 168, cardH: 48 };
const DETAIL_BUTTON = { x: 40, y: 3, w: 36, h: 18 };
const limit = (v, a, b) => Math.max(a, Math.min(b, v));
const smooth = (t) => t * t * (3 - 2 * t);
const edgeKey = e => `${e.from}\0${e.to}\0${e.rel}`;
const pairKey = e => `${e.from}\0${e.to}`;
const ARRIVAL = { node: 600, edge: 800 };
const NAVIGATION_MS = 680;

// Switching the highlight within one unchanged linear chain does not add a
// navigation level. Branches, different directions, and changed membership do.
export function samePanoramaChain(before, after) {
  if (!before || !after) return false;
  const a = before.ids || [], b = after.ids || [], edges = before.edges || [], other = after.edges || [];
  if (!a.length || a.length !== b.length || edges.length !== a.length - 1 || edges.length !== other.length) return false;
  const ids = new Set(a), links = new Set(edges.map(e => `${e.from}\0${e.to}`));
  if (ids.size !== a.length || b.some(id => !ids.has(id)) || other.some(e => !links.has(`${e.from}\0${e.to}`))) return false;
  const facts = view => Object.entries(view.results || {}).map(([id,rows]) => [id,(rows || []).map(f => f.id).sort().join("\0")]).sort(([a],[b]) => a.localeCompare(b));
  if (JSON.stringify(facts(before)) !== JSON.stringify(facts(after))) return false;
  const incoming = new Set(), next = new Map();
  for (const e of edges) {
    if (incoming.has(e.to) || next.has(e.from)) return false;
    incoming.add(e.to); next.set(e.from, e.to);
  }
  let root;
  for (const id of ids) if (!incoming.has(id)) { if (root !== undefined) return false; root = id; }
  const walked = new Set();
  while (root !== undefined && !walked.has(root)) { walked.add(root); root = next.get(root); }
  return walked.size === ids.size;
}

// The ledger supplies all node coordinates and edge routes. This renderer only
// projects them through a camera; picking and presentation use the same positions.
export class PanoramaEngine {
  constructor(canvas, minimap, cb = {}) {
    this.canvas = canvas; this.ctx = canvas.getContext("2d"); this.mm = minimap; this.mmCtx = minimap?.getContext("2d"); this.cb = cb;
    this.nodes = []; this.edges = []; this.byId = new Map(); this.aliases = {}; this.bounds = [0, 0, 1, 1];
    this.positions = new Map(); this.positionFlight = null; this.focusEdges = new Map(); this.focusResults = new Map(); this.focusEdgeFans = new Map(); this.focusBounds = null; this.focusPending = false;
    this.cam = { x: 0, y: 0, scale: 1, sy: 1 }; this.focus = null; this.returnView = null; this.active = null;
    this.flight = null; this.zoomFlight = null; this.panTarget = null; this.velocity = { x: 0, y: 0 }; this.pointer = null; this.hoveredDetail = null;
    this.fade = { from: 0, to: 0, value: 0, at: 0 }; this.born = new Map(); this.edgeBorn = new Map(); this.arrivalWait = null; this.fitted = false; this.dirty = true; this.destroyed = false;
    this.visibility = new Map(); this.visibilityFlight = null; this.routeFlight = null;
    this.palette = panoPalette(); this.reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.mac = /Mac|iPhone|iPad/i.test(navigator.userAgentData?.platform || navigator.platform); this.lastNotify = 0; this.lastViewport = null;
    this._resize = this._resize.bind(this); this._loop = this._loop.bind(this); this._bind(); this.ro = new ResizeObserver(this._resize); this.ro.observe(canvas.parentElement); this._resize();
    this.themeObserver = new MutationObserver(() => { this.palette = panoPalette(); this.dirty = true; }); this.themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style"] });
    this.frame = requestAnimationFrame(this._loop);
  }
  destroy() { this.destroyed = true; cancelAnimationFrame(this.frame); this.ro.disconnect(); this.themeObserver.disconnect(); for (const [name, fn] of Object.entries(this.handlers)) this.canvas.removeEventListener(name, fn); }
  _resize() { const r = this.canvas.getBoundingClientRect(); this.w = Math.max(1, r.width); this.h = Math.max(1, r.height); this.dpr = Math.min(window.devicePixelRatio || 1, 2); this.canvas.width = Math.round(this.w * this.dpr); this.canvas.height = Math.round(this.h * this.dpr); if (this.mm) { this.mm.width = 160 * this.dpr; this.mm.height = 96 * this.dpr; } this.dirty = true; }
  setData(data) {
    const old = this.byId, oldEdges = new Set(this.edges?.map(pairKey)), first = !this.fitted, now = performance.now(); this.data = data; this.nodes = data.nodes || []; this.edges = data.edges || []; this.bounds = data.bounds || [0, 0, 1, 1]; this.aliases = data.aliases || {}; this.byId = new Map(this.nodes?.map(n => [n.id, n]));
    let added = false;
    for (const n of this.nodes) if (!old.has(n.id) && !first) { this.born.set(n.id, now); added = true; }
    for (const id of (this.born?.keys() || [])) if (!this.byId.has(id)) this.born.delete(id);
    if (!this.edgeBorn) this.edgeBorn = new Map();
    const pairs = new Set(this.edges?.map(pairKey));
    for (const e of this.edges) if (!first && !oldEdges.has(pairKey(e))) { this.edgeBorn.set(pairKey(e), { at: now, delay: this.born.has(e.from) || this.born.has(e.to) ? ARRIVAL.node : 0 }); added = true; }
    for (const key of (this.edgeBorn?.keys() || [])) if (!pairs.has(key)) this.edgeBorn.delete(key);
    if (this.focus && this.arrivalWait == null && added) this.arrivalWait = now;
    // Dense source branches share a trunk. Each real source/target pair remains
    // independent for focus and provenance; batching only changes drawing.
    const fans = new Map(); this.edgeFans = new Map();
    for (const e of this.edges) {
      if (this.byId.get(e.from)?.y === this.byId.get(e.to)?.y) continue;
      const key = `${e.from}:${this.byId.get(e.to)?.y}`;
      if (!fans.has(key)) fans.set(key, []);
      fans.get(key).push(e);
    }
    for (const group of fans.values()) if (group.length >= 12) for (const e of group) this.edgeFans.set(e, group);
    if (first && (this.nodes?.length || 0)) { this.fitted = true; this.initialView(true); }
    if (this.focus) { if (!this.byId.has(this.focus)) this.exitFocus(); else { this.active = this.focusPending ? new Set([this.focus]) : new Set([...(this.active || [])].filter(id => this.byId.has(id))); this.active.add(this.focus); } }
    for (const id of (this.positions?.keys() || [])) if (!this.byId.has(id)) this.positions.delete(id);
    this.dirty = true;
  }
  // A focused refresh first resolves the selected scope. Start arrivals only
  // after that projection is ready, so a slow read cannot consume the fade.
  resumeArrivals(at = performance.now()) {
    if (this.arrivalWait == null) return;
    for (const [key,start] of (this.born || [])) if (start >= this.arrivalWait) this.born.set(key,at);
    for (const arrival of (this.edgeBorn?.values() || [])) if (arrival.at >= this.arrivalWait) arrival.at = at;
    this.arrivalWait = null; this.dirty = true;
  }
  _arrivalProgress(at, duration, delay = 0, now = this.paintAt ?? performance.now()) {
    if (at == null || this.reduced.matches) return 1;
    if (this.arrivalWait != null && at >= this.arrivalWait) return 0;
    return smooth(limit((now - at - delay) / duration, 0, 1));
  }
  _nodeProgress(id, now) { return this.born?.size ? this._arrivalProgress(this.born.get(id), ARRIVAL.node, 0, now) : 1; }
  _edgeProgress(e) {
    if (!this.edgeBorn?.size) return 1;
    const arrival = this.edgeBorn?.get(pairKey(e));
    return this._arrivalProgress(arrival?.at, ARRIVAL.edge, arrival?.delay || 0);
  }
  _viewBounds() { return this.focus && this.focusBounds ? this.focusBounds : this.bounds; }
  _world(n, now = this.paintAt ?? performance.now()) {
    const f = this.positionFlight;
    if (!f) return this.positions.get(n.id) || n;
    if (!f.from.has(n.id) && !f.to.has(n.id)) return n;
    const a = f.from.get(n.id) || n, b = f.to.get(n.id) || n, t = smooth(limit((now - f.at) / (f.ms || NAVIGATION_MS), 0, 1));
    if (t === 0) return { x: a.x, y: a.y }; if (t === 1) return { x: b.x, y: b.y };
    if (f.cameraFrom && f.cameraTo) {
      const c = f.cameraFrom, d = f.cameraTo, scale = c.scale + (d.scale - c.scale) * t, sy = c.sy + (d.sy - c.sy) * t;
      const x = (a.x - c.x) * c.scale * (1 - t) + (b.x - d.x) * d.scale * t;
      const y = (a.y - c.y) * c.scale * c.sy * (1 - t) + (b.y - d.y) * d.scale * d.sy * t;
      return { x: c.x + (d.x - c.x) * t + x / scale, y: c.y + (d.y - c.y) * t + y / (scale * sy) };
    }
    return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
  }
  _moveNodes(to, instant = false, at = performance.now()) {
    if (!instant && this.positions?.size === to.size && [...to].every(([id,p]) => { const prior = this.positions.get(id); return prior?.x === p.x && prior?.y === p.y; })) return;
    const now = at, ids = new Set(this.nodes?.map(n => n.id)), from = new Map();
    for (const id of ids) { const n = this.byId.get(id); if (n) from.set(id, this._world(n, this.paintAt ?? now)); }
    this.positions = to; this.positionFlight = instant || this.reduced.matches || !from.size ? null : { from, to, at: now, ms: NAVIGATION_MS }; this.dirty = true;
  }
  _displayEdge(e) { return this.focus ? this.focusEdges.get(edgeKey(e)) || e : e; }
  _edgeMode(e) {
    if (!this.focus) return false;
    return this.active?.has(e.from) && this.active?.has(e.to) && this.focusEdges.has(edgeKey(e)) ? true : false;
  }
  _paintEdges() { return this.edges?.map(e => this._displayEdge(e)); }
  _captureRoutes() { this.nextRouteFrom = new Map(this.edges?.map(e => [edgeKey(e), this._screenRoute(this._displayEdge(e))])); }
  _visibility(key, now = this.paintAt ?? performance.now()) {
    const f = this.visibilityFlight, target = this.visibility?.get(key) ?? 1;
    if (!f) return target;
    const from = f.from.get(key) ?? 1, t = this.reduced.matches ? 1 : smooth(limit((now - f.at) / NAVIGATION_MS, 0, 1));
    return from + (target - from) * t;
  }
  _setVisibility(ids, edges, at, instant = false) {
    const from = new Map(), to = new Map(), selected = new Set((edges || []).map(edgeKey));
    for (const n of this.nodes) { const key = `n:${n.id}`; from.set(key, this._visibility(key)); to.set(key, !ids || ids.has(n.id) ? 1 : .16); }
    for (const e of this.edges) { const key = `e:${edgeKey(e)}`; from.set(key, this._visibility(key)); to.set(key, !ids || selected.has(edgeKey(e)) ? 1 : .12); }
    this.visibility = to; this.visibilityFlight = instant || this.reduced.matches ? null : { from, at };
  }
  _resetFocus(instant = false, at = performance.now()) { this._captureRoutes(); this.resumeArrivals(at); this._setVisibility(null, null, at, instant); this.focus = null; this.focusPending = false; this.focusBounds = null; this.focusEdges.clear(); this.focusResults?.clear(); this.focusEdgeFans?.clear(); this._moveNodes(new Map(), instant, at); this._fade(0, at); }
  _releaseNodeCamera() {
    const f = this.positionFlight; if (!f?.cameraFrom) return;
    const at = this.paintAt ?? performance.now(), from = new Map();
    for (const id of (f.from?.keys() || [])) { const n = this.byId.get(id); if (n) from.set(id, this._world(n, at)); }
    const routesFrom = new Map(), routesTo = new Map(), camera = this.cam;
    const world = points => points?.map(p => [camera.x + (p[0] - this.w / 2) / camera.scale, camera.y + (p[1] - this.h / 2) / (camera.scale * camera.sy)]);
    for (const original of this.edges) { const e = this._displayEdge(original), key = edgeKey(e); routesFrom.set(key, world(this._screenRoute(e))); routesTo.set(key, world(this._routeFor(e, this.positions, camera))); }
    const ms = Math.max(1, (f.ms || NAVIGATION_MS) - (at - f.at));
    this.positionFlight = { from, to: this.positions, at, ms };
    this.routeFlight = { from: routesFrom, to: routesTo, at, ms, world: true };
  }
  _stop(keepNodeCamera = false) { if (!keepNodeCamera) this._releaseNodeCamera(); this.flight = null; this.zoomFlight = null; this.panTarget = null; this.velocity = { x: 0, y: 0 }; }
  _bound() { const [x, y, w, h] = this._viewBounds(); this.cam.x = limit(this.cam.x, x, x + Math.max(1, w)); this.cam.y = limit(this.cam.y, y, y + Math.max(1, h)); }
  _fly(target, instant = false, at = performance.now()) {
    this._stop(true);
    if (instant || this.reduced.matches) { this.cam = { ...target }; this.routeFlight = null; }
    else {
      const from = new Map(), to = new Map();
      for (const original of this.edges) { const e = this._displayEdge(original), key = edgeKey(e); from.set(key, this.nextRouteFrom?.get(key) || this._screenRoute(e)); to.set(key, this._routeFor(e, this.positions, target)); }
      if (this.positionFlight?.at !== at) this.positionFlight = { from: new Map(this.nodes?.map(n => [n.id, this._world(n)])), to: this.positions, at, ms: NAVIGATION_MS };
      this.routeFlight = { from, to, at, ms: NAVIGATION_MS };
      this.flight = { from: { ...this.cam }, to: { ...target }, at, ms: NAVIGATION_MS };
      if (this.positionFlight?.at === at) { this.positionFlight.cameraFrom = { ...this.cam }; this.positionFlight.cameraTo = { ...target }; }
    }
    this.nextRouteFrom = null;
    this.dirty = true;
  }
  _fade(to, at = performance.now()) { this.fade = { from: this.fade.value, value: this.fade.value, to, at }; if (this.reduced.matches) this.fade.value = to; this.dirty = true; }
  _initialX(center, scale) {
    if (this.bounds[2] * scale <= this.w - 96) return center;
    const counts = new Map();
    for (const n of this.nodes) if (n.t === "step" && n.root) counts.set(n.root, (counts.get(n.root) || 0) + 1);
    let root = null, largest = 0;
    for (const n of this.nodes) if (n.t === "asset" && (counts.get(n.id) || 0) > largest) { root = n; largest = counts.get(n.id); }
    return root?.x ?? center;
  }
  initialView(instant = false) { const at = performance.now(), [x, y, w, h] = this.bounds, readable = Math.max(.65, .9 - .04 * Math.log2(Math.max(1, (this.nodes?.length || 0) / 20))), scale = Math.max(.001, Math.min((this.h - 88) / Math.max(1, h), Math.max((this.w - 96) / Math.max(1, w), readable), 1.25)); this.returnView = null; this._resetFocus(instant, at); this._fly({ x: this._initialX(x + w / 2, scale), y: y + h / 2, scale, sy: 1 }, instant, at); this.cb.onFocus?.(null); }
  fitAll(instant = false) { const at = performance.now(), [x, y, w, h] = this.bounds, scale = Math.max(.001, Math.min((this.w - 96) / Math.max(1, w), (this.h - 80) / Math.max(1, h), 1)); this.returnView = null; this._resetFocus(instant, at); this._fly({ x: x + w / 2, y: y + h / 2, scale, sy: Math.max(.1, (this.h - 96) / (Math.max(1, h) * scale)) }, instant, at); this.cb.onFocus?.(null); }
  _focusCamera(ids, selected) {
    const members = this.nodes?.filter(n => ids.has(n.id)); let scale = 1, box;
    const measure = (z) => { const b = { left: Infinity, right: -Infinity, top: Infinity, bottom: -Infinity }; for (const n of members) { const pos = this.positions.get(n.id) || n, g = n.id === selected ? 1.025 : 1; b.left = Math.min(b.left, pos.x - 84 * g); b.right = Math.max(b.right, pos.x + 84 * g); b.top = Math.min(b.top, pos.y - 24 * g); b.bottom = Math.max(b.bottom, pos.y + 24 * g); } for (const original of this.edges) { const e = this._displayEdge(original); if (!e.blocked || !ids.has(e.from) || !ids.has(e.to)) continue; const a = this.positions.get(e.from) || this.byId.get(e.from), c = this.positions.get(e.to) || this.byId.get(e.to), sign = c.x >= a.x ? 1 : -1, peak = a.y - sign * Math.min(100, Math.max(50, Math.abs(c.x - a.x) * z * .22)) * Math.min(1, z) / (2 * z); b.top = Math.min(b.top, peak); b.bottom = Math.max(b.bottom, peak); } return b; };
    for (let i = 0; i < 5; i++) { box = measure(scale); scale = Math.max(.001, Math.min(1.5, (this.w - 128) / Math.max(1, box.right - box.left), (this.h - 112) / Math.max(1, box.bottom - box.top))); } box = measure(scale); return { x: (box.left + box.right) / 2, y: (box.top + box.bottom) / 2, scale, sy: 1 };
  }
  resolve(id) { return this.byId.has(id) ? id : (this.aliases[id] || []).find(x => this.byId.has(x)); }
  enterFocus(view) {
    const id = this.resolve(view?.node?.id || view?.id || view); if (!id) return false;
    if (!this.focus) { this._stop(); this.returnView = { ...this.cam }; }
    this.focus = id; this.focusPending = true;
    // Keep the painted graph intact while its selected scope is loading.
    // The fade, camera and layout start together when that scope is ready.
    this.active = view?.ids ? new Set(view.ids?.filter(x => this.byId.has(x))) : this.active || new Set([id]); this.active.add(id);
    this.cb.onFocus?.(id); return true;
  }
  reconcileFocus(view, preserveCamera = false, restoreCamera = null) {
    if (!view?.ids || this.resolve(view.node?.id) !== this.focus || view.ids?.some(id => !this.byId.has(id))) return;
    this._captureRoutes();
    this.active = new Set(view.ids); this.active.add(this.focus);
    const positions = new Map((view.positions || []).filter(p => this.active.has(p.id) && Number.isFinite(p.x) && Number.isFinite(p.y)).map(p => [p.id,p]));
    const canonical = new Map(this.edges?.map(e => [edgeKey(e), e]));
    this.focusEdges = new Map((view.edges || []).filter(e => canonical.has(edgeKey(e))).map(e => [edgeKey(e),{ ...e, dashed: canonical.get(edgeKey(e)).dashed }]));
    this.focusResults = new Map(Object.entries(view.results || {}));
    this.focusEdgeFans = new Map();
    const fans = new Map();
    for (const e of (this.focusEdges?.values() || [])) {
      if (positions.get(e.from)?.y === positions.get(e.to)?.y) continue;
      const key = `${e.from}:${positions.get(e.to)?.y}`;
      if (!fans.has(key)) fans.set(key, []); fans.get(key).push(e);
    }
    for (const group of fans.values()) if (group.length >= 12) for (const e of group) this.focusEdgeFans.set(e, group);
    this.focusBounds = positions.size === this.active?.size ? view.bounds : null;
    const at = performance.now(); this.resumeArrivals(at); this._setVisibility(this.active, [...this.focusEdges?.values()], at); this._fade(1, at);
    this._moveNodes(positions.size === this.active?.size ? positions : new Map(), false, at);
    if (restoreCamera) this._fly(restoreCamera, false, at);
    else if (!preserveCamera) this._fly(this._focusCamera(this.active, this.focus), false, at);
    else if (this.positionFlight?.at === at) this._fly({ ...this.cam }, false, at);
    this.focusPending = false; this.dirty = true;
  }
  exitFocus(restoreCamera = null) { const target = restoreCamera || this.returnView, at = performance.now(); this.returnView = null; this._resetFocus(false, at); if (target) this._fly(target, false, at); this.cb.onFocus?.(null); this.dirty = true; }
  zoomBy(factor) { this._zoomAt(this.w / 2, this.h / 2, factor, false); }
  setHorizontal(value) { this._stop(); this.cam.x = this._viewBounds()[0] + limit(value, 0, 1) * this._viewBounds()[2]; this.dirty = true; }
  _screen(n) { const p = this._world(n); return { x: this.w / 2 + (p.x - this.cam.x) * this.cam.scale, y: this.h / 2 + (p.y - this.cam.y) * this.cam.scale * this.cam.sy }; }
  _size() { const z = Math.min(1.5, this.cam.scale); return { w: 168 * z, h: 48 * z, z }; }
  _zoomAt(x, y, factor, direct) { const prior = direct ? this.cam.scale : this.zoomFlight?.target || this.cam.scale, wx = this.cam.x + (x - this.w / 2) / this.cam.scale, wy = this.cam.y + (y - this.h / 2) / (this.cam.scale * this.cam.sy); this._stop(); const target = limit(prior * factor, .0001, 3); if (direct || this.reduced.matches) { this.cam.scale = target; this.cam.x = wx - (x - this.w / 2) / target; this.cam.y = wy - (y - this.h / 2) / (target * this.cam.sy); } else this.zoomFlight = { from: this.cam.scale, target, x, y, wx, wy, at: performance.now() }; this.dirty = true; }
  _bind() {
    const c = this.canvas;
    this.handlers = {
      pointerdown: e => { if (e.button !== 0 || this.pointer) return; this._stop(); this.pointer = { id: e.pointerId, x: e.offsetX, y: e.offsetY, lastX: e.offsetX, lastY: e.offsetY, at: performance.now(), cam: { ...this.cam }, vx: 0, vy: 0, moved: false, hit: this._pick(e.offsetX, e.offsetY) }; c.setPointerCapture(e.pointerId); },
      pointermove: e => { const p = this.pointer; if (!p) { const hit = this._pick(e.offsetX, e.offsetY); const detail = hit?.detail ? hit.n.id : null; if (detail !== this.hoveredDetail) { this.hoveredDetail = detail; this.dirty = true; } c.style.cursor = hit ? "pointer" : "grab"; this.cb.onHover?.(hit?.n || null, { x: e.offsetX, y: e.offsetY }); return; } if (p.id !== e.pointerId) return; const now = performance.now(), dt = now - p.at, dx = e.offsetX - p.x, dy = e.offsetY - p.y; if (dt > 0) { p.vx = p.vx * .5 + .5 * (e.offsetX - p.lastX) / dt; p.vy = p.vy * .5 + .5 * (e.offsetY - p.lastY) / dt; } p.at = now; p.lastX = e.offsetX; p.lastY = e.offsetY; if (Math.hypot(dx, dy) > 4) p.moved = true; if (p.moved) { this.cam.x = p.cam.x - dx / this.cam.scale; this.cam.y = p.cam.y - dy / (this.cam.scale * this.cam.sy); this._bound(); this.dirty = true; this.cb.onHover?.(null); } },
      pointerup: e => { const p = this.pointer; if (!p || p.id !== e.pointerId) return; this.pointer = null; if (p.moved) { if (!this.reduced.matches && performance.now() - p.at < 80) { const length = Math.hypot(p.vx, p.vy), f = length > 1.1 ? 1.1 / length : 1; this.velocity = { x: p.vx * f, y: p.vy * f }; this.inertiaAt = performance.now(); } return; } if (p.hit?.detail) { this.cb.onOpenDetail?.({ id: p.hit.n.id, type: p.hit.n.t === "claim" ? "testclaim" : p.hit.n.t }); return; } if (p.hit) this.cb.onNodeClick?.(p.hit.n); else this.cb.onBackgroundClick?.(); },
      pointercancel: () => { this.pointer = null; this._stop(); },
      pointerleave: () => { if (this.hoveredDetail) { this.hoveredDetail = null; this.dirty = true; } this.cb.onHover?.(null); },
      wheel: e => { e.preventDefault(); const r = c.getBoundingClientRect(), unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? this.h : 1; if (!this.mac || e.ctrlKey || e.metaKey) { const direct = this.mac && (e.ctrlKey || e.metaKey); this._zoomAt(e.clientX - r.left, e.clientY - r.top, Math.exp(-e.deltaY * unit * (direct ? .0028 : .002)), direct); } else { this._releaseNodeCamera(); this.flight = null; this.zoomFlight = null; this.velocity = { x: 0, y: 0 }; const horizontal = e.shiftKey && Math.abs(e.deltaX) < .1, dx = (horizontal ? e.deltaY : e.deltaX) * unit, dy = (horizontal ? 0 : e.deltaY) * unit; if (!this.panTarget) this.panTarget = { x: this.cam.x, y: this.cam.y }; this.panTarget.x += dx / this.cam.scale; this.panTarget.y += dy / (this.cam.scale * this.cam.sy); const [x, y, w, h] = this._viewBounds(); this.panTarget.x = limit(this.panTarget.x, x, x + w); this.panTarget.y = limit(this.panTarget.y, y, y + h); if (this.reduced.matches) { this.cam.x = this.panTarget.x; this.cam.y = this.panTarget.y; this.panTarget = null; } this.dirty = true; } },
    };
    for (const [name, fn] of Object.entries(this.handlers)) c.addEventListener(name, fn, name === "wheel" ? { passive: false } : undefined);
  }
  _pick(x, y) { const s = this._size(); for (let i = (this.nodes?.length || 0) - 1; i >= 0; i--) { const n = this.nodes[i]; if (this.focus && this.active && !this.active.has(n.id)) continue; const p = this._screen(n); if (Math.abs(x - p.x) > s.w / 2 + 3 || Math.abs(y - p.y) > s.h / 2 + 3) continue; const z = s.z, dx = (x - p.x) / z, dy = (y - p.y) / z, b = DETAIL_BUTTON; const detail = Boolean(this.focus && dx >= b.x && dx <= b.x + b.w && dy >= b.y && dy <= b.y + b.h); return { n, detail }; } return null; }
  _loop(now) {
    if (this.destroyed) return; const dt = Math.min(32, now - (this.lastFrame || now)); this.lastFrame = now; let animated = false;
    if (this.positionFlight) { if (this.reduced.matches || now - this.positionFlight.at >= (this.positionFlight.ms || NAVIGATION_MS)) this.positionFlight = null; animated = true; }
    if (this.routeFlight) { if (this.reduced.matches || now - this.routeFlight.at >= this.routeFlight.ms) this.routeFlight = null; animated = true; }
    if (this.visibilityFlight) { if (this.reduced.matches || now - this.visibilityFlight.at >= NAVIGATION_MS) this.visibilityFlight = null; animated = true; }
    if (this.flight) { const f = this.flight, t = Math.min(1, (now - f.at) / f.ms), e = smooth(t); for (const k of ["x", "y", "scale", "sy"]) this.cam[k] = f.from[k] + (f.to[k] - f.from[k]) * e; if (t === 1) { this.cam = { ...f.to }; this.flight = null; } animated = true; }
    if (this.zoomFlight) { const f = this.zoomFlight, t = Math.min(1, (now - f.at) / 150), e = 1 - Math.pow(1 - t, 3); this.cam.scale = Math.exp(Math.log(f.from) + (Math.log(f.target) - Math.log(f.from)) * e); this.cam.x = f.wx - (f.x - this.w / 2) / this.cam.scale; this.cam.y = f.wy - (f.y - this.h / 2) / (this.cam.scale * this.cam.sy); if (t === 1) this.zoomFlight = null; animated = true; }
    if (this.panTarget) { const f = 1 - Math.exp(-dt / 40); this.cam.x += (this.panTarget.x - this.cam.x) * f; this.cam.y += (this.panTarget.y - this.cam.y) * f; if (Math.hypot((this.panTarget.x - this.cam.x) * this.cam.scale, (this.panTarget.y - this.cam.y) * this.cam.scale * this.cam.sy) < .1) { this.cam.x = this.panTarget.x; this.cam.y = this.panTarget.y; this.panTarget = null; } animated = true; }
    if (!this.pointer && Math.hypot(this.velocity.x, this.velocity.y) > .02) { this.cam.x -= this.velocity.x * dt / this.cam.scale; this.cam.y -= this.velocity.y * dt / (this.cam.scale * this.cam.sy); const x = this.cam.x, y = this.cam.y; this._bound(); const decay = Math.exp(-dt / 105); this.velocity.x *= decay; this.velocity.y *= decay; if (this.cam.x !== x) this.velocity.x = 0; if (this.cam.y !== y) this.velocity.y = 0; if (now - this.inertiaAt > 360) this.velocity = { x: 0, y: 0 }; animated = true; }
    if (this.fade.value !== this.fade.to) { const t = this.reduced.matches ? 1 : Math.min(1, (now - this.fade.at) / NAVIGATION_MS); this.fade.value = this.fade.from + (this.fade.to - this.fade.from) * smooth(t); animated = true; }
    for (const [id,at] of this.born) {
      if (this.reduced.matches || (this.arrivalWait == null || at < this.arrivalWait) && now-at >= ARRIVAL.node) { this.born.delete(id); animated = true; }
      else if (this.arrivalWait == null || at < this.arrivalWait) animated = true;
    }
    for (const [key,arrival] of (this.edgeBorn || [])) {
      if (this.reduced.matches || (this.arrivalWait == null || arrival.at < this.arrivalWait) && now-arrival.at >= arrival.delay+ARRIVAL.edge) { this.edgeBorn.delete(key); animated = true; }
      else if (this.arrivalWait == null || arrival.at < this.arrivalWait) animated = true;
    }
    if (this.dirty || animated) { this._draw(now); this.dirty = false; this.lastDraw = now; }
    if (now - this.lastNotify > 100) { const [x,, w] = this._viewBounds(); const view = { coverage: Math.min(100, Math.round(this.w / Math.max(1, w * this.cam.scale) * 100)), position: limit((this.cam.x - x) / Math.max(1, w), 0, 1) }; if (!this.lastViewport || view.coverage !== this.lastViewport.coverage || Math.abs(view.position - this.lastViewport.position) > .0001) { this.cb.onViewport?.(view); this.lastViewport = view; } this.lastNotify = now; }
    this.frame = requestAnimationFrame(this._loop);
  }
  _round(ctx, x, y, w, h, r) { ctx.beginPath(); ctx.roundRect(x, y, w, h, Math.min(r, w / 2, h / 2)); }
  _text(ctx, text, x, y, width) {
    const value = String(text || ""), key = `${ctx.font}\0${width}\0${value}`;
    if (!this.textCache) this.textCache = new Map();
    let shown = this.textCache.get(key);
    if (shown === undefined) {
      shown = value;
      if (ctx.measureText(value).width > width) {
        let lo = 0, hi = value.length;
        while (lo < hi) { const mid = Math.ceil((lo + hi) / 2); if (ctx.measureText(value.slice(0, mid) + "…").width <= width) lo = mid; else hi = mid - 1; }
        shown = value.slice(0, lo) + "…";
      }
      if (this.textCache?.size >= 10000) this.textCache.clear();
      this.textCache.set(key, shown);
    }
    ctx.fillText(shown, x, y);
  }
  _edgeFan(ctx, group, active) {
    const first = group[0], progress = this._edgeProgress(first), a = this.byId.get(first.from), b = this.byId.get(first.to);
    if (!a || !b || progress <= 0) return;
    const p = this._screen(a), s = this._size(), down = this._world(b).y > this._world(a).y ? 1 : -1;
    const endpoints = group.map(e => ({ e, p: this._screen(this.byId.get(e.to)) }));
    const sourceX = p.x + (first.out_port || 0) * this.cam.scale, y1 = p.y + down * s.h / 2, y2 = endpoints[0].p.y - down * s.h / 2, mid = (y1 + y2) / 2;
    if (Math.max(y1, y2) < 0 || Math.min(y1, y2) > this.h) return;
    const xs = endpoints.map(({e,p: q}) => q.x + (e.in_port || 0) * this.cam.scale);
    const left = Math.max(-10, Math.min(sourceX, ...xs)), right = Math.min(this.w + 10, Math.max(sourceX, ...xs));
    if (left > right) return;
    ctx.save(); ctx.strokeStyle = active ? this.palette.primary : this.palette.mutedFg; ctx.fillStyle = ctx.strokeStyle;
    ctx.lineWidth = active ? 1.4 : 1.1; ctx.globalAlpha = .8 * this._visibility(`e:${edgeKey(first)}`);
    ctx.setLineDash(first.dashed ? [4, 4] : []); ctx.beginPath();
    const trunk = limit(progress*3,0,1), across = limit(progress*3-1,0,1), tails = limit(progress*3-2,0,1);
    if (sourceX >= -10 && sourceX <= this.w + 10) { ctx.moveTo(sourceX,y1); ctx.lineTo(sourceX,y1+(mid-y1)*trunk); }
    if (across > 0) { ctx.moveTo(Math.max(-10,sourceX+(Math.min(sourceX,...xs)-sourceX)*across),mid); ctx.lineTo(Math.min(this.w+10,sourceX+(Math.max(sourceX,...xs)-sourceX)*across),mid); }
    if (tails > 0) for (const x of xs) if (x >= -10 && x <= this.w+10) { ctx.moveTo(x,mid); ctx.lineTo(x,mid+(y2-mid)*tails); }
    ctx.stroke(); ctx.setLineDash([]);
    const tip = Math.max(3.5, (active ? 8 : 6) * Math.min(1, this.cam.scale));
    if (progress === 1) for (const x of xs) if (x >= 0 && x <= this.w) { ctx.beginPath(); ctx.moveTo(x, y2); ctx.lineTo(x - tip * .4, y2 - down * tip); ctx.lineTo(x + tip * .4, y2 - down * tip); ctx.closePath(); ctx.fill(); }
    ctx.restore();
  }
  _edge(ctx, e, active) {
    const fan = this.routeFlight || this.visibilityFlight ? null : active && !this.focusPending ? this.focusEdgeFans?.get(e) : this.edgeFans?.get(e);
    if (fan) {
      let states = this.drawnFans.get(fan);
      if (!states) { states = new Set(); this.drawnFans.set(fan, states); }
      if (states.has(active)) return;
      states.add(active);
      const rows = new Map();
      // Animation can move one server row into several painted rows. Batch
      // those rows separately; this projects routes, never lays out nodes.
      for (const edge of fan) {
        if (this._edgeMode(edge) !== active || this._displayEdge(edge) !== edge) continue;
        const arrival = this.edgeBorn?.size ? this.edgeBorn.get(pairKey(edge)) : null, y = Math.round(this._screen(this.byId.get(edge.to)).y * 2);
        const row = arrival ? `${y}:${arrival.at}:${arrival.delay}` : y;
        if (!rows.has(row)) rows.set(row, []);
        rows.get(row).push(edge);
      }
      for (const members of rows.values()) {
        if (members.length >= 12) this._edgeFan(ctx, members, active);
        else for (const edge of members) this._singleEdge(ctx, edge, active);
      }
      return;
    }
    this._singleEdge(ctx, e, active);
  }
  _routeFor(e, positions, camera) {
    const a = this.byId.get(e.from), b = this.byId.get(e.to); if (!a || !b) return null;
    const screen = n => { const p = positions?.get(n.id) || n; return { x: this.w / 2 + (p.x - camera.x) * camera.scale, y: this.h / 2 + (p.y - camera.y) * camera.scale * camera.sy }; };
    return this._routePoints(e, screen(a), screen(b), camera);
  }
  _screenRoute(e) {
    const f = this.routeFlight, key = edgeKey(e), a = f?.from.get(key), b = f?.to.get(key);
    if (a && b) {
      const t = smooth(limit(((this.paintAt ?? performance.now()) - f.at) / f.ms, 0, 1)), points = a.map((p, i) => [p[0] + (b[i][0] - p[0]) * t, p[1] + (b[i][1] - p[1]) * t]);
      return f.world ? points.map(p => [this.w / 2 + (p[0] - this.cam.x) * this.cam.scale, this.h / 2 + (p[1] - this.cam.y) * this.cam.scale * this.cam.sy]) : points;
    }
    const from = this.byId.get(e.from), to = this.byId.get(e.to);
    return from && to ? this._routePoints(e, this._screen(from), this._screen(to), this.cam) : null;
  }
  _routePoints(e, p, q, camera) {
    const z = Math.min(1.5, camera.scale), s = { w: 168 * z, h: 48 * z };
    const same = Math.abs(p.y - q.y) < .01, sign = q.x >= p.x ? 1 : -1, down = q.y > p.y; let points;
    if (same) {
      const x1 = p.x + sign * s.w / 2, x2 = q.x - sign * s.w / 2, y1 = p.y + (e.out_port || 0) * camera.scale * camera.sy, y2 = q.y + (e.in_port || 0) * camera.scale * camera.sy;
      points = [[x1,y1], [x2,y2]];
      if (e.blocked) { const lift = Math.min(100, Math.max(50, Math.abs(q.x - p.x) * .22)) * Math.min(1, camera.scale * camera.sy); points.splice(1,0,[(x1+x2)/2,(y1+y2)/2-sign*lift]); }
    } else {
      const x1 = p.x + (e.out_port || 0) * camera.scale, x2 = q.x + (e.in_port || 0) * camera.scale, y1 = p.y + (down ? 1 : -1) * s.h / 2, y2 = q.y - (down ? 1 : -1) * s.h / 2, mid = (y1+y2)/2;
      points = Math.abs(x1-x2)<.1 ? [[x1,y1],[x2,y2]] : [[x1,y1],[x1,mid],[x2,mid],[x2,y2]];
    }
    // A common cubic form lets every route morph continuously, including a
    // horizontal link changing to a vertical link during focus layout.
    if (points.length === 2) { const [a,b] = points; return [a, [a[0]+(b[0]-a[0])/3,a[1]+(b[1]-a[1])/3], [a[0]+(b[0]-a[0])*2/3,a[1]+(b[1]-a[1])*2/3], b]; }
    if (points.length === 3) { const [a,c,b] = points; return [a,[a[0]+(c[0]-a[0])*2/3,a[1]+(c[1]-a[1])*2/3],[b[0]+(c[0]-b[0])*2/3,b[1]+(c[1]-b[1])*2/3],b]; }
    return points;
  }
  _singleEdge(ctx, e, active) {
    const progress = this._edgeProgress(e); if (progress <= 0) return;
    let points = this._screenRoute(e); if (!points) return;
    if (Math.max(...points.map(p => p[0])) < 0 || Math.min(...points.map(p => p[0])) > this.w || Math.max(...points.map(p => p[1])) < 0 || Math.min(...points.map(p => p[1])) > this.h) return;
    // De Casteljau's left curve preserves the actual route and its direction,
    // including upward/backward edges and curves around same-row cards.
    if (progress < 1) {
      let layer = points; const prefix = [layer[0]];
      while (layer.length > 1) { layer = layer.slice(1).map((p,i)=>[layer[i][0]+(p[0]-layer[i][0])*progress,layer[i][1]+(p[1]-layer[i][1])*progress]); prefix.push(layer[0]); }
      points = prefix;
    }
    ctx.beginPath(); ctx.moveTo(...points[0]);
    if (points.length === 2) ctx.lineTo(...points[1]);
    else if (points.length === 3) ctx.quadraticCurveTo(...points[1],...points[2]);
    else ctx.bezierCurveTo(...points[1],...points[2],...points[3]);
    ctx.strokeStyle = active ? this.palette.primary : this.palette.mutedFg; ctx.lineWidth = active ? 1.1 + this.fade.value * .5 : this.cam.scale < .15 ? .8 : 1.1; ctx.globalAlpha = .8 * this._visibility(`e:${edgeKey(e)}`); ctx.setLineDash(e.dashed ? [4, 4] : []); ctx.stroke(); ctx.setLineDash([]);
    if (progress === 1 && e.rel !== "asset_same_system" && e.rel !== "asset_related_to") {
      const [x2,y2] = points[points.length-1], [px,py] = points[points.length-2], angle = Math.atan2(y2-py,x2-px), length = Math.max(3.5, (active ? 8 : 6) * Math.min(1, this.cam.scale)); ctx.beginPath(); ctx.moveTo(x2,y2); ctx.lineTo(x2-length*Math.cos(angle-.45),y2-length*Math.sin(angle-.45)); ctx.lineTo(x2-length*Math.cos(angle+.45),y2-length*Math.sin(angle+.45)); ctx.closePath(); ctx.fillStyle = ctx.strokeStyle; ctx.fill();
    }
    if (e.label && progress === 1 && this.cam.scale >= .55) {
      const weights = points.length === 4 ? [1/8,3/8,3/8,1/8] : points.map(() => 1/points.length);
      const x = points.reduce((sum,p,i)=>sum+p[0]*weights[i],0), y = points.reduce((sum,p,i)=>sum+p[1]*weights[i],0);
      ctx.save(); ctx.font = `11px ${this.fontFamily}`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      const width = ctx.measureText(e.label).width + 12; ctx.fillStyle = this.palette.background; ctx.fillRect(x-width/2,y-9,width,18);
      ctx.fillStyle = this.palette.mutedFg; ctx.fillText(e.label,x,y); ctx.restore();
    }
    ctx.globalAlpha = 1;
  }
  _cardShadow(ctx, s) {
    const key = `${s.w}:${s.h}:${this.dpr}:${this.palette.card}:${this.palette.foreground}`;
    if (this.shadowKey !== key) {
      const layer = document.createElement("canvas"), pad = 20, dpr = this.dpr;
      layer.width = Math.ceil((s.w + pad * 2) * dpr); layer.height = Math.ceil((s.h + pad * 2) * dpr);
      const back = layer.getContext("2d"); back.scale(dpr, dpr); back.shadowColor = `rgba(${panoFadeRGB(this.palette.foreground)},0.07)`; back.shadowBlur = 10 * dpr; back.shadowOffsetY = 3 * dpr;
      this._round(back, pad, pad, s.w, s.h, 12 * s.z); back.fillStyle = this.palette.card; back.fill();
      this.shadowKey = key; this.shadowLayer = layer;
    }
    ctx.drawImage(this.shadowLayer, -s.w / 2 - 20, -s.h / 2 - 20, this.shadowLayer.width / this.dpr, this.shadowLayer.height / this.dpr);
  }
  _card(ctx, n, now, active) {
    const p = this._screen(n), s = this._size(); if (p.x + s.w / 2 < -10 || p.x - s.w / 2 > this.w + 10 || p.y + s.h / 2 < -10 || p.y - s.h / 2 > this.h + 10) return;
    const grow = this._nodeProgress(n.id, now), scale = .96 + .04 * grow;
    ctx.save(); ctx.translate(p.x, p.y); ctx.scale(scale, scale); ctx.globalAlpha = grow * this._visibility(`n:${n.id}`, now);
    const P = this.palette, severity = n.t === "claim" ? vulnColor(P, String(n.sev || "").toLowerCase()) : null, accent = severity || (n.t === "asset" ? P.asset : P.intent);
    if (s.w > 60) this._cardShadow(ctx, s); this._round(ctx, -s.w / 2, -s.h / 2, s.w, s.h, 12 * s.z); ctx.fillStyle = P.card; ctx.fill();
    ctx.save(); ctx.globalAlpha *= severity ? .12 : .16; ctx.fillStyle = accent; ctx.fill(); if (!severity) { ctx.globalAlpha *= 2; ctx.strokeStyle = accent; ctx.lineWidth = 1; ctx.stroke(); } ctx.restore();
    if (n.id === this.focus) { ctx.strokeStyle = P.primary; ctx.lineWidth = 1; ctx.stroke(); }
    if (n.t === "asset" || n.t === "claim") { ctx.fillStyle = accent; ctx.fillRect(-s.w / 2 + 7 * s.z, -s.h / 2, Math.max(0, s.w - 14 * s.z), Math.max(.4, 1.5 * s.z)); }
    ctx.scale(s.z, s.z); ctx.textAlign = "center"; ctx.textBaseline = "alphabetic"; ctx.fillStyle = P.foreground; ctx.font = `12px ${this.fontFamily}`;
    const title = n.t === "step" ? `${n.id} ${n.ti}` : n.ti; this._text(ctx, title, 0, -3, 152);
    ctx.fillStyle = P.mutedFg; ctx.font = `11px ${this.fontFamily}`; const results = this.focus && active && this.focusResults?.has(n.id) ? this.focusResults.get(n.id) : n.results; let sub = n.t === "asset" ? n.hint ? `${n.hint} · ${n.id}` : `${n.id} · 资产` : n.t === "claim" ? `${n.id} · ${n.sev || "未评级"}` : results?.length ? `${results[0].ti}${results?.length > 1 ? ` +${results?.length - 1}` : ""}` : this.focus && active ? "未关联事实" : "未产出事实";
    this._text(ctx, sub, this.focus && active ? -20 : 0, 14, this.focus && active ? 100 : 152);
    if (this.focus && active) { const b = DETAIL_BUTTON, hover = this.hoveredDetail === n.id; this._round(ctx, b.x, b.y, b.w, b.h, 5); ctx.save(); ctx.globalAlpha *= hover ? .22 : .1; ctx.fillStyle = accent; ctx.fill(); ctx.restore(); ctx.save(); ctx.globalAlpha *= hover ? .85 : .45; ctx.strokeStyle = accent; ctx.lineWidth = 1; ctx.stroke(); ctx.restore(); ctx.font = `11px ${this.fontFamily}`; ctx.fillStyle = accent; ctx.textAlign = "center"; ctx.fillText("详情", b.x + b.w / 2, b.y + 12); }
    ctx.restore();
  }
  _font() { return getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim() || "system-ui"; }
  _draw(now) {
    this.paintAt = now;
    const ctx = this.ctx, P = this.palette; ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0); ctx.clearRect(0, 0, this.w, this.h); ctx.fillStyle = P.background; ctx.fillRect(0, 0, this.w, this.h);
    this.fontFamily = this._font();
    this._paintBackground?.(ctx, now);
    this.drawnFans = new Map();
    for (const e of this._paintEdges()) { const mode = this._edgeMode(e); if (mode !== null) this._edge(ctx, e, mode); }
    // Blur one background layer instead of allocating a filtered canvas surface
    // for each card. Active cards and their detail targets remain above it.
    const blur = (this.focus || this.fade.value > 0) && this.active && this.fade.value > .05 && this._size().w > 50;
    if (blur) {
      if (!this.backgroundLayer) { this.backgroundLayer = document.createElement("canvas"); this.backgroundCtx = this.backgroundLayer.getContext("2d"); }
      const layer = this.backgroundLayer, back = this.backgroundCtx;
      if (layer.width !== this.canvas.width || layer.height !== this.canvas.height) { layer.width = this.canvas.width; layer.height = this.canvas.height; }
      back.setTransform(this.dpr, 0, 0, this.dpr, 0, 0); back.clearRect(0, 0, this.w, this.h);
      for (const n of this.nodes) if (!this.active.has(n.id)) this._card(back, n, now, false);
      ctx.save(); ctx.filter = `blur(${.6 * this.fade.value}px)`; ctx.drawImage(layer, 0, 0, this.w, this.h); ctx.restore();
      for (const n of this.nodes) if (this.active.has(n.id)) this._card(ctx, n, now, Boolean(this.focus));
    } else {
      for (const n of this.nodes) if (!this.focus || !this.active?.has(n.id)) this._card(ctx, n, now, false);
      if (this.focus) for (const n of this.nodes) if (this.active?.has(n.id)) this._card(ctx, n, now, true);
    }
    const pad = Math.min(40, this.w / 8), rgb = panoFadeRGB(P.background); for (const [x1, y1, x2, y2, x, y, w, h] of [[0, 0, pad, 0, 0, 0, pad, this.h], [this.w, 0, this.w - pad, 0, this.w - pad, 0, pad, this.h], [0, 0, 0, 24, 0, 0, this.w, 24], [0, this.h, 0, this.h - 32, 0, this.h - 32, this.w, 32]]) { const g = ctx.createLinearGradient(x1, y1, x2, y2); g.addColorStop(0, `rgba(${rgb},1)`); g.addColorStop(1, `rgba(${rgb},0)`); ctx.fillStyle = g; ctx.fillRect(x, y, w, h); }
    this._minimap();
  }
  _minimap() { if (!this.mmCtx) return; const c = this.mmCtx, [x, y, w, h] = this._viewBounds(), sx = 148 / Math.max(1, w), sy = 84 / Math.max(1, h); c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0); c.clearRect(0, 0, 160, 96); c.fillStyle = this.palette.card; c.fillRect(0, 0, 160, 96); for (const n of this.nodes) { if (this.focus && !this.active?.has(n.id)) continue; const pos = this._world(n); c.globalAlpha = this._nodeProgress(n.id); c.fillStyle = n.t === "claim" ? vulnColor(this.palette, n.sev) : n.t === "asset" ? this.palette.asset : this.palette.intent; c.fillRect(6 + (pos.x - x) * sx, 6 + (pos.y - y) * sy, 2, 2); } c.globalAlpha = 1; c.strokeStyle = this.palette.primary; c.lineWidth = 1; c.strokeRect(6 + (this.cam.x - this.w / (2 * this.cam.scale) - x) * sx, 6 + (this.cam.y - this.h / (2 * this.cam.scale * this.cam.sy) - y) * sy, this.w / this.cam.scale * sx, this.h / (this.cam.scale * this.cam.sy) * sy); }
  minimapJump(e) { if (!this.mm) return; const r = this.mm.getBoundingClientRect(), [x, y, w, h] = this._viewBounds(); this._stop(); this.cam.x = x + limit((e.clientX - r.left - 6) / 148, 0, 1) * w; this.cam.y = y + limit((e.clientY - r.top - 6) / 84, 0, 1) * h; this.dirty = true; }
}
