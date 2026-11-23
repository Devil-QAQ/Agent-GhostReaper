import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
const shell = await readFile(new URL("../components/app-shell.js", import.meta.url), "utf8");
const parseSource = shell.slice(shell.indexOf("function parseHash()"), shell.indexOf("function useHashRoute()"));
const navSource = await readFile(new URL("../lib/nav.js", import.meta.url), "utf8");
const nav = await import(`data:text/javascript;base64,${Buffer.from(navSource).toString("base64")}`);
const parse = new Function("location", "ROUTES", "LEGACY_TASK_ROUTES", `${parseSource};return parseHash();`);
for (const hash of ["#/task/t/progress", "#/task/t/annulus", "#/progress", "#/annulus", "#/unknown"]) {
 const location = { hash };
 const route = parse(location, nav.ROUTES, nav.LEGACY_TASK_ROUTES);
 assert.equal(location.hash, hash, "retired links never redirect");
 assert.ok(route.kind === "missing" || !nav.enabledTaskTabs().some(t => t.id === route.tab));
}
assert.equal(nav.enabledTaskTabs().length, 7);
assert.equal(parse({hash:""}, nav.ROUTES, nav.LEGACY_TASK_ROUTES).id, "tasks");
for (const tab of nav.enabledTaskTabs()) assert.equal(parse({hash:`#/task/t/${tab.id}`}, nav.ROUTES, nav.LEGACY_TASK_ROUTES).tab, tab.id);
const api = await readFile(new URL("../lib/api.js", import.meta.url), "utf8");
const home = new Function("type", api.slice(api.indexOf("export function nodeHomeView(type) {")+"export function nodeHomeView(type) {".length, api.indexOf("\n}",api.indexOf("export function nodeHomeView(type) {"))));
for (const type of ["step", "fact", "testclaim"]) assert.equal(home(type), "panorama");
assert.equal(home("asset"), "assets");
const pkg=JSON.parse(await readFile(new URL("../package.json",import.meta.url),"utf8"));
for (const name of ["@xyflow/react","cytoscape","cytoscape-navigator","cytoscape-node-html-label"]) assert.ok(!pkg.dependencies[name]);
const workbench=await readFile(new URL("../views/workbench-page.js",import.meta.url),"utf8");
assert.ok(!workbench.includes("GraphCanvas") && workbench.includes("/workbench`") && workbench.includes("tid && sheet ?"));
assert.ok(shell.includes('route.kind === "missing"'));
console.log("旧图退役：七页导航、旧链接不存在且不跳转、有效节点跳转、依赖移除、工作台按需详情检查通过");
