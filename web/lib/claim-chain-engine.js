import { PanoramaEngine } from "./panorama-engine";

// Cards, picking, input, camera and arrivals remain the panorama implementation.
// Transpose only the edge drawing space so its existing route/growth algorithm
// connects horizontal layers without rotating the cards or their text.
export class ClaimChainEngine extends PanoramaEngine {
  initialView(instant = false) { this._chainView(false, instant); }
  fitAll(instant = false) { this._chainView(true, instant); }
  _chainView(all, instant) {
    const [x, y, w, h] = this.bounds;
    const fit = (this.w - 48) / w;
    const scale = Math.max(.001, Math.min(all ? fit : Math.max(.75, fit), (this.h - 48) / h, 1));
    const claim = this.byId.get(this.cb.initialFocus);
    const center = !all && claim && scale > fit ? Math.min(x + w - this.w / (2 * scale), Math.max(x + this.w / (2 * scale), claim.x)) : x + w / 2;
    this._fly({ x: center, y: y + h / 2, scale, sy: 1 }, instant);
  }
  _edge(ctx, e, active) {
    const projection = Object.create(this);
    projection.w = this.h; projection.h = this.w;
    projection._screen = n => { const p = this._screen(n); return { x: p.y, y: p.x }; };
    projection._size = () => { const s = this._size(); return { ...s, w: s.h, h: s.w }; };
    ctx.save();
    ctx.transform(0, 1, 1, 0, 0, 0);
    super._singleEdge.call(projection, ctx, e, active);
    ctx.restore();
  }
  select(id) {
    const selected = this.resolve(id);
    if (!selected) return;
    this.focus = selected; this.focusPending = false;
    this.active = new Set(this.nodes?.map(n => n.id));
    this.focusEdges = new Map(this.edges?.map(e => [`${e.from}\0${e.to}\0${e.rel}`, e]));
    this.resumeArrivals(); this.dirty = true;
  }
}
