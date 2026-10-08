import { reviewFeedbackModel, ReviewIssues } from './researchReviewPresentation.js';

export function ResearchReviewFeedback({ issues, errorCode, errorMessage }: { issues?: ReviewIssues | null; errorCode?: string; errorMessage?: string }) {
  const model = reviewFeedbackModel(issues);
  const findings = issues?.findings ?? [];
  const repeated = findings.filter(item => item.status === 'open' && (item.origin === 'existing' || item.origin === 'reopened')).length;
  const newIssues = findings.filter(item => item.status === 'open' && item.origin === 'new').length;
  const resolved = findings.filter(item => item.status === 'resolved').length;
  return <div className="research-review-feedback">
    <div className="research-review-summary"><span className="research-review-eyebrow">独立审查 · 待返修</span><b>{model.title}</b><p>{model.summary}</p></div>
    {findings.length > 0 && <div className="research-review-progress" aria-label="审查问题变化"><span>仍未解决或重新出现 <b>{repeated}</b></span><span>新增 <b>{newIssues}</b></span><span>已解决 <b>{resolved}</b></span></div>}
    {repeated > 0 && <p className="research-review-repeat" role="status">有 {repeated} 项既有问题尚未解决，不代表每轮都是新问题。请先核对下面的修改要求及返修起点。</p>}
    {model.items.length > 0 && <ol className="research-review-fixes">{model.items.map((item, index) => <li key={index}>{item.title && <b>{item.title}</b>}<p>{item.explanation}</p>{item.title && <div><b>审查原文</b><p>{item.raw}</p></div>}</li>)}</ol>}
    {!!issues?.findings?.length && <details className="research-review-technical"><summary>逐项复核记录 · {issues.findings.filter(item => item.status === 'open').length} 项待修正 · {issues.findings.filter(item => item.status === 'resolved').length} 项已解决 · {issues.findings.filter(item => item.status === 'withdrawn').length} 项已撤回</summary>
      <ol>{issues.findings.map(item => <li key={item.id}><b>{item.status === 'resolved' ? '已解决' : item.status === 'withdrawn' ? '已撤回' : item.origin === 'reopened' ? '重新发现' : item.origin === 'existing' ? '仍待修正' : '新增问题'}：{item.summary}</b><p>{item.location}：{item.reason}</p>{item.changeReason && <p>{item.changeReason}</p>}<small>{item.id}</small></li>)}</ol>
    </details>}
    {!!issues?.suggestions?.length && <details className="research-review-technical"><summary>可选建议 · {issues.suggestions.length}</summary><ul>{issues.suggestions.map((item, index) => <li key={index}>{item}</li>)}</ul></details>}
    <p className="research-review-next">你可以在下方确认修改范围，让程序按原始审查意见继续处理。若需要补充数据或确认研究选择，会再请你决定；重新检查通过前不会交付。</p>
    <details className="research-review-technical"><summary>查看原始审查记录与技术详情</summary>
      <p>原始记录用于追溯问题，不会被上面的通俗说明替换。分类意见与修订要求可能描述同一个问题，不应相加计数。</p>
      {errorCode && <code>{errorCode}</code>}{errorMessage && <p>{errorMessage}</p>}
      {(issues?.groups || []).map(group => <section key={group.key}><b>{group.label} · {group.items.length}</b><ol>{group.items.map((raw, index) => <li key={index}>{raw}</li>)}</ol></section>)}
    </details>
  </div>;
}
