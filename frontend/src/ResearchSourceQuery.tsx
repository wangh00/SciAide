import {sourceQueryProjectionView, type SourceQueryAudit} from "./researchSources.js";

export function ResearchSourceQuery({source}: {source: SourceQueryAudit}) {
 const view = sourceQueryProjectionView(source);
 if (!view) return null;
 return <details style={{gridColumn:"1 / -1", minWidth:0, overflowWrap:"anywhere"}}>
  <summary>来源查询 · {view.mode}</summary>
  {view.logical && <p>对应逻辑检索式：<code>{view.logical}</code></p>}
  <p>{view.origin}：<code>{view.input}</code></p>
  {view.query && <p>编译后的来源查询表达式：<code>{view.query}</code></p>}
  <p>表达式及拟应用过滤不证明本次请求已发送；实际请求、缓存和失败需查执行记录。</p>
  {view.error && <p role="status">{view.error}</p>}
  {view.years && <p>{view.years}</p>}
  {view.statement && <p>{view.statement}</p>}
  {view.limitations.length > 0 && <ul>{view.limitations.map(text => <li key={text}>{text}</li>)}</ul>}
 </details>;
}
