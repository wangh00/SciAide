import { shareSnapshot } from './snapshotState.js';

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
