import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

// Exercise the actual presentation helpers and JSX, not just source regexes.
const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
const start = source.indexOf("function researchRouteValidation(");
const end = source.indexOf("function ResearchRouteDetailDialog(", start);
assert.ok(start >= 0 && end > start);
const compiled = ts.transpileModule(
  'import * as React from "react";\n' + source.slice(start, end) +
    "\nexport { ResearchRoutePlanningNotes, researchRouteCanAdopt, researchRouteUserGap };",
  { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } },
).outputText;
const exports = {};
vm.runInNewContext(compiled, { exports, require: createRequire(import.meta.url) });

test("ready design shows later research conditions without pretending they are resolved", () => {
  const route = {
    validation: "ready", availableNow: true, blockers: [],
    planningNotes: ["正式采集前须完成伦理审批", "量表授权尚待确认", "缺少 CSV 数据"],
  };
  const html = renderToStaticMarkup(React.createElement(exports.ResearchRoutePlanningNotes, { route }));
  assert.match(html, /研究边界与后续条件/);
  assert.match(html, /不代表已经获得实证结论/);
  assert.match(html, /正式采集前须完成伦理审批/);
  assert.match(html, /量表授权尚待确认/);
  assert.equal(exports.researchRouteUserGap(route), null);
  assert.equal(exports.researchRouteCanAdopt(route), true);
});

test("blocked analysis remains adoptable with an actionable file-selection instruction", () => {
  const route = { validation: "blocked", availableNow: false, blockers: ["请为本任务选择 CSV 研究数据"] };
  assert.equal(exports.researchRouteCanAdopt(route), true);
  assert.match(exports.researchRouteUserGap(route), /请上传或选择一份与本课题相关的/);
  assert.equal(renderToStaticMarkup(React.createElement(exports.ResearchRoutePlanningNotes, { route })), "");
});

test("adopted routes bind files and start the frozen route without replanning", () => {
  const choose = source.slice(source.indexOf("async function chooseInputFile("), source.indexOf("const inputDefinition =", source.indexOf("async function chooseInputFile(")));
  const launch = source.slice(source.indexOf("async function startRun()"), source.indexOf("async function runAction("));
  assert.match(choose, /ChooseInputFileForResearchRoute/);
  assert.match(launch, /StartAdoptedResearchRoute/);
  assert.doesNotMatch(choose + launch, /AnswerResearchClarification|ReplanResearchStarter/);
  assert.match(source, /已选路线 · 补充数据/);
  assert.match(source, /开始执行所选路线/);
});

test("invalid or unverified plans cannot be adopted even if the model says ready", () => {
  for (const validation of ["invalid", "checking"]) {
    assert.equal(exports.researchRouteCanAdopt({ validation, availableNow: true }), false);
  }
});

test("loaded Skill labels use the host snapshot rather than model declarations", () => {
  assert.match(source, /starterPlan\.loadedSkills\?\.length \?\? 0/);
  assert.match(source, /plan\.loadedSkills\?\.some\(\(loaded\) => loaded\.name === skill\.name\)/);
  assert.doesNotMatch(source, /已加载 \{starterPlan\.selectedSkills\.length\}/);
  assert.doesNotMatch(source, /\{usedSkills\.length\} 个已实际加载/);
});

test("literature retrieval failures are not presented as scientific evidence insufficiency", () => {
  assert.match(source, /retrievalStatus === "source_blocked" \? "部分来源检索受阻"/);
  assert.match(source, /retrievalStatus === "budget_exhausted" \? "检索尚未充分完成"/);
  assert.match(source, /当前材料覆盖不足/);
  assert.doesNotMatch(source, />扩大检索<|>继续补检索</);
});

test("discovery groups queries by task without reading all candidates for an empty group", () => {
  assert.match(source, /aria-label="文献任务来源"/);
  assert.match(source, /const visibleQueries = queries.filter/);
  assert.match(source, /if \(!effectiveQueryId\)/);
  assert.match(source, /effectiveQueryId, scopedTaskId/);
});

test("discovery navigation and history have separate bounded layouts", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\.research-layout > \.research-query-panel\s*\{[^}]*display: flex;[^}]*flex-direction: column/);
  assert.match(css, /\.research-origin-nav\s*\{[^}]*align-content: start;[^}]*grid-auto-rows: 72px/);
  assert.match(css, /\.research-history-toolbar\s*\{[^}]*flex: 0 0 42px/);
  assert.match(source, /effectiveQueryId === item.id/);
  assert.match(source, /deleteHistory\(\[\], true\)/);
  assert.match(source, /function selectOrigin\(id: string\)[\s\S]*?setCheckedQueries\(\[\]\)/);
  assert.match(source, /<summary>检索详情<\/summary>/);
  assert.match(source, /entry.createdAt/);
  assert.doesNotMatch(source, /aria-label="全选当前分组检索记录"/);
});

test("replanning has a labelled stateful card and separate consent/action area", () => {
  assert.match(source, /research-replan-card" aria-label="更新研究路线" aria-busy=/);
  assert.match(source, /<em role="status">/);
  assert.match(source, /research-replan-body/);
  assert.match(source, /research-replan-consent/);
  assert.match(source, /确认再次调用 AI 模型/);
  assert.match(source, /会消耗模型额度，可能产生费用/);
});
