const referencePattern = /^\[K-[0-9A-F]{12}\]$/;
const record = value => value && typeof value === "object" && !Array.isArray(value) ? value : null;

// Read only the snapshot belonging to these outputs, never a current task lookup.
export function reportCitationSnapshot(outputs) {
  const root = record(outputs);
  const version = record(record(root?.report)?.version);
  const values = version ? version.citations : root?.citations;
  return Array.isArray(values) ? values : [];
}

export function citationDisplayMap(values) {
  const items = (Array.isArray(values) ? values : []).filter(value =>
    record(value) && typeof value.reference === "string" && referencePattern.test(value.reference)
    && typeof value.sourceName === "string" && value.sourceName.trim()
    && typeof value.quote === "string" && value.quote.trim()
    && typeof value.quoteSha256 === "string" && /^[0-9a-f]{64}$/i.test(value.quoteSha256));
  const counts = new Map();
  for (const item of items) counts.set(item.reference, (counts.get(item.reference) || 0) + 1);
  const ordered = items.map((value, index) => ({ value, ordinal: Number.isInteger(value.ordinal) && value.ordinal >= 0 ? value.ordinal : index }))
    .sort((left, right) => left.ordinal - right.ordinal);
  const result = new Map();
  for (const { value } of ordered) {
    if (counts.get(value.reference) !== 1) continue;
    result.set(value.reference, { value, number: result.size + 1 });
  }
  return result;
}

export function citationTextSegments(text) {
  return String(text ?? "").split(/(\[K-[0-9A-F]{12}\])/g).filter(Boolean)
    .map(value => ({ text: value, reference: referencePattern.test(value) ? value : "" }));
}

export function citationSourceURL(citation) {
  const data = record(record(citation?.bibliography)?.data);
  const doi = typeof data?.doi === "string" ? data.doi.trim() : "";
  if (/^10\.\d{4,9}\/\S+$/i.test(doi)) return `https://doi.org/${encodeURI(doi).replaceAll("#", "%23").replaceAll("?", "%3F")}`;
  if (typeof data?.url !== "string") return "";
  try {
    const url = new URL(data.url);
    return ["https:", "http:"].includes(url.protocol) && !url.username && !url.password ? url.href : "";
  } catch {
    return "";
  }
}
