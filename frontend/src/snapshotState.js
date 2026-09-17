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

export function conditionalViews(capacity = 8) {
  const cache = new Map();
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
    cache.delete(key);
    cache.set(key, {revision: update.revision, value});
    while (cache.size > capacity) cache.delete(cache.keys().next().value);
    return value;
  });
}
