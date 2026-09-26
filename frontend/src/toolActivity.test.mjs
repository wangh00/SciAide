import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import ts from "typescript";
import { activityDurationLabel, activityTruncationLabel, isSkillToolName, safeToolArguments, toolPresentation, toolResultSummary } from "./toolActivity.js";

test("tool cards stay collapsed during execution and only a new approval opens them", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const component = source.slice(source.indexOf("function UnifiedToolActivityCard("), source.indexOf("function ToolActivityCard("));
  const { outputText } = ts.transpileModule(component, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } });
  let open;
  let initialized = false;
  let dependencies;
  let effect;
  const useState = (initial) => {
    if (!initialized) { open = initial; initialized = true; }
    return [open, value => { open = typeof value === "function" ? value(open) : value; }];
  };
  const useEffect = (callback, next) => {
    if (!dependencies || next.some((value, i) => value !== dependencies[i])) effect = callback;
    dependencies = next;
  };
  const React = { createElement: (type, props, ...children) => ({ type, props, children }) };
  const deps = { React, useState, useEffect, safeToolArguments, toolPresentation, toolResultSummary,
    activityDurationLabel, activityTruncationLabel, summarizeLocalExecution: () => "", normalizeDisplayText: value => value,
    toolStatusText: {}, Icon: "icon" };
  const Card = new Function(...Object.keys(deps), `${outputText}; return UnifiedToolActivityCard;`)(...Object.values(deps));
  const render = activity => {
    effect = undefined;
    let tree = Card({ activity: { toolName: "builtin.workspace.read_text", ...activity } });
    if (effect) { effect(); tree = Card({ activity: { toolName: "builtin.workspace.read_text", ...activity } }); }
    return tree.children[0];
  };
  for (const status of ["pending", "running", "completed", "failed", "running"]) {
    assert.equal(render({ status }).props["aria-expanded"], false, status);
  }
  render({ status: "running" }).props.onClick();
  assert.equal(render({ status: "completed" }).props["aria-expanded"], true);
  render({ status: "completed" }).props.onClick();
  assert.equal(render({ status: "running" }).props["aria-expanded"], false);
  const approval = { id: "approval-1" };
  const button = render({ status: "awaiting_approval", approval });
  assert.equal(button.props["aria-expanded"], true);
  assert.equal(render({ status: "running" }).props["aria-expanded"], false);
  render({ status: "awaiting_approval", approval }).props.onClick();
  assert.equal(render({ status: "awaiting_approval", approval: { ...approval } }).props["aria-expanded"], false);
  assert.equal(render({ status: "running" }).props["aria-expanded"], false);
  assert.equal(render({ status: "awaiting_approval", approval: { id: "approval-2" } }).props["aria-expanded"], true);
});

