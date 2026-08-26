export type WorkflowEnvironmentManifest = {
  stepId: string;
  stepName: string;
  implementation: string;
  version: string;
  architecture: string;
  environmentFingerprint: string;
  freezeSha256: string;
  baseExecutableSha256: string;
  lockCount: number;
};

export type WorkflowAnalysisManifest = {
  stepId: string;
  stepName: string;
  kernelId: string;
  executionId: string;
  sequence: number;
  codeSha256: string;
  environmentFingerprint: string;
  reproductionSha256: string;
  inputHashes: { path: string; sha256: string }[];
  outputHashes: { path: string; sha256: string }[];
};

export type WorkflowEvidenceNode = {
  id: string;
  stage: "evidence" | "analysis" | "artifact" | "report" | "export";
  label: string;
  detail: string;
  sha256: string;
  sourceStepId: string;
  sourceStepName: string;
  ordinal: number;
};

export type WorkflowEvidenceEdge = { from: string; to: string; label: string };

export function deriveWorkflowRunEvidence(detail: unknown): {
  environment: WorkflowEnvironmentManifest | null;
  analyses: WorkflowAnalysisManifest[];
  graph: { nodes: WorkflowEvidenceNode[]; edges: WorkflowEvidenceEdge[] };
};
