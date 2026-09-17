export function retryStatusLabel(retry) {
  if (!retry) return '';
  const action = retry.phase === 'request' ? '请求重试' : retry.phase === 'stream' ? '连接重连' : '';
  if (!action) return retry.message || '';
  const interval = retry.delayMillis > 0 ? ` · 本次重试间隔 ${(retry.delayMillis / 1000).toLocaleString('zh-CN', { maximumFractionDigits: 1 })} 秒` : '';
  return `${action} ${retry.attempt}/${retry.maxAttempts}${interval}`;
}
export function applyRetryEvent(current, event) {
  const id = event.aggregateId;
  if (!id) return current;
  if (event.type === 'run.retrying' && event.payload?.retry) return {...current, [id]: event.payload.retry};
  if (['run.retry.recovered','run.completed','run.failed','run.cancelled','run.interrupted'].includes(event.type) && current[id]) {
    const next = {...current}; delete next[id]; return next;
  }
  return current;
}
