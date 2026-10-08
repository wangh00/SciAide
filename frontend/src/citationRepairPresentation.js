/** Advisory only: exact offered snapshots remain the sole decision payload. */
export function citationRepairModel(step, selectedKeys) {
  const context = step?.nodeKind === 'citation_selection' ? step.input?.citationRepairContext : null;
  if (!context || typeof context.reviewOutputSha256 !== 'string' || !context.reviewOutputSha256) return null;
  const candidates = Array.isArray(step.input.candidates) ? step.input.candidates.filter(value => value && typeof value.id === 'string') : [];
  const available = new Set(candidates.map(value => value.reference).filter(Boolean));
  const previous = new Set((Array.isArray(step.input.previousSelectedReferences) ? step.input.previousSelectedReferences : []).filter(ref => available.has(ref)));
  const selected = new Set(candidates.filter(value => selectedKeys.includes(JSON.stringify(value))).map(value => value.reference));
  const mentioned = new Set((Array.isArray(context.mentionedReferences) ? context.mentionedReferences : []).filter(ref => available.has(ref)));
  const recommendedRefs = step.input.screening?.recommendedReferences;
  const recommended = new Set((Array.isArray(recommendedRefs) ? recommendedRefs : []).filter(ref => available.has(ref)));
  const missingMentioned = candidates.filter(value => mentioned.has(value.reference) && !selected.has(value.reference));
  const missingRecommended = candidates.filter(value => recommended.has(value.reference) && !selected.has(value.reference));
  const added = [...selected].filter(ref => !previous.has(ref)).length;
  const removed = [...previous].filter(ref => !selected.has(ref)).length;
  return { previousCount: previous.size, added, removed, unchanged: added === 0 && removed === 0,
    mentioned, missingMentioned, missingRecommended,
    corrections: Array.isArray(context.requiredCorrections) ? context.requiredCorrections.filter(value => typeof value === 'string' && value.trim()) : [],
    repeatedIssueCount: Number.isInteger(context.repeatedIssueCount) && context.repeatedIssueCount > 0 ? context.repeatedIssueCount : 0 };
}

// Explicit user action: add exact offered snapshots without dropping any choice.
export function supplementCitationKeys(selectedKeys, candidates) {
  return [...new Set([...selectedKeys, ...candidates.map(value => JSON.stringify(value))])];
}
