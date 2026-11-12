// Call the purpose-built read-only heartbeat directly, not backend(), so a
// health pulse cannot invalidate data caches or alter a task.
export function startUIHeartbeat(surface = window) {
  let pending = false, stopped = false;
  const pulse = async () => {
    if (pending || stopped) return;
    const send = surface.go?.wails?.SystemFacade?.UIHeartbeat;
    if (!send) return;
    pending = true;
    try { await send(surface.document.hidden); } catch { /* Native host owns recovery. */ }
    finally { pending = false; }
  };
  void pulse();
  const timer = surface.setInterval(pulse, 5000);
  surface.document.addEventListener('visibilitychange', pulse);
  const stop = () => { stopped = true; surface.clearInterval(timer); surface.document.removeEventListener('visibilitychange', pulse); surface.removeEventListener('pagehide', stop); };
  surface.addEventListener('pagehide', stop);
  return stop;
}

const viewKey = 'sciaide-ui-recovery-v1';
export function readUIView(surface = window) {
  try {
    const value = JSON.parse(surface.sessionStorage.getItem(viewKey) || 'null');
    if (!value || typeof value.projectId !== 'string' || typeof value.conversationId !== 'string' || !['chat','research'].includes(value.mode)) return null;
    const attachments = Array.isArray(value.attachments) ? value.attachments.filter(a => a && typeof a.id === 'string' && typeof a.originalName === 'string' && (typeof a.draftPath === 'string' || typeof a.projectId === 'string' && typeof a.format === 'string' && ['parsing','ready','failed'].includes(a.status))).slice(0,64) : [];
    return {projectId:value.projectId.slice(0,128),conversationId:value.conversationId.slice(0,128),mode:value.mode,draft:typeof value.draft==='string'?value.draft.slice(0,200000):'',attachments};
  } catch { return null; }
}
export function saveUIView(value, surface = window) {
  try { surface.sessionStorage.setItem(viewKey, JSON.stringify(value)); } catch { /* Storage is optional; tasks remain durable in SQLite. */ }
}
