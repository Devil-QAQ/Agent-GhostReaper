import assert from "node:assert/strict";
import { assetNetLayout } from "../lib/asset-net-layout.mjs";

const asset = id => ({ id, title: id, class: "domain" });
const cases = [
  { name: "empty", assets: [], edges: [] },
  { name: "single", assets: [asset("a")], edges: [] },
  { name: "disconnected", assets: [asset("a"), asset("b")], edges: [] },
  { name: "nested", assets: [asset("a"), asset("b"), asset("c")], edges: [{ from: "b", to: "a" }, { from: "c", to: "b" }] },
  { name: "shared", assets: [asset("a"), asset("b"), asset("c"), asset("d")], edges: [{ from: "c", to: "a" }, { from: "c", to: "b" }, { from: "d", to: "c" }] },
  { name: "cycle", assets: [asset("a"), asset("b")], edges: [{ from: "a", to: "b" }, { from: "b", to: "a" }] },
  { name: "duplicate and invalid edges", assets: [asset("a"), asset("b")], edges: [{ from: "a", to: "b" }, { from: "a", to: "b" }, { from: "ghost", to: "a" }, { from: "a", to: "a" }] },
];
for (const sample of cases) {
  const layout = assetNetLayout(sample.assets, sample.edges), ids = new Set(layout.nodes.map(n => n.id));
  assert.equal(ids.size, sample.assets.length, sample.name);
  assert.equal(layout.nodes.length, ids.size, sample.name);
  assert.ok(layout.nodes.every(n => Number.isFinite(n.x) && Number.isFinite(n.y) && n.systems.length), sample.name);
  assert.ok(layout.edges.every(e => sample.edges.some(original => original.to === e.from && original.from === e.to)), sample.name);
  assert.deepEqual(assetNetLayout([...sample.assets].reverse(), [...sample.edges].reverse()), layout, sample.name + " stable order");
  if (sample.name === "shared") {
    assert.deepEqual(layout.nodes.find(n => n.id === "c").systems, ["a", "b"]);
    assert.deepEqual(layout.nodes.find(n => n.id === "d").systems, ["a", "b"]);
    assert.equal(layout.roots.length, 2);
  }
}
// Many assets and systems: all records survive; layout stays off the UI thread.
const sharedAssets = [asset("a"),asset("b"),{...asset("ip"),source:"discovered"}];
const explicit = [{from:"a",to:"ip",kind:"resolves_to"},{from:"b",to:"ip",kind:"resolves_to"}];
const linked = assetNetLayout(sharedAssets, [], explicit);
assert.equal(linked.nodes.length,3);assert.deepEqual(linked.roots,["a","b","ip"]);
assert.deepEqual(linked.nodes.find(n=>n.id==="ip").systems,["ip"]);
assert.equal(linked.edges.length,2);assert.ok(linked.edges.every(e=>e.rel==="resolves_to"));
assert.deepEqual(assetNetLayout([...sharedAssets].reverse(),[],[...explicit].reverse()),linked);
const unrelated = assetNetLayout([asset("a"),asset("b")],[],[{from:"a",to:"b",kind:"related_to"}]);
assert.equal(unrelated.roots.length,2);assert.equal(unrelated.edges.length,1);
const declared = assetNetLayout([asset("a"),asset("ip")],[],[{from:"a",to:"ip",kind:"resolves_to"}]);
assert.equal(declared.roots.length,2,"DNS never becomes a hierarchy");
assert.deepEqual(declared.nodes.find(n=>n.id==="ip").systems,["ip"],"association does not change a declared root's membership");
const withService = [...sharedAssets, {...asset("service"),source:"discovered"}];
const deployed = assetNetLayout(withService,[{from:"service",to:"a"}],[...explicit,{from:"service",to:"ip",kind:"hosted_on"}]);
assert.deepEqual(deployed.nodes.find(n=>n.id==="service").systems,["a"],"deployment does not replace an existing containment");
assert.deepEqual(deployed.nodes.find(n=>n.id==="ip").systems,["ip"]);
assert.equal(deployed.edges.length,4,"all actual relationships remain visible");
assert.deepEqual(assetNetLayout([...withService].reverse(),[{from:"service",to:"a"}],[...explicit,{from:"service",to:"ip",kind:"hosted_on"}].reverse()),deployed);
const many = Array.from({ length: 5000 }, (_, i) => asset(`a${String(i).padStart(5, "0")}`));
const edges = many.slice(3).map((node, i) => ({ from: node.id, to: many[i % 3].id }));
const before = performance.now(), large = assetNetLayout(many, edges);
assert.equal(large.nodes.length, 5000); assert.equal(large.edges.length, 4997); assert.equal(large.roots.length, 3);
assert.ok(large.nodes.every(n => Number.isFinite(n.x) && Number.isFinite(n.y)));
console.log(`资产网布局通过：7 类结构、乱序稳定、共享资产唯一、5000 资产完整；预计算 ${Math.round(performance.now() - before)}ms。`);
const sameIdentity = assetNetLayout(sharedAssets.map(a => ({...a,source:"manual"})), [], explicit);
assert.deepEqual(sameIdentity.nodes.map(n => [n.id,n.x,n.y,n.systems]),linked.nodes.map(n => [n.id,n.x,n.y,n.systems]),"source labels cannot decide structural groups");
// Asset focus keeps address ancestry and only relations of the selected
// subtree, without expanding sibling services through a shared IP.
const {readFile}=await import('node:fs/promises');
const panoramaSource=await readFile(new URL('../lib/panorama-engine.js',import.meta.url),'utf8');
const panoramaURL=`data:text/javascript;base64,${Buffer.from(panoramaSource).toString('base64')}`;
const assetSource=(await readFile(new URL('../lib/asset-net-engine.js',import.meta.url),'utf8')).replace('"./panorama-engine"',JSON.stringify(panoramaURL));
const {AssetNetEngine}=await import(`data:text/javascript;base64,${Buffer.from(assetSource).toString('base64')}`);
const focused=Object.create(AssetNetEngine.prototype);
focused.nodes=['root','sub','one','two','ip','unrelated'].map(id=>({id}));focused.byId=new Map(focused.nodes.map(n=>[n.id,n]));
focused.edges=[{from:'root',to:'sub',rel:'sub_of'},{from:'sub',to:'one',rel:'sub_of'},{from:'sub',to:'two',rel:'sub_of'},{from:'sub',to:'ip',rel:'resolves_to'},{from:'one',to:'ip',rel:'hosted_on'},{from:'unrelated',to:'ip',rel:'hosted_on'}];
let selection,preserve;
focused.enterFocus=({id})=>{focused.focus=id};focused.reconcileFocus=(v,p)=>{selection=v;preserve=p};
focused.focusAsset('one');assert.deepEqual(new Set(selection.ids),new Set(['root','sub','one','ip']));
assert.equal(selection.ids.includes('two'),false);assert.equal(selection.ids.includes('unrelated'),false);
focused.focusAsset('ip',true);assert.equal(preserve,true);assert.equal(selection.ids.includes('two'),false,'shared IP does not collect sibling services via their domain');
assert.ok(selection.edges.every(e=>focused.edges.includes(e)),'asset focus never invents links');
