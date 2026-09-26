import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const source = await readFile(new URL("./ResearchClarificationDialog.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 } }).outputText;
const exports = {};
const actualRequire=createRequire(import.meta.url);
vm.runInNewContext(compiled, { exports, require: name => name === "./Modal" ? {ModalBackdrop: ({children}) => React.createElement("div",null,children)} : name === "react" ? {...actualRequire(name),useRef:value=>({current:value})} : actualRequire(name) });
const question = { id: "metrics", text: "想分析哪些指标？", required: true, selectionMode: "multiple", options: [{ id: "height", label: "株高" }, { id: "mass", label: "干重" }] };
function elements(node, type) {
  if (!node || typeof node !== "object") return [];
  return [...(node.type === type ? [node] : []), ...React.Children.toArray(node.props?.children).flatMap((child) => elements(child, type))];
}
function harness(q, initial = []) {
  let draft = [...initial], saved = [...initial], closed = false;
  return {
    render: () => exports.ResearchClarificationDialog({ question: q, draft, setDraft: (next) => { draft = next; }, close: () => { closed = true; }, confirm: (next) => { saved = next; closed = true; } }),
    values: () => ({ draft: Array.from(draft), saved: Array.from(saved), closed }),
  };
}
test("long questions stay complete inside the scrollable body, not the fixed header", () => {
  const text = "您的模拟实验数据包含哪些关键信息？例如：设置了几个光照时间梯度（如4h/8h/12h/16h）、幼苗是什么品种、一个月后测量了哪些生长指标（如高度/叶片数/生物量）、每组有多少样本？".repeat(8);
  const tree = harness({ ...question, text }).render();
  const header = elements(tree, "header")[0];
  assert.equal(React.Children.toArray(elements(header, "h2")[0].props.children).join(""), "研究边界选择 · 多选");
  assert.equal(renderToStaticMarkup(header).includes(text), false);
  const body = elements(tree, "div").find((node) => node.props.className === "research-clarification-dialog-body");
  assert.equal(elements(body, "h3")[0].props.children, text);
  assert.equal(elements(body, "div").find((node) => node.props.role === "group").props["aria-labelledby"], elements(body, "h3")[0].props.id);
  assert.equal(elements(tree, "footer").length, 1);
});
test("multiple choice renders checkboxes and commits all labels only on confirmation", () => {
  const h = harness(question);
  elements(h.render(), "input")[0].props.onChange();
  elements(h.render(), "input")[1].props.onChange();
  assert.deepEqual(h.values(), { draft: ["height", "mass"], saved: [], closed: false });
  const html = renderToStaticMarkup(h.render());
  assert.equal((html.match(/type="checkbox"/g) ?? []).length, 2);
  assert.match(html, /研究边界选择 · 多选/);
  elements(h.render(), "button").find((button) => button.props.children === "确认选择").props.onClick();
  assert.deepEqual(h.values().saved, ["height", "mass"]);
  assert.equal(exports.clarificationSummary(question, h.values().saved), "株高；干重");
});
test("single and legacy choice replace draft without closing prematurely", () => {
  for (const selectionMode of ["single", undefined]) {
    const h = harness({ ...question, selectionMode }, ["height"]);
    elements(h.render(), "input")[1].props.onChange();
    assert.deepEqual(h.values(), { draft: ["mass"], saved: ["height"], closed: false });
    assert.match(renderToStaticMarkup(h.render()), /type="radio"/);
    assert.match(renderToStaticMarkup(h.render()), /研究边界选择 · 单选/);
  }
});
test("cancel, close and backdrop dismiss without changing saved answers", () => {
  for (const action of ["取消", "close", "backdrop"]) {
    const h = harness(question, ["height"]);
    elements(h.render(), "input")[1].props.onChange();
    const tree = h.render();
    if (action === "backdrop") { tree.props.close(); }
    else elements(tree, "button").find((button) => action === "close" ? button.props.className === "close" : button.props.children === action).props.onClick();
    assert.deepEqual(h.values().saved, ["height"]);
    assert.equal(h.values().closed, true);
  }
});
test("multiple toggles and required empty confirmation, optional clearing", () => {
  const h = harness(question, ["height"]);
  elements(h.render(), "input")[0].props.onChange();
  assert.deepEqual(h.values().draft, []);
  assert.equal(elements(h.render(), "button").at(-1).props.disabled, true);
  const optional = harness({ ...question, required: false });
  assert.equal(elements(optional.render(), "button").at(-1).props.disabled, false);
});
test("parent resets on question content changes and gates invalid plans and replan cost", async () => {
  const app = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  assert.match(app, /JSON.stringify\(clarification \?\? null\)/);
  assert.match(source, /<ModalBackdrop/);
  assert.match(app, /starterPlanChecking \|\| Boolean\(starterPlanError\) \|\| !clarificationComplete/);
  assert.match(app, /disabled=\{!confirmReplan \|\| Boolean\(busy\) \|\| starterPlanChecking\}/);
  assert.match(app, /"WorkflowFacade", "ReplanResearchStarter"/);
});
