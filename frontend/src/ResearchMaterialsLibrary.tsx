import {ModalBackdrop, ModalDialog} from "./Modal";
import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import "./researchMaterialsLibrary.css";

export type ResearchMaterial = {
  id: string;
  projectId: string;
  scopeKind?: string;
  researchTaskId?: string;
  sourceKind?: string;
  originalName: string;
  format: string;
  sizeBytes: number;
  sha256: string;
  status: string;
  createdAt: string;
  title: string;
  notes: string;
  archived: boolean;
  reusable: boolean;
  collected?: boolean;
  originTaskTitle?: string;
  contentKind: "user_file" | "abstract" | "metadata" | "metadata_abstract" | "full_text" | "research_material";
  indexStatus?: string;
  indexChunks?: number;
  parseSummary?: string;
  extractedRunes?: number;
  unitCount?: number;
};

type ResearchTask = { id: string; title: string; createdAt?: string };
type MaterialText = { text: string; nextOffset?: number; totalRunes: number; truncated?: boolean };
type ImportBatch = { attachments?: unknown[]; errors?: { message?: string }[] };
type EmbeddingConfig = { enabled: boolean; baseUrl: string; modelId: string; dimensions: number; secretConfigured: boolean; secretMasked?: string; timeoutSeconds: number };
type Service = <T>(facade: string, method: string, ...args: unknown[]) => Promise<T>;

const formatDate = (value: string) => new Date(value).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", year: "numeric" });
const fileSize = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
const safeText = (value: unknown) => value instanceof Error ? value.message : typeof value === "string" ? value : "操作失败，请稍后重试。";

const sourceLabel = (value: ResearchMaterial) => ({
  user_import: "用户上传",
  research_import: "文献导入",
  conversation_upload: "会话资料",
}[value.sourceKind ?? ""] ?? "项目资料");

const contentLabel: Record<ResearchMaterial["contentKind"], string> = {
  user_file: "原始文件",
  abstract: "摘要材料",
  metadata: "题录信息",
  metadata_abstract: "题录 / 摘要，非全文",
  full_text: "来源提供全文材料",
  research_material: "研究材料",
};

const indexLabel = (value?: string) => {
  if (!value) return "未记录";
  return ({ ready: "已准备", pending: "等待处理", indexing: "处理中", failed: "处理失败", cancelled: "已取消" }[value] ?? value);
};

