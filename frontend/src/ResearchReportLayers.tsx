import { ReactNode, useMemo, useState } from 'react';
import { ResearchReportReader } from './ResearchReportReader';

// Display projection only: never use these slices for saving or exporting.
export function reportDisplayLayers(markdown: string) {
  const headings: {start: number; level: number; title: string}[] = [];
  let offset = 0, fence = '', fenceLength = 0;
  for (const line of markdown.split(/(?<=\n)/)) {
    const marker = /^ {0,3}(`{3,}|~{3,})(.*)/.exec(line);
    if (fence) {
      if (marker && marker[1]![0] === fence && marker[1]!.length >= fenceLength && !marker[2]!.trim()) fence = '';
    } else if (marker) {
      fence = marker[1]![0]!; fenceLength = marker[1]!.length;
    } else {
      const heading = /^ {0,3}(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*(?:\r?\n)?$/.exec(line);
      if (heading) headings.push({start: offset, level: heading[1]!.length, title: heading[2]!.trim()});
    }
    offset += line.length;
  }
  const programTitles = new Set(['附录：候选筛选记录', '附录：引用来源书目', '附录：检索方法记录']);
  // A later scientific section stays in the body, not in a program appendix.
  const body: string[] = [], provenance: string[] = [];
  let start = 0, inProvenance = false;
  for (const heading of headings.filter(value => value.level <= 2)) {
    (inProvenance ? provenance : body).push(markdown.slice(start, heading.start));
    inProvenance = heading.level === 2 && programTitles.has(heading.title);
    start = heading.start;
  }
  (inProvenance ? provenance : body).push(markdown.slice(start));
  const section = (index: number) => {
    const current = headings[index]!;
    const end = headings.slice(index + 1).find(value => value.level <= current.level)?.start ?? markdown.length;
    return markdown.slice(current.start, end);
  };
  const brief = headings.findIndex(value => /^(?:[一二三四五六七八九十\d]+[、.．]\s*)?(?:普通人说明|通俗说明|简版摘要)$/.test(value.title));
  const disclaimer = headings.findIndex(value => value.title === '非诊疗声明');
  return {
    body: body.join(''), provenance: provenance.join(''),
    brief: brief < 0 ? '' : section(brief) + (disclaimer >= 0 && disclaimer !== brief ? '\n\n' + section(disclaimer) : ''),
  };
}

type Report = {markdown: string; claimSummary: string[]; limitations: string[]};
export function ResearchReportLayers({report, renderMarkdown}: {report: Partial<Report>; renderMarkdown: (text: string) => ReactNode}) {
  return <ReportLayers key={report.markdown} report={{markdown: report.markdown ?? '', claimSummary: report.claimSummary ?? [], limitations: report.limitations ?? []}} renderMarkdown={renderMarkdown}/>;
}

function ReportLayers({report, renderMarkdown}: {report: Report; renderMarkdown: (text: string) => ReactNode}) {
  const layers = useMemo(() => reportDisplayLayers(report.markdown), [report.markdown]);
  const [view, setView] = useState<'brief' | 'body' | 'provenance'>('brief');
  const brief = layers.brief || (layers.body.length <= 5000 || !report.claimSummary.length ? layers.body : '');
  return <section className="report-layers" aria-label="报告分层阅读">
    <nav className="report-layer-switch" aria-label="报告内容层级">
      <button type="button" aria-pressed={view === 'brief'} onClick={() => setView('brief')}>先看简版</button>
      <button type="button" aria-pressed={view === 'body'} onClick={() => setView('body')}>详细正文与证据</button>
      {layers.provenance && <button type="button" aria-pressed={view === 'provenance'} onClick={() => setView('provenance')}>检索与溯源记录</button>}
    </nav>
    <p className="report-layer-note">{view === 'brief' ? '简版仅摘取已保存内容；完整依据与局限请看详细正文。' : view === 'body' ? '完整科学正文；程序生成的检索与来源记录单独展示。' : '已保存的检索、筛选和来源记录，不是新增的科学结论。'} 登记与导出保留完整原稿。</p>
    {view === 'brief' && (brief ? <ResearchReportReader resetKey="brief">{renderMarkdown(brief)}</ResearchReportReader> : <div className="report-brief-summary"><h3>主要结论摘要</h3><ol>{report.claimSummary.map((text, index) => <li key={index}>{renderMarkdown(text)}</li>)}</ol>{!!report.limitations.length && <><h4>局限与未核验内容</h4><ul>{report.limitations.map((text, index) => <li key={index}>{renderMarkdown(text)}</li>)}</ul></>}</div>)}
    {view === 'body' && <ResearchReportReader resetKey="body">{renderMarkdown(layers.body)}</ResearchReportReader>}
    {view === 'provenance' && <ResearchReportReader resetKey="provenance">{renderMarkdown(layers.provenance)}</ResearchReportReader>}
  </section>;
}
