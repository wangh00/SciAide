import {useEffect, useRef, useState} from "react";
import {ModalDialog} from "./Modal";
import type {Attachment} from "./App";

export function RevisionLibraryPicker({projectId, service, close, choose}: {
  projectId: string; service: <T>(facade:string,method:string,...args:unknown[])=>Promise<T>;
  close:()=>void; choose:(files:Attachment[])=>void;
}) {
  const dialog=useRef<HTMLDialogElement>(null);
  const [items,setItems]=useState<Attachment[]>([]);
  const [selected,setSelected]=useState<string[]>([]);
  const [error,setError]=useState("");
  const [loading,setLoading]=useState(true);
  useEffect(()=>{
    let disposed=false;
    service<Attachment[]>("KnowledgeFacade","ListReferenceMaterials",projectId,"").then(values=>{
      if(!disposed) setItems((values||[]).filter(v=>v.scopeKind==="project_shared" && v.status==="ready" && ["pdf","docx","text","markdown"].includes(v.format)));
    }).catch(e=>{if(!disposed)setError(String(e));}).finally(()=>{if(!disposed)setLoading(false);});
    return ()=>{disposed=true;};
  },[projectId,service]);
  return <ModalDialog dialogRef={dialog} className="reference-material-dialog" open close={close}>
    <header><h3>从资料库补充文献</h3><button type="button" aria-label="关闭资料选择" onClick={close}>×</button></header>
    <p>先随消息发送供 AI 核对，确认返修方案后才纳入研究。</p>
    {error&&<p role="alert">{error}</p>}
    <div className="reference-material-list">{items.map(v=><label key={v.id}><input type="checkbox" checked={selected.includes(v.id)} disabled={!selected.includes(v.id)&&selected.length>=16} onChange={e=>setSelected(ids=>e.target.checked?[...ids,v.id]:ids.filter(id=>id!==v.id))}/><span><b>{v.originalName}</b><small>{Math.max(1,Math.round(v.sizeBytes/1024))} KiB</small></span></label>)}{!items.length&&<p>{loading?"正在读取资料库…":"暂无可用文献，可使用附件按钮上传本地文件。"}</p>}</div>
    <footer><span>已选 {selected.length} / 16</span><button type="button" disabled={!selected.length||loading} onClick={()=>choose(items.filter(v=>selected.includes(v.id)))}>添加到待发送</button></footer>
  </ModalDialog>;
}
