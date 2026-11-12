import { useEffect, useState } from 'react';

export type RevisionTarget = { nodeId: string; label: string; repeatsSideEffects: boolean };
export type RevisionRecommendation = {
  status: 'recommended' | 'needs_input' | 'unavailable';
  nodeId?: string; label?: string; summary: string; reasons?: string[];
  affectedStages?: string[]; repeatsSideEffects: boolean; reviewOutputSha256?: string;
  requiredInputs?: string[];
  repairableCorrections?: string[]; pendingStartLabel?: string; pendingAffectedStages?: string[];
};

export type EvidenceReselection = {nodeId: string; availableCount: number; recommendedCount: number; selectedRecommendedCount: number; missingRecommendedCount: number; previouslyUnofferedCount: number; reviewOutputSha256: string};

type Props = {
  resetKey: string; targets: RevisionTarget[]; recommendation?: RevisionRecommendation;
  stages?: Array<{nodeId: string; label: string}>;
  busy: boolean;
  submit: (nodeId: string, useRecommendation: boolean, reviewSha: string, confirmSideEffect: boolean) => void;
  reselection?: EvidenceReselection;
};

export function ResearchRevisionControls({ resetKey, targets, recommendation, stages = [], busy, submit, reselection }: Props) {
  const [manual, setManual] = useState(false);
  const [selected, setSelected] = useState('');
  const needsInput = recommendation?.status === 'needs_input';
  const missingInputs = recommendation?.requiredInputs?.length ? recommendation.requiredInputs : [recommendation?.summary || '请查看原始审查记录，确认缺少的资料或研究选择。'];
  useEffect(() => { setManual(false); setSelected(''); }, [resetKey, recommendation?.reviewOutputSha256, reselection?.reviewOutputSha256]);
  const target = targets.find(value => value.nodeId === selected);
  const recommendedTarget = targets.find(value => value.nodeId === recommendation?.nodeId);
  const ready = recommendation?.status === 'recommended' && Boolean(recommendedTarget && recommendation.reviewOutputSha256);
  const reselectTarget = targets.find(value => value.nodeId === reselection?.nodeId);
  const reselectReady = Boolean(reselection?.reviewOutputSha256 && reselectTarget && (!recommendation?.reviewOutputSha256 || recommendation.reviewOutputSha256 === reselection.reviewOutputSha256));
  // One execution action. A historical input-blocked review may reopen verified
  // evidence selection, but this is not a claim that missing materials are fixed.
  const selectionRepair = needsInput && reselectReady && Boolean(reselection?.previouslyUnofferedCount);
  const activeTarget = manual && !needsInput ? target : ready ? recommendedTarget : selectionRepair ? reselectTarget : undefined;
  const opensCitationSelection = Boolean(activeTarget && reselectReady && activeTarget.nodeId === reselection?.nodeId);
  const repeats = Boolean(activeTarget?.repeatsSideEffects || (!manual && ready && recommendation?.repeatsSideEffects));
  const identityMismatch = activeTarget?.nodeId === reselection?.nodeId && Boolean(reselection && !reselectReady);
  const enabled = !busy && Boolean(activeTarget) && !identityMismatch;
  const startIndex = stages.findIndex(stage => stage.nodeId === activeTarget?.nodeId);
  const affectedStages = !activeTarget ? [] : startIndex >= 0 ? stages.slice(startIndex).map(stage => stage.label)
    : !manual && ready && recommendation?.affectedStages?.length ? recommendation.affectedStages : [activeTarget.label];
  const actionLabel = opensCitationSelection ? '打开引用补选' : manual ? '按所选范围修改' : '按建议修改';
  function toggleManual() { setManual(!manual); setSelected(''); }
  function start() {
    if (!enabled) return;
    const useRecommendation = !manual && ready;
    const reviewSha = activeTarget!.nodeId === reselection?.nodeId && reselectReady ? reselection!.reviewOutputSha256 : recommendation?.reviewOutputSha256 || '';
    // Consent belongs to this explicit click and scope, not a persistent checkbox.
    submit(activeTarget!.nodeId, useRecommendation, reviewSha, repeats);
  }
  return <>
    <section className="research-revision-advice" aria-label="修改起点建议">
      <header><span>{manual && !needsInput ? '手动调整 · 修改范围' : ready ? 'AI 建议 · 已校验修改范围' : selectionRepair ? '返修 · 确认采用已有证据' : needsInput ? '尚缺必要信息' : '尚无可靠的修改建议'}</span>
        <h4>{manual && !needsInput ? target ? `从「${target.label}」开始修改` : '请选择修改起点' : ready ? `建议从「${recommendation?.label || recommendedTarget?.label}」开始修改` : selectionRepair ? `从「${reselectTarget?.label}」开始返修` : needsInput ? '本次审查仍有未解决的输入' : '请选择修改范围，不会默认只修改报告'}</h4>
      </header>
      {needsInput ? <div role="status"><p>{selectionRepair ? '旧列表未完整提供已接受证据，可以先直接补选。补选不会提供新材料或自动改变研究目标，以下其他信息缺口仍须处理。' : '必要的研究选择会直接出现在审查中的问题卡里，回答后接着同一次审查。此处仅记录仍未提供或暂缓处理的信息，不再新增核查或整理方案步骤。'}</p>{Boolean(recommendation?.repairableCorrections?.length) && <div className="research-revision-scope"><b>已有修正建议</b><ul>{recommendation!.repairableCorrections!.map((value, index) => <li key={index}>{value}</li>)}</ul>{recommendation?.pendingStartLabel && <small>已定位的候选起点：{recommendation.pendingStartLabel}。仍有必要输入未解决，当前不会执行。</small>}</div>}<div className="research-revision-scope"><b>仍需补充</b><ol>{missingInputs.map((value, index) => <li key={index}>{value}</li>)}</ol></div><small>有补充文件时，请在下方对话框上传并发送。仅放入科研资料库或尚未发送的附件，不会自动纳入本任务；采用文件及返修范围会在确认卡中列明。原审查记录不会因上传自动变为通过。</small></div> : <p>{manual ? '手动选择会覆盖 AI 建议；下方显示选定起点及后续执行范围。' : recommendation?.summary || '这次审查没有提供可校验的修改位置。你可以展开下方选项手动选择；程序不会猜测，也不会自动重跑。'}</p>}
      {reselectReady && <div className="research-revision-scope"><b>已有证据可补选</b><div className="research-revision-evidence-stats"><span>候选 <strong>{reselection!.availableCount}</strong></span><span>推荐 <strong>{reselection!.recommendedCount}</strong></span><span>已采用推荐 <strong>{reselection!.selectedRecommendedCount}</strong></span><span>推荐未采用 <strong>{reselection!.missingRecommendedCount}</strong></span></div>{reselection!.previouslyUnofferedCount > 0 && <p>{reselection!.previouslyUnofferedCount} 条没有出现在旧选择列表中，这是程序遗漏，不是你主动排除。</p>}<small>打开列表只保留原选择，不会自动补选；不重跑导入、核读或综合。确认引用后才更新后续方法、报告与审查。这不保证其他科学缺口已解决。</small></div>}
      {Boolean(recommendation?.reasons?.length) && <details className="research-revision-reasons"><summary>{manual ? 'AI 原建议依据' : '为什么这样建议'}</summary><ol>{recommendation?.reasons?.map((reason, index) => <li key={index}>{reason}</li>)}</ol></details>}
      {!needsInput && targets.length > 0 && <button type="button" className="research-revision-adjust" aria-expanded={manual} disabled={busy} onClick={toggleManual}>{manual ? ready ? '返回推荐方案' : '收起修改范围' : '调整修改范围'}</button>}
      {manual && !needsInput && <label className="research-revision-start"><span>从哪里开始修改</span><select aria-label="返修起点" value={selected} disabled={busy} onChange={event => setSelected(event.target.value)}><option value="">请选择修改起点</option>{targets.map(value => <option key={value.nodeId} value={value.nodeId}>{value.label}{value.repeatsSideEffects ? ' · 需要重新计算或执行操作' : ''}</option>)}</select>
        <small>手动选择会覆盖推荐。若起点过晚，可能无法解决上游问题；选定步骤及后续检查会重新执行。</small></label>}
      {activeTarget && <div className="research-revision-scope"><b>返修及后续交付范围</b><ol className="research-revision-stage-list">{affectedStages.map((stage, index) => <li key={`${stage}:${index}`}>{stage}</li>)}</ol><small>起点之前的结果沿用；范围内已完成的阶段重新执行，尚未开始的后续阶段继续执行。</small></div>}
    </section>
    {(!needsInput || selectionRepair) && <footer className="research-retry-actions">
      <p className="research-retry-footnote">{opensCitationSelection ? `此按钮只打开引用补选；原选择保留，需要在列表中点“补入”或勾选，再确认继续。${repeats ? '后续范围含重新计算或执行操作；确认引用后才继续。' : '确认引用后才执行后续范围。'}` : activeTarget ? `${repeats ? '本次会重新计算或执行相关操作。' : ''}点击“${actionLabel}”即确认执行上述范围。` : '请选择修改起点后再开始。'}原始资料和旧版本保留；模型调用可能消耗额度，重新审查通过后才会交付。</p>
      <button type="button" className="primary" disabled={!enabled} onClick={start}><span>{busy ? '正在准备修改…' : actionLabel}</span></button>
    </footer>}
  </>;
}
