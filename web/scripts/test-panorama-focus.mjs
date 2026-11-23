import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("../lib/panorama-engine.js", import.meta.url), "utf8");
const { PanoramaEngine, samePanoramaChain } = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
function engine(reduced = false) {
  const e = Object.create(PanoramaEngine.prototype);
  const nodes = [{ id: "a1", t: "asset", x: 5000, y: 20 }, { id: "s1", t: "step", x: 50000, y: 158 }, { id: "tc1", t: "claim", x: 90000, y: 398 }, { id: "other", t: "step", x: 100000, y: 158 }];
  Object.assign(e, { nodes, byId: new Map(nodes.map(n => [n.id, n])), aliases: { f1: ["s1"] },
    edges: [{ from: "a1", to: "s1", rel: "sourced_from" }, { from: "s1", to: "tc1", rel: "produces" }],
    incoming: new Map([["s1", ["a1"]], ["tc1", ["s1"]]]), outgoing: new Map([["a1", ["s1"]], ["s1", ["tc1"]]]),
    positions: new Map(), positionFlight: null, focusEdges: new Map(), focusBounds: null,
    focus: null, focusPending: false, returnView: null, active: null,
    cam: { x: 40000, y: 260, scale: .8, sy: 1.2 }, bounds: [0, 0, 110000, 500], w: 1152, h: 720,
    reduced: { matches: reduced }, cb: {}, fade: { value: 0 }, velocity: { x: 0, y: 0 } });
  return e;
}
function view(focus = "s1", offset = 0) {
  return { node: { id: focus }, ids: ["a1", "s1", "tc1"], bounds: [-108 + offset, -48, 216, 336],
    positions: [{ id: "a1", x: offset, y: 0 }, { id: "s1", x: offset, y: 120 }, { id: "tc1", x: offset, y: 240 }],
    edges: [{ from: "a1", to: "s1", rel: "sourced_from", out_port: 0, in_port: 0 }, { from: "s1", to: "tc1", rel: "produces", out_port: 0, in_port: 0 }] };
}
function complete(e) { if (e.flight) e.cam = { ...e.flight.to }; e.flight = null; e.positionFlight = null; e.fade.value = e.fade.to; }

