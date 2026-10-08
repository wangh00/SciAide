import { ReactNode } from 'react';
import { ResearchCard } from './ResearchCard';

export type ReportAssessment = {coverage: string; limitedDraft: boolean; selectedExcerpts: number; unselectedRecommended: number; confidencePolicy: string};
export type ResearchReport = {markdown: string; claimSummary: string[]; methodSummary: string; limitations: string[]; confidence: 'low' | 'medium' | 'high'};

export function ResearchReportCard({ report, quality, label, acceptance, renderMarkdown, document, actions, files, computationOnly = false }: {report: ResearchReport; quality?: ReportAssessment; label: string; acceptance: ReactNode; renderMarkdown: (text: string) => ReactNode; document: ReactNode; actions: ReactNode; files?: ReactNode; computationOnly?: boolean}) {
  const confidence = report.confidence === 'high' ? '高' : report.confidence === 'medium' ? '中' : '低';
  const coverageLabels: Record<string, string> = {adequate: '筛查覆盖充足', limited: '证据覆盖有限', insufficient: '证据覆盖不足', selection_limited: '引用采用范围有限'};
  const coverage = coverageLabels[quality?.coverage ?? ''] ?? '覆盖未单独标注';
  const recordedCounts = quality && (quality.coverage !== 'unknown' || quality.selectedExcerpts > 0 || quality.unselectedRecommended > 0);
  return <ResearchCard className="research-deliverable-card research-report-card" aria-label="本轮研究报告" tone="success"
    title={quality?.limitedDraft ? '范围受限的研究初稿' : label} titleLevel={2} eyebrow="本轮报告 · 已保存" status="AI 审查通过"
    description="AI 审查已通过；这是本轮记录，不代表科学结论被独立验证或已达到发表要求。"
    headingClassName="report-card-heading" badgeClassName="report-reviewed-badge" bodyClassName="research-deliverable-body"
    footer={<><span>报告与引用快照保留 · 登记不会提升置信程度</span><div>{actions}</div></>}>
      <div className="report-assessment-grid" aria-label="报告判断与证据范围"><section><span>模型置信判断</span><b className={`confidence-${report.confidence}`}>{confidence}</b><small>{quality?.confidencePolicy === 'legacy_limited' ? '旧版有限证据提示要求低置信，原评分保留，未重新评估。' : '模型的定性判断，不是正确率或统计置信区间。'}</small></section><section><span>{computationOnly ? '结果依据' : '证据覆盖'}</span><b>{computationOnly ? '绑定数据与计算产物' : coverage}</b><small>{computationOnly ? '本轮为数据分析，不用文献摘录数量衡量结果；请结合输入、执行代码、指标和局限判断。' : recordedCounts ? `本轮采用 ${quality.selectedExcerpts} 条摘录${quality.unselectedRecommended ? `，另有 ${quality.unselectedRecommended} 条推荐未采用` : ''}；片段数量不等于可信度。` : '未提供单独的覆盖记录，请结合正文的来源、核读范围与局限判断。'}</small></section><section><span>应用与外推</span><b>以正文边界为准</b><small>{computationOnly ? '本次分割上的计算与预测不等于因果或外部验证，不能直接外推到其他数据。' : '原文陈述、研究者推断与未核验内容须区分，不能直接外推为实际效果。'}</small></section></div>
      {quality?.limitedDraft && <p className="report-scope-notice">本轮以有限证据继续。审查通过不自动说明原始目标的每个子项已充分完成；已知未完成内容仍应补齐，旧正文与审查记录不会被此界面改写。</p>}
      {!!report.claimSummary.length && <details className="report-summary-section"><summary><span>主要结论</span><small>{report.claimSummary.length} 项 · 按需展开</small></summary><ol>{report.claimSummary.map((claim, index) => <li key={index}>{renderMarkdown(claim)}</li>)}</ol></details>}
      <details className="report-summary-section"><summary><span>方法与研究范围</span><small>查看方法摘要</small></summary><div>{renderMarkdown(report.methodSummary)}</div></details>
      {!!report.limitations.length && <details className="report-summary-section report-limitations"><summary><span>局限与未核验内容</span><small>{report.limitations.length} 项</small></summary><ul>{report.limitations.map((text, index) => <li key={index}>{renderMarkdown(text)}</li>)}</ul></details>}
      {files}
      {document}
      <details className="report-review-record"><summary>查看本轮审查依据与原始局限</summary>{acceptance}</details>
  </ResearchCard>;
}