export function ResearchMaterialsLibrary({ project, taskId = "", close, service, taskMaterials = false, initialAttachmentId = "" }: {
  project: { id: string; name: string };
  taskId?: string;
  close: () => void;
  service: Service;
  taskMaterials?: boolean;
  initialAttachmentId?: string;
}) {
  const [materials, setMaterials] = useState<ResearchMaterial[]>([]);
  const [tasks, setTasks] = useState<ResearchTask[]>([]);
  const [activeScope, setActiveScope] = useState(taskMaterials ? taskId ? `task:${taskId}` : "legacy" : "shared");
  const [selectedId, setSelectedId] = useState("");
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("all");
  const [size, setSize] = useState("all");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const [confirmArchive, setConfirmArchive] = useState(false);
  const [checked, setChecked] = useState<string[]>([]);
  const [confirmBatch, setConfirmBatch] = useState(false);
  const [clearRequest,setClearRequest] = useState<{taskID:string;title:string;ids:string[]}|null>(null);
  const [previewTruncated,setPreviewTruncated] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [text, setText] = useState("");
  const [nextOffset, setNextOffset] = useState<number | undefined>();
  const [totalRunes, setTotalRunes] = useState(0);
  const [reading, setReading] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");
  const [draftNotes, setDraftNotes] = useState("");
  const [embeddingOpen, setEmbeddingOpen] = useState(false);
  const [embeddingLoading, setEmbeddingLoading] = useState(false);
  const [embeddingError, setEmbeddingError] = useState("");
  const embeddingDialog = useRef<HTMLDialogElement>(null);
  const [embedding, setEmbedding] = useState<EmbeddingConfig | null>(null);
  const [embeddingEnabled, setEmbeddingEnabled] = useState(false);
  const [embeddingURL, setEmbeddingURL] = useState("");
  const [embeddingModel, setEmbeddingModel] = useState("");
  const [embeddingKey, setEmbeddingKey] = useState("");
  const epoch = useRef(0);
  const readEpoch = useRef(0);

  const scopedTask = taskMaterials ? taskId.trim() : "";
  const activeTaskID = activeScope.startsWith("task:") ? activeScope.slice(5) : "";
  // A task can inspect shared material but only mutate its own material scope.
  const readOnly = Boolean(scopedTask && activeScope === "shared");
  const callArgs = (id?: string, offset?: number) => scopedTask
    ? [project.id, scopedTask, ...(id ? [id] : []), ...(offset === undefined ? [] : [offset])]
    : [project.id, "", ...(id ? [id] : []), ...(offset === undefined ? [] : [offset])];

  const reload = useCallback(async (quiet = false) => {
    const generation = ++epoch.current;
    if (!quiet) setLoading(true);
    try {
      const values = await service<ResearchMaterial[]>("KnowledgeFacade", taskMaterials ? "ListMaterials" : "ListLibraryMaterials", ...taskMaterials ? [project.id,scopedTask] : [project.id]);
      if (generation !== epoch.current) return;
      setMaterials((values ?? []).filter((item) => !item.archived && (taskMaterials || item.reusable)));
    } catch (error) {
      if (generation === epoch.current) setMessage(safeText(error));
    } finally {
      if (generation === epoch.current && !quiet) setLoading(false);
    }
  }, [project.id, scopedTask, service, taskMaterials]);

  useEffect(() => { void reload(); }, [reload]);
  useEffect(() => {
    if (!embeddingOpen) return;

    let active = true;
    setEmbeddingLoading(true); setEmbeddingError(""); setEmbedding(null); setEmbeddingKey("");
    void service<EmbeddingConfig>("KnowledgeFacade", "GetEmbeddingConfig").then((value) => {
      if (!active) return;
      setEmbedding(value); setEmbeddingEnabled(value.enabled); setEmbeddingURL(value.baseUrl); setEmbeddingModel(value.modelId); setEmbeddingKey("");
    }).catch(error => { if (active) setEmbeddingError(safeText(error)); })
      .finally(() => { if (active) setEmbeddingLoading(false); });
    return () => { active = false; };
  }, [service, embeddingOpen]);
  useEffect(() => {
    let active = true;
    void service<ResearchTask[]>("WorkflowFacade", "ListResearchTasks", project.id, 500)
      .then((values) => { if (active) setTasks(values ?? []); })
      .catch(() => { /* IDs remain usable when task labels cannot be loaded. */ });
    return () => { active = false; };
  }, [project.id, service]);
  useEffect(() => {
    readEpoch.current += 1;
    setActiveScope(taskMaterials ? scopedTask ? `task:${scopedTask}` : "legacy" : "shared"); setSelectedId(""); setDetailOpen(false); setText(""); setNextOffset(undefined); setMessage("");
  }, [scopedTask, taskMaterials]);

  const previewOpened = useRef("");
  useEffect(()=>{
    if (!loading && initialAttachmentId && previewOpened.current !== initialAttachmentId) {
      const item=materials.find(item=>item.id===initialAttachmentId);
      if(item){previewOpened.current=initialAttachmentId;setActiveScope(item.scopeKind==="task"?`task:${item.researchTaskId}`:item.reusable?"shared":"legacy");void openDetail(item);}
    }
  },[loading,materials,initialAttachmentId]);

  const taskNames = useMemo(() => new Map(tasks.map((task) => [task.id, `${task.createdAt ? new Date(task.createdAt).toLocaleString("zh-CN",{year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"}) : "时间未知"} · ${task.title || "未命名任务"} · ${task.id.slice(0,6)}`])), [tasks]);
  const grouped = useMemo(() => {
    const shared = materials.filter((item) => item.reusable);
    const legacy = materials.filter(item => !item.reusable && item.scopeKind !== "task");
    const byTask = new Map<string, ResearchMaterial[]>();
    materials.filter((item) => item.scopeKind === "task" && item.researchTaskId).forEach((item) => {
      const id = item.researchTaskId!;
      byTask.set(id, [...(byTask.get(id) ?? []), item]);
    });
    return { shared, byTask, legacy };
  }, [materials]);
  const activeMaterials = useMemo(() => {
    const values = activeScope === "shared" ? grouped.shared : activeScope === "legacy" ? grouped.legacy : grouped.byTask.get(activeScope.replace(/^task:/, "")) ?? [];
    const lower = query.trim().toLocaleLowerCase();
    return values.filter((item) => {
      const searchMatch = !lower || `${item.title} ${item.originalName} ${item.notes}`.toLocaleLowerCase().includes(lower);
      const kindMatch = kind === "all" || item.contentKind === kind;
      const sizeMatch = size === "all" || size === "small" && item.sizeBytes < 1024 * 1024 || size === "medium" && item.sizeBytes >= 1024 * 1024 && item.sizeBytes < 20 * 1024 * 1024 || size === "large" && item.sizeBytes >= 20 * 1024 * 1024;
      return searchMatch && kindMatch && sizeMatch;
    });
  }, [activeScope, grouped, kind, query, size]);
  const selected = materials.find((item) => item.id === selectedId);
  const taskDetails = useMemo(()=>new Map(tasks.map(task=>[task.id,task])),[tasks]);
  const taskMetadata = (grouped.byTask.get(activeTaskID) ?? []).filter(item=>item.sourceKind === "research_import" && item.format === "markdown");
  async function clearTaskMetadata() {
    if (!clearRequest || busy) return;
    setBusy("clear-task");setMessage("");
    try {
      const result=await service<{removedIds:string[];errors:string[]}>("KnowledgeFacade","ClearTaskMetadata",project.id,clearRequest.taskID,clearRequest.ids);
      const removed=new Set(result.removedIds);
      setMaterials(current=>current.filter(item=>!removed.has(item.id)));
      setChecked(current=>current.filter(id=>!removed.has(id)));
      setMessage(`已清空 ${removed.size} 份自动 MD 材料。${result.errors?.length ? `失败 ${result.errors.length} 份：${result.errors[0]}` : "收藏、原文件与历史引用保留；未释放磁盘空间。"}`);
      setClearRequest(null);
    } catch(error) {setMessage(safeText(error));setClearRequest(null);}
    finally {setBusy("");}
  }

  useEffect(() => { setChecked([]); setConfirmBatch(false); }, [activeScope, query, kind, size]);
  async function archiveBatch() {
    if (busy || readOnly) return;
    const targets = activeMaterials.filter(item => checked.includes(item.id));
    setBusy("batch"); setMessage("");
    const removed = new Set<string>();
    const failures: string[] = [];
    for (const item of targets) {
      try {
        await service<void>("KnowledgeFacade", "ArchiveMaterial", ...callArgs(item.id));
        removed.add(item.id);
      } catch (error) { failures.push(`${item.title}：${safeText(error)}`); }
    }
    setMaterials(current => current.filter(item => !removed.has(item.id)));
    setChecked(current => current.filter(id => !removed.has(id)));
    setConfirmBatch(false); setBusy("");
    setMessage(`已移出 ${removed.size} 份资料。${failures.length ? `失败 ${failures.length} 份：${failures[0]}` : "原文件及历史引用保留。"}`);
  }

  useEffect(() => {
    if (selectedId && !activeMaterials.some((item) => item.id === selectedId)) setSelectedId("");
  }, [activeMaterials, selectedId]);

  async function addMaterials() {
    if (busy || readOnly) return;
    setBusy("add"); setMessage("");
    try {
      const result = await service<ImportBatch>("KnowledgeFacade", "ChooseReferenceMaterials", project.id, activeTaskID);
      await reload(true);
      const errors = result.errors?.filter((item) => item.message) ?? [];
      if (errors.length) setMessage(`${errors.length} 个文件未能添加：${errors[0]?.message}`);
      else if (result.attachments?.length) setMessage("资料已添加。");
    } catch (error) { setMessage(safeText(error)); }
    finally { setBusy(""); }
  }

  async function collectSelected() {
    if (!selected || busy) return;
    setBusy("collect"); setMessage("");
    try {
      await service<ResearchMaterial>("KnowledgeFacade", "CollectMaterial", ...callArgs(selected.id));
      setMessage("已收藏到本项目资料库，可在新课题中选择。原任务材料保持不变。");
    } catch(error) { setMessage(safeText(error)); }
    finally { await reload(true); setBusy(""); }
  }

  async function saveEmbedding(event: FormEvent) {
    event.preventDefault();
    if (busy || embeddingLoading || !embedding || embeddingEnabled && (!embeddingURL.trim() || !embeddingModel.trim())) return;
    setBusy("embedding"); setEmbeddingError(""); setMessage("");
    try {
      const value = await service<EmbeddingConfig>("KnowledgeFacade", "SaveEmbeddingConfig", project.id, {
        enabled: embeddingEnabled, baseUrl: embeddingURL.trim(), modelId: embeddingModel.trim(), apiKey: embeddingKey, timeoutSeconds: embedding?.timeoutSeconds || 30,
      });
      setEmbedding(value); setEmbeddingEnabled(value.enabled); setEmbeddingURL(value.baseUrl); setEmbeddingModel(value.modelId); setEmbeddingKey("");
      setMessage(value.enabled ? "语义检索设置已保存并验证。" : "已关闭语义检索设置。");
      setEmbeddingOpen(false);
    } catch (error) { setEmbeddingError(safeText(error)); }
    finally { setBusy(""); }
  }

  async function openDetail(value: ResearchMaterial) {
    readEpoch.current += 1;
    setSelectedId(value.id); setDetailOpen(true); setConfirmArchive(false); setDraftTitle(value.title || value.originalName); setDraftNotes(value.notes ?? "");
    setText(""); setNextOffset(undefined); setTotalRunes(0); setMessage(""); setPreviewTruncated(false);
    await loadText(value, 0, false, readEpoch.current);
  }

  async function loadText(value: ResearchMaterial, offset: number, append: boolean, request = readEpoch.current) {
    if (offset < 0 || reading && append) return;
    setReading(true);
    try {
      const result = await service<MaterialText>("KnowledgeFacade", "ReadMaterial", ...callArgs(value.id, offset));
      if (request !== readEpoch.current) return;
      setText((current) => append ? `${current}${result.text ?? ""}` : result.text ?? "");
      setNextOffset(typeof result.nextOffset === "number" && result.nextOffset >= 0 ? result.nextOffset : undefined);
      setTotalRunes(result.totalRunes ?? 0);
      setPreviewTruncated(Boolean(result.truncated));
    } catch (error) { if (request === readEpoch.current) setMessage(safeText(error)); }
    finally { if (request === readEpoch.current) setReading(false); }
  }

  async function saveDetail(event: FormEvent) {
    event.preventDefault();
    if (!selected || busy || readOnly) return;
    setBusy("save"); setMessage("");
    try {
      const updated = await service<ResearchMaterial>("KnowledgeFacade", "SaveMaterial", ...callArgs(selected.id), draftTitle.trim(), draftNotes.trim());
      setMaterials((current) => current.map((item) => item.id === updated.id ? { ...item, ...updated, indexStatus: updated.indexStatus ?? item.indexStatus } : item));
      setDraftTitle(updated.title); setDraftNotes(updated.notes); setMessage("资料说明已保存。");
    } catch (error) { setMessage(safeText(error)); }
    finally { setBusy(""); }
  }

  async function archiveSelected() {
    if (!selected || busy || readOnly) return;
    setBusy("archive"); setMessage("");
    try {
      await service<void>("KnowledgeFacade", "ArchiveMaterial", ...callArgs(selected.id));
      setMaterials((current) => current.filter((item) => item.id !== selected.id));
      setSelectedId(""); setDetailOpen(false); setConfirmArchive(false); setMessage("已从资料清单移出；原文件和已冻结的历史记录不受影响。");
    } catch (error) { setMessage(safeText(error)); }
    finally { setBusy(""); }
  }

  async function downloadSelected() {
    if (!selected || busy) return;
    setBusy("download"); setMessage("");
    try { await service<void>("KnowledgeFacade", "DownloadMaterial", ...callArgs(selected.id)); }
    catch (error) { setMessage(safeText(error)); }
    finally { setBusy(""); }
  }

  const scopeTitle = activeScope === "shared" ? "我的资料" : activeScope === "legacy" ? "历史自动材料" : taskNames.get(activeScope.replace(/^task:/, "")) ?? "任务研究材料";
  const clearDialog = clearRequest && createPortal(<ModalBackdrop className="material-clear-backdrop" close={()=>setClearRequest(null)} busy={Boolean(busy)}><section role="dialog" aria-modal="true" aria-label="确认清空任务自动材料" className="material-clear-dialog"><h3>清空本任务的自动 MD 材料？</h3><p className="material-clear-scope">{clearRequest.title}</p><p>将从本任务清单移出 <strong>{clearRequest.ids.length}</strong> 份自动导入的 Markdown 材料，包含未显示的筛选结果。</p><p>不移出 PDF、用户上传文件或已收藏到“我的资料”的独立记录。保留原文件和历史引用，本操作不释放磁盘空间。</p><footer><button type="button" disabled={Boolean(busy)} data-dialog-dismiss onClick={()=>setClearRequest(null)}>取消</button><button type="button" className="danger" disabled={Boolean(busy)} onClick={()=>void clearTaskMetadata()}>{busy === "clear-task" ? "正在清空…" : `确认清空 ${clearRequest.ids.length} 份`}</button></footer></section></ModalBackdrop>,document.body);
  return <ModalBackdrop className="modal-backdrop research-materials-backdrop" close={close} busy={Boolean(busy)}>{clearDialog}<section className="model-modal research-materials-modal" role="dialog" aria-modal="true" aria-label="研究资料库">
    <header><div><span className="dialog-icon gradient"><span className="research-materials-glyph">▤</span></span><div><p>RESEARCH MATERIALS</p><h2>{project.name} · {taskMaterials ? "任务材料" : "研究资料库"}</h2></div></div><div className="research-materials-header-actions"><button type="button" className="research-materials-settings" disabled={Boolean(busy)} onClick={() => setEmbeddingOpen(true)} aria-haspopup="dialog">高级检索设置</button>{!readOnly && <button type="button" className="research-materials-add" disabled={Boolean(busy)} onClick={() => void addMaterials()}>{busy === "add" ? "正在添加" : "添加资料"}</button>}<button type="button" className="close" aria-label="关闭研究资料库" data-dialog-dismiss onClick={close}>×</button></div></header>
    <div className="research-materials-layout">
      <aside className="research-materials-scopes"><header><b>{project.name}</b><small>{taskMaterials ? "文献发现 · 已导入任务材料" : "仅你主动上传或加入的参考资料"}</small></header><button type="button" disabled={Boolean(busy)} className={activeScope === "shared" ? "selected" : ""} onClick={() => setActiveScope("shared")}><span>我的资料 · 开题可选</span><i>{grouped.shared.length}</i></button>{taskMaterials && <details open className="research-materials-task-group"><summary>任务研究材料 <span>{[...grouped.byTask.values()].reduce((sum,items)=>sum+items.length,0)+grouped.legacy.length}</span></summary><p>自动材料保留在原任务，收藏后可供新课题选择。</p>{[...grouped.byTask.keys()].map(id=><button type="button" disabled={Boolean(busy)} className={activeScope === `task:${id}` ? "selected" : ""} key={id} onClick={()=>setActiveScope(`task:${id}`)}><span className="material-task-copy" title={taskNames.get(id)}><small>{taskDetails.get(id)?.createdAt ? new Date(taskDetails.get(id)!.createdAt!).toLocaleString("zh-CN",{month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"}) : "历史任务"} · {id.slice(0,6)}</small><b>{taskDetails.get(id)?.title || "未命名任务"}</b></span><i>{grouped.byTask.get(id)?.length ?? 0}</i></button>)}{grouped.legacy.length>0 && <button type="button" disabled={Boolean(busy)} onClick={()=>setActiveScope("legacy")}><span>历史自动材料（未关联任务）</span><i>{grouped.legacy.length}</i></button>}</details>}</aside>
      <main className="research-materials-main">
        <header className="research-materials-toolbar"><div><b title={scopeTitle}>{scopeTitle}</b><small>{activeTaskID ? "本任务研究材料 · 点击文献预览或收藏" : readOnly ? "当前任务只读查看项目共享资料" : "仅主动添加或收藏的资料可供新课题选择"}</small></div><label className="research-materials-search"><span>⌕</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、标题或备注"/></label><select aria-label="按资料类型筛选" value={kind} onChange={(event) => setKind(event.target.value)}><option value="all">全部类型</option>{Object.entries(contentLabel).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select><select aria-label="按文件大小筛选" value={size} onChange={(event) => setSize(event.target.value)}><option value="all">全部大小</option><option value="small">小于 1 MB</option><option value="medium">1-20 MB</option><option value="large">大于 20 MB</option></select></header>
        {activeTaskID && !readOnly && <div className="material-scope-actions"><span>当前任务 · {grouped.byTask.get(activeTaskID)?.length ?? 0} 份资料</span><button type="button" className="material-clear-task" disabled={Boolean(busy) || !taskMetadata.length} onClick={()=>setClearRequest({taskID:activeTaskID,title:scopeTitle,ids:taskMetadata.map(item=>item.id)})}>清空本任务自动 MD{taskMetadata.length ? ` (${taskMetadata.length})` : ""}</button></div>}
        {message && <div className="research-materials-feedback" role="status"><span>{message}</span><button type="button" aria-label="关闭提示" onClick={() => setMessage("")}>×</button></div>}
        {!readOnly && activeMaterials.length > 0 && <div className="research-materials-batch"><label><input type="checkbox" aria-label="全选当前资料" checked={activeMaterials.every(item => checked.includes(item.id))} disabled={Boolean(busy)} onChange={e => setChecked(e.target.checked ? activeMaterials.map(item => item.id) : [])}/>全选</label><span>已选 {checked.length}</span><button type="button" disabled={!checked.length || Boolean(busy)} onClick={() => setConfirmBatch(true)}>移出所选</button>{confirmBatch && <><span>保留原件与历史引用，确认移出？</span><button type="button" disabled={Boolean(busy)} onClick={() => setConfirmBatch(false)}>取消</button><button type="button" disabled={Boolean(busy)} onClick={() => void archiveBatch()}>{busy === "batch" ? "正在移出…" : "确认批量移出"}</button></>}</div>}
        {loading ? <div className="research-materials-empty"><b>正在读取资料…</b></div> : activeMaterials.length ? <div className="research-materials-list">{activeMaterials.map((item) => <article key={item.id} className={selectedId === item.id ? "selected" : ""}>{!readOnly && <input className="research-materials-check" type="checkbox" aria-label={`选择 ${item.title}`} checked={checked.includes(item.id)} disabled={Boolean(busy)} onChange={e => setChecked(current => e.target.checked ? [...current, item.id] : current.filter(id => id !== item.id))}/>}<button type="button" className="research-materials-row" aria-label={`预览 ${item.title || item.originalName}`} onClick={() => void openDetail(item)}><span className={`research-materials-file format-${item.format.toLowerCase()}`}>{item.contentKind === "metadata_abstract" ? "题录" : item.format.slice(0, 4).toUpperCase()}</span><span className="research-materials-copy"><b>{item.title || item.originalName}</b><small>{sourceLabel(item)} · {fileSize(item.sizeBytes)} · {formatDate(item.createdAt)}</small><em className="material-index-summary">{item.indexChunks === undefined ? "索引未就绪" : `索引片段 ${item.indexChunks} 个`} · 提取 {(item.extractedRunes ?? 0).toLocaleString()} 字{item.parseSummary ? ` · ${item.parseSummary}` : ""}</em>{item.notes && <em>{item.notes}</em>}</span><span className="research-materials-tags"><i>{contentLabel[item.contentKind]}</i><small className="material-preview-link">预览文献 →</small></span></button></article>)}</div> : <div className="research-materials-empty"><span>▤</span><b>{query || kind !== "all" || size !== "all" ? "没有符合条件的资料" : "这里还没有资料"}</b><p>{readOnly ? "当前任务可查看项目共享资料。" : "添加本地文献，或在“文献发现”中选择文献并点击“加入资料库”。任务检索结果不会自动加入这里。"}</p>{!readOnly && <button type="button" onClick={() => void addMaterials()} disabled={Boolean(busy)}>添加资料</button>}</div>}
      </main>
    </div>
    {detailOpen && selected && createPortal(<ModalBackdrop className="research-materials-detail-backdrop" close={()=>setDetailOpen(false)} busy={Boolean(busy)} dirty={!readOnly && (draftTitle!==(selected.title||selected.originalName)||draftNotes!==(selected.notes||""))}><section className="research-materials-detail" role="dialog" aria-modal="true" aria-label={`${selected.title || selected.originalName} 资料详情`}><header><div><span className="research-materials-file">{selected.format.slice(0, 4).toUpperCase()}</span><div><p>RESEARCH MATERIAL</p><h3>{selected.title || selected.originalName}</h3><small>{contentLabel[selected.contentKind]}</small></div></div><button type="button" className="close" aria-label="关闭资料详情" disabled={Boolean(busy)} data-dialog-dismiss onClick={() => setDetailOpen(false)}>×</button></header><div className="research-materials-detail-body"><section className="research-materials-read"><header><div><b>文献预览</b><small>这是可读取的文本提取，不是原始 PDF 页面。</small></div><span>{totalRunes ? `${text.length.toLocaleString()} / ${totalRunes.toLocaleString()} 字符` : ""}</span></header>{selected.contentKind === "metadata_abstract" && <p className="research-materials-limited">这份资料是题录或摘要材料，不代表已获得全文。</p>}{selected.contentKind === "full_text" && <p className="research-materials-limited">来源提供了全文材料；请结合原文和定位内容核验具体结论。</p>}{previewTruncated && <p className="research-materials-limited">解析结果存在截断，以下预览不代表完整原文。</p>}<pre>{reading && !text ? "正在读取…" : text || "此资料没有可读取的正文。"}</pre>{nextOffset !== undefined && <button type="button" disabled={reading} onClick={() => void loadText(selected, nextOffset, true)}>{reading ? "正在继续读取" : "加载更多"}</button>}</section><aside><section className="material-index-info"><b>导入与索引</b><p>{selected.indexChunks === undefined ? "索引信息暂不可用" : `已分割为 ${selected.indexChunks} 个索引片段`}</p><small>片段保存在索引中，不是独立 MD 文件。</small><p>提取 {(selected.extractedRunes ?? 0).toLocaleString()} 字 · {selected.unitCount ?? 0} 个解析单元</p>{selected.parseSummary && <small>{selected.parseSummary}</small>}</section>{message && <div className="research-materials-feedback" role="status">{message}</div>}<form onSubmit={saveDetail}><header><b>资料说明</b><small>{readOnly ? "当前任务中只读" : "仅修改资料名称与备注"}</small></header><label>显示名称<input value={draftTitle} disabled={readOnly || Boolean(busy)} onChange={(event) => setDraftTitle(event.target.value)} maxLength={300}/></label><label>备注<textarea value={draftNotes} disabled={readOnly || Boolean(busy)} onChange={(event) => setDraftNotes(event.target.value)} maxLength={2000} placeholder="例如：作为方法参考，不作为结论证据"/></label>{!readOnly && <button type="submit" disabled={Boolean(busy) || !draftTitle.trim()}>{busy === "save" ? "正在保存" : "保存说明"}</button>}</form><details className="research-materials-technical"><summary>技术详情</summary><dl><div><dt>原文件</dt><dd>{selected.originalName}</dd></div>{selected.originTaskTitle && <div><dt>收藏来源</dt><dd>{selected.originTaskTitle}</dd></div>}<div><dt>来源</dt><dd>{sourceLabel(selected)}</dd></div><div><dt>文件</dt><dd>{selected.format.toUpperCase()} · {fileSize(selected.sizeBytes)}</dd></div><div><dt>保存时间</dt><dd>{formatDate(selected.createdAt)}</dd></div><div><dt>处理状态</dt><dd>{indexLabel(selected.indexStatus)}</dd></div><div><dt>SHA256</dt><dd><code>{selected.sha256 || "未记录"}</code></dd></div></dl></details><div className="research-materials-detail-actions">{!selected.reusable && <button type="button" disabled={Boolean(busy) || selected.status !== "ready"} onClick={() => void collectSelected()}>{busy === "collect" ? "正在收藏…" : "收藏到资料库"}</button>}<button type="button" disabled={Boolean(busy)} onClick={() => void downloadSelected()}>{busy === "download" ? "正在准备" : "下载原文件"}</button>{!readOnly && <button type="button" className="danger" disabled={Boolean(busy)} onClick={() => setConfirmArchive(true)}>移出资料库</button>}</div>{confirmArchive && <div className="research-materials-confirm"><b>移出这份资料？</b><p>资料将从当前清单、默认检索和后续选择中移除；不会物理删除原文件，也不影响已冻结的任务记录。</p><div><button type="button" disabled={Boolean(busy)} onClick={() => setConfirmArchive(false)}>取消</button><button type="button" className="danger" disabled={Boolean(busy)} onClick={() => void archiveSelected()}>{busy === "archive" ? "正在移出" : "确认移出"}</button></div></div>}</aside></div></section></ModalBackdrop>, document.body)}
  </section>
    {embeddingOpen && createPortal(<ModalDialog dialogRef={embeddingDialog} className="material-search-settings" aria-label="高级检索设置" close={()=>setEmbeddingOpen(false)} busy={Boolean(busy)} dirty={Boolean(embedding && (embeddingEnabled!==embedding.enabled||embeddingURL!==embedding.baseUrl||embeddingModel!==embedding.modelId||embeddingKey))}>
      <header><div><h3>高级检索设置</h3><p>配置用于资料语义检索的 Embedding 服务。</p></div><button type="button" aria-label="关闭高级检索设置" disabled={Boolean(busy)} data-dialog-dismiss onClick={() => setEmbeddingOpen(false)}>×</button></header>
      <form onSubmit={saveEmbedding}>
        {embeddingLoading && <p role="status">正在读取配置…</p>}
        {embeddingError && <p className="material-settings-error" role="alert">{embeddingError}</p>}
        <fieldset disabled={embeddingLoading || !embedding || Boolean(busy)}>
          <label className="material-settings-toggle"><input type="checkbox" checked={embeddingEnabled} onChange={event => setEmbeddingEnabled(event.target.checked)}/>启用语义检索</label>
          <label>服务地址<input aria-label="Embedding Base URL" value={embeddingURL} disabled={!embeddingEnabled} onChange={event => setEmbeddingURL(event.target.value)} placeholder="Base URL"/></label>
          <label>模型名称<input aria-label="Embedding 模型" value={embeddingModel} disabled={!embeddingEnabled} onChange={event => setEmbeddingModel(event.target.value)} placeholder="Embedding Model ID"/></label>
          <label>API Key<input aria-label="Embedding API Key" type="password" autoComplete="new-password" value={embeddingKey} disabled={!embeddingEnabled} onChange={event => setEmbeddingKey(event.target.value)} placeholder={embedding?.secretConfigured ? "留空保留现有密钥" : "API Key（可选）"}/></label>
        </fieldset>
        <footer><button type="button" disabled={Boolean(busy)} data-dialog-dismiss onClick={() => setEmbeddingOpen(false)}>取消</button><button type="submit" className="primary" disabled={Boolean(busy) || embeddingLoading || !embedding || embeddingEnabled && (!embeddingURL.trim() || !embeddingModel.trim())}>{busy === "embedding" ? "正在保存" : "保存设置"}</button></footer>
      </form>
    </ModalDialog>, document.body)}
  </ModalBackdrop>;
}
