import { useEffect, useRef, useState } from "react";
import {createWorkingSourceReader, validWorkingSources, workingSourceLabels, type WorkingSourceView} from "./researchWorkingSources";
import {WorkingBasisPage} from "./researchWorkingBasis";

export type ResearchWorkingState = {
  id: string;
  projectId: string;
  researchTaskId: string;
  version: number;
  currentFocus: string;
  rationale: string;
  nextStep: string;
  unknowns: string[];
  sourceChatRunId: string;
  sourceUserMessageId: string;
  sourceCallId: string;
  createdAt: string;
  status: "working_note";
};

type Service = <T>(facade: string, method: string, ...args: unknown[]) => Promise<T>;
export type ResearchStateView =
  | { phase: "loading" }
  | { phase: "empty" }
  | { phase: "error" }
  | { phase: "ready"; value: ResearchWorkingState };

export type ResearchStatePanelMode = "discussion" | "candidate";

// The existing chat snapshot is the signal; changing this key only reads the
// task state, and never starts a chat, planner or model request.
export function researchStateRefreshIdentity(conversationId: string, run: { id: string; conversationId: string; status: string } | null, calls: Array<{ id: string; toolName: string; status: string }>) {
  if (!conversationId || !run || run.conversationId !== conversationId) return "";
  const completed = calls.filter(call => call.toolName === "builtin.research.state.update" && call.status === "completed").map(call => call.id).sort();
  return JSON.stringify([run.id, run.status, ...completed]);
}

type ReaderOptions = {
  projectId: string;
  taskId: string;
  service: Service;
  onChange: (value: ResearchStateView) => void;
};

// One reader belongs to exactly one project/task. Refreshes are coalesced, and
// disposing the reader prevents late IPC responses from updating another view.
export function createResearchStateReader({ projectId, taskId, service, onChange }: ReaderOptions) {
  let disposed = false, inFlight = false, dirty = false, settled = false;
  async function refresh(): Promise<void> {
    if (disposed || !projectId || !taskId) return;
    if (inFlight) { dirty = true; return; }
    inFlight = true;
    if (!settled) onChange({ phase: "loading" });
    try {
      const value = await service<ResearchWorkingState | null>("WorkflowFacade", "GetResearchWorkingState", projectId, taskId);
      if (disposed) return;
      if (value === null) {
        onChange({ phase: "empty" });
      } else if (!value || value.projectId !== projectId || value.researchTaskId !== taskId || value.status !== "working_note" || !Number.isInteger(value.version) || value.version < 1) {
        throw new Error("Invalid research working state scope");
      } else {
        onChange({ phase: "ready", value });
      }
      settled = true;
    } catch {
      if (!disposed) { settled = false; onChange({ phase: "error" }); }
    } finally {
      inFlight = false;
      if (dirty && !disposed) { dirty = false; void refresh(); }
    }
  }
  return { refresh, dispose: () => { disposed = true; dirty = false; } };
}

function stateTime(value: string) {
  if (!value) return "时间未记录";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "时间未记录" : parsed.toLocaleString();
}

function StateField({ label, value }: { label: string; value: string | undefined }) {
  return <div className="research-working-field"><b>{label}</b><p>{typeof value === "string" && value.trim() ? value : "尚未记录"}</p></div>;
}

