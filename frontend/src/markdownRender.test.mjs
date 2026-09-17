import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { citationReferenceFromURL, remarkSciAideCitations, safeMarkdownURL } from "./markdownRender.js";

test("citation transformer preserves markdown structure and does not rewrite code or links", () => {
  const tree = {
    type: "root",
    children: [
      { type: "paragraph", children: [{ type: "text", value: "结论 [K-0123456789AB] 之后" }, { type: "text", value: "以及 [K-333333333333]" }] },
      { type: "code", value: "[K-111111111111]" },
      { type: "link", url: "https://example.com", children: [{ type: "text", value: "[K-222222222222]" }] },
    ],
  };
  remarkSciAideCitations()(tree);
  const paragraph = tree.children[0];
  assert.deepEqual(paragraph.children, [
    { type: "text", value: "结论 " },
    { type: "link", url: "sciaide-citation:K-0123456789AB", children: [{ type: "text", value: "[K-0123456789AB]" }] },
    { type: "text", value: " 之后" },
    { type: "text", value: "以及 " },
    { type: "link", url: "sciaide-citation:K-333333333333", children: [{ type: "text", value: "[K-333333333333]" }] },
  ]);
  assert.equal(tree.children[1].value, "[K-111111111111]");
  assert.equal(tree.children[2].children[0].value, "[K-222222222222]");
});

test("markdown URL policy allows expected links and rejects active or local schemes", () => {
  assert.equal(safeMarkdownURL("https://example.com/paper"), "https://example.com/paper");
  assert.equal(safeMarkdownURL("mailto:author@example.com"), "mailto:author@example.com");
  assert.equal(safeMarkdownURL("#method"), "#method");
  assert.equal(safeMarkdownURL("javascript:alert(1)"), "");
  assert.equal(safeMarkdownURL("file:///C:/Users/AA/secret.txt"), "");
  assert.equal(safeMarkdownURL("/internal/endpoint"), "");
});

test("citation URL parser accepts only the frozen reference shape", () => {
  assert.equal(citationReferenceFromURL("sciaide-citation:K-0123456789AB"), "[K-0123456789AB]");
  assert.equal(citationReferenceFromURL("sciaide-citation:K-0123456789ab"), "");
  assert.equal(citationReferenceFromURL("https://example.com"), "");
});

test("markdown pipeline renders GFM while dropping raw HTML and unsafe URLs", () => {
  const markdown = [
    "## 结论",
    "",
    "- **显著**差异 [K-0123456789AB]",
    "",
    "| 组别 | 均值 |",
    "| --- | ---: |",
    "| A | 3.2 |",
    "",
    "<script>alert('unsafe')</script>",
    "",
    "[危险链接](javascript:alert(1))",
  ].join("\n");
  const html = renderToStaticMarkup(React.createElement(ReactMarkdown, {
    remarkPlugins: [remarkGfm, remarkSciAideCitations],
    skipHtml: true,
    urlTransform: safeMarkdownURL,
  }, markdown));
  assert.match(html, /<h2>结论<\/h2>/);
  assert.match(html, /<strong>显著<\/strong>/);
  assert.match(html, /<table>/);
  assert.match(html, /href="sciaide-citation:K-0123456789AB"/);
  assert.doesNotMatch(html, /<script|alert\(&#x27;unsafe/);
  assert.doesNotMatch(html, /javascript:/);
});

test("markdown pipeline tolerates incomplete streamed blocks", () => {
  const samples = ["**正在生成", "```python\nprint('running')", "| 列 A | 列 B |\n| ---", "[尚未结束的链接](https://example.com"];
  for (const sample of samples) {
    assert.doesNotThrow(() => renderToStaticMarkup(React.createElement(ReactMarkdown, {
      remarkPlugins: [remarkGfm, remarkSciAideCitations],
      skipHtml: true,
      urlTransform: safeMarkdownURL,
    }, sample)));
  }
});
