export function researchAnswerPresentation(text, citations = []) {
  const source = String(text ?? "");
  const fences = [...source.matchAll(/^\s*```json\s*\n([\s\S]*?)^\s*```\s*$/gim)];
  const fence = fences.at(-1);
  let summary = source;
  const payload = fence?.[1] ?? (/^\s*\{/.test(source) ? source : "");
  if (payload) {
    try {
      const result = JSON.parse(payload);
      if (!result || typeof result !== "object" || Array.isArray(result)) return { text: "", details: source };
      summary = [result.summary, result.methodSummary, result.metadata?.methodSummary, result.normalizedQuestion]
        .find(value => typeof value === "string" && value.trim()) ?? "";
      if (!summary && Array.isArray(result.claimSummary)) {
        summary = result.claimSummary.filter(value => typeof value === "string").join("\n\n");
      }
    } catch {
      return { text: "", details: source };
    }
  } else if (/```json/i.test(source)) {
    return { text: "", details: source };
  }
  const bound = new Set((citations ?? []).map(value => value.reference));
  // Unbound identifiers remain inspectable in the raw audit, not fake links.
  summary = summary.replace(/\[K-[0-9A-F]{12}\]/g, marker => bound.has(marker) ? marker : "").trim();
  return { text: summary, details: summary === source ? "" : source };
}
