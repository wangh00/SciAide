import { useEffect, useState } from 'react';

export type RevisionTarget = { nodeId: string; label: string; repeatsSideEffects: boolean };
export type RevisionRecommendation = {
  status: 'recommended' | 'needs_input' | 'unavailable';
  nodeId?: string; label?: string; summary: string; reasons?: string[];
  affectedStages?: string[]; repeatsSideEffects: boolean; reviewOutputSha256?: string;
  requiredInputs?: string[];
};

type Props = {
  resetKey: string; targets: RevisionTarget[]; recommendation?: RevisionRecommendation;
  busy: boolean; confirmed: boolean; setConfirmed: (value: boolean) => void;
  submit: (nodeId: string, useRecommendation: boolean, reviewSha: string) => void;
};

export function ResearchRevisionControls({ resetKey, targets, recommendation, busy, confirmed, setConfirmed, submit }: Props) {
  const [manual, setManual] = useState(false);
  const [selected, setSelected] = useState('');
  const [copyStatus, setCopyStatus] = useState('');
  const needsInput = recommendation?.status === 'needs_input';
  const missingInputs = recommendation?.requiredInputs?.length ? recommendation.requiredInputs : [recommendation?.summary || '请查看原始审查记录，确认缺少的资料或研究选择。'];
  useEffect(() => { setManual(false); setSelected(''); setConfirmed(false); setCopyStatus(''); }, [resetKey, recommendation?.reviewOutputSha256, setConfirmed]);
  const target = targets.find(value => value.nodeId === selected);
  const recommendedTarget = targets.find(value => value.nodeId === recommendation?.nodeId);
  const ready = recommendation?.status === 'recommended' && Boolean(recommendedTarget && recommendation.reviewOutputSha256);
  const repeats = manual ? Boolean(target?.repeatsSideEffects) : Boolean(recommendation?.repeatsSideEffects || recommendedTarget?.repeatsSideEffects);
  const enabled = !busy && (manual ? Boolean(target) : ready) && (!repeats || confirmed);
  function toggleManual() { setManual(!manual); setSelected(''); setConfirmed(false); }
  function start() {
    if (!enabled) return;
    submit(manual ? selected : recommendation!.nodeId!, !manual, manual ? '' : recommendation!.reviewOutputSha256!);
  }
  async function copyRequirements() {
    try {
      await navigator.clipboard.writeText(['请先核查并补齐以下审查缺口，再生成有明确返修起点的方案：', ...missingInputs.map((value, index) => `${index + 1}. ${value}`), '已有文件请先核查是否真正纳入任务，不要只在文字中承诺导入；没有材料时不要虚构证据。'].join('\n'));
      setCopyStatus('已复制。复制不会执行返修或自动纳入附件。');
    } catch { setCopyStatus('复制失败，请手动选中上方补充要求复制。'); }
  }
  return <>
    <section className="research-revision-advice" aria-label="修改起点建议">
      <header><span>{ready ? 'AI 建议 · 已校验修改范围' : recommendation?.status === 'needs_input' ? '需要你补充信息' : '尚无可靠的修改建议'}</span>
        <h4>{ready ? `建议从「${recommendation?.label || recommendedTarget?.label}」开始修改` : recommendation?.status === 'needs_input' ? '先确认缺少的信息，再决定如何修改' : '请选择修改范围，不会默认只修改报告'}</h4>
      </header>
      {needsInput ? <div role="status"><p>暂不能按建议修改：审查认为还缺资料或需要你确认，尚无可执行的完整方案。</p><ol>{missingInputs.map((value, index) => <li key={index}>{value}</li>)}</ol><p>若文件已经上传，先核查是否列在确认方案的“补充文献”中；上传成功不等于已纳入任务。</p><p>当前审查失败任务不支持直接追加材料。需要补充材料或改变冻结输入时，请新建任务并明确选择参考材料；旧任务和报告保留。不要只重跑报告来替代补证。</p></div> : <p>{recommendation?.summary || '这次审查没有提供可校验的修改位置。你可以展开下方选项手动选择；程序不会猜测，也不会自动重跑。'}</p>}
      {Boolean(recommendation?.reasons?.length) && <details className="research-revision-reasons"><summary>为什么这样建议</summary><ol>{recommendation?.reasons?.map((reason, index) => <li key={index}>{reason}</li>)}</ol></details>}
      {ready && <div className="research-revision-scope"><b>将重新执行</b><p>{recommendation?.affectedStages?.join(' → ') || recommendation?.label}</p><small>原始资料和旧版本保留；重新执行会增加运行时间，并可能产生模型调用费用。只有重新检查通过后才会交付。</small></div>}
      {targets.length > 0 && <button type="button" className="research-revision-adjust" aria-expanded={manual} disabled={busy} onClick={toggleManual}>{manual ? ready ? '返回推荐方案' : '收起修改范围' : '调整修改范围'}</button>}
      {manual && <label className="research-revision-start"><span>从哪里开始修改</span><select aria-label="返修起点" value={selected} disabled={busy} onChange={event => { setSelected(event.target.value); setConfirmed(false); }}><option value="">请选择修改起点</option>{targets.map(value => <option key={value.nodeId} value={value.nodeId}>{value.label}{value.repeatsSideEffects ? ' · 需要重新计算或执行操作' : ''}</option>)}</select>
        <small>{recommendation?.status === 'needs_input' ? '注意：调整起点不能代替补充缺失资料或确认研究目标。请先处理上述信息缺口。' : '手动选择会覆盖推荐。若起点过晚，可能无法解决上游问题；选定步骤及后续检查会重新执行。'}</small></label>}
    </section>
    <footer className="research-retry-actions">
      {repeats && <label><input type="checkbox" checked={confirmed} disabled={busy} onChange={event => setConfirmed(event.target.checked)}/>我确认重新计算或执行相关操作，并承担额外运行时间与可能的费用</label>}
      {needsInput && !manual ? <button type="button" className="primary" disabled={busy} onClick={() => void copyRequirements()}>复制补充要求</button> : <button type="button" className="primary" disabled={!enabled} onClick={start}><span>{busy ? '正在准备修改…' : manual ? '按所选范围修改' : '按建议修改'}</span></button>}
      {copyStatus && <p role="status">{copyStatus}</p>}
    </footer>
  </>;
}
