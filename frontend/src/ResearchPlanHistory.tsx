import { useState } from "react";
import { createPortal } from "react-dom";
import { ModalDialog } from "./Modal";

type Plan = { id: string; name: string; description?: string; version: number };

export function ResearchPlanHistory<T extends Plan>({ plans, loading, busy, feedback, selecting, selection, setSelecting, setSelection, refresh, openPlan, deletePlan, deleteSelected, close }: {
  plans: T[]; loading: boolean; busy: boolean; feedback: string;
  selecting: boolean; selection: string[];
  setSelecting: (value: boolean) => void; setSelection: (ids: string[]) => void;
  refresh: () => void; openPlan: (plan: T) => Promise<void>;
  deletePlan: (plan: T) => Promise<void>; deleteSelected: () => Promise<void>; close: () => void;
}) {
  const [opening, setOpening] = useState("");
  const disabled = busy || loading || Boolean(opening);
  const selected = plans.filter(plan => selection.includes(plan.id));
  async function choose(plan: T) {
    if (disabled) return;
    setOpening(plan.id);
    try { await openPlan(plan); } finally { setOpening(""); }
  }
  return createPortal(<ModalDialog className="research-plan-history-dialog" aria-label="历史方案" close={close} busy={busy || Boolean(opening)}>
    <header><div><p>RESEARCH PLANS</p><h2>历史方案</h2><small>查看以前保存的路线。打开只查看设置，不会启动任务。</small></div><button type="button" aria-label="关闭历史方案" data-dialog-dismiss>×</button></header>
    <nav className="research-plan-history-tools" aria-label="历史方案管理">
      <span>{plans.length} 个方案</span>
      {selecting && <><label><input type="checkbox" aria-label="全选历史方案" disabled={disabled} checked={plans.length > 0 && selected.length === plans.length} onChange={event => setSelection(event.target.checked ? plans.map(plan => plan.id) : [])}/>全选</label><button type="button" disabled={disabled || !selected.length} onClick={() => void deleteSelected()}>删除所选（{selected.length}）</button></>}
      <button type="button" disabled={disabled || (!plans.length && !selecting)} onClick={() => {setSelecting(!selecting);setSelection([]);}}>{selecting ? "取消多选" : "多选"}</button>
      <button type="button" disabled={disabled} aria-label="刷新历史方案" onClick={refresh}>刷新</button>
    </nav>
    <div className="research-plan-history-body">
      {feedback && <p className="research-plan-history-feedback" role="status">{feedback}</p>}
      <div className="workflow-saved-plan-list">{loading ? <p>正在读取历史方案…</p> : plans.length ? plans.map(plan => <article key={plan.id}>
        {selecting && <input className="workflow-plan-checkbox" type="checkbox" aria-label={`选择历史方案 ${plan.name}`} checked={selection.includes(plan.id)} disabled={disabled} onChange={event => setSelection(event.target.checked ? [...selection, plan.id] : selection.filter(id => id !== plan.id))}/>}
        <button type="button" disabled={disabled} onClick={() => void choose(plan)}><i aria-hidden="true">{opening === plan.id ? "…" : "↗"}</i><span><b>{plan.name}</b><small>{plan.description || "未填写方案说明"}</small></span><em>v{plan.version}</em></button>
        <button type="button" className="workflow-delete" disabled={disabled} aria-label={`删除历史方案 ${plan.name}`} title="删除方案及已结束任务历史" onClick={() => void deletePlan(plan)}>×</button>
      </article>) : <div className="workflow-plan-home-empty"><span><b>暂无历史方案</b><small>先从对话开始，确认采用的研究路线会保存在这里。</small></span></div>}</div>
    </div>
    <footer><small>已有任务的进度和结果，请从左侧「科研任务」进入。</small><button type="button" data-dialog-dismiss>返回研究方案</button></footer>
  </ModalDialog>, document.body);
}
