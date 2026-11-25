import { useState } from "react";
import { ResearchStatePanel } from "./ResearchStatePanel";
import { researchDiscussionEntryAvailable, type ResearchDiscussionEntry } from "./researchDiscussion";

type Service = <T>(facade: string, method: string, ...args: unknown[]) => Promise<T>;

// A direct task view has no synthetic Run. Candidate Runs use the existing
// Workflow detail view only after the host actually creates them.
export function DirectResearchTaskView({ entry, notice, refreshKey, service, onBack, onOpenDiscovery, onOpenArtifacts }: {
  entry: ResearchDiscussionEntry;
  notice?: string;
  refreshKey: string;
  service: Service;
  onBack: () => void;
  onOpenDiscovery: () => void;
  onOpenArtifacts: () => void;
}) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const available = researchDiscussionEntryAvailable(entry);
  return <>
    <button type="button" className="research-mobile-toggle" aria-expanded={mobileOpen} aria-controls="direct-research-panel" onClick={() => setMobileOpen(value => !value)}>研究状态与工具</button>
    <aside id="direct-research-panel" className={`research-route-panel direct-research-panel${mobileOpen ? " is-mobile-open" : ""}`} aria-label="直接研究任务">
      <header><div><p>RESEARCH TASK</p><h2 title={entry.researchQuestion}>{entry.title || "研究任务"}</h2><small>{available ? "研究讨论 · 按需探索" : "历史任务 · 只读"}</small></div><button type="button" aria-label="返回科研任务列表" onClick={onBack}>←</button></header>
      {notice && <p className="research-guide-feedback" role="status">{notice}</p>}
      <section className="research-task-question"><header><span>当前研究问题</span></header><p title={entry.researchQuestion}>{entry.researchQuestion}</p></section>
      {entry.referenceAttachmentIds.length > 0 && <section className="research-task-question"><header><span>已选参考文献</span></header><p>{entry.referenceAttachmentIds.length} 项参考文献将在省略新选择时交接给首个候选。</p></section>}
      <div className="research-route-status"><span className={`workflow-status ${available ? "running" : "completed"}`}>{available ? "科研探索" : "历史只读"}</span><b>任务 {entry.researchTaskId.slice(0, 8)}</b><small>{entry.permissionMode === "full_access" ? "Full Access" : "Plan"} · {new Date(entry.createdAt).toLocaleString()}</small></div>
      <ResearchStatePanel key={`${entry.projectId}:${entry.researchTaskId}`} projectId={entry.projectId} taskId={entry.researchTaskId} service={service} refreshKey={refreshKey} mode="discussion" onOpenDiscovery={onOpenDiscovery}/>
    </aside>
    <aside className="research-evidence-panel" aria-label="科研任务核验与成果">
      <header><div><p>VERIFIABLE RESULTS</p><h2>核验与成果</h2></div><button type="button" onClick={onOpenArtifacts}>查看科研产物</button></header>
      <section className="workflow-run-output starter"><header><div><h3>围绕研究问题持续探索</h3><small>可以先核读、比较和记录未知项；需要正式路线时再生成候选，采用后才执行。</small></div></header></section>
      <div className="research-task-connection"><i className={available ? "active" : "terminal"}/><span><b>{available ? "研究讨论可继续" : "当前任务为历史只读"}</b><small>{available ? "当前显示主会话；读取和工作记录仍属于这个研究任务，候选须明确采用。" : "已有材料、记录和成果可查看，不重新开放执行。"}</small></span></div>
    </aside>
  </>;
}
