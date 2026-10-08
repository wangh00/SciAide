export function retryStatusLabel(retry) {
  if (!retry) return '';
  const action = retry.reason === 'rate_limit' ? '模型服务限流，等待后重试' : retry.reason === 'service_unavailable' ? '模型服务暂时不可用，等待后重试' : retry.phase === 'request' ? '请求重试' : retry.phase === 'stream' ? '连接重连' : '';
  if (!action) return retry.message || '';
  const interval = retry.delayMillis > 0 ? ` · 本次重试间隔 ${(retry.delayMillis / 1000).toLocaleString('zh-CN', { maximumFractionDigits: 1 })} 秒` : '';
  return `${action} ${retry.attempt}/${retry.maxAttempts}${interval}`;
}
export function toolDecisionLabel(activity) {
  if (activity?.errorCode === 'RESEARCH_REVIEW_NOT_APPROVED' || (activity?.toolName === 'builtin.research.workflow.review.gate' && activity?.status === 'failed' && (!activity.errorCode || (activity.errorCode === 'TOOL_INVOCATION_FAILED' && activity.errorMessage?.startsWith('独立审查未通过'))))) return '审查未通过 · 待修改';
  return '';
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