const oneChain = { ids: ["a", "b", "c"], edges: [{ from: "a", to: "b" }, { from: "b", to: "c" }] };
assert.ok(samePanoramaChain(oneChain, { ids: ["c", "a", "b"], edges: [...oneChain.edges].reverse() }));
assert.equal(samePanoramaChain(oneChain, { ids: oneChain.ids, edges: [{ from: "b", to: "a" }, { from: "c", to: "b" }] }), false, "opposite direction is another view");
const fork = { ids: ["a", "b", "c"], edges: [{ from: "a", to: "b" }, { from: "a", to: "c" }] };
assert.equal(samePanoramaChain(fork, fork), false, "a branch junction retains navigation history");
const cyclic = { ids: ["a", "b", "c"], edges: [{ from: "a", to: "b" }, { from: "b", to: "a" }] };
assert.equal(samePanoramaChain(cyclic, cyclic), false);
assert.equal(samePanoramaChain({...oneChain,results:{b:[{id:"fact-one"}]}}, {...oneChain,results:{b:[{id:"fact-two"}]}}), false, "two exact fact selections retain separate navigation history even when their cards are aliases");
const e = engine(), original = structuredClone(e.nodes), originalCamera = { ...e.cam };
assert.ok(e.enterFocus({ id: "f1" }));
assert.equal(e.focus, "s1", "fact alias resolves to its intent");
assert.deepEqual([...e.active], ["s1"], "pending reads cannot briefly expose an unlimited alias/descendant expansion");
e.reconcileFocus(view());
assert.equal(e.positions.size, 3);
assert.ok(e.positionFlight && e.flight, "nodes and camera move together");
const n = e.byId.get("s1"), motion = e.positionFlight;
assert.deepEqual(e._world(n, motion.at), { x: n.x, y: n.y });
assert.equal(motion.at, e.flight.at, "camera and nodes share one transition clock");
const mid = e._world(n, motion.at + motion.ms / 2), from = e.flight.from, to = e.flight.to;
const midCamera = { x: (from.x + to.x) / 2, y: (from.y + to.y) / 2, scale: (from.scale + to.scale) / 2, sy: (from.sy + to.sy) / 2 };
const expectedX = ((n.x - from.x) * from.scale + (0 - to.x) * to.scale) / 2;
assert.ok(Math.abs((mid.x - midCamera.x) * midCamera.scale - expectedX) < .00001, "screen motion is monotonic instead of flying away during zoom");
assert.deepEqual(e._world(n, motion.at + motion.ms), { x: 0, y: 120 });
assert.deepEqual(e.nodes, original, "overview coordinates remain immutable");
assert.deepEqual(e.returnView, originalCamera);
complete(e);
assert.ok(e.cam.scale > 1, "sparse relations fit as readable cards");
for (const scale of [.5, 1, 1.5]) {
  e.cam.scale = scale;
  const p = e._screen(n), z = e._size().z * 1.025;
  assert.ok(e._pick(p.x + 58 * z, p.y + 12 * z)?.detail, "button picking follows moved/scaled card");
  assert.equal(e._pick(p.x + 39 * z, p.y + 12 * z)?.detail, false, "card body remains navigation");
}
const browsedCamera = { ...e.cam };
e.reconcileFocus(view("s1", 100), true);
assert.deepEqual(e.cam, browsedCamera, "live refresh keeps user camera");
assert.deepEqual(e.flight?.to, browsedCamera, "layout refresh does not change camera target");
assert.equal(e.positions.get("s1").x, 100, "same membership can still update local positions");
complete(e);
e.enterFocus({ id: "tc1" });
e.reconcileFocus(view("tc1", 200));
assert.deepEqual(e.returnView, originalCamera, "switching focus must not overwrite return view");
const chosen = e.positions;
e.reconcileFocus(view("s1", 999));
assert.equal(e.positions, chosen, "stale response ignored");
e.exitFocus();
assert.equal(e.focus, null);
assert.equal(e.positions.size, 0);
assert.deepEqual(e.flight.to, originalCamera);
assert.deepEqual(e._world(n, e.positionFlight.at + e.positionFlight.ms), { x: n.x, y: n.y });
complete(e);
assert.deepEqual(e.cam, originalCamera);
for (const command of ["initialView", "fitAll"]) {
  e.enterFocus({ id: "s1" }); e.reconcileFocus(view()); complete(e); e[command](true);
  assert.equal(e.positions.size, 0, `${command} restores original coordinates`);
  assert.equal(e.positionFlight, null);
  assert.equal(e.focus, null);
}
const interrupted = engine();
interrupted.enterFocus({ id: "s1" }); interrupted.reconcileFocus(view());
const moving = interrupted.positionFlight, cameraFlight = interrupted.flight;
interrupted.paintAt = moving.at + moving.ms * .4;
const progress = .4 * .4 * (3 - 2 * .4);
for (const key of ["x", "y", "scale", "sy"]) interrupted.cam[key] = cameraFlight.from[key] + (cameraFlight.to[key] - cameraFlight.from[key]) * progress;
const painted = interrupted._screen(interrupted.byId.get("s1"));
interrupted._stop();
assert.deepEqual(interrupted._screen(interrupted.byId.get("s1")), painted, "interrupting a camera flight keeps the painted position");
assert.equal(interrupted.positionFlight.cameraFrom, undefined, "manual navigation detaches remaining node motion from the old camera");
assert.deepEqual(interrupted._world(interrupted.byId.get("s1"), moving.at + moving.ms), { x: 0, y: 120 });
const reduced = engine(true);
reduced.enterFocus({ id: "s1" }); reduced.reconcileFocus(view());
assert.equal(reduced.positionFlight, null);
assert.equal(reduced.flight, null);
assert.deepEqual(reduced._world(reduced.byId.get("s1")), { id: "s1", x: 0, y: 120 });
const restored = engine();
restored.enterFocus({ id: "s1" }); restored.reconcileFocus(view()); complete(restored);
const previousCamera = { x: 120.5, y: 186.25, scale: .95, sy: 1 };
restored.reconcileFocus(view("s1", 50), true, previousCamera);
assert.deepEqual(restored.flight.to, previousCamera, "returning restores the previous layer camera instead of fitting again");
restored.exitFocus(previousCamera);
assert.deepEqual(restored.flight.to, previousCamera, "overview back accepts the saved view");
const textEngine = engine();
let measurements = 0, shown;
const context = { font: "12px system-ui", measureText: value => { measurements++; return { width: value.length * 12 }; }, fillText: value => { shown = value; } };
textEngine._text(context, "长标题".repeat(300), 0, 0, 152);
assert.ok(shown.length * 12 <= 152);
assert.ok(measurements < 15, "long titles use bounded text measurement");
const count = measurements;
textEngine._text(context, "长标题".repeat(300), 0, 0, 152);
assert.equal(measurements, count, "panning reuses measured text");
textEngine._text(context, "长标题".repeat(300), 0, 0, 100);
assert.ok(measurements > count, "different available widths have independent cache entries");
const returned = engine();
returned._draw = () => {}; returned._viewBounds = () => returned.bounds;
returned.born = new Map(); returned.lastNotify = performance.now(); returned.fade = { value: 0, to: 0 };
returned.velocity = { x: 0, y: 0 }; returned.destroyed = false;
returned.flight = { from: { x: 90000.2, y: 12345.6, scale: 1.5, sy: .5 }, to: { x: 12345.6789, y: 234.56789, scale: .7171717, sy: 1.2 }, at: 10, ms: 250 };
const exact = { ...returned.flight.to };
globalThis.requestAnimationFrame = () => 0;
returned._loop(260);
assert.deepEqual(returned.cam, exact, "final animation frame restores exact camera values");
// Arrow direction follows the actual target even when it is above or to the left.
for (const [name, a, b, direction, blocked] of [
  ["down", [-120, -100], [120, 100], [0, 1], false],
  ["up", [-120, 100], [120, -100], [0, -1], false],
  ["right", [-200, 0], [200, 0], [1, 0], false],
  ["left", [200, 0], [-200, 0], [-1, 0], false],
  ["blocked-right", [-200, 0], [200, 0], [1, 0], true],
  ["blocked-left", [200, 0], [-200, 0], [-1, 0], true],
]) {
  const drawing = engine(); drawing.cam = { x: 0, y: 0, scale: 1, sy: 1 };
  drawing.palette = { primary: "#000000", mutedFg: "#888888" };
  drawing.byId = new Map([["source", { id: "source", x: a[0], y: a[1] }], ["target", { id: "target", x: b[0], y: b[1] }]]);
  let path = [], head, strokes = 0, fills = 0;
  const ctx = { beginPath() { path = []; }, moveTo(x, y) { path.push([x, y]); }, lineTo(x, y) { path.push([x, y]); }, quadraticCurveTo() {}, bezierCurveTo() {}, closePath() {}, setLineDash() {}, stroke() { strokes++; }, fill() { fills++; head = path; } };
  drawing._singleEdge(ctx, { from: "source", to: "target", blocked, out_port: -12, in_port: 12 }, false);
  assert.equal(strokes, 1, `${name}: a source/target pair draws one path`);
  assert.equal(fills, 1, `${name}: one arrowhead`);
  const expectedTip = a[1] === b[1] ? [drawing.w / 2 + b[0] - direction[0] * 84, drawing.h / 2 + b[1] + 12] : [drawing.w / 2 + b[0] + 12, drawing.h / 2 + b[1] - direction[1] * 24];
  assert.deepEqual(head[0], expectedTip, `${name}: renderer uses assigned incoming port`);
  const dx = head[0][0] - (head[1][0] + head[2][0]) / 2, dy = head[0][1] - (head[1][1] + head[2][1]) / 2;
  assert.ok(dx * direction[0] + dy * direction[1] > 0, `${name}: arrow points into target`);
}
const filtered = engine(); filtered.fitted = true; filtered.born = new Map();
filtered.enterFocus({ id: "s1" }); filtered.reconcileFocus(view()); complete(filtered);
assert.equal(filtered._edgeMode(filtered.edges[0]), true, "selected path is highlighted");
assert.equal(filtered._edgeMode({ from: "a1", to: "tc1", rel: "sourced_from" }), false, "a shortcut between active nodes stays in the background");
assert.equal(filtered._edgeMode({ from: "other", to: "s1", rel: "sourced_from" }), false, "outside branches remain background context");
assert.deepEqual([...filtered._paintEdges()].map(e => [e.from,e.to,e.rel]), filtered.edges.map(e => [e.from,e.to,e.rel]), "focus keeps the same graph, including background paths");
const entry = {from:"a1",to:"s1",rel:"covers"};
filtered.reconcileFocus({...view(), edges:[entry,...view().edges.slice(1)]});
assert.equal([...filtered._paintEdges()].some(e => e.rel === "covers"), false, "focus cannot invent an entry absent from the overview");
filtered.enterFocus({id:"tc1"});
assert.equal([...filtered._paintEdges()].length,filtered.edges.length,"pending scope retains the painted graph");
filtered.reconcileFocus(view("tc1"));complete(filtered);
filtered.enterFocus({id:"s1"});filtered.reconcileFocus(view());complete(filtered);
const filteredCamera = { ...filtered.cam };
filtered.routeFlight = null; filtered.visibilityFlight = null;
filtered.setData({ nodes: [...filtered.nodes, { id: "new-source", t: "asset", x: -5000, y: 20 }], edges: [...filtered.edges, { from: "new-source", to: "s1", rel: "sourced_from" }] });
assert.equal(filtered.active.has("new-source"), false, "live skeleton data does not expand a selected branch before its projection arrives");
filtered.reconcileFocus({ ...view(), ids: [...view().ids, "new-source"], positions: [...view().positions, { id: "new-source", x: -192, y: 0 }] }, true);
assert.ok(filtered.active.has("new-source"));
filtered.reconcileFocus({...view(),results:{s1:[{id:"only-referenced",ti:"实际引用"}]}});
assert.deepEqual(filtered.focusResults.get("s1"),[{id:"only-referenced",ti:"实际引用"}],"focused cards use the projection's exact results");
filtered.exitFocus();
assert.equal(filtered.focusResults.size,0,"returning to overview restores full original results");
assert.deepEqual(filtered.cam, filteredCamera, "live branch refresh keeps camera");
const initialGroup = engine();
initialGroup.nodes = [{ id: "root-a", t: "asset", x: 5000 }, { id: "root-b", t: "asset", x: 9000 }, ...Array.from({ length: 3 }, (_, i) => ({ id: `group${i}`, t: "step", root: "root-a" })), { id: "small-group", t: "step", root: "root-b" }];
initialGroup.edges = [];
assert.equal(initialGroup._initialX(55000, 1), 5000, "initial view uses the largest real display group without needing coverage lines");
const branching = engine(); branching.fitted = true; branching.born = new Map();
const branchNodes = [{ id: "source", t: "step", x: 0, y: 0 }, ...Array.from({ length: 20 }, (_, i) => ({ id: `target${i}`, t: "step", x: (i + 1) * 200, y: 0 }))];
const branchEdges = branchNodes.slice(1).map(n => ({ from: "source", to: n.id, rel: "sourced_from" }));
branching.setData({ nodes: branchNodes, edges: branchEdges });
assert.equal(branching.edgeFans.size, 0, "same-row lineage keeps horizontal routes");
branching.setData({ nodes: branchNodes.map(n => ({ ...n, y: n.id === "source" ? 0 : 150 })), edges: branchEdges });
assert.equal(branching.edgeFans.size, 20, "large source branches share drawing without dropping targets");
const animatedFans = engine(); animatedFans.fitted = true; animatedFans.born = new Map();
const movingNodes = [{ id: "source", t: "asset", x: 0, y: 0 }, ...Array.from({ length: 60 }, (_, i) => ({ id: `moving${i}`, t: "step", x: i * 200, y: 150 }))];
const movingEdges = movingNodes.slice(1).map(n => ({ from: "source", to: n.id, rel: "sourced_from" }));
animatedFans.setData({ nodes: movingNodes, edges: movingEdges });
animatedFans.positions = new Map(movingNodes.slice(1).map((n,i) => [n.id, { x: n.x, y: 150 + (i % 3) * 100 }]));
for (const focused of [true, false]) {
  animatedFans.focusEdges = new Map(movingEdges.map(e => [`${e.from}\0${e.to}\0${e.rel}`, e]));
  animatedFans.focus = focused ? "source" : null; animatedFans.focusPending = focused; animatedFans.active = new Set(movingNodes.map(n => n.id)); animatedFans.drawnFans = new Map();
  const drawn = [], batches = []; animatedFans._singleEdge = (_, edge) => drawn.push(edge.to); animatedFans._edgeFan = (_, group) => { batches.push(group.length); drawn.push(...group.map(e => e.to)); };
  for (const edge of movingEdges) animatedFans._edge({}, edge, focused);
  assert.equal(drawn.length, 60); assert.equal(new Set(drawn).size, 60);
  assert.deepEqual(batches, [20,20,20], "moving row groups retain batching on enter and exit");
}
animatedFans.focus = "source"; animatedFans.focusPending = true; animatedFans.drawnFans = new Map();
const changedRoute = { ...movingEdges[0], out_port: 12 };
animatedFans.focusEdges = new Map(movingEdges.map(e => [`${e.from}\0${e.to}\0${e.rel}`, e === movingEdges[0] ? changedRoute : e]));
const once = []; animatedFans._singleEdge = (_, edge) => once.push(edge.to); animatedFans._edgeFan = (_, group) => once.push(...group.map(e => e.to));
for (const edge of movingEdges) animatedFans._edge({}, animatedFans._displayEdge(edge), true);
assert.equal(once.length, 60); assert.equal(new Set(once).size, 60, "a route from the prior view is not drawn twice during navigation");
const fanDrawing = engine(); fanDrawing.cam = { x: 0, y: 0, scale: 1, sy: 1 };
fanDrawing.palette = { primary: "#000000", mutedFg: "#888888" };
fanDrawing.byId = new Map([["source", { id: "source", x: 0, y: 0 }], ["left", { id: "left", x: -100, y: 150 }], ["right", { id: "right", x: 100, y: 150 }]]);
const fanMoves = [], fanCtx = { save() {}, restore() {}, beginPath() {}, moveTo(x, y) { fanMoves.push([x, y]); }, lineTo() {}, stroke() {}, fill() {}, closePath() {}, setLineDash() {} };
fanDrawing._edgeFan(fanCtx, [{ from: "source", to: "left", out_port: 24 }, { from: "source", to: "right", out_port: 36 }], false);
assert.deepEqual(fanMoves[0], [fanDrawing.w / 2 + 24, fanDrawing.h / 2 + 24], "batched branches respect the reserved outgoing port");
// Live arrivals are distinct from navigation and from the initial page load.
const arrivals = engine(); arrivals.born = new Map(); arrivals.fitted = false;
const seed = { nodes: [{ id: "seed", t: "asset", x: 0, y: 0 }], edges: [] };
arrivals.setData(seed);
assert.equal(arrivals.born.size,0,"first load does not replay births");
const fresh = { nodes: [...seed.nodes,{id:"new",t:"step",x:200,y:150}], edges:[{from:"seed",to:"new",rel:"sourced_from"}] };
arrivals.setData(fresh);
const nodeAt = arrivals.born.get("new"), edgeBirth = arrivals.edgeBorn.get("seed\0new");
assert.equal(edgeBirth.delay,600);
assert.equal(arrivals._nodeProgress("new",nodeAt),0);
assert.ok(Math.abs(arrivals._nodeProgress("new",nodeAt+300)-.5)<1e-10);
assert.equal(arrivals._nodeProgress("new",nodeAt+600),1);
for(const [elapsed,expected] of [[0,0],[600,0],[1000,.5],[1400,1]]) { arrivals.paintAt=edgeBirth.at+elapsed; assert.ok(Math.abs(arrivals._edgeProgress(fresh.edges[0])-expected)<1e-10); }
arrivals.setData({...fresh,edges:[{...fresh.edges[0],rel:"derived_from"}]});
assert.equal(arrivals.born.get("new"),nodeAt,"repeated skeleton reads do not restart node fade");
assert.equal(arrivals.edgeBorn.get("seed\0new"),edgeBirth,"representative relation changes do not replay the same directed pair");
arrivals.focus="seed";arrivals.active=new Set(["seed"]);
const delayed={nodes:[...fresh.nodes,{id:"waiting",t:"step",x:400,y:150}],edges:[...fresh.edges,{from:"seed",to:"waiting",rel:"sourced_from"}]};
arrivals.setData(delayed); const waitAt=arrivals.arrivalWait;
assert.equal(arrivals._nodeProgress("waiting",waitAt+5000),0,"slow scope projection does not consume the new card fade");
arrivals.resumeArrivals(waitAt+5000);
assert.equal(arrivals._nodeProgress("waiting",waitAt+5000),0);
assert.ok(Math.abs(arrivals._nodeProgress("waiting",waitAt+5300)-.5)<1e-10);
assert.equal(arrivals.born.get("new"),nodeAt,"another batch never restarts older cards");
arrivals.reduced.matches=true;
assert.equal(arrivals._nodeProgress("waiting",waitAt+5000),1);
assert.equal(arrivals._edgeProgress(delayed.edges[1]),1,"reduced motion shows the final connection immediately");
// Every supported curve is truncated from its real source, in either direction.
for(const [name,a,b,blocked] of [["down",[-120,-100],[120,100],false],["up",[-120,100],[120,-100],false],["right",[-200,0],[200,0],false],["left",[200,0],[-200,0],false],["arc",[-200,0],[200,0],true]]) {
 const drawing=engine();drawing.cam={x:0,y:0,scale:1,sy:1};drawing.palette={primary:"#000000",mutedFg:"#888888"};drawing.byId=new Map([["a",{id:"a",x:a[0],y:a[1]}],["b",{id:"b",x:b[0],y:b[1]}]]);
 const e={from:"a",to:"b",rel:"sourced_from",blocked};drawing.edgeBorn=new Map([["a\0b",{at:0,delay:0}]]);
 let start,end,heads=0;const ctx={beginPath(){},moveTo(x,y){start=[x,y]},lineTo(x,y){end=[x,y]},quadraticCurveTo(...p){end=p.slice(-2)},bezierCurveTo(...p){end=p.slice(-2)},closePath(){},setLineDash(){},stroke(){},fill(){heads++}};
 drawing.paintAt=400;drawing._singleEdge(ctx,e,false);
 const expectedStart=a[1]===b[1]?[drawing.w/2+a[0]+(b[0]>a[0]?84:-84),drawing.h/2+a[1]]:[drawing.w/2+a[0],drawing.h/2+a[1]+(b[1]>a[1]?24:-24)];
 assert.deepEqual(start,expectedStart,`${name}: growing route begins at source`);
 assert.ok(Math.abs(end[0]-(drawing.w/2+(a[0]+b[0])/2))<.001,`${name}: half curve ends halfway across`);
 assert.equal(heads,0,"arrow appears only after the route reaches its target");
 drawing.paintAt=800;drawing._singleEdge(ctx,e,false);assert.equal(heads,1);
}
// Stable fan branches remain batched independently from a fresh batch.
const split=engine();split.fitted=true;split.born=new Map();split.setData({nodes:movingNodes,edges:movingEdges});split.edgeBorn.clear();split.born.clear();
for(const edge of movingEdges.slice(30))split.edgeBorn.set(`${edge.from}\0${edge.to}`,{at:0,delay:600});
split.paintAt=1000;split.drawnFans=new Map();const batches=[];split._edgeFan=(_,group)=>batches.push([group.length,split._edgeProgress(group[0])]);
for(const edge of movingEdges)split._edge({},edge,false);
assert.deepEqual(batches,[[30,1],[30,.5]],"old branches do not disappear or replay when a new batch grows");
console.log("panorama-focus: 聚拢插值、别名、换焦点、实时刷新、点击与还原类级检查通过");
// Navigation preserves painted geometry and background opacity at every boundary.
const continuous = engine();
continuous.edges.push({from:"other",to:"tc1",rel:"sourced_from",dashed:true});
const background = continuous.edges.at(-1), routeBefore = continuous._screenRoute(background);
continuous.enterFocus({id:"s1"});
assert.deepEqual(continuous._screenRoute(background),routeBefore,"pending read keeps background line geometry");
continuous.reconcileFocus(view());
const flight=continuous.routeFlight;
continuous.paintAt=flight.at;
assert.deepEqual(continuous._screenRoute(background),routeBefore,"first frame starts at the painted line");
assert.equal(continuous._visibility(`e:${background.from}\0${background.to}\0${background.rel}`),1,"background does not disappear at focus entry");
continuous.paintAt=flight.at+flight.ms/2;
const halfway=continuous._screenRoute(background), routeEnd=flight.to.get(`other\0tc1\0sourced_from`);
for(let i=0;i<4;i++)for(let j=0;j<2;j++)assert.ok(Math.abs(halfway[i][j]-(routeBefore[i][j]+routeEnd[i][j])/2)<1e-6,"line geometry and camera share continuous screen interpolation");
assert.ok(Math.abs(continuous._visibility(`e:other\0tc1\0sourced_from`)-.56)<1e-6,"background line dims gradually");
continuous.paintAt=flight.at+flight.ms;
assert.equal(continuous._visibility(`e:other\0tc1\0sourced_from`),.12,"background remains visible after focus");
assert.equal([...continuous._paintEdges()].length,continuous.edges.length);
assert.ok(flight.ms>=600,"focus zoom remains perceptible");