test("internal tool name is the first expanded row, not a collapsed subtitle", async () => {
  const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
  const card = source.slice(source.indexOf("function UnifiedToolActivityCard("), source.indexOf("function ToolActivityCard("));
  const header = card.slice(card.indexOf("return <article"), card.indexOf('{open && <div className="tool-card-body">'));
  assert.doesNotMatch(header, /className=\{"risk |中风险|高风险/);
  assert.doesNotMatch(header, /normalizeDisplayText\(activity.toolName\)/);
  assert.match(card, /\{open && <div className="tool-card-body">\s*<code className="tool-card-name">\{normalizeDisplayText\(activity.toolName\)\}<\/code>/);
});

test("activity arguments redact secrets and bound large values", () => {
  const value = safeToolArguments({
    path: "analysis/results.csv",
    apiKey: "sk-do-not-show",
    nested: { authorization: "Bearer do-not-show" },
    command: "Invoke-WebRequest https://example.test/?token=do-not-show",
    code: "x".repeat(1500),
  });
  assert.equal(value.apiKey, "[已隐藏]");
  assert.equal(value.nested.authorization, "[已隐藏]");
  assert.equal(value.path, "analysis/results.csv");
  assert.doesNotMatch(value.command, /do-not-show/);
  assert.match(value.code, /已截断/);
});

test("activity arguments remain bounded for very large payloads", () => {
  const value = safeToolArguments({ code: "x".repeat(20000) });
  assert.match(value.code, /已截断/);
  assert.ok(JSON.stringify(value).length <= 16 * 1024);
});

test("Skill calls expose the selected Skill and page without opening details", () => {
  assert.deepEqual(toolPresentation({
    toolName: "builtin.skill.load",
    arguments: { name: "hypothesis-generation", section: "section-9", limit: 1568 },
  }), {
    kind: "Skill", icon: "skill", title: "加载 Skill · hypothesis-generation", summary: "section-9 · 最多 1,568 字符",
  });
});

test("Skill activity hides implementation noise and describes paged content clearly", () => {
  assert.equal(isSkillToolName("builtin.skill.load"), true);
  assert.equal(isSkillToolName("builtin.skill.resource.read_text"), true);
  assert.equal(activityDurationLabel("builtin.skill.load", 2400), "");
  assert.equal(activityDurationLabel("builtin.document.read", 0), "");
  assert.equal(activityDurationLabel("builtin.document.read", 1250), "1.3 秒");
  assert.equal(activityTruncationLabel("builtin.skill.load"), "内容较长 · 分段读取");
  assert.equal(activityTruncationLabel("builtin.document.read"), "输出较长 · 已省略部分");
});

test("citation Seed explains its host-side purpose in one line", () => {
  assert.deepEqual(toolPresentation({
    toolName: "builtin.workflow.citation.seed",
    arguments: {},
  }), {
    kind: "工具", icon: "tool", title: "Seed", summary: "系统正在把已核验的科研证据“注入”当前 AI 对话",
  });
});

test("MCP calls expose server, native tool and primary URL", () => {
  assert.deepEqual(toolPresentation({
    toolName: "mcp.chrome-devtools.navigate_page",
    arguments: { url: "https://example.test/research", timeout: 30000 },
  }), {
    kind: "MCP", icon: "server", title: "chrome-devtools · navigate_page", summary: "https://example.test/research",
  });
});

test("result summary removes markdown heading noise and stays bounded", () => {
  const summary = toolResultSummary({ toolName: "builtin.document.read", result: { status: "success", text: `# Read result\n${"x".repeat(300)}` } });
  assert.equal(summary.startsWith("Read result"), true);
  assert.equal(summary.length <= 180, true);
});

test("resource actions display resolved objects rather than opaque handles", () => {
  const view = toolPresentation({ toolName: "builtin.resource.open", arguments: { actionId: "res_opaque" }, result: { structured: { label: "加载 Skill · statistical-analysis", sourceTool: "builtin.skill.load" } } });
  assert.equal(view.title, "加载 Skill · statistical-analysis");
  assert.equal(view.kind, "Skill");
  assert.equal(view.summary, "");
  const file = toolPresentation({ toolName: "builtin.resource.open", result: { structured: { label: "读取资料 · seedlings.csv", sourceTool: "builtin.document.read" } } });
  assert.equal(file.title, "读取资料 · seedlings.csv");
  assert.equal(file.kind, "工具");
  assert.equal(toolPresentation({ toolName: "builtin.resource.open" }).title, "操作任务资源");
});

test("workspace list limit is entries, not characters", () => {
 const view=toolPresentation({toolName:"builtin.workspace.list",arguments:{path:".sciaide",limit:500}});
 assert.equal(view.summary,".sciaide · 最多 500 项");
});


test("MCP discovery cards distinguish server status from tool search", () => {
  assert.equal(toolPresentation({toolName: "builtin.mcp.list", arguments: {limit: 20}}).title, "查看 MCP 状态");
  const search = toolPresentation({toolName: "builtin.tools.search", arguments: {query: "browser", limit: 8}});
  assert.equal(search.title, "查找 MCP 工具");
  assert.equal(search.summary, "browser");
});
