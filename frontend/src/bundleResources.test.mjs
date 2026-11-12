import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import postcss from "postcss";
import { katexFontPlugin, katexWoff2Plugin } from "../katexFontPlugin.mjs";

test("KaTeX retains every font family and weight with lossless WOFF2 fonts", async () => {
  const css = await readFile(new URL("../node_modules/katex/dist/katex.min.css", import.meta.url), "utf8");
  const original = postcss.parse(css);
  const result = await postcss([katexFontPlugin()]).process(css, { from: undefined });
  const faces = root => {
    const result = [];
    root.walkAtRules("font-face", rule => result.push(rule));
    return result;
  };
  const before = faces(original), after = faces(result.root);
  assert.equal(after.length, before.length);
  assert.ok(after.length >= 20);
  after.forEach((rule, index) => {
    assert.equal(rule.nodes.find(n => n.prop === "src").value, before[index].nodes.find(n => n.prop === "src").value.match(/url\([^)]*\)\s*format\(["']woff2["']\)/)[0]);
    assert.deepEqual(rule.nodes.filter(n => n.prop !== "src").map(n => n.toString()), before[index].nodes.filter(n => n.prop !== "src").map(n => n.toString()));
  });
  assert.doesNotMatch(result.css, /\.woff["')]|\.ttf["')]/);
});

test("font optimization leaves non-KaTeX declarations and fallback-only fonts unchanged", async () => {
  const css = '@font-face{font-family:Custom;src:url(a.woff2) format("woff2"),url(a.ttf) format("truetype")}@font-face{font-family:KaTeX_New;src:url(b.ttf) format("truetype")}';
  assert.equal((await postcss([katexFontPlugin()]).process(css, { from: undefined })).css, css);
});

test("KaTeX source optimization runs before Vite emits fallback assets and ignores other CSS", async () => {
  const plugin = katexWoff2Plugin();
  assert.equal(plugin.enforce, "pre");
  const css = '@font-face{font-family:KaTeX_Main;src:url(a.woff2) format("woff2"),url(a.ttf) format("truetype")}';
  const result = await plugin.transform(css, 'C:\\project\\node_modules\\katex\\dist\\katex.min.css');
  assert.doesNotMatch(result.code, /\.ttf/);
  assert.match(result.code, /\.woff2/);
  assert.equal(await plugin.transform(css, '/project/other.css'), undefined);
});

test("removed legacy CSS classes do not return while active material and card styles remain", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.doesNotMatch(css, /\.(?:knowledge-modal|knowledge-resource-browser|knowledge-detail-dialog|research-material-card|workflow-modal|workflow-list|workflow-side-actions|research-mode-indicator)(?![\w-])/);
  assert.match(css, /\.knowledge-open(?![\w-])/);
  assert.match(await readFile(new URL("./researchMaterialsLibrary.css", import.meta.url), "utf8"), /\.research-materials-modal/);
  assert.match(await readFile(new URL("./researchCard.css", import.meta.url), "utf8"), /\.research-card/);
});
