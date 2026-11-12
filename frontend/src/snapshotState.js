// Reuse unchanged JSON branches without serializing large code/report values.
export function shareSnapshot(previous, next) {
  if (Object.is(previous, next)) return previous;
  if (!previous || !next || typeof previous !== 'object' || typeof next !== 'object' || Array.isArray(previous) !== Array.isArray(next)) return next;
  const keys = Object.keys(next), oldKeys = Object.keys(previous);
  let same = keys.length === oldKeys.length;
  const result = Array.isArray(next) ? [] : Object.create(null);
  for (const key of keys) {
    result[key] = shareSnapshot(previous[key], next[key]);
    if (!Object.hasOwn(previous, key) || result[key] !== previous[key]) same = false;
  }
  return same ? previous : result;
}

// Only concurrent reads are coalesced; settled payloads are never cached here.
export function singleFlight() {
  const pending = new Map();
  return (key, load) => {
    if (pending.has(key)) return pending.get(key);
    const promise = Promise.resolve().then(load).finally(() => { if (pending.get(key) === promise) pending.delete(key); });
    pending.set(key, promise);
    return promise;
  };
}

// Estimate retained data without creating another serialized copy of a large
// report. The budget is a cache budget, not a limit on any returned view.
export function snapshotWeight(value) {
  if (typeof value === 'string') return value.length * 2;
  if (!value || typeof value !== 'object') return 8;
  let size = 32;
  for (const key of Object.keys(value)) size += key.length * 2 + snapshotWeight(value[key]);
  return size;
}

export function conditionalViews(capacity = 8, maxBytes = 16 * 1024 * 1024) {
  const cache = new Map();
  let bytes = 0;
  const read = singleFlight();
  return (key, load) => read(key, async () => {
    const prior = cache.get(key);
    const update = await load(prior?.revision ?? '');
    if (update.unchanged && !prior) throw new Error('Snapshot cache is missing its baseline');
    let next = update.value;
    if (update.patch !== undefined) {
      if (!prior || !prior.value || typeof prior.value !== 'object') throw new Error('Snapshot patch is missing its baseline');
      next = Object.assign(Object.create(null), prior.value, update.patch);
      for (const key of update.removed ?? []) delete next[key];
    }
    const value = update.unchanged ? prior.value : shareSnapshot(prior?.value, next);
    // Another read can evict this baseline while load() is pending. The
    // captured baseline still decodes its response, but is no longer counted.
    const retained = cache.get(key);
    if (retained) bytes -= retained.weight;
    cache.delete(key);
    const weight = update.unchanged ? prior.weight : snapshotWeight(value);
    if (capacity > 0 && weight <= maxBytes) {
      cache.set(key, {revision: update.revision, value, weight}); bytes += weight;
    }
    while (cache.size > capacity || bytes > maxBytes) {
      const oldest = cache.keys().next().value;
      bytes -= cache.get(oldest).weight;
      cache.delete(oldest);
    }
    return value;
  });
}
