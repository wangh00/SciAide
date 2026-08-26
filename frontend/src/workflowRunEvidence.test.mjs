import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { deriveWorkflowRunEvidence } from "./workflowRunEvidence.js";

const hash = (character) => character.repeat(64);

test("derives the frozen environment manifest and research artifact graph", () => {
  const detail = {
    run: { compilation: { nodes: [
      { id: "select", name: "人工选择引用" },
      { id: "environment", name: "确保项目 Python 环境" },
      { id: "analysis", name: "生成 CSV 与 SVG" },
      { id: "report", name: "发布可信报告" },
    ] } },
    steps: [
      { id: "s1", nodeId: "select", output: { structured: {}, citations: [{ id: "c1" }] } },
      { id: "s2", nodeId: "environment", output: { structured: { implementation: "CPython", baseExecutableVersion: "3.12.3", architecture: "64bit", environmentFingerprint: hash("e"), freezeSha256: hash("f"), baseExecutableSha256: hash("b"), lock: ["pip==24.0"] } } },
      { id: "s3", nodeId: "analysis", output: { structured: { kernelId: "k1", executionId: "x1", sequence: 1, codeSha256: hash("c"), environmentFingerprint: hash("e"), reproductionSha256: hash("r"), inputSha256: { "$data": hash("i") }, outputSha256: { "analysis-output/result.csv": hash("1"), "analysis-output/result.svg": hash("2") } }, artifacts: [{ name: "result.csv", workspacePath: "analysis-output/result.csv" }, { name: "result.svg", workspacePath: "analysis-output/result.svg" }] } },
      { id: "s4", nodeId: "report", output: { structured: { artifact: { id: "report-a", name: "可信科研报告" }, version: { fileName: "report.md", sha256: hash("m") }, docx: { id: "docx-a", fileName: "report.docx", generatorVersion: "p6.2-v2", sha256: hash("d") }, pdf: { id: "pdf-a", fileName: "report.pdf", generatorVersion: "p6.2-v2", sha256: hash("p") } }, artifacts: [{ id: "report-a", name: "可信科研报告" }, { id: "docx-a", name: "report.docx" }, { id: "pdf-a", name: "report.pdf" }] } },
    ],
  };
  const evidence = deriveWorkflowRunEvidence(detail);
  assert.equal(evidence.environment?.version, "3.12.3");
  assert.equal(evidence.environment?.lockCount, 1);
  assert.equal(evidence.analyses[0]?.reproductionSha256, hash("r"));
  assert.deepEqual(evidence.analyses[0]?.outputHashes.map((item) => item.path), ["analysis-output/result.csv", "analysis-output/result.svg"]);
  assert.deepEqual(evidence.graph.nodes.map((node) => node.label), ["1 条本地 Citation", "result.csv", "result.svg", "可信科研报告", "report.docx", "report.pdf"]);
  assert.equal(evidence.graph.edges.filter((edge) => edge.label === "分析输入").length, 2);
  assert.equal(evidence.graph.edges.filter((edge) => edge.label === "确定性导出").length, 2);
});

test("does not invent manifest or artifact evidence for an unfinished run", () => {
  const evidence = deriveWorkflowRunEvidence({ run: { compilation: { nodes: [] } }, steps: [{ id: "queued", output: {} }] });
  assert.equal(evidence.environment, null);
  assert.deepEqual(evidence.analyses, []);
  assert.deepEqual(evidence.graph, { nodes: [], edges: [] });
});

test("workflow evidence views remain bounded and stack on narrow screens", async () => {
  const css = await readFile(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(css, /\.workflow-run-evidence-grid\s*\{[^}]*grid-template-columns:\s*repeat\(2,minmax\(0,1fr\)\)/s);
  assert.match(css, /\.workflow-artifact-graph\s*\{[^}]*overflow:\s*auto/s);
  assert.match(css, /@media \(max-width:\s*720px\)[\s\S]*?\.workflow-run-evidence-grid\s*\{[^}]*grid-template-columns:\s*minmax\(0,1fr\)/s);
});
