import { ResearchCard } from './ResearchCard';

export type ResearchInterpretation = {
  summary: string; computedFacts: string[]; inferences: string[];
  alternativeExplanations: string[]; methodChecks: string[]; limitations: string[];
  confidence: "low" | "medium" | "high";
};

export function ResearchAnalysisResult({ result }: { result: ResearchInterpretation }) {
  const sections = [
    ["计算事实", result.computedFacts], ["推断与解释", result.inferences],
    ["其他可能解释", result.alternativeExplanations], ["方法适用性检查", result.methodChecks],
    ["局限", result.limitations],
  ] as const;
  return <ResearchCard className="research-analysis-result" aria-label="本次数据分析结果" tone="success" title="本次数据分析结果" status="AI 复核已通过" description="AI 复核已通过 · 不等于科学认证" footer={<span>模型判断置信度：{({low: "低", medium: "中", high: "高"})[result.confidence]}。可在下方原聊天框追问或提出修改。</span>}>
    <p className="research-analysis-summary">{result.summary}</p>
    {sections.filter(([, items]) => items.length).map(([title, items]) => <section key={title}><h4>{title}</h4><ul>{items.map((item, index) => <li key={index}>{item}</li>)}</ul></section>)}
  </ResearchCard>;
}
