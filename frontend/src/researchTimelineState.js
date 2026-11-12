import { shareSnapshot } from './snapshotState.js';

// Keep the card's action and the feed's mounting condition on the same rule.
// Cancellation marks untouched downstream steps too, not just the current one.
export function workflowStepCanRetry(run, step, recovery) {
  if (recovery === null) return false;
  if (recovery && (step.id !== recovery.stepId || step.attempt !== recovery.attempt)) return false;
  if (['failed', 'interrupted'].includes(run.status)) {
    return ['failed', 'interrupted', 'outcome_unknown'].includes(step.status);
  }
  return run.status === 'cancelled' && step.status === 'cancelled'
    && step.ordinal === run.currentStep && ['agent_stage', 'ai_analysis'].includes(step.nodeKind)
    && run.compilation.nodes.some(node => node.id === step.nodeId && node.kind === step.nodeKind);
}

export function mergeResearchTimeline(current, incoming) {
  const entries = new Map(current.map(entry => [entry.id, entry]));
  for (const entry of incoming) entries.set(entry.id, shareSnapshot(entries.get(entry.id), entry));
  const result = [...entries.values()].sort((a, b) => {
    const left = BigInt(a.sequence), right = BigInt(b.sequence);
    return left < right ? -1 : left > right ? 1 : 0;
  });
  return result.length === current.length && result.every((entry, i) => entry === current[i]) ? current : result;
}

export function compareRunCreatedAt(left, right) {
  const precise = value => {
    const match = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z$/.exec(value ?? "");
    return match ? match[1] + "." + (match[2] ?? "").padEnd(9, "0") : null;
  };
  const a = precise(left), b = precise(right);
  if (a && b) return a < b ? -1 : a > b ? 1 : 0;
  const delta = Date.parse(left) - Date.parse(right);
  return Number.isFinite(delta) ? Math.sign(delta) : 0;
}

// Storage cursors remain append-only. Render same-turn human replies before
// the reserved final-assistant placeholder, never before a previous turn.
export function orderTimelineDialogue(entries) {
 const result = [];
 for (const entry of entries) {
  const m=entry.message;
  const placeholder = m?.role === 'user' && m.runId ? result.findIndex(e => e.message?.role==='assistant' && e.message.runId===m.runId) : -1;
  if (placeholder>=0) result.splice(placeholder,0,entry); else result.push(entry);
 }
 return result;
}

// Same business/technical boundary as the workflow runtime. A node name alone
// cannot turn a failed input binding into a scientific rejection.
export function workflowReviewGateRejected(run, step) {
  return step?.status === 'failed'
    && run.compilation.nodes.some(node => node.id === step.nodeId && node.tool?.qualifiedName === 'builtin.research.workflow.review.gate')
    && (step.errorCode === 'RESEARCH_REVIEW_NOT_APPROVED'
      || (step.errorCode === 'TOOL_INVOCATION_FAILED' && step.errorMessage?.startsWith('独立审查未通过')));
}
