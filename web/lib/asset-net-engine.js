"use client";

import { PanoramaEngine } from "./panorama-engine";

// Share the panorama's continuous camera, fading and input implementation.
// Only solar presentation, picking and fitting differ from the panorama.
export class AssetNetEngine extends PanoramaEngine {
  setHoveredAsset(id) { if (this.hoverAsset !== id) { this.hoverAsset = id; this.dirty = true; } }
  _zoomAt(...args) { this.setHoveredAsset(null); super._zoomAt(...args); }
  _detailButton(n) {
    const p = this._screen(n), radius = (n.primary ? 30 : 12) * Math.min(1.5, this.cam.scale);
    return { x: Math.max(8, Math.min(this.w - 48, p.x + radius + 8)), y: Math.max(8, Math.min(this.h - 36, p.y - 14)), w: 40, h: 28 };
  }
  initialView(instant = false) { this.fitAll(instant); }
  fitAll(instant = false) {
    const [x, y, w, h] = this.bounds, at = performance.now();
    this.returnView = null; this._resetFocus(instant, at);
    this._fly({ x: x + w / 2, y: y + h / 2, scale: Math.max(.001, Math.min((this.w - 80) / w, (this.h - 72) / h, 1.15)), sy: 1 }, instant, at);
    this.cb.onFocus?.(null);
  }
  focusAsset(id, preserveCamera = false) {
    this.setHoveredAsset(null);
    const node = this.byId.get(id); if (!node) return;
    const children = new Map(), parents = new Map();
    for (const e of this.edges) if (e.rel === "sub_of") {
      if (!children.has(e.from)) children.set(e.from, []); children.get(e.from).push(e.to);
      if (!parents.has(e.to)) parents.set(e.to, []); parents.get(e.to).push(e.from);
    }
    const members = new Set([id]), pending = [id];
    for (let i = 0; i < pending.length; i++) for (const child of children.get(pending[i]) || []) if (!members.has(child)) { members.add(child); pending.push(child); }
    const kept = new Set(members);
    // Only actual relations touching this subtree expand the selection. DNS
    // never pulls every sibling service into the focus through a shared host.
    for (const e of this.edges) if (e.rel !== "sub_of") { if (members.has(e.from)) kept.add(e.to); if (members.has(e.to)) kept.add(e.from); }
    const ancestors = [...kept];
    for (let i = 0; i < ancestors.length; i++) for (const parent of parents.get(ancestors[i]) || []) if (!kept.has(parent)) { kept.add(parent); ancestors.push(parent); }
    const ids = [...kept];
    this.enterFocus({ id });
    this.reconcileFocus({ node: { id }, ids, edges: this.edges?.filter(e => kept.has(e.from) && kept.has(e.to)) }, preserveCamera);
  }
  _pick(x, y) {
    const z = Math.min(1.5, this.cam.scale);
    const hovered = this.focus && this.byId.get(this.hoverAsset);
    if (hovered && this.active?.has(hovered.id)) {
      const b = this._detailButton(hovered);
      if (x >= b.x && x <= b.x + b.w && y >= b.y && y <= b.y + b.h) return { n: hovered, detail: true };
    }
    for (let i = (this.nodes?.length || 0) - 1; i >= 0; i--) {
      const n = this.nodes[i]; if (this.focus && !this.active?.has(n.id)) continue;
      const p = this._screen(n), radius = (n.primary ? 30 : 12) * z;
      if (Math.hypot(x - p.x, y - p.y) <= Math.max(9, radius) || (Math.abs(x - p.x) < 76 * z && y > p.y + radius && y < p.y + radius + 34 * z)) return { n, detail: false };
    }
    // Keep the button reachable across the small gap beside the sphere.
    if (hovered && this.active?.has(hovered.id)) {
      const p = this._screen(hovered), b = this._detailButton(hovered), r = (hovered.primary ? 30 : 12) * z;
      if (x >= Math.min(p.x - r, b.x) && x <= Math.max(p.x + r, b.x + b.w) && y >= Math.min(p.y - r, b.y) && y <= Math.max(p.y + r, b.y + b.h)) return { n: hovered, detail: false };
    }
    return null;
  }
  _edge(ctx, e, active) {
    const a = this.byId.get(e.from), b = this.byId.get(e.to); if (!a || !b) return;
    const p = this._screen(a), q = this._screen(b);
    if (Math.max(p.x, q.x) < 0 || Math.min(p.x, q.x) > this.w || Math.max(p.y, q.y) < 0 || Math.min(p.y, q.y) > this.h) return;
    const progress = this._edgeProgress(e);
    const dx = q.x - p.x, dy = q.y - p.y, distance = Math.hypot(dx, dy), z = Math.min(1.5, this.cam.scale);
    const sourceRadius = (a.primary ? 27 : a.systems?.length > 1 ? 12 : 8) * z, targetRadius = (b.primary ? 27 : b.systems?.length > 1 ? 12 : 8) * z;
    if (distance <= sourceRadius + targetRadius || progress <= 0) return;
    const start = { x: p.x + dx / distance * sourceRadius, y: p.y + dy / distance * sourceRadius }, end = { x: q.x - dx / distance * targetRadius, y: q.y - dy / distance * targetRadius };
    ctx.save(); ctx.strokeStyle = active ? this.palette.primary : this.palette.border;
    ctx.globalAlpha = .7 * this._visibility(`e:${e.from}\0${e.to}\0${e.rel}`); ctx.lineWidth = active ? 1.2 : 1;
    ctx.setLineDash(e.rel === "sub_of" ? [] : [4, 4]);
    ctx.beginPath(); ctx.moveTo(start.x, start.y); ctx.lineTo(start.x + (end.x - start.x) * progress, start.y + (end.y - start.y) * progress); ctx.stroke(); ctx.setLineDash([]);
    if (e.rel !== "related_to" && progress === 1) {
      ctx.translate(end.x, end.y); ctx.rotate(Math.atan2(dy, dx)); ctx.fillStyle = ctx.strokeStyle;
      const tip = Math.max(3.5, 6 * Math.min(1, z)); ctx.beginPath(); ctx.moveTo(0, 0); ctx.lineTo(-tip, -tip / 2); ctx.lineTo(-tip, tip / 2); ctx.closePath(); ctx.fill();
    }
    ctx.restore();
  }
  _draw(now) {
    super._draw(now);
    if (!this.focus) return;
    const ctx = this.ctx;
    // Labels sit above the solar glow, which would otherwise erase their ends.
    for (const original of this.edges) {
      const e = this._displayEdge(original);
      if (this.cam.scale < .45 || !e.label || !this._edgeMode(e) || this._edgeProgress(e) !== 1) continue;
      const a = this.byId.get(e.from), b = this.byId.get(e.to); if (!a || !b) continue;
      const p = this._screen(a), q = this._screen(b), x = (p.x + q.x) / 2, y = (p.y + q.y) / 2 - 12;
      if (Math.hypot(q.x - p.x, q.y - p.y) < 100 || x < 32 || x > this.w - 32 || y < 24 || y > this.h - 32) continue;
      ctx.save(); ctx.font = `11px ${this.fontFamily}`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      const width = ctx.measureText(e.label).width;
      ctx.fillStyle = this.palette.background; ctx.fillRect(x - width / 2 - 4, y - 8, width + 8, 16);
      ctx.fillStyle = this.palette.mutedFg; this._text(ctx, e.label, x, y, 100); ctx.restore();
    }
    const hovered = this.byId.get(this.hoverAsset);
    if (hovered && this.active?.has(hovered.id)) {
      const b = this._detailButton(hovered);
      ctx.save(); ctx.fillStyle = this.palette.card; ctx.strokeStyle = this.hoveredDetail === hovered.id ? this.palette.primary : this.palette.border;
      ctx.lineWidth = 1; this._round(ctx, b.x, b.y, b.w, b.h, 8); ctx.fill(); ctx.stroke();
      ctx.font = `12px ${this.fontFamily}`; ctx.textAlign = "center"; ctx.textBaseline = "middle"; ctx.fillStyle = this.palette.foreground;
      ctx.fillText("详情", b.x + b.w / 2, b.y + b.h / 2); ctx.restore();
    }
  }
  _paintBackground(ctx, now) {
    const z = Math.min(1.5, this.cam.scale); if (z <= .2) return;
    // Orbits and glow sit below relations, so they cannot erase an arrow into
    // a primary asset. The accepted solar appearance remains unchanged.
    for (const n of this.nodes) if (n.primary) {
      const p = this._screen(n), radius = 27 * z;
      if (p.x + n.orbit * z < -100 || p.x - n.orbit * z > this.w + 100 || p.y + n.orbit * z < -100 || p.y - n.orbit * z > this.h + 100) continue;
      ctx.save(); ctx.translate(p.x, p.y); ctx.globalAlpha = this._nodeProgress(n.id, now) * this._visibility(`n:${n.id}`, now);
      ctx.strokeStyle = this.palette.border; ctx.lineWidth = .7; ctx.globalAlpha *= .55;
      for (let r = 134; r <= n.orbit; r += 112) { ctx.beginPath(); ctx.ellipse(0, 0, r * z, r * z * .82, 0, 0, Math.PI * 2); ctx.stroke(); }
      ctx.globalAlpha /= .55;
      const glow = ctx.createRadialGradient(0, 0, radius * .5, 0, 0, radius * 2.8); glow.addColorStop(0, this.palette.card); glow.addColorStop(1, this.palette.background);
      ctx.fillStyle = glow; ctx.beginPath(); ctx.arc(0, 0, radius * 2.8, 0, Math.PI * 2); ctx.fill();
      ctx.restore();
    }
  }
  _card(ctx, n, now, active) {
    const p = this._screen(n), z = Math.min(1.5, this.cam.scale), radius = (n.primary ? 27 : n.systems?.length > 1 ? 12 : 8) * z;
    if (p.x < -120 || p.x > this.w + 120 || p.y < -100 || p.y > this.h + 100) return;
    const color = n.scope_state === "pending" ? this.palette.warning : n.excluded || n.scope_state === "out_open" ? this.palette.mutedFg : n.covered ? this.palette.asset : this.palette.mutedFg;
    ctx.save(); ctx.translate(p.x, p.y); ctx.globalAlpha = this._nodeProgress(n.id, now) * this._visibility(`n:${n.id}`, now);
    const opacity = ctx.globalAlpha;
    if (!n.covered && !n.excluded && n.scope_state !== "pending") ctx.globalAlpha *= .35;
    ctx.fillStyle = n.primary ? this.palette.card : color; ctx.strokeStyle = color; ctx.lineWidth = n.primary ? 1.6 : 1;
    ctx.beginPath(); ctx.arc(0, 0, radius, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
    if (n.primary) { ctx.fillStyle = color; ctx.beginPath(); ctx.arc(0, 0, radius * .24, 0, Math.PI * 2); ctx.fill(); }
    ctx.globalAlpha = opacity;
    if (this.focus === n.id) { ctx.strokeStyle = this.palette.primary; ctx.lineWidth = 1; ctx.beginPath(); ctx.arc(0, 0, radius + 7 * z, 0, Math.PI * 2); ctx.stroke(); }
    // Subpixel labels add paint cost without readable information. Keep the
    // actual nodes and links; hover and zoom still reveal their full identity.
    if (z < .25 && !n.primary && !active) { ctx.restore(); return; }
    ctx.scale(z, z); ctx.textAlign = "center"; ctx.textBaseline = "top"; ctx.fillStyle = n.primary ? this.palette.foreground : this.palette.mutedFg;
    ctx.font = `${n.primary ? "500 13" : "12"}px ${this.fontFamily}`;
    this._text(ctx, n.ti, 0, radius / z + 12, 152);
    if (n.primary || active) { ctx.font = `10px ${this.fontFamily}`; ctx.fillStyle = this.palette.mutedFg; this._text(ctx, n.scope_state === "pending" ? "待批" : n.excluded ? "范围外" : n.id, 0, radius / z + 30, 152); }
    ctx.restore();
  }
}
