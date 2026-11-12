import {useEffect, useId, useLayoutEffect, useRef, useState} from "react";
import {ResearchCard} from "./ResearchCard";

export type UserInputQuestion = {
  id: string; header?: string; description?: string; question: string; required: boolean;
  selectionMode?: "single" | "multiple";
  options?: {id: string; label: string; description?: string}[];
};
export type UserInputAnswer = {optionIds?: string[]; text?: string};
export type UserInputAnswers = Record<string, UserInputAnswer>;
export type UserInputRequest = {runId: string; callId: string; questions: UserInputQuestion[]};

export function questionAnswered(question: UserInputQuestion, answer?: UserInputAnswer) {
  return !question.required || Boolean(answer?.optionIds?.length || answer?.text?.trim());
}
export function selectQuestionOption(question: UserInputQuestion, answer: UserInputAnswer, id: string): UserInputAnswer {
  if (!question.options?.some(option => option.id === id)) return answer;
  const selected = answer.optionIds ?? [];
  return {...answer, optionIds: question.selectionMode === "multiple"
    ? selected.includes(id) ? selected.filter(value => value !== id) : [...selected, id]
    : selected.includes(id) ? [] : [id]};
}

const slideDuration = 280;

// One inline surface for ordinary dialogue and saved planner questions. A
// question's Confirm advances locally; only the last Confirm submits the whole
// batch to the existing host wait/resume API. No portal, modal or focus trap.
export function UserInputCard({questions, answers, setAnswers, submit, busy = false, disabled = false,
  error = "", intro = "", skip, initialIndex = 0, onIndexChange}: {
  questions: UserInputQuestion[]; answers: UserInputAnswers; setAnswers: (answers: UserInputAnswers) => void;
  submit: (answers: UserInputAnswers) => void; busy?: boolean; disabled?: boolean; error?: string; intro?: string;
  skip?: () => void; initialIndex?: number; onIndexChange?: (index: number) => void;
}) {
  const [index, setIndex] = useState(Math.max(0, Math.min(initialIndex, questions.length - 1)));
  const [slide, setSlide] = useState<{from: number; direction: "forward" | "backward"} | null>(null);
  const [height, setHeight] = useState<number>();
  const panelRef = useRef<HTMLDivElement>(null);
  const movingRef = useRef(false);
  const prefix = useId();
  const current = Math.min(index, questions.length - 1);
  const question = questions[current];
  const locked = busy || disabled || Boolean(slide);
  const complete = questions.every(q => questionAnswered(q, answers[q.id]));

  useLayoutEffect(() => {
    const panel = panelRef.current;
    if (!panel) return;
    const measure = () => setHeight(panel.getBoundingClientRect().height);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(panel);
    return () => observer.disconnect();
  }, [current]);
  useEffect(() => {
    if (!slide) return;
    const timer = window.setTimeout(() => { movingRef.current = false; setSlide(null); }, slideDuration);
    return () => window.clearTimeout(timer);
  }, [slide]);
  useEffect(() => {
    if (!slide) return;
    panelRef.current?.querySelector<HTMLElement>("h3")?.focus({preventScroll: true});
  }, [current, slide]);

  if (!question) return null;
  function go(next: number) {
    if (movingRef.current || locked || next < 0 || next >= questions.length || next === current) return;
    movingRef.current = true;
    setSlide({from: current, direction: next > current ? "forward" : "backward"});
    setIndex(next); onIndexChange?.(next);
  }
  function confirm() {
    if (!question || locked || !questionAnswered(question, answers[question.id])) return;
    if (current < questions.length - 1) go(current + 1);
    else if (complete) submit(answers);
    else go(questions.findIndex(q => !questionAnswered(q, answers[q.id])));
  }
  function renderQuestion(q: UserInputQuestion, outgoing = false) {
    const answer = answers[q.id] ?? {};
    const titleId = `${prefix}-${outgoing ? "previous" : "current"}`;
    const update = (next: UserInputAnswer) => setAnswers({...answers, [q.id]: next});
    return <div key={outgoing ? "outgoing" : "current"} ref={outgoing ? undefined : panelRef} className={`user-input-panel ${outgoing ? "leaving" : "current"}`}
      aria-hidden={outgoing || undefined} {...(outgoing ? {inert: ""} : {})}>
      {intro && <p className="user-input-intro">{intro}</p>}
      <h3 id={titleId} tabIndex={-1}>{q.question}</h3>
      {q.description && <p className="user-input-intro">{q.description}</p>}
      {Boolean(q.options?.length) && <><small>{q.selectionMode === "multiple" ? "可多选" : "请选择一项"}，也可以填写自己的回答</small>
        <div className="user-input-options" role="group" aria-labelledby={titleId}>{q.options?.map(option => <label key={option.id} className={answer.optionIds?.includes(option.id) ? "selected" : ""}>
          <input type={q.selectionMode === "multiple" ? "checkbox" : "radio"} name={`${prefix}-${outgoing ? "previous" : "current"}-${q.id}`}
            checked={answer.optionIds?.includes(option.id) ?? false} disabled={locked || outgoing}
            onChange={() => update(selectQuestionOption(q, answer, option.id))}/>
          <span><b>{option.label}</b>{option.description && <small>{option.description}</small>}</span>
        </label>)}</div></>}
      <label className="user-input-free-text">你的回答或补充<textarea value={answer.text ?? ""} disabled={locked || outgoing} maxLength={8000}
        onChange={event => update({...answer, text: event.target.value})} placeholder="可以不选建议，直接写下你的想法"/></label>
    </div>;
  }
  return <ResearchCard className="user-input-card" aria-label="需要你补充一点信息" aria-busy={busy} title="需要你补充一点信息"
    tone="pending" status={<span aria-live="polite">{current + 1} / {questions.length}</span>} bodyMode="contents"
    footer={<><div className="user-input-secondary-actions">{skip && <button type="button" disabled={locked} onClick={skip}>跳过并继续</button>}
      {current > 0 && <button type="button" disabled={locked} onClick={() => go(current - 1)}>上一题</button>}</div>
      <button type="button" className="primary" disabled={locked || !questionAnswered(question, answers[question.id])} onClick={confirm}>{busy ? "正在提交…" : "确定"}</button></>}>
    <div className="user-input-body">
      <div className={`user-input-slider ${slide ? `sliding-${slide.direction}` : ""}`} style={height ? {height} : undefined}>
        {slide && questions[slide.from] && renderQuestion(questions[slide.from]!, true)}
        {renderQuestion(question)}
      </div>
      {error && <p className="user-input-error" role="alert">{error}</p>}
    </div>
  </ResearchCard>;
}
