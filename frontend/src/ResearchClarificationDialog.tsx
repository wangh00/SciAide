export type ResearchClarificationQuestion = {
  kind?: "research_direction";
  id: string; text: string; required: boolean; impact?: string;
  selectionMode?: "single" | "multiple";
  options: { id: string; label: string }[];
};

export function clarificationModeLabel(question: ResearchClarificationQuestion): string {
  return question.selectionMode === "multiple" ? "多选" : "单选";
}

export function clarificationSummary(question: ResearchClarificationQuestion, selected: string[]): string {
  return question.options.filter((option) => selected.includes(option.id)).map((option) => option.label).join("；");
}

export function clarificationSelection(question: ResearchClarificationQuestion, selected: string[], id: string): string[] {
  if (!question.options.some((option) => option.id === id)) return selected;
  if (question.selectionMode !== "multiple") return [id];
  return selected.includes(id) ? selected.filter((value) => value !== id) : [...selected, id];
}

// Controlled draft: only Confirm may commit; all dismissal paths discard the draft.
export function ResearchClarificationDialog({ question, draft, setDraft, close, confirm }: {
  question: ResearchClarificationQuestion; draft: string[];
  setDraft: (draft: string[]) => void; close: () => void; confirm: (draft: string[]) => void;
}) {
  return <div className="modal-backdrop research-clarification-backdrop" onMouseDown={(event) => { if (event.currentTarget === event.target) close(); }}>
    <section className="model-modal research-clarification-dialog" role="dialog" aria-modal="true" aria-labelledby="research-clarification-title">
      <header><h2 id="research-clarification-title">研究边界选择 · {clarificationModeLabel(question)}</h2><button type="button" className="close" onClick={close} aria-label="关闭选择窗口">×</button></header>
      <div className="research-clarification-dialog-body"><h3 id="research-clarification-question" className="research-clarification-question">{question.text}</h3>{question.impact && <p className="research-clarification-impact">{question.impact}</p>}<div className="research-clarification-dialog-options" role="group" aria-labelledby="research-clarification-question">{question.options.map((option) => <label key={option.id}><input type={question.selectionMode === "multiple" ? "checkbox" : "radio"} name={`clarification-dialog-${question.id}`} checked={draft.includes(option.id)} onChange={() => setDraft(clarificationSelection(question, draft, option.id))}/><span>{option.label}</span></label>)}</div></div>
      <footer className="research-clarification-dialog-actions"><button type="button" onClick={close}>取消</button><button type="button" className="primary" disabled={question.required && draft.length === 0} onClick={() => confirm([...draft])}>确认选择</button></footer>
    </section>
  </div>;
}
