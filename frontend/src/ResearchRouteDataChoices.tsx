import {type RouteDataCandidate, type RouteDataFile} from './routeDataDraft.js';

export function ResearchRouteDataChoices({candidates, files, portName, paths, disabled, maxItems, onToggle}: {
  candidates: RouteDataCandidate[]; files: Record<string, RouteDataFile>; portName: string; paths: string[];
  disabled: boolean; maxItems: number; onToggle: (attachmentId: string, selected: boolean) => void;
}) {
  const choices = candidates.filter(candidate => candidate.portNames.includes(portName));
  if (!choices.length) return null;
  return <section className="research-route-uploaded-data" aria-label="本任务已上传的数据">
    <p>{choices.length === 1 ? '已找到对话中的表格，无需重新上传。' : `本任务已上传 ${choices.length} 份表格，请选择本轮使用的数据，不会自动合并。`}<span>点击下方开始执行后才采用。</span></p>
    {choices.map(candidate => {
      const file = files[candidate.attachmentId];
      const checked = Boolean(file && paths.includes(file.relativePath));
      return <label key={candidate.attachmentId} className={checked ? 'selected' : ''} title={`SHA256 ${candidate.sha256}`}>
        <input type="checkbox" checked={checked} disabled={disabled || !checked && paths.length >= maxItems} onChange={event => onToggle(candidate.attachmentId, event.target.checked)}/>
        <span><b>{candidate.name}</b><small>来自本任务对话 · {Math.max(1, Math.ceil(candidate.sizeBytes / 1024))} KB · {candidate.sha256.slice(0, 12)}</small></span>
        <em>{checked ? '已选用' : '未选用'}</em>
      </label>;
    })}
  </section>;
}
