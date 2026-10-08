import { useEffect, useState } from 'react';

export type RevisionTarget = { nodeId: string; label: string; repeatsSideEffects: boolean };
export type RevisionRecommendation = {
  status: 'recommended' | 'needs_input' | 'unavailable';
  nodeId?: string; label?: string; summary: string; reasons?: string[];
  affectedStages?: string[]; repeatsSideEffects: boolean; reviewOutputSha256?: string;
  requiredInputs?: string[];
};

export type EvidenceReselection = {nodeId: string; availableCount: number; recommendedCount: number; selectedRecommendedCount: number; missingRecommendedCount: number; previouslyUnofferedCount: number; reviewOutputSha256: string};

type Props = {
  resetKey: string; targets: RevisionTarget[]; recommendation?: RevisionRecommendation;
  busy: boolean; confirmed: boolean; setConfirmed: (value: boolean) => void;
  submit: (nodeId: string, useRecommendation: boolean, reviewSha: string) => void;
  reselection?: EvidenceReselection;
};

export function ResearchRevisionControls({ resetKey, targets, recommendation, busy, confirmed, setConfirmed, submit, reselection }: Props) {
  const [manual, setManual] = useState(false);
  const [selected, setSelected] = useState('');
  const needsInput = recommendation?.status === 'needs_input';
  const missingInputs = recommendation?.requiredInputs?.length ? recommendation.requiredInputs : [recommendation?.summary || '请查看原始审查记录，确认缺少的资料或研究选择。'];
  useEffect(() => { setManual(false); setSelected(''); setConfirmed(false); }, [resetKey, recommendation?.reviewOutputSha256, reselection?.reviewOutputSha256, setConfirmed]);
  const target = targets.find(value => value.nodeId === selected);
  const recommendedTarget = targets.find(value => value.nodeId === recommendation?.nodeId);
  const ready = recommendation?.status === 'recommended' && Boolean(recommendedTarget && recommendation.reviewOutputSha256);
  const reselectTarget = targets.find(value => value.nodeId === reselection?.nodeId);
  const reselectReady = Boolean(reselection?.reviewOutputSha256 && reselectTarget && (!recommendation?.reviewOutputSha256 || recommendation.reviewOutputSha256 === reselection.reviewOutputSha256));
  // One execution action. A historical input-blocked review may reopen verified
  // evidence selection, but this is not a claim that missing materials are fixed.
  const selectionRepair = needsInput && reselectReady && Boolean(reselection?.previouslyUnofferedCount);
  const activeTarget = manual ? target : ready ? recommendedTarget : selectionRepair ? reselectTarget : undefined;
  const opensCitationSelection = Boolean(activeTarget && reselectReady && activeTarget.nodeId === reselection?.nodeId);
  const repeats = Boolean(activeTarget?.repeatsSideEffects || (!manual && ready && recommendation?.repeatsSideEffects));
  const identityMismatch = activeTarget?.nodeId === reselection?.nodeId && Boolean(reselection && !reselectReady);
  const enabled = !busy && Boolean(activeTarget) && !identityMismatch && (!repeats || confirmed);
  function toggleManual() { setManual(!manual); setSelected(''); setConfirmed(false); }
  function start() {
    if (!enabled) return;
    const useRecommendation = !manual && ready;
    const reviewSha = activeTarget!.nodeId === reselection?.nodeId && reselectReady ? reselection!.reviewOutputSha256 : recommendation?.reviewOutputSha256 || '';
    submit(activeTarget!.nodeId, useRecommendation, reviewSha);
  }
  return <>
    <section className="research-revision-advice" aria-label="修改起点建议">
      <header><span>{ready ? 'AI 建议 · 已校验修改范围' : selectionRepair ? '返修 · 确认采用已有证据' : needsInput ? '需要你补充信息' : '尚无可靠的修改建议'}</span>
        <h4>{ready ? `建议从「${recommendation?.label || recommendedTarget?.label}」开始修改` : selectionRepair ? `从「${reselectTarget?.label}」开始返修` : needsInput ? '先补充缺少的资料或研究选择' : '请选择修改范围，不会默认只修改报告'}</h4>
      </header>
      {needsInput ? <div role="status"><p>{selectionRepair ? '旧列表未完整提供已接受证据，可以先直接补选，无需再次让 AI 整理方案。补选不会提供新材料或自动改变研究目标，以下其他信息缺口仍须处理。' : '以下资料或研究选择尚未确定，单纯重复执行不能补齐。可在原对话补充材料或说明选择；无需复制要求，也不必重新整理已有审查意见。'}</p><ol>{missingInputs.map((value, index) => <li key={index}>{value}</li>)}</ol></div> : <p>{recommendation?.summary || '这次审查没有提供可校验的修改位置。你可以展开下方选项手动选择；程序不会猜测，也不会自动重跑。'}</p>}
      {reselectReady && <div className="research-revision-scope"><b>已有证据可补选</b><div className="research-revision-evidence-stats"><span>候选 <strong>{reselection!.availableCount}</strong></span><span>推荐 <strong>{reselection!.recommendedCount}</strong></span><span>已采用推荐 <strong>{reselection!.selectedRecommendedCount}</strong></span><span>推荐未采用 <strong>{reselection!.missingRecommendedCount}</strong></span></div>{reselection!.previouslyUnofferedCount > 0 && <p>{reselection!.previouslyUnofferedCount} 条没有出现在旧选择列表中，这是程序遗漏，不是你主动排除。</p>}<small>打开列表只保留原选择，不会自动补选；不重跑导入、核读或综合。确认引用后才更新后续方法、报告与审查。这不保证其他科学缺口已解决。</small></div>}
      {Boolean(recommendation?.reasons?.length) && <details className="research-revision-reasons"><summary>为什么这样建议</summary><ol>{recommendation?.reasons?.map((reason, index) => <li key={index}>{reason}</li>)}</ol></details>}
      {ready && !manual && <div className="research-revision-scope"><b>返修及后续交付范围</b><ol className="research-revision-stage-list">{(recommendation?.affectedStages?.length ? recommendation.affectedStages : [recommendation?.label || recommendedTarget?.label || '']).map((stage, index) => <li key={`${stage}:${index}`}>{stage}</li>)}</ol></div>}
      {targets.length > 0 && <button type="button" className="research-revision-adjust" aria-expanded={manual} disabled={busy} onClick={toggleManual}>{manual ? ready ? '返回推荐方案' : '收起修改范围' : '调整修改范围'}</button>}
      {manual && <label className="research-revision-start"><span>从哪里开始修改</span><select aria-label="返修起点" value={selected} disabled={busy} onChange={event => { setSelected(event.target.value); setConfirmed(false); }}><option value="">请选择修改起点</option>{targets.map(value => <option key={value.nodeId} value={value.nodeId}>{value.label}{value.repeatsSideEffects ? ' · 需要重新计算或执行操作' : ''}</option>)}</select>
        <small>{recommendation?.status === 'needs_input' ? '注意：调整起点不能代替补充缺失资料或确认研究目标。请先处理上述信息缺口。' : '手动选择会覆盖推荐。若起点过晚，可能无法解决上游问题；选定步骤及后续检查会重新执行。'}</small></label>}
    </section>
    <footer className="research-retry-actions">
      <p className="research-retry-footnote">{opensCitationSelection ? '此按钮只打开引用补选；原选择保留，需要在列表中点“补入”或勾选，再确认继续。' : ''}原始资料和旧版本保留；确认后可能产生模型调用费用，重新审查通过后才会交付。</p>
      {repeats && <label><input type="checkbox" checked={confirmed} disabled={busy} onChange={event => setConfirmed(event.target.checked)}/>我确认重新计算或执行相关操作，并承担额外运行时间与可能的费用</label>}
      <button type="button" className="primary" disabled={!enabled} onClick={start}><span>{busy ? '正在准备修改…' : opensCitationSelection ? '打开引用补选' : manual ? '按所选范围修改' : needsInput ? '等待补充信息' : '按建议修改'}</span></button>
    </footer>
  </>;
}