export function ResearchStatePanelView({ view, mode = "discussion", sourceView = {phase:"idle"}, basisService, onReadSources, onRetry, onOpenDiscovery }: {
  view: ResearchStateView;
  mode?: ResearchStatePanelMode;
  sourceView?: WorkingSourceView;
  basisService?: Service;
  onReadSources?: () => void;
  onRetry: () => void;
  onOpenDiscovery: () => void;
}) {
  const value = view.phase === "ready" ? view.value : null;
  const unknowns = Array.isArray(value?.unknowns) ? value.unknowns.filter((item): item is string => typeof item === "string" && Boolean(item.trim())) : [];
  const candidate = mode === "candidate";
  return <section className={`research-working-state ${candidate ? "is-candidate" : "is-discussion"}`} aria-label={candidate ? "候选路线协作状态" : "研究工作状态"}>
    <header><div><h3>{candidate ? "候选路线协作状态" : "研究工作状态"}</h3><small>{candidate ? "候选规划 · 尚未采用 · 工作记录未核验" : "研究讨论 · 模型工作记录 · 未核验"}</small></div></header>
    <p className="research-working-disclaimer">{candidate ? "候选路线尚未采用，不会自动执行；工作记录只用于继续讨论。" : "用于继续讨论，不是已确认目标或科学结论。"}</p>
    {view.phase === "loading" && <p role="status" className="research-working-feedback">正在读取研究记录…</p>}
    {view.phase === "empty" && <p className="research-working-feedback">尚未记录。继续讨论时，助手可按需保存工作记录，不要求每轮都记录。</p>}
    {view.phase === "error" && <div role="alert" className="research-working-feedback">无法读取研究记录。<button type="button" onClick={onRetry}>重试</button></div>}
    {value && <>
      <div className="research-working-meta">版本 {value.version} · {stateTime(value.createdAt)}</div>
      <StateField label="当前关注" value={value.currentFocus}/>
      <StateField label="工作理由" value={value.rationale}/>
      <div className="research-working-field"><b>待核实</b>{unknowns.length ? <ul>{unknowns.map((item, index) => <li key={`${index}:${item}`}>{item}</li>)}</ul> : <p>尚未记录</p>}</div>
      <StateField label="下一步建议 · 未自动执行" value={value.nextStep}/>
      <details key={`${value.id}:${value.version}`} className="research-working-source" onToggle={event => {if (event.currentTarget.open) onReadSources?.();}}>
        <summary>本版提交与字段来源</summary>
        <p>本版提交来源（继承字段不因此获得新来源）：</p>
        <p>对话运行：{value.sourceChatRunId || "未记录"}</p><p>用户消息：{value.sourceUserMessageId || "未记录"}</p><p>工具调用：{value.sourceCallId || "未记录"}</p>
        <p>字段来源不代表用户确认、证据支持或科学批准。</p>
        {sourceView.phase === "idle" && <p>展开后按需读取字段来源。</p>}
        {sourceView.phase === "loading" && <p role="status">正在读取字段来源…</p>}
        {(sourceView.phase === "error" || sourceView.phase === "ready" && !validWorkingSources(sourceView.value,{projectId:value.projectId,taskId:value.researchTaskId,stateId:value.id,version:value.version})) && <p role="alert">无法核对当前版本的字段来源，请刷新记录后重试。<button type="button" onClick={onReadSources}>重试字段来源</button></p>}
        {sourceView.phase === "ready" && validWorkingSources(sourceView.value,{projectId:value.projectId,taskId:value.researchTaskId,stateId:value.id,version:value.version}) && sourceView.value.fields.map(source => <div key={source.field}>
          <b>{workingSourceLabels[source.field]} · {source.status === "recorded" ? source.version === value.version ? "本版显式提交" : `继承自版本 ${source.version}` : source.status === "not_recorded" ? "尚未显式记录" : "来源无法核对"}</b>
          {source.status === "recorded" && <><p>对话运行：{source.sourceChatRunId}</p><p>用户消息：{source.sourceUserMessageId}</p><p>工具调用：{source.sourceCallId}</p></>}
          {source.basis?.map(basis=><WorkingBasisPage key={JSON.stringify([value.projectId,value.researchTaskId,value.id,value.version,source.field,basis])} scope={{projectId:value.projectId,taskId:value.researchTaskId,stateId:value.id,version:value.version,field:source.field,basis}} service={basisService}/>)}
        </div>)}
      </details>
    </>}
    <div className="research-working-tools"><b>{candidate ? "候选协作工具" : "科研工具"}</b><button type="button" onClick={onOpenDiscovery}>本任务文献发现</button><small>保存时的题名/摘要可回读，不联网。候选未核验，不等于论文全文，不自动导入或批准。</small></div>
  </section>;
}

export function ResearchStatePanel({ projectId, taskId, service, refreshKey, mode, onOpenDiscovery }: {
  projectId: string;
  taskId: string;
  service: Service;
  refreshKey: string;
  mode?: ResearchStatePanelMode;
  onOpenDiscovery: () => void;
}) {
  const scope = `${projectId}:${taskId}`;
  const [state, setState] = useState<{ scope: string; view: ResearchStateView }>({ scope, view: { phase: "loading" } });
  const reader = useRef<ReturnType<typeof createResearchStateReader> | null>(null);
  const previousRefreshKey = useRef(refreshKey);
  useEffect(() => {
    const current = createResearchStateReader({ projectId, taskId, service, onChange: view => setState({ scope, view }) });
    reader.current = current;
    previousRefreshKey.current = refreshKey;
    void current.refresh();
    const onSaved = (event: Event) => {
      const detail = (event as CustomEvent<{ projectId: string; taskId: string }>).detail;
      if (detail?.projectId === projectId && detail.taskId === taskId) void current.refresh();
    };
    window.addEventListener("research-message-saved", onSaved);
    return () => { current.dispose(); if (reader.current === current) reader.current = null; window.removeEventListener("research-message-saved", onSaved); };
  }, [projectId, taskId, scope, service]);
  useEffect(() => {
    if (previousRefreshKey.current === refreshKey) return;
    previousRefreshKey.current = refreshKey;
    void reader.current?.refresh();
  }, [refreshKey]);
  const view: ResearchStateView = state.scope === scope ? state.view : { phase: "loading" };
  const note = view.phase === "ready" ? view.value : null;
  const sourceScope = JSON.stringify([projectId,taskId,note?.id,note?.version]);
  const [sources,setSources] = useState<{scope:string;view:WorkingSourceView}>({scope:"",view:{phase:"idle"}});
  const sourceReader = useRef<ReturnType<typeof createWorkingSourceReader> | null>(null);
  useEffect(() => {
    if (!note) {sourceReader.current=null;return;}
    const current=createWorkingSourceReader({projectId,taskId,stateId:note.id,version:note.version,service,onChange:view=>setSources({scope:sourceScope,view})});
    sourceReader.current=current;
    return () => {current.dispose();if(sourceReader.current===current)sourceReader.current=null;};
  },[sourceScope,service]);
  return <ResearchStatePanelView view={view} mode={mode} sourceView={sources.scope===sourceScope ? sources.view : {phase:"idle"}} basisService={service} onReadSources={()=>void sourceReader.current?.load()} onRetry={() => void reader.current?.refresh()} onOpenDiscovery={onOpenDiscovery}/>;
}
