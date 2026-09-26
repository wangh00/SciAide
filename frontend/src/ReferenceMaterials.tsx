import {ModalDialog} from "./Modal";
import { useEffect, useRef, useState } from "react";
import "./referenceMaterials.css";

export type ReferenceMaterial = { id: string; originalName: string; status: string; format: string; scopeKind?: string; sizeBytes: number };
type Service = <T>(facade: string, method: string, ...args: unknown[]) => Promise<T>;

function MaterialIcon({ kind }: { kind: "upload" | "library" | "file" }) {
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{kind === "upload" ? <><path d="M12 16V4m-4 4 4-4 4 4"/><path d="M5 15v4a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-4"/></> : kind === "library" ? <><rect x="4" y="4" width="5" height="16" rx="1"/><path d="M13 4v16m4-15 4 14M4 8h5"/></> : <><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8zM14 3v5h5M9 13h6M9 17h4"/></>}</svg>;
}

export function ReferenceMaterials({ projectId, taskId = "", selected, onChange, disabled, service, onBusyChange }: {
  projectId: string; taskId?: string; selected: string[]; onChange: (ids: string[]) => void; disabled?: boolean; service: Service; onBusyChange?: (busy: boolean) => void;
}) {
  const [items, setItems] = useState<ReferenceMaterial[]>([]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const epoch = useRef(0);
  async function openPicker() {
    const generation = ++epoch.current;
    setBusy(true); setError("");
    try {
      const values = await service<ReferenceMaterial[]>("KnowledgeFacade", "ListReferenceMaterials", projectId, taskId);
      if (generation !== epoch.current) return;
      const ready = (values || []).filter(v => v.status === "ready" && ["pdf", "docx", "txt", "text", "md", "markdown"].includes(v.format));
      setItems(ready);
      onChange(selected.filter(id => ready.some(v => v.id === id)));
      setOpen(true);
    } catch (e) { if (generation === epoch.current) setError(String(e)); }
    finally { if (generation === epoch.current) setBusy(false); }
  }
  useEffect(() => { onBusyChange?.(busy); return () => { onBusyChange?.(false); }; }, [busy, onBusyChange]);
  useEffect(() => {
    const generation = ++epoch.current;
    setItems([]); setError(""); setBusy(false); setOpen(false);
    service<ReferenceMaterial[]>("KnowledgeFacade", "ListReferenceMaterials", projectId, taskId).then(values => {
      if (epoch.current === generation) setItems(current => {
        const loaded = (values || []).filter(v => v.status === "ready" && ["pdf", "docx", "txt", "text", "md", "markdown"].includes(v.format));
        return [...loaded.filter(v => !current.some(item => item.id === v.id)), ...current];
      });
    }).catch(e => { if (epoch.current === generation) setError(String(e)); });
    return () => { ++epoch.current; };
  }, [projectId, taskId, service]);

  async function upload() {
    const generation = ++epoch.current;
    setBusy(true); setError("");
    try {
      const result = await service<{ attachments: ReferenceMaterial[]; errors: { message: string }[] }>("KnowledgeFacade", "ChooseReferenceMaterials", projectId, taskId);
      if (generation !== epoch.current) return;
      const ready = (result.attachments || []).filter(v => v.status === "ready");
      setItems(values => [...values.filter(v => !ready.some(r => r.id === v.id)), ...ready]);
      const ids = [...new Set([...selected, ...ready.map(v => v.id)])];
      if (ids.length <= 16) onChange(ids);
      else setError("一次最多选择 16 份资料；文件已保存，请在资料库中调整选择。");
      if (result.errors?.length) setError(result.errors.map(e => e.message).join("；"));
    } catch (e) { if (generation === epoch.current) setError(String(e)); }
    finally { if (generation === epoch.current) setBusy(false); }
  }
  const visible = items.filter(v => v.originalName.toLocaleLowerCase().includes(query.toLocaleLowerCase()));
  return <section className="reference-materials" aria-label="课题参考资料">
    <div className="reference-material-toolbar"><div className="reference-material-actions"><button type="button" disabled={disabled || busy} title={taskId ? "仅保存到本任务，并自动选为参考" : "保存到本项目资料库，并自动选为本课题参考"} onClick={() => void upload()}><MaterialIcon kind="upload"/>{busy ? "正在处理…" : "添加本地文献"}</button><button type="button" disabled={disabled || busy} aria-expanded={open} aria-haspopup="dialog" onClick={() => void openPicker()}><MaterialIcon kind="library"/>从资料库选择</button></div>{selected.length > 0 && <span className="reference-material-count">已选 {selected.length} / 16</span>}</div>
    {selected.length > 0 && <ul>{selected.map(id => <li key={id}><MaterialIcon kind="file"/><span title={items.find(v => v.id === id)?.originalName}>{items.find(v => v.id === id)?.originalName || "已选参考资料"}</span><button type="button" title="取消选择" aria-label="取消选择参考资料" disabled={disabled || busy} onClick={() => onChange(selected.filter(v => v !== id))}>×</button></li>)}</ul>}
    {error && <p role="alert">{error}</p>}
    <ModalDialog dialogRef={dialog} className="reference-material-dialog" open={open} close={() => setOpen(false)} busy={busy}>
      <header><h3>选择参考资料</h3><button type="button" aria-label="关闭资料选择" data-dialog-dismiss onClick={() => setOpen(false)}>×</button></header>
      <input aria-label="搜索资料名称" placeholder="搜索资料名称" value={query} onChange={e => setQuery(e.target.value)}/>
      <div className="reference-material-list">{visible.map(v => <label key={v.id}><input type="checkbox" checked={selected.includes(v.id)} disabled={disabled || busy || (!selected.includes(v.id) && selected.length >= 16)} onChange={e => onChange(e.target.checked ? [...selected, v.id] : selected.filter(id => id !== v.id))}/><span><b>{v.originalName}</b><small>{v.scopeKind === "task" ? "本任务资料" : "项目共享"} · {Math.max(1, Math.round(v.sizeBytes / 1024))} KiB</small></span></label>)}{!visible.length && <p>{query ? "没有匹配的资料" : "暂无可选资料。请添加本地文献，或在文献发现中将文献加入资料库后再选择。未完成解析的文件暂不可选。"}</p>}</div>
      <footer><span>已选 {selected.length} / 16</span><button type="button" data-dialog-dismiss onClick={() => setOpen(false)}>完成</button></footer>
    </ModalDialog>
  </section>;
}
