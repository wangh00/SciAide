import {useEffect, useRef, useState} from "react";
import type {WorkingSourceField} from "./researchWorkingSources";

export type WorkingBasis = {readCallId:string;status:"linked_unverified_abstract"|"unavailable";queryId?:string;candidateId?:string;sha256?:string;offset:number;endOffset:number;totalRunes:number};
export type WorkingBasisScope = {projectId:string;taskId:string;stateId:string;version:number;field:WorkingSourceField;basis:WorkingBasis};
type BasisRead = {projectId:string;researchTaskId:string;stateId:string;version:number;field:WorkingSourceField;basis:WorkingBasis;content:string};
type BasisView = {phase:"idle"|"loading"|"error"}|{phase:"ready";value:BasisRead};
type Service = <T>(facade:string,method:string,...args:unknown[])=>Promise<T>;

export function validWorkingBasis(b:WorkingBasis): boolean {
  if (!b || typeof b.readCallId!=="string" || !b.readCallId.trim() || b.readCallId!==b.readCallId.trim() || Array.from(b.readCallId).length>100) return false;
  if (b.status==="unavailable") return !b.queryId&&!b.candidateId&&!b.sha256&&b.offset===0&&b.endOffset===0&&b.totalRunes===0;
  return b.status==="linked_unverified_abstract" && [b.queryId,b.candidateId].every(id=>typeof id==="string"&&Boolean(id.trim())) && typeof b.sha256==="string" && /^[a-f0-9]{64}$/.test(b.sha256) && [b.offset,b.endOffset,b.totalRunes].every(Number.isSafeInteger) && b.offset>=0 && b.endOffset>b.offset && b.endOffset<=b.totalRunes && b.endOffset-b.offset<=400;
}

export function createWorkingBasisReader(options:WorkingBasisScope & {service:Service;onChange:(view:BasisView)=>void}) {
  let disposed=false,inFlight=false,loaded=false;
  async function load(){
    if(disposed||inFlight||loaded||!validWorkingBasis(options.basis)||options.basis.status!=="linked_unverified_abstract")return;
    inFlight=true;options.onChange({phase:"loading"});
    try{
      const value=await options.service<BasisRead>("WorkflowFacade","GetResearchWorkingStateBasis",options.projectId,options.taskId,options.version,options.field,options.basis.readCallId);
      if(disposed)return;
      if(!value||value.projectId!==options.projectId||value.researchTaskId!==options.taskId||value.stateId!==options.stateId||value.version!==options.version||value.field!==options.field||!validWorkingBasis(value.basis)||Object.keys(options.basis).some(key=>value.basis[key as keyof WorkingBasis]!==options.basis[key as keyof WorkingBasis])||typeof value.content!=="string"||Array.from(value.content).length!==options.basis.endOffset-options.basis.offset)throw Error("Original working-note abstract page unavailable");
      loaded=true;options.onChange({phase:"ready",value});
    }catch{if(!disposed)options.onChange({phase:"error"});}finally{inFlight=false;}
  }
  return {load,dispose(){disposed=true;}};
}

// Original normalized abstract page only. Never fetch, import, invoke a model,
// interpret source text as HTML/instructions, or imply scientific approval.
export function WorkingBasisPage({scope,service}:{scope:WorkingBasisScope;service?:Service}){
  const identity=JSON.stringify(scope);
  const [state,setState]=useState<{identity:string;view:BasisView}>({identity,view:{phase:"idle"}});
  const view:BasisView=state.identity===identity?state.view:{phase:"idle"};
  const reader=useRef<ReturnType<typeof createWorkingBasisReader>|null>(null);
  useEffect(()=>{
    if(!service)return;
    const current=createWorkingBasisReader({...scope,service,onChange:view=>setState({identity,view})});reader.current=current;
    return()=>{current.dispose();if(reader.current===current)reader.current=null;};
  },[identity,service]);
  if(scope.basis.status!=="linked_unverified_abstract")return <p>模型关联的摘要页 · 原读取记录无法核对</p>;
  return <details className="research-working-basis" onToggle={event=>{if(event.currentTarget.open)void reader.current?.load();}}>
    <summary>模型关联的摘要页 · 未核验（字符 {scope.basis.offset}–{scope.basis.endOffset}）</summary>
    <p>保存的规范化摘要，不是全文；关联由模型选择，不代表支持该判断或科学批准。</p>
    <p>读取回执：{scope.basis.readCallId}</p><p>查询：{scope.basis.queryId} · 候选：{scope.basis.candidateId}</p><p>摘要 SHA-256：{scope.basis.sha256}</p>
    {!service&&<p role="alert">只读摘要入口不可用。</p>}
    {service&&view.phase==="idle"&&<p>展开后只读原摘要页，不联网。</p>}
    {view.phase==="loading"&&<p role="status">正在读取原摘要页…</p>}
    {view.phase==="error"&&<p role="alert">无法核对原摘要页，请刷新记录后重试。<button type="button" onClick={()=>void reader.current?.load()}>重试原摘要页</button></p>}
    {view.phase==="ready"&&<blockquote style={{whiteSpace:"pre-wrap",overflowWrap:"anywhere"}}>{view.value.content}</blockquote>}
  </details>;
}
