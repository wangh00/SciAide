import {useEffect, useState} from 'react';

type RouteStep = {id: string; nodeId: string; ordinal: number; status: string; attempt: number};
type RouteEvent = {id: string; sequence: number; type: string; payload: Record<string, unknown>; createdAt: string};
type RouteDetail = {
  run: {id: string; researchStarterRunId?: string; compilation: {nodes: Array<{id: string; name: string}>}};
  steps: RouteStep[]; events: RouteEvent[];
};
type RouteRound = {id: string; start: number; title: string; summary: string; createdAt: string};

// Derive display only from committed host revision events. An increased attempt,
// pending proposal, model correction or automatic Python repair is not a newly
// confirmed user execution plan. No execution state or frozen route is changed.
export function researchRouteView(detail: RouteDetail) {
  const steps = [...detail.steps].sort((a, b) => a.ordinal - b.ordinal);
  const rounds: RouteRound[] = [{id: 'initial', start: steps[0]?.ordinal ?? 0, title: '初始执行路线', summary: '首次确认的完整路线', createdAt: ''}];
  const seen = new Set<string>();
  for (const event of [...detail.events].sort((a, b) => a.sequence - b.sequence)) {
    if (seen.has(event.id)) continue;
    const payload = event.payload;
    let start: RouteStep | undefined;
    let title = '', summary = '';
    if (event.type === 'workflow.user_revision_queued') {
      const proposal = payload.proposal as {nodeId?: string; summary?: string} | undefined;
      start = steps.find(s => s.nodeId === proposal?.nodeId && s.ordinal === payload.startOrdinal);
      title = '对话返修路线'; summary = proposal?.summary ?? '';
    } else if (event.type === 'workflow.review_revision_queued') {
      start = steps.find(s => s.nodeId === payload.producerNodeId && s.id === payload.producerStepId);
      title = '审查返修路线'; summary = '按已确认的审查意见局部返修';
    } else if (event.type === 'workflow.upstream_revision_queued' && payload.manual === true) {
      start = steps.find(s => s.nodeId === payload.producerNodeId && s.id === payload.producerStepId);
      title = '确认修复路线'; summary = typeof payload.reason === 'string' ? payload.reason : '';
    }
    if (!start) continue;
    seen.add(event.id);
    rounds.push({id: event.id, start: start.ordinal, title, summary, createdAt: event.createdAt});
  }
  const current = rounds[rounds.length - 1]!;
  const currentSteps = steps.filter(s => s.ordinal >= current.start);
  const planning = rounds.length === 1 && Boolean(detail.run.researchStarterRunId);
  const completed = currentSteps.filter(s => s.status === 'completed').length + (planning ? 1 : 0);
  const total = currentSteps.length + (planning ? 1 : 0);
  return {steps, rounds, current, currentSteps, planning, completed, total, history: rounds.slice(0, -1)};
}

export function ResearchRoute({detail, statusLabels}: {detail: RouteDetail; statusLabels: Record<string, string>}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  useEffect(() => setHistoryOpen(false), [detail.run.id]);
  const view = researchRouteView(detail);
  const names = new Map(detail.run.compilation.nodes.map(n => [n.id, n.name]));
  const name = (s: RouteStep | undefined) => s ? names.get(s.nodeId) || `阶段 ${s.ordinal + 1}` : '未记录起点';
  return <section className="research-route-steps" aria-label="当前一轮研究路线">
    {view.history.length > 0 && <div className="research-route-rounds">
      <button type="button" aria-expanded={historyOpen} onClick={() => setHistoryOpen(!historyOpen)}>历史路线 · {view.history.length}</button>
      <p>从「{name(view.currentSteps[0])}」继续{view.current.start > (view.steps[0]?.ordinal ?? 0) ? '；之前的结果沿用。' : '；本轮重新执行完整路线。'}</p>
      {historyOpen && <section className="research-route-history" aria-label="历史研究路线">
        <p>仅查看各轮已确认的执行范围；不代表历史阶段全部成功，也不会重新执行。</p>
        {view.rounds.map((round, i) => <details key={round.id}>
          <summary>第 {i + 1} 轮 · {round.title}{round.id === view.current.id ? ' · 当前' : ''}</summary>
          <p>{round.summary}</p>{round.createdAt && <time dateTime={round.createdAt}>{new Date(round.createdAt).toLocaleString()}</time>}
          <ol>{view.steps.filter(s => s.ordinal >= round.start).map(s => <li key={s.id}>{name(s)}</li>)}</ol>
        </details>)}
      </section>}
    </div>}
    <header><b>{view.history.length ? '本轮返修路线' : '研究路线'}</b><span>{Math.round(view.completed / Math.max(view.total, 1) * 100)}%</span></header>
    <div className="research-route-current-list">
      {view.planning && <article className="completed"><i>✓</i><span><b><span>AI 路线规划</span></b></span></article>}
      {view.currentSteps.map((s, i) => <article key={s.id} className={s.status} title={statusLabels[s.status]}>
        <i>{s.status === 'completed' ? '✓' : i + 1 + (view.planning ? 1 : 0)}</i>
        <span><b><span>{name(s)}</span>{s.attempt > 1 && <em title={`已执行 ${s.attempt} 次`}>{s.attempt}</em>}</b></span>
      </article>)}
    </div>
  </section>;
}
