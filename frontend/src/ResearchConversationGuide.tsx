export const researchStartExamples = [
  {
    title: "文献比较",
    hint: "先添加本地文献，再描述要比较的问题",
    prompt: "请只比较我选择的文献，不联网。围绕它们共同的研究问题，比较研究对象、方法、主要发现与局限，给出带原文定位的比较表；证据不足的地方明确说明，不补造结论。交付比较稿，不需要设计新实验或分析数据。",
  },
  {
    title: "数据分析",
    hint: "先描述目标，采用计算路线后选择数据文件",
    prompt: "我有 CSV / TSV / XLSX 表格，请规划数据分析路线，采用路线后我会选择数据文件。先了解字段和数据质量，再依据数据及我的研究问题商定合适的方法，输出可复现代码、结果表、图形和局限说明。不预设显著性结论，也不需要额外检索文献。",
  },
  {
    title: "研究设计",
    hint: "还没有数据，也可以从问题与现实条件开始",
    prompt: "我还没有数据，想把研究想法变成可实施的研究设计。请先澄清研究问题、已有材料和现实条件，再讨论合适的方法、需要采集的数据与局限；不要把设计当作已经验证的结论。",
  },
] as const;

// Examples edit the existing draft; they never submit or replace typed work.
export function appendResearchExample(current: string, example: string): string {
  if (!current.trim()) return example;
  if (current.includes(example)) return current;
  return `${current}\n\n${example}`;
}

export function ResearchGettingStarted({ disabled, onDraft }: {
  disabled?: boolean; onDraft: (prompt: string) => void;
}) {
  return <section className="research-conversation-guide" aria-label="科研对话示例">
    <header><b>不知道怎么开始？试着这样描述</b><small>点击只填入问题，不会启动；可自由改写或组合，不必选择分类。</small></header>
    <div className="research-example-buttons">{researchStartExamples.map(example => <button type="button" key={example.title} disabled={disabled} onClick={() => onDraft(example.prompt)}>
      <b>{example.title}</b><small>{example.hint}</small><span>填入示例 ↗</span>
    </button>)}</div>
    <p>文献用下方「添加本地文献」或「从资料库选择」；表格数据在采用包含计算的路线后，用「添加数据文件」选择，不放入文献选择器。生成路线后，计划、确认与结果会回到原聊天流。</p>
  </section>;
}

export function ResearchFollowupGuide({ evidence, computation, disabled, onDraft }: {
  evidence: boolean; computation: boolean; disabled?: boolean; onDraft: (prompt: string) => void;
}) {
  const examples = [
    {title: "解释已有结果", prompt: "请解释当前结果的依据和局限，只讨论，不修改也不重新执行。"},
    ...(evidence ? [{title: "补充文献", prompt: "我会用聊天框的回形针添加补充文献并发送。请读取后讨论它与当前证据的异同，再提出明确列出补充文件的更新方案，等我确认后再更新，不要先改变旧结果。"}] : []),
    ...(computation ? [
      {title: "比较另一种方法", prompt: "请先读取当前实际采用的方法、数据和结果，讨论另一种适合本研究的方法及其适用条件，提出与原方法对照的返修方案，等我确认后再计算；保留旧代码和旧结果。"},
      {title: "更新数据", prompt: "我会用聊天框的回形针上传更新的数据并发送。请先讨论它与当前数据的关系，明确列出下一版使用的完整文件集合及替换、追加或关联规则，等我确认方案后再重算，保留旧数据和结果。"},
      {title: "参考已有 Python 方法", prompt: "我会用聊天框的回形针上传一份 .py 方法源码并发送。请先阅读完整源码，讨论方法、依赖和输入输出适配，提出明确列出该源码的方案，等我确认后再适配计算；不要原样执行上传脚本。"},
    ] : []),
  ];
  return <section className="research-conversation-guide followup" aria-label="在原对话继续研究">
    <header><b>在同一个聊天框继续研究</b><small>不是只有“纠错”才能续做。下面只填入草稿，不发送、不计算。</small></header>
    <div className="research-followup-buttons">{examples.map(example => <button type="button" key={example.title} disabled={disabled} onClick={() => onDraft(example.prompt)}>{example.title}</button>)}</div>
    <p>需要新材料时，在聊天框用回形针添加并发送。上传和讨论不会自动更新结果；核对确认卡中的文件、沿用阶段与重算范围后再执行。测试续做时先不要登记产物；已登记任务暂不能在原任务中返修。</p>
  </section>;
}
