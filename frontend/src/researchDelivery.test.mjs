import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
function segment(start, end) {
  const from = source.indexOf(start), to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from);
  return source.slice(from, to);
}
const compiled = ts.transpileModule('import * as React from "react";\n' +
  segment("function workflowResearchDesign(", "const workflowResearchDesignSections") +
  segment("function ResearchDeliveryStatus(", "function WorkflowInteractionCards(") +
  "\nexport { workflowResearchDesign, workflowResearchReport, workflowRevisionConversationId, ResearchDeliveryStatus, ResearchAcceptanceDetails };", {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 },
}).outputText;
const exports = {};
vm.runInNewContext(compiled, { exports, require: createRequire(import.meta.url), WorkflowResearchReportCard: () => null, WorkflowResearchDesignCard: () => null, ResearchRevisionCards: () => null });

test("completed flag and approved boolean alone cannot expose a final report", () => {
  const detail = { run: { status: "completed", outputs: { delivery_gate: { approved: true }, report_draft: { markdown: "# Report", methodSummary: "Method", confidence: "medium" } } } };
  assert.equal(exports.workflowResearchReport(detail), null);
  detail.deliveryAssessment = { status: "unverified" };
  assert.equal(exports.workflowResearchReport(detail), null);
  detail.deliveryAssessment.status = "reviewed";
  assert.equal(exports.workflowResearchReport(detail).markdown, "# Report");
  detail.run.status = "failed";
  assert.equal(exports.workflowResearchReport(detail), null);
});

test("revision placeholder is offered only for the current reviewed unregistered report", () => {
  const ready = { run: { conversationId: "research-chat", workflowPurpose: "user_plan", status: "completed", outputs: { delivery_gate: { approved: true }, report_draft: { markdown: "# Report", methodSummary: "Method", confidence: "medium" } } }, deliveryAssessment: { status: "reviewed" }, registeredDeliverables: [] };
  assert.equal(exports.workflowRevisionConversationId(ready), "research-chat");
  assert.equal(exports.workflowRevisionConversationId(null), "");
  for (const mutate of [
    detail => { detail.registeredDeliverables = ["report_draft"]; },
    detail => { detail.run.status = "running"; },
    detail => { detail.run.status = "failed"; },
    detail => { detail.deliveryAssessment.status = "unverified"; },
    detail => { detail.run.outputs.delivery_gate.approved = false; },
    detail => { detail.run.outputs.report_draft.markdown = ""; },
    detail => { detail.run.workflowPurpose = "research_starter"; },
    detail => { detail.run.conversationId = ""; },
  ]) {
    const detail = structuredClone(ready); mutate(detail);
    assert.equal(exports.workflowRevisionConversationId(detail), "");
  }
});

test("composer revision hint respects conversation scope and execution precedence", () => {
  const readyExpression = source.match(/const researchRevisionReady = (.+);/)[1];
  const expression = source.match(/placeholder=\{(!conversationId[^\n]+)\}\/\>/)[1];
  const state = { workspaceMode: "research", conversationId: "research-chat", researchConversationId: "research-chat", researchRevisionConversationId: "research-chat", researchConversationLocked: false, busy: false, activeRunIsWorkflowAI: false };
  const hint = overrides => {
    const context = { ...state, ...overrides };
    context.researchRevisionReady = vm.runInNewContext(readyExpression, context);
    return vm.runInNewContext(expression, context);
  };
  assert.equal(hint({}), "结论不对？向AI提出疑问并令其返修...");
  for (const overrides of [{ workspaceMode: "chat" }, { conversationId: "other" }, { researchRevisionConversationId: "" }]) {
    assert.equal(hint(overrides), "向 SciAide 描述研究问题，或输入 / 使用命令…");
  }
  assert.match(hint({ researchConversationLocked: true }), /科研流程正在自主推进/);
  assert.match(hint({ busy: true }), /正在回答，可先编辑下一条消息/);
  assert.match(hint({ busy: true, activeRunIsWorkflowAI: true }), /当前科研阶段由 AI 自主执行/);
  assert.match(source, /return \(\) => publishRevisionConversationId\(""\)/);
});

test("only the current completed event owns registration actions", () => {
  const renderer = segment("  renderTimelineRef.current =", "  const timelineTaskId =");
  assert.match(renderer, /entry.active && current && currentEvent/);
  assert.match(renderer, /event.type === entry.eventType/);
  assert.match(renderer, /workflowDeliverableRegistered\(runDetail, "report_draft"\)/);
  assert.match(renderer, /saveWorkflowDeliverable\("report_draft"\)/);
  assert.match(renderer, /return <ResearchTimelineHistory entry=\{entry\}/);
});

test("design delivery retains its non-empirical identity and shows acceptance basis", () => {
  const assessment = { status: "reviewed", kind: "research_design", label: "研究设计", summary: "AI 审查不等于科学结论已被独立验证。", checks: [{ criterionId: "criterion-1", status: "met", basis: "已列出变量与数据采集计划" }] };
  const html = renderToStaticMarkup(React.createElement(exports.ResearchAcceptanceDetails, { assessment }));
  assert.match(html, /已列出变量与数据采集计划/);
  assert.match(html, /AI 辅助审查/);
  assert.doesNotMatch(html, /实证研究报告/);
});

test("unverified delivery is not displayed as a successful acceptance", () => {
  const assessment = { status: "unverified", label: "研究交付稿", summary: "门禁未覆盖当前审查输入" };
  const html = renderToStaticMarkup(React.createElement(exports.ResearchDeliveryStatus, { assessment, runStatus: "completed" }));
  assert.match(html, /交付尚未验证/);
  assert.doesNotMatch(html, /交付检查已通过/);
});

test("normal work and accepted results never show an extra delivery status card", () => {
  for (const runStatus of ["queued", "running", "paused", "waiting_approval", "waiting_human_confirmation"]) {
    for (const status of ["pending", "blocked", "unverified", "reviewed"]) {
      assert.equal(renderToStaticMarkup(React.createElement(exports.ResearchDeliveryStatus, {runStatus, assessment: {status, label: "计算分析结果", summary: "任务尚未完成"}})), "");
    }
  }
  for (const status of ["pending", "reviewed"]) {
    assert.equal(renderToStaticMarkup(React.createElement(exports.ResearchDeliveryStatus, {runStatus:"completed", assessment:{status}})), "");
  }
});

test("existing retry card takes precedence and final artifacts retain acceptance details", () => {
  const assessment = {status:"revision_required", label:"结果", summary:"需要返修"};
  assert.equal(renderToStaticMarkup(React.createElement(exports.ResearchDeliveryStatus, {runStatus:"failed", assessment, recoveryVisible:true})), "");
  assert.match(renderToStaticMarkup(React.createElement(exports.ResearchDeliveryStatus, {runStatus:"failed", assessment})), /需要返修/);
  assert.match(source, /WorkflowResearchReportCard assessment=\{runDetail.deliveryAssessment\}/);
  assert.match(source, /WorkflowResearchDesignCard assessment=\{runDetail.deliveryAssessment\}/);
});

test("revision controls carry the selected stage and require confirmation for repeated side effects", () => {
  assert.match(source, /<ResearchRevisionControls/);
  assert.match(source, /retryStep\(retryableStep, nodeId, recommended, reviewSha\)/);
  assert.match(source, /revisionNodeId, useRecommendation, expectedReviewSha256/);
  assert.match(source, /confirmSideEffect: Boolean\(repeatsSideEffects && confirmRetry\)/);
});
