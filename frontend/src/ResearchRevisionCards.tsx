import { useEffect, useState } from 'react';
import { ResearchCard, ResearchCardFrame } from './ResearchCard';

export type ResearchRevisionProposal = {
  id: string; runId: string; chatRunId: string; userMessageId: string;
  status: 'pending' | 'superseded' | 'confirmed'; canConfirm: boolean;
  nodeId: string; label: string; summary: string; changes: string[]; reason: string;
  affectedStages: string[]; repeatsSideEffects: boolean; createdAt: string;
  reusedStages?: string[];
  materials?: Array<{attachmentId: string; name: string; sha256: string}>;
  dataVersion?: {previousPaths: string[]; files: Array<{relativePath: string; name: string; sha256: string; attachmentId?: string}>};
  sourceVersion?: {files: Array<{relativePath: string; name: string; sha256: string; attachmentId: string}>};
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
        <section className="research-revision-scope-summary" aria-label="返修执行范围">
          <div><span>返修起点</span><strong>{proposal.label}</strong></div>
          <div><span>将重新执行 · {proposal.affectedStages.length} 个阶段</span><ol className="research-revision-stage-list">{proposal.affectedStages.map((stage, index) => <li key={index}>{stage}</li>)}</ol></div>
          {!!proposal.reusedStages?.length && <div><span>沿用已有结果</span><p>{proposal.reusedStages.join('、')}；这些阶段不重新执行。</p></div>}
        </section>
        <details className="research-revision-changes"><summary><span>完整修改要求</span><small>{proposal.changes.length} 项 · 确认前请核对</small></summary><ol>{proposal.changes.map((change, index) => <li key={index}>{change}</li>)}</ol></details>
        {proposal.nodeId === 'evidence_extraction' && <p role="status">确认后先打开完整引用选择，保留原选择供你调整；不会重新导入或综合材料。确认引用后才执行后续方法、报告与独立审查。</p>}
        {!!proposal.materials?.length && <section className="research-revision-materials"><h4>确认后纳入的补充文献</h4><ul>{proposal.materials.map(material => <li key={material.attachmentId}><b>{material.name}</b><details className="research-revision-identity"><summary>文件身份</summary><p>attachmentId: {material.attachmentId}</p><code>SHA-256: {material.sha256}</code></details></li>)}</ul><p>先索引与核验证据，再更新报告；不会重新搜索候选文献。</p></section>}
        {proposal.nodeId === 'evidence_import' && !proposal.materials?.length && <p role="status"><b>此方案未绑定补充文献。</b>只会重新导入任务原有材料；文字中提到的附件不会自动纳入。如需补充全文，请先让 AI 重新生成列出文件的方案，再确认。</p>}
        {proposal.dataVersion && <section className="research-revision-materials"><h4>数据版本更新</h4>
          <details className="research-revision-identity"><summary>更新前的数据</summary><ul>{proposal.dataVersion.previousPaths.map(path => <li key={path}>{path}</li>)}</ul></details>
          <p>确认后使用的完整数据集合</p><ul>{proposal.dataVersion.files.map(file => <li key={file.relativePath}><b>{file.name}</b> · {file.attachmentId ? '对话上传' : '沿用'}<details className="research-revision-identity"><summary>文件身份</summary><p>{file.relativePath}</p>{file.attachmentId && <p>attachmentId: {file.attachmentId}</p>}<code>SHA-256: {file.sha256}</code></details></li>)}</ul>
          <p>未列出的旧数据不再参与本轮计算，但旧数据及旧成果保留。从数据预检开始重算；多表的合并规则以本方案修改要求为准，不会自动拼接。</p>
        </section>}
        {proposal.nodeId === 'data_preflight' && !proposal.dataVersion && <p role="status">此方案沿用当前已确认的数据版本，不会自动采用聊天附件。</p>}
        {proposal.sourceVersion && <section className="research-revision-materials"><h4>源码参考更新</h4>
          {proposal.sourceVersion.files.length ? <><ul>{proposal.sourceVersion.files.map(file => <li key={file.relativePath}><b>{file.name}</b><details className="research-revision-identity"><summary>文件身份</summary><p>{file.relativePath}</p><p>attachmentId: {file.attachmentId}</p><code>SHA-256: {file.sha256}</code></details></li>)}</ul><p>确认后读取此 Python 源码并适配到原计算链，不会直接运行上传文件；依赖、输入输出和方法变更仍沿原确认与审查流程处理。表格数据集合不因此改变，旧代码和旧结果保留。</p></> : <p>取消当前源码参考；不会自动撤销先前已经采用的方法，以本方案修改要求为准。</p>}
        </section>}
        <details className="research-revision-reason"><summary>起点依据</summary><p>{proposal.reason}</p></details>
      </>;
      return pending ? <ResearchCard key={proposal.id} className="research-revision-proposal pending" aria-label="返修确认方案" title="确认返修方案" eyebrow="对话协商 · 尚未执行" status="待确认" tone="pending"
          footer={<><p>{proposal.repeatsSideEffects ? '包含重新计算或执行操作。' : ''}旧版本保留，后续审查和交付检查将重新执行，可能产生模型调用费用。</p>
            <button type="button" className="primary" disabled={busy} onClick={() => confirm(proposal.id)}>{busy ? '正在处理…' : '确认方案并开始返修'}</button>
          </>}>{body}</ResearchCard> : <ResearchCardFrame key={proposal.id} className="research-revision-proposal historical" aria-label="返修确认方案">
        <details className="research-card-history"><summary><span className="research-card-badge">{superseded ? '旧方案已失效' : '已确认执行'}</span><span>{proposal.summary}</span></summary><div className="research-card-body">{body}</div></details>
      </ResearchCardFrame>;
    })}
  </div>;
}
