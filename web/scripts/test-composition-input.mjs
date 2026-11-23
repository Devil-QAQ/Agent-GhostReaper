import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";

const source = readFileSync(new URL("../lib/composition-input.js", import.meta.url), "utf8");
const { compositionInputHandlers } = await import(`data:text/javascript,${encodeURIComponent(source)}`);
let submitted = 0, starts = 0, ends = 0;
const state = { current: false };
const handlers = compositionInputHandlers(state, {
  onKeyDown(e) { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submitted++; } },
  onCompositionStart() { starts++; },
  onCompositionEnd() { ends++; },
});
function event(extra = {}) {
  return { key: "Enter", prevented: false, stopped: false,
    preventDefault() { this.prevented = true; }, stopPropagation() { this.stopped = true; }, ...extra };
}
for (const extra of [{ isComposing: true }, { nativeEvent: { isComposing: true } }, { keyCode: 229 }, { nativeEvent: { keyCode: 229 } }]) {
  const e = event(extra); handlers.onKeyDown(e);
  assert.equal(submitted, 0); assert.equal(e.prevented, true); assert.equal(e.stopped, true);
}
handlers.onCompositionStart({});
const tracked = event(); handlers.onKeyDown(tracked);
assert.equal(submitted, 0); assert.equal(tracked.prevented, true);
const letter = event({ key: "a" }); handlers.onKeyDown(letter);
assert.equal(letter.prevented, false, "native composition must keep editing text");
handlers.onCompositionEnd({});
const safari = event({ keyCode: 229 }); handlers.onKeyDown(safari);
assert.equal(submitted, 0, "compositionend before Enter must still honor 229");
handlers.onKeyDown(event()); assert.equal(submitted, 1);
const newline = event({ shiftKey: true }); handlers.onKeyDown(newline);
assert.equal(submitted, 1); assert.equal(newline.prevented, false);
assert.equal(starts, 1); assert.equal(ends, 1);

// Guard the shared components against a later prop spread overriding protection.
for (const name of ["input", "textarea"]) {
  const s = readFileSync(new URL(`../components/ui/${name}.js`, import.meta.url), "utf8");
  assert.ok(s.indexOf("{...compositionInputHandlers(composing, props)}") > s.indexOf("{...props}"));
}
// All free-text controls use those components; native date/range controls have no submit shortcut.
const root = new URL("..", import.meta.url).pathname;
function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = resolve(dir, entry.name);
    if (entry.isDirectory()) walk(path);
    else if (entry.name.endsWith(".js") && !path.endsWith("/ui/input.js") && !path.endsWith("/ui/textarea.js")) {
      const s = readFileSync(path, "utf8");
      assert.equal(/<textarea\b|<form\b/.test(s), false, `unprotected text/form control: ${path}`);
      for (const match of s.matchAll(/<input\b[\s\S]*?\/>/g)) assert.match(match[0], /type="(?:file|range|datetime-local)"/, `unprotected input: ${path}`);
    }
  }
}
walk(resolve(root, "views")); walk(resolve(root, "components"));
console.log("composition-input: composition, 229, bubbling, Enter, newline and control inventory passed");
