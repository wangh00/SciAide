import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import vm from "node:vm";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { citationReferenceFromURL, remarkSciAideCitations, safeMarkdownURL } from "./markdownRender.js";
import { citationDisplayMap, citationSourceURL, citationTextSegments, reportCitationSnapshot } from "./citationPresentation.js";

const first = { reference: "[K-0123456789AB]", sourceName: "paper.md", locator: "lines:1-8", quote: "Original abstract.", quoteSha256: "a".repeat(64) };
const second = { ...first, reference: "[K-111111111111]", quote: "Second abstract.", quoteSha256: "b".repeat(64) };

test("report citations come only from its frozen publication or selected-output snapshot", () => {
  const older = { citations: [first], report: { version: { citations: [{ ...first, bibliography: { data: { title: "Frozen title" } } }] } } };
  const newer = { citations: [second] };
  assert.equal(reportCitationSnapshot(older)[0].bibliography.data.title, "Frozen title");
  assert.deepEqual(reportCitationSnapshot(newer), [second]);
  assert.deepEqual(reportCitationSnapshot({ citations: [first], report: { version: { citations: [] } } }), []);
  assert.deepEqual(reportCitationSnapshot({ citations: [first], report: { version: {} } }), []);
  assert.deepEqual(reportCitationSnapshot({ report_draft: { citations: [first] } }), []);
  assert.deepEqual(reportCitationSnapshot(null), []);
});

test("display numbering follows frozen ordinals or frozen array order without mutating snapshots", () => {
  const values = [{ ...first, ordinal: 1 }, { ...second, ordinal: 0 }];
  const original = structuredClone(values);
  assert.deepEqual([...citationDisplayMap(values).keys()], [second.reference, first.reference]);
  assert.equal(citationDisplayMap(values).get(first.reference).number, 2);
  assert.deepEqual(values, original);
  assert.deepEqual([...citationDisplayMap([first, second]).keys()], [first.reference, second.reference]);
});

test("missing or ambiguous snapshots cannot create a clickable citation", () => {
  assert.equal(citationDisplayMap(null).size, 0);
  assert.equal(citationDisplayMap([null, {}, { ...first, quote: "" }, { ...first, quoteSha256: "invalid" }, { ...first, reference: "K-0123456789AB" }]).size, 0);
  assert.equal(citationDisplayMap([first, { ...first, quote: "Different text" }, second]).size, 1);
});

test("structured preview splits only exact citation markers and keeps surrounding text", () => {
  assert.deepEqual(citationTextSegments(`Result ${first.reference} ${second.reference}.`), [
    { text: "Result ", reference: "" }, { text: first.reference, reference: first.reference },
    { text: " ", reference: "" }, { text: second.reference, reference: second.reference }, { text: ".", reference: "" },
  ]);
  assert.equal(citationTextSegments("[K-wrong]")[0].reference, "");
});

test("source links use frozen bibliography and exclude active, local and credentialed URLs", () => {
  const withData = data => ({ ...first, bibliography: { data } });
  assert.equal(citationSourceURL(withData({ doi: "10.1234/paper", url: "https://example.org" })), "https://doi.org/10.1234/paper");
  assert.equal(citationSourceURL(withData({ url: "https://pubmed.ncbi.nlm.nih.gov/123/" })), "https://pubmed.ncbi.nlm.nih.gov/123/");
  for (const url of ["javascript:alert(1)", "file:///C:/secret", "data:text/html,test", "https://user:pass@example.org/", "/paper"]) {
    assert.equal(citationSourceURL(withData({ url })), "");
  }
  assert.equal(citationSourceURL(first), "");
});

const source = await readFile(new URL("./App.tsx", import.meta.url), "utf8");
function segment(start, end) {
  const from = source.indexOf(start), to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from);
  return source.slice(from, to);
}
const compiled = ts.transpileModule('import React, { Fragment, useState, useEffect, useMemo, useRef } from "react";\n' +
  segment("function CitationMarker(", "function runDuration(") +
  segment("function ArtifactDocumentPreview(", "function ArtifactLibrary(") +
  segment("const researchTimelineLabels:", "const workflowAnalysisMethods") +
  "\nexport { WorkflowResearchReportCard, ResearchTimelineHistory, ArtifactDocumentPreview, CitedMarkdown };", {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
}).outputText;
const exports = {};
vm.runInNewContext(compiled, {
  exports, require: createRequire(import.meta.url), ReactMarkdown, remarkGfm, citationDisplayMap, citationTextSegments,
  reportCitationSnapshot, citationSourceURL, citationReferenceFromURL, remarkSciAideCitations, safeMarkdownURL,
  Icon: () => null, ResearchAcceptanceDetails: () => null,
});
const render = (component, props) => renderToStaticMarkup(React.createElement(component, props));
const report = { markdown: `## Results\n\nFinding ${first.reference}.`, methodSummary: "Method", confidence: "medium", claimSummary: ["Finding"], limitations: [] };

test("final report card renders numbered references using the supplied snapshot", () => {
  const html = render(exports.WorkflowResearchReportCard, { report, citations: [first], deliveryLabel: "报告", busy: "", artifactCount: 0 });
  assert.match(html, /class="citation-marker"/);
  assert.match(html, />\[1\]<\/button>/);
  assert.doesNotMatch(html, /citation-unverified|K-0123456789AB/);
  assert.match(source, /citations=\{reportCitationSnapshot\(runDetail.run.outputs\)\}/);
});

test("history binds its own event snapshot, and never borrows current run references", () => {
  const entry = { id: "old-completion", kind: "event", eventType: "workflow.completed", createdAt: "2026-09-16T14:30:00Z", snapshot: { outputs: { report_draft: report, citations: [first] } } };
  assert.match(render(exports.ResearchTimelineHistory, { entry }), />\[1\]<\/button>/);
  entry.snapshot.outputs.citations = [second];
  const html = render(exports.ResearchTimelineHistory, { entry });
  assert.match(html, /citation-unverified/);
  assert.doesNotMatch(html, /class="citation-marker"/);
});

test("artifact headings, paragraphs, tables, lists and quotes resolve citations while code stays literal", () => {
  const blocks = ["heading", "paragraph", "list_item", "quote", "code"].map(kind => ({ kind, text: `Finding ${first.reference}`, level: 1 }));
  blocks.push({ kind: "table", rows: [["Result"], [first.reference]] });
  const html = render(exports.ArtifactDocumentPreview, { document: { blocks }, citations: [first] });
  assert.equal((html.match(/class="citation-marker"/g) || []).length, 5);
  assert.match(html, /<pre>Finding \[K-0123456789AB\]<\/pre>/);
  assert.doesNotMatch(html, /citation-unverified/);
  assert.match(source, /preview.versionId !== version.id/);
  assert.match(source, /ArtifactDocumentPreview key=\{version.id\} document=\{preview.document\} citations=\{version.citations\}/);
});

test("unknown report citations stay explicit, and markdown code does not become a reference button", () => {
  const html = render(exports.CitedMarkdown, { text: `${first.reference}\n\n\`${second.reference}\``, citations: [second] });
  assert.match(html, /citation-unverified/);
  assert.match(html, /<code>\[K-111111111111\]<\/code>/);
  assert.doesNotMatch(html, /class="citation-marker"/);
});
