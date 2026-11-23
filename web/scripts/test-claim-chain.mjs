import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { claimChainLayout } from "../lib/claim-chain-layout.mjs";
const panoSource = await readFile(new URL("../lib/panorama-engine.js", import.meta.url), "utf8");
const panoURL = `data:text/javascript;base64,${Buffer.from(panoSource).toString("base64")}`;
const engineSource = (await readFile(new URL("../lib/claim-chain-engine.js", import.meta.url), "utf8")).replace('"./panorama-engine"', JSON.stringify(panoURL));
const { ClaimChainEngine } = await import(`data:text/javascript;base64,${Buffer.from(engineSource).toString("base64")}`);
for (const count of [0, 1, 3, 60, 901]) {
  const nodes = Array.from({ length: count }, (_, i) => ({ id: `n${i}`, t: i === count - 1 ? "claim" : "step", x: i * 5000, y: i * 120, results: i === 0 ? [{ id: "f1", ti: "result" }, { id: "f2", ti: "second" }] : [] }));
  const edges = nodes.slice(1).map((n, i) => ({ from: nodes[i].id, to: n.id, rel: "sourced_from", refs: ["f1"], out_port: 0, in_port: 0 }));
  if (count > 3) edges.push({ from: "n0", to: `n${count - 1}`, rel: "derived_from", refs: ["f2"], out_port: 24, in_port: -24 });
  const skeleton = { rev: 4, nodes: [...nodes, { id: "unrelated", t: "asset" }], aliases: { f1: ["n0"], f2: ["n0"], unrelated: ["unrelated"] } };
  const links = { ids: nodes.map(n => n.id), positions: nodes.map((n, i) => ({ id: n.id, x: (i % 4) * 192, y: Math.floor(i / 4) * 80 })), edges };
  const before = structuredClone({ skeleton, links }), result = claimChainLayout(skeleton, links);
  assert.deepEqual({ skeleton, links }, before, "panorama input never mutates");
  assert.equal(result.nodes.length, count);
  assert.deepEqual(result.edges.map(({ from, to, rel, refs }) => ({ from, to, rel, refs })), edges.map(({ from, to, rel, refs }) => ({ from, to, rel, refs })), "all actual directions and references preserved");
  assert.equal(result.nodes.some(n => n.t === "fact"), false);
  if (count) { assert.deepEqual(result.nodes[0].results, nodes[0].results); assert.deepEqual(result.aliases.f1, ["n0"]); }
  for (let i = 0; i < result.nodes.length; i++) for (let j = i + 1; j < result.nodes.length; j++) {
    const a = result.nodes[i], b = result.nodes[j];
    assert.ok(Math.abs(a.x - b.x) >= 168 || Math.abs(a.y - b.y) >= 48, "wrapped columns do not overlap");
  }
}
assert.throws(() => claimChainLayout({ nodes: [] }, { ids: ["missing"] }));
const e = Object.create(ClaimChainEngine.prototype);
Object.assign(e, { nodes: [{ id: "s", t: "step", x: 0, y: 0 }, { id: "c", t: "claim", x: 288, y: 96 }], edges: [{ from: "s", to: "c", rel: "derived_from", out_port: 6, in_port: -6 }], aliases: { f: ["s"] }, cam: { x: 100, y: 20, scale: 1, sy: 1 }, w: 640, h: 288, born: new Map(), edgeBorn: new Map(), positions: new Map(), focusEdges: new Map(), reduced: { matches: false }, cb: {}, fade: { value: 0 }, palette: { primary: "red" } });
e.byId = new Map(e.nodes.map(n => [n.id, n]));
e.select("f");
assert.equal(e.focus, "s");
assert.equal(e._edgeMode(e.edges[0]), true);
const camera = { ...e.cam };
e.select("c"); assert.deepEqual(e.cam, camera, "selection keeps viewport");
const commands = [];
const ctx = new Proxy({}, { get: (_, name) => (...args) => commands.push([name, ...args]), set: () => true });
for (const [dx, dy] of [[288, 0], [-288, 0], [288, 96], [-288, -96], [0, 96]]) {
  e.nodes[1].x = dx; e.nodes[1].y = dy;
  const before = structuredClone(e.nodes); commands.length = 0;
  e._edge(ctx, e.edges[0], true);
  assert.deepEqual(e.nodes, before);
  assert.ok(commands.some(c => c[0] === "transform"));
  assert.ok(commands.some(c => c[0] === "fill"), "source-directed arrow rendered in each orientation");
}
// A live snapshot uses the inherited arrivals and never resets a browsed camera.
e.fitted = true; e.born = new Map(); e.edgeBorn = new Map(); e.focusEdgeFans = new Map();
e.bounds = [0, 0, 600, 200]; e.arrivalWait = null;
const nextNodes = [...e.nodes, { id: "new", t: "step", x: 576, y: 0 }];
const nextEdges = [...e.edges, { from: "c", to: "new", rel: "sourced_from" }];
const browsed = { ...e.cam };
e.setData({ nodes: nextNodes, edges: nextEdges, aliases: { f: ["s"] }, bounds: e.bounds });
e.select("c");
assert.deepEqual(e.cam, browsed);
assert.ok(e.born.has("new"));
assert.ok(e.edgeBorn.has("c\0new"));
const at = e.born.get("new");
assert.equal(e._nodeProgress("new", at), 0);
assert.ok(Math.abs(e._nodeProgress("new", at + 300) - .5) < 1e-9);
assert.equal(e._nodeProgress("new", at + 600), 1);
assert.equal(e.arrivalWait, null, "snapshot releases the arrival batch");
e.setData({ nodes: nextNodes, edges: nextEdges, aliases: { f: ["s"] }, bounds: e.bounds });
assert.equal(e.born.get("new"), at, "unchanged refresh does not restart arrivals");
e.bounds = [0, 0, 1600, 160]; e.w = 520; e.h = 288; e.cb.initialFocus = "c";
e._fly = target => { e.cam = target; };
e.initialView(true); assert.equal(e.cam.scale, .75, "initial viewport keeps card text readable");
e.fitAll(true); assert.ok(e.cam.scale < .75, "all-chain fit is a separate explicit action");
const source = await readFile(new URL("../components/claim-chain-graph.js", import.meta.url), "utf8");
assert.ok(!source.includes("/graph") && !source.includes("@xyflow"));
assert.ok(source.includes("pview/links/") && source.includes("pview/skeleton"));
console.log("攻击链小图：同源截取、横向布局、多来源、合并事实、方向、视野及缺失数据检查通过");
