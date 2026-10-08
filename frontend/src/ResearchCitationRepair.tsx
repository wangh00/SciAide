import { CitationRepairModel, supplementCitationKeys } from './citationRepairPresentation.js';

export function ResearchCitationRepair({ model }: {model: CitationRepairModel}) {
  return <section className="research-citation-repair" aria-label="本轮引用补选">
    <header><span>返修检查点</span><h4>先确认引用，再更新报告</h4></header>
    {model.repeatedIssueCount > 0 && <p className="research-repair-repeat">审查记录中有 {model.repeatedIssueCount} 项问题仍未解决或重新出现。本轮沿用原选择，不会自动补入引用。</p>}
    {model.corrections.length > 0 && <details className="research-repair-requirements" open><summary>本轮修改要求 · {model.corrections.length} 项</summary><ol>{model.corrections.map((text, index) => <li key={index}>{text}</li>)}</ol></details>}
    <div className="research-repair-stats"><span>原选择 <b>{model.previousCount}</b></span><span>本轮新增 <b>{model.added}</b></span><span>本轮移除 <b>{model.removed}</b></span></div>
    <small>修改要求来自上一轮审查。下面数量旁的补入按钮仅调整勾选，保留其他选择；最后确认才会继续返修。推荐或提及不等于科学充分性，补选后仍需更新报告并重新审查。</small>
  </section>;
}

export function ResearchCitationRepairActions({ model, selectedKeys, update, busy }: {model: CitationRepairModel; selectedKeys: string[]; update: (keys: string[]) => void; busy: boolean}) {
  return <>
    {model.mentioned.size > 0 && <button type="button" className="citation-repair-supplement" disabled={busy || !model.missingMentioned.length} onClick={() => update(supplementCitationKeys(selectedKeys, model.missingMentioned))}>{model.missingMentioned.length ? `补入审查提及引用（${model.missingMentioned.length}）` : '审查提及引用已选入'}</button>}
    {model.missingRecommended.length > 0 && <button type="button" disabled={busy} onClick={() => update(supplementCitationKeys(selectedKeys, model.missingRecommended))}>补入未选推荐（{model.missingRecommended.length}）</button>}
  </>;
}

export function ResearchCitationRepairStatus({ model }: {model: CitationRepairModel}) {
  return <p className={`research-repair-selection-status ${model.unchanged || model.missingMentioned.length ? 'warning' : ''}`} role="status">{model.unchanged ? '引用选择尚未变化，直接继续可能再次遇到相同审查问题。你仍可按当前选择继续。' : `选择已变化：新增 ${model.added} 条、移除 ${model.removed} 条。`}{model.missingMentioned.length > 0 && ` 审查提及的 ${model.missingMentioned.length} 条引用仍未采用，已排在列表前面。`}</p>;
}
