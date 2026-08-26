function record(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : null;
}

function text(value) {
  return typeof value === "string" ? value.trim() : "";
}

function basename(value) {
  const normalized = text(value).replaceAll("\\", "/");
  return normalized.split("/").filter(Boolean).at(-1) || normalized;
}

function hashEntries(value) {
  const source = record(value);
  if (!source) return [];
  return Object.entries(source)
    .filter(([path, hash]) => text(path) && text(hash))
    .map(([path, hash]) => ({ path, sha256: hash }))
    .sort((left, right) => left.path.localeCompare(right.path));
}

function artifactRefs(value) {
  return Array.isArray(value) ? value.filter((item) => record(item)) : [];
}

export function deriveWorkflowRunEvidence(detail) {
  const root = record(detail);
  const run = record(root?.run);
  const compilation = record(run?.compilation);
  const compiledNodes = Array.isArray(compilation?.nodes) ? compilation.nodes : [];
  const nodeNames = new Map(compiledNodes.map((node) => [text(node?.id), text(node?.name)]));
  const steps = Array.isArray(root?.steps) ? root.steps : [];
  let environment = null;
  const analyses = [];
  const nodes = new Map();
  const edges = [];
  const citationNodes = [];
  const analysisNodes = [];
  const reportNodes = [];

  const addNode = (value) => {
    if (!value.id || !value.label) return;
    const existing = nodes.get(value.id);
    nodes.set(value.id, existing ? { ...existing, ...value } : value);
  };
  const addEdge = (from, to, label) => {
    if (!from || !to || from === to || edges.some((edge) => edge.from === from && edge.to === to && edge.label === label)) return;
    edges.push({ from, to, label });
  };

  for (const [ordinal, step] of steps.entries()) {
    const stepRecord = record(step);
    if (!stepRecord) continue;
    const stepId = text(stepRecord.id) || `step-${ordinal}`;
    const nodeId = text(stepRecord.nodeId);
    const stepName = nodeNames.get(nodeId) || nodeId || `步骤 ${ordinal + 1}`;
    const output = record(stepRecord.output);
    const structured = record(output?.structured);
    if (!structured) continue;

    const environmentFingerprint = text(structured.environmentFingerprint);
    if (environmentFingerprint && text(structured.baseExecutableVersion)) {
      environment = {
        stepId,
        stepName,
        implementation: text(structured.implementation),
        version: text(structured.baseExecutableVersion),
        architecture: text(structured.architecture),
        environmentFingerprint,
        freezeSha256: text(structured.freezeSha256),
        baseExecutableSha256: text(structured.baseExecutableSha256),
        lockCount: Array.isArray(structured.lock) ? structured.lock.length : 0,
      };
    }

    const reproductionSha256 = text(structured.reproductionSha256);
    const outputHashes = hashEntries(structured.outputSha256);
    if (reproductionSha256 || outputHashes.length) {
      const analysis = {
        stepId,
        stepName,
        kernelId: text(structured.kernelId),
        executionId: text(structured.executionId),
        sequence: Number.isInteger(structured.sequence) ? structured.sequence : 0,
        codeSha256: text(structured.codeSha256),
        environmentFingerprint,
        reproductionSha256,
        inputHashes: hashEntries(structured.inputSha256),
        outputHashes,
      };
      analyses.push(analysis);
      for (const outputHash of outputHashes) {
        const id = `workspace:${outputHash.path}`;
        analysisNodes.push(id);
        addNode({ id, stage: "analysis", label: basename(outputHash.path), detail: outputHash.path, sha256: outputHash.sha256, sourceStepId: stepId, sourceStepName: stepName, ordinal });
      }
    }

    const citations = Array.isArray(output.citations) ? output.citations : [];
    if (citations.length) {
      const id = `citations:${stepId}`;
      citationNodes.push(id);
      addNode({ id, stage: "evidence", label: `${citations.length} 条本地 Citation`, detail: stepName, sha256: "", sourceStepId: stepId, sourceStepName: stepName, ordinal });
    }

    for (const [index, ref] of artifactRefs(output.artifacts).entries()) {
      const idValue = text(ref.id);
      const path = text(ref.workspacePath);
      const name = text(ref.name) || basename(path) || `产物 ${index + 1}`;
      const id = idValue ? `artifact:${idValue}` : path ? `workspace:${path}` : `step-artifact:${stepId}:${index}`;
      const matchingHash = outputHashes.find((item) => item.path === path)?.sha256 || "";
      addNode({ id, stage: path ? "analysis" : "artifact", label: name, detail: path || text(ref.mimeType) || idValue, sha256: matchingHash, sourceStepId: stepId, sourceStepName: stepName, ordinal });
      if (path && !analysisNodes.includes(id)) analysisNodes.push(id);
    }

    const reportArtifact = record(structured.artifact);
    const reportVersion = record(structured.version);
    if (reportArtifact && text(reportArtifact.id)) {
      const reportId = `artifact:${text(reportArtifact.id)}`;
      reportNodes.push(reportId);
      addNode({
        id: reportId,
        stage: "report",
        label: text(reportArtifact.name) || text(reportVersion?.fileName) || "Markdown 报告",
        detail: text(reportVersion?.fileName) || text(reportArtifact.id),
        sha256: text(reportVersion?.sha256),
        sourceStepId: stepId,
        sourceStepName: stepName,
        ordinal,
      });
      for (const [format, value] of [["DOCX", structured.docx], ["PDF", structured.pdf]]) {
        const exported = record(value);
        if (!exported || !text(exported.id)) continue;
        const exportId = `artifact:${text(exported.id)}`;
        addNode({ id: exportId, stage: "export", label: text(exported.fileName) || `${format} 导出`, detail: `${format} · ${text(exported.generatorVersion)}`, sha256: text(exported.sha256), sourceStepId: stepId, sourceStepName: stepName, ordinal });
        addEdge(reportId, exportId, "确定性导出");
      }
    }
  }

  for (const reportId of reportNodes) {
    for (const citationId of citationNodes) addEdge(citationId, reportId, "核验证据");
    for (const analysisId of [...new Set(analysisNodes)]) addEdge(analysisId, reportId, "分析输入");
  }

  const rank = { evidence: 0, analysis: 1, artifact: 2, report: 3, export: 4 };
  const graphNodes = [...nodes.values()].sort((left, right) => (rank[left.stage] ?? 9) - (rank[right.stage] ?? 9) || left.ordinal - right.ordinal || left.label.localeCompare(right.label));
  return { environment, analyses, graph: { nodes: graphNodes, edges } };
}
