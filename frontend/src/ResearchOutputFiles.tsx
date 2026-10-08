import { ReactNode } from 'react';

type OutputStep = { nodeId: string; status: string; attempt: number; input?: unknown; output?: unknown };
type OutputFile = { name?: string; workspacePath: string; id?: string };
const generatedPrefix = /^(?:dynamic|preflight)-[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}-\d+(?:-|(?=\.))/i;
function filePresentation(file: OutputFile) {
  const name = typeof file.name === 'string' && file.name ? file.name : file.workspacePath.split(/[\\/]/).pop() || file.workspacePath;
  const short = name.replace(generatedPrefix, '');
  const extension = name.split('.').pop()?.toUpperCase() || 'FILE';
  return { name, label: name.startsWith('preflight-') && short === '.json' ? '数据预检.json' : short || name, extension };
}

export function ResearchOutputFiles({ steps, openArtifacts, renderCode, downloadCode, downloading = false }: { steps: OutputStep[]; openArtifacts: () => void; renderCode?: (code: string) => ReactNode; downloadCode?: (nodeId: string, attempt: number) => void; downloading?: boolean }) {
  const files = steps.flatMap(step => {
    if (step.status !== 'completed' || !step.output || typeof step.output !== 'object') return [];
    const artifacts = (step.output as { artifacts?: unknown }).artifacts;
    if (!Array.isArray(artifacts)) return [];
    return artifacts.filter((value): value is OutputFile =>
      !!value && typeof value === 'object' && typeof value.workspacePath === 'string' && !!value.workspacePath
    ).map(file => ({ ...file, nodeId: step.nodeId, attempt: step.attempt }));
  });
  // Show the submitted code actually passed to a successful Kernel, not an
  // earlier AI draft. Download resolves the same node/attempt on the host.
  const executions = steps.flatMap(step => {
    if (step.status !== 'completed' || !step.input || !step.output) return [];
    const input = step.input as { code?: unknown };
    const output = step.output as { structured?: { status?: unknown; codeSha256?: unknown } };
    const kernel = output.structured;
    return typeof input.code === 'string' && input.code && kernel?.status === 'success' && typeof kernel.codeSha256 === 'string' && /^[\da-f]{64}$/i.test(kernel.codeSha256)
      ? [{ nodeId: step.nodeId, attempt: step.attempt, code: input.code, sha256: kernel.codeSha256 }] : [];
  });
  if (!files.length && !executions.length) return null;
  return <section className="research-output-files" aria-label="本次生成的文件">
    <header><div><b>本次生成的文件 · {files.length}</b><small>分析文件与预检记录 · 原始名称和执行版本保留</small></div>{files.length > 0 && <button type="button" onClick={openArtifacts}>查看、预览或下载产物 <span aria-hidden="true">↗</span></button>}</header>
    {files.length > 0 && <ul>{files.map((file, index) => {
      const { name, label, extension } = filePresentation(file);
      return <li key={`${file.nodeId}:${file.id ?? file.workspacePath}:${index}`} title={`${name}\n${file.workspacePath}`}>
        <span className={`output-file-type type-${extension.toLowerCase()}`}>{extension}</span>
        <div><span className="output-file-name">{label}</span><small>第 {file.attempt} 次执行{file.nodeId === 'data_preflight' ? ' · 预检记录' : ''}</small></div>
      </li>;
    })}</ul>}
    {executions.map(execution => <details className="output-executed-code" key={`${execution.nodeId}:${execution.attempt}:${execution.sha256}`}>
      <summary><span>实际执行代码{execution.nodeId === 'data_preflight' ? ' · 数据预检' : execution.nodeId === 'python_analysis' ? ' · 分析计算' : ''}</span><small>第 {execution.attempt} 次执行 · {execution.sha256.slice(0, 8)}</small></summary>
      <div><p>Kernel 成功运行的原始 Python 源码；查看、复制或下载不会重新执行。复现仍需原任务的输入、参数与 Kernel 注入变量。<span title={execution.sha256}>SHA-256 · {execution.sha256}</span></p>
        {downloadCode && <button type="button" disabled={downloading} onClick={() => downloadCode(execution.nodeId, execution.attempt)}>{downloading ? '正在保存…' : '下载执行代码 .py'}</button>}
        {renderCode ? renderCode(execution.code) : <pre><code>{execution.code}</code></pre>}
      </div>
    </details>)}
  </section>;
}
