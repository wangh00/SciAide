import { ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ResearchTimelineEntry, ResearchTimelinePage } from "./App";
import { mergeResearchTimeline } from "./researchTimelineState.js";

export function ResearchTaskTimeline({ projectId, taskId, read, render, follow, pending = [], acknowledge }: {
  projectId: string; taskId: string;
  read: (query: {projectId: string; taskId: string; before?: string; after?: string; limit: number}) => Promise<ResearchTimelinePage>;
  render: (entry: ResearchTimelineEntry) => ReactNode;
  follow: (value: boolean) => void;
  pending?: ResearchTimelineEntry[];
  acknowledge?: (messageIds: string[]) => void;
}) {
  const [entries, setEntries] = useState<ResearchTimelineEntry[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const state = useRef({ disposed: false, inFlight: false, entries: [] as ResearchTimelineEntry[] });
  const anchor = useRef<{element: Element; top: number} | null>(null);
  const nextPollAt = useRef(0);
  const wake = useRef<() => void>(() => undefined);

  useEffect(() => {
    if (pending.length && acknowledge) {
      const ids = new Set(entries.flatMap(entry => entry.message ? [entry.message.id] : []));
      const matched = pending.flatMap(entry => entry.message && ids.has(entry.message.id) ? [entry.message.id] : []);
      if (matched.length) acknowledge(matched);
    }
  }, [entries, pending, acknowledge]);

  useLayoutEffect(() => {
    const saved = anchor.current;
    if (!saved) return;
    anchor.current = null;
    const chat = root.current?.closest<HTMLElement>(".chat");
    if (chat && saved.element.isConnected) chat.scrollTop += saved.element.getBoundingClientRect().top - saved.top;
  }, [entries]);

  useEffect(() => {
    const current = { disposed: false, inFlight: false, entries: [] as ResearchTimelineEntry[] };
    state.current = current;
    follow(true);
    let idleRounds = 0, refreshRequested = false;
    const refresh = async () => {
      if (current.inFlight || current.disposed) return;
      current.inFlight = true;
      try {
        const previous = current.entries;
        const last = current.entries.at(-1)?.sequence;
        const page = await read({projectId, taskId, after: last, limit: last ? 100 : 50});
        if (current.disposed) return;
        if (!last) setHasMore(page.hasMore);
        current.entries = mergeResearchTimeline(current.entries, page.entries);
        // Catch up every persisted row before refreshing mutable message/tool state.
        if (last && !page.hasMore) {
          const latest = await read({projectId, taskId, limit: 50});
          if (current.disposed) return;
          // New rows will be picked up by the after cursor, never jump over a gap.
          const known = new Set(current.entries.map(entry => entry.id));
          current.entries = mergeResearchTimeline(current.entries, latest.entries.filter(entry => known.has(entry.id)));
        }
        setEntries(current.entries); setError("");
        idleRounds = previous === current.entries ? idleRounds + 1 : 0;
        nextPollAt.current = Date.now() + (idleRounds >= 3 ? 5000 : 900);
      } catch (e) { if (!current.disposed) setError(String(e)); }
      finally {
        current.inFlight = false;
        if (!current.disposed) {
          setLoading(false);
          if (refreshRequested) { refreshRequested = false; void refresh(); }
        }
      }
    };
    const requestRefresh = () => {
      idleRounds = 0; nextPollAt.current = 0;
      if (current.inFlight) refreshRequested = true;
      else void refresh();
    };
    wake.current = requestRefresh;
    const onSaved = (event: Event) => {
      const scope = (event as CustomEvent<{projectId: string; taskId: string}>).detail;
      if (scope?.projectId === projectId && scope.taskId === taskId) requestRefresh();
    };
    window.addEventListener('research-message-saved', onSaved);
    void refresh();
    const timer = window.setInterval(() => { if (!document.hidden && Date.now() >= nextPollAt.current) void refresh(); }, 900);
    return () => { current.disposed = true; wake.current = () => undefined; window.clearInterval(timer); window.removeEventListener('research-message-saved', onSaved); };
  }, [projectId, taskId, read, follow]);

  async function older() {
    const current = state.current;
    if (current.inFlight || current.disposed) return;
    current.inFlight = true; setLoading(true); follow(false);
    try {
      const page = await read({projectId, taskId, before: current.entries[0]?.sequence, limit: 50});
      if (current.disposed) return;
      const chat = root.current?.closest<HTMLElement>(".chat");
      const element = [...(root.current?.querySelectorAll("[data-timeline-id]") ?? [])].find(item => item.getBoundingClientRect().bottom > (chat?.getBoundingClientRect().top ?? 0));
      if (element) anchor.current = {element, top: element.getBoundingClientRect().top};
      current.entries = mergeResearchTimeline(current.entries, page.entries);
      setEntries(current.entries); setHasMore(page.hasMore); setError("");
    } catch (e) { if (!current.disposed) setError(String(e)); }
    finally { current.inFlight = false; if (!current.disposed) { setLoading(false); wake.current(); } }
  }

  const visibleEntries = [...entries, ...pending.filter(local => !entries.some(entry => entry.id === local.id))];
  return <div ref={root} className="message-stack research-task-timeline" aria-label="科研任务时间线">
    {hasMore && <button type="button" className="timeline-load-older" disabled={loading} onClick={() => void older()}>{loading ? "正在读取记录…" : "加载更早记录"}</button>}
    {error && <p role="alert">时间线读取失败：{error}</p>}
    {loading && !entries.length && <p role="status">正在读取科研任务…</p>}
    {visibleEntries.map(entry => <div data-timeline-id={entry.id} key={entry.id}>{render(entry)}</div>)}
  </div>;
}
