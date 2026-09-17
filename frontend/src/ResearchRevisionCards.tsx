import { useEffect, useState } from 'react';

export type ResearchRevisionProposal = {
  id: string; runId: string; chatRunId: string; userMessageId: string;
  status: 'pending' | 'superseded' | 'confirmed'; canConfirm: boolean;
  nodeId: string; label: string; summary: string; changes: string[]; reason: string;
  affectedStages: string[]; repeatsSideEffects: boolean; createdAt: string;
};

export function ResearchRevisionCards({ proposals, busy, conversationId, confirm }: {
  proposals: ResearchRevisionProposal[]; busy: boolean; conversationId: string;
  confirm: (proposalId: string) => void;
}) {
  const [invalidated, setInvalidated] = useState<Set<string>>(new Set());
  useEffect(() => {
    const invalidate = (event: Event) => {
      if ((event as CustomEvent<string>).detail === conversationId) {
        setInvalidated(current => new Set([...current, ...proposals.filter(p => p.status === 'pending').map(p => p.id)]));
      }
    };
    window.addEventListener('research-discussion-sent', invalidate);
    return () => window.removeEventListener('research-discussion-sent', invalidate);
  }, [conversationId, proposals]);
  if (!proposals.length) return null;
  return <div className="research-revision-proposals">
    {proposals.map(proposal => {
      const superseded = proposal.status === 'superseded' || invalidated.has(proposal.id);
      const pending = proposal.status === 'pending' && !superseded;
      if (pending && !proposal.canConfirm) return <p key={proposal.id} role="status">正在整理返修方案</p>;
      const body = <>
        <p className="research-revision-goal">{proposal.summary}</p>
        <ul>{proposal.changes.map((change, index) => <li key={index}>{change}</li>)}</ul>
        <dl><dt>返修起点</dt><dd>{proposal.label}</dd><dt>将重新执行</dt><dd>{proposal.affectedStages.join(' → ')}</dd></dl>
        <details><summary>起点依据</summary><p>{proposal.reason}</p></details>
      </>;
      return <section key={proposal.id} className={`research-revision-proposal ${pending ? 'pending' : 'historical'}`} aria-label="返修确认方案">
        {pending ? <>
          <header><h3>确认返修方案</h3><span>待确认</span></header>
          {body}
          <footer><p>{proposal.repeatsSideEffects ? '包含重新计算或执行操作。' : ''}旧版本保留，后续审查和交付检查将重新执行，可能产生模型调用费用。</p>
            <button type="button" className="primary" disabled={busy} onClick={() => confirm(proposal.id)}>{busy ? '正在处理…' : '确认方案并开始返修'}</button>
          </footer>
        </> : <details><summary>{superseded ? '旧方案已失效' : '已确认执行'} · {proposal.summary}</summary>{body}</details>}
      </section>;
    })}
  </div>;
}
