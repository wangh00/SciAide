import { ReactNode, useEffect, useRef, useState } from 'react';

// Navigation is derived from rendered headings; it never rewrites saved text
// or creates identifiers from untrusted Markdown.
export function ResearchReportReader({ children, resetKey }: {children: ReactNode; resetKey: string}) {
  const body = useRef<HTMLDivElement>(null);
  const [headings, setHeadings] = useState<string[]>([]);
  const [expanded, setExpanded] = useState(false);
  const [comfortable, setComfortable] = useState(false);
  useEffect(() => {
    setHeadings(Array.from(body.current?.querySelectorAll('h1,h2,h3,h4,h5') ?? []).map(element => element.textContent || '未命名章节'));
    setExpanded(false);
    setComfortable(false);
    if (body.current) body.current.scrollTop = 0;
  }, [resetKey]);
  function jump(index: number) {
    const container = body.current;
    const heading = container?.querySelectorAll<HTMLElement>('h1,h2,h3,h4,h5')[index];
    if (container && heading) {
      if (expanded) heading.scrollIntoView({block: 'start', behavior: 'auto'});
      else container.scrollTop += heading.getBoundingClientRect().top - container.getBoundingClientRect().top - 16;
      heading.tabIndex = -1;
      heading.focus({preventScroll: true});
    }
  }
  return <section className={`research-report-reader${expanded ? ' expanded' : ''}${comfortable ? ' comfortable' : ''}`} aria-label="报告阅读区">
    <header className="report-reader-toolbar"><span>正文阅读<small>引用可点击核对原文</small></span><nav aria-label="阅读设置"><button type="button" aria-pressed={comfortable} onClick={() => setComfortable(!comfortable)}>{comfortable ? '标准字号' : '舒适字号'}</button><button type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{expanded ? '收起阅读区' : '展开阅读区'}</button></nav></header>
    {headings.length > 1 && <details className="report-reader-contents"><summary>章节导航 · {headings.length}</summary><nav aria-label="报告章节">{headings.map((title, index) => <button type="button" key={index} onClick={() => jump(index)}>{title}</button>)}</nav></details>}
    <div className="report-reader-body" ref={body}>{children}</div>
  </section>;
}
