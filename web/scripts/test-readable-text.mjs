import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";

const require = createRequire(new URL("../package.json", import.meta.url));
const React = require("react");
const { renderToStaticMarkup } = require("react-dom/server");
const { transform } = require("next/dist/build/swc");
const source = await readFile(new URL("../components/readable-text.js", import.meta.url), "utf8");
const compiled = await transform(source, {
  filename: "readable-text.js",
  jsc: { parser: { syntax: "ecmascript", jsx: true }, transform: { react: { runtime: "classic" } } },
  module: { type: "es6" },
});
const code = compiled.code.replace(/from ["']([^"']+)["']/g,
  (_, name) => `from ${JSON.stringify(pathToFileURL(require.resolve(name)).href)}`);
const { ReadableText } = await import(`data:text/javascript;base64,${Buffer.from(code).toString("base64")}`);
const render = (text, renderText) => renderToStaticMarkup(React.createElement(ReadableText, { text, renderText }));
assert.equal(render(""), "");
const tail = "仅适用于已验证的接口。";
const long = "完整说明。".repeat(150) + tail;
assert.ok(render(long).includes(long), "长说明不得裁句");
const html = render("## 结果\n\n已确认**公开备份**，见 fact:f-one。\n\n- 入口需认证\n- 影响未扩大\n\n```http\nGET / HTTP/1.1\n```",
  text => text.includes("fact:f-one") ? React.createElement("button", { "data-ref": "f-one" }, text) : text);
for (const tag of ["<h2", "<strong>公开备份</strong>", "<ul", "<li", "<pre", "<code", 'data-ref="f-one"']) {
  assert.ok(html.includes(tag), `缺少正文结构或引用: ${tag}`);
}
assert.ok(!html.includes("**公开备份**"), "Markdown 标记不应直接显示");
const protectedText = render("`fact:f-code` 和 [fact:f-link](https://example.com)",
  text => React.createElement("span", { "data-decorated": "yes" }, text));
assert.ok(!/<code[^>]*>[^<]*<span/.test(protectedText), "代码不装饰为引用");
assert.ok(!/<a [^>]*>[^<]*<span/.test(protectedText), "链接内部不嵌套引用控件");
const unsafe = render('[危险链接](javascript:alert(1))\n\n<script>alert(1)</script>');
assert.ok(!unsafe.includes('href="javascript:'), "不允许脚本链接");
assert.ok(!unsafe.includes("<script>"), "不执行原始 HTML");
const table = render("| 名称 | 结果 |\n| --- | --- |\n| 检查 | 完成 |");
assert.ok(table.includes("<table") && table.includes("<td"), "表格结构保留");
console.log("readable-text: 正文结构、长文、引用、代码与内容安全检查通过");
